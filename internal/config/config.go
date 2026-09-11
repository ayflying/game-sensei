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

	// ---- Phase 2：老师在线示范（出动作 + 采集训练数据） ----
	//
	// Demo 为 true 时不跑固定节拍的实时回路，改由老师「一步一决策」地驱动：
	// 抓彩色画面 → 送审老师 → 执行动作 → 等游戏响应 → 再抓。
	// 之所以不复用 30FPS 的实时回路：老师单次要 1.2s，节拍天然是「秒级」，
	// 硬塞进更快的回路只会不停丢帧、看不出真实因果。

	// Demo 启用老师在线示范模式。
	Demo bool
	// DemoSteps 示范步数上限（0=直到 Ctrl+C）。
	DemoSteps int
	// DemoWidth 送审老师的彩色帧降采样宽度（比 EvalWidth 大，界面元素要看得清）。
	DemoWidth int
	// DemoWait 每步动作后等待游戏响应的时间。
	DemoWait time.Duration
	// DemoOut 示范数据落盘目录；空则用默认目录。
	DemoOut string
	// DemoColor 是否同时保存老师看到的彩色帧（便于人工复核，占空间）。
	DemoColor bool

	// ---- 游戏档案（internal/game） ----
	//
	// Game 指定当前要玩的游戏档案：档案名（如 nrc、pc_generic）或 JSON 路径。
	//
	// 档案回答四个问题：怎么移动（虚拟摇杆/十字键/WASD）、摇杆在哪、
	// 有哪些命名按钮、有哪些界面先验。它把「跨游戏通用的语义动作」
	// 翻译成「这台设备上的具体操作」，因此**换游戏只需换一份档案**，
	// 动作空间、提示词模板与决策代码都不动。
	//
	// 留空则不带任何游戏知识：只能做 TAP/SWIPE/HOLD/KEY/WAIT，
	// MOVE 与 PRESS 会明确报错（而不是瞎猜一个坐标去点）。
	Game string

	// ---- Android（ADB）后端 ----

	// Target 控制目标："pc" 驱动本机键鼠（默认）；"android" 通过 ADB 遥控手机。
	Target string
	// ADBPath adb 可执行文件路径；空则依次从 ANDROID_HOME、PATH、常见目录自动查找。
	ADBPath string
	// Serial ADB 设备序列号；空则要求恰好一台在线设备（多台时报错，避免误操作到别的手机）。
	Serial string
	// AppPackage android 模式下要操作的应用包名（如 com.tencent.nrc）。
	AppPackage string
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

		Demo:      false,
		DemoSteps: 20,
		DemoWidth: 1024,
		DemoWait:  2 * time.Second,
		DemoOut:   "",
		DemoColor: false,

		Game: "",

		Target:     "pc",
		ADBPath:    "",
		Serial:     "",
		AppPackage: "",
	}
}
