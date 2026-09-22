// Command adb（编译产物建议命名 gadb，避免与 SDK 的 adb.exe 混淆）是安卓设备
// 的正式命令行操控入口，覆盖原临时 adb 脚本的全部能力。
//
// 为什么必须是正式命令而不是 py 脚本：AGENTS.md 的硬约束——常用能力落正式
// Go 包与 cmd 命令。本命令把两条排障结论直接固化进链路，调用方不用再记：
//
//  1. 独立 server 端口（-P）：本机多个 adb 构建共用默认 server 会互杀重启，
//     表现为「抓帧正常但注入丢失、时通时断」。多个对话/工具并行时各自固定
//     一个端口（如 -P 5038）即可互不干扰。
//  2. 注入前自包含 connect：server 可能刚被外部清理，devices 列表看似正常
//     但实际未连接，此时 tap/swipe 会被静默丢弃。本命令每次运行都先
//     start-server + connect（host:port 形式的 serial），再做动作。
//
// 用法（坐标一律像素，与实测笔记同一口径；-P 与 -serial 是全局参数）：
//
//	gadb -P 5038 -serial <host:port> connect                    # 自包含连接自检
//	gadb -P 5038 -serial <host:port> tap 495 645                # 像素坐标点击
//	gadb -P 5038 -serial ... swipe 100 200 300 400 300         # 滑动（时长 ms 可省）
//	gadb -P 5038 -serial ... key back                          # back/home/enter...
//	gadb -P 5038 -serial ... shell wm size                     # 设备端 shell
//	gadb -P 5038 -serial ... seq 450,1375:1500;560,880:600     # 像素连击序列
//	gadb -P 5038 -serial ... shot -o _now.png                  # 截图存 PNG
//	gadb -P 5038 -serial ... shot -tap 450,1375 -o _after.png  # 点击后等一拍再截
//	gadb -P 5038 -serial ... front                             # 打印前台包名
//	gadb -P 5038 -serial ... size                              # 屏幕像素尺寸
//	gadb -P 5038 devices                                       # 该 server 上的设备
package main

import (
	"flag"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/ayflying/game-sensei/internal/android"
)

func main() {
	var (
		adbPath = flag.String("adb", "", "adb 可执行文件路径（空则自动查找）")
		serial  = flag.String("serial", "", "设备序列号；host:port 形式会自动 connect，空则自动发现")
		port    = flag.Int("P", 0, "adb server 端口；0=默认 server。多工具并行时各自固定独立端口")
		wait    = flag.Duration("wait", time.Second, "动作后等待多久再截图（shot/seq 的默认步间隔）")
	)
	flag.Parse()

	args := flag.Args()
	if len(args) == 0 {
		usage()
		os.Exit(2)
	}

	// devices/connect 不需要预先打开设备句柄。
	switch args[0] {
	case "devices":
		list, err := android.DevicesOnPort(*adbPath, *port)
		if err != nil {
			fail(err)
		}
		for _, d := range list {
			fmt.Printf("%-28s %-8s %s\n", d.Serial, d.State, d.Model)
		}
		return
	case "connect":
		if *serial == "" {
			fail(fmt.Errorf("connect 需要 -serial host:port"))
		}
		if err := android.EnsureServer(*adbPath, *port); err != nil {
			fail(err)
		}
		out, err := android.Connect(*adbPath, *serial, *port)
		if err != nil {
			fail(err)
		}
		fmt.Println(out)
		if strings.Contains(out, "cannot") || strings.Contains(out, "failed") {
			os.Exit(1)
		}
		return
	}

	dev, err := android.OpenServer(*adbPath, *serial, *port)
	if err != nil {
		fail(err)
	}

	switch args[0] {
	case "tap":
		if len(args) != 3 {
			fail(fmt.Errorf("tap 需要 x y 两个像素坐标"))
		}
		x, y := atoi(args[1]), atoi(args[2])
		if err := dev.Tap(x, y); err != nil {
			fail(err)
		}
		fmt.Printf("tap (%d,%d)\n", x, y)
	case "swipe":
		if len(args) != 5 && len(args) != 6 {
			fail(fmt.Errorf("swipe 需要 x1 y1 x2 y2 [ms]"))
		}
		ms := 200
		if len(args) == 6 {
			ms = atoi(args[5])
		}
		if err := dev.Swipe(atoi(args[1]), atoi(args[2]), atoi(args[3]), atoi(args[4]),
			time.Duration(ms)*time.Millisecond); err != nil {
			fail(err)
		}
		fmt.Printf("swipe (%s,%s)→(%s,%s) %dms\n", args[1], args[2], args[3], args[4], ms)
	case "key":
		if len(args) != 2 {
			fail(fmt.Errorf("key 需要一个按键名，如 back/home/enter"))
		}
		if err := dev.Key(args[1]); err != nil {
			fail(err)
		}
		fmt.Printf("key %s\n", args[1])
	case "shell":
		if len(args) < 2 {
			fail(fmt.Errorf("shell 需要至少一条设备端命令"))
		}
		out, err := dev.Shell(strings.Join(args[1:], " "))
		if err != nil {
			fail(err)
		}
		fmt.Print(out)
	case "seq":
		if len(args) != 2 {
			fail(fmt.Errorf("seq 需要一个 \"x,y:ms;x,y:ms\" 序列（像素坐标，ms 可省略）"))
		}
		steps, err := parseSeq(args[1])
		if err != nil {
			fail(err)
		}
		for i, s := range steps {
			if err := dev.Tap(s.x, s.y); err != nil {
				fail(fmt.Errorf("seq 第 %d 步失败: %w", i+1, err))
			}
			fmt.Printf("seq %d/%d tap (%d,%d) 等 %dms\n", i+1, len(steps), s.x, s.y, s.waitMs)
			time.Sleep(time.Duration(s.waitMs) * time.Millisecond)
		}
	case "shot":
		runShot(dev, args[1:], *wait)
	case "front":
		pkg, err := dev.Foreground()
		if err != nil {
			fail(err)
		}
		fmt.Println(pkg)
	case "size":
		sz, err := dev.ScreenSize()
		if err != nil {
			fail(err)
		}
		fmt.Printf("%dx%d\n", sz.X, sz.Y)
	default:
		fmt.Fprintf(os.Stderr, "未知子命令 %q\n", args[0])
		usage()
		os.Exit(2)
	}
}

// runShot 实现 shot 子命令：截图落盘，可选先点一下再等一拍。
//
// 和 cmd/shot 的区别：这里的 -tap 是像素坐标（forge-shop 实测笔记的口径），
// 且不强制生成压缩图——本命令的产出主要是给 vlm.exe 判读与 A/B 对比用的原始帧。
func runShot(dev *android.Device, args []string, defWait time.Duration) {
	fs := flag.NewFlagSet("shot", flag.ExitOnError)
	out := fs.String("o", ".workbuddy/tmp/screenshots/gadb.png", "PNG 输出路径")
	tapArg := fs.String("tap", "", "截图前先点一下，像素坐标 x,y")
	wait := fs.Duration("wait", defWait, "点击后等待多久再截图")
	if err := fs.Parse(args); err != nil {
		fail(err)
	}
	if *tapArg != "" {
		parts := strings.Split(*tapArg, ",")
		if len(parts) != 2 {
			fail(fmt.Errorf("-tap %q 不是 x,y 形式", *tapArg))
		}
		x, y := atoi(parts[0]), atoi(parts[1])
		if err := dev.Tap(x, y); err != nil {
			fail(err)
		}
		fmt.Printf("tap (%d,%d) 等 %s\n", x, y, *wait)
		time.Sleep(*wait)
	}
	img, err := dev.Screenshot()
	if err != nil {
		fail(err)
	}
	if err := android.SavePNG(img, *out); err != nil {
		fail(err)
	}
	b := img.Bounds()
	fmt.Printf("PNG %s (%dx%d)\n", *out, b.Dx(), b.Dy())
}

// parseSeq 解析 "x,y:ms;x,y:ms" 形式的像素连击序列（ms 可省略，默认取全局步间隔）。
func parseSeq(s string) ([]seqStep, error) {
	var out []seqStep
	for _, item := range strings.Split(s, ";") {
		item = strings.TrimSpace(item)
		if item == "" {
			continue
		}
		coord, waitStr, hasWait := strings.Cut(item, ":")
		parts := strings.Split(coord, ",")
		if len(parts) != 2 {
			return nil, fmt.Errorf("seq 里的 %q 不是 x,y 形式", coord)
		}
		wait := 800
		if hasWait {
			v, err := strconv.Atoi(strings.TrimSuffix(strings.TrimSpace(waitStr), "ms"))
			if err != nil {
				return nil, fmt.Errorf("seq 里的等待 %q 不是毫秒整数", waitStr)
			}
			wait = v
		}
		out = append(out, seqStep{x: atoi(parts[0]), y: atoi(parts[1]), waitMs: wait})
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("seq 为空")
	}
	return out, nil
}

type seqStep struct {
	x, y   int
	waitMs int
}

func atoi(s string) int {
	v, err := strconv.Atoi(strings.TrimSpace(s))
	if err != nil {
		fail(fmt.Errorf("%q 不是整数", s))
	}
	return v
}

func usage() {
	fmt.Fprint(os.Stderr, `用法: gadb [-adb 路径] [-serial host:port] [-P server端口] <子命令> [参数]
子命令:
  connect                    连接 -serial 指定的网络设备（自包含自检）
  devices                    列出该 server 上的设备
  tap x y                    像素坐标点击
  swipe x1 y1 x2 y2 [ms]     滑动（默认 200ms）
  key <back|home|enter...>   发送按键
  shell <命令...>            设备端 shell
  seq "x,y:ms;x,y:ms"        像素连击序列
  shot [-o 路径] [-tap x,y]  截图（可先点击再等一拍）
  front                      打印前台包名
  size                       屏幕像素尺寸
`)
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "gadb:", err)
	os.Exit(1)
}
