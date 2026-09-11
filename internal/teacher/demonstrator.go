package teacher

import (
	"context"
	"fmt"
	"strings"

	"github.com/ayflying/game-sensei/internal/agent"
)

// Demonstrator 让老师从「看回放写评语」升级为「直接出动作」——
// Phase 2 的示范来源：一帧画面 + 一个目标 → 一个可执行的 agent.Action。
//
// 关键前提是 Options.Think=false（见 ActionOptions）：实测 qwen3.5:9b
// 开着思考要 60s+ 且正文常被思考挤空，关掉后 1.2s 稳定产出合规动作。
// 这决定了老师能不能跑在在线回路里，而不是只能异步查岗。
type Demonstrator struct {
	Client *Client
	Goal   string   // 游戏目标，写进提示词引导决策
	Hints  []string // 界面/设备先验（如摇杆中心坐标），减少模型乱猜

	// PrevAction 是上一步已执行的动作串（agent.Action.String()）。
	//
	// 为什么要喂回去：单帧 VLM 没有记忆，若不告诉它刚做过什么，
	// 它会连续给出同一个动作直到天荒地老（实测 8 步逐字节相同）。
	// 有了这一句，它至少能在「画面没推进」时换个方向。
	PrevAction string
}

// DemoResult 是一次示范。
type DemoResult struct {
	Action agent.Action // 解析出的动作
	Raw    string       // 老师的原始输出（便于排查解析失败）
	Reply  *Reply       // 含耗时与 token 统计
	Used   bool         // 解析是否成功（失败时 Action 为 ActionNone）
}

// interfacePrior 是洛克王国：世界的界面先验。
//
// 实测教训：不给先验时，模型会输出退化的坐标数组；给了先验，
// 格式合规率 6/6。先验不是「帮模型作弊」，而是补上单帧图像里
// 难以推断的界面语义（哪个圆是摇杆、哪个是技能）。
const interfacePrior = `界面布局常识：
- 左下角半透明圆形区域 = 虚拟摇杆，按住并推向某方向可移动角色
- 右下角有若干圆形按钮，一般包括精灵切换、奔跑、跳跃、交互/捕捉技能
- 顶部有任务追踪文字与坐标；画面中央是玩家角色
- 若出现对话气泡或选项列表，说明处于对话/菜单中，需要点击选项才能继续`

// BuildDemoPrompt 拼装「出动作」提示词。
//
// 刻意写得短而具体：内容越长，模型越容易绕回长篇分析，
// 而我们要的只有最后那一行动作。
func (d *Demonstrator) BuildDemoPrompt() string {
	var b strings.Builder
	b.WriteString("你是手机游戏《洛克王国：世界》的实时操作助手，这是一款 3D 开放世界精灵收集游戏。\n")
	b.WriteString(interfacePrior + "\n")
	if len(d.Hints) > 0 {
		b.WriteString("\n本机已知信息：\n")
		for _, h := range d.Hints {
			b.WriteString("- " + h + "\n")
		}
	}
	if d.Goal != "" {
		fmt.Fprintf(&b, "\n【当前目标】%s\n", d.Goal)
	}
	if d.PrevAction != "" {
		fmt.Fprintf(&b, "\n【上一步你执行的动作】%s\n", d.PrevAction)
		b.WriteString("如果画面显示这一步没有推进目标（角色没靠近目标、界面没变化），" +
			"请换一个明显不同的动作；如果正在有效推进，就保持方向继续。\n")
	}
	b.WriteString("\n看这张截图。先判断三件事（不要写出来）：主角在画面什么位置、" +
		"目标在哪一侧、当前是自由探索还是对话/菜单。然后据此决定此刻最该做的**一个**动作。\n")
	b.WriteString(agent.ActionProtocol())
	b.WriteString("\n坐标必须是 0~1 的归一化值（左上角 0,0；右下角 1,1）。")
	b.WriteString("只输出一行动作，不要解释，不要输出多行。")
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
