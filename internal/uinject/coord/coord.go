// Package coord 负责「屏幕像素坐标 ↔ uinput 虚拟触摸屏原始坐标」的换算。
//
// 为什么单独成包：设备侧守护进程（internal/uinject/device，只在 android 上编译）
// 与宿主侧标定都要用这套换算，而它必须是可在 Windows 上单测的纯函数——真机标定
// 一次很贵，不能让「换了个旋转方向就点错位置」这种事靠猜。
//
// 换算规则与 Android InputReader 的 rotateAndScale 互逆：
//
//	rot   0 : (xn, yn)
//	rot  90 : (1-yn, xn)
//	rot 180 : (1-xn, 1-yn)
//	rot 270 : (yn, 1-xn)
//
// 其中 xn/yn 是「当前旋转下」屏幕像素归一化到 0..1，结果是虚拟设备的原始
// 坐标归一化值，再乘设备声明的值域上限 rx/ry。
package coord

// NormalizeRot 把任意角度吸附到 0/90/180/270；非法值按 0 处理。
func NormalizeRot(deg int) int {
	switch ((deg % 360) + 360) % 360 {
	case 90:
		return 90
	case 180:
		return 180
	case 270:
		return 270
	default:
		return 0
	}
}

// RotatedSize 返回物理竖屏尺寸 (physW×physH) 在给定旋转下的屏幕逻辑尺寸。
//
// Android 的触摸映射用的是「旋转后」的显示尺寸，横屏时宽高互换，这一步搞错
// 会让所有点击整体镜像或错位。
func RotatedSize(physW, physH, rot int) (w, h int) {
	if NormalizeRot(rot) == 90 || NormalizeRot(rot) == 270 {
		return physH, physW
	}
	return physW, physH
}

// Rotate 把屏幕像素点 (x, y) 换算成虚拟触摸屏原始坐标。
//
// rot 为显示旋转角度；w/h 为该旋转下的屏幕逻辑像素；rx/ry 为虚拟设备声明的
// 原始值域上限（默认取物理竖屏的宽/高）。结果已夹到 [0, rx] / [0, ry]。
func Rotate(rot, w, h, rx, ry, x, y int) (int32, int32) {
	if w <= 0 || h <= 0 || rx <= 0 || ry <= 0 {
		return 0, 0
	}
	xn := float64(x) / float64(w)
	yn := float64(y) / float64(h)
	var rxn, ryn float64
	switch NormalizeRot(rot) {
	case 90:
		rxn, ryn = 1-yn, xn
	case 180:
		rxn, ryn = 1-xn, 1-yn
	case 270:
		rxn, ryn = yn, 1-xn
	default:
		rxn, ryn = xn, yn
	}
	return clampRaw(rxn, rx), clampRaw(ryn, ry)
}

func clampRaw(n float64, max int) int32 {
	if n < 0 {
		return 0
	}
	if n > 1 {
		return int32(max)
	}
	return int32(n * float64(max))
}

// InferRot 在没有可靠旋转读数时，用屏幕逻辑尺寸猜一个初始旋转：
// 竖屏（h≥w）按 0 度，横屏按 90 度。270 度与 90 度在「尺寸」上不可区分，
// 若实际点按整体镜像，改传 -rot 270 即可（真实生效值必须由标定确认）。
func InferRot(w, h int) int {
	if w > h {
		return 90
	}
	return 0
}
