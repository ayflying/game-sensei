// focusdbg 窗口置顶诊断：区分三种置顶失败原因——
// 「目标窗口不存在」「前台被系统 UI（锁屏/UAC）占用」「前台锁拒绝」。
//
// 为什么需要它：置顶失败时错误信息只有一句「可能被前台锁拒绝」，
// 但真实原因往往不同（实测 2026-09-12 是锁屏，重试多少次都没用），
// 必须靠前台类名区分，否则会浪费大量时间在无意义的重试上。
//
// 用法: go run ./cmd/focusdbg <窗口标题关键词>
package main

import (
	"fmt"
	"os"

	"github.com/ayflying/game-sensei/internal/gamewin"
)

func main() {
	gamewin.EnsureDPIAware()

	fmt.Println("前台类名:", gamewin.ForegroundClassName())
	fmt.Println("是系统UI:", gamewin.ForegroundIsSystemUI())
	fmt.Println("前台标题:", gamewin.ForegroundTitle())

	if len(os.Args) < 2 {
		fmt.Println("用法: go run ./cmd/focusdbg <窗口标题关键词>")
		return
	}
	kw := os.Args[1]

	for _, w := range gamewin.FindByTitle(kw) {
		fmt.Println("匹配窗口:", w.String())
	}

	n, err := gamewin.FocusByTitle(kw)
	if err != nil {
		fmt.Println("置顶失败:", err)
		if gamewin.ForegroundIsSystemUI() {
			fmt.Println("提示: 前台是系统 UI（锁屏/UAC），重试无效，需人工解锁后重跑")
		} else {
			fmt.Println("提示: 前台非系统 UI，可再试一次；持续失败检查窗口是否已销毁")
		}
		os.Exit(1)
	}
	if n == 0 {
		fmt.Println("未找到匹配窗口:", kw)
		os.Exit(1)
	}
	fmt.Printf("置顶成功: %d 个窗口，当前前台=%q\n", n, gamewin.ForegroundTitle())
}
