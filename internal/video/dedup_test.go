package video

import (
	"image"
	"image/color"
	"testing"
)

// scene 生成一幅中低频结构的测试画面：水平渐变底 + 中心亮盘 + 右侧暗条。
//
// 选这种结构而不是随机噪声或棋盘：它有明确的几何布局，缩放后
// 各元素的位置与占比不变，正好用来验证 dHash 的尺度鲁棒性；
// 棋盘那种高频图案在缩放时会混叠，哈希抖动属于正常现象，
// 拿它做断言只会得到一个脆弱且没有意义的测试。
func scene(w, h int) image.Image {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	cx, cy, r := w/2, h/2, h/4
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			v := uint8(30 + 60*x/w) // 水平渐变底
			dx, dy := x-cx, y-cy
			if dx*dx+dy*dy < r*r {
				v = 230 // 中心亮盘
			}
			if x > w*3/4 && x < w*4/5 {
				v = 10 // 右侧暗条
			}
			img.Set(x, y, color.RGBA{R: v, G: v, B: v, A: 255})
		}
	}
	return img
}

// hgrad 生成纯水平渐变（左暗右亮）。
func hgrad(w, h int) image.Image {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			v := uint8(x * 255 / w)
			img.Set(x, y, color.RGBA{R: v, G: v, B: v, A: 255})
		}
	}
	return img
}

// solid 生成纯色画面。
func solid(w, h int, v uint8) image.Image {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.Set(x, y, color.RGBA{R: v, G: v, B: v, A: 255})
		}
	}
	return img
}

func TestDHash_Basic(t *testing.T) {
	t.Run("水平渐变每一位都亮于右侧", func(t *testing.T) {
		if got := dHash(hgrad(320, 180)); got != ^uint64(0) {
			t.Errorf("单调递增渐变的哈希应为全 1，得到 %#x", got)
		}
	})

	t.Run("纯色画面没有明暗关系", func(t *testing.T) {
		if got := dHash(solid(320, 180, 128)); got != 0 {
			t.Errorf("纯色画面的哈希应为 0，得到 %#x", got)
		}
	})

	t.Run("nil 图返回 0 而不 panic", func(t *testing.T) {
		if got := dHash(nil); got != 0 {
			t.Errorf("nil 图的哈希应为 0，得到 %#x", got)
		}
	})

	t.Run("不同结构的画面哈希不同", func(t *testing.T) {
		d := Hamming(dHash(scene(640, 360)), dHash(solid(640, 360, 128)))
		if d < 8 {
			t.Errorf("有结构画面与纯色画面的距离应明显大于 0，得到 %d", d)
		}
	})
}

func TestDHash_Robustness(t *testing.T) {
	t.Run("整体提亮不改变哈希", func(t *testing.T) {
		// dHash 比较的是相邻像素的相对明暗，整体加常数不该改变任何一位。
		// 这正是它比「画面均值」更适合判断变化的原因：均值会被亮度变化带跑。
		base := scene(640, 360)
		brighter := image.NewRGBA(base.Bounds())
		for y := base.Bounds().Min.Y; y < base.Bounds().Max.Y; y++ {
			for x := base.Bounds().Min.X; x < base.Bounds().Max.X; x++ {
				r, g, b, _ := base.At(x, y).RGBA()
				clamp := func(v uint32) uint8 {
					n := int(v>>8) + 12
					if n > 255 {
						n = 255
					}
					return uint8(n)
				}
				brighter.Set(x, y, color.RGBA{R: clamp(r), G: clamp(g), B: clamp(b), A: 255})
			}
		}
		if a, b := dHash(base), dHash(brighter); a != b {
			t.Errorf("整体提亮后哈希应不变：%#x vs %#x", a, b)
		}
	})

	t.Run("缩放后哈希稳定", func(t *testing.T) {
		// 抽帧宽度是可配的，同一段视频用不同 -width 抽出的帧必须仍然可比，
		// 否则换了参数就得重跑整条去重流程。
		big, mid, small := dHash(scene(1280, 720)), dHash(scene(640, 360)), dHash(scene(320, 180))
		if d := Hamming(big, mid); d > 4 {
			t.Errorf("1280 与 640 的距离应很小，得到 %d", d)
		}
		if d := Hamming(big, small); d > 4 {
			t.Errorf("1280 与 320 的距离应很小，得到 %d", d)
		}
	})
}

func TestHamming(t *testing.T) {
	cases := []struct {
		a, b uint64
		want int
	}{
		{0, 0, 0},
		{0, 1, 1},
		{0, ^uint64(0), 64},
		{0b1011, 0b0010, 2},
	}
	for _, c := range cases {
		if got := Hamming(c.a, c.b); got != c.want {
			t.Errorf("Hamming(%#x, %#x) = %d，期望 %d", c.a, c.b, got, c.want)
		}
	}
}

func TestCollapse(t *testing.T) {
	t.Run("折叠重复帧", func(t *testing.T) {
		in := []Frame{
			{Index: 1, Hash: 0b0000},
			{Index: 2, Hash: 0b0000},
			{Index: 3, Hash: 0b1111},
			{Index: 4, Hash: 0b1111},
		}
		got := Collapse(in, 2)
		if len(got) != 2 || got[0].Index != 1 || got[1].Index != 3 {
			t.Errorf("期望保留第 1、3 帧，得到 %+v", indices(got))
		}
	})

	t.Run("与保留帧比较可捕获累计漂移", func(t *testing.T) {
		// 这一步是 Collapse 的核心语义：每帧只和上一帧差 1 位，
		// 但累计到第 4 帧已与起点差 2 位。若实现成「与上一帧比较」，
		// 整段会被折叠成 1 帧，缓慢移动的画面就消失了。
		in := []Frame{
			{Index: 1, Hash: 0b00},
			{Index: 2, Hash: 0b01},
			{Index: 3, Hash: 0b10},
			{Index: 4, Hash: 0b11},
		}
		got := Collapse(in, 1)
		if len(got) != 2 || got[0].Index != 1 || got[1].Index != 4 {
			t.Errorf("期望保留第 1、4 帧（累计漂移被捕获），得到 %+v", indices(got))
		}
	})

	t.Run("阈值为负时不去重", func(t *testing.T) {
		in := []Frame{{Index: 1, Hash: 1}, {Index: 2, Hash: 1}, {Index: 3, Hash: 1}}
		got := Collapse(in, -1)
		if len(got) != 3 {
			t.Errorf("阈值负值应跳过去重，得到 %d 帧", len(got))
		}
	})

	t.Run("不去重时不共享底层数组", func(t *testing.T) {
		// Collapse 的返回值会被调用方就地改写（改 File 路径），
		// 若直接返回入参切片，会悄悄改掉调用方手里的原始数据。
		in := []Frame{{Index: 1, Hash: 1}, {Index: 2, Hash: 2}}
		got := Collapse(in, -1)
		got[0].File = "被改了"
		if in[0].File == "被改了" {
			t.Error("不去重路径必须返回副本，不能与入参共享底层数组")
		}
	})

	t.Run("空输入返回空", func(t *testing.T) {
		if got := Collapse(nil, 5); got != nil {
			t.Errorf("空输入应返回 nil，得到 %+v", got)
		}
	})
}

func TestSegmentFrames(t *testing.T) {
	const (
		low  = 0x00000000
		high = 0xFFFFFFFF
	)

	t.Run("在哈希突变处切段", func(t *testing.T) {
		in := []Frame{
			{Index: 1, Hash: low, AtMs: 0},
			{Index: 2, Hash: low, AtMs: 500},
			{Index: 3, Hash: low, AtMs: 1000},
			{Index: 4, Hash: high, AtMs: 1500},
			{Index: 5, Hash: high, AtMs: 2000},
			{Index: 6, Hash: low, AtMs: 2500},
		}
		segs := SegmentFrames(in, 4, 2)
		if len(segs) != 3 {
			t.Fatalf("期望 3 段，得到 %d 段", len(segs))
		}
		want := [][2]int{{0, 2}, {3, 4}, {5, 5}}
		for i, w := range want {
			if segs[i].From != w[0] || segs[i].To != w[1] {
				t.Errorf("第 %d 段区间 = [%d,%d]，期望 [%d,%d]",
					i+1, segs[i].From, segs[i].To, w[0], w[1])
			}
		}
		if segs[0].StartMs != 0 || segs[0].EndMs != 1000 {
			t.Errorf("第 1 段时间 = %d~%d，期望 0~1000", segs[0].StartMs, segs[0].EndMs)
		}
	})

	t.Run("最小帧数合并碎片", func(t *testing.T) {
		// 每帧都突变（模拟快速转视角），若不加最小帧数约束会切出
		// 一堆只有一两帧的碎段，送审给老师既费算力又没信息。
		in := []Frame{
			{Index: 1, Hash: low}, {Index: 2, Hash: high}, {Index: 3, Hash: low},
			{Index: 4, Hash: high}, {Index: 5, Hash: low}, {Index: 6, Hash: high},
			{Index: 7, Hash: low}, {Index: 8, Hash: high},
		}
		segs := SegmentFrames(in, 4, 3)
		for i, s := range segs {
			n := s.To - s.From + 1
			// 最后一段承接剩余帧，可以短于 minFrames；其余段不得短于它
			if i < len(segs)-1 && n < 3 {
				t.Errorf("第 %d 段只有 %d 帧，应被合并", i+1, n)
			}
		}
		if len(segs) >= 4 {
			t.Errorf("最小 3 帧约束下不应切出 %d 段", len(segs))
		}
	})

	t.Run("段内变化量是相邻距离之和", func(t *testing.T) {
		in := []Frame{
			{Index: 1, Hash: 0b0000},
			{Index: 2, Hash: 0b0001},
			{Index: 3, Hash: 0b0011},
		}
		segs := SegmentFrames(in, 8, 1)
		if len(segs) != 1 {
			t.Fatalf("期望 1 段，得到 %d", len(segs))
		}
		if segs[0].Motion != 2 {
			t.Errorf("段内变化量 = %d，期望 2（1+1）", segs[0].Motion)
		}
	})

	t.Run("空输入返回空", func(t *testing.T) {
		if got := SegmentFrames(nil, 4, 2); got != nil {
			t.Errorf("空输入应返回 nil，得到 %+v", got)
		}
	})
}

func TestSegmentReps(t *testing.T) {
	seg := Segment{From: 0, To: 9}

	t.Run("k=3 覆盖首中尾", func(t *testing.T) {
		got := seg.Reps(3)
		want := []int{0, 4, 9}
		if !equalInts(got, want) {
			t.Errorf("Reps(3) = %v，期望 %v", got, want)
		}
	})

	t.Run("k=1 取段中点", func(t *testing.T) {
		// 取中点而不是首帧：首帧往往还是上一个动作的残留画面
		if got := seg.Reps(1); !equalInts(got, []int{5}) {
			t.Errorf("Reps(1) = %v，期望 [5]", got)
		}
	})

	t.Run("k=0 按 1 处理", func(t *testing.T) {
		if got := seg.Reps(0); !equalInts(got, []int{5}) {
			t.Errorf("Reps(0) = %v，期望 [5]", got)
		}
	})

	t.Run("k 超过段长时返回全部", func(t *testing.T) {
		got := seg.Reps(20)
		if len(got) != 10 || got[0] != 0 || got[9] != 9 {
			t.Errorf("Reps(20) = %v，期望 0..9 全部", got)
		}
	})

	t.Run("单帧段返回该帧", func(t *testing.T) {
		one := Segment{From: 7, To: 7}
		if got := one.Reps(3); !equalInts(got, []int{7}) {
			t.Errorf("单帧段 Reps(3) = %v，期望 [7]", got)
		}
	})

	t.Run("越界段返回空", func(t *testing.T) {
		bad := Segment{From: 5, To: 4}
		if got := bad.Reps(3); got != nil {
			t.Errorf("非法区间应返回 nil，得到 %v", got)
		}
	})
}

func TestRenumber(t *testing.T) {
	in := []Frame{
		{Index: 12, File: "a.jpg", AtMs: 5500, Hash: 1},
		{Index: 37, File: "b.jpg", AtMs: 18000, Hash: 2},
		{Index: 55, File: "c.jpg", AtMs: 27000, Hash: 3},
	}
	got := Renumber(in)
	for i, f := range got {
		if f.Index != i+1 {
			t.Errorf("第 %d 个元素编号 = %d，期望 %d", i, f.Index, i+1)
		}
		if f.Raw != in[i].Index {
			t.Errorf("第 %d 个元素 Raw = %d，期望 %d", i, f.Raw, in[i].Index)
		}
		if f.File != in[i].File || f.AtMs != in[i].AtMs || f.Hash != in[i].Hash {
			t.Errorf("第 %d 个元素除编号外的字段被改动: %+v", i, f)
		}
	}
}

func indices(fs []Frame) []int {
	out := make([]int, len(fs))
	for i, f := range fs {
		out[i] = f.Index
	}
	return out
}

func equalInts(a, b []int) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}
