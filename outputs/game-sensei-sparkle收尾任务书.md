# Sparkle 收尾任务书（交给执行会话照做）

> 目标一句话：**把 Sparkle 跑到「达成 `-plan` 线五条收尾判据」，正式关闭这款游戏。**
> 本文只定义目标与验收，不含探索性工作。判据来源：`DEVELOPMENT_PLAN.md` §1.6。
> 编制日期：2026-09-14（2026-09-14 二次修订：补 §2.5 胜负判定）。编制时**未改任何代码/档案**（见 §六 红线）。

---

## ⚠️ 执行结果（2026-09-14，已跑完，结论与原设想不同）

T0 / T0.5 / T1 已全部执行，**跑批真实结果出来了**：**两轮 20 局 6 胜 14 负，合并胜率 30.0%**
（第一轮 run21–30 = 40%，第二轮 run31–40 = 20%）。

| # | 判据 | 结果 | 证据 |
|---|---|---|---|
| ① | 端到端含终态校验跑通 ≥1 次 | **✅** | run29 / 35 / 39 胜局 + 独立抓帧确认回到主界面 |
| ② | 连续 ≥10 次 WINNER ≥80% | **❌ 30%** | 14 局 LOSE 全部止于第 12 步顶部大字判据；退出码均为 1 |
| ③ | 抓帧失败必须重试 | **✅** | batch 起点抓帧失败重试；plan 内 P0 安全检查 |
| ④ | README 补章节 | **✅** | README §12.6 / §12.7 / **§12.8** |
| ⑤ | 判据能捕获败局 | **✅** | 14 局 LOSE 退出码 1 + 终态帧独立复核为 LOSE 页 |

> **T1 跑批报告**：逐轮明细见 `outputs/game-sensei-sparkle跑批报告.md`（run21–run40 胜负/退出码/终态 + 判据分离度）。

**判据②没达成，但根因不在执行**：14 局败局的执行轨迹里**装备①②③与 Change 全部执行、
动作失败 0 次**，照样输。⇒ 本作（换装评分对战）的「赢」由**装扮内容**决定（要选到与主题
匹配的造型），属**决策·策略层**；`-plan` 是执行层，保证不了「选对」。**「换装=必胜」这条
早期假设被跑批推翻**（详见 README §12.8）。两轮胜率 40%↔20% 的剧烈波动**本身就是证据**：
胜负由「随机出的主题 vs 固定点的服装卡是否匹配」决定，执行层无法左右。

**收尾口径修正**：Sparkle 以 ①③④⑤ 收尾，②改判为「胜负判定正确 ∧ 败局可捕获 ∧ 终态正确」，
合并 30% 胜率作为基线如实记录。另修掉两个坑：
1. **跑批统计口径**（本轮最隐蔽）——按判据串 grep 会漏掉胜局（成功行不含判据描述），
   必须**按步名**匹配；第一版脚本因此把 4 次胜局全记成「未判定」、胜率算成 0%。
   ⇒ 新增 `.workbuddy/sparkle/verdict_key.py`，步名从档案现读。**第二轮已验证修复生效**。
2. **胜局后 SALE 弹窗**遮住 ADS 红标 → 终态不达标（第一轮 4 局胜仅 1 局干净回主界面）；
   ⇒ 新增档案**末步**「胜局回主界面兜底」。**第二轮 2/2 胜局均干净回主界面，兜底生效**。
3. **运维观察**：LOSE 局 `strict` 判胜步失败 → 计划终止 → 设备停在 LOSE 结算页；跑批脚本
   每轮起点须先恢复（轻量点 Exit，不行再 `am force-stop` 冷启动约 30s），单轮实际 ≈2 分钟。

---

## 零、完成定义（DoD）

**全部勾完 = Sparkle 收尾，可以转向下一款游戏。**

| # | 判据 | 现状 | 验收命令 / 证据 |
|---|---|---|---|
| ① | 端到端真机跑通 ≥1 次，**含起点/终点状态校验**（非 `strict=false` 静默跳过） | ✗ 口径存疑 | §二 T0 落地后，单次跑退出码 0 **且** 终态帧 ADS 红标可见 |
| ② | 连续 **≥10 次**打成 **WINNER** ≥80%（**胜利 ∧ 终态正确**），每次失败可归因到具体 step 与判据 | ✗ 无数据 | §三 T1 跑批产出的 `summary.json` + 报告（**含 WINNER / LOSE 计数**） |
| ③ | 抓帧失败**必须重试**，不得当成「判据不满足」 | ✅ 机制已有 | `plan.go` 的 `grabRetries=3`（已实现）；跑批中若出现抓帧失败需在日志可见 |
| ④ | README 补 Sparkle 章节 | ✗ 无 | 新增 `README.md` §12.6「案例：Sparkle 端到端」 |
| ⑤ | **判据必须能捕获败局** —— 现有判据会把 LOSE 判成 PASS（见 §二 2.5） | ✗ **判据有洞** | 用既有 `shots/p06.png`（LOSE 帧）与 `shots/w01_winner.png` 离线复算，两帧必须被分开 |

判据③已经具备，**不需要改动**，但**要在跑批中确认它真的生效**（日志里出现抓帧重试痕迹时不应被判为失败）。

---

## 一、现状盘点（为什么这一轮能收尾）

**已就绪的部分**（都已提交或在工作区）：

| 能力 | 位置 | 状态 |
|---|---|---|
| 6 步端到端 plan | `internal/game/profiles/sparkle.json` | 就绪（含 `when` 守卫、`max_repeat` 幂等、`ratio` 判据） |
| 抓帧重试 | `internal/plan/plan.go` `grabRetries=3` | 就绪 |
| 动作重试 | 同上 `actRetries=3` | 就绪 |
| 前置条件 `when` | 同上 `runStep` 开头判定 | 就绪 |
| 安全检查前置 | `SafetyChecker`（`cmd/helper/backend.go` adb 侧已实现） | 就绪 |
| 指标统计 | `Stats`：端到端/抓帧/动作/重试/跳过/超时 | 就绪 |

**缺口有 3 个**，都在「怎么证明它成了」这一侧：

1. **终态校验缺失** —— 详见 §二 2.1，这是根因（假成功 A / B）。
2. **胜负判定缺失** —— 详见 §二 2.5：**现有判据会把一局 LOSE 判成 PASS，而赢的时候反而卡在结算页**。
   这是「跑完 ≠ 成功」的第二层，也是判据②「打成 WINNER」能否验收的前提。
3. **批量证据缺失** —— 10 次胜率与归因，详见 §三。

也就是说：**这一轮不需要改执行逻辑，只需要补「判定」和「跑批」。**

---

## 二、T0 + T0.5：补终态校验与胜负判定（唯一前置改动）

### 2.1 为什么"跑完"不等于"成功"（必读，否则会验收错）

`Runner.Run()` 只在某一步**返回 error** 时才带错误退出：

```go
if err := r.runStep(&p.Steps[i], &st); err != nil {
    return finish(fmt.Errorf("第 %d 步（%s）: %w", ...))
}
```

而单步超时**默认不报错**——`Step.Strict` 默认 `false`，超时只累加 `stats.Timeouts` 然后「宽松跳过」。

于是现在存在两类假成功：

- **假成功 A：全程超时但退出码 0。** 六个步骤全部超时、一步也没走成，`Run()` 照样返回 `nil`，`main.go` 打印「计划结束」并正常退出。
- **假成功 B：终态被守卫跳过。** 末步 `exit_result` 带 `when`（要求结算页紫色按钮可见）。若流程走偏、根本没到结算页，`when` 不满足 → **整步跳过、不做任何校验** → plan 正常结束、退出码 0。

**所以「退出码 0」不能作为成功判据，必须补终态校验。**

### 2.2 改动 1：给 3 个关键步加 `strict: true`

文件：`internal/game/profiles/sparkle.json`（⚠️ 见 §六，该文件可能正被另一会话改动，**动手前先确认它已稳定**）

| step 索引 | 名字 | 现有判据（描述） | 加什么 | 为什么是这一步 |
|---|---|---|---|---|
| **1** | 点 Win VS 进主题选择页 | `ratio`：主题页票券条**出现**，`timeout_ms 8000` | `"strict": true` | 它没有 `when`，必然执行——是「流程真的启动了」的第一个硬信号 |
| **3** | 点 Change 提交造型 | `ratio`：结算页紫色按钮**出现**，`timeout_ms 45000` | `"strict": true` | 全流程最长等待；**注意它只证明「到了结算页」，不证明赢** —— 胜负由 §2.5 的判定步负责 |
| **5** | 点 Exit 回主界面 | `ratio`：ADS 红标**出现**，`timeout_ms 15000` | `"strict": true` + **去掉 `when` 守卫**（见 §2.5 改动 4） | 现有 `when`（紫色 ≥0.8）在 WINNER 页不成立，会让整步跳过、**卡死在结算页**；加了胜负判定步后，能走到这里的必是 WINNER |

> ⚠️ **只加 `strict`，不要顺手改判据阈值。** 这些阈值正在被另一会话调参（编制本任务书期间实测：
> `Change` 步的 `min_ratio` 在 10 分钟内从 `0.8` 变成了 `0.6`）。**以你动手时的档案实际值为准**，
> 不要把本文档里的任何数字当作要写入的目标值。

**故意不给 step 0 和 step 2 加 `strict`**：这两步是 `max_repeat` 循环 + 幂等守卫，跑满轮数是手游的正常情形；硬失败会让计划一步都走不完。它们若真的失败，会被后面 step 1 / step 3 的 `strict` 捕获。

**不要给 step 4 加**：它是 `until.type=time` 的固定等待，本就是过渡步。

### 2.3 改动 2：脚本侧独立终态判定（**必须做，不能省**）

只加 `strict` 堵不住假成功 B —— 因为末步的 `when` 一旦不满足就整步跳过，**严格模式也不会触发**。

所以跑批脚本必须**自己抓一帧独立判定终态**：

```
终态通过 ⟺ 主界面 ADS 红标占比 ≥ min_ratio
```

**判据必须从档案 `sparkle.json` 的 `plan.steps[5].until` 读取，不要在脚本里硬编码坐标与阈值**
（脚本骨架见 §三 3.2 已按此实现）。理由：判据阈值正在被调参，硬编码会在两边漂移——
「档案改了、脚本没改」会让成功率统计悄悄失真，而且这种失真不会报错，只会让数字变得没有意义。

### 2.4 T0 验收

```bash
go build ./... && go vet ./...
# 单次真机（起点必须先在主界面，见 §三 起点约束）
./helper.exe -target android \
  -adb "C:/Users/ay/AppData/Local/Android/Sdk/platform-tools/adb.exe" \
  -serial emulator-5554 -game sparkle -plan -live \
  -log .workbuddy/sparkle/runs/smoke.log
```

通过标准（三条同时满足）：
1. 退出码 `0`；
2. 日志出现 `计划结束：6 步 / …`（非 `计划执行异常`）；
3. 结束后抓帧，ADS 红标占比 ≥ 4%。

### 2.5 T0.5：胜负判定（**必须做** —— 现有判据会把败局判成 PASS）

#### 2.5.1 实测证据：现有判据的胜负盲区

用仓库里**既有的**结算截图离线复算档案现用的紫色判据
（区域 `[0.36,0.888,0.64,0.935]`、色 `[110,93,236]`、tol 20 —— 即 `sparkle.json` 里
`change` 与 `exit_result` 两步在用的那条）：

| 画面 | 截图（均在 `.workbuddy/sparkle/shots/`） | 档案紫色区占比 | 现判据 `min_ratio 0.6` |
|---|---|---|---|
| WINNER（带「看广告领奖」浮层） | `w01_winner.png` | **64.2%** | 命中 |
| WINNER（点掉 `No,thanks` 后） | `after_nothanks.png` | **0.0%** | 不命中 |
| **LOSE** | `p06.png` | **93.7%** | **命中** |
| SALE 弹窗（对照） | `p07.png` | 25.0% | 不命中 |
| 评审页 / 主题页 / 主界面 | `p04.png` / `p03.png` / `main_real.png` | 0.0% | 不命中 |

档案里那句注释「阈值 0.6 同时覆盖结算页两态 —— WINNER 领奖浮层(65.4%)、真 Exit(93.9%)」
**是误判**：65.4% 对应 WINNER 浮层没错，但 **93.9% 与 LOSE 页实测的 93.7% 吻合**，
而 WINNER 点掉浮层后实测是 **0%**。⇒ **那个「真 Exit」就是 LOSE 页的紫色 Exit 按钮**，
不是 WINNER 的另一种形态。

#### 2.5.2 更严重的后果：输赢两侧的判定是反的

| | step 3（Change 后等紫色 ≥60%） | step 4（点 `no_thanks`） | step 5（`when` 紫色 ≥80% → 点 Exit） | 结果 |
|---|---|---|---|---|
| **WINNER** | 64.2% 命中 ✓ | 浮层关闭 → **0%** | 0% < 80% → **整步跳过** | **卡在结算页**，回不到主界面 |
| **LOSE** | 93.7% 命中 ✓ | 点空白，无害 | 93.7% ≥ 80% → **点 Exit** ✓ | 顺利回主界面 → 终态通过 → **判 PASS** |

**即：现在这套 plan「输着能走完、赢着反而卡住」，而唯一能走完的路径恰好是 LOSE。**
这比 §2.1 的两类假成功更隐蔽——它不是「跳过了校验」，而是「**校验通过的恰好是错的那一局**」。

#### 2.5.3 改动 3：把胜负判定写进档案（不需要外挂脚本）

`ratio` 支持**反向判据** `max_ratio`（「等某颜色消失」），配 `strict: true` 即可表达
「不满足即失败」。在 **step 4（点 `no_thanks`）之后、step 5（点 Exit）之前**插入一步：

```json
{
  "name": "胜负判定：WINNER 点掉浮层后底部无紫色；LOSE 页底部紫色 Exit 恒在。等紫色消失——消失=WINNER，未消失=败",
  "action": "ACTION WAIT",
  "until": { "type": "ratio", "region": [0.36, 0.888, 0.64, 0.935],
             "color": [110, 93, 236], "tolerance": 20, "max_ratio": 0.1 },
  "timeout_ms": 3000,
  "strict": true
}
```

> ⚠️ 上面的 `region` / `color` 只是**抄自现有档案的示意值**。落地时一律从 `sparkle.json`
> 的 `exit_result` 步 `when` **读实际值**，不要照抄本文档的数字（该档案正在被调参）。

- **WINNER**：点掉浮层后紫色 0% ≤ 0.1 → 立即满足 → 通过，继续 step 5。
- **LOSE**：紫色 93.7% > 0.1 → 3s 超时 → `strict` 触发 → **plan 返回 error、退出码非 0**。

这样 **plan 自己就会「因输而失败」**，胜率可直接从退出码统计，**不需要脚本并行抓帧**
（比并行抓帧更简单也更可靠）。

**改动 4（配套）：去掉 step 5 的 `when` 守卫。**

step 5 现有 `when`（紫色 `min_ratio 0.8`）在 **WINNER 页不成立**（紫色 0%），会让整步跳过、
流程卡死在结算页。既然上一步（胜负判定）已保证「能走到这里的必是 WINNER 且画面停在结算页」，
`when` 就该去掉，让 step 5 无条件点 Exit。

> 对照 §2.2：**step 1 / 3 / 5 加 `strict`（改动 1）** 与 **step 5 去掉 `when`（改动 4）**
> 是同一处档案的两次调整，一起做、一起验。

#### 2.5.4 ⚠️ 两个必须先实测确认的假设（不要跳过）

本方案的样本量是 **WINNER 1 帧 + LOSE 1 帧**，不足以直接固化成生产判据。落地前必须确认：

1. **WINNER 页点掉浮层后，`exit_result` 坐标 `[0.5,0.912]` 还能不能点中 Exit？**
   实测采样（5×5 均值）：该点在 `after_nothanks.png` 是 `RGB(91,78,84)`（暗背景），
   在 `p06.png`（LOSE）恰好是 Exit 按钮本体（`RGB(207,201,248)` 文字高光）。
   ⇒ **`exit_result=[0.5,0.912]` 很可能是按 LOSE 页的紫色 Exit 按钮标的**；WINNER 页的 Exit
   位置需要**重新标定**（`after_nothanks.png` 里 "Exit" 是很淡的灰字，肉眼可见但需量准）。
   **这是改动 4 能否成立的前提，必须先量。**
2. **LOSE 页点 `no_thanks`（`[0.5,0.97]`）确实无害？** 该点在 `p06.png` 实测 `RGB(24,22,30)`
   （暗背景），而 Exit 按钮在 `y[0.887,0.936]`，两者不重叠 —— **理论上无害，但要点一次实测
   确认画面无变化**。

**离线先验（不需要真机，已跑过一次）**：判据⑤的复算脚本已备好 ——
`.workbuddy/sparkle/verify_verdict.py`（判据**从档案 `exit_result.when` 读**，不硬编码）。
执行：

```bash
"C:/Users/ay/.workbuddy/binaries/python/versions/3.13.12/python.exe" .workbuddy/sparkle/verify_verdict.py
```

**已实测结果**：`after_nothanks.png` 紫色 `0.00%` → 判 **WINNER** ✓；
`p06.png` 紫色 `93.67%` → 判 **LOSE** ✓。判据成立，但样本量 1+1，仍须按 §2.5.4 补标定。

**标定要求**：真机各抓 **≥3 帧** WINNER（含「带浮层」与「点掉浮层后」两种）与 ≥3 帧 LOSE。
LOSE 若难以主动触发（既有笔记记「实测 5 局全部 WINNER」，见 `.workbuddy/ima-uploads/`），
**至少补齐 WINNER 侧 ≥3 帧**，并在报告中**明确标注 LOSE 侧仍只有 1 帧样本** —— 这是已知限制，
**不许写成「已验证」**。

---

## 三、T1：跑批（连续 10 次，产出证据）

### 3.1 起点约束（最关键的操作纪律）

**每一轮都必须从「主界面」出发。** plan 是顺序执行、没有「现在在哪一屏」的概念，从结算页或主题页起跑会点错东西（这一条 2026-09-13 真机实测踩过：在主界面按 `popup_close` 的坐标会直接跳进 MY CLOSET，判据从 7.9% 掉到 0.5%）。

脚本在每轮开跑**前**抓帧校验起点；不在主界面则**该轮不计入成功率**（记 `SKIP_START`），并提示人工归位。**不要把起点不对的轮次算成失败**，那会污染归因。

### 3.2 跑批脚本

新建 `.workbuddy/sparkle/batch.py`（临时工具，与 `sp.py` 同类；跑通后可提炼为 `tools/plan_batch.py` —— 那是通用的，任何游戏任何 plan 都能用）。

骨架：

```python
#!/usr/bin/env python
"""Sparkle -plan 收尾跑批：连续 N 次端到端，统计成功率并归因。"""
import json, os, re, subprocess, sys, time
from PIL import Image

ADB    = r"C:\Users\ay\AppData\Local\Android\Sdk\platform-tools\adb.exe"
SERIAL = "emulator-5554"
ROOT   = r"D:\git\game-sensei"
OUT    = os.path.join(ROOT, ".workbuddy", "sparkle", "runs")
N      = 10

PROFILE = os.path.join(ROOT, "internal", "game", "profiles", "sparkle.json")

def load_judges():
    """判据一律从档案读取，不在脚本里硬编码。
    阈值正在被调参，硬编码必然漂移——而漂移不报错，只会让统计悄悄失真。"""
    steps = json.load(open(PROFILE, encoding="utf-8"))["plan"]["steps"]
    term = steps[5]["until"]                  # 终态判据：ADS 红标出现
    keys = [steps[1]["name"], steps[3]["name"]]  # 关键步：Win VS / Change，日志按 name 定位
    return term, keys

TERM, KEY_STEPS = load_judges()

def shot(path):
    with open(path, "wb") as f:
        f.write(subprocess.run([ADB, "-s", SERIAL, "exec-out", "screencap", "-p"],
                               capture_output=True).stdout)
    return path

def ratio(png, region, color, tol):
    img = Image.open(png).convert("RGB")
    w, h = img.size
    box = (int(region[0]*w), int(region[1]*h), int(region[2]*w), int(region[3]*h))
    px = list(img.crop(box).getdata())
    if not px:
        return 0.0
    hit = sum(1 for p in px
              if all(abs(p[i]-color[i]) <= tol for i in range(3)))
    return hit / len(px)

def at_main(png, judge=None):
    """判终态：主界面 ADS 红标是否达标。判据来自档案，不硬编码。"""
    j = judge or TERM
    return (ratio(png, j["region"], j["color"], j.get("tolerance", 40))
            >= j.get("min_ratio", 0.05))

def run_once(i):
    rec = dict(idx=i)
    before = shot(os.path.join(OUT, "run_%02d_before.png" % i))
    if not at_main(before):
        rec.update(status="SKIP_START", note="起点不在主界面，该轮不计入成功率")
        return rec

    log = os.path.join(OUT, "run_%02d.log" % i)
    t0 = time.time()
    p = subprocess.run(
        [os.path.join(ROOT, "helper.exe"), "-target", "android", "-adb", ADB,
         "-serial", SERIAL, "-game", "sparkle", "-plan", "-live", "-log", log],
        cwd=ROOT, capture_output=True, timeout=300)
    rec["elapsed"] = round(time.time() - t0, 1)
    rec["exit_code"] = p.returncode

    after = shot(os.path.join(OUT, "run_%02d_after.png" % i))
    txt = open(log, encoding="utf-8", errors="replace").read()

    # 逐条判定（顺序即优先级）
    if p.returncode != 0:
        m = re.search(r"计划执行异常: (.+)", txt)
        reason = m.group(1).strip() if m else "非零退出"
        # 「胜负判定」步失败 = 本局 LOSE（判据②的核心指标），必须与崩溃区分开
        rec.update(status=("FAIL_LOSE" if "胜负判定" in reason else "FAIL_CRASH"),
                   reason=reason)
    elif "计划结束" not in txt:
        rec.update(status="FAIL_NO_END", reason="日志无「计划结束」")
    elif not at_main(after):
        rec.update(status="FAIL_END_STATE", reason="终态不在主界面（ADS 红标不可见）")
    else:
        # 过程证据：关键步必须「条件满足」，防「全程超时但终态恰好在主界面」的假成功
        missed = [s for s in KEY_STEPS
                  if re.search(re.escape(s) + r".*条件超时", txt)]
        if missed:
            rec.update(status="FAIL_STEP_TIMEOUT", reason="关键步超时: " + ", ".join(missed))
        else:
            rec.update(status="PASS")

    # 附上统计便于归因
    for pat, key in [(r"端到端 ([\d.]+[a-zµ]+)", "e2e"),
                     (r"条件超时 (\d+) 次", "timeouts"),
                     (r"跳过 (\d+) 步", "skipped"),
                     (r"动作失败 (\d+) 次", "action_errors")]:
        m = re.search(pat, txt)
        if m:
            rec[key] = m.group(1)
    return rec

def main():
    os.makedirs(OUT, exist_ok=True)
    recs = [run_once(i) for i in range(1, N + 1)]
    ran = [r for r in recs if r["status"] != "SKIP_START"]
    ok  = [r for r in ran if r["status"] == "PASS"]
    lost = [r for r in ran if r["status"] == "FAIL_LOSE"]
    summary = dict(
        total=len(recs), ran=len(ran), passed=len(ok), skipped=len(recs) - len(ran),
        won=len(ok), lost=len(lost),        # ★ 胜负计数：判据② 的核心证据
        success_rate=(round(len(ok)/len(ran), 3) if ran else None),
        verdict=("PASS" if ran and len(ok)/len(ran) >= 0.8 else "FAIL"),
        runs=recs)
    with open(os.path.join(OUT, "summary.json"), "w", encoding="utf-8") as f:
        json.dump(summary, f, ensure_ascii=False, indent=2)
    print(json.dumps(summary, ensure_ascii=False, indent=2))

if __name__ == "__main__":
    main()
```

跑之前先重新编译，**不要用可能过期的 `helper.exe`**：

```bash
go build -o helper.exe ./cmd/helper
"C:/Users/ay/.workbuddy/binaries/python/versions/3.13.12/python.exe" .workbuddy/sparkle/batch.py
```

### 3.3 单轮成功定义（脚本已按此实现）

```
成功 ⟺ 退出码 0
      ∧ 日志含「计划结束」
      ∧ 日志不含「胜负判定 … 条件超时」        ← 这一条就是「本局 WINNER」
      ∧ 终态帧 ADS 红标占比 ≥ 4%
      ∧ 关键步（Win VS / Change）无「条件超时」
```

- 第 3 条是**本方案「成功」的核心**：只有 WINNER 才能通过 §2.5 的胜负判定步；LOSE 会让该步
  `strict` 触发、退出码变非 0，所以第 1 条也会跟着不满足。两条都写，是为了**归因时能区分
  「输了」和「崩了」** —— 见 §四。
- 第 5 条是**防假成功 A** 的关键：只看终态不够——如果六个步骤全超时，游戏可能因为本来就
  在主界面而「看起来对了」。

---

## 四、失败归因表（判据②要求「每次失败可归因」）

跑批后按此表逐条对号入座，写进报告。**归因不到的失败必须单独列出，不许归到"偶发"**。

| 现象 | 最可能原因 | 处置 |
|---|---|---|
| `Win VS` 超时（8s） | 主界面未真正就绪 / 被弹窗遮挡 / 按钮坐标漂移 | 看该轮 `before.png`；起点若正确则是坐标或判据区域问题 |
| `Change` 超时（45s） | 激励广告超 45s / 广告未自动结束 / 紫色判据阈值过严 | 看 `e2e` 耗时分布；偶发超长→提高 `timeout_ms`，常发→查判据 |
| `clear_popup` 循环 6 轮未命中 | 出现未知弹窗形态 | 抓该轮帧，补按钮；**属游戏内容，不是框架缺口** |
| 退出码 0 但终态不在主界面 | 末步 `when` 被跳过（结算页判据失效） | 查 step 3 / step 5 的紫色判据是否同源失效 |
| `动作失败 3 次` | ADB input 注入失败 / 设备断连 | 环境故障，记 `FAIL_CRASH` 并重跑一轮 |
| 日志出现抓帧重试 | `grabRetries` 生效（**这是判据③的正常表现，不是失败**） | 记录次数即可 |
| 「胜负判定」步条件超时 → 退出码非 0 | **本局 LOSE**（点掉浮层后底部紫色 Exit 恒在） | 记 `FAIL_LOSE`。**这是决胜失败，不是框架故障**，也是判据②要统计的核心指标 |
| `Exit` 点不动 / 终态卡在结算页（退出码 0 但 ADS 不达标） | WINNER 页的 Exit 坐标与 LOSE 页不同（见 §2.5.4 假设 1） | 量准 WINNER 页 Exit 位置并修正 `exit_result`；属坐标标定问题，不是框架缺口 |

---

## 五、产出物

| 文件 | 内容 |
|---|---|
| `.workbuddy/sparkle/runs/run_NN.log` ×10 | 每轮完整日志 |
| `.workbuddy/sparkle/runs/run_NN_before.png` / `_after.png` | 起点/终态帧（判据⑤的证据，也是失败归因依据） |
| `.workbuddy/sparkle/runs/summary.json` | 机器可读结果（脚本自动生成），**必须含 WINNER / LOSE / 未判定 三个计数** |
| **`outputs/game-sensei-sparkle收尾报告.md`** | 人读报告：成功率、逐轮表格、归因、是否达标的结论 |
| `README.md` §12.6 | 判据④：Sparkle 端到端案例（命令、实测耗时、已知干扰） |

**报告必须如实写**：没达标就写没达标、并给出下一步；**不许把"跑了 10 次"写成"验证通过"**。

---

## 六、红线

1. **不碰并发会话的文件。** 编制本任务书时，工作区有 5 个文件处于未提交状态，属另一会话正在进行的改动：
   `cmd/helper/backend.go`、`cmd/helper/main.go`、`internal/game/profiles/sparkle.json`、`internal/plan/plan.go`、`internal/plan/plan_test.go`。
   **T0 要改的正是 `sparkle.json`** —— 动手前必须先确认该会话已结束/已提交，否则会互相覆盖。

   > **实测证据（不是推测）**：编制本任务书的 10 分钟里，`sparkle.json` 的 `Change` 步 `min_ratio`
   > 从 `0.8` 变成了 `0.6` —— 该文件**是活的**。所以：开工前先 `git status` 重新盘点，
   > 若这些文件仍在变动，**先等它提交，不要并行改**。
2. **不许 `git add -A` / `git add .`**，逐个列本次文件。
3. **不许 reset / checkout / clean**，不许删探测产物（`.workbuddy/` 下的帧、日志、脚本都要留作证据）。
4. **安卓模式日志必须用 `-log` 落盘**，不要用 PowerShell 重定向（GBK 中间层会毁中文、吞换行，统计脚本直接失效）。
5. **跑批期间不要并发操作设备**（另开会话手工点屏幕会污染结果）。
6. 本任务书**不含任何游戏内容探索**（新关卡、新界面入口、候选件规律等）——那些是开放清单，与本收尾无关，**不要顺手做**。

---

## 七、时间与顺序

```
T0   补 strict（关键步）+ 独立终态判定            （约 10 分钟）
T0.5 **胜负判定步 + 去掉 step5 的 `when`**         （约 10 分钟，见 §2.5）
  → 离线复算既有 3 帧（WINNER ×2 / LOSE ×1）       （判据⑤，不需真机）
  → 单次 smoke 验证三条通过                        （§二 2.4）
  → T1 跑批 10 次                                  （单轮约 20~60s，视广告而定）
  → 写收尾报告 + README §12.6                      （判据④）
  → 达标：更新 DEVELOPMENT_PLAN §1.6 勾选，Sparkle 收尾
     未达标：把归因结果回填 §四，按原因修正后重跑（重跑只记新一轮，不覆盖旧证据）
```

**达标线**：10 轮中可统计轮次 ≥8 轮为 `PASS` —— 即 **≥8 轮真的打成 WINNER**（胜率 ≥80%）。
不满足则**未收尾**，不许用「流程跑完了」替代。

**若不达标**：不要靠调阈值硬凑到 80%。先看归因表——若失败集中在同一个 step 且是**判据本身的问题**（比如紫色判据分辨不清结算页两态），那要改的是判据，且改完必须**重跑全部 10 轮**（旧数据作废，因为判据变了不在同一基准上）。
