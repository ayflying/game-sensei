//go:build windows

// win —— 窗口探照灯：列出顶层窗口 / 把某个窗口还原并切到前台。
//
// Agent 要操作游戏，前提是这个游戏窗口**在前台且不是最小化**：
//   - 最小化的窗口 GDI 抓不到内容（抓出来是桌面或黑块）；
//   - 后台窗口收不到键盘输入，SendInput 的按键会落到别的程序上。
//
// 所以实时控制之前先跑一次这个命令，看清楚 Agent 眼中的窗口是什么状态。
//
// 用法：
//
//	go run ./cmd/win list                 # 列出所有带标题的窗口
//	go run ./cmd/win list 洛克            # 只看标题含「洛克」的
//	go run ./cmd/win focus 洛克           # 还原 + 置顶第一个匹配的窗口
//	go run ./cmd/win shot [out.png]       # 用 Agent 的同一条抓屏链路存一张图
package main

import (
	"fmt"
	"image/png"
	"os"
	"strings"

	"github.com/ayflying/game-sensei/internal/capture"
	"github.com/ayflying/game-sensei/internal/gamewin"
)

func main() {
	args := os.Args[1:]
	if len(args) == 0 {
		usage()
		os.Exit(2)
	}

	switch strings.ToLower(args[0]) {
	case "list", "ls":
		keyword := ""
		if len(args) > 1 {
			keyword = strings.Join(args[1:], " ")
		}
		wins := gamewin.List()
		if keyword != "" {
			wins = gamewin.FindByTitle(keyword)
		}
		if len(wins) == 0 {
			fmt.Printf("没有标题含 %q 的窗口（不加关键词跑 list 可以看全部）\n", keyword)
			return
		}
		fmt.Printf("共 %d 个窗口：\n", len(wins))
		for _, w := range wins {
			fmt.Println("  " + w.String())
		}

	case "focus":
		if len(args) < 2 {
			fmt.Println("focus 需要关键词：go run ./cmd/win focus 洛克")
			os.Exit(2)
		}
		keyword := strings.Join(args[1:], " ")
		for _, w := range gamewin.FindByTitle(keyword) {
			fmt.Println("匹配: " + w.String())
		}
		n, err := gamewin.FocusByTitle(keyword)
		if err != nil {
			fmt.Printf("⚠️  %v\n", err)
			os.Exit(1)
		}
		if n == 0 {
			fmt.Printf("找不到标题含 %q 的窗口\n", keyword)
			os.Exit(1)
		}
		fmt.Printf("✅ 已还原并置顶 %d 个窗口\n", n)

	case "shot":
		out := "shot.png"
		if len(args) > 1 {
			out = args[1]
		}
		gamewin.EnsureDPIAware() // 让抓屏尺寸与真实像素一致
		img, err := capture.GrabColor()
		if err != nil {
			fmt.Printf("⚠️  抓屏失败: %v\n", err)
			os.Exit(1)
		}
		f, err := os.Create(out)
		if err != nil {
			fmt.Printf("⚠️  无法写 %s: %v\n", out, err)
			os.Exit(1)
		}
		defer f.Close()
		if err := png.Encode(f, img); err != nil {
			fmt.Printf("⚠️  编码失败: %v\n", err)
			os.Exit(1)
		}
		b := img.Bounds()
		fmt.Printf("✅ Agent 眼中的画面已存 %s（%dx%d）\n", out, b.Dx(), b.Dy())

	default:
		usage()
		os.Exit(2)
	}
}

func usage() {
	fmt.Println(`用法:
  win list [关键词]    列出顶层窗口（可按标题关键词过滤）
  win focus <关键词>   把匹配的窗口还原并切到前台`)
}
