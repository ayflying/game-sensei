// go run ./cmd/winlist 临时列出所有可见顶层窗口（调试用）
package main

import (
	"fmt"

	"github.com/ayflying/game-sensei/internal/gamewin"
)

func main() {
	for _, w := range gamewin.List() {
		fmt.Printf("pid=%-8d visible=%-5v iconic=%-5v title=%q\n", w.PID, w.Visible, w.Iconic, w.Title)
	}
}
