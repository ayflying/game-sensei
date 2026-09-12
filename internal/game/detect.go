package game

import (
	"image"
)

// detect.go 在游戏档案里内建「回合战斗态」的像素判定，使回路不必依赖外部
// Python 脚本就能知道当前处于哪个界面态，进而按态收窄动作空间
// （战斗态禁止 MOVE、卡死不推摇杆、兜底只在战斗按钮里轮换）。
//
// 判据移植自 tools/detect_state.py（2026-09-12 真机标定，洛克王国：世界）：
// 战斗态底部横排五个奶油色圆钮【逃跑 背包 捕捉 更换 技能】落在右下一条
// 深蓝半透明条上；在该带内统计「亮且低饱和」像素的列投影，连通亮列簇达到
// BattleDetect.MinClusters 即判战斗。大世界底部没有这排圆钮，簇数自然为 0。

// pixelRGB 是统一的 8 位 RGB 取值器，屏蔽不同 image.Image 底层布局差异。
type pixelRGB func(x, y int) (r, g, b uint8)

// rgbSampler 给常见的连续内存图像类型走快速路径，其余退回 image.Image.At。
func rgbSampler(img image.Image) pixelRGB {
	switch m := img.(type) {
	case *image.RGBA:
		return func(x, y int) (uint8, uint8, uint8) {
			i := m.PixOffset(x, y)
			return m.Pix[i], m.Pix[i+1], m.Pix[i+2]
		}
	case *image.NRGBA:
		return func(x, y int) (uint8, uint8, uint8) {
			i := m.PixOffset(x, y)
			return m.Pix[i], m.Pix[i+1], m.Pix[i+2]
		}
	case *image.YCbCr:
		// 安卓截图常是 YCbCr（JPEG 解码）。直接用 Y 平面做亮度，
		// 饱和度判据在 YCbCr 上代价高且战斗带本就低彩，退化为纯亮度判定。
		return func(x, y int) (uint8, uint8, uint8) {
			yi := m.YOffset(x, y)
			yv := m.Y[yi]
			return yv, yv, yv
		}
	default:
		return func(x, y int) (uint8, uint8, uint8) {
			rr, gg, bb, _ := img.At(x, y).RGBA()
			return uint8(rr >> 8), uint8(gg >> 8), uint8(bb >> 8)
		}
	}
}
// IsBattle 用档案判据判断一帧是否处于回合战斗态。未配判据或图像为空时返回 false。
//
// 入参用彩色全分辨率帧（与 detect_state.py 的标定基准一致）。
func (p *Profile) IsBattle(img image.Image) bool {
	if !p.CanDetectBattle() || img == nil {
		return false
	}
	d := p.BattleDetect
	bright, minPixels, minClusters := d.defaults()
	satMax := d.SatMax
	if satMax <= 0 {
		satMax = 70
	}

	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	if w <= 0 || h <= 0 {
		return false
	}
	x0 := b.Min.X + int(d.Band[0]*float64(w))
	x1 := b.Min.X + int(d.Band[2]*float64(w))
	y0 := b.Min.Y + int(d.Band[1]*float64(h))
	y1 := b.Min.Y + int(d.Band[3]*float64(h))
	bandW := x1 - x0
	if bandW <= 4 || y1-y0 <= 4 {
		return false
	}

	at := rgbSampler(img)

	// 1) 每列亮像素计数（列投影），同时累计亮像素的 x/y 之和，
	//    以便后面算出每个簇的质心——位置判据比「数够几个簇」可靠得多。
	colCount := make([]int, bandW)
	colSumX := make([]int, bandW)
	colSumY := make([]int, bandW)
	for x := x0; x < x1; x++ {
		cx := x - x0
		for y := y0; y < y1; y++ {
			r, g, bb := at(x, y)
			mx := int(max3(r, g, bb))
			mn := int(min3(r, g, bb))
			if mx >= bright && mx-mn <= satMax {
				colCount[cx]++
				colSumX[cx] += cx
				colSumY[cx] += y - y0
			}
		}
	}

	// 2) 亮列阈值：取列峰的 15%（与 Python 一致），高于阈值的相邻列连成簇。
	maxCol := 0
	for _, c := range colCount {
		if c > maxCol {
			maxCol = c
		}
	}
	if maxCol == 0 {
		return false
	}
	colThresh := maxCol*15/100
	// 簇宽下限：Python 用 20 列（约占检测带宽的 1.4%），这里按比例换算并保底 8 列。
	minRunCols := bandW * 14 / 1000
	if minRunCols < 8 {
		minRunCols = 8
	}

	// 3) 连通亮列分簇，簇内总亮像素达标才算一个候选圆钮，并记录质心（归一化）。
	type cluster struct{ cx, cy float64 }
	var clusters []cluster
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
		if runW := cx - start; runW >= minRunCols && sum >= minPixels {
			// 质心 = 亮像素绝对坐标的均值。colSumX/colSumY 记的是**带内相对**坐标，
			// 所以要补回带原点：Σx = Σ(x-x0) + x0*像素数。
			clusters = append(clusters, cluster{
				cx: float64(sumX+x0*sum) / float64(sum) / float64(w),
				cy: float64(sumY+y0*sum) / float64(sum) / float64(h),
			})
		}
	}
	if len(clusters) < minClusters {
		return false
	}

	// 4) 位置判据：真正的战斗圆钮固定在底部一条横排上（洛克王国实测
	//    x≈0.664/0.728/0.786/0.847/0.908，y≈0.905）。只数簇的个数会在大地图上
	//    误报——地图上散布着大量又亮又低饱和的圆形图标，在检测带里同样能凑出
	//    好几个簇（实测 esc_map7/probe_map3/p_map_fresh 全部被判成战斗）。
	//    所以这里要求簇必须落到标定位置上，命中数够才算战斗。
	if len(d.Positions) == 0 {
		// 没标位置就退回「数簇」的老判据（兼容只配了 band 的档案）。
		return true
	}
	tolX, tolY := d.tolerances()
	minHits := d.MinHits
	if minHits <= 0 {
		minHits = minClusters
	}
	used := make([]bool, len(clusters))
	hits := 0
	for _, want := range d.Positions {
		for i, c := range clusters {
			if used[i] {
				continue
			}
			if absf(c.cx-want[0]) <= tolX && absf(c.cy-want[1]) <= tolY {
				used[i] = true
				hits++
				break
			}
		}
	}
	return hits >= minHits
}

func absf(v float64) float64 {
	if v < 0 {
		return -v
	}
	return v
}

func max3(a, b, c uint8) uint8 {
	if a >= b && a >= c {
		return a
	}
	if b >= c {
		return b
	}
	return c
}

func min3(a, b, c uint8) uint8 {
	if a <= b && a <= c {
		return a
	}
	if b <= c {
		return b
	}
	return c
}
