// 学生权重体检：回答「这个 .weights.json 能不能被运行时安全加载、它到底会不会用」。
//
// 用法:
//
//	go run ./tools/wcheck models/nrc_real_v1.weights.json
//
// 为什么要单独做这个工具：权重文件是 Python 训练与 Go 推理之间的**唯一接口**，
// 两侧的类别顺序/维度只要有一处漂移，推理就会「不报错但决策全错」。
// 训练侧已经在 meta 里写了如实的能力说明（val_acc / 多数类基线 / 空类清单），
// 这个工具把这些说明连同加载结果一起摊开给人看。
package main

import (
	"encoding/json"
	"fmt"
	"image"
	"os"

	"github.com/ayflying/game-sensei/internal/student"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Println("用法: go run ./tools/wcheck <weights.json>")
		os.Exit(2)
	}
	path := os.Args[1]

	fmt.Printf("权重文件: %s\n", path)

	// 1) 运行时会怎么看待这个文件（类别声明/维度是否一致）
	net, err := student.Load(path)
	if err != nil {
		fmt.Printf("运行时加载: 失败\n  %v\n", err)
		os.Exit(1)
	}
	fmt.Printf("运行时加载: OK（声明 %d 类，与运行时逐项一致）\n", len(student.Classes))

	// 2) meta 里的能力说明（训练侧如实写入，这里原样透出）
	if raw, rerr := os.ReadFile(path); rerr == nil {
		var doc struct {
			Meta map[string]any `json:"meta"`
			Arch struct {
				Classes []string `json:"classes"`
			} `json:"arch"`
		}
		if json.Unmarshal(raw, &doc) == nil {
			fmt.Printf("arch.classes: %v\n", doc.Arch.Classes)
			for _, k := range []string{"samples", "val_acc", "val_acc_final",
				"val_majority_baseline", "val_samples", "missing_classes", "epochs"} {
				if v, ok := doc.Meta[k]; ok {
					fmt.Printf("meta.%-22s %v\n", k+":", v)
				}
			}
			if note, ok := doc.Meta["val_acc_note"].(string); ok {
				fmt.Printf("meta.val_acc_note: %s\n", note)
			}
		}
	}

	// 3) 前向跑通 + 输出合法
	frame := image.NewGray(image.Rect(0, 0, 1024, 683))
	for i := range frame.Pix {
		frame.Pix[i] = uint8((i * 7) % 256)
	}
	act, err := net.Decide(frame)
	if err != nil {
		fmt.Printf("前向: 失败 %v\n", err)
		os.Exit(1)
	}
	valid := false
	for _, c := range student.Classes {
		if c == act.Class {
			valid = true
		}
	}
	fmt.Printf("前向: 类别=%q 坐标=(%.3f, %.3f) 类别合法=%v\n", act.Class, act.X, act.Y, valid)
	if !valid {
		fmt.Println("❌ 输出类别不在运行时类别集内")
		os.Exit(1)
	}
	fmt.Println("结论: 可被运行时加载并前向（能力是否够用见上面的 meta）")
}
