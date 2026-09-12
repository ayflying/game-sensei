"""统计一轮采集日志：动作分布、兜底轮换、宏调用、Δ 分档、策略质量（横跳）。

用法: python tools/runstats.py <日志文件> [--json]

⚠️ 编码坑（踩过）：helper 写 stdout 是 UTF-8，Windows PowerShell 5.1 默认用本地
代码页（GBK）解码它再落盘，于是日志里**部分中文字符会被毁成不可逆的替换字符**。
所以本工具**只用 ASCII 锚点**解析（动作名、按钮名、宏名、Δ 值都是 ASCII），
不依赖任何中文关键词。

更省事的做法（下次采集直接用）：先 `[Console]::OutputEncoding=[Text.Encoding]::UTF8`
再重定向，或干脆用 `cmd /c "... > run.log 2>&1"` —— 字节直达，不经过 GBK 中间层。
"""
import json
import re
import sys

# 动作行：[ N/M] ... move:up_right/2000ms → joy:...   （取段内**第一个**动作）
STEP_RE = re.compile(r"\[(\d+)/(\d+)\]")
ACT_RE = re.compile(r"\b(move|press|tap|swipe|hold|key|wait|joystick):([^\s|]+)")
DIFF_RE = re.compile(r"Δ([\d.]+)")
MACRO_RE = re.compile(r"\b(cast_\w+|cancel_\w+)\s+(\d+)/(\d+)")
# 兜底行没有 Δ（正常动作行才有 `| Δ.. | ..s ..tok`），据此区分
KINDS = ("move", "press", "tap", "swipe", "hold", "key", "wait", "joystick")
# battle 态专属的 press 名前缀（用于在不依赖中文的前提下判定界面态）
BATTLE_PREFIX = ("battle_", "cast_", "cancel_", "sk_")


def segments(lines):
    """把物理行按 `[N/M]` 标记切成 (step, total, 段文本)。

    为什么要切段而不是「一行一步」：PowerShell 的 GBK 中间层**会吞换行**，
    把相邻两步挤进同一物理行（实测 pet_run9：40 步里只剩 27 个行首标记）。
    按标记切段能把被吞掉的步全部捞回来，统计口径才和真实步数一致。
    没有标记的行（表头、续行）step 返回 0。
    """
    for ln in lines:
        marks = list(STEP_RE.finditer(ln))
        if not marks:
            yield 0, 0, ln
            continue
        for i, m in enumerate(marks):
            end = marks[i + 1].start() if i + 1 < len(marks) else len(ln)
            yield int(m.group(1)), int(m.group(2)), ln[m.start():end]


def load_lines(path):
    raw = open(path, "rb").read()
    for enc in ("utf-16", "utf-16-le"):
        try:
            s = raw.decode(enc)
        except Exception:
            continue
        # 还原：把「按 GBK 解出来的字符」编回 GBK 字节，再按 UTF-8 解码。
        # 必须用容错模式——只要有一个中文字符在 GBK 下不可逆，strict 就会整体失败，
        # 于是回退到未还原的乱码，连 ASCII 锚点都匹配不上（踩过）。
        try:
            return s.encode("gbk", errors="replace").decode("utf-8", errors="replace").splitlines()
        except Exception:
            return s.splitlines()
    try:
        return raw.decode("utf-8").splitlines()
    except Exception:
        return raw.decode("gbk", errors="replace").splitlines()


# 横跳检测窗口，与 internal/teacher.DetectOscillation 的 oscWindow 保持一致。
OSC_WINDOW = 6


def osc_hits(seq, window=OSC_WINDOW):
    """统计有多少个滑动窗口命中「横跳」。

    判据与 internal/teacher.DetectOscillation **逐条对齐**（窗口内不同动作 ≤2 种
    且换向 ≥3 次）——两处必须同源，否则会出现「日志说没打转、提示词却在告警」
    这种自相矛盾，排查时会把人带偏。
    """
    hits = 0
    for i in range(len(seq) - window + 1):
        w = seq[i:i + window]
        if len(set(w)) != 2:
            continue
        if sum(1 for a, b in zip(w, w[1:]) if a != b) >= 3:
            hits += 1
    return hits


def longest_run(seq):
    """最长「同一动作连走」长度，返回 (动作, 长度)。"""
    best, best_n = "", 0
    cur, n = "", 0
    for s in seq:
        if s == cur:
            n += 1
        else:
            cur, n = s, 1
        if n > best_n:
            best, best_n = cur, n
    return best, best_n


def switches(seq):
    """相邻两步动作不同的次数（换向次数）。"""
    return sum(1 for a, b in zip(seq, seq[1:]) if a != b)


def main():
    path = sys.argv[1]
    as_json = "--json" in sys.argv[2:]
    lines = load_lines(path)

    kinds, moves, presses, macros = {}, {}, {}, {}
    fb_by_step, fb_steps = {}, set()   # step -> press 名（去重：决策行与执行行同 step）
    battle_acts, world_acts = [], []
    diffs, steps, total = [], set(), None
    teacher_seq = []                   # [(step, "kind:name")]，只含老师驱动的动作

    for step, tot, seg in segments(lines):
        if step:
            steps.add(step)
            total = total or tot
        has_diff = bool(DIFF_RE.search(seg))
        if has_diff:
            act = ACT_RE.search(seg)
            if act and act.group(1) in KINDS:
                kind, name = act.group(1), act.group(2)
                kinds[kind] = kinds.get(kind, 0) + 1
                teacher_seq.append((step, f"{kind}:{name}"))
                if kind == "move":
                    moves[name] = moves.get(name, 0) + 1
                elif kind == "press":
                    presses[name] = presses.get(name, 0) + 1
                (battle_acts if name.startswith(BATTLE_PREFIX) else world_acts).append(step)
        else:
            # 兜底行（无 Δ）：真正的执行动作是段内的 press 名（重复动作文本也含动作，
            # 所以不能直接取第一个匹配）。同一 step 的决策行与执行行按 step 去重。
            pm = re.search(r"\bpress:([^\s|,，]+)", seg)
            if pm and step:
                fb_by_step[step] = pm.group(1)
                if step not in fb_steps:
                    fb_steps.add(step)
                    (battle_acts if pm.group(1).startswith(BATTLE_PREFIX) else world_acts).append(step)
        for name, _i, _n in MACRO_RE.findall(seg):
            macros[name] = macros.get(name, 0) + 1
        for d in DIFF_RE.findall(seg):
            diffs.append(float(d))

    fallbacks = {}
    for name in fb_by_step.values():
        fallbacks[name] = fallbacks.get(name, 0) + 1

    def table(d):
        if not d:
            return "  （无）"
        return "\n".join(f"  {k:<18} {v}" for k, v in sorted(d.items(), key=lambda kv: -kv[1]))

    out = []
    out.append(f"日志: {path}")
    out.append(f"步数: {len(steps)}" + (f" / {total}" if total else ""))
    out.append("动作种类: " + (", ".join(f"{k}={v}" for k, v in sorted(kinds.items(), key=lambda kv: -kv[1])) or "（无）"))
    out.append(f"态线索: 战斗侧动作步数={len(battle_acts)} 大世界侧动作步数={len(world_acts)}")
    out.append("MOVE 方向:")
    out.append(table(moves))
    out.append("PRESS 按钮:")
    out.append(table(presses))
    out.append("兜底强制轮换（按 step 去重）:")
    out.append(table(fallbacks))
    out.append("宏调用（按展开步计数）:")
    out.append(table(macros))
    if diffs:
        nz = [d for d in diffs if d > 0]
        out.append(
            f"Δ 统计: n={len(diffs)} max={max(diffs):.1f} avg={sum(diffs)/len(diffs):.2f} "
            f"非零均值={(sum(nz)/len(nz) if nz else 0):.2f} 零值={sum(1 for d in diffs if d == 0)}"
        )
    # 策略质量：老师是「在一个方向上持续走出去」还是「原地横跳」。
    # 这是 2026-09-13 那轮实况暴露的核心问题，单独列出来便于改动前后对比。
    seq = [a for _s, a in sorted(teacher_seq)]
    if seq:
        action, run = longest_run(seq)
        out.append(
            f"策略: 老师动作数={len(seq)} 换向次数={switches(seq)} "
            f"横跳窗口={osc_hits(seq)}/{max(0, len(seq) - OSC_WINDOW + 1)} "
            f"最长连走={run}（{action}）"
        )
        out.append("老师动作序列: " + " → ".join(seq))
    text = "\n".join(out)
    print(text if not as_json else json.dumps({"text": text}, ensure_ascii=False, indent=1))


if __name__ == "__main__":
    main()
