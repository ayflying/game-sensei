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
	"github.com/ayflying/game-sensei/internal/game"
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

// 带游戏档案时，提示词必须由**档案**驱动：游戏名、界面先验、可用动作子集
// 全部来自 Profile。这是「不为单一游戏设计」的回归测试——
// 一旦有人把某款游戏的文案硬编码回本包，这条会立刻红。
func TestBuildDemoPrompt_带档案(t *testing.T) {
	prof, err := game.Load("nrc")
	if err != nil {
		t.Fatalf("加载 nrc 档案失败: %v", err)
	}
	d := &Demonstrator{
		Profile: prof,
		Goal:    "抓到一只水系精灵",
		Hints:   []string{"现场补充的先验"},
	}
	p := d.BuildDemoPrompt()

	for _, want := range []string{
		"洛克王国：世界", // 游戏名来自档案
		"抓到一只水系精灵",
		"现场补充的先验",      // 临时先验要合并进去
		"虚拟摇杆",         // 档案里的界面先验
		"ACTION MOVE",  // 档案配了移动 → 列出
		"ACTION PRESS", // 档案配了按钮 → 列出
		"star",         // 按钮名
		"0~1",
	} {
		if !strings.Contains(p, want) {
			t.Errorf("提示词缺少 %q\n---\n%s", want, p)
		}
	}
	// 摇杆四坐标写法绝不能出现在提示词里：实测模型会纠结其语义到打满预算
	if strings.Contains(p, "JOYSTICK") {
		t.Error("提示词不该出现 JOYSTICK 写法")
	}
	if strings.Contains(p, "【本次评估】") {
		t.Error("出动作提示词不应带评估模板")
	}
}

// 战斗态的提示词是「按态收窄」的关键落点：动作空间必须切到战斗项，
// 且绝不能出现 MOVE——战斗界面没有摇杆，列了只会让模型空耗步数。
func TestBuildDemoPrompt_战斗态只给战斗项且无MOVE(t *testing.T) {
	prof, err := game.Load("nrc")
	if err != nil {
		t.Fatalf("加载 nrc 档案失败: %v", err)
	}
	d := &Demonstrator{Profile: prof, Goal: "抓到一只稀有精灵", UIState: game.StateBattle}
	p := d.BuildDemoPrompt()

	for _, want := range []string{
		"cast_hetu",    // 技能宏才是模型该选的东西
		"battle_catch", // 捕捉钮
		"battle_flee",  // 逃跑钮
	} {
		if !strings.Contains(pressLine(p), want) {
			t.Errorf("战斗态 PRESS 候选缺少 %q\n---\n%s", want, pressLine(p))
		}
	}
	if strings.Contains(p, "ACTION MOVE") {
		t.Error("战斗态不该列出 MOVE（没有摇杆，给 MOVE 只会空耗一步）")
	}
	// 断言只盯「可用动作」里的 PRESS 候选行：界面先验（hints）是全局散文，
	// 天然会提到 star/mount，用它做断言会误报。真正要钉住的是**候选清单**不越界。
	line := pressLine(p)
	// hidden 中转钮绝不外泄：模型一旦看见 battle_skill/sk_*，就会退回「复读第一步」的老毛病
	for _, hidden := range []string{"battle_skill", "sk_"} {
		if strings.Contains(line, hidden) {
			t.Errorf("战斗态 PRESS 候选泄露了 hidden 中转钮 %q\n---\n%s", hidden, line)
		}
	}
	// 大世界按钮也不该进候选（在战斗界面它们不存在）
	for _, world := range []string{"star", "wolf", "run", "jump", "mount"} {
		if wordInList(line, world) {
			t.Errorf("战斗态 PRESS 候选混进了大世界按钮 %q\n---\n%s", world, line)
		}
	}
}

// pressLine 取出提示词里「ACTION PRESS name=<...>」那一行的候选清单。
func pressLine(prompt string) string {
	for _, ln := range strings.Split(prompt, "\n") {
		if strings.Contains(ln, "ACTION PRESS") {
			return ln
		}
	}
	return ""
}

// wordInList 判断 name 是否作为独立项出现在「a|b|c」清单里（避免 run 命中 run_demo 之类）。
func wordInList(list, name string) bool {
	for _, it := range strings.Split(list, "|") {
		it = strings.TrimSpace(it)
		if it == name {
			return true
		}
		if rest, ok := strings.CutPrefix(it, name); ok &&
			(strings.HasPrefix(rest, "(") || strings.HasPrefix(rest, "（")) {
			return true
		}
	}
	return false
}

// 没有档案时也要能跑，但动作空间必须收窄：不列 MOVE/PRESS。
// 没有可信坐标就列 PRESS，只会让模型瞎按。
func TestBuildDemoPrompt_无档案(t *testing.T) {
	d := &Demonstrator{Goal: "探索地图"}
	p := d.BuildDemoPrompt()
	for _, want := range []string{"ACTION TAP", "ACTION WAIT", "0~1"} {
		if !strings.Contains(p, want) {
			t.Errorf("通用协议缺少 %q", want)
		}
	}
	if strings.Contains(p, "ACTION MOVE") {
		t.Error("无档案时不该列出 MOVE（不知道摇杆在哪）")
	}
	if strings.Contains(p, "ACTION PRESS") {
		t.Error("无档案时不该列出 PRESS（没有可信按钮坐标）")
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

// ===== 复读/横跳 相关：本轮问题的回归测试 =====

// 2026-09-13 洛克王国 40 步实况的原始病象：up_right/up_left 交替、净位移≈0。
// 判据必须抓住它，同时对「健康的连续走」和「三个方向有序推进」保持沉默——
// 否则每次一走远就被判成打转，等于把赶路打断。
func TestDetectOscillation(t *testing.T) {
	const ur, ul = "move:up_right/500ms", "move:up_left/500ms"
	cases := []struct {
		name  string
		in    []string
		want  bool
		first string
	}{
		{"实况原样横跳", []string{ur, ur, ul, ul, ur, ul}, true, ur},
		{"严格交替", []string{ur, ul, ur, ul, ur, ul}, true, ur},
		{"同一方向连走", []string{ur, ur, ur, ur, ur, ur}, false, ""},
		{"同向走完换向走（正常赶路）", []string{ur, ur, ur, ul, ul, ul}, false, ""},
		{"三种以上动作轮流", []string{ur, ul, ur, "press:jump", ur, ul}, false, ""},
		{"不足窗口不判", []string{ur, ul, ur, ul, ur}, false, ""},
		{"只有两个动作但没打转", []string{ur, ur, ul, ul, ul, ul}, false, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			a, _, ok := DetectOscillation(tc.in)
			if ok != tc.want {
				t.Fatalf("判断=%v，期望 %v（输入 %v）", ok, tc.want, tc.in)
			}
			if ok && a != tc.first {
				t.Errorf("首个动作=%q，期望 %q", a, tc.first)
			}
		})
	}
}

// 移动（MOVE）是持续型动作，重复本身不等于无进展：走远本来就要连续走。
// 这条钉住「不许把重复禁令套到移动上」——正是它逼出 up_right/up_left 横跳。
func TestBuildDemoPrompt_移动重复不套禁令(t *testing.T) {
	d := &Demonstrator{
		PrevAction:  "move:up_right/500ms",
		PrevKind:    agent.ActionMove,
		RepeatCount: 5,
	}
	p := d.BuildDemoPrompt()
	if strings.Contains(p, "禁止再选它") {
		t.Errorf("移动被重复禁止了——这会把赶路逼成原地横跳\n---\n%s", p)
	}
	if !strings.Contains(p, "移动本来就会重复很多次") {
		t.Errorf("移动应给出「确认有没有真在前进」的指引\n---\n%s", p)
	}

	// 对照组：瞬时型动作（按钮）连按多次仍要禁止——实测它才是真复读。
	d2 := &Demonstrator{
		PrevAction:  "press:jump",
		PrevKind:    agent.ActionPress,
		RepeatCount: 5,
	}
	p2 := d2.BuildDemoPrompt()
	if !strings.Contains(p2, "禁止再选它") {
		t.Errorf("按钮连按 5 次必须禁止再选\n---\n%s", p2)
	}
}

// 最近动作列表与横跳告警：模型只看得见「最近几步」才可能发现自己打转。
func TestBuildDemoPrompt_最近动作与横跳告警(t *testing.T) {
	const ur, ul = "move:up_right/500ms", "move:up_left/500ms"
	d := &Demonstrator{
		PrevAction:    ul,
		PrevKind:      agent.ActionMove,
		RepeatCount:   1,
		RecentActions: []string{ur, ur, ul, ul, ur, ul},
	}
	p := d.BuildDemoPrompt()
	if !strings.Contains(p, "【你最近的动作】") {
		t.Fatalf("提示词缺少最近动作列表\n---\n%s", p)
	}
	// 列表要按「由早到晚」原样摊开，不能只留最后一条
	if !strings.Contains(p, ur+" → "+ur+" → "+ul) {
		t.Errorf("最近动作没有按顺序完整列出\n---\n%s", p)
	}
	for _, want := range []string{"横跳", ur, ul} {
		if !strings.Contains(p, want) {
			t.Errorf("横跳告警缺少 %q", want)
		}
	}

	// 没打转时不许出现横跳告警（否则等于每步都在喊狼来了）
	d2 := &Demonstrator{RecentActions: []string{ur, ur, ur, ur, ur, ur}}
	if strings.Contains(d2.BuildDemoPrompt(), "横跳") {
		t.Error("同向连走不该被判成横跳")
	}
}

// 列表要截断到上限：无限累积既费 token，也会用早期无关动作干扰判断。
func TestBuildDemoPrompt_最近动作截断(t *testing.T) {
	recent := make([]string, 0, 20)
	for i := 0; i < 20; i++ {
		recent = append(recent, "move:up/"+string(rune('a'+i)))
	}
	d := &Demonstrator{RecentActions: recent}
	p := d.BuildDemoPrompt()

	head := recent[20-recentActionPromptMax]
	if !strings.Contains(p, head) {
		t.Errorf("应保留最近 %d 条里的第一条 %q", recentActionPromptMax, head)
	}
	if strings.Contains(p, recent[0]) {
		t.Errorf("最早的动作 %q 应被截掉", recent[0])
	}
}
