package main

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"image/png"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/ayflying/game-sensei/internal/agent"
	"github.com/ayflying/game-sensei/internal/config"
	"github.com/ayflying/game-sensei/internal/dataset"
	"github.com/ayflying/game-sensei/internal/game"
	"github.com/ayflying/game-sensei/internal/teacher"
	"github.com/ayflying/game-sensei/internal/vision"
)

// maxFailStreak 是连续执行失败的容忍上限。
//
// 偶发一次失败不该中断整段示范——已经采到的数据是真金白银；
// 但连续失败说明配置或链路坏了（例如档案里没有模型给的那个按钮），
// 继续跑只是在刷屏，早停早排查。
const maxFailStreak = 5

// runDemo 是「老师在线示范」回路（Phase 2 的第一块）。
//
// 与 Phase 1 的 teachLoop 有本质区别：
//   - teachLoop 在**旁路**观察学生，只产出评语，不影响游戏；
//   - runDemo 直接**驱动**游戏，每一步都是老师看着画面给出的动作，
//     同时把这些 (学生观测, 老师动作) 对存成示范数据集，供后续蒸馏学生。
//
// 节拍由老师推理速度决定（实测 qwen3.5:9b 关思考后约 1.2s/步），
// 动作之间再留 DemoWait 让游戏把状态变完——否则下一帧拍的还是旧画面，
// 老师会基于「没变的画面」重复下同一个动作。
func runDemo(cfg config.Config, be backend, dem *teacher.Demonstrator, prof *game.Profile, stop <-chan os.Signal,
	logHook func(string)) error {
	sw, sh, err := be.Size()
	if err != nil {
		return fmt.Errorf("获取屏幕尺寸失败: %w", err)
	}

	dir := cfg.DemoOut
	if dir == "" {
		dir = filepath.Join(".workbuddy", "demos", time.Now().Format("20060102-150405"))
	}
	w, err := dataset.NewWriter(dir, dataset.Meta{
		Goal:      cfg.Goal,
		Model:     cfg.TeacherModel,
		Backend:   be.Describe(),
		ScreenW:   sw,
		ScreenH:   sh,
		DownWidth: cfg.DownsampleWidth,
		DemoWidth: cfg.DemoWidth,
		Live:      be.Live(),
	})
	if err != nil {
		return err
	}
	closed := false
	defer func() {
		if !closed {
			_ = w.Close()
		}
	}()

	steps := cfg.DemoSteps
	if steps > 0 {
		fmt.Printf("示范数据: %s | 计划 %d 步 | 每步等待 %v\n", w.Dir(), steps, cfg.DemoWait)
	} else {
		fmt.Printf("示范数据: %s | 不限步数（Ctrl+C 退出）| 每步等待 %v\n", w.Dir(), cfg.DemoWait)
	}
	if cfg.DemoColor {
		fmt.Println("同时保存老师彩色帧到 color/（用于人工复核）")
	}
	fmt.Println()

	var parsedOK, applied, failStreak int
	var prevAction string
	var prevRepeat int // prevAction 已连续执行的次数（喂给老师做重复禁令）
	for step := 1; ; step++ {
		select {
		case <-stop:
			fmt.Println("\n收到退出信号，正在收尾…")
			return finishDemo(w, &closed, parsedOK, applied)
		default:
		}
		if steps > 0 && step > steps {
			return finishDemo(w, &closed, parsedOK, applied)
		}

		// 1) 感知：灰度图是「学生观测」，彩色图是「老师视野」。
		//    Android 后端下两次调用共用同一张截图缓存，不会多截一次。
		//    GDI 抓屏偶发失败（前台切换/锁屏过渡会让 DC 短暂失效），
		//    重试 2 次、间隔 500ms——一次抖动不该让整段示范报废。
		var gray *image.Gray
		for attempt := 0; ; attempt++ {
			gray, err = be.Grab(cfg.DownsampleWidth)
			if err == nil {
				break
			}
			if attempt >= 2 {
				return fmt.Errorf("第 %d 步抓取灰度帧失败（已重试 %d 次）: %w", step, attempt, err)
			}
			fmt.Printf("[%s] ⚠️  抓屏失败将重试: %v\n", progress(step, steps), err)
			select {
			case <-stop:
				return finishDemo(w, &closed, parsedOK, applied)
			case <-time.After(500 * time.Millisecond):
			}
		}
		color, err := be.GrabColor()
		if err != nil {
			return fmt.Errorf("第 %d 步抓取彩色帧失败: %w", step, err)
		}
		// 2) 降采样 + JPEG 编码后送审。JPEG 而非 PNG：1024 宽下 130KB vs 1.5MB。
		small := vision.Downscale(color, cfg.DemoWidth)
		jpg, err := vision.EncodeJPEG(small, 88)
		if err != nil {
			return fmt.Errorf("第 %d 步编码送审帧失败: %w", step, err)
		}
		grayPNG := encodeGrayPNG(gray)

		// 3) 问老师。超时按「本步失败」处理，不中断整段示范——
		//    偶发一次超时不该让已经采到的数据全废。
		//
		//    复读机兜底（2026-09-12 解忧梦幻岛实测）：PrevAction 喂回 +
		//    提示词禁令对 9B 小模型都不够硬，实测连点同一坐标 20+ 步、
		//    甚至误开 VIP 付费弹窗。连续重复达 repeatForceSwitch 次时，
		//    本步不信老师，直接从档案按钮里轮换一个（logHook 说明原因），
		//    强行制造动作多样性——示范数据的价值在覆盖，不在“听话”。
		const repeatForceSwitch = 3
		if prevRepeat >= repeatForceSwitch && prof != nil && len(prof.Buttons) > 0 {
			btn := prof.Buttons[step%len(prof.Buttons)]
			forcedAct := agent.Action{Kind: agent.ActionPress, Name: btn.Name}
			fmt.Printf("[%s] ⚠️  老师连续 %d 步重复 %s，本步强制换档案按钮 %s\n",
				progress(step, steps), prevRepeat, prevAction, forcedAct.String())
			logHook(fmt.Sprintf("强制换动作 %s（老师复读 %d 步）", forcedAct.String(), prevRepeat))
			// 直接走执行段并落样，然后跳过老师决策进入下一步
			expanded, expErr := be.Resolve(forcedAct)
			if forcedAct.String() == prevAction {
				prevRepeat++
			} else {
				prevRepeat = 1
			}
			prevAction = forcedAct.String()
			if expErr != nil {
				fmt.Printf("[%s] ⚠️  强制动作无法执行: %v\n", progress(step, steps), expErr)
			} else if err := be.Apply(forcedAct); err != nil {
				fmt.Printf("[%s] ⚠️  强制动作执行失败: %v\n", progress(step, steps), err)
			} else {
				applied++
				fmt.Printf("[%s] 强制 %-20s → %-28s | %s\n",
					progress(step, steps), forcedAct.String(), expanded.String(), agent.ExplainAction(forcedAct))
			}
			if err := w.Step(forcedAct, dataset.StepInfo{Raw: "(forced-switch " + forcedAct.String() + ")", PrevAction: prevAction, Parsed: true}, grayPNG, jpg, cfg.DemoColor); err != nil {
				return fmt.Errorf("第 %d 步写示范数据失败: %w", step, err)
			}
			select {
			case <-stop:
				return finishDemo(w, &closed, parsedOK, applied)
			case <-time.After(cfg.DemoWait):
			}
			continue
		}
		dem.PrevAction = prevAction // 把上一步喂回去，否则老师会无限重复同一动作
		dem.RepeatCount = prevRepeat
		act, res, actErr := askTeacher(cfg, dem, jpg)

		info := dataset.StepInfo{Raw: "", LatencyMs: 0, PrevAction: prevAction}
		if res != nil {
			info.Raw = res.Raw
			info.Parsed = res.Used
			if res.Reply != nil {
				info.LatencyMs = res.Reply.Stats.TotalMs
				info.OutTokens = res.Reply.Stats.OutputTokens
			}
		}

		if actErr != nil {
			fmt.Printf("[%s] ⚠️  老师无响应: %v\n", progress(step, steps), actErr)
			logHook(fmt.Sprintf("⚠️ 老师无响应: %v", actErr))
		} else if !res.Used {
			fmt.Printf("[%s] ⚠️  动作解析失败（%.1fs）| 原始输出: %s\n",
				progress(step, steps), info.LatencyMs/1000, oneLine(res.Raw, 80))
			logHook(fmt.Sprintf("⚠️ 动作解析失败: %s", oneLine(res.Raw, 60)))
		} else {
			parsedOK++
			// 4) 展开 + 执行。
			//
			// 先单独展开一次只为日志：把「朝前走」显示成
			// 「摇杆 0.21,0.69 推向 0.21,0.61」，排查动作对不对时这行信息量最大。
			// （Apply 内部还会再展开一次，展开是纯计算，代价可忽略。）
			expanded, expErr := be.Resolve(act)
			// 无论成功与否都告诉老师「你刚给的是这个动作」，
			// 否则失败的动作下一轮还会被重复提出。
			// 连续重复计数：同动作 +1，换动作清零（供下一轮的重复禁令）。
			if act.String() == prevAction {
				prevRepeat++
			} else {
				prevRepeat = 1
			}
			prevAction = act.String()

			if expErr != nil {
				// 展开失败通常是配置问题：档案里没有这个按钮、斜向没配键位。
				failStreak++
				fmt.Printf("[%s] ⚠️  动作无法执行: %v\n", progress(step, steps), expErr)
				logHook(fmt.Sprintf("⚠️ 动作无法执行: %v", expErr))
				if failStreak >= maxFailStreak {
					return fmt.Errorf("连续 %d 步动作无法执行，最后错误: %w", failStreak, expErr)
				}
			} else if err := be.Apply(act); err != nil {
				failStreak++
				fmt.Printf("[%s] ⚠️  执行失败: %v\n", progress(step, steps), err)
				logHook(fmt.Sprintf("⚠️ 执行失败: %v", err))
				if failStreak >= maxFailStreak {
					return fmt.Errorf("连续 %d 步执行失败，最后错误: %w", failStreak, err)
				}
			} else {
				failStreak = 0
				applied++
				mark := "执行"
				if !be.Live() {
					mark = "dry-run"
				}
				repeat := ""
				if step > 1 && act.String() == info.PrevAction {
					repeat = " ⚠️与上一步相同"
				}
				// L1 → L2 都打出来：左边是模型的意图，右边是最终落到设备上的操作
				line := fmt.Sprintf("[%s] %s %-20s → %-28s | %s | %.1fs %dtok%s",
					progress(step, steps), mark, act.String(), expanded.String(),
					agent.ExplainAction(act), info.LatencyMs/1000, info.OutTokens, repeat)
				fmt.Println(line)
				logHook(line)
			}
		}

		// 5) 落样。解析失败也存：这是衡量老师输出稳定性的原始数据。
		if err := w.Step(act, info, grayPNG, jpg, cfg.DemoColor); err != nil {
			return fmt.Errorf("第 %d 步写示范数据失败: %w", step, err)
		}

		// 6) 等游戏响应。
		select {
		case <-stop:
			return finishDemo(w, &closed, parsedOK, applied)
		case <-time.After(cfg.DemoWait):
		}
	}
}

// askTeacher 包一层超时；保持 runDemo 主循环干净。
func askTeacher(cfg config.Config, dem *teacher.Demonstrator, jpg []byte) (agent.Action, *teacher.DemoResult, error) {
	ctx, cancel := context.WithTimeout(context.Background(), cfg.EvalTimeout)
	defer cancel()
	res, err := dem.Act(ctx, jpg)
	if err != nil {
		return agent.Action{Kind: agent.ActionNone}, nil, err
	}
	return res.Action, res, nil
}

func finishDemo(w *dataset.Writer, closed *bool, parsedOK, applied int) error {
	if err := w.Close(); err != nil {
		return err
	}
	*closed = true
	fmt.Printf("\n示范结束：解析成功 %d 步，实际执行 %d 步\n", parsedOK, applied)
	fmt.Printf("数据已保存到: %s\n", w.Dir())
	fmt.Printf("  meta.json        会话元信息与统计\n")
	fmt.Printf("  trajectory.jsonl 逐样本动作（trainer/ 的输入）\n")
	fmt.Printf("  frames/          学生观测灰度帧\n")
	return nil
}

// encodeGrayPNG 把学生观测编码为 PNG。灰度 160 宽时只有几十 KB，
// 用 PNG 是为了无损——训练数据不该引入 JPEG 块效应。
func encodeGrayPNG(g *image.Gray) []byte {
	if g == nil {
		return nil
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, g); err != nil {
		return nil
	}
	return buf.Bytes()
}

// progress 生成进度标签：限定步数时显示 "3/20"，不限时只显示 "3"。
func progress(step, total int) string {
	if total > 0 {
		return fmt.Sprintf("%2d/%d", step, total)
	}
	return fmt.Sprintf("%2d", step)
}

func oneLine(s string, max int) string {
	s = strings.Join(strings.Fields(s), " ")
	if len(s) > max {
		return s[:max] + "…"
	}
	return s
}
