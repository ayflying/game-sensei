package teacher

import (
	"bytes"
	"fmt"
	"image"
	"image/png"
	"strings"
	"time"

	"github.com/ayflying/game-sensei/internal/agent"
)

// Frame 是送审的一帧：序号、采集时刻、该帧学生的动作，以及灰度截图。
//
// 送审用的是学生自己的观测（降采样灰度图），而不是原始彩色屏幕 —— 老师要评估的是
// 「学生在它看到的世界里做得对不对」，观测一致才谈得上诊断。
type Frame struct {
	No       int64
	At       time.Time
	Action   agent.Action
	GrayMean uint8
	Gray     *image.Gray
}

// PNG 把该帧编码为 PNG 字节；无图像时返回 nil，由调用方跳过。
func (f Frame) PNG() []byte {
	if f.Gray == nil {
		return nil
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, f.Gray); err != nil {
		return nil
	}
	return buf.Bytes()
}

// Episode 是一次送审的轨迹片段。
type Episode struct {
	Goal   string        // 游戏目标（老师据此判断动作合理性）
	Frames []Frame       // 关键帧，按时间升序
	Window time.Duration // 本次评估覆盖的时间跨度
}

// ActionLabel 返回动作的人类可读描述（提示词与日志共用）。
func ActionLabel(a agent.Action) string {
	switch a.Kind {
	case agent.ActionKey:
		return "按 " + a.Code
	case agent.ActionMove:
		return fmt.Sprintf("移动 (%d,%d)", a.Dx, a.Dy)
	default:
		return "不动"
	}
}

// SummarizeActions 统计一段轨迹里的动作分布，压缩成一行文字给老师。
// 逐帧罗列会浪费大量 token，老师只需知道动作倾向。
func SummarizeActions(frames []Frame) string {
	if len(frames) == 0 {
		return "（无动作）"
	}
	counts := map[string]int{}
	order := make([]string, 0, 4)
	for _, f := range frames {
		label := ActionLabel(f.Action)
		if _, seen := counts[label]; !seen {
			order = append(order, label)
		}
		counts[label]++
	}
	parts := make([]string, 0, len(order))
	for _, label := range order {
		pct := float64(counts[label]) * 100 / float64(len(frames))
		parts = append(parts, fmt.Sprintf("%s %d次(%.0f%%)", label, counts[label], pct))
	}
	return strings.Join(parts, "；")
}

// BuildPrompt 生成送审提示词。
//
// 三个刻意的设计：
//  1. 结构化输出（局面/评价/建议/评分），方便人工快速扫读，也为 Phase 2 解析打分铺路；
//  2. 明确要求「直接给结论、不要复述题目」——实测 qwen3 系列思考开销约 900 token，
//     不约束的话正文常被挤出预算；
//  3. 字数上限，避免小模型长篇大论拖慢教学回路。
func BuildPrompt(ep Episode) string {
	var b strings.Builder
	b.WriteString("你是游戏操作教练，正在评估一个 AI 学生的操作表现。\n\n")
	if ep.Goal != "" {
		fmt.Fprintf(&b, "【游戏目标】%s\n", ep.Goal)
	}
	fmt.Fprintf(&b, "【本次评估】覆盖 %d 个关键帧（按时间从早到晚排列，灰度图即学生所见）、约 %.1f 秒。\n",
		len(ep.Frames), ep.Window.Seconds())
	fmt.Fprintf(&b, "【学生动作分布】%s\n", SummarizeActions(ep.Frames))
	b.WriteString("\n请按下面四行结构输出，直接给结论，不要复述题目、不要展开长篇推理，总长不超过 200 字：\n")
	b.WriteString("局面：<一句话描述当前画面处于什么状态>\n")
	b.WriteString("评价：<学生动作是否合理，指出最明显的错误>\n")
	b.WriteString("建议：<下一步该做什么，给出可执行的具体动作>\n")
	b.WriteString("评分：<0-100 的整数>\n")
	return b.String()
}
