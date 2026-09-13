package dataset

import (
	"bufio"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/ayflying/game-sensei/internal/agent"
)

func TestWriter落盘结构(t *testing.T) {
	dir := t.TempDir()
	w, err := NewWriter(dir, Meta{Goal: "抓精灵", Model: "qwen3.5:9b", DownWidth: 160, DemoWidth: 1024})
	if err != nil {
		t.Fatalf("NewWriter 失败: %v", err)
	}

	steps := []struct {
		act   agent.Action
		info  StepInfo
		gray  []byte
		color []byte
	}{
		{agent.Action{Kind: agent.ActionTap, Nx: 0.8, Ny: 0.81},
			StepInfo{Raw: "ACTION TAP x=0.8 y=0.81", Parsed: true, LatencyMs: 1200, OutTokens: 16},
			[]byte("gray1"), []byte("color1")},
		{agent.Action{Kind: agent.ActionJoystick, Nx: 0.21, Ny: 0.69, Nx2: 0.81, Ny2: 0.69, Dur: 1200 * time.Millisecond},
			StepInfo{Raw: "ACTION JOYSTICK ...", Parsed: true, LatencyMs: 1300, OutTokens: 20},
			[]byte("gray2"), []byte("color2")},
		{agent.Action{Kind: agent.ActionNone},
			StepInfo{Raw: "我应该点击右下角", Parsed: false, LatencyMs: 900},
			[]byte("gray3"), nil},
	}
	for _, s := range steps {
		if err := w.Step(s.act, s.info, s.gray, s.color, true); err != nil {
			t.Fatalf("Step 失败: %v", err)
		}
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close 失败: %v", err)
	}

	// trajectory.jsonl 行数与内容
	f, err := os.Open(filepath.Join(dir, "trajectory.jsonl"))
	if err != nil {
		t.Fatalf("打开 trajectory.jsonl 失败: %v", err)
	}
	defer f.Close()
	var got []Step
	sc := bufio.NewScanner(f)
	for sc.Scan() {
		var s Step
		if err := json.Unmarshal(sc.Bytes(), &s); err != nil {
			t.Fatalf("解析样本行失败: %v (行=%s)", err, sc.Text())
		}
		got = append(got, s)
	}
	if len(got) != 3 {
		t.Fatalf("样本数 %d, 期望 3", len(got))
	}

	if got[0].Index != 1 || got[0].Kind != "tap" || got[0].Action != "tap:0.800,0.810" {
		t.Errorf("样本1 字段错: %+v", got[0])
	}
	if got[1].Kind != "joy" || got[1].DurMs != 1200 || got[1].Nx2 != 0.81 {
		t.Errorf("样本2 字段错: %+v", got[1])
	}
	if got[2].Parsed {
		t.Error("样本3 应标记为解析失败")
	}
	if got[2].Raw == "" {
		t.Error("解析失败时也必须保留原始输出，否则无法排查")
	}

	// 帧文件确实存在
	for _, s := range got {
		if s.FrameGray == "" {
			t.Errorf("样本 %d 缺少灰度帧路径", s.Index)
			continue
		}
		if _, err := os.Stat(filepath.Join(dir, s.FrameGray)); err != nil {
			t.Errorf("灰度帧不存在: %v", err)
		}
	}
	if got[2].FrameColor != "" {
		t.Error("未提供彩色帧时不该写 frame_color")
	}

	// 第三帧没给彩色，检查前两帧有
	if got[0].FrameColor == "" || got[1].FrameColor == "" {
		t.Error("前两帧应写入彩色帧路径")
	}
}

func TestWriterMeta统计(t *testing.T) {
	dir := t.TempDir()
	w, err := NewWriter(dir, Meta{Model: "m"})
	if err != nil {
		t.Fatalf("NewWriter 失败: %v", err)
	}
	_ = w.Step(agent.Action{Kind: agent.ActionTap, Nx: 0.1, Ny: 0.2},
		StepInfo{Parsed: true, LatencyMs: 1000}, nil, nil, false)
	_ = w.Step(agent.Action{Kind: agent.ActionNone},
		StepInfo{Parsed: false, LatencyMs: 2000}, nil, nil, false)
	if err := w.Close(); err != nil {
		t.Fatalf("Close 失败: %v", err)
	}

	raw, err := os.ReadFile(filepath.Join(dir, "meta.json"))
	if err != nil {
		t.Fatalf("读取 meta.json 失败: %v", err)
	}
	var m Meta
	if err := json.Unmarshal(raw, &m); err != nil {
		t.Fatalf("解析 meta.json 失败: %v", err)
	}
	if m.Steps != 2 || m.ParsedOK != 1 {
		t.Errorf("统计错误: steps=%d parsedOK=%d", m.Steps, m.ParsedOK)
	}
	if m.TotalMs != 3000 || m.AvgMs != 1500 {
		t.Errorf("耗时统计错误: total=%v avg=%v", m.TotalMs, m.AvgMs)
	}
	if m.StartedAt.IsZero() || m.FinishedAt.IsZero() {
		t.Error("起止时间未填写")
	}
	if m.FinishedAt.Before(m.StartedAt) {
		t.Error("结束时间早于开始时间")
	}
}

func TestWriter关闭后拒绝写入(t *testing.T) {
	dir := t.TempDir()
	w, _ := NewWriter(dir, Meta{})
	if err := w.Close(); err != nil {
		t.Fatalf("Close 失败: %v", err)
	}
	if err := w.Step(agent.Action{}, StepInfo{}, nil, nil, false); err == nil {
		t.Error("关闭后写入应报错")
	}
	// 重复 Close 应幂等（defer 里常见重复调用）
	if err := w.Close(); err != nil {
		t.Errorf("重复 Close 应幂等，实际: %v", err)
	}
}

func TestWriter自动建目录(t *testing.T) {
	base := t.TempDir()
	nested := filepath.Join(base, "a", "b", "c")
	w, err := NewWriter(nested, Meta{})
	if err != nil {
		t.Fatalf("多级目录应自动创建: %v", err)
	}
	defer w.Close()
	if _, err := os.Stat(filepath.Join(nested, "frames")); err != nil {
		t.Errorf("frames 目录未创建: %v", err)
	}
}

func TestKindName(t *testing.T) {
	cases := map[agent.ActionKind]string{
		agent.ActionTap:       "tap",
		agent.ActionSwipe:     "swipe",
		agent.ActionLongPress: "hold",
		agent.ActionJoystick:  "joy",
		agent.ActionKey:       "key",
		agent.ActionMove:      "move",
		agent.ActionPress:     "press",
		agent.ActionNone:      "none",
	}
	for k, want := range cases {
		if got := KindName(k); got != want {
			t.Errorf("KindName(%v)=%q, 期望 %q", k, got, want)
		}
	}
	// 反向钉死：**所有会被落盘的**动作类型都必须有名字，不许再出现「漏分支静默变 none」。
	// 这样以后新增 ActionKind 时，这条会在改 KindName 之前就红。
	// ActionMouseMove（L2 鼠标相对移动）不进示范数据集，故不在其列。
	recorded := []agent.ActionKind{
		agent.ActionNone, agent.ActionKey, agent.ActionMove, agent.ActionTap,
		agent.ActionSwipe, agent.ActionLongPress, agent.ActionJoystick, agent.ActionPress,
	}
	for _, k := range recorded {
		if got := KindName(k); got == "none" && k != agent.ActionNone {
			t.Errorf("动作类型 %v 落到了 default(\"none\")，KindName 漏了分支", k)
		}
	}
}

// 训练侧（Python）会按这个格式读 meta.json / trajectory.jsonl，
// 字段名一旦改动就是破坏性变更，这里把它钉住。
func Test数据集字段名稳定(t *testing.T) {
	dir := t.TempDir()
	w, _ := NewWriter(dir, Meta{Goal: "g", Model: "m", ScreenW: 2608, ScreenH: 1200,
		DownWidth: 160, DemoWidth: 1024, Live: true})
	_ = w.Step(agent.Action{Kind: agent.ActionTap, Nx: 0.5, Ny: 0.6},
		StepInfo{Parsed: true, LatencyMs: 1, OutTokens: 2, Raw: "r"}, nil, nil, false)
	_ = w.Close()

	line := firstLine(t, filepath.Join(dir, "trajectory.jsonl"))
	for _, key := range []string{
		"index", "at", "action", "kind", "nx", "ny", "parsed",
		"latency_ms", "out_tokens", "raw", "frame_gray",
	} {
		if !strings.Contains(line, `"`+key+`":`) {
			t.Errorf("trajectory.jsonl 缺少字段 %q：%s", key, line)
		}
	}

	meta := string(readAll(t, filepath.Join(dir, "meta.json")))
	for _, key := range []string{
		"goal", "model", "screen_w", "screen_h", "down_width",
		"demo_width", "started_at", "steps", "parsed_ok", "live",
	} {
		if !strings.Contains(meta, `"`+key+`":`) {
			t.Errorf("meta.json 缺少字段 %q", key)
		}
	}
}

func firstLine(t *testing.T, path string) string {
	t.Helper()
	f, err := os.Open(path)
	if err != nil {
		t.Fatalf("打开失败: %v", err)
	}
	defer f.Close()
	sc := bufio.NewScanner(f)
	if sc.Scan() {
		return sc.Text()
	}
	t.Fatal("文件为空")
	return ""
}

func readAll(t *testing.T, path string) []byte {
	t.Helper()
	b, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读取失败: %v", err)
	}
	return b
}
