// go run ./cmd/winshot 临时工具：把标题含关键词的窗口置顶后抓全屏（调试用）
package main

import (
	"fmt"
	"image/png"
	"os"

	"github.com/ayflying/game-sensei/internal/capture"
	"github.com/ayflying/game-sensei/internal/gamewin"
)

func main() {
	if len(os.Args) < 3 {
		fmt.Println("用法: winshot <窗口标题关键词> <输出png路径>")
		os.Exit(1)
	}
	kw, out := os.Args[1], os.Args[2]
	gamewin.EnsureDPIAware()
	wins := gamewin.FindByTitle(kw)
	if len(wins) == 0 {
		fmt.Println("未找到窗口:", kw)
		os.Exit(1)
	}
	for _, w := range wins {
		fmt.Println("匹配窗口:", w.String())
	}
	if n, err := gamewin.FocusByTitle(kw); err != nil || n == 0 {
		fmt.Println("置顶失败:", err)
		os.Exit(1)
	}
	img, err := capture.GrabColor()
	if err != nil {
		fmt.Println("截屏失败:", err)
		os.Exit(1)
	}
	f, err := os.Create(out)
	if err != nil {
		fmt.Println(err)
		os.Exit(1)
	}
	defer f.Close()
	if err := png.Encode(f, img); err != nil {
		fmt.Println(err)
		os.Exit(1)
	}
	b := img.Bounds()
	fmt.Printf("已保存: %s (%dx%d)\n", out, b.Dx(), b.Dy())
}
