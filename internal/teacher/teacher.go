// Package teacher 大模型教师（VLM/LLM）——Phase 1 起接入。
//
// 职责（见 README §2 异步教学回路）：
//   - 周期性读取 memory 里的轨迹回放，诊断学生错误；
//   - 产出「正确示范」动作序列，用于蒸馏微调学生；
//   - 定义/更新按游戏类型的评分标准。
//
// 实现走标准 net/http 调用本地 Ollama (/api/chat) 或 OpenAI 兼容接口，
// 无 CGO 依赖。本文件为 Phase 0 占位，仅声明接口，不含网络调用。
package teacher

// Teacher 是异步教学回路的能力抽象。
type Teacher interface {
	// Evaluate 接收一段轨迹摘要，返回诊断意见（文字反馈）。Phase 1 实现。
	// Evaluate(traj []memory.Record) (string, error)
	// Demonstrate 针对当前局面产出示范动作序列。Phase 2 实现。
}
