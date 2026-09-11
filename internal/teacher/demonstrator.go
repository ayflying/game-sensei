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
	PrevAction string
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
func (d *Demonstrator) protocolOptions() agent.ProtocolOptions {
	o := d.Profile.ProtocolOptions() // nil Profile 安全，返回零值
	if len(d.Hints) > 0 {
		merged := make([]string, 0, len(o.Hints)+len(d.Hints))
		merged = append(merged, o.Hints...)
		merged = append(merged, d.Hints...)
		o.Hints = merged
	}
	return o
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
	if d.PrevAction != "" {
		fmt.Fprintf(&b, "\n【上一步你执行的动作】%s\n", d.PrevAction)
		b.WriteString("如果画面显示这一步没有推进目标（角色没靠近目标、界面没变化），" +
			"请换一个明显不同的动作；如果正在有效推进，就保持方向继续。\n")
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
