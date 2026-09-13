// cmd/hunt 是一个「不依赖大模型」的确定性探索器：按游戏档案推摇杆在野外晃，
// 一判定进入回合战斗态就落图并退出（退出码 0）。
//
// 为什么需要它：真机标定战斗内动作（技能卡坐标、投球抛物线）的前提是**先有一场
// 战斗**。靠 helper 的完整回路去撞运气太贵（每一步都要一次老师推理），靠人肉
// 走到遇敌又太慢。把「找战斗」这件纯机械的事拆成独立小工具后，标定流程变成：
//
//	./hunt.exe -game nrc            # 撞进战斗，落一张 before.png
//	./shot.exe -tap ... -o step.png # 在战斗里逐步标定
//
// 探索策略（刻意做得又笨又稳，方便复盘）：
//  1. 一直朝当前方向推摇杆（dur 默认 1200ms，足够产生肉眼可见位移）；
//  2. 这一步画面变化量 Δ 够大就继续同向推（沿开阔地带直走），最多连推 holdMax 步；
//  3. Δ 太小（撞墙/被地形卡住）就换下一个方向——方向表从「横向」开头，
//     因为卡死几乎总是因为一路朝同一侧顶，先横着挪出去比继续往前顶有效；
//  4. 连续 stuckWarn 步都推不动，就跑档案里的脱困脚本（大地图传送），冷却 cooldown 步；
//  5. 任何一步判定进入战斗态 → 落图 + 打印 BATTLE，退出。
package main

import (
	"flag"
	"fmt"
	"image"
	"os"
	"path/filepath"
	"time"

	"github.com/ayflying/game-sensei/internal/agent"
	"github.com/ayflying/game-sensei/internal/android"
	"github.com/ayflying/game-sensei/internal/game"
	"github.com/ayflying/game-sensei/internal/vision"
)

// probeDirs 是探索时依次尝试的方向。刻意从「横向」开头：卡死几乎总是因为
// 一路朝同一侧顶（山壁/水岸），先沿障碍边缘横着挪出去最容易脱困。
var probeDirs = []agent.Dir{
	agent.DirRight, agent.DirDownRight, agent.DirDown, agent.DirDownLeft,
	agent.DirLeft, agent.DirUpLeft, agent.DirUp, agent.DirUpRight,
}

// blackFrameMean 是「这一帧基本全黑」的灰度均值上限。
//
// 小米平板 screen_off_timeout=60s，停顿久了会息屏：熄屏后 screencap 只能拿到
// 全黑图，input 也会被系统静默吞掉。黑帧不叫醒屏幕，整个探索会空转到天亮。
const blackFrameMean = 3.0

func main() {
	var (
		gameName = flag.String("game", "nrc", "游戏档案名或路径（同 helper 的 -game）")
		steps    = flag.Int("steps", 300, "最多走多少步")
		durMs    = flag.Int("dur", 1200, "每步移动时长（毫秒）")
		downW    = flag.Int("down", 320, "算 Δ 用的灰度降采样宽度")
		holdMax  = flag.Int("hold", 6, "同方向最多连推几步（之后换向，避免一路走到底不回头）")
		eps      = flag.Float64("eps", 4.0, "判定「这一步几乎没挪窝」的画面变化量阈值（与 helper 的 moveStallDiffEps 一致）")
		stuckMin = flag.Int("stuck", 6, "连续多少步推不动才跑脱困脚本")
		useEsc   = flag.Bool("escape", true, "卡死时执行档案里的脱困脚本")
		escCD    = flag.Int("escape-cooldown", 15, "两次脱困之间至少隔多少步")
		settleMs = flag.Int("settle", 400, "动作后额外静置（毫秒），等移动动画/遇敌转场起来")
		outDir   = flag.String("out", ".workbuddy/codex/hunt", "命中战斗时落图目录")
		adbPath  = flag.String("adb", "", "adb 可执行文件路径（留空自动查找）")
		serial   = flag.String("serial", "", "设备序列号（留空取唯一在线设备）")
		verbose  = flag.Bool("v", true, "逐步骤打印")
	)
	flag.Parse()

	dev, err := android.Open(*adbPath, *serial)
	if err != nil {
		fail(err)
	}
	prof, err := game.Load(*gameName)
	if err != nil {
		fail(err)
	}
	fmt.Printf("设备 %s ｜ 档案 %s（%s）\n", dev.Describe(), prof.Name, *gameName)
	if !prof.CanDetectBattle() {
		fail(fmt.Errorf("档案 %s 没配 battle_detect，无法判定战斗态；本工具依赖它", *gameName))
	}

	// 前台不是目标游戏就先拉起来——否则会在系统桌面上空推摇杆。
	if prof.Package != "" {
		if fg, err := dev.Foreground(); err != nil || fg != prof.Package {
			fmt.Printf("前台是 %q，拉起 %s …\n", fg, prof.Package)
			if err := dev.Launch(prof.Package); err != nil {
				fail(err)
			}
			if err := dev.WaitForeground(prof.Package, 60*time.Second, time.Second); err != nil {
				fmt.Printf("⚠️  %v（继续尝试）\n", err)
			}
			time.Sleep(2 * time.Second)
		}
	}

	// 首帧：既做一次战斗判定，也给 Δ 一个基准。
	prev, err := grabGray(dev, *downW)
	if err != nil {
		fail(err)
	}
	if hitBattle(dev, prof, *outDir, 0) {
		return
	}

	dirIdx := 0
	hold := 0
	stuck := 0
	lastEscape := -1 << 30
	escMin, escCDN := prof.EscapeMinRepeat(), *escCD
	// -stuck 只作「地板」：档案里的 MinRepeat 才是游戏特有的标定值，
	// 命令行的默认值不该把它顶掉。
	if *stuckMin > escMin {
		escMin = *stuckMin
	}
	if escCDN <= 0 {
		escCDN = 15
	}

	for step := 1; step <= *steps; step++ {
		dir := probeDirs[dirIdx]
		act, err := prof.Resolve(agent.Action{
			Kind: agent.ActionMove, Dir: dir, Dur: time.Duration(*durMs) * time.Millisecond,
		})
		if err != nil {
			fmt.Printf("步 %d 解析移动失败: %v\n", step, err)
			return
		}
		if err := dev.Apply(act); err != nil {
			fmt.Printf("步 %d 移动失败: %v\n", step, err)
			return
		}
		time.Sleep(time.Duration(*durMs+*settleMs) * time.Millisecond)

		img, err := dev.Screenshot()
		if err != nil {
			fmt.Printf("步 %d 截图失败: %v\n", step, err)
			time.Sleep(time.Second)
			continue
		}
		cur := android.ToGrayDownsampled(img, *downW)
		m := meanGray(cur)

		// 黑帧 = 息屏。叫醒后重新取基准，本步不计入卡死判定。
		if m < blackFrameMean {
			fmt.Printf("步 %d 画面全黑（m=%.1f）→ 唤醒屏幕重试\n", step, m)
			_ = dev.Key("wakeup")
			time.Sleep(1200 * time.Millisecond)
			if g, err := grabGray(dev, *downW); err == nil {
				prev = g
			}
			continue
		}

		if prof.IsBattle(img) {
			saveHit(dev, *outDir, step)
			fmt.Printf("步 %d BATTLE（方向 %s，Δ=%.1f）→ 已落图，退出\n", step, dir, frameDiff(prev, cur))
			return
		}

		d := frameDiff(prev, cur)
		prev = cur
		progress := d >= *eps
		if progress {
			hold++
			stuck = 0
		} else {
			hold = 0
			stuck++
		}

		if *verbose {
			state := "○"
			if progress {
				state = "●"
			}
			fmt.Printf("步 %d %s %-10s Δ=%5.1f  连推=%d 卡=%d\n", step, state, dir, d, hold, stuck)
		} else if step%10 == 0 {
			fmt.Printf("步 %d/%d 方向=%s Δ=%.1f 卡=%d\n", step, *steps, dir, d, stuck)
		}

		// 换向：走不动，或同方向已连推够久（避免一路走到底不回头）。
		if !progress || hold >= *holdMax {
			dirIdx = (dirIdx + 1) % len(probeDirs)
			hold = 0
		}

		// 脱困：连续推不动够多步，且距上次脱困已过冷却。
		if *useEsc && stuck >= escMin && step-lastEscape >= escCDN {
			if runEscape(dev, prof, "步 "+fmt.Sprint(step)) {
				lastEscape = step
				stuck = 0
				if g, err := grabGray(dev, *downW); err == nil {
					prev = g
				}
			} else {
				fmt.Println("（档案未配脱困脚本，跳过）")
				*useEsc = false
			}
		}
	}
	fmt.Printf("%d 步都没遇敌（方向轮了 %d 圈）\n", *steps, (*steps)/(len(probeDirs) * (*holdMax)))
}

// grabGray 抓一帧灰度图（Δ 判定用）。
func grabGray(dev *android.Device, downW int) (*image.Gray, error) {
	img, err := dev.Screenshot()
	if err != nil {
		return nil, err
	}
	return android.ToGrayDownsampled(img, downW), nil
}

// hitBattle 判定当前帧是否战斗态；是则落图并返回 true。
func hitBattle(dev *android.Device, prof *game.Profile, outDir string, step int) bool {
	img := dev.LastColor()
	if img == nil || !prof.IsBattle(img) {
		return false
	}
	saveHit(dev, outDir, step)
	fmt.Printf("BATTLE（首帧，步 %d）→ 已落图，退出\n", step)
	return true
}

func saveHit(dev *android.Device, outDir string, step int) {
	path := filepath.Join(outDir, "before.png")
	if err := dev.SaveLastPNG(path); err != nil {
		fmt.Printf("⚠️  落图失败: %v\n", err)
		return
	}
	fmt.Printf("已落图 %s\n", path)
}

// runEscape 顺序执行档案里的脱困脚本（点击/拖拽 + 等待），返回是否真的跑了。
func runEscape(dev *android.Device, prof *game.Profile, tag string) bool {
	if !prof.CanEscape() {
		return false
	}
	steps := prof.Escape.Steps
	fmt.Printf("[%s] 执行脱困脚本（%d 步）\n", tag, len(steps))
	for i, st := range steps {
		act := agent.Action{Kind: agent.ActionTap, Nx: st.Pos[0], Ny: st.Pos[1]}
		desc := fmt.Sprintf("点 %.3f,%.3f", st.Pos[0], st.Pos[1])
		if st.To[0] != 0 || st.To[1] != 0 {
			dragMs := st.DragMs
			if dragMs <= 0 {
				dragMs = 600
			}
			act = agent.Action{
				Kind: agent.ActionSwipe,
				Nx:   st.Pos[0], Ny: st.Pos[1],
				Nx2: st.To[0], Ny2: st.To[1],
				Dur: time.Duration(dragMs) * time.Millisecond,
			}
			desc = fmt.Sprintf("拖 %.3f,%.3f→%.3f,%.3f/%dms", st.Pos[0], st.Pos[1], st.To[0], st.To[1], dragMs)
		}
		if err := dev.Apply(act); err != nil {
			fmt.Printf("[%s] 脱困第 %d 步失败: %v\n", tag, i+1, err)
			return true
		}
		fmt.Printf("[%s] 脱困 %d/%d %s\n", tag, i+1, len(steps), desc)
		wait := st.WaitMs
		if wait <= 0 {
			wait = 1500
		}
		time.Sleep(time.Duration(wait) * time.Millisecond)
	}
	return true
}

// frameDiff / meanGray 已收敛到 internal/vision（hunt/shot/helper 共用同一份判据）。
func frameDiff(a, b *image.Gray) float64 { return vision.FrameDiff(a, b) }

func meanGray(g *image.Gray) float64 { return vision.MeanGrayFrame(g) }

func fail(err error) {
	fmt.Fprintln(os.Stderr, "hunt:", err)
	os.Exit(1)
}
