// Package student 是「量化学生」运行时：加载 trainer 导出的权重文件，
// 用纯 Go 前向传播对单帧观测做决策，实现 agent.Actor 接口接入实时回路。
//
// 设计约束：
//   - 零 CGO：不引 onnxruntime_go、不挂 dll。网络只有 3 层 CNN，
//     48x64 输入下纯 Go 前向 <1ms，远快于 30FPS 节拍。
//   - 跨游戏通用：网络结构固定（方向 8 选 1 + tap 坐标回归），
//     换游戏只换权重文件，Go 代码不动。
//
// 权重文件由 trainer/train.py 导出，格式：
//
//	{ "format": "game-sensei-student-weights", "version": 1,
//	  "arch": { "hidden": 64, "classes": [...] },
//	  "weights": { "conv1_w": [...], ... } }
//
// 前向顺序必须与 train.py 的 StudentNet.forward 完全一致：
//	conv1(3x3)+relu+pool2 -> conv2(3x3)+relu+pool2 -> conv3(3x3)+relu
//	-> GAP -> fc+relu -> cls_head / coord_head+sigmoid
package student

import (
	"encoding/json"
	"fmt"
	"image"
	"math"
	"os"
)

// Classes 与 trainer/train.py 的 CLS 一一对应（索引即标签，顺序不可改）。
//
// 方向类取 8 向，与 agent.AllDirs 同集合——L1 动作空间是 8 向，学生头若只留
// 4 向就无法表达老师实际发出的斜向移动（实测 nrc 真机示范的移动全是斜向），
// 只能把 up_right/up_left 硬塞进 up，等于系统性错标。
// 顺序漂移会让权重标签错位且**不会报错**，故 Load 会校验 arch.classes。
var Classes = []string{
	"up", "down", "left", "right",
	"up_left", "up_right", "down_left", "down_right",
	"tap", "press", "wait", "none",
}

// InW/InH 是网络输入尺寸（与 train.py 的 center_crop_resize 目标一致）。
const (
	InW = 64
	InH = 48
)

type weightsFile struct {
	Format  string `json:"format"`
	Version int    `json:"version"`
	Arch    struct {
		Hidden  int      `json:"hidden"`
		Classes []string `json:"classes"`
	} `json:"arch"`
	Weights map[string][]float32 `json:"weights"`
	Meta    struct {
		ValAcc float64 `json:"val_acc"`
	} `json:"meta"`
}

// Net 是加载好的学生网络。
type Net struct {
	hidden int

	conv1W []float32 // [8,1,3,3]
	conv1B []float32 // [8]
	conv2W []float32 // [16,8,3,3]
	conv2B []float32
	conv3W []float32 // [24,16,3,3]
	conv3B []float32
	fcW    []float32 // [hidden,24]
	fcB    []float32
	clsW   []float32 // [len(Classes),hidden]
	clsB   []float32
	coordW []float32 // [2,hidden]
	coordB []float32

	meta struct {
		valAcc float64
	}
}

// Load 从权重文件加载网络。
func Load(path string) (*Net, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var f weightsFile
	if err := json.Unmarshal(raw, &f); err != nil {
		return nil, fmt.Errorf("解析权重文件失败: %w", err)
	}
	if f.Format != "game-sensei-student-weights" {
		return nil, fmt.Errorf("不是学生权重文件: %q", f.Format)
	}
	if f.Version != 1 {
		return nil, fmt.Errorf("不支持的权重版本: %d", f.Version)
	}
	if err := validateClasses(f.Arch.Classes); err != nil {
		return nil, err
	}
	w := f.Weights
	n := &Net{hidden: f.Arch.Hidden}
	n.meta.valAcc = f.Meta.ValAcc
	need := map[string]int{
		"conv1_w": 8 * 1 * 9, "conv1_b": 8,
		"conv2_w": 16 * 8 * 9, "conv2_b": 16,
		"conv3_w": 24 * 16 * 9, "conv3_b": 24,
		"fc_w": f.Arch.Hidden * 24, "fc_b": f.Arch.Hidden,
		"cls_w": len(Classes) * f.Arch.Hidden, "cls_b": len(Classes),
		"coord_w": 2 * f.Arch.Hidden, "coord_b": 2,
	}
	for k, sz := range need {
		if len(w[k]) != sz {
			return nil, fmt.Errorf("权重 %s 长度 %d != 期望 %d", k, len(w[k]), sz)
		}
	}
	n.conv1W, n.conv1B = w["conv1_w"], w["conv1_b"]
	n.conv2W, n.conv2B = w["conv2_w"], w["conv2_b"]
	n.conv3W, n.conv3B = w["conv3_w"], w["conv3_b"]
	n.fcW, n.fcB = w["fc_w"], w["fc_b"]
	n.clsW, n.clsB = w["cls_w"], w["cls_b"]
	n.coordW, n.coordB = w["coord_w"], w["coord_b"]
	return n, nil
}

// validateClasses 校验权重声明的类别与运行时规范逐项一致。
//
// 为什么必须逐项（而不是只看集合）：logits 是按**下标**取 Classes 的，
// 顺序不同就会把「预测出的 up」当成「down」执行——模型照跑不误，
// 只是动作全错。这是「不报错但决策全错」，只能在加载期挡掉。
//
// 旧格式（4 向头）权重会落进「缺少类别」分支：这里特意把缺失项列出来，
// 因为它们的失败原因不是坏文件，而是**结构上无法表达斜向移动**。
func validateClasses(got []string) error {
	if len(got) == 0 {
		return fmt.Errorf("权重未声明类别（arch.classes 为空）")
	}
	known := map[string]bool{}
	for _, c := range Classes {
		known[c] = true
	}
	seen := map[string]bool{}
	for _, c := range got {
		if seen[c] {
			return fmt.Errorf("权重类别重复: %q", c)
		}
		seen[c] = true
		if !known[c] {
			return fmt.Errorf("权重含运行时无法识别的类别 %q（权重与代码版本不匹配）", c)
		}
	}
	if len(got) != len(Classes) {
		var missing []string
		for _, c := range Classes {
			if !seen[c] {
				missing = append(missing, c)
			}
		}
		return fmt.Errorf("权重只声明了 %d/%d 个类别，缺少 %v —— "+
			"这类权重（多为 4 向旧格式）无法表达 L1 的斜向移动，请用当前 trainer 重新训练",
			len(got), len(Classes), missing)
	}
	for i, c := range Classes {
		if got[i] != c {
			return fmt.Errorf("权重类别顺序与运行时不一致: [%d] %q != %q（拒绝加载）", i, got[i], c)
		}
	}
	return nil
}

// Meta 返回训练时的验证准确率（用于日志展示）。
func (n *Net) MetaValAcc() float64 { return n.meta.valAcc }

// Decide 实现 agent.Actor（本包不 import agent，避免依赖环——见 Adapter）。
// 输入任意尺寸灰度帧：内部居中裁剪到 64:48 再最近邻缩放到 64x48（与训练一致）。
func (n *Net) Decide(frame *image.Gray) (Action, error) {
	if frame == nil || frame.Bounds().Dx() == 0 {
		return Action{}, nil
	}
	x := preprocess(frame)
	logits, coords := n.forward(x)
	// argmax
	best, bi := logits[0], 0
	for i := 1; i < len(logits); i++ {
		if logits[i] > best {
			best, bi = logits[i], i
		}
	}
	return Action{Class: Classes[bi], X: float64(coords[0]), Y: float64(coords[1])}, nil
}

// Action 是学生的原始输出（类别 + 坐标）。
// cmd/helper 的适配层负责把它翻译成 agent.Action（L1）。
type Action struct {
	Class string // Classes 之一
	X, Y  float64 // tap 类别时的归一化坐标
}

// ForwardRaw 直接对 48x64 归一化输入做前向，暴露给一致性校验
// （.workbuddy 下的 parity 脚本用）与未来可能的批量推理场景。
func (n *Net) ForwardRaw(x []float32) (logits, coords []float32) {
	return n.forward(x)
}

// forward 是纯 Go 前向传播，布局与 train.py 逐层一致。
func (n *Net) forward(x []float32) (logits, coords []float32) {
	// conv1: [1,48,64] -> [8,46,62]
	c1 := conv3x3(x, 1, InH, InW, n.conv1W, n.conv1B, 8)
	relu(c1)
	p1 := maxpool2(c1, 8, 46, 62) // [8,23,31]

	// conv2: -> [16,21,29]
	c2 := conv3x3(p1, 8, 23, 31, n.conv2W, n.conv2B, 16)
	relu(c2)
	p2 := maxpool2(c2, 16, 21, 29) // [16,10,14]

	// conv3: -> [24,8,12]
	c3 := conv3x3(p2, 16, 10, 14, n.conv3W, n.conv3B, 24)
	relu(c3)

	// GAP: [24]
	gap := make([]float32, 24)
	inv := float32(1.0 / float32(8*12))
	for ch := 0; ch < 24; ch++ {
		var s float32
		base := ch * 8 * 12
		for i := 0; i < 8*12; i++ {
			s += c3[base+i]
		}
		gap[ch] = s * inv
	}

	// fc + relu: [hidden]
	h := make([]float32, n.hidden)
	for o := 0; o < n.hidden; o++ {
		var s float32
		row := n.fcW[o*24 : o*24+24]
		for ch := 0; ch < 24; ch++ {
			s += row[ch] * gap[ch]
		}
		h[o] = s + n.fcB[o]
		if h[o] < 0 {
			h[o] = 0
		}
	}

	// cls head（类别数由 Classes 决定，不写字面量——避免改类别时漏改这里）
	logits = make([]float32, len(Classes))
	for o := 0; o < len(Classes); o++ {
		var s float32
		row := n.clsW[o*n.hidden : o*n.hidden+n.hidden]
		for i := 0; i < n.hidden; i++ {
			s += row[i] * h[i]
		}
		logits[o] = s + n.clsB[o]
	}

	// coord head + sigmoid
	coords = make([]float32, 2)
	for o := 0; o < 2; o++ {
		var s float32
		row := n.coordW[o*n.hidden : o*n.hidden+n.hidden]
		for i := 0; i < n.hidden; i++ {
			s += row[i] * h[i]
		}
		coords[o] = float32(1.0 / (1.0 + math.Exp(-float64(s+n.coordB[o]))))
	}
	return logits, coords
}

// preprocess 把任意灰度帧转成 [1*48*64] 的输入向量：
// 居中裁剪到 64:48 比例 + 最近邻缩放 + 归一化到 0~1。
func preprocess(frame *image.Gray) []float32 {
	b := frame.Bounds()
	W, H := b.Dx(), b.Dy()
	// 居中裁剪窗口（源图上取多大区域）
	var sx, sy, sw, sh int
	if float64(W)/float64(H) > float64(InW)/float64(InH) {
		sh = H
		sw = H * InW / InH
		sx = (W - sw) / 2
		sy = 0
	} else {
		sw = W
		sh = W * InH / InW
		sx = 0
		sy = (H - sh) / 2
	}

	out := make([]float32, InH*InW)
	for y := 0; y < InH; y++ {
		srcY := sy + y*sh/InH
		row := (srcY-b.Min.Y)*frame.Stride - b.Min.X
		dst := y * InW
		for x := 0; x < InW; x++ {
			srcX := sx + x*sw/InW
			out[dst+x] = float32(frame.Pix[row+srcX]) / 255.0
		}
	}
	return out
}

// conv3x3 做有效 3x3 卷积（无 padding）。inW/inC 是输入的通道与尺寸，
// w 布局 [outC,inC,3,3]（与 torch Conv2d weight 一致）。
func conv3x3(in []float32, inC, inH, inW int, w, b []float32, outC int) []float32 {
	outH, outW := inH-2, inW-2
	out := make([]float32, outC*outH*outW)
	for oc := 0; oc < outC; oc++ {
		wBase := oc * inC * 9
		for y := 0; y < outH; y++ {
			for x := 0; x < outW; x++ {
				var s float32
				for ic := 0; ic < inC; ic++ {
					inBase := ic*inH*inW + y*inW + x
					wb := wBase + ic*9
					// 3x3 核展开
					s += w[wb+0]*in[inBase] + w[wb+1]*in[inBase+1] + w[wb+2]*in[inBase+2]
					s += w[wb+3]*in[inBase+inW] + w[wb+4]*in[inBase+inW+1] + w[wb+5]*in[inBase+inW+2]
					s += w[wb+6]*in[inBase+2*inW] + w[wb+7]*in[inBase+2*inW+1] + w[wb+8]*in[inBase+2*inW+2]
				}
				out[oc*outH*outW+y*outW+x] = s + b[oc]
			}
		}
	}
	return out
}

// relu 原地修正。
func relu(v []float32) {
	for i := range v {
		if v[i] < 0 {
			v[i] = 0
		}
	}
}

// maxpool2 做 2x2 最大池化（尺寸为奇数时丢弃最后一行/列）。
func maxpool2(in []float32, c, h, w int) []float32 {
	oh, ow := h/2, w/2
	out := make([]float32, c*oh*ow)
	for ch := 0; ch < c; ch++ {
		for y := 0; y < oh; y++ {
			for x := 0; x < ow; x++ {
				i0 := ch*h*w + (2*y)*w + 2*x
				i1 := i0 + 1
				i2 := i0 + w
				i3 := i2 + 1
				m := in[i0]
				if in[i1] > m {
					m = in[i1]
				}
				if in[i2] > m {
					m = in[i2]
				}
				if in[i3] > m {
					m = in[i3]
				}
				out[ch*oh*ow+y*ow+x] = m
			}
		}
	}
	return out
}

