// Command shot 是真机（安卓后端）标定/复盘用的截图小工具。
//
// 为什么需要它：AGENTS.md §0.4 立了一条铁律——**读图只读压缩版 `.s.jpg`**。
// 真机截图（`screencap`）出来的就是全尺寸 PNG（2136x3200、约 1.2MB），
// 直接交给多模态模型会把对话请求体撑到 8MB+，服务端直接 400 打断整个回合。
// 所以每次要看画面，都必须先压成 1024 宽的小图（约 90KB，UI 文字仍清晰）。
//
// 本机没有 PIL（python 侧压不动），所以这里用 Go 复用 `internal/vision` 的
// `Downscale` + `EncodeJPEG`，同时复用 `internal/android` 的截图与点击，
// 一个命令就能「点一下 → 截图 → 压出小图」——标定新坐标时最省事。
//
// 用法：
//
//	shot -o _cal.png                                  # 截图并压出 _cal.s.jpg
//	shot -tap 0.786,0.906 -o _aim.png -wait 2s        # 先点「捕捉」再截图（看投球瞄准态）
//	shot -swipe 0.30,0.28,0.62,0.52 -drag 220ms -o _fling.png   # 拖拽（试投球抛物线）
//	shot -front                                       # 只打印前台包名（判断游戏在不在前台）
//	shot -pack .workbuddy/demos                       # 把目录里已有的 PNG 批量压成 .s.jpg
//
// 坐标一律用归一化 0~1（与游戏档案、动作协议同一套口径），不用像素。
package main

import (
	"flag"
	"fmt"
	"image"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/ayflying/game-sensei/internal/agent"
	"github.com/ayflying/game-sensei/internal/android"
	"github.com/ayflying/game-sensei/internal/game"
	"github.com/ayflying/game-sensei/internal/vision"
)

// smallWidth 压缩图宽度。与 AGENTS.md §0.4 的口径一致（720~1024 宽、
// q80~88 下约 90~130KB），再大就逼近 400 的临界值了。
const smallWidth = 1024

func main() {
	var (
		adbPath  = flag.String("adb", "", "adb 可执行文件路径（空则自动查找）")
		serial   = flag.String("serial", "", "设备序列号（空则取唯一在线设备）")
		out      = flag.String("o", "", "原始 PNG 输出路径（同时生成同名 .s.jpg）")
		quality  = flag.Int("q", 88, "JPEG 质量")
		width    = flag.Int("w", smallWidth, "压缩图最大宽度（像素）")
		tapArg   = flag.String("tap", "", "截图前先点一下，归一化坐标 x,y（如 0.786,0.906）")
		swipeArg = flag.String("swipe", "", "截图前先拖拽，归一化 x1,y1,x2,y2")
		pressArg = flag.String("press", "", "截图前先按「命名按钮/宏」（如 cast_hetu、gather_energy、battle_catch）；宏会按档案整条跑完")
		seqArg   = flag.String("seq", "", "截图前执行一串点击，语法 x,y:等待ms;x,y:等待ms（如 \"0.10,0.30:800;0.60,0.35:4000\"）。标定「点A再点B」这类两步手势用")
		dragMs   = flag.Int("drag", 220, "拖拽时长（毫秒），仅 -swipe 有效")
		wait     = flag.Duration("wait", 1500*time.Millisecond, "动作后等待多久再截图")
		delta    = flag.Bool("delta", false, "打印动作前后的画面变化量 Δ（逐像素平均绝对差）；标定时用它判「这一步到底生效没有」")
		ab       = flag.Bool("ab", false, "额外把动作前的帧存成 <out>.before.png（做 A/B 对照）")
		downW    = flag.Int("down", 320, "算 Δ 用的灰度降采样宽度")
		front    = flag.Bool("front", false, "只打印前台包名后退出")
		pack     = flag.String("pack", "", "批量模式：把该目录（含子目录）里的 PNG 压成同名 .s.jpg")
		noJPG    = flag.Bool("nojpg", false, "只存原始 PNG，不生成压缩图")
		check    = flag.Bool("check", false, "只打印当前帧的界面态判定（world/battle）后退出，不落图")
		gameName = flag.String("game", "nrc", "判定界面态/解析命名按钮用哪个游戏档案")
	)
	flag.Parse()

	if *pack != "" {
		if err := packDir(*pack, *width, *quality); err != nil {
			fail(err)
		}
		return
	}

	dev, err := openDevice(*adbPath, *serial)
	if err != nil {
		fail(err)
	}

	if *front {
		pkg, err := dev.Foreground()
		if err != nil {
			fail(err)
		}
		fmt.Println(pkg)
		return
	}

	// -check：只做界面态判定。给「自动找一场战斗」这类循环用——
	// 走一步、判一次，比人肉看截图快得多，也避免把帧堆进对话（§0.4）。
	if *check {
		img, err := dev.Screenshot()
		if err != nil {
			fail(err)
		}
		prof, err := game.Load(*gameName)
		if err != nil {
			fail(err)
		}
		if prof.IsBattle(img) {
			fmt.Println("battle")
		} else {
			fmt.Println("world")
		}
		return
	}

	// 可选：先做动作——标定时最常用的就是「点一下再截图」。
	//
	// -delta / -ab 需要动作**前**的一帧做基准，所以先抓一帧（灰度算 Δ，彩色备用）。
	var beforeGray *image.Gray
	var beforeImg image.Image
	if *delta || *ab {
		beforeImg, err = dev.Screenshot()
		if err != nil {
			fail(err)
		}
		beforeGray = android.ToGrayDownsampled(beforeImg, *downW)
	}

	if *tapArg != "" {
		x, y, err := parsePair(*tapArg)
		if err != nil {
			fail(fmt.Errorf("-tap %q 不是 x,y 形式: %w", *tapArg, err))
		}
		px, py, err := dev.TapNorm(x, y)
		if err != nil {
			fail(err)
		}
		fmt.Printf("tap (%.3f,%.3f) → 像素 (%d,%d)\n", x, y, px, py)
		time.Sleep(*wait)
	}
	if *swipeArg != "" {
		v, err := parseQuad(*swipeArg)
		if err != nil {
			fail(fmt.Errorf("-swipe %q 不是 x1,y1,x2,y2 形式: %w", *swipeArg, err))
		}
		d := time.Duration(*dragMs) * time.Millisecond
		if err := dev.SwipeNorm(v[0], v[1], v[2], v[3], d); err != nil {
			fail(err)
		}
		fmt.Printf("swipe (%.3f,%.3f)→(%.3f,%.3f) %v\n", v[0], v[1], v[2], v[3], d)
		time.Sleep(*wait)
	}
	// -press：按「命名按钮或宏」出招。宏（cast_hetu / gather_energy …）是**多步**序列，
	// 这里按档案里的 steps 依次点/拖 + 等待，语义与 helper 回路里跑宏完全一致——
	// 这样标定时试出来的结论，回到回路里行为不变。
	if *pressArg != "" {
		if err := pressByName(dev, *gameName, *pressArg, *wait); err != nil {
			fail(err)
		}
	}
	// -seq：一串「点一下 → 等一会」的定点连击。
	//
	// 存在的理由：有些手势是**两步**的（洛克王国战斗抓宠：「点左上咕噜球」→「点目标精灵」），
	// 单点工具表达不了。把序列写成命令行参数而不是往档案里塞宏，是因为标定期间这些
	// 坐标大多还是**猜的**——猜错的坐标不该污染档案。
	if *seqArg != "" {
		steps, err := parseSeq(*seqArg)
		if err != nil {
			fail(err)
		}
		for i, s := range steps {
			px, py, err := dev.TapNorm(s.x, s.y)
			if err != nil {
				fail(err)
			}
			fmt.Printf("seq %d/%d tap (%.3f,%.3f) → 像素 (%d,%d) 等 %dms\n",
				i+1, len(steps), s.x, s.y, px, py, s.waitMs)
			time.Sleep(time.Duration(s.waitMs) * time.Millisecond)
		}
	}

	img, err := dev.Screenshot()
	if err != nil {
		fail(err)
	}
	b := img.Bounds()
	fmt.Printf("screenshot %dx%d\n", b.Dx(), b.Dy())

	// Δ：动作前 vs 动作后的逐像素平均绝对差。量纲见 internal/vision.FrameDiff。
	// 经验读数（洛克王国真机）：<3 画面基本没动（动作没生效）；10~60 角色在走；
	// >60 整屏变化（开图/转场/技能特效）。
	if beforeGray != nil {
		after := android.ToGrayDownsampled(img, *downW)
		fmt.Printf("Δ=%.1f\n", vision.FrameDiff(beforeGray, after))
	}
	if *ab && beforeImg != nil {
		beforePath := strings.TrimSuffix(*out, filepath.Ext(*out)) + ".before.png"
		if err := android.SavePNG(beforeImg, beforePath); err != nil {
			fail(err)
		}
		fmt.Printf("PNG（动作前）%s\n", beforePath)
	}

	if *out == "" {
		*out = filepath.Join(".workbuddy", "demos", "_shot.png")
	}
	if err := os.MkdirAll(filepath.Dir(*out), 0o755); err != nil && filepath.Dir(*out) != "." {
		fail(err)
	}
	if err := android.SavePNG(img, *out); err != nil {
		fail(err)
	}
	abs, _ := filepath.Abs(*out)
	st, _ := os.Stat(*out)
	fmt.Printf("PNG  %s (%d KB)\n", abs, sizeKB(st))

	if *noJPG {
		return
	}
	small := vision.Downscale(img, *width)
	raw, err := vision.EncodeJPEG(small, *quality)
	if err != nil {
		fail(err)
	}
	jpgPath := strings.TrimSuffix(*out, filepath.Ext(*out)) + ".s.jpg"
	if err := os.WriteFile(jpgPath, raw, 0o644); err != nil {
		fail(err)
	}
	absJ, _ := filepath.Abs(jpgPath)
	sb := small.Bounds()
	fmt.Printf("JPG  %s (%dx%d, %d KB) ← 读这张\n", absJ, sb.Dx(), sb.Dy(), len(raw)/1024)
}

// pressByName 执行一次「命名按钮 / 宏」。
//
// 宏是多步序列（展开技能盘 → 点技能卡 → 等回合结算），这里按 steps 依次执行，
// 与 helper 回路的宏执行语义保持一致；普通按钮则交给档案把 L1 PRESS 解析成
// L2 的点击/按键再执行。两条路都不经过任何模型，结果可复现。
func pressByName(dev *android.Device, gameName, name string, defWait time.Duration) error {
	prof, err := game.Load(gameName)
	if err != nil {
		return err
	}
	if m, ok := prof.Macro(name); ok {
		fmt.Printf("宏 %s（%d 步）\n", m.Name, len(m.Steps))
		for i, st := range m.Steps {
			wait := time.Duration(st.WaitMs) * time.Millisecond
			if wait <= 0 {
				wait = defWait
			}
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
					Nx2:  st.To[0], Ny2: st.To[1],
					Dur:  time.Duration(dragMs) * time.Millisecond,
				}
				desc = fmt.Sprintf("拖 %.3f,%.3f→%.3f,%.3f/%dms", st.Pos[0], st.Pos[1], st.To[0], st.To[1], dragMs)
			}
			if err := dev.Apply(act); err != nil {
				return fmt.Errorf("宏第 %d 步失败: %w", i+1, err)
			}
			fmt.Printf("  步 %d/%d %s\n", i+1, len(m.Steps), desc)
			time.Sleep(wait)
		}
		return nil
	}

	act, err := prof.Resolve(agent.Action{Kind: agent.ActionPress, Name: name})
	if err != nil {
		return fmt.Errorf("档案里没有按钮/宏 %q: %w", name, err)
	}
	if err := dev.Apply(act); err != nil {
		return err
	}
	fmt.Printf("press %s → %v\n", name, act)
	time.Sleep(defWait)
	return nil
}

// seqStep 是 -seq 里的一步：归一化坐标 + 这一步之后的等待毫秒数。
type seqStep struct {
	x, y   float64
	waitMs int
}

// parseSeq 解析 "x,y:wait;x,y:wait" 形式的连击序列。
// wait 可省略（默认 800ms），也可带 ms 后缀。
func parseSeq(s string) ([]seqStep, error) {
	var out []seqStep
	for _, item := range strings.Split(s, ";") {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		coord, waitStr, hasWait := strings.Cut(item, ":")
		x, y, err := parsePair(coord)
		if err != nil {
			return nil, fmt.Errorf("-seq 里的 %q 不是 x,y 形式: %w", coord, err)
		}
		wait := 800
		if hasWait {
			w := strings.TrimSuffix(strings.TrimSpace(waitStr), "ms")
			v, err := strconv.Atoi(w)
			if err != nil {
				return nil, fmt.Errorf("-seq 里的等待 %q 不是毫秒整数", waitStr)
			}
			wait = v
		}
		out = append(out, seqStep{x: x, y: y, waitMs: wait})
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("-seq 为空")
	}
	return out, nil
}

// openDevice 打开安卓设备：adbPath/serial 留空时自动发现。
func openDevice(adbPath, serial string) (*android.Device, error) {
	if adbPath == "" {
		p, err := android.FindADB()
		if err != nil {
			return nil, fmt.Errorf("找不到 adb（用 -adb 指定）: %w", err)
		}
		adbPath = p
	}
	if serial == "" {
		infos, err := android.Devices(adbPath)
		if err != nil {
			return nil, err
		}
		if len(infos) == 0 {
			return nil, fmt.Errorf("没有在线设备；无线调试请先 adb connect <ip:port>")
		}
		if len(infos) > 1 {
			var names []string
			for _, i := range infos {
				names = append(names, i.Serial)
			}
			return nil, fmt.Errorf("有多个设备，请用 -serial 指定：%s", strings.Join(names, "、"))
		}
		serial = infos[0].Serial
	}
	return android.Open(adbPath, serial)
}

// packDir 批量压缩目录里已有的 PNG。用于给历史采集/标定产物补压缩图。
func packDir(dir string, width, quality int) error {
	n, saved := 0, 0
	err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return err
		}
		if !strings.EqualFold(filepath.Ext(path), ".png") {
			return nil
		}
		f, err := os.Open(path)
		if err != nil {
			return err
		}
		img, _, derr := image.Decode(f)
		f.Close()
		if derr != nil {
			fmt.Printf("  跳过（解码失败）%s: %v\n", path, derr)
			return nil
		}
		raw, err := vision.EncodeJPEG(vision.Downscale(img, width), quality)
		if err != nil {
			return err
		}
		dst := strings.TrimSuffix(path, filepath.Ext(path)) + ".s.jpg"
		if err := os.WriteFile(dst, raw, 0o644); err != nil {
			return err
		}
		n++
		saved += len(raw)
		return nil
	})
	if err != nil {
		return err
	}
	fmt.Printf("pack 完成：%d 张 PNG → .s.jpg，共 %d KB\n", n, saved/1024)
	return nil
}

func parsePair(s string) (float64, float64, error) {
	parts := strings.Split(s, ",")
	if len(parts) != 2 {
		return 0, 0, fmt.Errorf("需要 2 个数")
	}
	a, err := strconv.ParseFloat(strings.TrimSpace(parts[0]), 64)
	if err != nil {
		return 0, 0, err
	}
	b, err := strconv.ParseFloat(strings.TrimSpace(parts[1]), 64)
	if err != nil {
		return 0, 0, err
	}
	return a, b, nil
}

func parseQuad(s string) ([4]float64, error) {
	var out [4]float64
	parts := strings.Split(s, ",")
	if len(parts) != 4 {
		return out, fmt.Errorf("需要 4 个数")
	}
	for i, p := range parts {
		v, err := strconv.ParseFloat(strings.TrimSpace(p), 64)
		if err != nil {
			return out, err
		}
		out[i] = v
	}
	return out, nil
}

func sizeKB(st os.FileInfo) int64 {
	if st == nil {
		return 0
	}
	return st.Size() / 1024
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "shot:", err)
	os.Exit(1)
}
