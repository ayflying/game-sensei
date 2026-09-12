package main

import (
	"image"
	"testing"

	"github.com/ayflying/game-sensei/internal/agent"
	"github.com/ayflying/game-sensei/internal/game"
)

// 这一组钉住「息屏黑帧」的两个纯函数。它们的意义在于：黑帧如果没被拦住，
// 卡死判据说「跨窗口画面零变化」，会把一次息屏判成「角色被钉住」，
// 然后在黑屏上连着跑好几轮脱困（传送/摇杆长推），全程静默无效。
// 详见 demo.go 里 blackFrameMean 的注释。

func grayFramePlain(w, h int, v uint8) *image.Gray {
	g := image.NewGray(image.Rect(0, 0, w, h))
	for i := range g.Pix {
		g.Pix[i] = v
	}
	return g
}

func TestImageMeanGray(t *testing.T) {
	// 全黑帧（息屏）必须落在阈值以下
	black := imageMeanGray(grayFramePlain(160, 120, 0))
	if black >= blackFrameMean {
		t.Errorf("全黑帧均值 %.2f 应低于阈值 %.1f", black, blackFrameMean)
	}
	// 真实游戏画面（大世界/战斗）在 60 以上，必须远高于阈值
	lit := imageMeanGray(grayFramePlain(160, 120, 180))
	if lit <= blackFrameMean {
		t.Errorf("正常亮帧均值 %.2f 不该被判成黑屏（阈值 %.1f）", lit, blackFrameMean)
	}
	if lit < 60 {
		t.Errorf("亮帧均值 %.2f 异常，样本构造有误", lit)
	}
	// 边界：刚好等于阈值不算黑（用 < 判定）
	if imageMeanGray(grayFramePlain(8, 8, 12)) < blackFrameMean {
		t.Error("均值等于阈值时不应判为黑屏")
	}
	// nil / 空图必须安全返回 255（宁可漏判一次黑屏，也不要在无帧时打乱主循环）
	if got := imageMeanGray(nil); got != 255 {
		t.Errorf("nil 应返回 255，得到 %.1f", got)
	}
	if got := imageMeanGray(image.NewGray(image.Rect(0, 0, 0, 0))); got != 255 {
		t.Errorf("空图应返回 255，得到 %.1f", got)
	}
	// 非全黑但偏暗的画面（夜晚草地）不该被误判：用 40 模拟
	if imageMeanGray(grayFramePlain(64, 64, 40)) < blackFrameMean {
		t.Error("偏暗但可见的夜景不应被判成黑屏")
	}
}

func TestFrameDiff(t *testing.T) {
	a := grayFramePlain(32, 32, 100)
	// 完全相同 → 0
	if d := frameDiff(a, a); d != 0 {
		t.Errorf("同帧差应为 0，得到 %.2f", d)
	}
	// 全黑 vs 全白 → 255
	if d := frameDiff(grayFramePlain(32, 32, 0), grayFramePlain(32, 32, 255)); d != 255 {
		t.Errorf("黑白对差应为 255，得到 %.2f", d)
	}
	// 尺寸不一致 / nil → 255（视为变化很大，宁可漏判一次卡死）
	if d := frameDiff(nil, a); d != 255 {
		t.Errorf("nil 帧应返回 255，得到 %.2f", d)
	}
	if d := frameDiff(a, grayFramePlain(16, 16, 100)); d != 255 {
		t.Errorf("尺寸不一致应返回 255，得到 %.2f", d)
	}
}

// escapeToScript 是脱困脚本与技能宏共用执行内核的桥：字段必须一一搬对，
// 否则脱困会点错位置（这类错误在黑屏/脱困场景里极难排查）。
func TestEscapeToScript(t *testing.T) {
	in := []game.EscapeStep{
		{Pos: [2]float64{0.1, 0.2}, WaitMs: 1000, Note: "第一步"},
		{Pos: [2]float64{0.3, 0.4}, To: [2]float64{0.5, 0.6}, DragMs: 700, WaitMs: 2000},
	}
	out := escapeToScript(in)
	if len(out) != len(in) {
		t.Fatalf("步数 %d != %d", len(out), len(in))
	}
	for i := range in {
		if out[i].Pos != in[i].Pos || out[i].To != in[i].To ||
			out[i].DragMs != in[i].DragMs || out[i].WaitMs != in[i].WaitMs ||
			out[i].Note != in[i].Note {
			t.Errorf("第 %d 步字段没搬对:\n  in =%+v\n  out=%+v", i+1, in[i], out[i])
		}
	}
}

// appendRecent 维护「最近 N 步动作」历史。它是横跳检测与提示词的唯一数据源，
// 两条约束必须同时成立：只留最近 N 条（不漏、不多），且不与入参共享底层数组
// ——共享会让老师手里的历史被后续 append 追改，出问题时几乎无法复现。
func TestAppendRecent(t *testing.T) {
	var h []string
	for i := 0; i < 3; i++ {
		h = appendRecent(h, string(rune('a'+i)), 3)
	}
	if len(h) != 3 || h[0] != "a" || h[2] != "c" {
		t.Fatalf("未满容量时应顺序累积，得到 %v", h)
	}
	// 越界后只保留最后 capN 条（最老的被挤掉）
	h = appendRecent(h, "d", 3)
	want := []string{"b", "c", "d"}
	for i, w := range want {
		if h[i] != w {
			t.Fatalf("超容量后应只留最近 3 条，得到 %v，期望 %v", h, want)
		}
	}
	// capN<=0 退化为「只留最新一条」，不该 panic
	if got := appendRecent([]string{"x"}, "y", 0); len(got) != 1 || got[0] != "y" {
		t.Errorf("capN=0 应只留最新一条，得到 %v", got)
	}
	// 不共享底层数组：故意给一个「有富余容量」的切片（naive 的 append 实现
	// 会原地复用它），改写返回值里的元素绝不能波及入参。
	spare := make([]string, 2, 8)
	spare[0], spare[1] = "p", "q"
	out := appendRecent(spare, "r", 8)
	out[0] = "CHANGED"
	if spare[0] != "p" || spare[1] != "q" {
		t.Errorf("appendRecent 与入参共享底层数组，改写结果污染了入参 %v", spare)
	}
}

// moveDirOf 给复读兜底用：卡住时要换到**另一个**方向，先得从动作串里认出当前方向。
// 认错方向的后果是「强制换向」换成同一个方向——比不换更糟（白送一步还照撞）。
func TestMoveDirOf(t *testing.T) {
	cases := []struct {
		in   string
		want string
	}{
		{"move:up_right/1500ms", "up_right"},
		{"move:down/500ms", "down"},
		{"move:left", "left"},
		{"press:star", ""},
		{"press:cast_hetu", ""},
		{"", ""},
		{"none", ""},
	}
	for _, tc := range cases {
		if got := moveDirOf(tc.in); got != tc.want {
			t.Errorf("moveDirOf(%q)=%q，期望 %q", tc.in, got, tc.want)
		}
	}
	// 复读兜底要挑的方向必须与当前方向不同，否则等于没换方向（白送一步还照撞）。
	// 前提一：escapeDirs 里至少要有两个互不相同的方向，换向循环才可能避开当前方向。
	seen := map[string]bool{}
	for _, d := range escapeDirs {
		if seen[string(d)] {
			t.Errorf("escapeDirs 有重复方向 %q，换向循环可能避不开当前方向", d)
		}
		seen[string(d)] = true
	}
	if len(seen) < 2 {
		t.Fatalf("escapeDirs 只有 %d 个方向，无法换到不同方向", len(seen))
	}
	// 前提二：每个 escapeDir 经 moveDirOf 必须能原样认回，否则兜底会把「当前方向」
	// 认成别的、于是“换向”换到同一个方向。
	for _, d := range escapeDirs {
		if got := moveDirOf("move:" + string(d) + "/1500ms"); got != string(d) {
			t.Errorf("moveDirOf 认不回 escapeDir %q（得到 %q）", d, got)
		}
	}
}

// moveStallStep 是「顶着墙走」的唯一量化信号，错一格就会让整轮采集静默空耗
// （pet_run12 连打 43 步 move:up_right 而无人察觉）。逐条钉死：
// 只有「上一步是移动 + 本步 Δ 小 + 方向没变」才累加。
func TestMoveStallStep(t *testing.T) {
	const small, big = 1.0, 9.9
	cases := []struct {
		name     string
		prevKind agent.ActionKind
		diff     float64
		prevDir  string
		streak   int
		dir      string
		wantN    int
		wantDir  string
	}{
		{"非移动动作清零", agent.ActionPress, small, "up_right", 3, "up_right", 0, ""},
		{"Δ 偏大不算停滞", agent.ActionMove, big, "up_right", 2, "up_right", 0, ""},
		{"方向未知（无法判定）清零", agent.ActionMove, small, "", 2, "", 0, ""},
		{"首次同向小 Δ 起算", agent.ActionMove, small, "up_right", 0, "", 1, "up_right"},
		{"同向再走一步累加", agent.ActionMove, small, "up_right", 1, "up_right", 2, "up_right"},
		{"换方向后重新计数", agent.ActionMove, small, "down", 3, "up_right", 1, "down"},
		{"Δ 恰等于阈值不算停滞", agent.ActionMove, moveStallDiffEps, "up_right", 5, "up_right", 0, ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			n, d := moveStallStep(tc.prevKind, tc.diff, tc.prevDir, tc.streak, tc.dir)
			if n != tc.wantN || d != tc.wantDir {
				t.Errorf("moveStallStep(%v, %.1f, %q, %d, %q) = (%d, %q)，期望 (%d, %q)",
					tc.prevKind, tc.diff, tc.prevDir, tc.streak, tc.dir, n, d, tc.wantN, tc.wantDir)
			}
		})
	}
}
