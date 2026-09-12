// unlock-watch 等待锁屏/系统 UI 解除后自动置顶目标窗口。
//
// 为什么需要它：锁屏（LockApp，类名 Windows.UI.Core.CoreWindow）在前台时，
// 任何跨进程置顶都会被拒，SendInput 的点击还会落到锁屏上，并让本进程的
// GDI 句柄批量失效（实测 BitBlt 连续报 "The handle is invalid"，2026-09-12）。
// 这类阻塞只能靠人工解锁解决，采集前挂上它，解锁那一刻自动恢复流程。
//
// 用法: go run ./cmd/unlock-watch <窗口标题关键词> [最长等待分钟数，默认 10]
package main

import (
	"fmt"
	"os"
	"strconv"
	"time"

	"github.com/ayflying/game-sensei/internal/gamewin"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Println("用法: go run ./cmd/unlock-watch <窗口标题关键词> [最长等待分钟数]")
		os.Exit(1)
	}
	kw := os.Args[1]
	minutes := 10
	if len(os.Args) > 2 {
		if v, err := strconv.Atoi(os.Args[2]); err == nil && v > 0 {
			minutes = v
		}
	}

	for i := 0; i < minutes*12; i++ { // 每 5 秒探一次
		if !gamewin.ForegroundIsSystemUI() {
			fmt.Printf("锁屏已解除，尝试置顶窗口 %q…\n", kw)
			n, err := gamewin.FocusByTitle(kw)
			if err != nil {
				fmt.Println("置顶失败:", err)
				os.Exit(1)
			}
			fmt.Printf("置顶成功: %d 个窗口，当前前台=%q\n", n, gamewin.ForegroundTitle())
			return
		}
		time.Sleep(5 * time.Second)
	}
	fmt.Printf("等待超时（%d 分钟），仍在锁屏\n", minutes)
	os.Exit(1)
}
