package video

import (
	"context"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"github.com/ayflying/game-sensei/internal/teacher"
)

// 这一层负责把「动作段」交给老师 VLM 判读，产出人能读的策略知识。
//
// # 必须先说清的边界：视频里没有动作 ground truth
//
// 录像只有画面，没有「玩家手指按在哪」。老师看到 3 张连续画面，
// 推断出的「操作」是**弱标签**——合理但未经证实，绝不能当成真值
// 去训练学生。本阶段真正可靠的产出是**策略与局面判断**
// （「目标在远处时持续朝它走」这类），操作字段只作人工核对时的线索。
//
// 想要真标签只有一条路：录制时打开 Android 开发者选项里的
// 「显示点按操作反馈」，让触点圆点直接画进画面，那才可以机械提取。

// FrameRef 是送审给老师的一帧。
type FrameRef struct {
	// Path 帧文件路径。
	Path string
	// OffsetSec 该帧相对**段首**的秒数（不是相对视频开头）。
	OffsetSec float64
}

// ClipSpec 是判读一个动作段所需的全部输入。
type ClipSpec struct {
	// Index 段序号（从 0 开始），仅用于日志。
	Index int
	// DurSec 段的总时长（秒）。
	DurSec float64
	// Frames 送审帧，必须按时间顺序排列。
	Frames []FrameRef
	// Game 游戏名，进提示词帮助老师识别界面（如「洛克王国：世界」）。
	Game string
	// Goal 当前游戏目标。
	Goal string
	// Buttons 画面上的按钮清单（取自游戏档案），例如 "jump(跳跃)"。
	// 必须给：不给的话老师只能说「点右下角某个圆按钮」，产出的策略没法执行。
	Buttons []string
	// Hints 额外界面先验，逐条拼进提示词。
	Hints []string
}

// Annotation 是老师对一个动作段的判读结果。
type Annotation struct {
	// Observation 局面：画面上发生了什么。
	Observation string `json:"observation,omitempty"`
	// Action 操作：玩家做了什么。**弱标签**，见包内说明。
	Action string `json:"action,omitempty"`
	// Strategy 策略：可复用的经验规则。
	Strategy string `json:"strategy,omitempty"`
	// Model 产出该判读的模型名。
	Model string `json:"model,omitempty"`
	// Raw 老师原始输出，解析失败时靠它排查。
	Raw string `json:"raw,omitempty"`
	// LatencyMs 老师推理耗时。
	LatencyMs float64 `json:"latency_ms,omitempty"`
	// Tokens 输出 token 数。
	Tokens int `json:"tokens,omitempty"`
	// Error 失败原因；非空时其余字段无意义。
	Error string `json:"error,omitempty"`
}

// Annotate 把一个动作段交给老师判读。
//
// 失败不返回 error，而是记进 Annotation.Error：批量判读几百段时，
// 个别段超时或空答复不该让整批任务中断，调用方按段检查即可。
func Annotate(ctx context.Context, c *teacher.Client, spec ClipSpec) Annotation {
	imgs, err := loadFrames(spec.Frames)
	if err != nil {
		return Annotation{Error: err.Error()}
	}
	reply, err := c.Chat(ctx, BuildClipPrompt(spec), imgs)
	if err != nil {
		return Annotation{Error: err.Error()}
	}
	a := ParseAnnotation(reply.Text)
	a.Model = reply.Model
	a.LatencyMs = reply.Stats.TotalMs
	a.Tokens = reply.Stats.OutputTokens
	return a
}

// loadFrames 读取送审帧的 JPEG 字节。
//
// 直接读文件不重新编码：抽帧产物本来就是送审尺寸的 JPEG，
// 再编一遍只会掉一次画质、多花一次时间。
func loadFrames(refs []FrameRef) ([][]byte, error) {
	out := make([][]byte, 0, len(refs))
	for _, r := range refs {
		b, err := os.ReadFile(r.Path)
		if err != nil {
			return nil, fmt.Errorf("读取送审帧 %s 失败: %w", filepath.Base(r.Path), err)
		}
		out = append(out, b)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("没有可送审的帧")
	}
	return out, nil
}

// BuildClipPrompt 构造判读一个动作段的提示词。
//
// 四条约束都来自实测教训（README §9.1 / §9.4），不是风格偏好：
//
//   - **不写具体数值示例**。小模型会把示例逐字节照抄——早期动作协议里
//     写了 `cx=0.21 cy=0.69`，模型 8 步输出与示例完全一致，看着像
//     「不会决策」，其实是「抄了示例」。所以格式行里只用尖括号占位。
//   - **明确要求直接给结论**。qwen3 的思考链会吃光输出预算，
//     结果是正文为空（400 token 预算实测必空，1500 才稳）。
//   - **必须交代帧的时间顺序与跨度**。多图送审时模型不知道哪张在前，
//     也不这段跨了多久——不告诉它，它会把「朝目标走了 20 秒」
//     读成「在原地站了一会儿」。
//   - **必须给按钮名**。否则产出的策略只会是「点右下角那个圆按钮」，
//     没法映射成可执行动作。
func BuildClipPrompt(spec ClipSpec) string {
	var b strings.Builder

	name := strings.TrimSpace(spec.Game)
	if name == "" {
		name = "某款手机游戏"
	}
	fmt.Fprintf(&b, "你在看一段手机游戏《%s》的教学录像片段。\n\n", name)
	fmt.Fprintf(&b, "下面是同一个片段里抽出的 %d 张画面，已按时间先后排列。整个片段时长约 %.0f 秒：\n",
		len(spec.Frames), spec.DurSec)
	for i, f := range spec.Frames {
		fmt.Fprintf(&b, "- 第 %d 张：片段第 %.0f 秒（%s）\n", i+1, f.OffsetSec, positionLabel(i, len(spec.Frames)))
	}

	if spec.Goal != "" {
		fmt.Fprintf(&b, "\n玩家当前的目标：%s\n", spec.Goal)
	}
	if len(spec.Buttons) > 0 {
		fmt.Fprintf(&b, "画面上可用的按钮：%s\n", strings.Join(spec.Buttons, "、"))
	}
	if len(spec.Hints) > 0 {
		fmt.Fprintf(&b, "已知界面信息（**仅用于辨认画面元素，不要把这些说法直接当作结论**）：%s\n",
			strings.Join(spec.Hints, "；"))
	}

	b.WriteString("\n请判断玩家在这段时间里做了什么，并总结出可复用的经验。\n")
	b.WriteString("直接给出结论，不要复述题目，不要解释推理过程。\n")
	// 这条约束是实测逼出来的：档案 hints 里写了「点任务追踪文字可以自动寻路」，
	// 结果 8 段里有 6 段的「策略」都在照搬这一句，连纯过场动画那段也不例外——
	// 模型把先验当成了答案。不压住它，产出的策略清单看着热闹，实际全是同一句话。
	b.WriteString("结论必须来自这几张画面自身的差异。画面只能说明「角色在移动」时就只写移动，")
	b.WriteString("不要附会成通用的游戏建议，也不要把上面给的界面信息当成结论。\n")
	b.WriteString("如果这段时间是过场动画、加载画面或纯界面停留、没有可复用的操作经验，")
	b.WriteString("就在策略里如实写「本段无操作可学」。\n\n")
	b.WriteString("严格按下面三行回答，每行一项，不要写其他内容（尖括号内是填写提示，不要原样输出）：\n")
	b.WriteString("局面：<画面上发生了什么，角色在什么位置、正在做什么>\n")
	b.WriteString("操作：<玩家做了哪些操作，用方向或按钮名描述>\n")
	b.WriteString("策略：<从本段画面前后变化中能看出的、可复用的做法>\n")
	return b.String()
}

// positionLabel 描述某帧在段内的位置，帮助老师建立时间感。
func positionLabel(i, n int) string {
	switch {
	case n == 1:
		return "唯一一张"
	case i == 0:
		return "片段开始"
	case i == n-1:
		return "片段结束"
	default:
		return "片段中间"
	}
}

// fieldAliases 是三个字段的标签别名，顺序即匹配优先级。
//
// 别名收这么多是因为小模型不老实：同一份提示词下它可能写
// 「局面/画面/场景」、「操作/动作/按键」，全部认下来比事后返工便宜。
var fieldAliases = []struct {
	key   string
	names []string
}{
	{"observation", []string{"局面", "画面", "场景", "观察"}},
	{"action", []string{"操作", "动作", "按键"}},
	{"strategy", []string{"策略", "建议", "规则", "要点"}},
}

// ParseAnnotation 从老师输出里提取三行结构化结果。
//
// 容错点都是模型输出的实际形态：中英文冒号混用、行首带项目符号、
// 标签被 ** 包起来，以及把三项挤在同一行（小模型常这么干）。
// 任何一项没解析出来就留空，不编造内容。
func ParseAnnotation(text string) Annotation {
	a := Annotation{Raw: strings.TrimSpace(text)}
	body := strings.ReplaceAll(a.Raw, "\r\n", "\n")

	lines := strings.Split(body, "\n")
	if len(lines) < 3 {
		// 挤在一行了，先按标签位置切开
		lines = splitByLabels(body)
	}

	for _, ln := range lines {
		ln = strings.TrimSpace(ln)
		if ln == "" {
			continue
		}
		ln = strings.TrimLeft(ln, "-*•·> \t")
		key, v, ok := matchField(ln)
		if !ok {
			continue
		}
		switch key {
		case "observation":
			if a.Observation == "" {
				a.Observation = v
			}
		case "action":
			if a.Action == "" {
				a.Action = v
			}
		case "strategy":
			if a.Strategy == "" {
				a.Strategy = v
			}
		}
	}
	return a
}

// maxLabelOffset 是「标签算作字段名」的最大行内偏移。
//
// 留 6 个字符是为了容下 "- **局面**：" 这类前缀（项目符号 + 加粗），
// 再靠后基本可以断定是正文里恰好出现的同名词。
const maxLabelOffset = 6

// matchField 从一行里找出字段标签并取出其后的内容。
//
// 两个判据缺一不可，都是被真实输出逼出来的：
//
//   - **标签必须靠近行首**（≤ maxLabelOffset）。正文里会出现同名词——
//     「策略：本段无操作可学」这行含「操作」二字，只看包含关系会把它
//     误判成操作字段，内容被切成「可学」；
//   - **标签后面必须紧跟冒号**。模型写字段一定带冒号，而正文里的
//     「操作」后面跟的是别的字。有了这条，字段名和正文里的同名词
//     才能彻底分开。
func matchField(line string) (key, value string, ok bool) {
	best := -1
	var bestKey, bestName string

	for _, f := range fieldAliases {
		for _, n := range f.names {
			idx := strings.Index(line, n)
			if idx < 0 || idx > maxLabelOffset {
				continue
			}
			if !followedByColon(line, idx+len(n)) {
				continue
			}
			if best < 0 || idx < best {
				best, bestKey, bestName = idx, f.key, n
			}
		}
	}
	if best < 0 {
		return "", "", false
	}

	rest := strings.TrimLeft(line[best+len(bestName):], "：: 　\t*#（(")
	rest = strings.TrimRight(rest, "）) ")
	if strings.TrimSpace(rest) == "" {
		return "", "", false
	}
	return bestKey, strings.TrimSpace(rest), true
}

// followedByColon 判断从 from 起（跳过加粗标记与空白）是否紧跟冒号。
//
// 用 TrimLeft + HasPrefix 而不是逐字节比较：全角冒号和全角空格都是
// 多字节字符，byte 装不下它们的 rune 常量，逐字节写法直接编译不过。
func followedByColon(s string, from int) bool {
	if from > len(s) {
		return false
	}
	rest := strings.TrimLeft(s[from:], "*# \t　")
	return strings.HasPrefix(rest, "：") || strings.HasPrefix(rest, ":")
}

// splitByLabels 把挤在同一行的输出按字段标签切开。
//
// 与 matchField 共用同一套判据：只看标签出现的位置，会把正文里的同名词
// 也当成切点——「策略：本段无操作可学」会被从「操作」处切成两半。
func splitByLabels(text string) []string {
	var pos []int
	seen := make(map[int]bool)

	for _, f := range fieldAliases {
		for _, n := range f.names {
			from := 0
			for {
				i := strings.Index(text[from:], n)
				if i < 0 {
					break
				}
				i += from
				if followedByColon(text, i+len(n)) && !seen[i] {
					seen[i] = true
					pos = append(pos, i)
				}
				from = i + len(n)
			}
		}
	}
	if len(pos) < 2 {
		return []string{text}
	}
	sort.Ints(pos)

	out := make([]string, 0, len(pos))
	for i, p := range pos {
		end := len(text)
		if i+1 < len(pos) {
			end = pos[i+1]
		}
		out = append(out, text[p:end])
	}
	return out
}
