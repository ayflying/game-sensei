package teacher

import (
	"context"
	"fmt"
	"strings"

	"github.com/ayflying/game-sensei/internal/agent"
	"github.com/ayflying/game-sensei/internal/game"
)

// Demonstrator 让老师从「看回放写评语」升级为「直接出动作」——
// Phase 2 的示范来源：一帧画面 + 一个目标 → 一个可执行的 agent.Action。
//
// 关键前提是 Options.Think=false（见 ActionOptions）：实测 qwen3.5:9b
// 开着思考要 60s+ 且正文常被思考挤空，关掉后 1.2s 稳定产出合规动作。
// 这决定了老师能不能跑在在线回路里，而不是只能异步查岗。
//
// ⚠️ 本类型**不含任何具体游戏的文案**。游戏名、界面先验、可用按钮清单
// 全部来自 Profile（游戏档案）。加一款新游戏 = 加一份 JSON，这个文件不用动。
type Demonstrator struct {
	Client *Client

	// Profile 是游戏档案：提供游戏名、界面先验、可用动作子集。
	//
	// 为 nil 时不带任何游戏知识，提示词退化成「通用动作协议」——
	// 仍能跑，但模型只能靠猜界面布局，实测格式合规率会明显下降。
	Profile *game.Profile

	// Goal 游戏目标，写进提示词引导决策。
	Goal string

	// Hints 是档案之外的临时先验（CLI -demo-hints），用于现场调试
	// 新游戏、还没攒成档案时先手喂几条。
	Hints []string

	// PrevAction 是上一步已执行的动作串（agent.Action.String()）。
	//
	// 为什么要喂回去：单帧 VLM 没有记忆，若不告诉它刚做过什么，
	// 它会连续给出同一个动作直到天荒地老。
	//
	// ⚠️ 诚实记录：实测只靠这一条**没能打破重复**（8 步动作仍逐字节相同）。
	// 保留它是因为它仍是正确的接口设计（多帧示范时迟早要用到），
	// 但别指望它单独解决问题——真正的解法是让动作空间本身更适合小模型
	// （见 agent.ActionProtocol 与游戏档案的 MOVE/PRESS 设计）。
	//
	// 2026-09-12 补充（解忧梦幻岛实测）：纯点击游戏上连喂 PrevAction
	// 依然复读同一坐标 20+ 步，误点了 VIP 图标弹出付费弹窗。新增
	// RepeatCount：把「已连续重复 N 次」直接写进提示词并禁止再选同一
	// 动作——对「画面没变化 + 明确禁令」的场景，这是实测必要的最小干预。
	PrevAction string

	// RepeatCount 是 PrevAction 已连续被执行的次数（1=执行过一次）。
	// 0 或 1 时不写进提示词；≥2 时明确禁止再选同一动作。
	RepeatCount int

	// PrevKind 是 PrevAction 的动作种类（agent.ActionKind）。
	//
	// 为什么需要它：重复禁令**不能对移动一视同仁**。2026-09-13 洛克王国 40 步
	// 实况暴露了后果——老师朝 up_right 连走 2 步后被提示「禁止再选它」，于是换
	// up_left；走 2 步又被禁，于是换回 up_right。净位移≈0，整轮原地横跳。
	//
	// 移动是**持续型**动作：重复走同一方向通常正是对的（要走出去总得连续走）。
	// 「走了但没进展」由回路的卡死判据（跨窗口画面变化量）兜底，不靠禁令。
	// 因此只有**瞬时型**动作（PRESS/TAP/KEY）才套用重复禁令。
	PrevKind agent.ActionKind

	// RecentActions 是最近若干步已执行的动作串（由早到晚）。
	//
	// 单给 PrevAction 只能看到一步，模型察觉不到自己正在 A/B 之间反复横跳
	// （两步之内怎么看都「没有重复」）。把最近 6~8 步摊开写进提示词，模型才有
	// 机会识别出「我在打转」并主动换策略。提示词里最多列 recentActionPromptMax 条。
	RecentActions []string

	// LastDiff 是上一步执行后画面的变化量（相邻两帧灰度平均绝对差，0~255）。
	//
	// 为什么要喂给老师：单帧 VLM 分不清「在往前走」和「顶着墙走」——两次截图
	// 在任何一种情形下都能看到同一个主角。2026-09-13 洛克王国 60 步实况：
	// 老师连续 43 步 move:up_right/1500ms，画面变化量多次掉到 1~3（撞墙无进展），
	// 但它无从知道，于是死磕同一个方向。
	LastDiff float64

	// MoveStallStreak 是「连续朝同一方向移动且画面几乎没变化」的步数。
	//
	// 与 LastDiff 配套：≥2 时提示词不再说「继续走」，而是点名要求换方向绕行。
	// 这是对 PrevKind 分支的补充——PrevKind 只消解了「横跳」，
	// 「单向死撞」得靠这个进度反馈才能解。
	MoveStallStreak int

	// UIState 是回路检测出的当前界面态（game.StateWorld / game.StateBattle）。
	// 空串表示未知/不按态收窄。战斗态下提示词会隐藏 MOVE、只列战斗按钮与技能宏，
	// 避免小模型在没有摇杆的回合界面里空推、或跨态去点世界按钮。
	UIState string
}

// DemoResult 是一次示范。
type DemoResult struct {
	Action agent.Action // 解析出的动作
	Raw    string       // 老师的原始输出（便于排查解析失败）
	Reply  *Reply       // 含耗时与 token 统计
	Used   bool         // 解析是否成功（失败时 Action 为 ActionNone）
}

// gameName 返回当前游戏名，无档案时用中性说法。
func (d *Demonstrator) gameName() string {
	if d.Profile != nil && d.Profile.Name != "" {
		return "《" + d.Profile.Name + "》"
	}
	return "这款游戏"
}

// protocolOptions 合并档案先验与临时先验，得到给老师的动作协议配置。
// 按 UIState 收窄（战斗态只给战斗项、无 MOVE、无自由坐标）。
//
// 注：档案先验已按界面态分层——o.Hints 是 Profile.HintsForState(d.UIState) 的
// 「通用 + 该态专属」合并结果；-demo-hints 的临时先验追加在其后。
func (d *Demonstrator) protocolOptions() agent.ProtocolOptions {
	// 零值兜底 = 全开（无档案时 TAP/SWIPE 是老师唯一的交互手段）；
	// 有档案时由 ProtocolOptionsForState 按态决定收窄程度。
	o := agent.ProtocolOptions{AllowFreePointer: true}
	if d.Profile != nil {
		o = d.Profile.ProtocolOptionsForState(d.UIState) // nil Profile 由内部兜底
	}
	if len(d.Hints) > 0 {
		merged := make([]string, 0, len(o.Hints)+len(d.Hints))
		merged = append(merged, o.Hints...)
		merged = append(merged, d.Hints...)
		o.Hints = merged
	}
	return o
}

// recentActionPromptMax 提示词里最多列多少条最近动作。
// 取 8：再多对「看出在打转」没有增量，只会拉长提示词、增加小模型跑偏的概率。
const recentActionPromptMax = 8

// oscWindow 横跳检测的窗口（步）。
const oscWindow = 6

// actionProgressEps 是「上一步动作到底有没有生效」的画面变化量阈值，
// 单位与回路侧一致（相邻两帧灰度平均绝对差，0~255），取值也与
// cmd/helper 的 moveStallDiffEps 对齐——两处判的是同一件事，别各定一套。
//
// 为什么要引入它：2026-09-13 洛克王国 pet_run14 实况暴露了「重复禁令」的
// **第二个误伤面**。战斗段里 battle_catch 每次都让画面明显变化（Δ≈25，是在
// 正常投球），却被 RepeatCount>=2 的禁令锁死；老师被逼着改按 cast_hetu
// （Δ≈1.2，技能根本没放出去），再被禁，再换回来——于是
// cast_hetu ↔ battle_catch 无限 A/B 横跳（日志里 🔁 告警 2 次）。
//
// 这与 PrevKind 那次是**同一个设计缺陷的两个面孔**：
// **「重复」本身不是问题，「重复且画面没变化」才是问题。**
// 所以禁令必须由 Δ 把关，而不是由次数把关。
const actionProgressEps = 4.0

// DetectOscillation 判断最近的动作是否在**少数几个动作之间来回横跳**。
//
// 判据：最近 oscWindow 步里出现的不同动作 ≤ 2 种，且相邻两步不同的次数 ≥ 3。
// 直觉是「至少换了三次向、来去只有那两种」——这正是 2026-09-13 实况里
// up_right/up_left 交替的形状（净位移≈0），而健康的连走（同一方向重复、
// 或三个以上方向有序推进）都不会命中。
//
// 返回横跳涉及的两个动作（按首次出现顺序）。回路侧拿它打日志，
// 提示词侧拿它点名告警——同一判据两处复用，避免日志与提示词结论打架。
func DetectOscillation(recent []string) (a, b string, ok bool) {
	if len(recent) < oscWindow {
		return "", "", false
	}
	w := recent[len(recent)-oscWindow:]

	var distinct []string
	for _, s := range w {
		found := false
		for _, d := range distinct {
			if d == s {
				found = true
				break
			}
		}
		if !found {
			distinct = append(distinct, s)
			if len(distinct) > 2 {
				return "", "", false
			}
		}
	}
	if len(distinct) != 2 {
		return "", "", false
	}
	switches := 0
	for i := 1; i < len(w); i++ {
		if w[i] != w[i-1] {
			switches++
		}
	}
	if switches < 3 {
		return "", "", false
	}
	return distinct[0], distinct[1], true
}

// BuildDemoPrompt 拼装「出动作」提示词。
//
// 刻意写得短而具体：内容越长，模型越容易绕回长篇分析，
// 而我们要的只有最后那一行动作。
func (d *Demonstrator) BuildDemoPrompt() string {
	var b strings.Builder
	b.WriteString("你是" + d.gameName() + "的实时操作助手。\n")

	opts := d.protocolOptions()
	b.WriteString("\n看这张截图，决定此刻最该做的**一个**动作。\n")
	if d.Goal != "" {
		fmt.Fprintf(&b, "\n【当前目标】%s\n", d.Goal)
	}
	if len(d.RecentActions) > 0 {
		recent := d.RecentActions
		if len(recent) > recentActionPromptMax {
			recent = recent[len(recent)-recentActionPromptMax:]
		}
		fmt.Fprintf(&b, "\n【你最近的动作】（由早到晚）%s\n", strings.Join(recent, " → "))
		if x, y, osc := DetectOscillation(d.RecentActions); osc {
			fmt.Fprintf(&b, "注意：你最近一直在 %s 和 %s 之间来回横跳，净位移≈原地打转。\n", x, y)
			if d.UIState == game.StateBattle {
				// 战斗态下必须改口径：提示词里根本没有 MOVE，「朝同一方向连续走」
				// 是错的建议（pet_run14 就是照着世界态那句写的，白耗步数）。
				b.WriteString("但**别忙着换动作**：回合战斗里重复用同一个招是正常的。" +
					"先看【上一步】那段给的画面变化量 Δ——" +
					"哪个动作让画面明显变化就继续用它，重复不要紧；" +
					"若两个动作 Δ 都很小，缺的不是「换个招」，而是前置条件没满足" +
					"（能量/资源不足、或目标血量还不到能捕捉的程度）——" +
					"这一步去按能满足前置条件的那个动作（回能/继续削血），不要再来回换。\n")
			} else {
				b.WriteString("这一步必须换策略：要么朝**同一个方向连续走**（一次 dur 给大些，比如 1500ms），" +
					"要么去按一个你还没按过的按钮，看看有没有新界面（地图/背包/对话/靠近精灵）。\n")
			}
		}
	}
	if d.PrevAction != "" {
		fmt.Fprintf(&b, "\n【上一步你执行的动作】%s\n", d.PrevAction)
		switch {
		case d.PrevKind == agent.ActionMove:
			// 移动是持续型动作：不套禁令，改为要求它自证「有没有真在前进」。
			//
			// ⚠️ 2026-09-13 洛克王国 60 步实况补充：只给「自己判断有没有在前进」
			// 是不够的——老师连续 43 步 move:up_right/1500ms，画面变化量多次掉到
			// 1~3（明确的撞墙信号），它照样死磕。单帧 VLM 看不出「顶着墙走」和
			// 「在往前走」的区别，所以必须由回路把进度（Δ）直接告诉它。
			if d.MoveStallStreak >= 2 {
				fmt.Fprintf(&b, "⚠️ 你已连续 %d 步朝同一方向移动，但画面变化量很小（上一步 Δ≈%.1f，"+
					"基本等于只播了角色/粒子动画）。这说明**被地形挡住了**（山体/建筑/围栏/空气墙）。"+
					"这一步**禁止再沿原方向走**：换一个明显不同的方向（例如 down、left、up 中你还没试过的），"+
					"或者先按一个世界按钮看看有没有新界面。\n", d.MoveStallStreak, d.LastDiff)
			} else {
				b.WriteString("这是移动。移动本来就会重复很多次——如果画面里场景在滚动、" +
					"主角在接近目标，就继续朝这个方向走（可以把 dur 加大到 1000~1500ms 走得更远）；" +
					"只有当画面几乎没变化（被地形/空气墙挡住）时才换方向。\n")
				if d.LastDiff > 0 {
					fmt.Fprintf(&b, "（回路实测：上一步执行后画面变化量 Δ≈%.1f。数值越小说明越没走动。）\n", d.LastDiff)
				}
			}
		case d.RepeatCount >= 2:
			// ⚠️ 重复禁令必须由 Δ 把关，不能由次数把关（见 actionProgressEps 注释）。
			switch {
			case d.LastDiff >= actionProgressEps:
				// 重复但画面确实在变：这个动作是有效的，别打断它。
				// pet_run14 的 battle_catch（Δ≈25）就是被旧禁令误杀的典型。
				fmt.Fprintf(&b, "这个动作你已经执行 %d 次了。"+
					"（回路实测：上一步执行后画面变化量 Δ≈%.1f，画面确实在变，说明**它在起作用**。）"+
					"重复执行是允许的——**不要因为「重复」就换掉它**。"+
					"只有当画面几乎不动、或目标已经达成时才换动作。\n",
					d.RepeatCount, d.LastDiff)
			case d.LastDiff > 0:
				// 重复且画面几乎没变化：动作没生效。这才是该禁的情形，
				// 并且要点出「没生效」的三类常见成因，否则模型只会换个动作接着空按。
				fmt.Fprintf(&b, "⚠️ 这个动作已经连续执行 %d 次，而且上一步执行后画面几乎没变化"+
					"（Δ≈%.1f）——它**没有生效**，再按一次也不会有任何不同。**禁止再选它**。\n"+
					"先判断它为什么没生效，再换一个**性质不同**的动作：\n"+
					"① 资源/次数不足——需要消耗能量、体力或道具的动作，不足时按下去毫无反应，"+
					"这时要先去做**补充**那件事（可用动作里若有回能/补给类动作，先去用它），再回来做原动作；\n"+
					"② 当前状态下它本来就不响应——例如技能盘没展开就去点技能卡、目标血量还不到能被捕捉的程度、"+
					"菜单没打开就去点菜单项；\n"+
					"③ 时机未到——还在动画或回合结算中，等画面稳定后再说。\n",
					d.RepeatCount, d.LastDiff)
			default:
				// 拿不到 Δ（回放/首步）：沿用原来的保守禁令。
				fmt.Fprintf(&b, "这个动作已经连续执行 %d 次了，画面却没有推进目标"+
					"（任务计数没涨/界面没变化）。**禁止再选它**——换一个明显不同的动作，"+
					"比如先判断画面上是否弹出了新菜单或弹窗，有 × 就关掉它。\n", d.RepeatCount)
			}
		default:
			b.WriteString("如果画面显示这一步没有推进目标（角色没靠近目标、界面没变化），" +
				"请换一个明显不同的动作；如果正在有效推进，就保持方向继续。\n")
		}
	}
	b.WriteString("\n先判断三件事（不要写出来）：主角在画面什么位置、" +
		"目标在哪一侧、当前是自由探索还是对话/菜单。然后据此输出动作。\n")
	b.WriteString(agent.ActionProtocol(opts))
	return b.String()
}

// Act 让老师看一帧 PNG 并给出动作。
//
// 返回 error 只代表**调用失败**（网络/模型报错）；动作解析失败不算 error，
// 而是 DemoResult.Used=false —— 因为解析失败本身是要观测的指标
// （衡量老师输出格式的稳定性），不该中断示范回路。
func (d *Demonstrator) Act(ctx context.Context, png []byte) (*DemoResult, error) {
	if d.Client == nil {
		return nil, fmt.Errorf("Demonstrator: Client 未设置")
	}
	reply, err := d.Client.Chat(ctx, d.BuildDemoPrompt(), [][]byte{png})
	if err != nil {
		return nil, err
	}
	res := &DemoResult{Raw: reply.Text, Reply: reply}
	act, perr := agent.ParseAction(reply.Text)
	if perr != nil {
		return res, nil // 解析失败但调用成功，交给调用方决定是否跳过
	}
	res.Action = act
	res.Used = true
	return res, nil
}
