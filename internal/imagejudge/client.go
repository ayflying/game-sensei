package imagejudge

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

type Attempt struct{ Backend, Model, Status string }
type Result struct {
	Text, Backend, Model string
	Attempts             []Attempt
}

// Judge 逐渠道模型尝试，最后本地兜底；不持有对话历史。
func Judge(ctx context.Context, c Config, p Picture, question, backend string) (Result, error) {
	var r Result
	if strings.TrimSpace(question) == "" || len(p.Data) == 0 {
		return r, fmt.Errorf("图片和问题不能为空")
	}
	if backend != "auto" && backend != "aiferry" && backend != "ollama" {
		return r, fmt.Errorf("backend必须为auto、aiferry或ollama")
	}
	if c.Timeout <= 0 {
		c.Timeout = 120 * time.Second
	}
	run := func(b, m string) bool {
		text, err := call(ctx, c, p, question, b, m)
		a := Attempt{b, m, "成功"}
		if err != nil {
			a.Status = err.Error()
		}
		r.Attempts = append(r.Attempts, a)
		if err != nil {
			return false
		}
		r.Text = text
		r.Backend = b
		r.Model = m
		return true
	}
	if backend != "ollama" {
		if c.ChannelURL == "" || c.APIKey == "" || len(c.Models) == 0 {
			r.Attempts = append(r.Attempts, Attempt{Backend: "aiferry", Status: "渠道配置不完整"})
		} else {
			for _, m := range c.Models {
				if ctx.Err() != nil {
					return r, fmt.Errorf("判读已取消")
				}
				if run("aiferry", m) {
					return r, nil
				}
			}
		}
	}
	if backend != "aiferry" && ctx.Err() == nil {
		if c.OllamaURL == "" || c.OllamaModel == "" {
			r.Attempts = append(r.Attempts, Attempt{Backend: "ollama", Status: "本地配置不完整"})
		} else if run("ollama", c.OllamaModel) {
			return r, nil
		}
	}
	return r, fmt.Errorf("所选识图后端全部失败，请检查尝试状态")
}

func call(ctx context.Context, c Config, p Picture, q, b, m string) (string, error) {
	enc := base64.StdEncoding.EncodeToString(p.Data)
	var payload any
	endpoint := c.ChannelURL
	if b == "aiferry" {
		payload = map[string]any{"model": m, "stream": false, "messages": []any{map[string]any{"role": "system", "content": "只依据图片回答，区分观察与推断，不确定就明说。"}, map[string]any{"role": "user", "content": []any{map[string]any{"type": "text", "text": q}, map[string]any{"type": "image_url", "image_url": map[string]string{"url": "data:" + p.MIME + ";base64," + enc}}}}}}
	} else {
		endpoint = c.OllamaURL + "/api/chat"
		payload = map[string]any{"model": m, "stream": false, "think": false, "options": map[string]any{"num_predict": 2000}, "messages": []any{map[string]any{"role": "user", "content": q, "images": []string{enc}}}}
	}
	data, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("请求编码失败")
	}
	send := func(proxy string) ([]byte, bool, error) {
		reqCtx, cancel := context.WithTimeout(ctx, c.Timeout)
		defer cancel()
		req, e := http.NewRequestWithContext(reqCtx, http.MethodPost, endpoint, bytes.NewReader(data))
		if e != nil {
			return nil, false, fmt.Errorf("请求地址无效")
		}
		req.Header.Set("Content-Type", "application/json")
		if b == "aiferry" {
			req.Header.Set("Authorization", "Bearer "+c.APIKey)
		}
		tr := http.DefaultTransport.(*http.Transport).Clone()
		tr.Proxy = nil
		defer tr.CloseIdleConnections()
		if proxy != "" {
			u, e := url.Parse(proxy)
			if e != nil {
				return nil, false, fmt.Errorf("代理地址无效")
			}
			tr.Proxy = http.ProxyURL(u)
		}
		client := &http.Client{Transport: tr, CheckRedirect: func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }}
		resp, e := client.Do(req)
		if e != nil {
			return nil, true, fmt.Errorf("连接失败或请求超时")
		}
		defer resp.Body.Close()
		if resp.StatusCode < 200 || resp.StatusCode >= 300 {
			return nil, false, fmt.Errorf("HTTP %d", resp.StatusCode)
		}
		const limit = 2 * 1024 * 1024
		raw, e := io.ReadAll(io.LimitReader(resp.Body, limit+1))
		if e != nil {
			return nil, false, fmt.Errorf("响应读取失败")
		}
		if len(raw) > limit {
			return nil, false, fmt.Errorf("响应超过大小限制")
		}
		return raw, false, nil
	}
	raw, network, err := send("")
	if err != nil && network && b == "aiferry" && c.ProxyURL != "" && ctx.Err() == nil {
		raw, _, err = send(c.ProxyURL)
	}
	if err != nil {
		return "", err
	}
	var text string
	if b == "aiferry" {
		var out struct {
			Choices []struct {
				FinishReason string `json:"finish_reason"`
				Message      struct {
					Content string `json:"content"`
				} `json:"message"`
			} `json:"choices"`
		}
		if json.Unmarshal(raw, &out) != nil || len(out.Choices) == 0 {
			return "", fmt.Errorf("响应格式无效")
		}
		reason := out.Choices[0].FinishReason
		if reason != "stop" && reason != "" {
			return "", fmt.Errorf("输出未正常结束或被截断")
		}
		text = out.Choices[0].Message.Content
	} else {
		var out struct {
			Done    bool   `json:"done"`
			Reason  string `json:"done_reason"`
			Error   string `json:"error"`
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		}
		if json.Unmarshal(raw, &out) != nil {
			return "", fmt.Errorf("响应格式无效")
		}
		if out.Error != "" || !out.Done || (out.Reason != "" && out.Reason != "stop") {
			return "", fmt.Errorf("本地输出未完成或被截断")
		}
		text = out.Message.Content
	}
	text = strings.TrimSpace(text)
	if text == "" {
		return "", fmt.Errorf("未返回正文")
	}
	// 防止服务回显图片编码或密钥进入主对话。
	if strings.Contains(text, "base64,") || strings.Contains(text, enc) || (c.APIKey != "" && strings.Contains(text, c.APIKey)) {
		return "", fmt.Errorf("响应包含敏感请求内容，已拦截")
	}
	return text, nil
}
