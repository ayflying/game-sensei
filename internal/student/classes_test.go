package student_test

// 学生动作空间与 L1 的一致性守卫。
//
// 这组测试存在的理由（都是实际踩过的坑）：
//
//	1) internal/agent 的 L1 动作空间是 8 向（AllDirs），学生头曾经只有 4 向，
//	   注释却写着「方向 8 选 1」——设计意图与实现悄悄漂移，没人发现。
//	   结果是老师发出的斜向移动（实测真机示范**全是斜向**）无法表达，
//	   只能在训练器里被硬塞成正向，等于用错标样本训练。
//	2) 类别顺序若在两侧不一致，权重加载后 logits 与标签错位，模型照跑不误
//	   ——「不报错但决策全错」。所以 Load 必须拒绝顺序不一致的权重。

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"

	"github.com/ayflying/game-sensei/internal/agent"
	"github.com/ayflying/game-sensei/internal/student"
)

// TestClasses与L1方向对齐 保证学生的方向类就是 L1 的全部 8 向（集合相同）。
func TestClasses与L1方向对齐(t *testing.T) {
	got := map[string]bool{}
	for _, c := range student.Classes {
		if got[c] {
			t.Fatalf("学生类别重复: %q", c)
		}
		got[c] = true
	}

	for _, d := range agent.AllDirs {
		if !got[string(d)] {
			t.Fatalf("L1 方向 %q 在学生类别里缺失——学生无法表达该方向", d)
		}
	}

	// 非方向类：学生头独有的语义动作（L1 里对应 Tap/Press/None）。
	// 4 个端到端语义类（2026-09-16）：tap_pk/tap_start/tap_pick 由档案
	// student_semantics 落地坐标，back 落成系统返回键。
	for _, c := range []string{"tap", "press", "wait", "none",
		"tap_pk", "tap_start", "tap_pick", "back"} {
		if !got[c] {
			t.Fatalf("学生类别缺少 %q", c)
		}
	}

	want := len(agent.AllDirs) + 8
	if len(student.Classes) != want {
		t.Fatalf("学生类别数 %d，期望 %d（8 向 + tap/press/wait/none + 4 语义类）",
			len(student.Classes), want)
	}
}

// TestLoad拒绝类别顺序不一致 保证顺序漂移会在加载期被挡掉而不是静默错标。
func TestLoad拒绝类别顺序不一致(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bad.weights.json")

	// 类别集合相同但顺序被打乱（把 up 和 down 对调）
	bad := append([]string{}, student.Classes...)
	bad[0], bad[1] = bad[1], bad[0]

	hidden := 64
	zeros := func(n int) []float32 { return make([]float32, n) }
	build := func(classes []string) map[string]any {
		return map[string]any{
			"format":  "game-sensei-student-weights",
			"version": 1,
			"arch":    map[string]any{"hidden": hidden, "classes": classes},
			"weights": map[string][]float32{
				"conv1_w": zeros(8 * 1 * 9), "conv1_b": zeros(8),
				"conv2_w": zeros(16 * 8 * 9), "conv2_b": zeros(16),
				"conv3_w": zeros(24 * 16 * 9), "conv3_b": zeros(24),
				"fc_w": zeros(hidden * 24), "fc_b": zeros(hidden),
				"cls_w": zeros(len(student.Classes) * hidden), "cls_b": zeros(len(student.Classes)),
				"coord_w": zeros(2 * hidden), "coord_b": zeros(2),
			},
			"meta": map[string]any{"val_acc": 0.5},
		}
	}

	write := func(v any) {
		raw, err := json.Marshal(v)
		if err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(path, raw, 0o644); err != nil {
			t.Fatal(err)
		}
	}

	write(build(bad))
	if _, err := student.Load(path); err == nil {
		t.Fatal("类别顺序不一致的权重竟然加载成功——会导致 logits 与标签错位且无告警")
	}

	write(build(student.Classes))
	if _, err := student.Load(path); err != nil {
		t.Fatalf("顺序一致的权重应当能加载，却报错: %v", err)
	}
}
