// Package config 定义运行时配置：抓屏区域、目标帧率、降采样尺寸、动作集与开关。
// Phase 0 使用内置默认值 + 命令行覆盖；后续 Phase 可切换为 YAML/JSON。
package config

import "time"

// Config 是辅助框架的全局配置。
type Config struct {
	// TargetFPS 实时回路的节拍目标（Phase 0 仅用于节流与统计）。
	TargetFPS int
	// DownsampleWidth 抓屏后送入感知/决策的灰度图目标宽度（保持长宽比）。
	// 小分辨率是为了让学生网络输入可控、延迟低。
	DownsampleWidth int
	// Live 为 true 时真正调用 SendInput 发送键鼠；默认 false（dry-run），
	// 避免在桌面上误操作。
	Live bool
	// MaxFrames 运行多少帧后自动退出；<=0 表示一直运行直到 Ctrl+C。
	MaxFrames int
	// ReportEvery 每多少帧打印一次延迟统计。
	ReportEvery int

	// ---- Phase 1：异步教学回路（老师） ----

	// TeacherEnabled 是否启用老师评估。默认关闭，避免无人值守时意外消耗算力。
	TeacherEnabled bool
	// TeacherURL 老师服务地址（Ollama）。
	TeacherURL string
	// TeacherModel 老师模型名。
	TeacherModel string
	// EvalEvery 每多少帧抽一帧送给老师（抽样间隔）。
	EvalEvery int
	// EvalFrames 一次评估覆盖的关键帧数量，攒够即送审。
	EvalFrames int
	// EvalWidth 送审帧的降采样宽度。比学生输入（DownsampleWidth）宽，
	// 好让老师看清界面结构；仍保持灰度，与学生的观测形态一致。
	EvalWidth int
	// EvalTimeout 单次送审的超时。
	EvalTimeout time.Duration
	// Goal 游戏目标描述，写进提示词供老师判断动作合理性。
	Goal string
	// EvalOutDir 评估报告落盘目录；空则只打印到控制台。
	EvalOutDir string
}

// Default 返回一份安全的默认配置（dry-run，老师关闭）。
func Default() Config {
	return Config{
		TargetFPS:       30,
		DownsampleWidth: 160,
		Live:            false,
		MaxFrames:       0,
		ReportEvery:     30,

		TeacherEnabled: false,
		TeacherURL:     "http://127.0.0.1:11435",
		TeacherModel:   "qwen3.5:9b",
		EvalEvery:      300, // 30FPS 下约每 10 秒抽一帧
		EvalFrames:     6,   // 6 帧 ≈ 覆盖 1 分钟
		EvalWidth:      640,
		EvalTimeout:    180 * time.Second,
		Goal:           "",
		EvalOutDir:     "",
	}
}
