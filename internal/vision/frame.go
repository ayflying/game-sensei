package vision

import "image"

// frame.go 收敛「两帧灰度图之间差多少」这一类判据的**唯一实现**。
//
// 为什么必须收敛：这个判据在四处被独立写过一遍——cmd/helper 的卡死判定、
// cmd/hunt 的探索推进判定、cmd/shot 的标定效果判定、以及各种一次性诊断脚本。
// 四处各写一份的代价不是代码量，而是**阈值无法比较**：一处 `Δ<4` 是「撞墙」，
// 另一处 `Δ<4` 可能是「画面完全静止」，复盘时谁也说不清说的是哪个 Δ。
// 统一到一个函数后，所有工具面对的是同一个数、同一个量纲（逐像素平均绝对差 0~255）。
//
// 量纲参考（洛克王国：世界，3200x2136，下采样到 320 宽后）：
//
//	0.0      完全静止（连续两帧逐字节相同）
//	1~3      只有环境动画/呼吸动作（角色没动，或输入没送达）
//	4~8      边界带：小步位移、或只有远处景物在动
//	10~60    角色确实在移动（3D 场景整体平移）
//	>60      转场/开地图/放技能特效这类整屏变化

// FrameDiff 返回两帧灰度图的逐像素平均绝对差（0~255）。
//
// 尺寸不一致或任一帧为空时返回 255（视为「变化很大」）：宁可漏判一次「没变化」，
// 也不要因为一次尺寸抖动（横竖屏切换、抓帧失败）误判成「画面静止」。
func FrameDiff(a, b *image.Gray) float64 {
	if a == nil || b == nil || a.Bounds() != b.Bounds() {
		return 255
	}
	bb := a.Bounds()
	if bb.Dx() == 0 || bb.Dy() == 0 {
		return 255
	}
	var sum uint64
	for y := bb.Min.Y; y < bb.Max.Y; y++ {
		row := y * a.Stride
		for x := bb.Min.X; x < bb.Max.X; x++ {
			d := int(a.Pix[row+x]) - int(b.Pix[row+x])
			if d < 0 {
				d = -d
			}
			sum += uint64(d)
		}
	}
	return float64(sum) / float64(bb.Dx()*bb.Dy())
}

// MeanGrayFrame 返回灰度帧的像素均值（0~255）。
//
// 与 MeanGray 的区别：那个吃彩色 image.Image（逐点转换，用于一次性判断），
// 这个吃已经是灰度的帧（逐字节直读，用于每步都在跑的循环里判断息屏黑帧）。
func MeanGrayFrame(g *image.Gray) float64 {
	if g == nil {
		return 0
	}
	bb := g.Bounds()
	n := bb.Dx() * bb.Dy()
	if n == 0 {
		return 0
	}
	var sum uint64
	for y := bb.Min.Y; y < bb.Max.Y; y++ {
		row := y * g.Stride
		for x := bb.Min.X; x < bb.Max.X; x++ {
			sum += uint64(g.Pix[row+x])
		}
	}
	return float64(sum) / float64(n)
}
