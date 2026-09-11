package video

import (
	"bytes"
	"image/jpeg"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func TestParseFrameRate(t *testing.T) {
	cases := []struct {
		in   string
		want float64
	}{
		{"30/1", 30},
		{"30000/1001", 29.9700299700299},
		{"0/0", 0}, // ffprobe 对某些流会给出无意义的值，不能 panic 或除零
		{"", 0},    // 字段缺失
		{"25", 25}, // 少数容器直接给数值
		{" 30/1 ", 30},
		{"abc", 0},
		{"30/0", 0}, // 分母为零
	}
	for _, c := range cases {
		got := parseFrameRate(c.in)
		if diff := got - c.want; diff > 1e-9 || diff < -1e-9 {
			t.Errorf("parseFrameRate(%q) = %v，期望 %v", c.in, got, c.want)
		}
	}
}

func TestFormatSeconds(t *testing.T) {
	cases := []struct {
		in   time.Duration
		want string
	}{
		{0, "0.000"},
		{90 * time.Second, "90.000"},
		{1500 * time.Millisecond, "1.500"},
		{2*time.Minute + 500*time.Millisecond, "120.500"},
	}
	for _, c := range cases {
		if got := formatSeconds(c.in); got != c.want {
			t.Errorf("formatSeconds(%v) = %q，期望 %q", c.in, got, c.want)
		}
	}
}

func TestTail(t *testing.T) {
	if got := tail("  hello  ", 10); got != "hello" {
		t.Errorf("短字符串应原样返回（去空白），得到 %q", got)
	}
	if got := tail("0123456789", 4); got != "...6789" {
		t.Errorf("超长字符串应保留末尾，得到 %q", got)
	}
}

func TestFramesErrorPaths(t *testing.T) {
	t.Run("频率非法时报错", func(t *testing.T) {
		if _, err := Frames([]string{"x.jpg"}, 0, 0); err == nil {
			t.Error("抽帧频率为 0 时应报错")
		}
	})

	t.Run("帧文件缺失时报错而不是静默跳过", func(t *testing.T) {
		// 静默跳过会让「关键帧少了几个」这种问题一直藏到训练阶段才暴露，
		// 而且那时候已经无法区分是解码失败还是本来就没抽到。
		dir := t.TempDir()
		missing := filepath.Join(dir, "不存在.jpg")
		if _, err := Frames([]string{missing}, 2, 0); err == nil {
			t.Error("帧文件不存在时应报错")
		}
	})

	t.Run("空列表返回空切片且不报错", func(t *testing.T) {
		got, err := Frames(nil, 2, 0)
		if err != nil {
			t.Fatalf("空列表不该报错: %v", err)
		}
		if len(got) != 0 {
			t.Errorf("空列表应返回空，得到 %d 项", len(got))
		}
	})
}

func TestFramesTimestamps(t *testing.T) {
	// 时间戳来自「序号 + 频率」，读不出图片时报错即可验证前置逻辑；
	// 这里用一张真实的小 JPEG 走通时间戳计算。
	dir := t.TempDir()
	p := filepath.Join(dir, "f.jpg")
	if err := os.WriteFile(p, jpegFixture(t), 0o644); err != nil {
		t.Fatal(err)
	}

	frames, err := Frames([]string{p, p, p}, 2, 1500*time.Millisecond)
	if err != nil {
		t.Fatalf("Frames 失败: %v", err)
	}
	want := []int64{1500, 2000, 2500}
	for i, f := range frames {
		if f.AtMs != want[i] {
			t.Errorf("第 %d 帧时间 = %dms，期望 %dms", i+1, f.AtMs, want[i])
		}
		if f.Index != i+1 {
			t.Errorf("第 %d 帧序号 = %d，期望 %d", i+1, f.Index, i+1)
		}
	}
	if frames[0].Hash != frames[2].Hash {
		t.Error("同一张图算出的哈希必须一致")
	}
}

// jpegFixture 生成一张最小的合法 JPEG，供测试走通真实解码路径。
func jpegFixture(t *testing.T) []byte {
	t.Helper()
	var buf bytes.Buffer
	if err := jpeg.Encode(&buf, scene(64, 36), &jpeg.Options{Quality: 80}); err != nil {
		t.Fatalf("生成测试 JPEG 失败: %v", err)
	}
	return buf.Bytes()
}
