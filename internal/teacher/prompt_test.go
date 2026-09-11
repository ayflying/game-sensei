package teacher

import (
	"context"
	"image"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/ayflying/game-sensei/internal/agent"
)

func grayFrame(no int64, v uint8) Frame {
	img := image.NewGray(image.Rect(0, 0, 8, 8))
	for i := range img.Pix {
		img.Pix[i] = v
	}
	return Frame{
		No:       no,
		At:       time.Now(),
		Action:   agent.Action{Kind: agent.ActionKey, Code: "right"},
		GrayMean: v,
		Gray:     img,
	}
}

func TestSummarizeActions(t *testing.T) {
	frames := []Frame{
		{Action: agent.Action{Kind: agent.ActionKey, Code: "left"}},
		{Action: agent.Action{Kind: agent.ActionKey, Code: "left"}},
		{Action: agent.Action{Kind: agent.ActionKey, Code: "right"}},
		{Action: agent.Action{Kind: agent.ActionNone}},
	}
	got := SummarizeActions(frames)
	for _, want := range []string{"按 left 2次(50%)", "按 right 1次(25%)", "不动 1次(25%)"} {
		if !strings.Contains(got, want) {
			t.Errorf("动作摘要缺少 %q，实际: %s", want, got)
		}
	}
	if SummarizeActions(nil) != "（无动作）" {
		t.Errorf("空轨迹摘要错误: %s", SummarizeActions(nil))
	}
}

func TestBuildPrompt(t *testing.T) {
	ep := Episode{
		Goal:   "把方块推到右侧终点",
		Frames: []Frame{grayFrame(1, 10), grayFrame(2, 20)},
		Window: 3 * time.Second,
	}
	p := BuildPrompt(ep)
	for _, want := range []string{
		"把方块推到右侧终点",
		"覆盖 2 个关键帧",
		"约 3.0 秒",
		"按 right 2次(100%)",
		"局面：", "评价：", "建议：", "评分：",
	} {
		if !strings.Contains(p, want) {
			t.Errorf("提示词缺少 %q\n实际:\n%s", want, p)
		}
	}
}

func TestEvaluateEndToEnd(t *testing.T) {
	var gotImages int
	var gotPrompt string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body := make([]byte, r.ContentLength)
		_, _ = r.Body.Read(body)
		gotPrompt = string(body)
		// 简单数一下 images 数组里的 base64 条目（每帧一个 "iVBOR" PNG 头）
		gotImages = strings.Count(gotPrompt, "iVBOR")
		_, _ = w.Write([]byte(`{"model":"qwen3.5:9b","message":{"content":"局面：方块在左侧\n评价：一直按右是对的\n建议：继续按右\n评分：72"},"done":true,"prompt_eval_count":2100,"eval_count":900,"eval_duration":1000000000}`))
	}))
	defer srv.Close()

	ev := NewEvaluator(NewClient(srv.URL, "qwen3.5:9b", Options{}), "把方块推到右侧终点")
	rep, err := ev.Evaluate(context.Background(), Episode{
		Frames: []Frame{grayFrame(1, 10), grayFrame(2, 20), grayFrame(3, 30)},
		Window: 2 * time.Second,
	})
	if err != nil {
		t.Fatalf("Evaluate 失败: %v", err)
	}
	if gotImages != 3 {
		t.Errorf("应送审 3 张图，实际 %d", gotImages)
	}
	if rep.Score != 72 {
		t.Errorf("评分解析错误: %d", rep.Score)
	}
	if rep.Frames != 3 || rep.Model != "qwen3.5:9b" {
		t.Errorf("报告元信息错误: %+v", rep)
	}
	if !strings.Contains(rep.Markdown(), "评分：72") || !strings.Contains(rep.Markdown(), "老师评估报告") {
		t.Errorf("Markdown 渲染异常:\n%s", rep.Markdown())
	}
}

func TestEvaluateRejectsEmptyEpisode(t *testing.T) {
	ev := NewEvaluator(NewClient("http://127.0.0.1:1", "qwen3.5:9b", Options{}), "goal")
	if _, err := ev.Evaluate(context.Background(), Episode{}); err == nil {
		t.Fatal("空轨迹应报错")
	}
	// 有帧但没有图像，同样应报错（避免把空图送给老师）
	if _, err := ev.Evaluate(context.Background(), Episode{Frames: []Frame{{No: 1}}}); err == nil {
		t.Fatal("无可用图像应报错")
	}
}

func TestParseScoreVariants(t *testing.T) {
	cases := map[string]int{
		"评分：85":      85,
		"评分: 85 分":   85,
		"评分 100":     100,
		"没有任何评分字段":   -1,
		"评分：999":     -1, // 越界视为无效
		"局面正常\n评分：0": 0,
	}
	for in, want := range cases {
		if got := parseScore(in); got != want {
			t.Errorf("parseScore(%q) = %d, 期望 %d", in, got, want)
		}
	}
}
