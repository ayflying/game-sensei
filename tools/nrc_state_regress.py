#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""界面判据离线回归（detect_state 判据顺序的回归集）。

为什么需要它
------------
坑⑤（家园浮层盖在地图上）、坑⑥（地图正在关闭的过渡帧）、坑⑧（区域面板上两个锚点
图标仍在屏）**全都是"判据顺序"问题**：OCR 见到地图词、或锚点齐，都不代表"就在干净
地图上"。这三处每次都是**真机造异常起点**才暴露的，成本高、难复现。

⇒ 把各类界面各存几张帧当**回归集**，改 `detect_state` 前后各跑一遍，
   比每次真机造起点便宜得多（一次全跑约 3.6s × 帧数，纯离线）。

两条判据口径
------------
- A 老口径：`detect_state(frame)` 不传 hits —— 靠 OCR 语义。会把叠加层/过渡帧判成 map。
- B 新口径：`detect_state(frame, hits=...)` 传锚点命中 —— 锚点只当**必要条件**
  （锚点齐才 map，不齐退 unknown），且排在叠加层判据**之后**。

回归通过 = **B 与"期望态"一致**；A 与 B 的差异一并打印，供人判断是不是又漏了一种叠加层。

用法
----
    python tools/nrc_state_regress.py                    # 跑内置用例集
    python tools/nrc_state_regress.py --diagnose 帧.png  # 只打印判据分解
    python tools/nrc_state_regress.py --list             # 列用例集
"""
import argparse
import importlib.util
import os
import sys

HERE = os.path.dirname(os.path.abspath(__file__))
ROOT = os.path.dirname(HERE)
# 帧集放**证据目录**，不放 tmp/screenshots（tmp 随时可能被清理 ⇒ 回归集就废了）。
# 属本地资产、未入 git（.workbuddy/ 整目录被忽略）⇒ 换机器会缺帧并被标 MISSING。
FRAMEDIR = os.path.join(ROOT, ".workbuddy", "evidence", "nrc", "regress-frames")
# 旧位置（当年取证时抓的原始帧），仅作回退用
LEGACY_DIR = os.path.join(ROOT, ".workbuddy", "tmp", "screenshots", "nrc-20260918")
# 语义名 → 原始帧名（溯源用；归档时已从 g4_panel 这类**会骗人的名字**改成语义名）
ORIGIN = {
    "panel.png": "regress_post.png",
    "map_clean.png": "regress_pre.png",
    "home_panel.png": "g4_panel.png",
    "world.png": "dw_world.png",
    "marker_edit.png": "s_now5_panel.png",
}


def _load_soak():
    path = os.path.join(HERE, "nrc_soak.py")
    spec = importlib.util.spec_from_file_location("nrc_soak", path)
    mod = importlib.util.module_from_spec(spec)
    spec.loader.exec_module(mod)
    return mod


def resolve(name):
    """帧名 → 真实路径：先归档目录，再绝对路径/相对路径，最后按原始名回退到旧 tmp 目录。"""
    p = os.path.join(FRAMEDIR, name)
    if os.path.exists(p):
        return p
    if os.path.isabs(name) or os.path.exists(name):
        return name
    legacy = ORIGIN.get(name)
    if legacy:
        lp = os.path.join(LEGACY_DIR, legacy)
        if os.path.exists(lp):
            return lp
    return p


# ── 内置回归用例集 ──────────────────────────────────────────────────────────
# expect 是"人看帧得出的真实态"。帧名沿用当年取证时的命名。
# 说明：这些帧都在 .workbuddy/tmp/screenshots/nrc-20260918/（本地资产，不入 git）；
#       缺帧会被标 MISSING 而不是直接报错 —— 换了机器也能看出"回归集不完整"。
CASES = [
    # ⚠️ 帧名要用**语义名**：原始帧名 g4_panel、s_now5_panel 都带 panel 却是家园 / 标记编辑态
    #    ⇒ 按名字猜态必错（第一版期望值就是这么错的）。溯源见 ORIGIN 表。
    # 区域进度面板：地图固有词与两个锚点图标**都在屏**（坑⑧ 的现场）
    ("panel", "panel.png", "panel"),
    # 干净地图：锚点齐 + 地图固有词（OCR 有「11/14」收集进度，但不是 panel 的「15/15」）
    ("map", "map_clean.png", "map"),
    # 家园界面：锚点被浮层盖住 ⇒ 不齐（坑⑤ 的现场）
    ("home_panel", "home_panel.png", "home_panel"),
    # 大世界：地图已关，「触碰」在屏（坑⑥ 的现场之一）
    ("world", "world.png", "world"),
    # 标记编辑态：OCR 把「点击修改名称」认成「点击修破名称」⇒ 长词判据漏判（坑⑨ 的现场）
    ("marker_edit", "marker_edit.png", "marker_edit"),
]


def diag(mod, frame_path):
    """返回 (A 老口径判态, B 新口径判态, hits 数, 锚点齐否, 命中名单)"""
    hd, score = mod.locate(frame_path, {}, record=False)
    keys = set(hd.keys()) if isinstance(hd, dict) else set()
    a = mod.detect_state(frame_path)
    b = mod.detect_state(frame_path, hits=keys)
    anchors_ok = mod.MAP_ANCHORS.issubset(keys)
    return a, b, len(keys), anchors_ok, sorted(keys), round(score, 3)


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--diagnose", nargs="*", default=None,
                    help="只诊断这些帧（路径或帧名），不判对错")
    ap.add_argument("--list", action="store_true", help="列内置用例集")
    args = ap.parse_args()

    if args.list:
        for name, f, exp in CASES:
            p = resolve(f)
            print("%-10s %-24s expect=%-10s %s" % (name, f, exp,
                  "OK" if os.path.exists(p) else "MISSING"))
        return 0

    mod = _load_soak()

    if args.diagnose is not None:
        targets = args.diagnose
        if not targets:
            print("--diagnose 后面要给帧名/路径")
            return 2
        for t in targets:
            p = resolve(t)
            if not os.path.exists(p):
                print("MISSING  %s" % t)
                continue
            a, b, n, ok, keys, score = diag(mod, p)
            print("=" * 72)
            print("%s  (score=%s)" % (os.path.basename(p), score))
            print("  A 老口径(OCR) = %-12s   B 新口径(锚点) = %s" % (a, b))
            print("  命中 %d 条 / 锚点齐=%s" % (n, ok))
            print("  命中名单: %s" % (", ".join(keys) if keys else "（无）"))
        return 0

    # ── 跑回归 ──────────────────────────────────────────────────────────────
    n_pass = n_fail = n_missing = 0
    rows = []
    for name, f, exp in CASES:
        p = resolve(f)
        if not os.path.exists(p):
            n_missing += 1
            rows.append((name, f, exp, "MISSING", "-", "-", "-"))
            continue
        a, b, n, ok, keys, score = diag(mod, p)
        good = (b == exp)
        n_pass += good
        n_fail += (not good)
        rows.append((name, f, exp, b, a, n, "锚点齐" if ok else "锚点不齐"))

    print("=" * 96)
    print("%-8s %-22s %-10s %-10s %-10s %-6s %s" %
          ("场景", "帧", "期望", "B(新)", "A(旧)", "命中", "锚点"))
    print("-" * 96)
    for name, f, exp, b, a, n, anc in rows:
        flag = "✓" if b == exp else ("MISSING" if b == "MISSING" else "✗")
        extra = "" if (a == b or a == "-") else "   ← A/B 不一致"
        print("%-8s %-22s %-10s %-10s %-10s %-6s %-8s %s%s" %
              (name, f, exp, b, a, n, anc, flag, extra))
    print("-" * 96)
    print("通过 %d / 失败 %d / 缺帧 %d" % (n_pass, n_fail, n_missing))
    if n_missing:
        print("⚠️ 缺帧说明回归集不完整：帧应在 %s（本地资产、未入 git）。" % FRAMEDIR)
    return 0 if n_fail == 0 else 1


if __name__ == "__main__":
    sys.exit(main())
