// Package config 定义运行时配置：抓屏区域、目标帧率、降采样尺寸、动作集与开关。
// Phase 0 使用内置默认值 + 命令行覆盖；后续 Phase 可切换为 YAML/JSON。
package config

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
}

// Default 返回一份安全的 Phase 0 默认配置（dry-run）。
func Default() Config {
	return Config{
		TargetFPS:       30,
		DownsampleWidth: 160,
		Live:            false,
		MaxFrames:       0,
		ReportEvery:     30,
	}
}
