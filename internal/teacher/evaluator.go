package teacher

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// Evaluator 把轨迹片段送给老师并整理成评估报告。
type Evaluator struct {
	Client *Client
	Goal   string // 默认游戏目标，可被 Episode.Goal 覆盖
}

// NewEvaluator 构造评估器。
func NewEvaluator(c *Client, goal string) *Evaluator {
	return &Evaluator{Client: c, Goal: goal}
}

// Report 是一次评估的结果。
type Report struct {
	At            time.Time
	Model         string
	Frames        int
	WindowSeconds float64
	ActionSummary string
	Text          string // 老师的完整正文
	Score         int    // 从正文里解析出的 0-100 评分；解析失败为 -1
	Stats         Stats
}

// scoreRe 匹配「评分：85」「评分: 85 分」等写法。
var scoreRe = regexp.MustCompile(`评分\s*[:：]?\s*(\d{1,3})`)

// Evaluate 送审一次轨迹，返回评估报告。
func (e *Evaluator) Evaluate(ctx context.Context, ep Episode) (*Report, error) {
	if len(ep.Frames) == 0 {
		return nil, fmt.Errorf("空轨迹，无需评估")
	}
	if ep.Goal == "" {
		ep.Goal = e.Goal
	}

	images := make([][]byte, 0, len(ep.Frames))
	used := 0
	for _, f := range ep.Frames {
		if png := f.PNG(); png != nil {
			images = append(images, png)
			used++
		}
	}
	if used == 0 {
		return nil, fmt.Errorf("轨迹中没有可用图像帧")
	}

	reply, err := e.Client.Chat(ctx, BuildPrompt(ep), images)
	if err != nil {
		return nil, err
	}

	return &Report{
		At:            time.Now(),
		Model:         reply.Model,
		Frames:        used,
		WindowSeconds: ep.Window.Seconds(),
		ActionSummary: SummarizeActions(ep.Frames),
		Text:          reply.Text,
		Score:         parseScore(reply.Text),
		Stats:         reply.Stats,
	}, nil
}

// parseScore 从老师正文里抓评分；抓不到返回 -1（Phase 1 只做展示，不参与决策）。
func parseScore(text string) int {
	m := scoreRe.FindStringSubmatch(text)
	if len(m) < 2 {
		return -1
	}
	n, err := strconv.Atoi(m[1])
	if err != nil || n < 0 || n > 100 {
		return -1
	}
	return n
}

// Markdown 渲染为可落盘的报告。
func (r *Report) Markdown() string {
	var b strings.Builder
	fmt.Fprintf(&b, "# 老师评估报告\n\n")
	fmt.Fprintf(&b, "- 时间：%s\n", r.At.Format("2006-01-02 15:04:05"))
	fmt.Fprintf(&b, "- 模型：%s\n", r.Model)
	fmt.Fprintf(&b, "- 覆盖：%d 帧 / %.1f 秒\n", r.Frames, r.WindowSeconds)
	fmt.Fprintf(&b, "- 动作分布：%s\n", r.ActionSummary)
	if r.Score >= 0 {
		fmt.Fprintf(&b, "- 评分：%d\n", r.Score)
	}
	fmt.Fprintf(&b, "- 性能：%.1fs，输入 %d tok / 输出 %d tok，%.0f tok/s\n\n",
		r.Stats.TotalMs/1000, r.Stats.PromptTokens, r.Stats.OutputTokens, r.Stats.TokPerSec)
	b.WriteString("## 反馈正文\n\n")
	b.WriteString(r.Text)
	b.WriteString("\n")
	return b.String()
}

// SaveMarkdown 把报告写入目录（不存在则创建），文件名带时间戳。
func (r *Report) SaveMarkdown(dir string) (string, error) {
	if dir == "" {
		return "", fmt.Errorf("未指定输出目录")
	}
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return "", err
	}
	name := fmt.Sprintf("eval-%s.md", r.At.Format("20060102-150405"))
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, []byte(r.Markdown()), 0o644); err != nil {
		return "", err
	}
	return path, nil
}
