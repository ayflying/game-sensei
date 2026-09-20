#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""图标档案接入器：把「批量学习产出的关键帧」一次性转成「待确认清单 + 可入库模板」。

## 为什么需要它

`video-bulk-learn` 的产出是「**文字** → 坐标」图谱（OCR 驱动），没有「图标长什么样」这一层。
后果：真机上遇到**不带 OCR 文字**的图标（庇护所、宝箱、任务点、事件标记）只能盲试坐标。
2026-09-20 找「眠枭庇护所」卡住的根因就在这里。

所以「建图标档案」必须成为**批量学习的固定收尾步骤**（本工具 = 第⑨步），
否则每批学完都留下同一块缺口，下次再遇到又要从零摸。

## 流水线

  ① 逐帧调 `tools/icon_extract.py` 取候选（复用其 CLI，不复制提取逻辑）
  ② **跨帧聚合**：判断"不同帧里的两个候选是不是同一个图标"。
     不能用绝对坐标——地图视口会平移，同一图标在不同帧坐标完全不同；
     改用**内容签名**（灰度缩略图零均值归一化后的相关系数）+ 宽高比/尺寸一致性。
  ③ **标注已入库**：用图标库现有模板回搜每帧（≥0.85），命中的候选标 `known`，
     人工确认时自动跳过 —— 这是"只问一次"的关键。
  ④ 产出三件套（都在 `--out` 目录）：
       `candidates.json`  聚合候选（频次降序，含代表帧 + bbox + 成员列表）
       `grid.png`         聚合候选网格总览图（人工一眼确认）
       `checklist.md`     待确认清单（名称列留 `?` 待回填）
  ⑤ `--apply <checklist.md>`：回填后批量建模板，并自动跑一次回归确认没搞坏旧模板。

## 用法

  # ① 出清单（看 grid.png 认图标，在 checklist.md 里把 ? 改成中文名）
  python tools/icon_intake.py --frames-dir .workbuddy/tmp/screenshots/nrc-20260918 \\
      --pick k0,g2,q2,tapD,v11 --out .workbuddy/nrc/icon_atlas/intake/20260920
  # ② 回填后入库
  python tools/icon_intake.py --apply .workbuddy/nrc/icon_atlas/intake/20260920/checklist.md

输出纯文字（不读图），供主对话直接消费。
"""
import argparse
import json
import os
import re
import subprocess
import sys

import cv2
import numpy as np
from PIL import Image

TOOLS = os.path.dirname(os.path.abspath(__file__))
ROOT = os.path.dirname(TOOLS)
sys.path.insert(0, TOOLS)

import icon_extract            # noqa: E402  复用 make_grid（网格总览图）
import icon_match              # noqa: E402  复用 match_one / imread / load_lib / LIB_DIR

PY = sys.executable
EXTRACT_PY = os.path.join(TOOLS, "icon_extract.py")
MATCH_PY = os.path.join(TOOLS, "icon_match.py")
REGRESS_PY = os.path.join(TOOLS, "icon_regression.py")

SIG = 16          # 内容签名缩略图边长
KNOWN_MIN = 0.85  # 判定"已入库"的匹配分下限（与 icon_match 默认一致）


# ---------------------------------------------------------------- ① 逐帧提取

def run_extract(png, out_dir, extra):
    """调 icon_extract.py 取候选（复用 CLI，避免复制一份提取逻辑出来漂移）。"""
    cmd = [PY, EXTRACT_PY, png, "--out", out_dir] + list(extra)
    r = subprocess.run(cmd, capture_output=True, text=True,
                       encoding="utf-8", errors="replace")
    if r.returncode != 0:
        return None
    p = os.path.join(out_dir, "icons.json")
    if not os.path.exists(p):
        return None
    with open(p, encoding="utf-8") as f:
        return json.load(f)


def signature(gray, c, pad=2):
    """候选的内容签名：裁 bbox（带 pad）→ 灰度缩略图 → 零均值归一化向量。

    用内容而不是坐标，才能跨视口把同一图标认成同一个（地图平移时坐标全变）。
    """
    h, w = gray.shape
    x0, y0 = max(0, c["x0"] - pad), max(0, c["y0"] - pad)
    x1, y1 = min(w, c["x1"] + pad + 1), min(h, c["y1"] + pad + 1)
    if x1 - x0 < 3 or y1 - y0 < 3:
        return None
    sub = gray[y0:y1, x0:x1]
    v = cv2.resize(sub, (SIG, SIG), interpolation=cv2.INTER_AREA).astype(np.float32).ravel()
    v -= float(v.mean())
    n = float(np.linalg.norm(v))
    return v / n if n > 1e-6 else None


def collect(frames_dir, picks, out_dir, extra, min_side=18, min_area=150, verbose=True):
    """逐帧提取 → 带签名的候选池。

    `min_side`/`min_area` 是**本层的质量门槛**，与 `icon_extract` 的过滤无关：
    实测 10x10~16x12 这类极小候选的内容签名**没有区分度**（缩到 16x16 后任意小块
    相关系数都 >0.9），会把不同视口里毫不相干的小块误聚成一组，跑出
    「频次 5、spread 1463」的假高频。真实可点图标不会只有十几像素。
    """
    pool = []
    per_frame = {}
    dropped = 0
    for name in picks:
        png = os.path.join(frames_dir, name + ".png")
        if not os.path.exists(png):
            if verbose:
                print(f"  跳过（缺帧）：{name}")
            continue
        meta = run_extract(png, os.path.join(out_dir, "frames", name), extra)
        if meta is None:
            if verbose:
                print(f"  提取失败：{name}")
            continue
        gray = cv2.cvtColor(icon_match.imread(png), cv2.COLOR_BGR2GRAY)
        icons = []
        for c in meta["icons"]:
            if max(c["w"], c["h"]) < min_side or c["area"] < min_area:
                dropped += 1
                continue
            sg = signature(gray, c)
            if sg is None:
                continue
            c = dict(c)
            c["sig"] = sg
            c["frame"] = name
            c["ar"] = c["w"] / float(c["h"])
            icons.append(c)
        per_frame[name] = icons
        pool.extend(icons)
        if verbose:
            print(f"  {name}: 候选 {len(icons)}（过滤掉过小 {len(meta['icons']) - len(icons)}）")
    if verbose and dropped:
        print(f"  尺寸门槛：最长边 ≥{min_side}px 且面积 ≥{min_area}px²，共滤掉 {dropped} 个过小候选")
    return pool, per_frame


# ---------------------------------------------------------------- ② 跨帧聚合

def cluster(pool, n_frames, thr=0.90, ar_tol=0.35, sz_tol=0.45):
    """贪心聚类：签名相关系数 ≥ thr 且宽高比/尺寸接近 ⇒ 判为同一图标。

    按面积降序处理，让尺寸大、纹理丰富的那一个当代表（签名更稳）。

    聚类后顺带做**类型自动分类**（这是本工具最有价值的一条判据）：
      - `marker` 跨帧出现但**坐标漂移** ⇒ 随视口移动的地图图标，**必须靠模板定位**
      - `ui`     跨多帧出现且**坐标几乎不动** ⇒ 屏幕常驻 HUD，**用坐标就够，不必建模板**
      - `repeat` **同一帧内就出现多次** ⇒ 重复地形图案（树/石头/草地），不是图标
      - `single` 只在单帧出现 ⇒ 临时元素/噪声，优先级最低

    实测（5 帧 3 个视口）：
      - 眠枭庇护所 k0(861,299) 与 v11(1035,498) 位置不同 ⇒ marker
      - 而首轮跑出「50 组全是 marker、频次 5」的假象，根因是**同形地形块跨视口误聚**
        （同一棵树在几个视口都出现、内容签名相同）⇒ 靠 `repeat` 判据剥掉：
        真图标在地图上每处唯一（每帧 1 个），重复图案一帧内就有几十个。
    """
    groups = []
    for c in sorted(pool, key=lambda c: -c["area"]):
        placed = False
        for g in groups:
            r = g["rep"]
            if abs(c["ar"] - r["ar"]) / max(c["ar"], r["ar"]) > ar_tol:
                continue
            rs = (c["area"] / max(1, r["area"])) ** 0.5
            if not (1 - sz_tol < rs < 1 + sz_tol):
                continue
            if float(np.dot(c["sig"], r["sig"])) >= thr:
                g["members"].append(c)
                placed = True
                break
        if not placed:
            groups.append({"rep": c, "members": [c]})
    for g in groups:
        # 频次按帧去重：同一帧里出现两次只算一次
        g["frames"] = sorted({m["frame"] for m in g["members"]})
        g["freq"] = len(g["frames"])
        g["per_frame"] = round(len(g["members"]) / max(1, g["freq"]), 2)
        xs = [m["cx"] for m in g["members"]]
        ys = [m["cy"] for m in g["members"]]
        g["spread"] = int(max(max(xs) - min(xs), max(ys) - min(ys))) if len(xs) > 1 else 0
        if g["per_frame"] > 1.6:
            g["kind"] = "repeat"      # 同帧内重复 ⇒ 地形图案，不是图标
        elif g["freq"] >= max(2, int(0.6 * n_frames)) and g["spread"] <= 24:
            g["kind"] = "ui"          # 跨帧且不动 ⇒ 常驻 HUD
        elif g["freq"] >= 2:
            g["kind"] = "marker"      # 跨帧但漂移 ⇒ 随视口移动的图标
        else:
            g["kind"] = "single"
    return groups


# ---------------------------------------------------------------- ③ 已入库标注

def mark_known(shot_bgr, lib, scales):
    """用图标库现有模板回搜本帧，返回 [(名称, 命中, 模板本体尺寸)]。"""
    hits = []
    for name, meta in lib.get("templates", {}).items():
        tmpl = icon_match.imread(os.path.join(icon_match.LIB_DIR, meta["file"]))
        if tmpl is None:
            continue
        csz = meta.get("core_size") or meta["size"]
        for h in icon_match.match_one(shot_bgr, tmpl, scales, KNOWN_MIN):
            hits.append((name, h, csz))
    return hits


def annotate_known(groups, frames_dir, picks, lib, scales, dist=14, verbose=True):
    """给每个聚合组标注「是不是已经入库的图标」。

    两个坑，都是实测踩出来的：

    ① **距离判据不能用中心距单卡**：模板匹配点与 `icon_extract` 的 bbox 中心是两个口径，
       大图标上能差 18px（星光对决 1688,472 vs 1706,487），卡死 12px 会把已入库图标
       误报成「新图标」而重复问一遍。⇒ 判据 = 匹配点落在候选 bbox 内（外扩 dist）。
    ② **一个命中只能归一个组**：放宽判据后，大图标（星光对决 59x61）的 bbox 会罩住
       邻近好几个小候选，一次跑出 7 组 known（真实只有 3 个图标）。
       ⇒ 每个命中按「候选尺寸与模板本体尺寸的贴合度」择优归属，其余组撤销标注。
    """
    per_frame_hits = {}
    for name in picks:
        png = os.path.join(frames_dir, name + ".png")
        if not os.path.exists(png):
            continue
        shot = icon_match.imread(png)
        if shot is None:
            continue
        per_frame_hits[name] = mark_known(shot, lib, scales)

    # 第一遍：每个命中挑一个「尺寸最贴合模板本体」的候选组作为归属
    claims = {}          # (frame, idx) -> (组下标, 贴合差)
    for gi, g in enumerate(groups):
        for m in g["members"]:
            for idx, (nm, h, csz) in enumerate(per_frame_hits.get(m["frame"], [])):
                if not (m["x0"] - dist <= h["cx"] <= m["x1"] + dist and
                        m["y0"] - dist <= h["cy"] <= m["y1"] + dist):
                    continue
                tw, th = csz
                diff = (abs(m["w"] - tw) / max(m["w"], tw, 1) +
                        abs(m["h"] - th) / max(m["h"], th, 1))
                key = (m["frame"], idx)
                if key not in claims or diff < claims[key][1]:
                    claims[key] = (gi, diff)

    # 第二遍：按归属标注
    for g in groups:
        g["known"] = ""
        g["known_score"] = 0.0
    for (frame, idx), (gi, _) in claims.items():
        nm, h, _ = per_frame_hits[frame][idx]
        g = groups[gi]
        if h["score"] > g["known_score"]:
            g["known"] = nm
            g["known_score"] = round(h["score"], 3)

    if verbose:
        n_known = sum(1 for g in groups if g["known"])
        print(f"  已入库标注：{n_known} 组命中现有模板（阈值 {KNOWN_MIN}，bbox 外扩 {dist}px）")
        # 诊断：命中了但没被任何候选吸收 ⇒ 该图标这次没被 icon_extract 切出来
        lost = []
        for name, hits in per_frame_hits.items():
            for idx, (nm, h, _) in enumerate(hits):
                if (name, idx) not in claims:
                    lost.append(f"{nm}@{name}({h['cx']},{h['cy']})={h['score']:.3f}")
        if lost:
            print(f"  ⚠ 已入库但本次未被候选吸收 {len(lost)} 处（icon_extract 没切出该图标）：")
            for s in lost[:8]:
                print(f"      {s}")
    return groups


# ---------------------------------------------------------------- ④ 产出

def write_outputs(groups, frames_dir, picks, out_dir, top, min_freq, verbose=True):
    os.makedirs(out_dir, exist_ok=True)
    # 排序：未入库优先 → marker（随视口移动、最需要模板）→ ui → single → repeat（地形噪声，垫底）
    # 组内按**面积降序**而不是频次降序：实测「频次高 = 重要」在本场景**不成立**——
    # 地形元素（石头/树丛）在每个视口都出现，频次虚高；而真图标会动态显隐
    # （眠枭在 5 帧里只出现 2 帧），频次反而低。尺寸才是更可靠的优先级信号。
    KR = {"marker": 0, "ui": 1, "single": 2, "repeat": 3}
    groups = sorted(groups, key=lambda g: (bool(g["known"]), KR.get(g["kind"], 4),
                                           -g["rep"]["area"]))
    shown = [g for g in groups if g["freq"] >= min_freq][:top]
    for i, g in enumerate(shown):
        g["id"] = f"C{i + 1:03d}"
        g["cx"], g["cy"] = g["rep"]["cx"], g["rep"]["cy"]
        g["w"], g["h"] = g["rep"]["w"], g["rep"]["h"]
        # 供 make_grid 出总览图用（它按 bbox 裁剪，不认 cx/cy）
        g["x0"], g["y0"] = g["rep"]["x0"], g["rep"]["y0"]
        g["x1"], g["y1"] = g["rep"]["x1"], g["rep"]["y1"]
        g["rep_color"] = g["rep"]["color"]

    meta = {
        "frames_dir": frames_dir,
        "frames": picks,
        "groups_total": len(groups),
        "groups_shown": len(shown),
        "groups": [
            {k: v for k, v in g.items() if k != "members" and k != "rep"} | {
                "rep_bbox": [g["rep"]["x0"], g["rep"]["y0"], g["rep"]["x1"], g["rep"]["y1"]],
                "rep_color": g["rep"]["color"],
                "members": [{"frame": m["frame"], "id": m["id"], "cx": m["cx"], "cy": m["cy"]}
                            for m in g["members"]],
            }
            for g in shown
        ],
    }
    jp = os.path.join(out_dir, "candidates.json")
    with open(jp, "w", encoding="utf-8") as f:
        json.dump(meta, f, ensure_ascii=False, indent=1)

    # 网格总览图：借代表帧出图（人工看一眼就能认）
    rep_frame = shown[0]["rep"]["frame"] if shown else (picks[0] if picks else None)
    gp = ""
    if rep_frame:
        im = Image.open(os.path.join(frames_dir, rep_frame + ".png")).convert("RGB")
        gp = os.path.join(out_dir, "grid.png")
        icon_extract.make_grid(im, shown, gp, cols=8, cell=110)

    # 待确认清单
    cp = os.path.join(out_dir, "checklist.md")
    known_all = [g for g in groups if g["known"]]
    lines = [
        "# 图标档案待确认清单",
        "",
        f"- 来源帧目录：`{frames_dir}`",
        f"- 帧（{len(picks)}）：{', '.join(picks)}",
        f"- 聚合候选 {len(groups)} 组，列出前 {len(shown)} 组（频次 ≥{min_freq}）",
        "",
        "**回填规则**：把「名称」列的 `?` 改成图标的中文名即视为要入库；",
        "保持 `?` 或 `-` 表示跳过；`known` 行已入库、无需处理。",
        f"**回填后执行**：`python tools/icon_intake.py --apply {cp}`",
        "",
        "网格总览图（先看这张认图标）：`" + (gp or "(未生成)") + "`",
        "",
    ]
    if known_all:
        # 同名合并：同一图标可能有多个命中位置（如家园在两个视口各命中一处），
        # 逐条列出会让清单看着像重复报，合并成一行并取最高分
        merged = {}
        for g in known_all:
            nm = g["known"]
            if nm not in merged or g["known_score"] > merged[nm]["known_score"]:
                merged[nm] = g
        lines += [f"### 本次已识别（{len(merged)} 个，无需处理）", ""]
        for nm, g in sorted(merged.items(), key=lambda kv: -kv[1]["known_score"]):
            lines.append(f"- **{nm}** 分数 {g['known_score']} "
                         f"@{g['rep']['cx']},{g['rep']['cy']} 帧={','.join(g['frames'])}")
        lines.append("")
    lines += [
        "### 待确认",
        "",
        "类型说明：`marker` = 随视口移动的地图图标（**最需要模板**）、"
        "`ui` = 屏幕常驻 HUD（坐标就够）、`single` = 单帧出现（优先级最低）",
        "",
        "| # | 名称 | 类型 | 频次 | 尺寸 | 色系 | 代表位置 | 帧 | 状态 |",
        "|---|---|---|---|---|---|---|---|---|",
    ]
    for g in shown:
        lines.append(
            f"| {g['id']} | ? | {g['kind']} | {g['freq']} | {g['w']}x{g['h']} | {g['rep_color']} "
            f"| {g['cx']},{g['cy']} | {','.join(g['frames'])} "
            f"| {'known:' + g['known'] if g['known'] else ''} |")
    with open(cp, "w", encoding="utf-8") as f:
        f.write("\n".join(lines) + "\n")

    if verbose:
        n_new = sum(1 for g in shown if not g["known"])
        print(f"\n聚合候选 {len(groups)} 组 → 列出 {len(shown)} 组（其中待确认 {n_new} 组）")
        print(f"  清单 → {cp}")
        print(f"  数据 → {jp}")
        if gp:
            print(f"  总览 → {gp}")
        print("\n频次最高的待确认候选：")
        for g in [x for x in shown if not x["known"]][:12]:
            print(f"  {g['id']} [{g['kind']:6s}] 频次{g['freq']} ({g['cx']:4d},{g['cy']:4d}) "
                  f"{g['w']:3d}x{g['h']:3d} {g['rep_color']} 帧={','.join(g['frames'])}")
        stat = {}
        for g in shown:
            if not g["known"]:
                stat[g["kind"]] = stat.get(g["kind"], 0) + 1
        if stat:
            print("  待确认类型分布：" +
                  "  ".join(f"{k}={v}" for k, v in sorted(stat.items())))
    return cp


# ---------------------------------------------------------------- ⑤ 回填入库

ROW = re.compile(r"^\|\s*(C\d+)\s*\|\s*([^|]*?)\s*\|")


def cmd_apply(a):
    cp = a.apply
    out_dir = os.path.dirname(os.path.abspath(cp))
    jp = os.path.join(out_dir, "candidates.json")
    if not os.path.exists(jp):
        print(f"缺 {jp}（--apply 需与 candidates.json 同目录）")
        return 1
    with open(jp, encoding="utf-8") as f:
        meta = json.load(f)
    by_id = {g["id"]: g for g in meta["groups"]}

    named = []
    with open(cp, encoding="utf-8") as f:
        for line in f:
            m = ROW.match(line)
            if not m:
                continue
            gid, name = m.group(1), m.group(2).strip()
            if name in ("", "?", "-", "?"):
                continue
            g = by_id.get(gid)
            if not g:
                print(f"  清单里的 {gid} 不在 candidates.json 中，跳过")
                continue
            if g["known"] and name == g["known"]:
                continue          # 已入库且未改名
            named.append((gid, name, g))

    if not named:
        print("清单里没有回填名称（名称列仍全是 ?）——先把要入库的图标名填上再跑。")
        return 1

    print(f"准备入库 {len(named)} 个模板：")
    ok = 0
    for gid, name, g in named:
        shot = os.path.join(meta["frames_dir"], g["members"][0]["frame"] + ".png")
        bbox = ",".join(str(v) for v in g["rep_bbox"])
        note = (f"icon_intake 批量入库（{gid} 频次{g['freq']} "
                f"帧={','.join(g['frames'])}）")
        cmd = [PY, MATCH_PY, "--make", name, "--from-shot", shot,
               "--bbox", bbox, "--pad", str(a.pad), "--note", note]
        r = subprocess.run(cmd, capture_output=True, text=True,
                           encoding="utf-8", errors="replace")
        if r.returncode == 0:
            ok += 1
            print(f"  ✓ {name:12s} ← {gid} {g['w']}x{g['h']} @{os.path.basename(shot)} {bbox}")
        else:
            print(f"  ✗ {name} 建模板失败：{(r.stderr or r.stdout).strip()[:120]}")
    print(f"\n入库 {ok}/{len(named)}")

    if a.regress and os.path.exists(REGRESS_PY):
        r = subprocess.run([PY, REGRESS_PY], capture_output=True, text=True,
                           encoding="utf-8", errors="replace")
        tail = [l for l in (r.stdout or "").strip().splitlines() if l.strip()][-3:]
        print(f"回归（应 rc=0）：rc={r.returncode}")
        for l in tail:
            print(f"  {l}")
    return 0 if ok == len(named) else 1


# ---------------------------------------------------------------- main

def cmd_intake(a):
    if not os.path.isdir(a.frames_dir):
        print(f"帧目录不存在：{a.frames_dir}")
        return 1
    picks = [p.strip() for p in a.pick.split(",") if p.strip()] if a.pick else []
    if not picks:
        pngs = [f for f in os.listdir(a.frames_dir) if f.lower().endswith(".png")]
        pngs.sort(key=lambda f: -os.path.getmtime(os.path.join(a.frames_dir, f)))
        picks = [os.path.splitext(f)[0] for f in pngs[:a.max_frames]]
    if not picks:
        print("没选到帧。用 --pick 显式指定，或确认目录里有 png。")
        return 1
    extra = a.extra.split() if a.extra else []

    os.makedirs(a.out, exist_ok=True)
    print(f"① 逐帧提取（{len(picks)} 帧）")
    pool, per_frame = collect(a.frames_dir, picks, a.out, extra,
                             min_side=a.min_side, min_area=a.min_area)
    if not pool:
        print("没有提取到任何候选。")
        return 1

    print(f"② 跨帧聚合（签名阈值 {a.sig_thr}）")
    groups = cluster(pool, len(picks), a.sig_thr)
    print(f"  {len(pool)} 个候选 → {len(groups)} 组")

    print("③ 标注已入库")
    lib = icon_match.load_lib()
    scales = [float(x) for x in a.scales.split(",")]
    annotate_known(groups, a.frames_dir, picks, lib, scales, a.known_dist)

    print("④ 产出清单")
    cmd_apply_path = write_outputs(groups, a.frames_dir, picks, a.out,
                                   a.top, a.min_freq)
    print(f"\n下一步：看 grid.png 认图标 → 在 {os.path.basename(cmd_apply_path)} 里填名称 → --apply")
    return 0


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--frames-dir", default="", help="关键帧目录（png，文件名=帧名）")
    ap.add_argument("--pick", default="", help="要处理的帧名，逗号分隔（不给则取最新 N 帧）")
    ap.add_argument("--max-frames", type=int, default=8, help="未给 --pick 时取最新几帧")
    ap.add_argument("--out", default="", help="产出目录")
    ap.add_argument("--top", type=int, default=50, help="清单最多列几组（按类型/频次排序）")
    ap.add_argument("--min-freq", type=int, default=1, help="只列出现次数 ≥N 的组")
    ap.add_argument("--sig-thr", type=float, default=0.90, help="跨帧聚类签名阈值")
    ap.add_argument("--min-side", type=int, default=18,
                    help="候选最长边下限（过小候选的签名没区分度，会跨视口误聚）")
    ap.add_argument("--min-area", type=int, default=150, help="候选面积下限（px²）")
    ap.add_argument("--scales", default="0.85,0.9,0.95,1.0,1.05,1.1,1.15",
                    help="已入库标注用的多尺度列表")
    ap.add_argument("--known-dist", type=int, default=14,
                    help="已入库标注的坐标容差（碎片化图标中心天然有偏差，别调太小）")
    ap.add_argument("--extra", default="", help="透传给 icon_extract 的参数（如 --no-texture）")
    ap.add_argument("--apply", default="", help="回填后的 checklist.md 路径（批量入库）")
    ap.add_argument("--pad", type=int, default=3, help="建模板时四向留边")
    ap.add_argument("--regress", action="store_true", help="入库后跑一次 icon_regression")
    a = ap.parse_args()

    if a.apply:
        return cmd_apply(a)
    if not (a.frames_dir and a.out):
        ap.print_help()
        return 0
    return cmd_intake(a)


if __name__ == "__main__":
    sys.exit(main())
