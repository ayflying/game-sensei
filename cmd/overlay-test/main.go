//go:build windows

// overlay 命令：日志浮窗的手工验证工具。
//
// 运行后屏幕右下角应出现一个半透明黑底置顶小窗，逐行滚动打印示例日志。
// 任何窗口（包括全屏程序）都不应遮挡它；它不接受焦点、不抢键盘。
//
// 用法：go run ./cmd/overlay-test [-seconds 30]
package main

import (
	"flag"
	"fmt"
	"time"

	"github.com/ayflying/game-sensei/internal/overlay"
)

func main() {
	seconds := flag.Int("seconds", 30, "运行秒数（0=直到 Ctrl+C）")
	flag.Parse()

	w, err := overlay.Start(overlay.Options{MaxLines: 6, WidthPx: 560, FontSize: 15})
	if err != nil {
		fmt.Println("浮窗启动失败:", err)
		return
	}
	fmt.Println("浮窗已启动（屏幕右下角）。本窗口打印与浮窗内容同步。")
	w.Push("game-sensei 日志浮窗已启动")
	w.Push("如果你能看到这行字，说明浮窗工作正常")

	for i := 1; ; i++ {
		if *seconds > 0 && i > *seconds {
			break
		}
		time.Sleep(time.Second)
		w.Push(fmt.Sprintf("[%d] 示例日志：老师 MOVE dir=前 → 摇杆推向 (0.21,0.61) | 1.2s 42tok", i))
	}
	w.Close()
	fmt.Println("测试结束。")
}
