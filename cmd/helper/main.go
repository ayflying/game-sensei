package main

import "fmt"

// game-sensei —— 泛型游戏辅助：大模型教师 + 量化学生自学习闭环。
//
// 架构（详见仓库 README.md）：
//   实时执行回路 (goroutine A): capture(截屏) -> agent(量化学生推理) -> input(键鼠)
//   异步教学回路 (goroutine B): teacher(VLM 评估回放) -> 产出示范 -> 微调并重载学生 onnx
//   共享状态: memory(轨迹缓冲 channel) / config(目标·评分标准)
//
// 本文件是 Phase 0 占位骨架：仅打印启动信息，不引入任何外部依赖，
// 保证 `go build ./...` 通过。设计阶段再按 internal/ 下各包填充实现。
func main() {
	fmt.Println("game-sensei: bootstrap scaffold (Phase 0)")
	fmt.Println("TODO: 启动 capture/agent/input 实时 goroutine 与 teacher 异步 goroutine；详见 README.md 路线图与待设计项。")
}
