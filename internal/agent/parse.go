package agent

import (
	"errors"
	"fmt"
	"regexp"
	"strconv"
	"strings"
	"time"
)

// ErrNoAction 表示文本里找不到可识别的动作（老师没给出动作，或格式不认识）。
var ErrNoAction = errors.New("agent: 文本中未找到可识别的动作")

// 各动作在「模型没给 dur」时的默认时长（毫秒）。
// 这些只是兜底值：移动给得偏长，是因为一次移动指令必须让画面产生可观测位移，
// 太短等于原地抖一下，学生学不到任何东西。
const (
	DefaultMoveMs     = 1000
	DefaultSwipeMs    = 300
	DefaultHoldMs     = 600
	DefaultJoystickMs = 1000
)

// 老师（VLM）输出的动作协议，一行一个动作，便于解析与人工核对：
//
//	ACTION TAP x=0.79 y=0.75
//	ACTION SWIPE x=0.50 y=0.50 x2=0.30 y2=0.50 dur=400
//	ACTION HOLD x=0.50 y=0.50 dur=600
//	ACTION JOYSTICK cx=0.21 cy=0.69 tx=0.81 ty=0.69 dur=1200
//	ACTION KEY code=back
//	ACTION MOVE dx=10 dy=0
//	ACTION WAIT
//
// 解析是**容错**的：大小写不敏感、全角半角标点都认、键值顺序任意、
// 允许和中文说明混在一行（只要动作片段本身完整）。原因很现实——
// 本地小模型输出格式不稳定，严格解析会大量丢样本，而丢样本比解析宽松更糟。

// verbKind 把各种叫法映射到 ActionKind。
//
// ⚠️ 顺序有讲究：indexWord 做词边界匹配，但中文靠子串匹配，
// 所以「按键」必须排在「按」类词之前，否则「按键」会被拆成「按」。
// 同理 keypress 里的 press 靠词边界天然排除，无需特殊处理。
var verbKind = []struct {
	word string
	kind ActionKind
}{
	{"longpress", ActionLongPress},
	{"long_press", ActionLongPress},
	{"hold", ActionLongPress},
	{"长按", ActionLongPress},
	{"joystick", ActionJoystick},
	{"stick", ActionJoystick},
	{"摇杆", ActionJoystick},
	{"swipe", ActionSwipe},
	{"drag", ActionSwipe},
	{"滑动", ActionSwipe},
	{"keypress", ActionKey},
	{"key", ActionKey},
	{"按键", ActionKey},
	{"press", ActionPress},
	{"button", ActionPress},
	{"btn", ActionPress},
	{"按下", ActionPress},
	{"按钮", ActionPress},
	{"tap", ActionTap},
	{"click", ActionTap},
	{"点击", ActionTap},
	{"move", ActionMove},
	{"移动", ActionMove},
	{"行走", ActionMove},
	{"wait", ActionNone},
	{"none", ActionNone},
	{"noop", ActionNone},
	{"等待", ActionNone},
}

// ParseAction 从老师的一段文本里解析出**第一个**可识别动作。
//
// 逐行扫描，跳过不含动作动词的行；命中后只取该行，避免把
// 「建议: 先点A再点B」这类多动作描述误合并成一个动作。
func ParseAction(text string) (Action, error) {
	for _, line := range strings.Split(text, "\n") {
		if act, err := parseActionLine(line); err == nil {
			return act, nil
		}
	}
	return Action{Kind: ActionNone}, ErrNoAction
}

// ParseAllActions 解析文本里所有可识别的动作行（用于老师给出多步示范）。
func ParseAllActions(text string) []Action {
	var out []Action
	for _, line := range strings.Split(text, "\n") {
		if act, err := parseActionLine(line); err == nil {
			out = append(out, act)
		}
	}
	return out
}

var kvRe = regexp.MustCompile(`([a-zA-Z_][a-zA-Z0-9_]*)\s*=\s*(-?[0-9]*\.?[0-9]+)`)

// normalize 统一标点：全角转半角，逗号/分号/箭头/竖线转空格，去掉 markdown 装饰。
func normalize(line string) string {
	rep := strings.NewReplacer(
		"：", ":", "，", " ", "；", " ", "、", " ",
		"，", " ", "＝", "=",
		",", " ", ";", " ", "|", " ",
		"->", " ", "→", " ", "=>", " ",
		"（", " ", "）", " ", "(", " ", ")", " ",
		"**", "", "`", "", "#", "", "*", "",
		"　", " ",
	)
	s := rep.Replace(line)
	return strings.TrimSpace(s)
}

// parseActionLine 解析单行。返回 ErrNoAction 表示这行不是动作行。
func parseActionLine(line string) (Action, error) {
	s := normalize(line)
	if s == "" {
		return Action{}, ErrNoAction
	}
	lower := strings.ToLower(s)

	// 找最靠前的动词（避免 "tap" 出现在 "back" 之类里面，用边界判断）
	bestIdx, bestKind, bestWord := -1, ActionNone, ""
	for _, v := range verbKind {
		idx := indexWord(lower, v.word)
		if idx < 0 {
			continue
		}
		// 动词前若还有别的动词，取更靠前的
		if bestIdx == -1 || idx < bestIdx {
			bestIdx, bestKind, bestWord = idx, v.kind, v.word
		}
	}
	if bestIdx < 0 {
		return Action{}, ErrNoAction
	}

	// 统一从 lower 上切片，保证下标一定有效（lower 与 s 逐字节对应）。
	rest := lower[bestIdx+len(bestWord):]

	// 键值对
	kv := map[string]float64{}
	for _, m := range kvRe.FindAllStringSubmatch(rest, -1) {
		if f, err := strconv.ParseFloat(m[2], 64); err == nil {
			kv[strings.ToLower(m[1])] = f
		}
	}
	// 位置参数（ACTION TAP 0.79 0.75 这种裸数字写法）
	var bare []float64
	for _, f := range strings.Fields(stripKV(rest)) {
		if v, err := strconv.ParseFloat(f, 64); err == nil {
			bare = append(bare, v)
		}
	}

	get := func(keys ...string) (float64, bool) {
		for _, k := range keys {
			if v, ok := kv[k]; ok {
				return v, true
			}
		}
		return 0, false
	}
	// pick 按顺序优先取键值对，缺了再从裸数字里按位补
	pick := func(pos int, keys ...string) float64 {
		if len(keys) > 0 {
			if v, ok := get(keys...); ok {
				return v
			}
		}
		if pos >= 0 && pos < len(bare) {
			return bare[pos]
		}
		return 0
	}

	act := Action{Kind: bestKind}
	// hasKV 判断是否出现了某个显式键；hasBare 判断裸数字够不够。
	hasKV := func(keys ...string) bool {
		for _, k := range keys {
			if _, ok := kv[k]; ok {
				return true
			}
		}
		return false
	}
	hasBare := func(n int) bool { return len(bare) >= n }

	switch bestKind {
	case ActionNone:
		return act, nil

	case ActionTap:
		// 必须真有成对坐标：否则「建议：点击右下角按钮」「摇杆: x=.. y=..」
		// 这类**描述**行会被误当动作执行
		if !(hasKV("x", "nx", "cx") && hasKV("y", "ny", "cy")) && !hasBare(2) {
			return Action{}, ErrNoAction
		}
		act.Nx = clamp01(pick(0, "x", "nx", "cx"))
		act.Ny = clamp01(pick(1, "y", "ny", "cy"))
		return act, nil

	case ActionSwipe:
		if !(hasKV("x2", "nx2", "tx") && hasKV("y2", "ny2", "ty")) && !hasBare(4) {
			return Action{}, ErrNoAction
		}
		act.Nx = clamp01(pick(0, "x", "nx", "x1"))
		act.Ny = clamp01(pick(1, "y", "ny", "y1"))
		act.Nx2 = clamp01(pick(2, "x2", "nx2", "tx"))
		act.Ny2 = clamp01(pick(3, "y2", "ny2", "ty"))
		act.Dur = durMs(pick(-1, "dur", "duration", "ms", "time"), DefaultSwipeMs)
		return act, nil

	case ActionLongPress:
		if !(hasKV("x", "nx") && hasKV("y", "ny")) && !hasBare(2) {
			return Action{}, ErrNoAction
		}
		act.Nx = clamp01(pick(0, "x", "nx"))
		act.Ny = clamp01(pick(1, "y", "ny"))
		act.Dur = durMs(pick(-1, "dur", "duration", "ms", "time"), DefaultHoldMs)
		return act, nil

	case ActionJoystick:
		// 摇杆是「中心 + 目标点」两个坐标，缺任一点都不算动作。
		// 这一步很关键：游戏界面分析里「摇杆: x=0.21 y=0.69」只是描述位置。
		centerOK := hasKV("cx") || hasKV("x", "nx")
		targetOK := hasKV("tx") || hasKV("x2", "nx2")
		if !(centerOK && targetOK) && !hasBare(4) {
			return Action{}, ErrNoAction
		}
		// 兼容两种写法：cx/cy + tx/ty（推荐），或 x/y + x2/y2
		if hasKV("cx") {
			act.Nx = clamp01(pick(0, "cx"))
			act.Ny = clamp01(pick(1, "cy"))
			act.Nx2 = clamp01(pick(2, "tx"))
			act.Ny2 = clamp01(pick(3, "ty"))
		} else {
			act.Nx = clamp01(pick(0, "x", "nx"))
			act.Ny = clamp01(pick(1, "y", "ny"))
			act.Nx2 = clamp01(pick(2, "x2", "nx2", "tx"))
			act.Ny2 = clamp01(pick(3, "y2", "ny2", "ty"))
		}
		act.Dur = durMs(pick(-1, "dur", "duration", "ms", "time"), DefaultJoystickMs)
		return act, nil

	case ActionKey:
		if code, ok := kv["code"]; ok {
			act.Code = strconv.Itoa(int(code)) // 数字键码
		} else {
			// code=back 这种非数字值不能被 kvRe 捕获，单独抓
			act.Code = grabStringArg(rest, "code", "key", "name")
		}
		if act.Code == "" {
			return Action{}, ErrNoAction
		}
		return act, nil

	case ActionMove:
		// 优先按「方向移动」（L1 语义动作）解析。
		if ds := grabStringArg(rest, "dir", "direction", "d"); ds != "" {
			d, err := ParseDir(ds)
			if err != nil {
				return Action{}, ErrNoAction
			}
			act.Dir = d
			act.Dur = durMs(pick(-1, "dur", "duration", "ms", "time"), DefaultMoveMs)
			return act, nil
		}
		// 省略 dir= 的写法：ACTION MOVE forward dur=800
		if w := firstWord(stripKV(rest)); w != "" {
			if d, err := ParseDir(w); err == nil {
				act.Dir = d
				act.Dur = durMs(pick(-1, "dur", "duration", "ms", "time"), DefaultMoveMs)
				return act, nil
			}
		}
		// 回退到 L2 的鼠标相对移动：ACTION MOVE dx=10 dy=-5
		if hasKV("dx") || hasKV("dy") {
			act.Kind = ActionMouseMove
			act.Dx = int(pick(0, "dx"))
			act.Dy = int(pick(1, "dy"))
			return act, nil
		}
		return Action{}, ErrNoAction

	case ActionPress:
		// 按钮名优先从 name=/button=/btn=/按钮= 抓；也允许位置写法 ACTION PRESS jump
		name := grabStringArg(rest, "name", "button", "btn", "press", "按钮")
		if name == "" {
			name = firstWord(stripKV(rest))
		}
		name = strings.ToLower(strings.TrimSpace(name))
		// 按钮名必须「像一个名字」：只有英数字、下划线和汉字。
		// 否则「按钮: 星星(x=0.80,y=0.81)」这种描述行会被解析成 press::
		// （名字是个冒号），机器人就去按一个不存在的按钮。
		if !buttonNameRe.MatchString(name) {
			return Action{}, ErrNoAction
		}
		// 纯数字不是按钮名（Go 的 RE2 不支持否定预查，只能单独判一次）
		if _, err := strconv.ParseFloat(name, 64); err == nil {
			return Action{}, ErrNoAction
		}
		act.Name = name
		return act, nil
	}
	return Action{}, ErrNoAction
}

// firstWord 返回第一个空白分隔的词；空串表示没有。
func firstWord(s string) string {
	for _, f := range strings.Fields(s) {
		return f
	}
	return ""
}

// strArgRe 抓「非数字」的字符串参数，如 code=back、name=jump、dir=前、方向=右。
// 值允许中文与连字符：方向别名（前/左上）和按钮名都可能是中文。
//
// ⚠️ 前缀用 `(?:^|[^a-z0-9_])` 而不是 `\b`：Go 的 `\b` 是 ASCII 词边界，
// 中文键名（「方向=右」「按钮=jump」）前面会被判定为「无边界」而匹配失败——
// 实测就是这条让「方向=右」整行解析不出来。
var strArgRe = regexp.MustCompile(
	`(?i)(?:^|[^a-z0-9_])(code|key|name|dir|direction|button|btn|按键|方向|按钮)\s*=\s*([^\s=,;]+)`)

// argKeyAlias 把中文键名归一成英文，调用方就不必在每个动词分支里
// 都记得同时列出中英文键名（漏一个就会出现「方向=右 解析不出来」这种怪事）。
var argKeyAlias = map[string]string{
	"方向": "dir",
	"按钮": "button",
	"按键": "key",
}

// grabStringArg 抓字符串参数并归一化为小写（中文不受影响）。
func grabStringArg(s string, names ...string) string {
	for _, m := range strArgRe.FindAllStringSubmatch(s, -1) {
		got := strings.ToLower(m[1])
		if en, ok := argKeyAlias[got]; ok {
			got = en
		}
		for _, n := range names {
			if got == n {
				return strings.ToLower(strings.TrimSpace(m[2]))
			}
		}
	}
	return ""
}

// buttonNameRe 校验按钮名是否「像一个名字」：只允许英数字、下划线和汉字。
//
// 必须有这一道：动词表里有「按钮」这种名词性词，「按钮: 星星(x=0.80,y=0.81)」
// 这类**描述界面**的行会被动词匹配到，若不做校验，它会解析成一次
// 名字为 ":" 的 PRESS——机器人于是去按一个根本不存在的按钮。
var buttonNameRe = regexp.MustCompile(`^[a-z0-9_\p{Han}]+$`)

// stripKV 去掉键值对，只留裸数字，供位置参数解析用。
func stripKV(s string) string {
	return kvRe.ReplaceAllString(s, " ")
}

// durMs 把毫秒数转 Duration，越界时回退默认值。
func durMs(ms float64, def int) time.Duration {
	if ms <= 0 || ms > 60000 {
		return time.Duration(def) * time.Millisecond
	}
	return time.Duration(ms) * time.Millisecond
}

func clamp01(v float64) float64 {
	if v < 0 {
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}

// indexWord 找词边界匹配的下标（大小写不敏感，needle 需已小写）。
// 中文没有词边界概念，直接按子串找。
func indexWord(haystack, needle string) int {
	if needle == "" {
		return -1
	}
	if needle[0] > 127 || needle[len(needle)-1] > 127 {
		// 含非 ASCII（中文），直接子串匹配
		return strings.Index(haystack, needle)
	}
	for from := 0; ; {
		i := strings.Index(haystack[from:], needle)
		if i < 0 {
			return -1
		}
		i += from
		before := i == 0 || !isAlnum(haystack[i-1])
		after := i+len(needle) >= len(haystack) || !isAlnum(haystack[i+len(needle)])
		if before && after {
			return i
		}
		from = i + 1
		if from >= len(haystack) {
			return -1
		}
	}
}

func isAlnum(b byte) bool {
	return (b >= 'a' && b <= 'z') || (b >= 'A' && b <= 'Z') || (b >= '0' && b <= '9') || b == '_'
}

// ProtocolOptions 控制「给老师看的动作协议」内容。
//
// 之所以要可配：动作空间是**跨游戏通用**的，但每个游戏支持的子集不同——
// 没有摇杆的游戏不该列 MOVE，没配按钮表的游戏不该列 PRESS。
// 让模型只看见「这个游戏真正能做的动作」，是提高它输出质量最省事的手段。
type ProtocolOptions struct {
	// Hints 界面先验（来自游戏档案）：摇杆在哪、按钮什么含义、屏幕方向等。
	// 比让模型自己从像素里猜要可靠得多。
	Hints []string
	// Buttons 可用按钮名清单（ActionPress 的合法取值）。
	// 空则不列 PRESS——没有档案就没有可信坐标，列了只会让它瞎按。
	Buttons []string
	// HasMove 游戏档案里配了移动方式时才列 MOVE。
	HasMove bool
	// MoveNote 移动方式的说明，如「虚拟摇杆（位置固定）」。
	MoveNote string
}

// ActionProtocol 返回给老师看的动作协议说明（拼进提示词）。
//
// ⚠️ 占位符里**绝不能出现具体坐标数字**。踩过的坑：早期示例写成
//
//	ACTION JOYSTICK cx=0.21 cy=0.69 tx=0.81 ty=0.69 dur=1200
//
// 结果模型逐字节照抄这一行当答案，8 步实况动作完全一致，
// 看着像「模型不会决策」，实际是「示例被当成了答案」。
// 小模型对提示词里的具体数字极其敏感，示例必须写成不可直接复制的形式。
//
// ⚠️ 另外**不要把摇杆四坐标（cx/cy/tx/ty）写进协议**。实测模型会反复纠结
// 这几个参数各是什么含义，思考链 74 秒打满预算仍给不出动作。
// 换成 ACTION MOVE dir=... 之后，决策退化成一次 8 选 1 的分类，
// 方向语义由游戏档案翻译成具体触摸坐标——这是模型能稳定做对的形式。
func ActionProtocol(o ProtocolOptions) string {
	var b strings.Builder

	if len(o.Hints) > 0 {
		b.WriteString("【界面先验】\n")
		for _, h := range o.Hints {
			b.WriteString("- ")
			b.WriteString(h)
			b.WriteString("\n")
		}
	}

	b.WriteString("\n【可用动作】每条一行，只输出一行：\n")
	if o.HasMove {
		note := ""
		if o.MoveNote != "" {
			note = "（" + o.MoveNote + "）"
		}
		fmt.Fprintf(&b, "ACTION MOVE dir=<%s> dur=<毫秒>   朝该方向持续移动%s\n",
			DirListZh(), note)
	}
	if len(o.Buttons) > 0 {
		fmt.Fprintf(&b, "ACTION PRESS name=<%s>   按某个按钮\n",
			strings.Join(o.Buttons, "|"))
	}
	b.WriteString("ACTION TAP x=<横坐标> y=<纵坐标>   点击画面某处\n")
	b.WriteString("ACTION SWIPE x=<> y=<> x2=<> y2=<> dur=<>   滑动（转视角/拖拽）\n")
	b.WriteString("ACTION HOLD x=<> y=<> dur=<>   长按\n")
	b.WriteString("ACTION KEY code=<back|home|enter>   发送系统按键\n")
	b.WriteString("ACTION WAIT   不动，等画面变化\n")

	b.WriteString("\n坐标用 0~1 的归一化值（左上角 0,0，右下角 1,1）。\n")
	b.WriteString("尖括号是占位符，必须换成你看着这张图判断出的真实内容，不要照抄任何示例。\n")
	b.WriteString("只输出一行动作，不要解释，不要输出多行。")
	return b.String()
}

// ExplainAction 给动作配一句人话，用于日志与报告。
func ExplainAction(a Action) string {
	switch a.Kind {
	case ActionMove:
		if a.Dir.IsValid() {
			return fmt.Sprintf("朝%s方向移动 %dms", a.Dir.Zh(), a.Dur.Milliseconds())
		}
		return "方向移动"
	case ActionPress:
		return "按【" + a.Name + "】按钮"
	case ActionTap:
		return fmt.Sprintf("点击画面 %.0f%%,%.0f%% 处", a.Nx*100, a.Ny*100)
	case ActionSwipe:
		return fmt.Sprintf("从 %.0f%%,%.0f%% 滑到 %.0f%%,%.0f%%（%dms）",
			a.Nx*100, a.Ny*100, a.Nx2*100, a.Ny2*100, a.Dur.Milliseconds())
	case ActionLongPress:
		return fmt.Sprintf("长按 %.0f%%,%.0f%%（%dms）", a.Nx*100, a.Ny*100, a.Dur.Milliseconds())
	case ActionJoystick:
		return fmt.Sprintf("摇杆 %.0f%%,%.0f%% 推向 %.0f%%,%.0f%%（%dms）",
			a.Nx*100, a.Ny*100, a.Nx2*100, a.Ny2*100, a.Dur.Milliseconds())
	case ActionKey:
		return "按键 " + a.Code
	case ActionMouseMove:
		return fmt.Sprintf("移动鼠标 %+d,%+d", a.Dx, a.Dy)
	default:
		return "保持不动"
	}
}
