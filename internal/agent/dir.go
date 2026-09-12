package agent

import (
	"fmt"
	"math"
	"strings"
)

// Dir 是「方向移动」的抽象方向。
//
// 存在的理由（实测踩出来的）：
//
//	手游普遍用虚拟摇杆移动。早期让老师直接输出摇杆四坐标
//	（ACTION JOYSTICK cx=.. cy=.. tx=.. ty=..），结果小模型在这个任务上
//	反复纠结「cx/cy/tx/ty 到底是什么」，思考链 74s 打满预算仍然失败；
//	而摇杆中心本来就是个**设备常量**（同一游戏同一分辨率下永远一样），
//	让模型每帧重新回归它，既浪费预算又引入噪声。
//
// 改成离散方向后，模型只需要做一次 8 选 1 的分类——这正是小模型最擅长、
// 最稳定的输出形式。摇杆中心与推杆幅度交给 GameProfile（游戏档案），
// 由平台后端换算成实际触摸坐标；PC 上同一套方向语义落成 WASD，
// 手柄/十字键游戏上落成 dpad。**一套方向语义跨游戏、跨平台通用。**
type Dir string

const (
	DirUp        Dir = "up"
	DirDown      Dir = "down"
	DirLeft      Dir = "left"
	DirRight     Dir = "right"
	DirUpLeft    Dir = "up_left"
	DirUpRight   Dir = "up_right"
	DirDownLeft  Dir = "down_left"
	DirDownRight Dir = "down_right"
)

// 缩放方向（只被 ActionZoom 使用，不进 AllDirs——那是 MOVE 的 8 向语义）。
const (
	DirIn  Dir = "in"  // 放大
	DirOut Dir = "out" // 缩小
)

// AllDirs 是全部方向的固定顺序列表。
// 顺序固定是为了让提示词、日志、测试输出可复现（map 遍历顺序不稳定）。
var AllDirs = []Dir{
	DirUp, DirDown, DirLeft, DirRight,
	DirUpLeft, DirUpRight, DirDownLeft, DirDownRight,
}

// dirAlias 汇总各种写法 → 规范方向。
//
// 覆盖四类叫法，因为模型可能用任何一种：
//   - 英文规范名（up / down / left / right / up_left）
//   - 「前后左右」这类游戏语汇：第三人称游戏里摇杆向上 = 朝镜头前方走，
//     所以 forward 与 up 等价、backward 与 down 等价
//   - 罗盘方位全称（north / south / east / west）与不冲突的单字母 n / e
//   - 中文（上/前/左上/前左）
//
// ⚠️ 单字母 w / a / s / d 一律按 **WASD 键位语义**解释（w=前，a=左，s=后，d=右），
// 不按罗盘缩写解释。这两套语义在 w / d 上是冲突的（罗盘 w=西=左，WASD w=前进），
// 而游戏操作语境下 WASD 含义压倒性地常见。s 在两套里都指向「下/后」，不冲突。
var dirAlias = map[string]Dir{
	"up": DirUp, "u": DirUp, "north": DirUp, "n": DirUp,
	"forward": DirUp, "fwd": DirUp, "front": DirUp, "ahead": DirUp, "w": DirUp,
	"上": DirUp, "前": DirUp, "前进": DirUp, "向前": DirUp, "上方": DirUp, "北": DirUp,

	"down": DirDown, "south": DirDown, "s": DirDown,
	"back": DirDown, "backward": DirDown, "bwd": DirDown, "rear": DirDown,
	"下": DirDown, "后": DirDown, "后退": DirDown, "向后": DirDown, "下方": DirDown, "南": DirDown,

	"left": DirLeft, "l": DirLeft, "west": DirLeft, "a": DirLeft,
	"左": DirLeft, "左边": DirLeft, "向左": DirLeft, "西": DirLeft,

	"right": DirRight, "r": DirRight, "east": DirRight, "e": DirRight, "d": DirRight,
	"右": DirRight, "右边": DirRight, "向右": DirRight, "东": DirRight,

	"up_left": DirUpLeft, "upleft": DirUpLeft, "up-left": DirUpLeft,
	"left_up": DirUpLeft, "leftup": DirUpLeft, "forward_left": DirUpLeft,
	"nw": DirUpLeft, "northwest": DirUpLeft, "wa": DirUpLeft,
	"左上": DirUpLeft, "前左": DirUpLeft, "左前": DirUpLeft, "西北": DirUpLeft,

	"up_right": DirUpRight, "upright": DirUpRight, "up-right": DirUpRight,
	"right_up": DirUpRight, "rightup": DirUpRight, "forward_right": DirUpRight,
	"ne": DirUpRight, "northeast": DirUpRight, "wd": DirUpRight,
	"右上": DirUpRight, "前右": DirUpRight, "右前": DirUpRight, "东北": DirUpRight,

	"down_left": DirDownLeft, "downleft": DirDownLeft, "down-left": DirDownLeft,
	"left_down": DirDownLeft, "leftdown": DirDownLeft, "backward_left": DirDownLeft,
	"sw": DirDownLeft, "southwest": DirDownLeft, "sa": DirDownLeft,
	"左下": DirDownLeft, "后左": DirDownLeft, "左后": DirDownLeft, "西南": DirDownLeft,

	"down_right": DirDownRight, "downright": DirDownRight, "down-right": DirDownRight,
	"right_down": DirDownRight, "rightdown": DirDownRight, "backward_right": DirDownRight,
	"se": DirDownRight, "southeast": DirDownRight, "sd": DirDownRight,
	"右下": DirDownRight, "后右": DirDownRight, "右后": DirDownRight, "东南": DirDownRight,
}

// ParseDir 把各种写法归一化成 Dir。无法识别时返回错误。
//
// 容错点（都是模型输出的实际形态）：大小写不敏感、首尾空白、
// 用「-」或空格代替「_」（up-left / up left 都认）。
func ParseDir(s string) (Dir, error) {
	k := strings.TrimSpace(strings.ToLower(s))
	if k == "" {
		return "", fmt.Errorf("agent: 方向为空")
	}
	// up-left / up left / up  left → up_left
	k = strings.NewReplacer("-", "_", " ", "_", "\t", "_", "／", "_", "/", "_").Replace(k)
	for strings.Contains(k, "__") {
		k = strings.ReplaceAll(k, "__", "_")
	}
	k = strings.Trim(k, "_")

	if d, ok := dirAlias[k]; ok {
		return d, nil
	}
	return "", fmt.Errorf("agent: 无法识别的方向 %q", strings.TrimSpace(s))
}

// IsValid 报告方向是否合法（零值 "" 视为非法）。
func (d Dir) IsValid() bool {
	for _, x := range AllDirs {
		if x == d {
			return true
		}
	}
	return false
}

// IsDiagonal 报告是否为斜向（PC 键盘上斜向要同时按两个键）。
func (d Dir) IsDiagonal() bool {
	switch d {
	case DirUpLeft, DirUpRight, DirDownLeft, DirDownRight:
		return true
	}
	return false
}

// invSqrt2 = 1/√2，斜向分量。用同一个值保证八个方向推杆幅度一致——
// 否则斜向会比正向多推 41%，游戏里的移动速度就不一致了。
const invSqrt2 = math.Sqrt2 / 2

// Vector 返回方向的单位向量，坐标系为**屏幕坐标**（右为 +x，下为 +y）。
//
// 屏幕 y 轴向下是所有后端（Android 触摸 / Windows GDI）的一致约定，
// 这里统一按屏幕坐标返回，避免每个调用方各自记得要不要翻 y。
func (d Dir) Vector() (x, y float64) {
	switch d {
	case DirUp:
		return 0, -1
	case DirDown:
		return 0, 1
	case DirLeft:
		return -1, 0
	case DirRight:
		return 1, 0
	case DirUpLeft:
		return -invSqrt2, -invSqrt2
	case DirUpRight:
		return invSqrt2, -invSqrt2
	case DirDownLeft:
		return -invSqrt2, invSqrt2
	case DirDownRight:
		return invSqrt2, invSqrt2
	default:
		return 0, 0
	}
}

// Zh 返回中文名，用于日志与中文提示词。
func (d Dir) Zh() string {
	switch d {
	case DirUp:
		return "前（上）"
	case DirDown:
		return "后（下）"
	case DirLeft:
		return "左"
	case DirRight:
		return "右"
	case DirUpLeft:
		return "左前"
	case DirUpRight:
		return "右前"
	case DirDownLeft:
		return "左后"
	case DirDownRight:
		return "右后"
	default:
		return string(d)
	}
}

// DirListZh 返回「前/后/左/右/左前/右前/左后/右后」这样的中文枚举串，
// 用于拼提示词——比罗列八个英文名更省 token，模型也更少拼错。
func DirListZh() string {
	parts := make([]string, 0, len(AllDirs))
	for _, d := range AllDirs {
		parts = append(parts, string(d))
	}
	return strings.Join(parts, "|")
}
