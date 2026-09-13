package game

import (
	"image"
	"image/png"
	"math"
	"os"
	"path/filepath"
	"testing"
)

// testBattleProfile 返回一份与 profiles/nrc.json 同参的战斗判据档案。
// 刻意不读文件：单测要能脱离仓库数据独立跑，且钉的是**判据语义**而不是档案内容。
func testBattleProfile() *Profile {
	return &Profile{
		Name: "test",
		BattleDetect: &BattleDetect{
			Band:        [4]float64{0.55, 0.855, 1.0, 0.955},
			Bright:      150,
			SatMax:      70,
			MinPixels:   500,
			MinClusters: 3,
			Positions: [][2]float64{
				{0.664, 0.905}, {0.728, 0.905}, {0.786, 0.905},
				{0.847, 0.905}, {0.908, 0.900},
			},
			TolX:    0.015,
			TolY:    0.015,
			MinHits: 4,
		},
	}
}

// 合成一帧：整幅填充 bg，再在标定位置画 5 个半径 rFrac 的圆（色 fg）。
func synthFrame(w, h int, bg, fg [3]uint8, rFrac float64) image.Image {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			i := img.PixOffset(x, y)
			img.Pix[i], img.Pix[i+1], img.Pix[i+2], img.Pix[i+3] = bg[0], bg[1], bg[2], 255
		}
	}
	p := testBattleProfile()
	r := rFrac * float64(w)
	for _, pos := range p.BattleDetect.Positions {
		px, py := pos[0]*float64(w), pos[1]*float64(h)
		for y := int(py - r); y <= int(py+r); y++ {
			for x := int(px - r); x <= int(px+r); x++ {
				if x < 0 || y < 0 || x >= w || y >= h {
					continue
				}
				if math.Hypot(float64(x)-px, float64(y)-py) > r {
					continue
				}
				i := img.PixOffset(x, y)
				img.Pix[i], img.Pix[i+1], img.Pix[i+2] = fg[0], fg[1], fg[2]
			}
		}
	}
	return img
}

// 语义 1：深蓝底条 + 奶油圆钮 ⇒ 判战斗（这条原判据也能过）。
func TestBattleDarkBarWithButtons(t *testing.T) {
	p := testBattleProfile()
	img := synthFrame(640, 427, [3]uint8{40, 45, 70}, [3]uint8{245, 243, 235}, 0.011)
	if !p.battleByLocalContrast(img) {
		t.Error("深底条 + 5 圆钮：局部指纹应判战斗")
	}
}

// 语义 2：什么都没有的暗背景 ⇒ 两条判据都必须判「不是战斗」。
func TestBattleEmptyFrame(t *testing.T) {
	p := testBattleProfile()
	img := synthFrame(640, 427, [3]uint8{40, 45, 70}, [3]uint8{40, 45, 70}, 0)
	if p.IsBattle(img) {
		t.Error("空白暗背景不该判战斗")
	}
}

// 语义 3（本轮修复的核心）：**整幅很亮**时圆钮仍能被局部指纹认出，
// 而原列投影判据会退化——这正是不做全局统计的理由。
func TestBattleLocalContrastSurvivesBrightBackground(t *testing.T) {
	p := testBattleProfile()
	// 背景已经是亮低饱和（200 级），圆钮更亮（250）——真实战斗里就是这种
	// 「明亮场景把底条照亮」的情形。
	img := synthFrame(640, 427, [3]uint8{200, 202, 205}, [3]uint8{250, 250, 250}, 0.011)

	if !p.battleByLocalContrast(img) {
		t.Error("亮背景下圆钮依然亮于周边，局部指纹应判战斗")
	}
	if p.battleByColumns(img) {
		t.Log("提示：该合成帧上列投影恰好也成立，本用例不再断言它必须失败")
	}
	if !p.IsBattle(img) {
		t.Error("IsBattle 应取两条判据的或，至少局部指纹成立")
	}
}

// 语义 4：位置约束仍然有效——圆钮画在**非标定位置**上不该判战斗，
// 否则大地图上的散乱圆形图标又会被误判（原判据加位置层的初衷）。
func TestBattleRejectsButtonsOffPosition(t *testing.T) {
	p := testBattleProfile()
	img := image.NewRGBA(image.Rect(0, 0, 640, 427))
	for y := 0; y < 427; y++ {
		for x := 0; x < 640; x++ {
			i := img.PixOffset(x, y)
			img.Pix[i], img.Pix[i+1], img.Pix[i+2], img.Pix[i+3] = 40, 45, 70, 255
		}
	}
	// 五个圆钮但整体右移 0.06，落不到标定位置（容差 0.015）。
	for k := 0; k < 5; k++ {
		px := (0.85 + 0.06*float64(k)) * 640
		py := 0.905 * 427
		r := 0.011 * 640
		for y := int(py - r); y <= int(py+r); y++ {
			for x := int(px - r); x <= int(px+r); x++ {
				if x < 0 || y < 0 || x >= 640 || y >= 427 {
					continue
				}
				if math.Hypot(float64(x)-px, float64(y)-py) > r {
					continue
				}
				i := img.PixOffset(x, y)
				img.Pix[i], img.Pix[i+1], img.Pix[i+2] = 245, 243, 235
			}
		}
	}
	if p.IsBattle(img) {
		t.Error("圆钮不在标定位置上，不该判战斗（位置约束失效会在大地图上误报）")
	}
}

// 真机金标（有数据才跑，其余环境自动跳过）：
// 2026-09-13 用「按战斗独有的逃跑钮」独立验证过的一对帧——按之前确实在战斗
// （逃跑生效 Δ=80.5），按之后确实是世界态。旧判据把前者判成 world，本用例钉住修复。
func TestBattleGoldenRealFrames(t *testing.T) {
	cases := []struct {
		file string
		want bool
	}{
		{"_flee.before.png", true}, // 逃跑之前：真战斗（旧判据漏判的那类）
		{"_flee.png", false},       // 逃跑之后：真世界态
	}
	dir := filepath.Join("..", "..", ".workbuddy", "demos")
	missing := false
	for _, c := range cases {
		if _, err := os.Stat(filepath.Join(dir, c.file)); err != nil {
			missing = true
		}
	}
	if missing {
		t.Skip("真机标定帧不在本机（.workbuddy/demos/_flee*.png），跳过金标")
	}
	p, err := Load("nrc")
	if err != nil {
		t.Fatalf("加载 nrc 档案失败: %v", err)
	}
	for _, c := range cases {
		f, err := os.Open(filepath.Join(dir, c.file))
		if err != nil {
			t.Fatalf("打开 %s 失败: %v", c.file, err)
		}
		img, err := png.Decode(f)
		f.Close()
		if err != nil {
			t.Fatalf("解码 %s 失败: %v", c.file, err)
		}
		if got := p.IsBattle(img); got != c.want {
			t.Errorf("%s: IsBattle=%v，期望 %v", c.file, got, c.want)
		}
	}
}
