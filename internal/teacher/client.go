// Package teacher 实现「老师」：调用 Ollama 上的视觉语言模型（VLM），
// 对学生轨迹做离线评估并产出文字反馈（Phase 1）。
//
// 实测踩坑（详见 README §9.1），客户端必须处理：
//   - qwen3 系列把推理过程写在 message.thinking、正文写在 message.content，
//     正文可能为空 —— 必须双字段兜底，否则拿到空答复；
//   - 思考会吃掉大量 token 预算，num_predict 需给足（默认 1500），
//     否则整轮都在思考、正文永远出不来；
//   - options.think=false 在 Ollama 0.34.0 上对 qwen3.5 系列无效，不能依赖。
//
// 本包只依赖标准库，与平台无关，便于单测。
package teacher

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// 默认推理参数。MaxTokens 给足是因为 qwen3 系列的思考开销很大（实测约 900 token）。
const (
	DefaultMaxTokens   = 1500
	DefaultTemperature = 0.0
	DefaultTimeout     = 180 * time.Second
)

// Options 是单次推理的采样参数与超时。
type Options struct {
	MaxTokens   int           // num_predict：输出 token 上限（含思考）
	Temperature float64       // 采样温度，评估场景用 0
	Timeout     time.Duration // 单次请求超时
}

// DefaultOptions 返回评估场景的保守默认值。
func DefaultOptions() Options {
	return Options{
		MaxTokens:   DefaultMaxTokens,
		Temperature: DefaultTemperature,
		Timeout:     DefaultTimeout,
	}
}

// Stats 是一次推理的性能统计（用于观测老师是否拖慢教学回路）。
type Stats struct {
	TotalMs      float64 // 端到端耗时
	PromptTokens int     // 输入 token（含视觉编码）
	OutputTokens int     // 输出 token（含思考）
	TokPerSec    float64 // 生成速度
}

// Reply 是老师的一次答复。
type Reply struct {
	Text     string // 正文；content 为空时退回 thinking（双字段兜底）
	Thinking string // 原始思考内容，便于排查
	Model    string
	Stats    Stats
}

// Client 是 Ollama /api/chat 的最小客户端。
type Client struct {
	BaseURL string // 例如 http://127.0.0.1:11435
	Model   string // 例如 qwen3.5:9b
	Options Options
	HTTP    *http.Client
}

// NewClient 构造客户端；opts 为零值时使用 DefaultOptions。
func NewClient(baseURL, model string, opts Options) *Client {
	if opts.MaxTokens <= 0 || opts.Timeout <= 0 {
		opts = DefaultOptions()
	}
	return &Client{
		BaseURL: strings.TrimRight(baseURL, "/"),
		Model:   model,
		Options: opts,
		HTTP:    &http.Client{Timeout: opts.Timeout},
	}
}

// Name 返回模型标识，用于日志。
func (c *Client) Name() string { return c.Model }

// Ping 探测服务可用性（GET /api/version），供启动自检使用。
func (c *Client) Ping(ctx context.Context) (string, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.BaseURL+"/api/version", nil)
	if err != nil {
		return "", err
	}
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return "", fmt.Errorf("连接 %s 失败: %w", c.BaseURL, err)
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("Ollama 返回 %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var v struct {
		Version string `json:"version"`
	}
	if err := json.Unmarshal(body, &v); err != nil {
		return "", nil // 版本号解析失败不影响可用性
	}
	return v.Version, nil
}

type chatMessage struct {
	Role    string   `json:"role"`
	Content string   `json:"content"`
	Images  []string `json:"images,omitempty"`
}

type chatRequest struct {
	Model    string         `json:"model"`
	Messages []chatMessage  `json:"messages"`
	Stream   bool           `json:"stream"`
	Options  map[string]any `json:"options"`
}

type chatResponse struct {
	Model   string `json:"model"`
	Message struct {
		Role     string `json:"role"`
		Content  string `json:"content"`
		Thinking string `json:"thinking"`
	} `json:"message"`
	Done            bool   `json:"done"`
	PromptEvalCount int    `json:"prompt_eval_count"`
	EvalCount       int    `json:"eval_count"`
	TotalDuration   int64  `json:"total_duration"` // 纳秒
	PromptEvalDur   int64  `json:"prompt_eval_duration"`
	EvalDuration    int64  `json:"eval_duration"`
	Error           string `json:"error"`
}

// Chat 发一次非流式请求。images 为按时间顺序排列的 PNG 字节。
//
// 采用非流式是刻意的：教学回路是异步低频的（每 N 局一次），
// 不需要边生成边消费，一次拿全更简单、统计也更准。
func (c *Client) Chat(ctx context.Context, prompt string, images [][]byte) (*Reply, error) {
	msg := chatMessage{Role: "user", Content: prompt}
	for _, img := range images {
		msg.Images = append(msg.Images, base64.StdEncoding.EncodeToString(img))
	}
	reqBody := chatRequest{
		Model:    c.Model,
		Messages: []chatMessage{msg},
		Stream:   false,
		Options: map[string]any{
			"temperature": c.Options.Temperature,
			"num_predict": c.Options.MaxTokens,
		},
	}
	raw, err := json.Marshal(reqBody)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.BaseURL+"/api/chat", bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")

	t0 := time.Now()
	resp, err := c.HTTP.Do(req)
	if err != nil {
		return nil, fmt.Errorf("调用老师失败: %w", err)
	}
	defer resp.Body.Close()
	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("读取老师响应失败: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("老师返回 %d: %s", resp.StatusCode, truncate(string(body), 200))
	}

	var out chatResponse
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, fmt.Errorf("解析老师响应失败: %w", err)
	}
	if out.Error != "" {
		return nil, fmt.Errorf("老师报错: %s", out.Error)
	}

	text := strings.TrimSpace(out.Message.Content)
	thinking := strings.TrimSpace(out.Message.Thinking)
	if text == "" {
		// 双字段兜底：qwen3 系列常把正文写进 thinking、content 留空
		text = thinking
	}
	if text == "" {
		return nil, fmt.Errorf("老师未返回任何内容（输出 %d token，可能思考预算不足）", out.EvalCount)
	}

	stats := Stats{
		TotalMs:      float64(time.Since(t0).Microseconds()) / 1000.0,
		PromptTokens: out.PromptEvalCount,
		OutputTokens: out.EvalCount,
	}
	if out.EvalDuration > 0 {
		stats.TokPerSec = float64(out.EvalCount) / (float64(out.EvalDuration) / 1e9)
	}

	return &Reply{
		Text:     text,
		Thinking: thinking,
		Model:    out.Model,
		Stats:    stats,
	}, nil
}

func truncate(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
