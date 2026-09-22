# 「资料 → 老师」落地任务书

> 日期：2026-09-13
> 版本：**v2 —— 由「问题清单」升级为「可执行任务书」**（v1 只列缺口，v2 补上精确改动点、代码骨架、逐条迁移表与验收命令）
> 定位：这是「资料越多越聪明」在本项目**唯一能立刻兑现**的两处之一（另一处是无标签视觉预训练）。
> 用法：把本文档交给一个执行会话，按 §四 的顺序逐项做，每项做完对照 §五 验收。
> 结论一句话：**机制已经存在（hints → 提示词），但缺分层、缺防护、缺门禁——三个缺口都有代码证据。**

---

## 零、一页速览

### 0.1 要改什么

| 编号 | 目标 | 主要文件 | 规模 | 依赖 | 验收手段 |
|---|---|---|---|---|---|
| **T0** | 补 `-print-protocol` 协议打印入口 | `cmd/helper/main.go` | ≈15 行 | 无 | 直接跑，看输出 |
| **T1** | 给 `ActionProtocol` 加 hints 防护 | `internal/agent/parse.go` | 2 行 + 注释 | T0（验收） | 单测 + 真机一段 |
| **T2** | hints 按界面态分层 | `internal/game/profile.go`、`resolve.go`、**两份** `nrc.json`（见 T2.4） | ≈40 行 + 档案迁移 | T0 | `-print-protocol` 对基线数字 |
| **T3** | 容量门禁 + hints 瘦身 | `internal/game/profile.go`、两份 `nrc.json` | ≈30 行 | T2 | 单测（超限报错） |

### 0.2 执行顺序与理由

```text
T0（打印入口）→ T1（防护）→ T2（分层）→ T3（门禁）
```

- **T0 必须最先做**：它是 T1/T2/T3 三者唯一的「肉眼可验证」手段（当前 `ActionProtocol` 只有 `internal/teacher/demonstrator.go:293` 一个消费点，跑之前看不到协议长什么样）。
- **T1 与 T2 无耦合**，谁先都行；但 T1 改动只有两行，先做可快速建立信心。
- **T3 依赖 T2**：门禁里的「两态合计字符上限」要用到 `HintsForState`。

### 0.3 红线（执行时必须遵守）

1. **逐个文件 `git add`，绝不用 `git add -A` / `git add .`**（工作区可能被并发会话改动；本次已知 `internal/game/profiles/sparkle.json`、`internal/plan/plan.go`、`internal/plan/plan_test.go` 有并发改动，**不属于本任务，不要碰、不要提交**）。
2. **不改核心零 CGO 约束**、不引入新依赖（本任务全部是标准库）。
3. **档案字段名拼错立刻报错**，不许静默忽略（与 `DisallowUnknownFields` 同一原则）。
4. **hints 内容改动必须真机实测**：「代码存在」≠「跑通」，提示词类改动尤其如此。
5. 完成后同步 README（helper 参数表在 §9.4、档案字段在 §9.5；各任务末尾都有「同步」项写明精确位置）。

### 0.4 关键前置：先补齐"能看见"

`cmd/helper/main.go:105` 现在只打印：

```go
fmt.Printf("  界面先验: %d 条\n", len(p.Hints))
```

**只有条数、没有内容、也不反映按态收窄**。而 `ActionProtocol` 在非 `-demo` 路径下根本不会被调用。⇒ **T1/T2/T3 在补上 T0 之前无法验收。**

---

## 一、背景：资料到老师的通路现状

资料进老师的唯一通路是**档案的 `hints` 字段**：

```text
profile.json 的 hints[]  →  internal/game/resolve.go:223  →  agent.ProtocolOptions.Hints
  →  agent.ActionProtocol()（internal/agent/parse.go:457）拼进提示词  →  Ollama 老师
```

另有一条临时通路：`helper -demo-hints "a;b"`（`cmd/helper/main.go:166` 定义，`internal/teacher/demonstrator.go:121-126` 合并），用于现场补先验。

**当前盘点的 hints 规模**（实测读取三个档案）：

| 档案 | 条数 | 总字符 | 战斗相关 | 大世界相关 | 两态都涉及 |
|---|---:|---:|---:|---:|---:|
| `profiles/nrc.json` | 18 | 2291 | **9 条 / 1610 字符** | 7 条 / 496 字符 | 2 条 / 185 字符 |
| `profiles/jieyou.json` | 10 | 753 | 0 | 10 / 753 | 0 |
| `internal/game/profiles/sparkle.json` | 11 | 729 | 0 | 11 / 729 | 0 |

> ⚠️ `sparkle.json` **正被另一个会话修改中**（`git status` 显示 modified：HEAD 版本 7 条 / 292 字符，
> 工作区当前 11 条 / 729 字符）。**本任务不要碰这个文件**——它在 T2 里也不需要迁移（0 条战斗先验）。
> 表内为工作区实测值，仅供理解规模。

> ⚠️ `nrc` 有**两份同名档案**（`profiles/nrc.json` 与 `internal/game/profiles/nrc.json`），内容当前完全一致，
> 任何 hints 改动都要**两份同步**——详见 T2.4。
>
> 分类口径：关键词粗筛（战斗侧命中 战斗/技能/捕捉/咕噜球/能量/逃跑/投球/回合/抓宠/出招/聚能/准星/球/瞄准/血；
> 世界侧命中 摇杆/移动/传送/地图/坐骑/滑翔/游泳/任务追踪/大地图）。**粗筛仅用于定位候选，最终归属见 §四 T2.3 的逐条复核表**——
> 其中 `idx[14]` 会被关键词误判成战斗，实际讲的是「在大世界直接抓、不必进战斗」，应归 world。

`nrc` 是压力最大的样本，也是最有力的证据：

- **战斗类 1610 字符 = 全部 hints 的 70.3%**，但**大世界态完全用不上**；
  反过来大世界的约 496 字符（摇杆位置、传送流程、坐骑滑翔）在战斗态也没用。
- 两态都涉及的那 2 条（`idx[5][6]`）恰恰是**界面态判据本身**
  （「两种界面必须先分清…」「战斗态下禁止 MOVE」）——它们**应该**两态都留。
  这说明分层设计是自洽的：把「态特有细节」收进对应态，把「态判据」留为通用。

⇒ **在大世界态，约 70% 的 hint 字符是无效负担**；9b 老师本就要在 `num_predict ≥ 1500`
的预算里完成思考，这部分先验直接稀释注意力。

---

## 二、三个缺口（均有代码证据）

### 缺口 1（结构性）｜`hints` 没有界面态分层，但按钮/宏已经有

| 项 | 是否支持 `state` | 位置 |
|---|---|---|
| 按钮 | ✅ `State string`（world \| battle） | `internal/game/profile.go:111` |
| 宏 | ✅ `State string` | `internal/game/profile.go:279` |
| 动作协议收窄 | ✅ `PressNamesForState(state)` | `internal/game/profile.go:743` |
| **hints** | ❌ **只有 `[]string`，无态** | `internal/game/profile.go:142-143` |

`internal/game/resolve.go:223` 是裸的 `Hints: p.Hints`——**无条件全量注入**。

**改法见 §四 T2**（仿照已有 `state` 机制，不发明新东西）。

### 缺口 2（防护不对称）｜视频判读侧已修，决策侧还没修

`internal/video/annotate.go:146-157` 对 hints 做了**两段式防护**，注释写明了原因：

> 这条约束是实测逼出来的：档案 hints 里写了「点任务追踪文字可以自动寻路」，
> 结果 8 段里有 6 段的「策略」都在照搬这一句，连纯过场动画那段也不例外——模型把先验当成了答案。

做法是：hints 前加「**仅用于辨认画面元素，不要把这些说法直接当作结论**」+ 结尾再次强调。

**但 `internal/agent/parse.go:460-467` 的 `ActionProtocol` 没有任何防护**：

```go
if len(o.Hints) > 0 {
    b.WriteString("【界面先验】\n")
    for _, h := range o.Hints {
        b.WriteString("- "); b.WriteString(h); b.WriteString("\n")
    }
}
b.WriteString("\n【可用动作】每条一行，只输出一行：\n")   // ← 紧接着就是动作协议
```

值得注意：`ActionProtocol` **已经吸收过同类教训**（`internal/agent/parse.go:445-456` 明确写了「占位符里绝不能出现
具体坐标数字」「不要把摇杆四坐标写进协议」），**唯独 hints 这一段没有防护**。

**现存的具体风险点**（本意是防误解，但含动作动词）：

- `nrc` hints[2]：「…**点它不会自动寻路**（2026-09-12 用 tools/watch.py 实测…）」
- `jieyou` hints[8]：「左下角任务条…只是进度显示，**点它不是收获**」

这两条都是**否定句**。按 annotate 侧的实测，模型对先验的照抄**不区分肯定/否定**——
它看到的是「点 + 自动寻路」这个词组。

**改法见 §四 T1**。

### 缺口 3（无门禁）｜hints 没有容量上限，也没有「加了有没有用」的验证

`internal/game/profile.go` 的 `normalize()` 校验覆盖了字段名（`DisallowUnknownFields`）、
按钮/宏的 `state` 合法性（`:477-480`），但**不校验 hints 的条数与单条长度**。

**后果**：

1. **线性膨胀**：按攻略持续追加会无限增长（`nrc` 已达 2291 字符），挤占提示词预算。
2. **无法归因**：加一条 hint 之后老师变好还是变差，现在没有任何机制能测出来。
3. **知识形态错配**：现有 hints 里混着对决策**无用**的内容。典型是
   `nrc` hints[12] 的抓宠成功率数值机制、hints[13] 的咕噜球分档——这是**数值知识**，
   对「这一帧该按什么」没有帮助，却实打实占着预算（合计 301 字符）。

**改法见 §四 T3**。

---

## 三、资料转换规则（比改代码更重要）

攻略原文**不能直接拷进 hints**。按「这一帧该按什么」是否有用来分拣：

| 攻略内容的形态 | 能否进 hints | 理由与去向 |
|---|---|---|
| **画面元素是什么**（摇杆在左下、五个圆钮=战斗态） | ✅ 进 | 这正是 hints 设计的用途：帮模型认画面 |
| **可观测判据**（出招后 Δ<4 就是没生效；★N/10 是能量） | ✅ 进 | 把状态变成模型能验证的判据，价值最高 |
| **怎么做**（点任务追踪→寻路、先清弹窗再走） | ⚠️ 改写成判据才能进 | 直接写会被照抄成动作（`annotate.go` 实测）；应改写成「什么情况下该做什么」的**条件式**，或直接转成 `macros`/`-plan` |
| **数值与概率**（成功率 ×1.5、球分档） | ❌ 不进 | 对单帧决策无用、纯占预算；进知识库文档 |
| **坐标** | ❌ 不进 hints | 一律放 `buttons[].pos`（一份，防漂移）；hints 里出现坐标会被照抄 |
| **单游戏玩法/剧情/关卡** | ❌ 不进仓库 | 按归档分界进 ima 知识库 |

**一句话**：hints 存的是**「怎么认」**，不是**「怎么玩」**。`macros` 与 `-plan` 才是存「怎么玩」的地方。

> ⚠️ 本规则与现有档案有冲突：`nrc` hints[0][1] 里就写死了 `x=0.21 y=0.79`、`(0.72,0.72)` 等坐标。
> **T2 不处理这个**（会引起行为变化，需单独实测），只作为 T3 之后的待办记在 §五 的「已知遗留」里。

---

## 四、实施任务书

### T0｜补 `-print-protocol` 协议打印入口

**为什么**：当前看不到最终协议，T1/T2/T3 全部无法验收（`ActionProtocol` 唯一消费点是 `internal/teacher/demonstrator.go:293`，只在 `-demo` 路径触发）。

**改动 1**：`cmd/helper/main.go`，flag 定义区（建议紧跟 `planMode`（`cmd/helper/main.go:169`）之后）：

```go
	// ---- 档案自检：打印最终动作协议（不初始化后端、不截屏、不送审）----
	printProtocol := flag.Bool("print-protocol", false,
		"打印该档案在 world/battle 两态下的完整动作协议（含界面先验）后退出。用于核验档案先验是否按态正确注入")
```

**改动 2**：`cmd/helper/main.go:287`（`printProfile(p)` 之后、`openBackend`（`:297`）之前）插入：

```go
		if *printProtocol {
			for _, st := range []string{game.StateWorld, game.StateBattle} {
				// 档案没配 battle_detect 时判不出战斗态，只打 world 一份就够了。
				if st == game.StateBattle && !p.CanDetectBattle() {
					continue
				}
				o := p.ProtocolOptionsForState(st)
				n := 0
				for _, h := range o.Hints {
					n += utf8.RuneCountInString(h)
				}
				fmt.Printf("\n===== 界面态 %s（先验 %d 条 / %d 字符）=====\n", st, len(o.Hints), n)
				fmt.Println(agent.ActionProtocol(o))
			}
			return
		}
```

**注意事项**：

- `agent` 包在 `cmd/helper/main.go:45` 已 import ✅；`utf8` 需要新增 import `"unicode/utf8"`。
- 插在档案加载块内部。若用户没给 `-game`，这段不会执行——建议在 `else` 分支开头补
  `if *printProtocol { log.Fatalf("-print-protocol 需要 -game 指定档案") }`，
  避免出现「跑了但什么都没打印」的困惑。
- `return` 在 `main()` 里是合法的（`openBackend` 尚未执行，不会有资源泄漏）。
- **行数统计用 `utf8.RuneCountInString` 而不是 `len()`**：中文字符 `len()` 是字节数（×3），字符数才是提示词预算的可比单位。本任务书里所有「字符数」都是 rune 数。

**验收**：

```bash
go run ./cmd/helper -game nrc -print-protocol
```

预期：打印两段协议，含【界面先验】与【可用动作】；header 里的条数/字符数与 §一 表格一致（**此时分层还没做，world 与 battle 两段应完全相同，都是 18 条 / 2291 字符**——这正好验证「当前确实没有分层」）。

**同步**：README §9.5（`internal/game`：一套动作跨游戏，`README.md:508` 起）补 `-print-protocol`——
它是档案自检入口，和档案字段说明同节更好找；另在 §9.4 的 `-demo-hints` 参数行（`README.md:454`）
加一句指向它，说明「只验档案先验不必开 demo」。

---

### T1｜给 `ActionProtocol` 补 hints 防护（照抄 annotate 侧的两段式）

**改动**：`internal/agent/parse.go:460-467`，整体替换为：

```go
	if len(o.Hints) > 0 {
		b.WriteString("【界面先验】（仅用于辨认画面元素，不要把这些说法直接当作结论）\n")
		for _, h := range o.Hints {
			b.WriteString("- ")
			b.WriteString(h)
			b.WriteString("\n")
		}
		// 与 internal/video/annotate.go:146-157 同一套两段式防护。原因见该处注释：
		// 档案先验里写了「点任务追踪文字可以自动寻路」后，8 段判读里有 6 段照搬这一句。
		// 决策侧的表现形式不同（模型不是复述、而是照做），但根因相同：模型把先验当答案。
		//
		// ⚠️ 措辞必须只压「把先验里的画面描述当动作」，**不能否定先验里出现的合法动作名**：
		// 分层后的 battle 先验里有 5 条直接写了 PRESS name=cast_xxx / gather_energy / cancel_aim，
		// 那是刻意的动作空间说明；若被一起压掉，老师会退回「连点技能钮」的老毛病。
		b.WriteString("以上先验只用来认画面：要执行动作时，动作名只能取下面【可用动作】里列出的那些，")
		b.WriteString("并依据当前这张画面判断该用哪一个；不要把先验里的描述直接当成动作执行。\n")
	}
```

**单测**：`internal/agent/parse_test.go`，在 `TestActionProtocol_带档案`（`:287`）后新增：

```go
// 决策侧的 hints 同样需要防护：视频判读侧（internal/video/annotate.go:146-157）
// 已因「8 段里 6 段照搬先验」补过两段式约束，决策侧（本函数）此前是零防护。
func TestActionProtocol_界面先验带防护(t *testing.T) {
	p := ActionProtocol(ProtocolOptions{Hints: []string{"点任务追踪文字可以自动寻路"}})
	for _, want := range []string{"不要把这些说法直接当作结论", "只能取下面【可用动作】里列出"} {
		if !contains(p, want) {
			t.Errorf("界面先验缺少防护话术 %q", want)
		}
	}
	// 防护话术不能把合法动作名一起否掉——battle 先验里直接写了 PRESS name=cast_xxx。
	p2 := ActionProtocol(ProtocolOptions{Buttons: []string{"cast_resha"}, Hints: []string{"直接 PRESS name=cast_resha 出招"}})
	if !contains(p2, "PRESS") || !contains(p2, "cast_resha") {
		t.Error("带档案时协议不应丢失可用动作名")
	}
}
```

**验收**：分两步，先静态后真机。

```bash
# ① 静态（零成本，先跑这个）：防护话术确实进了协议文本。
#    nrc 有 battle_detect，会打 world/battle 两段，两段都带先验 → 期望 2。
go run ./cmd/helper -game nrc -print-protocol | grep -c "不要把这些说法直接当作结论"

# ② 真机（必须做，提示词改动不能只靠静态断言）
# ⚠️ android 模式务必用 -log 落盘，不要用 PowerShell 重定向：
#    后者会经 GBK 中间层毁掉中文、吞掉换行，统计工具直接失效（现象记录在 cmd/helper/main.go 的 -log flag 说明里）。
go run ./cmd/helper -target android -game nrc -serial 1cd89cd4 -demo -demo-steps 12 -log run_t1.log
# 只验老师时不需要 -student；要顺带增量训练学生才加（见 README §9.3 的 --init 流程）
```

判定口径——**这一步的验收标准是「不劣化」，不是「变更好」**：

| 指标 | 怎么看 | 合格线 |
|---|---|---|
| 空答复/格式错误率 | 日志里「老师未返回任何内容」/ 解析失败次数 | **不高于改前** |
| 动作分布 | 12 步内出现的动作种类数 | **不少于改前** |
| 是否照抄先验里的路径 | 是否出现「TAP 到任务追踪区域」这类先验描述的动作 | 观察项，不设硬线 |

**⚠️ 必须写进报告的真实局限（别让后人误判 T1 白做）**：

- 本防护**不能**阻止模型输出 `TAP` 到任务追踪区域——`TAP` 在 world 态是**合法动作**（`AllowFreePointer=true`），防护话术压不住"选了个合法动作但动机来自先验"。
- 真正的解法是**改写 hints 内容本身**（把「做法」改成「判据」，见 §三 与 T2.4）。
- 所以 T1 的定位是：**零成本对齐 annotate 侧、消掉「把先验当模板」这一类失败模式**，不是治本。

**同步（README）**：`README.md:846-848` 已记录「hints 会被当成答案」这个坑，但括注的约束是**视频判读侧**加的。
T1 实现后在该条补一句「决策侧（`ActionProtocol`）同期对齐」——否则读 README 的人会以为两个消费点都已防护。

---

### T2｜hints 按界面态分层（核心改动）

#### T2.1 数据结构（`internal/game/profile.go:142-143`）

把：

```go
	// Hints 额外的界面先验，逐条拼进老师提示词。
	Hints []string `json:"hints,omitempty"`
```

替换为：

```go
	// Hints 额外的界面先验，逐条拼进老师提示词。**两态通用**（典型内容：界面态判据本身）。
	Hints []string `json:"hints,omitempty"`
	// HintsWorld / HintsBattle 按界面态细分的先验：只在对应态注入。
	//
	// 为什么必须分：先验是直接拼进提示词的，而老师本就要在 num_predict>=1500 的预算里
	// 完成思考。实测 nrc 档案 18 条 hints 共 2291 字符，其中战斗类 1610 字符 = 70.3%，
	// 在大世界态全是无效负担；分层后大世界态降到 887 字符（降 61.3%）。
	//
	// 语义与按钮/宏的 State 完全一致（见 PressNamesForState）：
	//   - world  态 → Hints + HintsWorld
	//   - battle 态 → Hints + HintsBattle
	//   - 未知（""）→ 按 world 处理（与 ProtocolOptionsForState 现有约定一致）
	HintsWorld  []string `json:"hints_world,omitempty"`
	HintsBattle []string `json:"hints_battle,omitempty"`
```

#### T2.2 取用方法（`internal/game/profile.go`，建议紧接 `PressNamesForState`（`:743`）之后）

```go
// HintsForState 返回某界面态下应注入老师的全部先验（通用 + 该态专属）。
//
// 未知态（""）按 world 处理，与 ProtocolOptionsForState / PressNamesForState 的既有
// 约定一致：宁可多给大世界先验，也不要因为判不出战斗态就什么都不给。
func (p *Profile) HintsForState(state string) []string {
	if p == nil {
		return nil
	}
	out := make([]string, 0, len(p.Hints)+len(p.HintsWorld)+len(p.HintsBattle))
	out = append(out, p.Hints...)
	if normalizeState(state) == StateBattle {
		out = append(out, p.HintsBattle...)
	} else {
		out = append(out, p.HintsWorld...)
	}
	return out
}
```

#### T2.3 注入点改造（`internal/game/resolve.go:222-227`）

```go
	o := agent.ProtocolOptions{
		Hints:            p.HintsForState(state), // ← 原为 p.Hints（无条件全量注入）
		HasMove:          p.Move.Mode != MoveNone && p.Move.Mode != "",
		MoveNote:         p.Move.moveNote(),
		AllowFreePointer: true,
	}
```

其余逻辑（battle 分支收窄按钮、world 分支 `p.buttonList()`）**一行都不要动**。

#### T2.4 `nrc.json` 逐条迁移表（人工复核后的归属）

⚠️ **这个仓库里有两份 `nrc.json`，内容当前完全相同（逐字节比对过），必须同步改**：

- `profiles/nrc.json` —— **工作目录优先**，`Load("nrc")` 实际加载的就是这份；
- `internal/game/profiles/nrc.json` —— 经 `go:embed` 内置，是找不到外部文件时的兜底。

只改一份的后果：本地 `-game nrc` 用新档案、而分发后（无外部 `profiles/` 目录时）走内置旧档案，
行为不一致且很难查。**两处的 `hints` 数组下标完全一致，照下表同步搬。**

**T2 阶段：除 idx[12][13] 之外全部照搬，12/13 先放进 `hints_battle`，等 T3 再迁出。**

| idx | 字符 | 建议归属 | 摘要（用于对号入座） |
|---|---:|---|---|
| 0 | 51 | `hints_world` | 横屏 3D 开放世界精灵收集游戏；左下角半透明圆形区…摇杆中心 |
| 1 | 116 | `hints_world` | 右下角按钮群（从上到下）：精灵/狼头面板、奔跑、交互星形手掌… |
| 2 | 145 | `hints_world` | 右侧上方是任务追踪文字（含目标名与距离）… |
| 3 | 49 | `hints_world` | 顶部一排图标是各类功能入口…不要乱点 |
| 4 | 47 | `hints_world` | 长按摇杆方向是持续移动；水面游泳、高山滑翔… |
| 5 | 122 | **`hints`（通用）** | ★两种界面必须先分清，动作完全不同… |
| 6 | 63 | **`hints`（通用）** | ★战斗态下【禁止 MOVE】… |
| 7 | 155 | `hints_battle` | ★战斗出招【只需一次 PRESS】… |
| 8 | 340 | `hints_battle` | ★能量机制…（见下方「建议瘦身」） |
| 9 | 56 | `hints_battle` | ★出招后想停手…PRESS name=cancel_aim |
| 10 | 134 | `hints_battle` | ★战斗抓宠固定流程：先用 cast_hetu… |
| 11 | 323 | `hints_battle` | ★投球是**两次点击**，不是划动…（见下方「建议瘦身」） |
| 12 | 164 | **`hints_battle`** → T3 迁出 | ★战斗抓宠的成功率机制…麻醉≈×1.5、睡眠≈×2 |
| 13 | 137 | **`hints_battle`** → T3 迁出 | ★咕噜球分档（决定成功率，从低到高）… |
| 14 | 206 | `hints_world` | ★大世界也能直接抓（不必进战斗）…（关键词会误判成战斗，实为世界） |
| 15 | 95 | `hints_battle` | 战斗打不过用 PRESS name=battle_flee 逃跑… |
| 16 | 42 | **`hints`（通用）** | 弹出奖励/解锁弹窗时先点关闭…黑屏转场是在加载 |
| 17 | 46 | **`hints`（通用）** | 画面中央偏下是玩家角色；出现对话气泡… |

**迁移后的基线数字（分两阶段，别混用）**：

**T2 阶段——只做分层，不做内容取舍**：idx[12][13] 暂时也放进 `hints_battle`，验收按这张表：

| 分组 | 条数 | 字符 |
|---|---:|---:|
| `hints`（通用） | 4 | 273 |
| `hints_world` | 6 | 614 |
| `hints_battle` | 6 | 1103 |
| **world 态注入** = 通用 + world | **10** | **887**（原 2291，**降 61.3%**） |
| **battle 态注入** = 通用 + battle | **10** | **1376**（原 2291，**降 39.9%**） |

**T3 阶段——再把 idx[12][13]（共 301 字符）迁出之后**：`hints_battle` 变 **4 条 / 802 字符**，
**battle 态注入降到 1075 字符（原 2291，降 53.1%）**；world 态注入不变（仍 887 字符）。

> ⚠️ 两次验收的 battle 数字不同（T2 是 **1376**，T3 后是 **1075**）。**不要拿 T3 的数字去卡 T2**，
> 也不要在 T2 里顺手把 12/13 删掉——分层与瘦身分开做，验收数字才有可比性（见 §七）。

> 迁出的 idx[12][13] 去向建议：这类数值对「这一帧按什么」无用，但属于单游戏玩法知识，
> **放 ima 知识库**（`nrc` 攻略条目）或 `outputs/` 下的单游戏笔记；
> **不要**为了"以后可能有用"继续挂在提示词里。

**建议瘦身（可选，需单独实测，不要和分层混在一轮做）**：

- `idx[8]`（340 字符）、`idx[11]`（323 字符）是两条最长的先验，内容里混着**过程性叙述**
  （「旧笔记的『沿黄色抛物线 fling』已被真机实测证伪」「两坐标尚在标定中」）——这是**文档口吻，不是提示词口吻**。
  改写方向：只留可观测判据（出招后 Δ<4 → 能量不足 → `gather_energy`），把证伪过程移到档案外的笔记。
- `idx[2]`、`jieyou hints[8]` 的**否定句改写**（去掉「点它…」这个动作动词模式，改成判据）：
  - 原：`…它只用来读目标方向和距离，点它不会自动寻路…`
  - 改：`…它是**只读显示区**，不是按钮；长距离移动靠坐骑或大地图传送。`
  - 原：`左下角任务条…只是进度显示，点它不是收获`
  - 改：`左下角任务是**只读进度显示**，不是可点击的交互目标。`

#### T2.5 校验（可选，属于 T3 的一部分，可一起做）

在 `normalize()`（`internal/game/profile.go:427`）末尾调用校验函数；实现见 T3.1。

#### T2.6 单测

`internal/game/resolve_test.go`，在 `TestProtocolOptions_按档案裁剪动作空间`（`:282`）后新增：

```go
// hints 必须按界面态分层注入：战斗态不该看到摇杆类先验，世界态不该看到技能类先验。
func TestProtocolOptions_先验按界面态分层(t *testing.T) {
	p := &Profile{
		Name:        "分层测试",
		Hints:       []string{"通用"},
		HintsWorld:  []string{"摇杆在世界态"},
		HintsBattle: []string{"技能在战斗态"},
	}
	join := func(ss []string) string { return strings.Join(ss, "|") }

	w := join(p.ProtocolOptionsForState(StateWorld).Hints)
	if !strings.Contains(w, "通用") || !strings.Contains(w, "摇杆在世界态") || strings.Contains(w, "技能在战斗态") {
		t.Errorf("world 态先验 = %q", w)
	}
	b := join(p.ProtocolOptionsForState(StateBattle).Hints)
	if !strings.Contains(b, "通用") || !strings.Contains(b, "技能在战斗态") || strings.Contains(b, "摇杆在世界态") {
		t.Errorf("battle 态先验 = %q", b)
	}
	// 未知态按 world 处理（与按钮收窄的既有约定一致）
	u := join(p.ProtocolOptionsForState("").Hints)
	if !strings.Contains(u, "摇杆在世界态") || strings.Contains(u, "技能在战斗态") {
		t.Errorf("未知态先验 = %q，应等同 world", u)
	}
}
```

另外把 `internal/game/profile_test.go:51-53` 的断言从「`p.Hints` 非空」放宽为「任一分层字段非空」，否则它只覆盖通用桶：

```go
	if len(p.Hints)+len(p.HintsWorld)+len(p.HintsBattle) == 0 {
		t.Error("nrc 档案应带有界面先验")
	}
```

#### T2.7 验收

```bash
go run ./cmd/helper -game nrc -print-protocol | head -60
```

对照检查：

1. world 段 header 应为 **10 条 / 887 字符**；battle 段应为 **10 条 / 1376 字符**
   （T2 阶段 idx[12][13] 仍留在 battle 桶里，见 T2.4 的两阶段口径；等 T3 迁出后才变成
   4 条 / 802 字符 → battle 注入 1075）。
2. world 段**不含** `cast_` / `gather_energy` / `咕噜球` / `能量机制` 等战斗条目。
3. battle 段**不含** `摇杆` / `传送` / `坐骑` / `滑翔` 等大世界条目。
4. **两段都必须包含** `idx[5][6]` 这两条界面态判据（「两种界面必须先分清」「战斗态下禁止 MOVE」）——
   它们是分层的边界用例，漏了就是把判据本身也裁掉了。
5. 真机各跑一段（world 一段 + 战斗一段）确认老师动作空间未被抖散。

**⚠️ 静态打印 ≠ 运行时判定**：`-print-protocol` 打印的是「`ProtocolOptionsForState` 在给定态下会生成什么」，
**不是**「运行时真的会进到哪个态」。实际态由 `internal/game/detect.go` 的像素判据给出（写入 `UIState`）。
所以真机验收时要去日志里确认**确实出现过 `界面态: battle` 之类的判定**，否则 battle 段的先验永远注入不到。

**同步（README，必须做）**：

- `README.md:545-550` 的档案字段说明补 `hints_world` / `hints_battle`，并写清注入规则
  （world 态 = `hints` + `hints_world`；battle 态 = `hints` + `hints_battle`；**未知态按 world**）。
- `README.md:454`（§9.4 的 `-demo-hints` 参数行）加一句指向 `-print-protocol`，说明「只验档案先验不必开 demo」。
- 门禁上限值（T3）也一并写进去，否则下次改档案的人不知道边界在哪。

---

### T3｜容量门禁 + A/B 验证流程

#### T3.1 门禁（`internal/game/profile.go`）

在 `normalizeState` 附近新增：

```go
// 先验容量门禁。取值依据：现网最大档案 nrc 分层后 world 887 字符、
// battle 1075 字符（迁出数值类条目后；迁出前 1376），单条最长 340 字符。
// 上限取「现有档案全过、再涨一倍就报错」的量级，
// 确保门禁先卡住「线性膨胀」，而不是一上来就否掉现有档案。
const (
	maxHintsTotalChars = 4000 // 单态注入（通用 + 该态）的字符上限
	maxHintChars       = 500  // 单条先验的字符上限
)

// normalizeHints 逐条 trim 并执行容量门禁。
// 与按钮/宏的 state 校验同一原则：档案写错要在加载时报出来，不许静默退化。
func (p *Profile) normalizeHints() error {
	groups := []struct {
		field string
		items *[]string
	}{
		{"hints", &p.Hints},
		{"hints_world", &p.HintsWorld},
		{"hints_battle", &p.HintsBattle},
	}
	for _, g := range groups {
		for i := range *g.items {
			s := strings.TrimSpace((*g.items)[i])
			(*g.items)[i] = s
			if s == "" {
				return fmt.Errorf("%s[%d] 是空条目（先验逐条进提示词，空条只是白占位置）", g.field, i)
			}
			if n := utf8.RuneCountInString(s); n > maxHintChars {
				return fmt.Errorf("%s[%d] 长 %d 字符，超过单条上限 %d——先验直接进提示词，太长会挤掉思考预算（请精简为可观测判据，或拆成多条）",
					g.field, i, n, maxHintChars)
			}
		}
	}
	for _, st := range []string{StateWorld, StateBattle} {
		n := 0
		for _, s := range p.HintsForState(st) {
			n += utf8.RuneCountInString(s)
		}
		if n > maxHintsTotalChars {
			return fmt.Errorf("%s 态先验合计 %d 字符，超过上限 %d（请拆到 hints_world/hints_battle，或把数值/概率类内容移出先验）",
				st, n, maxHintsTotalChars)
		}
	}
	return nil
}
```

在 `normalize()` 里调用。`normalize()` 内的现有顺序是 name → plan → move → buttons → Escape（`:496`）
→ Macros（`:519`）→ BattleDetect（`:557`）→ `return nil`（`:587`）。hints 与其它字段无依赖，
**建议插在 `BattleDetect` 校验块之后、`return nil` 之前（即 `:586` 之后）**，最不容易和并发改动冲突：

```go
	if err := p.normalizeHints(); err != nil {
		return err
	}
```

**注意事项**：

- 需要新增 import `"unicode/utf8"`。
- `items *[]string` 用指针是刻意的：`[]string` 直接放进 struct 只在语法上更短，语义上仍可写
  （切片共享底层数组），但**指针形式不依赖这个细节**，避免后人误以为改动没生效。
- 上限值 `4000` / `500` **没有实测支撑，是量级判断**。执行者若认为不合适可调，但**必须同步写进 README**，否则下一次改档案的人不知道边界在哪。

#### T3.2 单测

`internal/game/profile_test.go` 的 `TestLoad_校验规则`（`:88`）里补三条：

```go
	// 空条目 / 单条超长 / 单态合计超限，都必须在加载时报错
	for _, tc := range []struct{ name, raw, want string }{
		{"空条目", `{"name":"t","move":{"mode":"none"},"hints":["ok","  "]}`,
			"空条目"},
		{"单条超长", `{"name":"t","move":{"mode":"none"},"hints":["` + strings.Repeat("字", 501) + `"]}`,
			"超过单条上限"},
		{"合计超限", `{"name":"t","move":{"mode":"none"},"hints_world":["` + strings.Repeat("字", 500) + `","` + strings.Repeat("字", 500) + `","` + strings.Repeat("字", 500) + `","` + strings.Repeat("字", 500) + `","` + strings.Repeat("字", 500) + `","` + strings.Repeat("字", 500) + `","` + strings.Repeat("字", 500) + `","` + strings.Repeat("字", 500) + `","` + strings.Repeat("字", 500) + `","` + strings.Repeat("字", 500) + `"]}`,
			"超过上限"},
	} {
		_, err := decode(tc.name, []byte(tc.raw))
		if err == nil || !strings.Contains(err.Error(), tc.want) {
			t.Errorf("%s: err = %v, 期望含 %q", tc.name, err, tc.want)
		}
	}
```

> 「合计超限」用例是 10 条 × 500 字 = 5000 > 4000。照上面的写法太啰嗦，执行时用
> `strings.Join` + `strings.Repeat` 拼 JSON。⚠️ `internal/game/profile_test.go` 当前 import 块
> **没有 `strings`**（只有 `os`/`path/filepath`/`testing`/`internal/agent`），需补上；
> `decode` 是包内非导出函数，可直接调用，签名为 `decode(src string, data []byte) (*Profile, error)`（`internal/game/profile.go:413`）。

#### T3.3 A/B 验证流程（加 hint 的标准动作）

门禁只解决"能不能加"，不解决"该不该加"。新增一条 hint 的流程：

1. 固定起始画面（同一存档点/同一张截图流）、固定 `-teacher-model` 与提示词版本。
2. 跑 N 次 `-demo`（建议 N≥5，小模型输出有随机性），记录动作序列与 `-demo-out` 产物。
3. 在同一批画面上**只多/少这一条 hint**，再跑 N 次，对比：
   - 动作分布是否朝预期方向变化；
   - 空答复/重复/横跳告警次数是否变差；
   - 是否出现「照抄该条 hint」的迹象。
4. 有正向证据才入库；无证据不入库，报告里如实写「无显著差异」。
5. 报告落地到本目录（`outputs/`），文件名带日期。

**验收**：一份 A/B 报告 + 一份 hints 瘦身前后（分层前后）的字符数对照表。

---

## 五、验收清单（DoD）

执行完成后，以下每一条都要有输出证据，**不接受"代码已写"作为完成**：

- [ ] `go test ./...` 全绿。
- [ ] `go vet ./...`、`go build ./...`、`gofmt -l` 无输出。
- [ ] `go run ./cmd/helper -game nrc -print-protocol`：**T2 阶段** world 10 条 / 887 字符、battle 10 条 / 1376 字符；**T3 迁出数值条目后** battle 变 8 条 / 1075 字符（4 条通用 + 4 条 battle 桶），world 仍 10 条 / 887 字符。两段都必须含界面态判据 `idx[5][6]`。
- [ ] `go run ./cmd/helper -game jieyou -print-protocol` 与 `-game mobile_generic` 不报错、行为与改前一致（这两个档案没有战斗条目，**分层对它们应是零影响** —— 这是回归基线）。`sparkle` 被并发会话改动中，不纳入本轮验收。
- [ ] 真机（`-serial 1cd89cd4`）world 段 + 战斗段各跑一段 `-demo`，日志给出空答复率、动作种类数、重复告警次数。
- [ ] README 已同步：`-print-protocol`、`hints_world`/`hints_battle` 字段、门禁上限值。

**已知遗留（本轮不做，但要写进报告）**：

1. `nrc` hints[0][1] 里写死的坐标违反 §三「坐标不进 hints」规则（应只留在 `buttons[].pos`）。
   处理需要单独一轮实测（模型改从按钮 note 获取位置信息）。
2. `hints[8]`/`hints[11]` 的过程性叙述尚未精简。
3. `jieyou`/`sparkle` 的 hints 未做「判据化」改写（当前全部是 world 条目，分层不影响它们）。

---

## 六、影响面（改 hints 会牵动谁）

| 调用点 | 是否受影响 | 处理 |
|---|---|---|
| `internal/game/resolve.go:223` | ✅ **主要改动点** | 换成 `HintsForState(state)` |
| `internal/teacher/demonstrator.go:114-128` | ⚠️ 无需改代码，但注释要更新 | `o.Hints` 已是按态合并结果，`-demo-hints` 追加在其后，顺序正确 |
| `internal/teacher/demonstrator.go:293` | 间接（协议文本变长/变短） | 无代码改动 |
| `cmd/video/main.go:369`（`hints = append(hints, prof.Hints...)`） | ⚠️ **决定：本轮不改** | 视频判读是**离线**的、没有界面态概念，且 `annotate.go` 已有防护。**但要注明**：分层后它只拿到通用桶，可能丢失世界/战斗类先验——若以后发现判读质量下降，再按「判读时段内的界面态」注入 |
| `cmd/helper/main.go:105` | 建议顺手改 | 打印条数时区分三个桶，或直接指向 `-print-protocol` |
| `profiles/jieyou.json`、`internal/game/profiles/sparkle.json` | ❌ 不受影响 | 无战斗条目 → 全在通用/世界桶（`sparkle` 正被并发会话改动，**不要碰**） |

---

## 七、明确不做

- **不引入向量检索 / RAG**。当前 hints 规模是**几千字符**，全量注入的 token 成本远低于
  引入检索的复杂度与不确定性。**RAG 要等 hints 上万字符规模才值得考虑**——先做分层与门禁，
  把规模问题暴露出来再说。
- **不把攻略原文成段拷进 hints**（理由见 §三，且 `annotate.go` 的坑已验证照抄风险）。
- **不改 teacher 的异步架构**（`DEVELOPMENT_PLAN` §1.3 硬边界：教师不得阻塞实时回路）。
- **不在没有 A/B 证据的情况下批量追加 hints**（等价于「堆数据＝提升」的错觉，本项目已吃过一次）。
- **不把这套 hints 机制与「让策略头学会新游戏」混为一谈**——资料对策略头是间接作用，
  中间「采真机示范」省不掉（见 §九）。
- **不在 T2 里顺手做 hints 内容瘦身**：分层与瘦身是两件事，混做会让验收数字失去可比性。

---

## 八、风险与回滚

| 风险 | 触发条件 | 处置 |
|---|---|---|
| 分层后漏注入 | 档案作者把两态都需要的判据写进了 `hints_battle` | `-print-protocol` 能一眼看出；T2.7 第 4 条专门验这个 |
| 门禁误伤现有档案 | 上限设得过紧 | 现有最大档案 1376 < 4000，有余量；上调上限需同步 README |
| T1 改提示词导致老师变差 | 防护话术被理解成「不要用先验」 | 回滚只需删两行字符串；验收口径是「不劣化」（见 T1） |
| 未知态行为变化 | 未配 `battle_detect` 的档案永远走 world 分支 | 这是**刻意的**，与 `ProtocolOptionsForState` 既有约定一致；`jieyou`/`sparkle` 回归基线已覆盖 |

---

## 九、与两份计划文档的关系

- 本清单服务 `DEVELOPMENT_PLAN` §1.4 第 3 条与学习计划 §1.3 第 4 条
  （资料对策略头是间接作用，对老师是直接作用）。
- T2 依赖的界面态能力来自 `internal/game/profile.go` 的 `state` 机制与
  `internal/game/detect.go` 的战斗判据，**不新增架构**。
- 本任务书已于 **2026-09-16** 由执行会话实施完毕（T0–T3 全部落地；逐条验收与遗留见 §十 执行状态）。

---

## 十、执行状态（2026-09-16 实施记录）

**结论：T0–T3 全部落地；§五 验收清单除「真机 `-demo` 两段」一项（设备不在线）外全部通过。**
实施提交：见仓库 git log 2026-09-16（`学生模式真机接入彩色` 之后的 `资料→老师` 系列提交）。

### 10.1 逐条验收证据

| 验收项 | 结果 |
|---|---|
| `go test ./...` | 全绿（`-count=1` 强制重跑过） |
| `go vet ./...` / `go build ./...` | 通过 |
| `gofmt -l` | 无输出（按 **LF 化副本**口径核验——仓库存量 CRLF 文件的原生 `gofmt -l` 全文件误报，与本轮无关） |
| `-game nrc -print-protocol` | world **10 条 / 887 字符**、battle **10 条 / 1376 字符**；两段均含 idx[5][6] 判据；world 段零战斗条目（无 `cast_` / 咕噜球 / `gather_energy`）、battle 段零大世界专属条目（摇杆长按与坐标仅在 world） |
| `-game jieyou -print-protocol` | 加载成功、world 10 条 / 753 字符。顺手清理 `"zoom": true` 存量残留（全代码库已无消费点，且会致 `DisallowUnknownFields` 加载失败——**改前该档案完全无法加载**） |
| `-game mobile_generic -print-protocol` | 零影响（world 3 条 / 82 字符与改前一致；无 `battle_detect`，只有 world 段） |
| 文档同步 | README 已拆分 → 正文同步到 `docs/action-layer.md`（§9.5：三桶字段与注入规则、门禁 500/4000、`-print-protocol` 自检）与 `docs/teacher-demo-video.md`（§9.4：`-demo-hints` 行指向 `-print-protocol`；hints 坑条目补「决策侧（`ActionProtocol`）同期对齐」） |
| 真机 `-demo` 两段（world / 战斗） | **未执行**：验收所需真机 `1cd89cd4` 不在线（当时仅 `ecbff3a5` 与远程机），Ollama 亦未响应。待设备恢复后补跑，口径 = 不劣化（空答复率 / 动作种类数 / 重复告警次数） |

### 10.2 与 §五 标称数字的差异说明（重要）

实测分两阶段记录：**T2 阶段** battle **12 条 / 1677 字符**（12/13 尚在桶内）→ **T3 迁出后** battle **10 条 / 1376 字符**。

§五 标称「T2 阶段 battle 10 条 / 1376；T3 迁出后 8 条 / 1075」的核对：

- **1376 与实际终态吻合**（标称的「T2 阶段」数字实为 12/13 迁出后的终态值）；
- `1075` 与「8 条」无法由任何条目组合导出（逐条核算 155/340/56/134/323/164/137/95 的全部组合）——判定为算术笔误；
- 执行以 §四 分组表（T2.4）的**文字指令**为准：12/13 先入 battle 桶（T2）→ 迁出到 `outputs/洛克王国世界-抓宠玩法知识.md`（T3）。终态实测 world 10/887、battle 10/1376。

### 10.3 附带修复与收尾（§六 影响面三条 + 计划外两条）

1. `cmd/helper/main.go`：档案摘要打印改为三桶分布（实测输出「界面先验: 16 条（通用 4 / 大世界 6 / 战斗 6）」）。
2. `internal/teacher/demonstrator.go`：`protocolOptions` 注释更新（`o.Hints` 是 `HintsForState(d.UIState)` 的按态合并结果；`-demo-hints` 追加在其后）。
3. `cmd/video/main.go`：注明视频判读只取通用桶（离线无界面态概念）；若日后判读质量下降，再按「判读时段内的界面态」注入。
4. **计划外**：`profiles/jieyou.json` 清理 `"zoom": true` 残留（见 10.1）。
5. **计划外**：`internal/game/profile.go` 的 `Hints` 字段对齐空格修正（gofmt 核验发现）。

### 10.4 §五「已知遗留」承接（本轮不做，留待后续轮）

1. nrc hints[0][1] 坐标写死（违反「坐标不进 hints」规则）——需单独一轮实测（模型改从按钮 note 获取位置）。
2. hints[8]/hints[11] 过程性叙述精简——未做。
3. `jieyou`/`sparkle` hints 判据化改写——未做（分层对二者零影响；`sparkle` 因并发会话改动未纳入本轮）。
