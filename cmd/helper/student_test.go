package main

// 适配层穷举守卫：学生能输出的每一个类别都必须有明确的 L1 动作落点。
//
// 为什么值得单独测：适配层的 default 分支曾经把「未知类别」和
// wait/none 合并处理，都是「什么都不做」。于是权重类别与代码不一致时，
// 症状表现成「学生有点笨」，而不是一个能定位的错误。

import (
	"testing"

	"github.com/ayflying/game-sensei/internal/agent"
	"github.com/ayflying/game-sensei/internal/game"
	"github.com/ayflying/game-sensei/internal/student"
)

// semanticProfile 是配了 student_semantics 的最小档案：
// 三个语义类各指向一个按钮（坐标任意但可辨认，用于断言「落地的是档案坐标」）。
func semanticProfile() *game.Profile {
	return &game.Profile{
		Buttons: []game.Button{
			{Name: "pk_contest", Pos: [2]float64{0.678, 0.845}},
			{Name: "start", Pos: [2]float64{0.5, 0.794}},
			{Name: "pick_mid", Pos: [2]float64{0.5, 0.782}},
		},
		StudentSemantics: map[string]string{
			"tap_pk": "pk_contest", "tap_start": "start", "tap_pick": "pick_mid",
		},
	}
}

// TestClassToAction穷举全部类别 保证没有类别落进「未识别」分支。
//
// 语义类（tap_pk/tap_start/tap_pick）的落点是档案知识（student_semantics 映射到
// 按钮），所以穷举必须给配好映射的档案；「档案没配」这个负例由
// TestClassToAction语义类缺档案映射不瞎点 单独固化（ok=false，不静默退化）。
func TestClassToAction穷举全部类别(t *testing.T) {
	prof := semanticProfile()
	for _, c := range student.Classes {
		if _, ok := classToAction(c, 0.5, 0.5, prof, 0); !ok {
			t.Fatalf("类别 %q 没有映射到 L1 动作（会静默退化成 WAIT）", c)
		}
	}
	// 真·未知类别必须被识别为未知，而不是悄悄当 WAIT
	if _, ok := classToAction("bogus", 0.5, 0.5, prof, 0); ok {
		t.Fatal("未知类别应当返回 ok=false")
	}
}

// TestClassToAction语义类坐标来自档案 保证语义类落地的是**档案坐标**——
// 学生回归头对语义类不被消费（它只管看画面说「点哪个语义按钮」）。
func TestClassToAction语义类坐标来自档案(t *testing.T) {
	prof := semanticProfile()
	// 刻意给一个错误的回归坐标，断言它不被采用
	act, ok := classToAction("tap_pk", 0.1, 0.2, prof, 0)
	if !ok {
		t.Fatal("tap_pk 未被识别")
	}
	if act.Kind != agent.ActionTap || act.Nx != 0.678 || act.Ny != 0.845 {
		t.Fatalf("tap_pk 落点 = (%v, %.3f, %.3f)，期望 (Tap, 0.678, 0.845) 即档案坐标",
			act.Kind, act.Nx, act.Ny)
	}

	// back：系统返回键（导航广告场景）
	act, ok = classToAction("back", 0, 0, prof, 0)
	if !ok || act.Kind != agent.ActionKey || act.Code != "back" {
		t.Fatalf("back 落点 = (%v,%q)，期望 (ActionKey, back)", act.Kind, act.Code)
	}
}

// TestClassToAction语义类缺档案映射不瞎点 固化「宁可不点，不能点错」：
// 档案没配 student_semantics（或按钮名拼错）时必须 ok=false，由 Decide 打可见警告。
func TestClassToAction语义类缺档案映射不瞎点(t *testing.T) {
	profs := map[string]*game.Profile{
		"nil 档案":                  nil,
		"空档案":                     {},
		"有按钮但无 student_semantics": {Buttons: []game.Button{{Name: "pk_contest", Pos: [2]float64{0.678, 0.845}}}},
		"映射指向不存在的按钮":              {StudentSemantics: map[string]string{"tap_pk": "nope"}},
	}
	for name, prof := range profs {
		if _, ok := classToAction("tap_pk", 0.5, 0.5, prof, 0); ok {
			t.Fatalf("%s：tap_pk 竟然可落地（会点到错误坐标或空点）", name)
		}
	}
}

// TestClassToAction8向落成Move 保证 8 个方向都被翻译成 ActionMove 且方向保真。
func TestClassToAction8向落成Move(t *testing.T) {
	prof := &game.Profile{}
	for _, d := range agent.AllDirs {
		act, ok := classToAction(string(d), 0, 0, prof, 0)
		if !ok {
			t.Fatalf("方向 %q 未被识别", d)
		}
		if act.Kind != agent.ActionMove {
			t.Fatalf("方向 %q 的动作类型是 %v，期望 Move", d, act.Kind)
		}
		if act.Dir != d {
			t.Fatalf("方向 %q 被翻成 %q（方向丢失）", d, act.Dir)
		}
		if act.Dur <= 0 {
			t.Fatalf("方向 %q 的持续时长为 %v，会导致零位移", d, act.Dur)
		}
	}
}

// TestPress退回档案首个按钮 固化当前已知天花板的行为，避免无意变更。
// 学术上的正解是「按钮头」，但那是下一版；在换掉之前，行为要稳定可见。
func TestPress退回档案首个按钮(t *testing.T) {
	prof := &game.Profile{Buttons: []game.Button{{Name: "gather_energy"}, {Name: "cast_hetu"}}}
	act, ok := classToAction("press", 0, 0, prof, 0)
	if !ok {
		t.Fatal("press 未被识别")
	}
	if act.Kind != agent.ActionPress || act.Name != "gather_energy" {
		t.Fatalf("press 落点 = (%v,%q)，期望 (Press,gather_energy)", act.Kind, act.Name)
	}

	// 无档案/无按钮时不得瞎按
	act, _ = classToAction("press", 0, 0, nil, 0)
	if act.Kind != agent.ActionNone {
		t.Fatalf("无档案时 press 应退化为 None，实际 %v", act.Kind)
	}
}

// TestSemanticSlots 固化 student_semantics 多槽位解析：单值=单槽（旧行为），
// `|` 分隔=多槽（轮换补点用），未配置=nil（不可落地）。
func TestSemanticSlots(t *testing.T) {
	prof := &game.Profile{
		StudentSemantics: map[string]string{
			"tap_pk":   "pk_contest",
			"tap_pick": "pick_left|pick_mid|pick_right",
		},
	}
	if got := prof.SemanticSlots("tap_pk"); len(got) != 1 || got[0] != "pk_contest" {
		t.Fatalf("单值映射应得 1 槽 [pk_contest]，实际 %v", got)
	}
	got := prof.SemanticSlots("tap_pick")
	if len(got) != 3 || got[0] != "pick_left" || got[1] != "pick_mid" || got[2] != "pick_right" {
		t.Fatalf("多槽位映射应得 3 槽，实际 %v", got)
	}
	if s := prof.SemanticSlots("tap_start"); s != nil {
		t.Fatalf("未配置的语义类应得 nil，实际 %v", s)
	}
}

// pickProfile 是多槽位轮换测试用的最小档案：三张选装卡（坐标与 yoyastar.json 一致）。
func pickProfile() *game.Profile {
	return &game.Profile{
		Buttons: []game.Button{
			{Name: "pick_left", Pos: [2]float64{0.224, 0.782}},
			{Name: "pick_mid", Pos: [2]float64{0.5, 0.782}},
			{Name: "pick_right", Pos: [2]float64{0.776, 0.782}},
		},
		StudentSemantics: map[string]string{
			"tap_pick": "pick_left|pick_mid|pick_right",
		},
	}
}

// tapXOf 把 slot 换算成 classToAction 的落点 x，顺便校验动作类型可落地。
func tapXOf(t *testing.T, prof *game.Profile, slot int) float64 {
	t.Helper()
	act, ok := classToAction("tap_pick", 0, 0, prof, slot)
	if !ok || act.Kind != agent.ActionTap {
		t.Fatalf("slot=%d 的 tap_pick 不可落地或类型错误（%v, ok=%v）", slot, act.Kind, ok)
	}
	return act.Nx
}

// TestStudentActor点击闭环轮换补点 固化行为级「点了没点上」检测：
// 同一槽位最多连点 pickRetryStreak 次 tap_pick，之后轮换下一张卡
// （修「永远点中间一张」的漏点：首击被吞、或该点的装扮不在固定坐标时，
// 轮换总会试到其它卡）；输出任何其他类别 ⇒ 计数与槽位复位回首槽。
func TestStudentActor点击闭环轮换补点(t *testing.T) {
	prof := pickProfile()
	a := &studentActor{prof: prof}

	// 槽位推进序列：每槽 pickRetryStreak 击后换下一槽，右卡之后绕回左卡（取模）。
	wantSlots := []int{0, 0, 0, 1, 1, 1, 2, 2, 2, 0, 0}
	wantX := map[int]float64{0: 0.224, 1: 0.5, 2: 0.776}
	for i, want := range wantSlots {
		if got := a.nextSlot("tap_pick"); got != want {
			t.Fatalf("第 %d 步 tap_pick 槽位 = %d，期望 %d", i+1, got, want)
		}
		if x := tapXOf(t, prof, want); x != wantX[want] {
			t.Fatalf("槽位 %d 落点 x = %.3f，期望 %.3f", want, x, wantX[want])
		}
	}

	// 场景推进即复位：连点 2 次后输出一次 tap_start（离开选装页），
	// 再回到 tap_pick 应从首槽重新计数（而不是接着上一轮轮换）。
	a2 := &studentActor{prof: prof}
	if s := a2.nextSlot("tap_pick"); s != 0 {
		t.Fatalf("首击槽位应为 0，实际 %d", s)
	}
	if s := a2.nextSlot("tap_pick"); s != 0 {
		t.Fatalf("第 2 击槽位应为 0，实际 %d", s)
	}
	if s := a2.nextSlot("tap_start"); s != 0 {
		t.Fatalf("非 tap_pick 步应复位并返回 0，实际 %d", s)
	}
	if s := a2.nextSlot("tap_pick"); s != 0 {
		t.Fatalf("复位后的 tap_pick 应回首槽 0（重新计数），实际 %d", s)
	}
}
