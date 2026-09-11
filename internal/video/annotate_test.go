package video

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestBuildClipPrompt(t *testing.T) {
	spec := ClipSpec{
		DurSec: 22,
		Frames: []FrameRef{
			{Path: "a.jpg", OffsetSec: 0},
			{Path: "b.jpg", OffsetSec: 9},
			{Path: "c.jpg", OffsetSec: 21},
		},
		Game:    "洛克王国：世界",
		Goal:    "捕捉精灵",
		Buttons: []string{"star(交互)", "wolf(面板)"},
		Hints:   []string{"左下角是虚拟摇杆"},
	}
	p := BuildClipPrompt(spec)

	t.Run("交代帧的时间顺序与跨度", func(t *testing.T) {
		// 不给时间信息，模型不知道哪张在前、这段跨了多久，
		// 会把「朝目标走了 20 秒」读成「在原地站了一会儿」。
		for _, want := range []string{
			"第 1 张", "第 2 张", "第 3 张",
			"片段开始", "片段中间", "片段结束",
			"22 秒",
		} {
			if !strings.Contains(p, want) {
				t.Errorf("提示词缺少 %q", want)
			}
		}
	})

	t.Run("带上游戏名、目标与按钮名", func(t *testing.T) {
		// 不给按钮名，产出的策略只会是「点右下角那个圆按钮」，没法执行。
		for _, want := range []string{"洛克王国：世界", "捕捉精灵", "star(交互)", "wolf(面板)"} {
			if !strings.Contains(p, want) {
				t.Errorf("提示词缺少 %q", want)
			}
		}
	})

	t.Run("要求直接给结论", func(t *testing.T) {
		// qwen3 的思考链会吃光输出预算导致正文为空，必须明说不要解释推理过程。
		if !strings.Contains(p, "不要解释推理过程") {
			t.Error("提示词应明确要求跳过推理过程直接给结论")
		}
	})

	t.Run("约束结论必须来自画面", func(t *testing.T) {
		// 没有这条约束时，档案 hints 里的「点任务追踪文字可自动寻路」
		// 会被照搬进几乎每一段的策略里，连过场动画段也不例外。
		if !strings.Contains(p, "必须来自这几张画面自身的差异") {
			t.Error("提示词应约束结论来自画面差异，而不是照搬已知界面信息")
		}
		if !strings.Contains(p, "不要把这些说法直接当作结论") {
			t.Error("提示词应说明界面信息不可直接当作结论")
		}
	})

	t.Run("允许模型如实说没有可学的操作", func(t *testing.T) {
		// 过场动画段本来就无操作可学，不给这条出口它会硬编一条策略出来。
		if !strings.Contains(p, "本段无操作可学") {
			t.Error("提示词应允许模型如实标注「本段无操作可学」")
		}
	})

	t.Run("格式行里不含具体数值", func(t *testing.T) {
		// 回归防线：小模型会把示例逐字节照抄。早期动作协议里写了
		// `cx=0.21 cy=0.69`，模型连续 8 步输出与示例完全一致，
		// 看着像「不会决策」，实际是「抄了示例」。
		for _, line := range strings.Split(p, "\n") {
			for _, label := range []string{"局面：", "操作：", "策略："} {
				if strings.HasPrefix(line, label) && strings.ContainsAny(line, "0123456789") {
					t.Errorf("格式行 %q 含具体数值，会被模型照抄", line)
				}
			}
		}
	})

	t.Run("未指定游戏名时有兜底", func(t *testing.T) {
		empty := BuildClipPrompt(ClipSpec{DurSec: 5, Frames: []FrameRef{{Path: "a.jpg"}}})
		if !strings.Contains(empty, "某款手机游戏") {
			t.Error("未指定游戏名时应有兜底称呼")
		}
	})
}

func TestParseAnnotation(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want Annotation
	}{
		{
			name: "标准三行",
			in:   "局面：角色站在草地上\n操作：MOVE forward\n策略：目标在远处时持续前进",
			want: Annotation{
				Observation: "角色站在草地上",
				Action:      "MOVE forward",
				Strategy:    "目标在远处时持续前进",
			},
		},
		{
			name: "英文冒号",
			in:   "局面: 角色站在草地上\n操作: TAP star\n策略: 靠近后交互",
			want: Annotation{
				Observation: "角色站在草地上",
				Action:      "TAP star",
				Strategy:    "靠近后交互",
			},
		},
		{
			name: "带项目符号与加粗标签",
			in:   "- **局面**：角色站在草地上\n- **操作**：TAP star\n- **策略**：靠近后交互",
			want: Annotation{
				Observation: "角色站在草地上",
				Action:      "TAP star",
				Strategy:    "靠近后交互",
			},
		},
		{
			name: "三项挤在同一行",
			in:   "局面：角色站在草地上 操作：TAP star 策略：靠近后交互",
			want: Annotation{
				Observation: "角色站在草地上",
				Action:      "TAP star",
				Strategy:    "靠近后交互",
			},
		},
		{
			name: "近义标签",
			in:   "画面：标题界面\n动作：无\n建议：本段无操作可学",
			want: Annotation{
				Observation: "标题界面",
				Action:      "无",
				Strategy:    "本段无操作可学",
			},
		},
		{
			name: "字段名出现在正文里时不误判",
			in:   "策略：本段无操作可学",
			want: Annotation{Strategy: "本段无操作可学"},
		},
		{
			name: "只有部分字段",
			in:   "局面：角色站在草地上",
			want: Annotation{Observation: "角色站在草地上"},
		},
		{
			name: "空输出",
			in:   "",
			want: Annotation{},
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := ParseAnnotation(c.in)
			if got.Observation != c.want.Observation {
				t.Errorf("局面 = %q，期望 %q", got.Observation, c.want.Observation)
			}
			if got.Action != c.want.Action {
				t.Errorf("操作 = %q，期望 %q", got.Action, c.want.Action)
			}
			if got.Strategy != c.want.Strategy {
				t.Errorf("策略 = %q，期望 %q", got.Strategy, c.want.Strategy)
			}
		})
	}
}

func TestParseAnnotation_KeepsRaw(t *testing.T) {
	// 解析失败时只能靠原文排查，原文必须原样保留。
	raw := "局面：角色站在草地上\n操作：TAP star\n策略：靠近后交互"
	if got := ParseAnnotation(raw); got.Raw != raw {
		t.Errorf("Raw 应保留老师原文，得到 %q", got.Raw)
	}
}

func TestParseAnnotation_DoesNotFabricate(t *testing.T) {
	// 老师答非所问时，三个字段应当留空——宁可空着也不能编内容，
	// 否则报告里会出现看起来合理但根本没人说过的「策略」。
	got := ParseAnnotation("我无法判断这个画面。")
	if got.Observation != "" || got.Action != "" || got.Strategy != "" {
		t.Errorf("无法解析时三个字段都应留空，得到 %+v", got)
	}
}

func TestLoadFrames(t *testing.T) {
	t.Run("空列表报错", func(t *testing.T) {
		if _, err := loadFrames(nil); err == nil {
			t.Error("没有送审帧时应报错")
		}
	})

	t.Run("文件缺失时报错", func(t *testing.T) {
		_, err := loadFrames([]FrameRef{{Path: filepath.Join(t.TempDir(), "没有.jpg")}})
		if err == nil {
			t.Error("帧文件不存在时应报错")
		}
	})

	t.Run("按顺序读出字节", func(t *testing.T) {
		dir := t.TempDir()
		a := filepath.Join(dir, "a.jpg")
		b := filepath.Join(dir, "b.jpg")
		if err := os.WriteFile(a, []byte("AAA"), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(b, []byte("BBB"), 0o644); err != nil {
			t.Fatal(err)
		}
		got, err := loadFrames([]FrameRef{{Path: a}, {Path: b}})
		if err != nil {
			t.Fatalf("loadFrames 失败: %v", err)
		}
		if len(got) != 2 || string(got[0]) != "AAA" || string(got[1]) != "BBB" {
			t.Errorf("帧顺序必须与传入一致，得到 %q", got)
		}
	})
}

func TestPositionLabel(t *testing.T) {
	cases := []struct {
		i, n int
		want string
	}{
		{0, 1, "唯一一张"},
		{0, 3, "片段开始"},
		{1, 3, "片段中间"},
		{2, 3, "片段结束"},
	}
	for _, c := range cases {
		if got := positionLabel(c.i, c.n); got != c.want {
			t.Errorf("positionLabel(%d, %d) = %q，期望 %q", c.i, c.n, got, c.want)
		}
	}
}
