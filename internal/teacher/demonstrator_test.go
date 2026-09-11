package teacher

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/ayflying/game-sensei/internal/agent"
)

// 这是本轮最关键的回归测试：顶层 think 字段必须真的下发。
// 早期把它写进 options 里，导致 qwen3.5 关不掉思考——单次要 60s+ 且正文为空。
func TestActionOptions关思考(t *testing.T) {
	opts := ActionOptions()
	if opts.Think == nil || *opts.Think {
		t.Fatalf("ActionOptions().Think 应为 false，实际 %v", opts.Think)
	}
	if opts.MaxTokens != ActionMaxTokens {
		t.Errorf("MaxTokens=%d, 期望 %d", opts.MaxTokens, ActionMaxTokens)
	}
}

// 默认（评估场景）不该动 think，保持模型原有行为。
func TestDefaultOptions不设think(t *testing.T) {
	if DefaultOptions().Think != nil {
		t.Errorf("DefaultOptions().Think 应为 nil，实际 %v", *DefaultOptions().Think)
	}
}

// NewClient 补默认值时不能把调用方设好的 Think 覆盖掉。
func TestNewClient保留Think(t *testing.T) {
	c := NewClient("http://x", "m", Options{Think: NoThink()})
	if c.Options.Think == nil || *c.Options.Think {
		t.Fatalf("Think 被覆盖: %v", c.Options.Think)
	}
	if c.Options.MaxTokens != DefaultMaxTokens || c.Options.Timeout <= 0 {
		t.Errorf("默认值未补齐: %+v", c.Options)
	}
}

func TestChat下发顶层think(t *testing.T) {
	cases := []struct {
		name      string
		opts      Options
		wantField bool
	}{
		{"关思考应下发 think=false", Options{Think: NoThink()}, true},
		{"未设置则不下发 think 字段", Options{}, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var gotBody []byte
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				gotBody, _ = io.ReadAll(r.Body)
				_, _ = w.Write([]byte(ollamaReply("ok", "", 5)))
			}))
			defer srv.Close()

			c := NewClient(srv.URL, "qwen3.5:9b", tc.opts)
			if _, err := c.Chat(context.Background(), "p", nil); err != nil {
				t.Fatalf("Chat 失败: %v", err)
			}
			// 用原始 JSON 检查字段是否存在（结构体零值无法区分「没传」）
			var raw map[string]any
			if err := json.Unmarshal(gotBody, &raw); err != nil {
				t.Fatalf("请求体解析失败: %v", err)
			}
			v, present := raw["think"]
			if present != tc.wantField {
				t.Fatalf("think 字段存在性=%v, 期望 %v (body=%s)", present, tc.wantField, gotBody)
			}
			if tc.wantField && v != false {
				t.Errorf("think=%v, 期望 false", v)
			}
			// 确保没有把 think 塞进 options（那是当初踩的坑）
			if opts, ok := raw["options"].(map[string]any); ok {
				if _, bad := opts["think"]; bad {
					t.Error("think 被错误地写进了 options，Ollama 会忽略它")
				}
			}
		})
	}
}

func TestBuildDemoPrompt(t *testing.T) {
	d := &Demonstrator{
		Goal:  "抓到一只水系精灵",
		Hints: []string{"虚拟摇杆中心约在 x=0.21 y=0.69"},
	}
	p := d.BuildDemoPrompt()

	for _, want := range []string{
		"洛克王国：世界", "抓到一只水系精灵", "虚拟摇杆中心",
		"ACTION TAP", "ACTION JOYSTICK", "0~1",
	} {
		if !strings.Contains(p, want) {
			t.Errorf("提示词缺少 %q", want)
		}
	}
	if strings.Contains(p, "【本次评估】") {
		t.Error("出动作提示词不应带评估模板")
	}
}

func TestDemonstrator解析动作(t *testing.T) {
	cases := []struct {
		name     string
		reply    string
		wantKind agent.ActionKind
		wantUsed bool
	}{
		{"标准点击", "ACTION TAP x=0.80 y=0.81", agent.ActionTap, true},
		{"摇杆", "ACTION JOYSTICK cx=0.21 cy=0.69 tx=0.81 ty=0.69 dur=1200", agent.ActionJoystick, true},
		// 注意用 \\n：ollamaReply 是把内容直接拼进 JSON 串的，真实换行会让 JSON 非法
		{"带前后缀噪音", "好的，我建议：\\nACTION TAP x=0.5 y=0.5\\n以上。", agent.ActionTap, true},
		{"解析失败但不算错", "我应该点击右下角的交互按钮", agent.ActionNone, false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				_, _ = w.Write([]byte(ollamaReply(tc.reply, "", 16)))
			}))
			defer srv.Close()

			d := &Demonstrator{Client: NewClient(srv.URL, "qwen3.5:9b", ActionOptions())}
			res, err := d.Act(context.Background(), []byte{1, 2, 3})
			if err != nil {
				t.Fatalf("Act 不该报错: %v", err)
			}
			if res.Used != tc.wantUsed {
				t.Fatalf("Used=%v, 期望 %v (raw=%q)", res.Used, tc.wantUsed, res.Raw)
			}
			if res.Action.Kind != tc.wantKind {
				t.Errorf("Kind=%v, 期望 %v", res.Action.Kind, tc.wantKind)
			}
			if res.Reply == nil || res.Reply.Stats.OutputTokens != 16 {
				t.Errorf("统计未带出: %+v", res.Reply)
			}
		})
	}
}

func TestDemonstrator调用失败应报错(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(500)
		_, _ = w.Write([]byte(`{"error":"boom"}`))
	}))
	defer srv.Close()

	d := &Demonstrator{Client: NewClient(srv.URL, "qwen3.5:9b", ActionOptions())}
	if _, err := d.Act(context.Background(), nil); err == nil {
		t.Fatal("HTTP 500 时应报错")
	}
}

func TestDemonstrator无客户端(t *testing.T) {
	d := &Demonstrator{}
	if _, err := d.Act(context.Background(), nil); err == nil {
		t.Fatal("未设置 Client 时应报错")
	}
}
