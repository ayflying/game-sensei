// Command uinject（编译产物建议命名 guinject）是 uinput 虚拟触摸屏注入的正式入口。
//
// 为什么需要它（2026-10-08）：Redmi K50（Android 14 / HyperOS V816）对 adb shell
// 关掉了 INJECT_EVENTS，`adb shell input tap|swipe|keyevent` 一律抛 SecurityException，
// `sendevent` 也被 SELinux 拒绝，internal/android 的整套输入在这台机器上失效。
// 但 /dev/uinput 仍对 shell 可写，于是改由设备侧守护进程自建虚拟触摸屏注入——
// 只是换了合法的输入设备，不读内存、不接封包、不绕反作弊。
//
// 用法（-adb / -serial / -P 与其他安卓命令同义；坐标为「当前旋转下的屏幕像素」）：
//
//	guinject -serial HA9TFI49ZDUGQKF6 -install          # 只编译并推送设备侧程序
//	guinject -serial HA9TFI49ZDUGQKF6 -probe            # 建会话并打印设备侧 READY 行
//	guinject -serial HA9TFI49ZDUGQKF6 -tap "540 1200"    # 点击
//	guinject -serial HA9TFI49ZDUGQKF6 -swipe "200 2000 200 800 400"
//	guinject -serial HA9TFI49ZDUGQKF6 -key wakeup        # 虚拟键盘发唤醒键（-kb）
//	guinject -serial HA9TFI49ZDUGQKF6 -script aim.txt    # 序列脚本
//
// 脚本每行一条命令：设备命令（tap/swipe/down/move/up/key）之外，还有宿主侧
// `sleep MS` 与 `shot 本地路径`（用于「按住不放→抓图取证→抬起」这类序列），
// `#` 开头为注释。整个脚本在同一个会话里执行，触摸状态跨命令保持。
//
// 坐标方向若整体镜像，改传 -rot 270（90/270 在屏幕尺寸上无法区分，必须标定）。
package main

import (
	"bufio"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/ayflying/game-sensei/internal/uinject"
)

func main() {
	var (
		adbPath = flag.String("adb", "", "adb 可执行文件路径（空则自动查找）")
		serial  = flag.String("serial", "", "设备序列号；空则取唯一在线设备")
		port    = flag.Int("P", 0, "adb server 端口；0=默认 server")
		repo    = flag.String("repo", "", "仓库根目录（含 go.mod）；空=当前目录")

		rebuild = flag.Bool("rebuild", false, "强制重新编译并推送设备侧程序")
		install = flag.Bool("install", false, "只编译并推送设备侧程序，不建会话")
		probe   = flag.Bool("probe", false, "建会话并打印设备侧 READY 行后就退出")
		rot     = flag.Int("rot", -1, "显示旋转角度 0/90/180/270；-1=按屏幕尺寸自动推断")
		rx      = flag.Int("rx", 0, "虚拟设备原始 X 值域；0=按物理竖屏尺寸推断")
		ry      = flag.Int("ry", 0, "虚拟设备原始 Y 值域；0=按物理竖屏尺寸推断")
		kb      = flag.Bool("kb", false, "同时创建虚拟键盘（-key 需要）")
		timeout = flag.Duration("timeout", 0, "等待设备侧 READY 的超时；0=8s")

		tap   = flag.String("tap", "", "点击：\"X Y [MS]\"")
		swipe = flag.String("swipe", "", "滑动：\"X1 Y1 X2 Y2 MS [STEPS]\"")
		down  = flag.String("down", "", "按住：\"X Y\"")
		move  = flag.String("move", "", "拖动：\"X Y\"")
		up    = flag.Bool("up", false, "抬起")
		key   = flag.String("key", "", "按键：Linux keycode 或别名（wakeup/power/back/home…）")
		file  = flag.String("script", "", "序列脚本路径")
	)
	flag.Parse()

	opt := uinject.Options{
		ADBPath: *adbPath, Serial: *serial, Port: *port, RepoDir: *repo,
		Rebuild: *rebuild, Rot: *rot, RX: *rx, RY: *ry, Keyboard: *kb, Timeout: *timeout,
	}

	if *install {
		local, built, err := uinject.Install(opt)
		if err != nil {
			fail(err)
		}
		if built {
			fmt.Println("已重新编译设备侧程序:", local)
		} else {
			fmt.Println("设备侧程序已是最新（用 -rebuild 可强制重编）:", local)
		}
		fmt.Println("已推送到设备:", uinject.RemoteBin)
		return
	}

	// 收集本次要跑的命令：命令行动作与脚本互斥，避免「跑了一半脚本又插一条」。
	cmds := collectCommands(*tap, *swipe, *down, *move, *up, *key)
	if *file != "" && len(cmds) > 0 {
		fail(fmt.Errorf("-script 与 -tap/-swipe/-down/-move/-up/-key 不能同时使用"))
	}

	s, err := uinject.Start(opt)
	if err != nil {
		fail(err)
	}
	defer s.Close()

	fmt.Println("会话就绪:", s.Info())
	if *rot < 0 {
		fmt.Println("提示：旋转角度由屏幕尺寸推断；若点按整体镜像，请显式传 -rot 270")
	}
	if *probe && *file == "" && len(cmds) == 0 {
		return
	}

	if *file != "" {
		cmds, err = readScript(*file)
		if err != nil {
			fail(err)
		}
	}
	if len(cmds) == 0 {
		fmt.Println("没有要执行的命令；用 -tap/-swipe/-script 指定动作")
		return
	}

	for _, c := range cmds {
		switch {
		case strings.HasPrefix(c, "sleep "):
			ms, err := uinject.ParseInts(strings.TrimPrefix(c, "sleep "))
			if err != nil || len(ms) != 1 {
				fail(fmt.Errorf("sleep 参数非法: %q", c))
			}
			time.Sleep(time.Duration(ms[0]) * time.Millisecond)
			continue
		case strings.HasPrefix(c, "shot "):
			path := strings.TrimSpace(strings.TrimPrefix(c, "shot "))
			if err := s.Screencap(path); err != nil {
				fail(err)
			}
			fmt.Println("已截图:", path)
			continue
		}
		reply, err := s.Command(c)
		if err != nil {
			fail(fmt.Errorf("%s → %v", c, err))
		}
		fmt.Println(reply)
	}
}

// collectCommands 把 CLI 上的动作参数整理成设备命令列表。
func collectCommands(tap, swipe, down, move string, up bool, key string) []string {
	var cmds []string
	if tap != "" {
		cmds = append(cmds, "tap "+strings.Join(fields(tap), " "))
	}
	if swipe != "" {
		cmds = append(cmds, "swipe "+strings.Join(fields(swipe), " "))
	}
	if down != "" {
		cmds = append(cmds, "down "+strings.Join(fields(down), " "))
	}
	if move != "" {
		cmds = append(cmds, "move "+strings.Join(fields(move), " "))
	}
	if up {
		cmds = append(cmds, "up")
	}
	if key != "" {
		cmds = append(cmds, "key "+strings.TrimSpace(key))
	}
	return cmds
}

func fields(s string) []string { return strings.Fields(strings.TrimSpace(s)) }

// readScript 读序列脚本：忽略空行与 # 注释，保留原命令行。
func readScript(path string) ([]string, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("打开脚本失败: %w", err)
	}
	defer f.Close()

	var out []string
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		line := strings.TrimSpace(sc.Text())
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		out = append(out, line)
	}
	if err := sc.Err(); err != nil {
		return nil, fmt.Errorf("读取脚本失败: %w", err)
	}
	return out, nil
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "错误:", err)
	os.Exit(1)
}
