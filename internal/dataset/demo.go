// Package dataset 负责把老师示范（老师看画面 → 给出的动作）落成可训练的样本。
//
// 为什么单独一个包：示范数据是 Phase 2 蒸馏的输入，格式一旦定下来就要能被
// trainer/ 的 Python 侧稳定读取，不能散在 cmd/helper 里当临时日志写。
//
// 落盘结构（一个目录一次示范会话）：
//
//	<dir>/
//	  meta.json          会话元信息：目标、模型、屏幕尺寸、起止时间、统计
//	  trajectory.jsonl   每行一个样本：动作、耗时、原文本、帧文件名
//	  frames/step_0001.png   学生观测（灰度、降采样后）—— 训练时的网络输入
//	  color/step_0001.jpg    老师看到的彩色帧（可选，便于人工复核）
package dataset

import (
	"bufio"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sync"
	"time"

	"github.com/ayflying/game-sensei/internal/agent"
)

// Meta 是一次示范会话的元信息。
type Meta struct {
	Goal       string    `json:"goal"`
	Model      string    `json:"model"`
	Backend    string    `json:"backend"`
	ScreenW    int       `json:"screen_w"`
	ScreenH    int       `json:"screen_h"`
	DownWidth  int       `json:"down_width"` // 学生观测降采样宽度
	DemoWidth  int       `json:"demo_width"` // 送审老师的降采样宽度
	StartedAt  time.Time `json:"started_at"`
	FinishedAt time.Time `json:"finished_at"`
	Steps      int       `json:"steps"`
	ParsedOK   int       `json:"parsed_ok"` // 动作解析成功数
	TotalMs    float64   `json:"total_ms"`  // 老师累计耗时
	AvgMs      float64   `json:"avg_ms"`    // 平均单步老师耗时
	Live       bool      `json:"live"`      // 是否真实发送了动作
}

// StepInfo 是调用方要提供的、无法从动作本身推出的元信息。
type StepInfo struct {
	At         time.Time // 采集时刻，零值则用写入时刻
	Raw        string    // 老师原始输出（解析失败时靠它排查）
	Parsed     bool      // 是否成功解析成动作
	LatencyMs  float64   // 老师推理耗时
	OutTokens  int       // 老师输出 token 数
	PrevAction string    // 上一步动作串（老师决策时的上下文，训练时可作为额外输入）
}

// Step 是落盘的一行样本（字段与 trajectory.jsonl 一一对应）。
type Step struct {
	Index      int       `json:"index"`
	At         time.Time `json:"at"`
	Action     string    `json:"action"` // 归一化动作串，如 tap:0.800,0.810
	Kind       string    `json:"kind"`   // tap/swipe/hold/joy/key/move/none
	Nx         float64   `json:"nx,omitempty"`
	Ny         float64   `json:"ny,omitempty"`
	Nx2        float64   `json:"nx2,omitempty"`
	Ny2        float64   `json:"ny2,omitempty"`
	DurMs      int64     `json:"dur_ms,omitempty"`
	Code       string    `json:"code,omitempty"`
	Codes      []string  `json:"codes,omitempty"` // 多键（PC 斜向移动 s+d）
	Parsed     bool      `json:"parsed"`
	LatencyMs  float64   `json:"latency_ms"`
	OutTokens  int       `json:"out_tokens"`
	Raw        string    `json:"raw"`
	PrevAction string    `json:"prev_action,omitempty"`
	FrameGray  string    `json:"frame_gray"`
	FrameColor string    `json:"frame_color,omitempty"`
}

// Writer 把示范数据写进一个目录。
type Writer struct {
	dir    string
	meta   Meta
	frames int
	mu     sync.Mutex
	f      *os.File
	bw     *bufio.Writer
	closed bool
}

// NewWriter 创建目录结构并打开 trajectory.jsonl。
func NewWriter(dir string, meta Meta) (*Writer, error) {
	if err := os.MkdirAll(filepath.Join(dir, "frames"), 0o755); err != nil {
		return nil, fmt.Errorf("dataset: 创建 frames 目录失败: %w", err)
	}
	f, err := os.Create(filepath.Join(dir, "trajectory.jsonl"))
	if err != nil {
		return nil, fmt.Errorf("dataset: 创建 trajectory.jsonl 失败: %w", err)
	}
	if meta.StartedAt.IsZero() {
		meta.StartedAt = time.Now()
	}
	return &Writer{
		dir:  dir,
		meta: meta,
		f:    f,
		bw:   bufio.NewWriter(f),
	}, nil
}

// Dir 返回数据目录。
func (w *Writer) Dir() string { return w.dir }

// Step 写入一个样本，并落盘对应的灰度观测帧；withColor 为真时同时存彩色帧。
//
// grayPNG 必须是「学生将来会看到的观测」，不是老师看到的彩色图——
// 训练输入与推理输入不一致是模仿学习里最隐蔽也最致命的坑。
func (w *Writer) Step(act agent.Action, info StepInfo, grayPNG, colorJPEG []byte, withColor bool) error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return fmt.Errorf("dataset: Writer 已关闭")
	}

	w.frames++
	s := Step{
		Index:      w.frames,
		At:         info.At,
		Action:     act.String(),
		Kind:       KindName(act.Kind),
		Nx:         act.Nx,
		Ny:         act.Ny,
		Nx2:        act.Nx2,
		Ny2:        act.Ny2,
		DurMs:      act.Dur.Milliseconds(),
		Code:       act.Code,
		Codes:      act.Codes,
		Parsed:     info.Parsed,
		LatencyMs:  info.LatencyMs,
		OutTokens:  info.OutTokens,
		Raw:        info.Raw,
		PrevAction: info.PrevAction,
	}
	if s.At.IsZero() {
		s.At = time.Now()
	}

	if len(grayPNG) > 0 {
		name := fmt.Sprintf("step_%04d.png", s.Index)
		if err := os.WriteFile(filepath.Join(w.dir, "frames", name), grayPNG, 0o644); err != nil {
			return fmt.Errorf("dataset: 写灰度帧失败: %w", err)
		}
		s.FrameGray = "frames/" + name
	}
	if withColor && len(colorJPEG) > 0 {
		if err := os.MkdirAll(filepath.Join(w.dir, "color"), 0o755); err != nil {
			return err
		}
		name := fmt.Sprintf("step_%04d.jpg", s.Index)
		if err := os.WriteFile(filepath.Join(w.dir, "color", name), colorJPEG, 0o644); err != nil {
			return fmt.Errorf("dataset: 写彩色帧失败: %w", err)
		}
		s.FrameColor = "color/" + name
	}

	line, err := json.Marshal(s)
	if err != nil {
		return fmt.Errorf("dataset: 序列化样本失败: %w", err)
	}
	if _, err := w.bw.Write(line); err != nil {
		return err
	}
	if err := w.bw.WriteByte('\n'); err != nil {
		return err
	}

	w.meta.Steps++
	if s.Parsed {
		w.meta.ParsedOK++
	}
	w.meta.TotalMs += s.LatencyMs
	return nil
}

// Close 落盘 meta.json 并关闭文件。
func (w *Writer) Close() error {
	w.mu.Lock()
	defer w.mu.Unlock()
	if w.closed {
		return nil
	}
	w.closed = true

	if err := w.bw.Flush(); err != nil {
		w.f.Close()
		return err
	}
	if err := w.f.Close(); err != nil {
		return err
	}

	w.meta.FinishedAt = time.Now()
	if w.meta.Steps > 0 {
		w.meta.AvgMs = w.meta.TotalMs / float64(w.meta.Steps)
	}
	raw, err := json.MarshalIndent(w.meta, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(w.dir, "meta.json"), raw, 0o644)
}

// KindName 返回动作类型名，用于数据集与报告。
//
// ⚠️ 每个动作类型都必须在这里有分支：漏一个不会报错，只会静默落到 default 的
// "none"。实测踩坑（2026-09-13）：ActionPress（按命名按钮，如 press:cast_hetu /
// press:gather_energy）此前没有分支，于是战斗示范里**最该学的那些动作**全被记成
// "none"，训练器按 "none" 读进去，学生学到的是「战斗里什么都不做」——数据看着有
// 几十条，实际把正确行为标成了反面样本。
func KindName(k agent.ActionKind) string {
	switch k {
	case agent.ActionTap:
		return "tap"
	case agent.ActionSwipe:
		return "swipe"
	case agent.ActionLongPress:
		return "hold"
	case agent.ActionJoystick:
		return "joy"
	case agent.ActionKey:
		return "key"
	case agent.ActionMove:
		return "move"
	case agent.ActionPress:
		return "press"
	default:
		return "none"
	}
}
