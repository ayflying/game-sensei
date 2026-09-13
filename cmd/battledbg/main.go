// cmd/battledbg 是为「战斗态判据为什么漏判」而写的量测器。
//
// 背景：Profile.IsBattle 依赖「检测带内亮且低饱和像素的列投影 → 连通亮列簇 →
// 质心必须落到标定位置」。实测（2026-09-13）它在**真实战斗**上判 world：
// 底部那条深蓝条被场景高亮背景抬高了列峰，15% 列阈值把真正的圆钮淹没，
// 只分出 2 个簇、0 个命中。列投影是**全局**统计，天生对背景亮度不鲁棒。
//
// 本工具换一个**局部**统计来量：对每个标定按钮位置，比较
//
//	「内盘平均亮度」 vs 「外环平均亮度」= 对比度
//
// 圆钮是奶油白、周边是深蓝条，所以真实按钮处对比度应为正且明显；
// 背景再亮，只要按钮相对周边更亮，这个量就稳。
//
// 用法：
//
//	battledbg -game nrc -in frame.png
//	battledbg -game nrc -dir .workbuddy/demos/pet_run15/color -limit 40
package main

import (
	"flag"
	"fmt"
	"image"
	"image/jpeg"
	"image/png"
	"math"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/ayflying/game-sensei/internal/game"
)

// probe 是一个被量测的候选按钮位置（归一化坐标 + 名字 + 是否仅战斗态存在）。
type probe struct {
	name       string
	x, y       float64
	battleOnly bool
}

func main() {
	var (
		gameName = flag.String("game", "nrc", "档案名")
		in       = flag.String("in", "", "单张图片路径")
		dir      = flag.String("dir", "", "目录：批量量测其中所有图片")
		limit    = flag.Int("limit", 0, "批量模式下最多处理几张（0=全部）")
		rIn      = flag.Float64("rin", 0.011, "内盘半径（按宽度归一化）")
		rOut     = flag.Float64("rout", 0.026, "外环外半径（按宽度归一化）")
		bright   = flag.Float64("bright", 150, "「亮」阈值（与档案一致）")
		satMax   = flag.Float64("satmax", 70, "「低饱和」阈值（与档案一致）")
		quiet    = flag.Bool("quiet", true, "批量模式只打每张图的汇总行")
	)
	flag.Parse()

	prof, err := game.Load(*gameName)
	if err != nil {
		fmt.Fprintln(os.Stderr, "加载档案失败:", err)
		os.Exit(1)
	}
	probes := buildProbes(prof)
	fmt.Printf("档案 %s | 探测点 %d 个: ", prof.Name, len(probes))
	for _, p := range probes {
		tag := ""
		if p.battleOnly {
			tag = "*"
		}
		fmt.Printf("%s%s(%.3f,%.3f) ", p.name, tag, p.x, p.y)
	}
	fmt.Println("\n（* = 仅战斗态存在的按钮）")

	files := collect(*in, *dir, *limit)
	if len(files) == 0 {
		fmt.Println("没有输入图片")
		return
	}
	fmt.Printf("内盘 r=%.3f 外环 r=%.3f | 亮阈值 %.0f 饱和上限 %.0f\n\n", *rIn, *rOut, *bright, *satMax)

	passCounts := map[int]int{}
	for _, f := range files {
		img, err := openImage(f)
		if err != nil {
			fmt.Printf("%-44s 读取失败: %v\n", base(f), err)
			continue
		}
		verdict := prof.IsBattle(img)
		rows, nPass := measure(img, probes, *rIn, *rOut, *bright, *satMax)
		passCounts[nPass]++
		if *quiet {
			fmt.Printf("%-44s IsBattle=%-5v 局部命中=%d/%d  对比度[%s]\n",
				base(f), verdict, nPass, len(rows), strings.Join(rows, " "))
		} else {
			fmt.Printf("── %s  IsBattle=%v  局部命中=%d/%d\n", base(f), verdict, nPass, len(rows))
			for i, p := range probes {
				fmt.Printf("     %-14s (%.3f,%.3f)  %s\n", p.name, p.x, p.y, rows[i])
			}
		}
	}
	fmt.Printf("\n局部命中数分布（命中数 → 张数）：")
	keys := make([]int, 0, len(passCounts))
	for k := range passCounts {
		keys = append(keys, k)
	}
	sort.Ints(keys)
	for _, k := range keys {
		fmt.Printf(" %d→%d", k, passCounts[k])
	}
	fmt.Println()
}

// buildProbes 取档案标定的 5 个战斗按钮位置，再加上「聚能」这类仅战斗态存在的
// 按钮位置——它们是战斗态独有的指纹，做判据时价值最高。
func buildProbes(prof *game.Profile) []probe {
	var ps []probe
	if prof.BattleDetect != nil {
		for i, pos := range prof.BattleDetect.Positions {
			ps = append(ps, probe{name: fmt.Sprintf("bar%d", i+1), x: pos[0], y: pos[1], battleOnly: true})
		}
	}
	for _, b := range prof.Buttons {
		if b.State != "battle" {
			continue
		}
		if b.Name == "battle_energy" || b.Name == "battle_catch" || b.Name == "battle_skill" {
			ps = append(ps, probe{name: b.Name, x: b.Pos[0], y: b.Pos[1], battleOnly: true})
		}
	}
	return ps
}

// measure 返回每个探测点的可读结论，以及「通过局部对比度判据」的个数。
//
// 单点判据（三条全满足才算命中）：
//  1. 内盘比外环亮 contrast ≥ contrastMin——圆钮亮于周边条；
//  2. 内盘低饱和 innerSat ≤ satMax——奶油白，不是彩色背景；
//  3. 内盘本身够亮 innerLuma ≥ bright——排除「周边更暗」造成的假对比（如深色文字）。
func measure(img image.Image, probes []probe, rIn, rOut, bright, satMax float64) ([]string, int) {
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	at := sampler(img)
	rInPx := rIn * float64(w)
	rOutPx := rOut * float64(w)
	const contrastMin = 18.0

	out := make([]string, 0, len(probes))
	pass := 0
	for _, p := range probes {
		px := p.x * float64(w)
		py := p.y * float64(h)
		var inL, inS, inN, inBright float64
		var ringL, ringN float64
		x0 := int(px - rOutPx - 1)
		x1 := int(px + rOutPx + 1)
		y0 := int(py - rOutPx - 1)
		y1 := int(py + rOutPx + 1)
		for y := y0; y <= y1; y++ {
			for x := x0; x <= x1; x++ {
				if x < b.Min.X || y < b.Min.Y || x >= b.Max.X || y >= b.Max.Y {
					continue
				}
				d := math.Hypot(float64(x)-px, float64(y)-py)
				r, g, bb := at(x, y)
				l := 0.299*float64(r) + 0.587*float64(g) + 0.114*float64(bb)
				s := float64(max3(r, g, bb) - min3(r, g, bb))
				switch {
				case d <= rInPx:
					inL += l
					inS += s
					inN++
					if l >= bright && s <= satMax {
						inBright++
					}
				case d >= rInPx*1.35 && d <= rOutPx:
					ringL += l
					ringN++
				}
			}
		}
		if inN == 0 || ringN == 0 {
			out = append(out, "n/a")
			continue
		}
		inL /= inN
		inS /= inN
		ringL /= ringN
		contrast := inL - ringL
		ok := contrast >= contrastMin && inS <= satMax && inL >= bright
		if ok {
			pass++
		}
		mark := "  "
		if ok {
			mark = "✅"
		}
		out = append(out, fmt.Sprintf("%s%d", mark, int(math.Round(contrast))))
	}
	return out, pass
}

func collect(in, dir string, limit int) []string {
	var files []string
	if in != "" {
		files = append(files, in)
	}
	if dir != "" {
		ents, err := os.ReadDir(dir)
		if err == nil {
			for _, e := range ents {
				if e.IsDir() {
					continue
				}
				ext := strings.ToLower(filepath.Ext(e.Name()))
				if ext == ".png" || ext == ".jpg" || ext == ".jpeg" {
					files = append(files, filepath.Join(dir, e.Name()))
				}
			}
		}
		sort.Strings(files)
	}
	if limit > 0 && len(files) > limit {
		files = files[:limit]
	}
	return files
}

func openImage(p string) (image.Image, error) {
	f, err := os.Open(p)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	if strings.EqualFold(filepath.Ext(p), ".png") {
		return png.Decode(f)
	}
	return jpeg.Decode(f)
}

func base(p string) string {
	return filepath.Base(p)
}

type pixelFn func(x, y int) (uint8, uint8, uint8)

func sampler(img image.Image) pixelFn {
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
	default:
		return func(x, y int) (uint8, uint8, uint8) {
			r, g, b, _ := img.At(x, y).RGBA()
			return uint8(r >> 8), uint8(g >> 8), uint8(b >> 8)
		}
	}
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
