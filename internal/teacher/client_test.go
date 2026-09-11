package teacher

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strconv"
	"strings"
	"testing"
	"time"
)

// 模拟 Ollama /api/chat 的响应体。
func ollamaReply(content, thinking string, evalCount int) string {
	return `{
	  "model": "qwen3.5:2b",
	  "message": {"role": "assistant", "content": "` + content + `", "thinking": "` + thinking + `"},
	  "done": true,
	  "prompt_eval_count": 2090,
	  "eval_count": ` + strconv.Itoa(evalCount) + `,
	  "eval_duration": 2000000000
	}`
}

func TestChatUsesContent(t *testing.T) {
	var gotBody []byte
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotBody, _ = io.ReadAll(r.Body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(ollamaReply("局面：正常\\n评分：80", "内部思考", 913)))
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "qwen3.5:2b", Options{})
	reply, err := c.Chat(context.Background(), "评估一下", [][]byte{{1, 2, 3}})
	if err != nil {
		t.Fatalf("Chat 失败: %v", err)
	}
	if !strings.Contains(reply.Text, "评分：80") {
		t.Errorf("正文解析错误: %q", reply.Text)
	}
	if reply.Thinking != "内部思考" {
		t.Errorf("thinking 未保留: %q", reply.Thinking)
	}
	if reply.Stats.PromptTokens != 2090 || reply.Stats.OutputTokens != 913 {
		t.Errorf("token 统计错误: %+v", reply.Stats)
	}
	if reply.Stats.TokPerSec <= 0 {
		t.Errorf("生成速度未计算: %+v", reply.Stats)
	}

	// 校验请求体：模型名、非流式、图片被 base64 编码、num_predict 已下发
	var req chatRequest
	if err := json.Unmarshal(gotBody, &req); err != nil {
		t.Fatalf("请求体解析失败: %v", err)
	}
	if req.Model != "qwen3.5:2b" || req.Stream {
		t.Errorf("请求参数错误: model=%s stream=%v", req.Model, req.Stream)
	}
	if len(req.Messages) != 1 || len(req.Messages[0].Images) != 1 {
		t.Fatalf("图片未正确附带: %+v", req.Messages)
	}
	if req.Messages[0].Images[0] != "AQID" { // base64({1,2,3})
		t.Errorf("图片 base64 编码错误: %q", req.Messages[0].Images[0])
	}
	if req.Options["num_predict"] != float64(DefaultMaxTokens) {
		t.Errorf("num_predict 未按默认下发: %v", req.Options["num_predict"])
	}
}

// qwen3 系列常把正文塞进 thinking、content 留空——必须兜底，否则会拿到空答复。
func TestChatFallsBackToThinking(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(ollamaReply("", "思考里的正文：局面正常", 913)))
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "qwen3.5:2b", Options{})
	reply, err := c.Chat(context.Background(), "p", nil)
	if err != nil {
		t.Fatalf("Chat 失败: %v", err)
	}
	if !strings.Contains(reply.Text, "思考里的正文") {
		t.Errorf("thinking 兜底失效: %q", reply.Text)
	}
}

func TestChatErrors(t *testing.T) {
	cases := []struct {
		name    string
		status  int
		body    string
		wantSub string
	}{
		{"HTTP 500", 500, `{"error":"boom"}`, "老师返回 500"},
		{"模型缺失", 404, `{"error":"model not found"}`, "老师返回 404"},
		{"空答复", 200, ollamaReply("", "", 1500), "未返回任何内容"},
		{"内嵌错误", 200, `{"error":"context canceled"}`, "老师报错"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(tc.status)
				_, _ = w.Write([]byte(tc.body))
			}))
			defer srv.Close()

			c := NewClient(srv.URL, "qwen3.5:2b", Options{})
			_, err := c.Chat(context.Background(), "p", nil)
			if err == nil {
				t.Fatalf("期望报错，实际成功")
			}
			if !strings.Contains(err.Error(), tc.wantSub) {
				t.Errorf("错误信息不含 %q: %v", tc.wantSub, err)
			}
		})
	}
}

func TestChatTimeout(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		time.Sleep(300 * time.Millisecond)
		_, _ = w.Write([]byte(ollamaReply("迟到", "", 10)))
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "qwen3.5:2b", Options{MaxTokens: 100, Timeout: 50 * time.Millisecond})
	if _, err := c.Chat(context.Background(), "p", nil); err == nil {
		t.Fatal("期望超时报错，实际成功")
	}
}

func TestPing(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/api/version" {
			w.WriteHeader(404)
			return
		}
		_, _ = w.Write([]byte(`{"version":"0.34.0"}`))
	}))
	defer srv.Close()

	c := NewClient(srv.URL, "qwen3.5:2b", Options{})
	v, err := c.Ping(context.Background())
	if err != nil {
		t.Fatalf("Ping 失败: %v", err)
	}
	if v != "0.34.0" {
		t.Errorf("版本号错误: %q", v)
	}
}
