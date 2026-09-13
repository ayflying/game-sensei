// cmd/see 把一帧游戏画面翻译成**纯文本**报告，让「读不了图的模型」也能看清屏幕。
//
// 存在理由：本项目的老师是纯文本模型（当前模型不支持图片输入），一旦需要判断
// 「屏幕上现在是什么界面」，人肉看图这条路就断了。可是真机标定的每一小步都依赖
// 这件事：投球瞄准态长什么样、大地图开到哪一层、有没有弹窗挡住摇杆……
// see 用四张文本图把「看」这件事补回来：
//
//	亮度图   每格一个字符（. :-=+*#%@），一眼看出哪里是暗背景、哪里是亮面板
//	色相图   每格一个字母（R/O/Y/G/C/B/P/M），能定位红血条、黄按钮、蓝地图
//	面板图   近白像素占比够高的格子打 #，用来发现「弹了个大白框/对话框」
//	标尺     列/行索引对应的归一化坐标，标定时可直接读出「要点哪一格」
//
// 用法：
//
//	see.exe -in frame.png            # 分析一张已落盘的 PNG
//	see.exe                          # 现场截一帧再分析
//	see.exe -crop 0.0,0.5,0.5,1.0    # 只看左下半屏（归一化）
//	see.exe -game nrc                # 顺带跑档案的战斗态判定
package main

import (
	"flag"
	"fmt"
	"image"
	"image/png"
	"os"
	"strings"

	"github.com/ayflying/game-sensei/internal/android"
	"github.com/ayflying/game-sensei/internal/game"
)

// lumaRamp 从暗到亮的字符梯度（越靠后越亮）。
const lumaRamp = " .:-=+*#%@"

func main() {
	var (
		in       = flag.String("in", "", "待分析的 PNG 路径；留空则现场截一帧")
		cols     = flag.Int("cols", 96, "文本图列数")
		rows     = flag.Int("rows", 32, "文本图行数（字符高约是宽的 2 倍，96x32 ≈ 16:9）")
		crop     = flag.String("crop", "", "裁剪区域 x0,y0,x1,y1（归一化 0~1），留空为全屏")
		vs       = flag.String("vs", "", "A/B 对照：再给一张同尺寸 PNG，额外打印「哪里变了」的变化图")
		maskArg  = flag.String("mask", "", "颜色掩膜：逗号分隔，可选 yellow,orange,red,white,cyan,green；打印每格命中率图 + 命中像素的质心与包围盒")
		gameName = flag.String("game", "", "档案名：填了就跑一次战斗态判定")
		adbPath  = flag.String("adb", "", "adb 路径（现场截图时用）")
		serial   = flag.String("serial", "", "设备序列号")
		legend   = flag.Bool("legend", true, "打印图例与统计头")
	)
	flag.Parse()

	img, err := load(*in, *adbPath, *serial)
	if err != nil {
		fail(err)
	}

	x0, y0, x1, y1 := 0.0, 0.0, 1.0, 1.0
	if *crop != "" {
		x0, y0, x1, y1, err = parseCrop(*crop)
		if err != nil {
			fail(err)
		}
	}
	sub := cropImage(img, x0, y0, x1, y1)
	b := sub.Bounds()
	w, h := b.Dx(), b.Dy()
	if w <= 0 || h <= 0 {
		fail(fmt.Errorf("裁剪后为空（crop=%s）", *crop))
	}

	if *legend {
		fmt.Printf("尺寸 %dx%d（原图 %dx%d）　裁剪 x[%.3f,%.3f] y[%.3f,%.3f]\n",
			w, h, img.Bounds().Dx(), img.Bounds().Dy(), x0, x1, y0, y1)
		if *in == "" {
			fmt.Println("来源：现场截图")
		} else {
			fmt.Printf("来源：%s\n", *in)
		}
	}

	cells := analyze(sub, *cols, *rows)

	if *legend {
		fmt.Printf("整幅均值 luma=%5.1f  sat=%5.1f　近白占比=%4.1f%%　全黑=%v\n",
			meanAll(cells, "luma"), meanAll(cells, "sat"),
			100*meanAll(cells, "white"), meanAll(cells, "luma") < 3)
		fmt.Println()
	}

	fmt.Println("── 亮度图  " + lumaRamp + "  （越右越亮）──")
	printRuler(*cols, x0, x1)
	for r := 0; r < *rows; r++ {
		var sb strings.Builder
		for c := 0; c < *cols; c++ {
			v := cells[r*(*cols)+c].luma
			i := int(v/256*float64(len(lumaRamp)))
			if i >= len(lumaRamp) {
				i = len(lumaRamp) - 1
			}
			sb.WriteByte(lumaRamp[i])
		}
		fmt.Printf("%5.3f %s\n", y0+(y1-y0)*(float64(r)+0.5)/float64(*rows), sb.String())
	}

	// 第二张图：色相。用来定位「红血条/黄按钮/蓝地图/绿草」这类有色彩语义的区域。
	if *legend {
		fmt.Println("\n── 色相图  R红 O橙 Y黄 G绿 C青 B蓝 P紫 M品红  ·灰/暗  ──")
	} else {
		fmt.Println("\n── 色相图 ──")
	}
	printRuler(*cols, x0, x1)
	for r := 0; r < *rows; r++ {
		var sb strings.Builder
		for c := 0; c < *cols; c++ {
			sb.WriteString(hueLetter(cells[r*(*cols)+c]))
		}
		fmt.Printf("%5.3f %s\n", y0+(y1-y0)*(float64(r)+0.5)/float64(*rows), sb.String())
	}

	// 第三张图：近白占比。大白框（对话框/结算面板/大地图详情）会整片亮起来，
	// 是「弹窗挡住了输入」最快的判据。
	fmt.Println("\n── 面板图  #=近白占比≥40%（疑似 UI 面板/弹窗）──")
	printRuler(*cols, x0, x1)
	for r := 0; r < *rows; r++ {
		var sb strings.Builder
		for c := 0; c < *cols; c++ {
			cell := cells[r*(*cols)+c]
			switch {
			case cell.white >= 0.40:
				sb.WriteByte('#')
			case cell.white >= 0.15:
				sb.WriteByte('+')
			default:
				sb.WriteByte('.')
			}
		}
		fmt.Printf("%5.3f %s\n", y0+(y1-y0)*(float64(r)+0.5)/float64(*rows), sb.String())
	}

	if *vs != "" {
		vsImg, err := load(*vs, "", "")
		if err != nil {
			fail(err)
		}
		if vsImg.Bounds() != img.Bounds() {
			fail(fmt.Errorf("两张图尺寸不同：%v vs %v", img.Bounds(), vsImg.Bounds()))
		}
		fmt.Printf("\n── A/B 变化图　A=%s → B=%s　（两张图都已经过同一 -crop）──\n", *in, *vs)
		fmt.Println("  #=变化很大  +=中等  .=轻微  空格=几乎没变")
		printRuler(*cols, x0, x1)
		for r := 0; r < *rows; r++ {
			var sb strings.Builder
			for c := 0; c < *cols; c++ {
				switch dd := cellDiff(img, vsImg, x0, y0, x1, y1, *cols, *rows, r, c); {
				case dd >= 45:
					sb.WriteByte('#')
				case dd >= 20:
					sb.WriteByte('+')
				case dd >= 8:
					sb.WriteByte('.')
				default:
					sb.WriteByte(' ')
				}
			}
			fmt.Printf("%5.3f |%s|\n", y0+(y1-y0)*(float64(r)+0.5)/float64(*rows), sb.String())
		}
	}

	if *maskArg != "" {
		masks := strings.Split(*maskArg, ",")
		fmt.Println()
		for _, name := range masks {
			nm := strings.ToLower(strings.TrimSpace(name))
			pred := maskPredicate(nm)
			if pred == nil {
				fmt.Printf("（未知掩膜 %q，跳过；可选 yellow/orange/red/white/cyan/green）\n", nm)
				continue
			}
			printMask(img, nm, pred, x0, y0, x1, y1, *cols, *rows)
		}
	}

	if *gameName != "" {
		prof, err := game.Load(*gameName)
		if err != nil {
			fail(err)
		}
		// 战斗态判据必须吃全分辨率彩色帧（标定基准），故这里用原图而不是裁剪图。
		battle := prof.IsBattle(img)
		fmt.Printf("\n── 档案判定（%s）──\n战斗态: %v\n", prof.Name, battle)
		if prof.CanDetectBattle() && !battle {
			hits, n := battleClusters(img, prof)
			fmt.Printf("（底部检测带内亮列簇 %d 个，落在标定位置上的 %d 个，需 ≥%d）\n",
				n, hits, prof.BattleDetect.MinHits)
			for _, c := range clusterList(img, prof) {
				fmt.Printf("  簇 质心 %.4f,%.4f\n", c[0], c[1])
			}
		}
	}
}

// cell 是一个文本格的统计量。
type cell struct {
	luma  float64 // 平均亮度 0~255
	sat   float64 // 平均饱和度 (max-min) 0~255
	r, g, bAvg float64
	white float64 // 近白像素比例 0~1
}

// analyze 把图像切成 cols×rows 个格子并逐格统计。
//
// 采样是「每格取代表像素」而不是「每个像素都算」：标定看的是布局与颜色分布，
// 全分辨率逐像素只慢不更准。每格采样 step 像素，保证 3200x2136 也在几十毫秒内跑完。
func analyze(img image.Image, cols, rows int) []cell {
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	out := make([]cell, cols*rows)
	// 每格至少取 ~6x6 个样本点。
	stepX := max1(w / (cols * 6))
	stepY := max1(h / (rows * 6))

	for r := 0; r < rows; r++ {
		y0 := b.Min.Y + h*r/rows
		y1 := b.Min.Y + h*(r+1)/rows
		for c := 0; c < cols; c++ {
			x0 := b.Min.X + w*c/cols
			x1 := b.Min.X + w*(c+1)/cols
			var n, sl, ss, sr, sg, sb, sw int
			for y := y0; y < y1; y += stepY {
				for x := x0; x < x1; x += stepX {
					rr, gg, bb, _ := img.At(x, y).RGBA()
					r8, g8, b8 := int(rr>>8), int(gg>>8), int(bb>>8)
					mx, mn := maxi(r8, g8, b8), mini(r8, g8, b8)
					sl += (r8*299 + g8*587 + b8*114) / 1000
					ss += mx - mn
					sr += r8
					sg += g8
					sb += b8
					if mx >= 200 && mx-mn <= 30 {
						sw++
					}
					n++
				}
			}
			if n == 0 {
				n = 1
			}
			out[r*cols+c] = cell{
				luma:  float64(sl) / float64(n),
				sat:   float64(ss) / float64(n),
				r:     float64(sr) / float64(n),
				g:     float64(sg) / float64(n),
				bAvg:  float64(sb) / float64(n),
				white: float64(sw) / float64(n),
			}
		}
	}
	return out
}

// hueLetter 把一个格子归到最接近的颜色字母：饱和度太低或太暗就归为「灰/暗」。
func hueLetter(c cell) string {
	mx := maxi3f(c.r, c.g, c.bAvg)
	mn := mini3f(c.r, c.g, c.bAvg)
	if mx-mn < 28 || mx < 45 {
		return "."
	}
	h := hueAngle(c.r, c.g, c.bAvg, mx, mn)
	switch {
	case h < 15 || h >= 345:
		return "R"
	case h < 45:
		return "O"
	case h < 70:
		return "Y"
	case h < 160:
		return "G"
	case h < 200:
		return "C"
	case h < 260:
		return "B"
	case h < 300:
		return "P"
	default:
		return "M"
	}
}

// hueAngle 返回 0~360 的色相角。
func hueAngle(r, g, b, mx, mn float64) float64 {
	if mx == mn {
		return 0
	}
	var h float64
	switch mx {
	case r:
		h = 60 * ((g - b) / (mx - mn))
	case g:
		h = 60 * (2 + (b-r)/(mx-mn))
	default:
		h = 60 * (4 + (r-g)/(mx-mn))
	}
	if h < 0 {
		h += 360
	}
	return h
}

// printRuler 打印一行归一化 x 刻度，方便把「哪一列」翻译回坐标。
func printRuler(cols int, x0, x1 float64) {
	var sb strings.Builder
	sb.WriteString("      ")
	for c := 0; c < cols; c++ {
		nx := x0 + (x1-x0)*(float64(c)+0.5)/float64(cols)
		if c%8 == 0 {
			s := fmt.Sprintf("%.2f", nx)
			sb.WriteString(s)
			// 刻度串长 4，若下个刻度落在已写的区间里就补空格对齐。
			for i := len(s); i < 8 && c+1 < cols; i++ {
				sb.WriteByte(' ')
			}
		}
	}
	fmt.Println(sb.String())
}

// meanAll 求所有格子的某项均值（字段名 luma/sat/white）。
func meanAll(cells []cell, field string) float64 {
	if len(cells) == 0 {
		return 0
	}
	var s float64
	for _, c := range cells {
		switch field {
		case "luma":
			s += c.luma
		case "sat":
			s += c.sat
		case "white":
			s += c.white
		}
	}
	return s / float64(len(cells))
}

// load 读取 PNG；in 为空时现场截一帧。
func load(in, adbPath, serial string) (image.Image, error) {
	if in != "" {
		f, err := os.Open(in)
		if err != nil {
			return nil, fmt.Errorf("打开 %s: %w", in, err)
		}
		defer f.Close()
		return png.Decode(f)
	}
	dev, err := android.Open(adbPath, serial)
	if err != nil {
		return nil, err
	}
	return dev.Screenshot()
}

// cropImage 按归一化矩形裁剪（x0,y0 左上，x1,y1 右下）。
func cropImage(img image.Image, x0, y0, x1, y1 float64) image.Image {
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	r := image.Rect(
		b.Min.X+int(x0*float64(w)), b.Min.Y+int(y0*float64(h)),
		b.Min.X+int(x1*float64(w)), b.Min.Y+int(y1*float64(h)),
	).Intersect(b)
	return img.(interface {
		SubImage(r image.Rectangle) image.Image
	}).SubImage(r)
}

// parseCrop 解析 "x0,y0,x1,y1"。
func parseCrop(s string) (x0, y0, x1, y1 float64, err error) {
	parts := strings.Split(s, ",")
	if len(parts) != 4 {
		return 0, 0, 0, 0, fmt.Errorf("-crop 需要 4 个数 x0,y0,x1,y1，得到 %q", s)
	}
	v := make([]float64, 4)
	for i, p := range parts {
		if _, e := fmt.Sscanf(strings.TrimSpace(p), "%f", &v[i]); e != nil {
			return 0, 0, 0, 0, fmt.Errorf("-crop 第 %d 项不是数字: %q", i+1, p)
		}
	}
	return v[0], v[1], v[2], v[3], nil
}

// battleClusters 返回「检测带内亮列簇总数」与其中命中标定位置的个数。
func battleClusters(img image.Image, prof *game.Profile) (hits, total int) {
	list := clusterList(img, prof)
	return countPositionHits(prof, list), len(list)
}

// countPositionHits 数一数有几个簇落在标定位置上（与 IsBattle 第 4 步同逻辑）。
func countPositionHits(prof *game.Profile, clusters [][2]float64) int {
	tolX, tolY := 0.015, 0.015
	if prof.BattleDetect != nil {
		if prof.BattleDetect.TolX > 0 {
			tolX = prof.BattleDetect.TolX
		}
		if prof.BattleDetect.TolY > 0 {
			tolY = prof.BattleDetect.TolY
		}
	}
	used := make([]bool, len(clusters))
	hits := 0
	for _, want := range prof.BattleDetect.Positions {
		for i, c := range clusters {
			if used[i] {
				continue
			}
			if absf(c[0]-want[0]) <= tolX && absf(c[1]-want[1]) <= tolY {
				used[i] = true
				hits++
				break
			}
		}
	}
	return hits
}

// clusterList 复刻 IsBattle 的亮列分簇，但把簇质心吐出来——判不出来时，
// 「簇在哪」比「判成什么」有用得多（能直接看出检测带里到底有什么）。
func clusterList(img image.Image, prof *game.Profile) [][2]float64 {
	if prof.BattleDetect == nil {
		return nil
	}
	d := prof.BattleDetect
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	band := d.Band
	x0 := b.Min.X + int(band[0]*float64(w))
	x1 := b.Min.X + int(band[2]*float64(w))
	y0 := b.Min.Y + int(band[1]*float64(h))
	y1 := b.Min.Y + int(band[3]*float64(h))
	bandW := x1 - x0
	if bandW <= 4 || y1-y0 <= 4 {
		return nil
	}
	bright := d.Bright
	if bright == 0 {
		bright = 150
	}
	satMax := d.SatMax
	if satMax <= 0 {
		satMax = 70
	}
	colCount := make([]int, bandW)
	colSumX := make([]int, bandW)
	colSumY := make([]int, bandW)
	for x := x0; x < x1; x++ {
		cx := x - x0
		for y := y0; y < y1; y++ {
			rr, gg, bb, _ := img.At(x, y).RGBA()
			r8, g8, b8 := int(rr>>8), int(gg>>8), int(bb>>8)
			mx, mn := maxi(r8, g8, b8), mini(r8, g8, b8)
			if mx >= bright && mx-mn <= satMax {
				colCount[cx]++
				colSumX[cx] += cx
				colSumY[cx] += y - y0
			}
		}
	}
	maxCol := 0
	for _, c := range colCount {
		if c > maxCol {
			maxCol = c
		}
	}
	if maxCol == 0 {
		return nil
	}
	colThresh := maxCol * 15 / 100
	minRunCols := bandW * 14 / 1000
	if minRunCols < 8 {
		minRunCols = 8
	}
	minPixels := d.MinPixels
	if minPixels <= 0 {
		minPixels = 500
	}
	var out [][2]float64
	for cx := 0; cx < bandW; {
		if colCount[cx] <= colThresh {
			cx++
			continue
		}
		start := cx
		sum, sumX, sumY := 0, 0, 0
		for cx < bandW && colCount[cx] > colThresh {
			sum += colCount[cx]
			sumX += colSumX[cx]
			sumY += colSumY[cx]
			cx++
		}
		if cx-start >= minRunCols && sum >= minPixels {
			out = append(out, [2]float64{
				float64(sumX+x0*sum) / float64(sum) / float64(w),
				float64(sumY+y0*sum) / float64(sum) / float64(h),
			})
		}
	}
	return out
}

// colorPred 判断一个像素是否属于某个颜色掩膜。
type colorPred func(r, g, b int) bool

// maskPredicate 把掩膜名翻译成像素判据。阈值取「肉眼一眼能分辨」的宽松档，
// 目的是**定位**（这条黄线在哪、这个红条在哪），不是做精确分割。
func maskPredicate(name string) colorPred {
	switch name {
	case "yellow":
		// 亮黄：红绿都高、蓝明显低。瞄准态的抛物线、任务追踪的分隔线都是这个色。
		return func(r, g, b int) bool { return r >= 170 && g >= 150 && b <= 140 && r-b >= 60 && g-b >= 40 }
	case "orange":
		return func(r, g, b int) bool { return r >= 190 && g >= 110 && g <= 195 && b <= 110 && r-g >= 40 }
	case "red":
		// 红：血条、危险提示。
		return func(r, g, b int) bool { return r >= 150 && g <= 105 && b <= 105 && r-g >= 70 }
	case "white":
		return func(r, g, b int) bool {
			mx, mn := maxi(r, g, b), mini(r, g, b)
			return mx >= 200 && mx-mn <= 30
		}
	case "cyan":
		return func(r, g, b int) bool { return b >= 150 && g >= 150 && r <= 140 && r+30 < b }
	case "green":
		return func(r, g, b int) bool { return g >= 140 && g-r >= 40 && g-b >= 40 }
	default:
		return nil
	}
}

// printMask 打印某个颜色掩膜的分布：每格命中率图 + 命中像素的质心与包围盒。
//
// 质心/包围盒是给「标定动作坐标」用的：要找那条抛物线，先看黄色像素聚在哪一段，
// 起止点就落在它的两端，而不是靠猜。
func printMask(img image.Image, name string, pred colorPred, x0, y0, x1, y1 float64, cols, rows int) {
	b := img.Bounds()
	w, h := b.Min.X, b.Min.Y
	dw, dh := b.Dx(), b.Dy()

	// 逐格统计命中率；同时累计全局质心与包围盒。
	hits := make([]int, cols*rows)
	total := 0
	var sx, sy, bx0, by0, bx1, by1 int
	bx0, by0 = 1<<30, 1<<30
	bx1, by1 = -1, -1

	for r := 0; r < rows; r++ {
		fy0 := y0 + float64(r)*(y1-y0)/float64(rows)
		fy1 := y0 + float64(r+1)*(y1-y0)/float64(rows)
		cy0 := h + int(fy0*float64(dh))
		cy1 := h + int(fy1*float64(dh))
		for c := 0; c < cols; c++ {
			fx0 := x0 + float64(c)*(x1-x0)/float64(cols)
			fx1 := x0 + float64(c+1)*(x1-x0)/float64(cols)
			cx0 := w + int(fx0*float64(dw))
			cx1 := w + int(fx1*float64(dw))
			stepX := max1((cx1 - cx0) / 8)
			stepY := max1((cy1 - cy0) / 8)
			n, hit := 0, 0
			for y := cy0; y < cy1 && y < b.Max.Y; y += stepY {
				for x := cx0; x < cx1 && x < b.Max.X; x += stepX {
					rr, gg, bb, _ := img.At(x, y).RGBA()
					r8, g8, b8 := int(rr>>8), int(gg>>8), int(bb>>8)
					if pred(r8, g8, b8) {
						hit++
						sx += x
						sy += y
						total++
						if x < bx0 {
							bx0 = x
						}
						if y < by0 {
							by0 = y
						}
						if x > bx1 {
							bx1 = x
						}
						if y > by1 {
							by1 = y
						}
					}
					n++
				}
			}
			hits[r*cols+c] = hit
			_ = n
		}
	}

	fmt.Printf("── 掩膜 %s　命中采样点 %d ──\n", name, total)
	if total > 0 {
		fmt.Printf("   质心 (%.3f,%.3f)　包围盒 x[%.3f,%.3f] y[%.3f,%.3f]\n",
			float64(sx)/float64(total)/float64(dw), float64(sy)/float64(total)/float64(dh),
			float64(bx0)/float64(dw), float64(bx1)/float64(dw),
			float64(by0)/float64(dh), float64(by1)/float64(dh))
	}
	fmt.Println("   #=该格≥4 个命中采样点  +=≥2  .=1  空格=0")
	printRuler(cols, x0, x1)
	for r := 0; r < rows; r++ {
		var sb strings.Builder
		for c := 0; c < cols; c++ {
			switch hits[r*cols+c] {
			case 0:
				sb.WriteByte(' ')
			case 1:
				sb.WriteByte('.')
			case 2, 3:
				sb.WriteByte('+')
			default:
				sb.WriteByte('#')
			}
		}
		fmt.Printf("%5.3f |%s|\n", y0+(y1-y0)*(float64(r)+0.5)/float64(rows), sb.String())
	}
	fmt.Println()
}

// cellDiff 计算两张全图在同一个格子里的平均绝对亮度差（0~255）。
//
// 与 FrameDiff 的区别：那个是整幅一个标量（判「有没有动」），这个是逐格标量
// （判「动的是哪一块」）。标定界面元素时看后者——A/B 两张图之间多出来的那块 UI，
// 在变化图上会整片亮起来，比在亮度图里靠肉眼找轮廓可靠得多。
func cellDiff(imgA, imgB image.Image, x0, y0, x1, y1 float64, cols, rows, r, c int) float64 {
	ba, bb := imgA.Bounds(), imgB.Bounds()
	w, h := ba.Dx(), ba.Dy()
	cx0 := ba.Min.X + int((x0+float64(c)*(x1-x0)/float64(cols))*float64(w))
	cx1 := ba.Min.X + int((x0+float64(c+1)*(x1-x0)/float64(cols))*float64(w))
	cy0 := ba.Min.Y + int((y0+float64(r)*(y1-y0)/float64(rows))*float64(h))
	cy1 := ba.Min.Y + int((y0+float64(r+1)*(y1-y0)/float64(rows))*float64(h))
	if cx1 <= cx0 {
		cx1 = cx0 + 1
	}
	if cy1 <= cy0 {
		cy1 = cy0 + 1
	}
	stepX := max1((cx1 - cx0) / 6)
	stepY := max1((cy1 - cy0) / 6)
	var sum float64
	var n int
	for y := cy0; y < cy1 && y < ba.Max.Y; y += stepY {
		for x := cx0; x < cx1 && x < ba.Max.X; x += stepX {
			la := lumaAt(imgA, x, y)
			lb := lumaAt(imgB, x, y)
			d := la - lb
			if d < 0 {
				d = -d
			}
			sum += d
			n++
		}
	}
	if n == 0 {
		return 0
	}
	_ = bb
	return sum / float64(n)
}

func lumaAt(img image.Image, x, y int) float64 {
	r, g, b, _ := img.At(x, y).RGBA()
	return float64((int(r>>8)*299 + int(g>>8)*587 + int(b>>8)*114) / 1000)
}

func absf(v float64) float64 {
	if v < 0 {
		return -v
	}
	return v
}

func maxi(a, b, c int) int {
	if a >= b && a >= c {
		return a
	}
	if b >= c {
		return b
	}
	return c
}

func mini(a, b, c int) int {
	if a <= b && a <= c {
		return a
	}
	if b <= c {
		return b
	}
	return c
}

func maxi3f(a, b, c float64) float64 {
	if a >= b && a >= c {
		return a
	}
	if b >= c {
		return b
	}
	return c
}

func mini3f(a, b, c float64) float64 {
	if a <= b && a <= c {
		return a
	}
	if b <= c {
		return b
	}
	return c
}

func max1(v int) int {
	if v < 1 {
		return 1
	}
	return v
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "see:", err)
	os.Exit(1)
}
