# Sparkle 收尾任务书（交给执行会话照做）

> 目标一句话：**把 Sparkle 跑到「达成 `-plan` 线四条收尾判据」，正式关闭这款游戏。**
> 本文只定义目标与验收，不含探索性工作。判据来源：`DEVELOPMENT_PLAN.md` §1.6。
> 编制日期：2026-09-14。编制时**未改任何代码/档案**（见 §六 红线）。

---

## 零、完成定义（DoD）

**全部勾完 = Sparkle 收尾，可以转向下一款游戏。**

| # | 判据 | 现状 | 验收命令 / 证据 |
|---|---|---|---|
| ① | 端到端真机跑通 ≥1 次，**含起点/终点状态校验**（非 `strict=false` 静默跳过） | ✗ 口径存疑 | §二 T0 落地后，单次跑退出码 0 **且** 终态帧 ADS 红标可见 |
| ② | 连续 **≥10 次**成功率 **≥80%**，每次失败可归因到具体 step 与判据 | ✗ 无数据 | §三 T1 跑批产出的 `summary.json` + 报告 |
| ③ | 抓帧失败**必须重试**，不得当成「判据不满足」 | ✅ 机制已有 | `plan.go` 的 `grabRetries=3`（已实现）；跑批中若出现抓帧失败需在日志可见 |
| ④ | README 补 Sparkle 章节 | ✗ 无 | 新增 `README.md` §12.6「案例：Sparkle 端到端」 |

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

**缺口只有 2 个**，都在「怎么证明它成了」这一侧：

1. **终态校验缺失** —— 详见 §二，这是根因。
2. **批量证据缺失** —— 10 次成功率与归因，详见 §三。

也就是说：**这一轮不需要改执行逻辑，只需要补「判定」和「跑批」。**

---

## 二、T0：补终态校验（唯一前置改动）

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
| **3** | 点 Change 提交造型 | `ratio`：结算页紫色按钮**出现**，`timeout_ms 45000` | `"strict": true` | 全流程最长等待，也是「这一局真的打完了」的唯一判据 |
| **5** | 点 Exit 回主界面 | `ratio`：ADS 红标**出现**，`timeout_ms 15000` | `"strict": true` | 有 `when` 守卫，加 `strict` 后**守卫满足却不达成**才算失败 |

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
        rec.update(status="FAIL_CRASH", reason=(m.group(1).strip() if m else "非零退出"))
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
    summary = dict(
        total=len(recs), ran=len(ran), passed=len(ok), skipped=len(recs) - len(ran),
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
      ∧ 终态帧 ADS 红标占比 ≥ 4%
      ∧ 关键步（Win VS / Change）无「条件超时」
```

第四条是**防假成功 A** 的关键：只看终态不够——如果六个步骤全超时，游戏可能因为本来就在主界面而「看起来对了」。

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

---

## 五、产出物

| 文件 | 内容 |
|---|---|
| `.workbuddy/sparkle/runs/run_NN.log` ×10 | 每轮完整日志 |
| `.workbuddy/sparkle/runs/run_NN_before.png` / `_after.png` | 起点/终态帧（判据⑤的证据，也是失败归因依据） |
| `.workbuddy/sparkle/runs/summary.json` | 机器可读结果（脚本自动生成） |
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
T0 补 strict + 独立终态判定        （约 10 分钟）
  → 单次 smoke 验证三条通过        （§二 2.4）
  → T1 跑批 10 次                  （单轮约 20~60s，视广告而定；总计 5~12 分钟）
  → 写收尾报告 + README §12.6      （判据④）
  → 达标：更新 DEVELOPMENT_PLAN §1.6 勾选，Sparkle 收尾
     未达标：把归因结果回填 §四，按原因修正后重跑（重跑只记新一轮，不覆盖旧证据）
```

**达标线**：10 轮中可统计轮次 ≥8 轮为 `PASS`（成功率 ≥80%）。

**若不达标**：不要靠调阈值硬凑到 80%。先看归因表——若失败集中在同一个 step 且是**判据本身的问题**（比如紫色判据分辨不清结算页两态），那要改的是判据，且改完必须**重跑全部 10 轮**（旧数据作废，因为判据变了不在同一基准上）。
