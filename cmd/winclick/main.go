// go run ./cmd/winclick 临时工具：把窗口置顶后在指定像素坐标点击一次（调试用）
package main

import (
	"fmt"
	"os"
	"strconv"
	"time"

	"github.com/ayflying/game-sensei/internal/agent"
	"github.com/ayflying/game-sensei/internal/gamewin"
	"github.com/ayflying/game-sensei/internal/input"
)

func main() {
	if len(os.Args) < 4 {
		fmt.Println("用法: winclick <窗口标题关键词> <x> <y>  （屏幕绝对像素坐标）")
		os.Exit(1)
	}
	kw := os.Args[1]
	x, _ := strconv.Atoi(os.Args[2])
	y, _ := strconv.Atoi(os.Args[3])
	gamewin.EnsureDPIAware()
	wins := gamewin.FindByTitle(kw)
	if len(wins) == 0 {
		fmt.Println("未找到窗口:", kw)
		os.Exit(1)
	}
	fmt.Println("目标窗口:", wins[0].String())
	if n, err := gamewin.FocusByTitle(kw); err != nil || n == 0 {
		fmt.Println("置顶失败:", err)
		os.Exit(1)
	}
	time.Sleep(300 * time.Millisecond)
	act := input.NewActuator(true)
	act.Screen = func() (int, int, error) { return 1920, 1080, nil }
	a := agent.Action{Kind: agent.ActionTap, Nx: float64(x) / 1920, Ny: float64(y) / 1080}
	if err := act.Apply(a); err != nil {
		fmt.Println("点击失败:", err)
		os.Exit(1)
	}
	fmt.Printf("已点击 (%d,%d)\n", x, y)
}
