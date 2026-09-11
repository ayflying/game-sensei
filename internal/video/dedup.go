package video

import (
	"image"
	"image/color"
	"math/bits"

	"github.com/ayflying/game-sensei/internal/vision"
)

// 去重与切段都建立在 dHash（difference hash，差异哈希）之上。
//
// 为什么用感知哈希而不是逐像素比较：录屏经过有损压缩，同一静止画面
// 相邻两帧的像素值也有细微差异，逐像素比较会把噪声当成变化；
// dHash 只看「相邻像素的明暗关系」，对压缩噪声、亮度微扰都稳定，
// 而且 64 位哈希用汉明距离比较，比任何逐像素方案都快几个数量级。
//
// 为什么不用画面均值（vision.MeanGray）：均值只看整体亮度，
// 「镜头从左边转到右边」这种等亮度的剧烈变化会被判为没变。

// dHash 的比较网格：9×8 个采样点，横向比较 8 次得到 8×8 = 64 位。
const (
	hashW = 9
	hashH = 8
)

// preDownWidth 是算哈希前的预降采样宽度。
//
// 直接在大图上按 9×8 网格做区域平均太慢（每帧要扫两百万像素）；
// 先靠 vision.Downscale 的区域平均快速降到 64 宽，再在小图上取网格，
// 既保住了抗噪性，又把每帧的哈希开销压到千级采样。
const preDownWidth = 64

// dHash 计算一帧的 64 位差异哈希。
//
// 做法：拉伸到 9×8 灰度网格（**不保持宽高比**，这样不同分辨率、
// 不同宽高比的画面落在同一个几何网格上才可比），逐行比较相邻采样点
// 的明暗关系，亮于右侧记 1、否则记 0。
func dHash(img image.Image) uint64 {
	if img == nil {
		return 0
	}
	small := vision.Downscale(img, preDownWidth)
	g := grayGrid(small, hashW, hashH)

	var h uint64
	var bit uint
	for y := 0; y < hashH; y++ {
		for x := 0; x < hashW-1; x++ {
			if g[y*hashW+x] < g[y*hashW+x+1] {
				h |= 1 << bit
			}
			bit++
		}
	}
	return h
}

// grayGrid 把 img 拉伸采样成 w×h 的灰度网格（每个格子取区域平均）。
//
// 这是 dHash 专用的小工具，刻意不放进 vision 包：vision 的语义是
// 「给老师看的画面预处理」，而这里是「算指纹」，两者都不该被对方的
// 需求拖着改。
func grayGrid(img image.Image, w, h int) []uint8 {
	out := make([]uint8, w*h)
	if img == nil {
		return out
	}
	b := img.Bounds()
	sw, sh := b.Dx(), b.Dy()
	if sw == 0 || sh == 0 {
		return out
	}

	for gy := 0; gy < h; gy++ {
		y0 := b.Min.Y + gy*sh/h
		y1 := b.Min.Y + (gy+1)*sh/h
		if y1 <= y0 {
			y1 = y0 + 1
		}
		for gx := 0; gx < w; gx++ {
			x0 := b.Min.X + gx*sw/w
			x1 := b.Min.X + (gx+1)*sw/w
			if x1 <= x0 {
				x1 = x0 + 1
			}
			var sum, n uint64
			for y := y0; y < y1; y++ {
				for x := x0; x < x1; x++ {
					c := color.GrayModel.Convert(img.At(x, y)).(color.Gray)
					sum += uint64(c.Y)
					n++
				}
			}
			if n == 0 {
				n = 1
			}
			out[gy*w+gx] = uint8(sum / n)
		}
	}
	return out
}

// Hamming 返回两个哈希的汉明距离（不同的二进制位数，0~64）。
func Hamming(a, b uint64) int {
	return bits.OnesCount64(a ^ b)
}

// Collapse 折叠连续重复的帧，只留下「画面确实变了」的关键帧。
//
// 比较对象是**上一个被保留的帧**，而不是上一帧——这是关键：
// 缓慢平移的画面（相邻帧差异都低于阈值，但累计几十帧后早已面目全非）
// 若按相邻帧比较会被整段折叠掉，按保留帧比较才能捕获累积漂移。
//
// threshold < 0 表示不做去重，原样返回（此时关键帧数量等于原始帧数）。
func Collapse(frames []Frame, threshold int) []Frame {
	if len(frames) == 0 {
		return nil
	}
	if threshold < 0 {
		out := make([]Frame, len(frames))
		copy(out, frames)
		return out
	}

	out := make([]Frame, 0, len(frames))
	out = append(out, frames[0])
	for _, f := range frames[1:] {
		if Hamming(f.Hash, out[len(out)-1].Hash) > threshold {
			out = append(out, f)
		}
	}
	return out
}

// Renumber 把序列重新编号成 1..N 的连续值，并把原有序号记进 Raw。
//
// 为什么需要：去重之后帧序号会变得稀疏（如 12、37、38、55…），
// 而动作段记录的是「关键帧下标区间」，用稀疏序号既要人工换算又容易看错。
// 重排成连续编号后，段区间和报告可以直接对照；原序号留在 Raw 里，
// 需要回溯到抽帧产物时随时能对上。
//
// 只应调用一次：重复调用会把已经重排过的编号再当成「原序号」覆盖掉 Raw。
func Renumber(frames []Frame) []Frame {
	out := make([]Frame, len(frames))
	for i, f := range frames {
		out[i] = Frame{
			Index: i + 1,
			Raw:   f.Index,
			File:  f.File,
			AtMs:  f.AtMs,
			Hash:  f.Hash,
		}
	}
	return out
}

// Segment 是切出来的一个「动作段」。
//
// 段的语义是「画面结构基本稳定的一段时间」——玩家在做同一件事，
// 比如「朝目标走」「翻菜单」「打一场战斗」。老师是按段判读的，
// 不是按帧判读：单帧看不出动作，一段连续帧才能看出意图。
type Segment struct {
	// Index 段序号，从 0 开始。
	Index int `json:"index"`
	// From / To 是段覆盖的关键帧下标（闭区间）。
	From int `json:"from"`
	To   int `json:"to"`
	// StartMs / EndMs 是段在视频中的时间范围。
	StartMs int64 `json:"start_ms"`
	EndMs   int64 `json:"end_ms"`
	// Motion 是段内相邻关键帧哈希距离之和，可当「这段有多热闹」的粗略指标。
	// 值高多为快速移动或转视角，值低多为静止观察或停在菜单里。
	Motion int `json:"motion"`
}

// SegmentFrames 按相邻关键帧的哈希距离切出动作段。
//
// 切段阈值应当明显大于去重阈值：去重关心「画面是否变了」，
// 切段关心「画面是否**大改**」——换场景、开面板、进战斗这类结构性变化，
// 而不是走路时草地的细微流动。
//
// minFrames 是段的最小帧数，作用是合并碎片：小于它的段不切出去，
// 否则快速运动时会出现大量只有一两帧的碎段，送审时既费算力又无信息。
func SegmentFrames(frames []Frame, threshold, minFrames int) []Segment {
	if len(frames) == 0 {
		return nil
	}
	if minFrames < 1 {
		minFrames = 1
	}

	// 先找出所有段的起始下标
	starts := []int{0}
	for i := 1; i < len(frames); i++ {
		if i-starts[len(starts)-1] < minFrames {
			continue // 当前段还太短，先不切
		}
		if Hamming(frames[i].Hash, frames[i-1].Hash) > threshold {
			starts = append(starts, i)
		}
	}

	segs := make([]Segment, 0, len(starts))
	for si, s := range starts {
		e := len(frames) - 1
		if si+1 < len(starts) {
			e = starts[si+1] - 1
		}
		segs = append(segs, buildSegment(frames, si, s, e))
	}
	return segs
}

// buildSegment 组装一个段并统计其内部变化量。
func buildSegment(frames []Frame, index, from, to int) Segment {
	s := Segment{
		Index:   index,
		From:    from,
		To:      to,
		StartMs: frames[from].AtMs,
		EndMs:   frames[to].AtMs,
	}
	for i := from + 1; i <= to; i++ {
		s.Motion += Hamming(frames[i].Hash, frames[i-1].Hash)
	}
	return s
}

// Reps 返回段内均匀分布的 k 个代表帧下标（含段首与段尾，闭区间）。
//
// 送审给老师的帧数必须**有上限**：一段可能有几十个关键帧，
// 全送过去既超上下文又让模型抓不住重点。均匀取样能覆盖
// 「动作开始 → 过程 → 结果」，比只送首尾或只送中间更有信息量。
//
// k <= 0 时取 1；k 超过段长时返回段内全部下标。
func (s Segment) Reps(k int) []int {
	n := s.To - s.From + 1
	if n <= 0 {
		return nil
	}
	if k <= 0 {
		k = 1
	}
	if k > n {
		k = n
	}
	if k == 1 {
		// 单帧取段中点：首帧往往还是上一个动作的残留画面
		return []int{s.From + n/2}
	}

	out := make([]int, 0, k)
	for i := 0; i < k; i++ {
		idx := s.From + i*(n-1)/(k-1)
		out = append(out, idx)
	}
	return out
}
