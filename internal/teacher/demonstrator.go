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
			fmt.Fprintf(&b, "注意：你最近一直在 %s 和 %s 之间来回横跳，净位移≈原地打转。"+
				"这一步必须换策略：要么朝**同一个方向连续走**（一次 dur 给大些，比如 1500ms），"+
				"要么去按一个你还没按过的按钮，看看有没有新界面（地图/背包/对话/靠近精灵）。\n", x, y)
		}
	}
	if d.PrevAction != "" {
		fmt.Fprintf(&b, "\n【上一步你执行的动作】%s\n", d.PrevAction)
		switch {
		case d.PrevKind == agent.ActionMove:
			// 移动是持续型动作：不套禁令，改为要求它自证「有没有真在前进」。
			b.WriteString("这是移动。移动本来就会重复很多次——如果画面里场景在滚动、" +
				"主角在接近目标，就继续朝这个方向走（可以把 dur 加大到 1000~1500ms 走得更远）；" +
				"只有当画面几乎没变化（被地形/空气墙挡住）时才换方向。\n")
		case d.RepeatCount >= 2:
			fmt.Fprintf(&b, "这个动作已经连续执行 %d 次了，画面却没有推进目标"+
				"（任务计数没涨/界面没变化）。**禁止再选它**——换一个明显不同的动作，"+
				"比如先判断画面上是否弹出了新菜单或弹窗，有 × 就关掉它。\n", d.RepeatCount)
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
