#!/usr/bin/env python
# -*- coding: utf-8 -*-
"""跨帧巡检图标库：一个模板在多帧地图上出现在哪、是固定 UI 还是随视口移动。

为什么需要它（2026-09-20 实测教训）：
  库里的模板混了两类东西，用错方式定位会白跑：
    ① **固定 UI**（家园 / 皮卡月刊 / 眠底护所）：多帧坐标逐像素不动 ⇒ **坐标就够**，
       不需要每次跑模板匹配；视口怎么平移都在原地。
    ② **随视口移动**（眠枭庇护所 / 星光对决 / 炼金釜 / 魔法师之家）：3 帧 3 个位置
       ⇒ 必须模板匹配，写死坐标必错。
  光看「点击有反应」分不出这两类（点击探针只判"是不是图标"，不判"会不会动"）。
  ⇒ 每次扩库后跑一遍本工具，把性质标注进库，后续定位才知道该用坐标还是模板。

用法：
  python tools/icon_crossframe.py --shots v11.png,hm1.png,probe_base.png
  python tools/icon_crossframe.py --shots ... --annotate   # 把结论写回 library.json 的 note
  python tools/icon_crossframe.py --shots ... --out crossframe.md

⚠ 所选帧**必须覆盖不同视口**：同视口多帧会把一切都判成「固定 UI」。
⚠ 单帧只出现一次的模板无法定性（判为「单帧出现」），要换帧再验。
"""
import argparse
import os
import sys

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
sys.path.insert(0, os.path.join(ROOT, "tools"))
import icon_match  # noqa: E402  复用读图与匹配（中文路径安全）

SCALES = [0.85, 0.9, 0.95, 1.0, 1.05, 1.1, 1.15]
TAG = "【跨帧行为】"
# 判定"坐标没动"的容差：模板匹配点在缩放/重采样下有 ±2~3px 抖动，给到 8px
SAME_POS = 8


def classify(cells):
    """cells: [(帧名, hit|None)] → (类别, 说明)。"""
    pos = [(bn, h) for bn, h in cells if h]
    if not pos:
        return "未出现", "所选帧中一次都没命中（可能不在这些视口里，或已失效）"
    if len(pos) < 2:
        bn, h = pos[0]
        return "单帧出现", f"只在 {bn} 命中 ({h['cx']},{h['cy']})，需再换帧验证"
    xs = [h["cx"] for _, h in pos]
    ys = [h["cy"] for _, h in pos]
    span = f"x 跨度 {max(xs) - min(xs)}px / y 跨度 {max(ys) - min(ys)}px"
    if max(xs) - min(xs) <= SAME_POS and max(ys) - min(ys) <= SAME_POS:
        return "固定UI", f"{len(pos)} 帧同坐标 ({pos[0][1]['cx']},{pos[0][1]['cy']}) ⇒ 坐标就够，不必模板匹配"
    where = " / ".join(f"{bn} ({h['cx']},{h['cy']})" for bn, h in pos)
    return "随视口移动", f"{where} —— {span} ⇒ 必须模板匹配"


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--shots", required=True, help="逗号分隔的帧路径（≥2 帧，须覆盖不同视口）")
    ap.add_argument("--min-score", type=float, default=0.85, help="匹配分下限（默认 0.85）")
    ap.add_argument("--out", default="", help="把矩阵写成 markdown")
    ap.add_argument("--annotate", action="store_true",
                    help="把跨帧行为写回 library.json 各条 note（覆盖旧的同类标注）")
    a = ap.parse_args()

    shot_paths = [s.strip() for s in a.shots.split(",") if s.strip()]
    if len(shot_paths) < 2:
        print("至少给 2 帧（--shots a.png,b.png）；只给 1 帧无法判定是否随视口移动")
        return 1

    lib = icon_match.load_lib()
    if not lib["templates"]:
        print("图标库为空")
        return 1

    imgs = []
    for p in shot_paths:
        im = icon_match.imread(p)
        if im is None:
            print(f"读图失败：{p}")
            return 1
        imgs.append((os.path.basename(p), im))
    print(f"跨帧巡检 {len(lib['templates'])} 个模板 × {len(imgs)} 帧"
          f"（阈值 {a.min_score}）：" + "、".join(bn for bn, _ in imgs))

    rows = []
    for name, meta in lib["templates"].items():
        tmpl = icon_match.imread(os.path.join(icon_match.LIB_DIR, meta["file"]))
        if tmpl is None:
            print(f"  模板图读取失败：{meta['file']}")
            continue
        cells = []
        for bn, im in imgs:
            hits = icon_match.match_one(im, tmpl, SCALES, a.min_score)
            cells.append((bn, hits[0] if hits else None))
        kind, detail = classify(cells)
        rows.append({"name": name, "kind": kind, "detail": detail, "cells": cells})
        mark = {"固定UI": "▣", "随视口移动": "↔", "单帧出现": "·", "未出现": "✗"}[kind]
        print(f"\n  {mark} {name}  [{kind}]")
        for bn, h in cells:
            if h:
                print(f"      {bn:16s} {h['score']:.3f}  ({h['cx']},{h['cy']})  "
                      f"{h['w']}x{h['h']} 尺度 {h['scale']}")
            else:
                print(f"      {bn:16s} MISS")
        print(f"      ⇒ {detail}")

    counts = {}
    for r in rows:
        counts[r["kind"]] = counts.get(r["kind"], 0) + 1
    print("\n=== 汇总 ===  " + "  ".join(f"{k} {v}" for k, v in counts.items()))
    fixed = [r["name"] for r in rows if r["kind"] == "固定UI"]
    moving = [r["name"] for r in rows if r["kind"] == "随视口移动"]
    if fixed:
        print(f"  固定UI（坐标就够）：{'、'.join(fixed)}")
    if moving:
        print(f"  随视口移动（须模板匹配）：{'、'.join(moving)}")

    if a.out:
        lines = ["# 图标库跨帧巡检", "",
                 f"- 帧：{'、'.join(bn for bn, _ in imgs)}（阈值 {a.min_score}）",
                 f"- 模板 {len(rows)} 个：" + "  ".join(f"{k} {v}" for k, v in counts.items()),
                 "", "| 模板 | 类别 | " + " | ".join(bn for bn, _ in imgs) + " | 判据 |",
                 "|---|---|" + "---|" * len(imgs) + "---|"]
        for r in rows:
            cells = " | ".join(
                (f"{h['score']:.3f} ({h['cx']},{h['cy']})" if h else "MISS")
                for _, h in r["cells"])
            lines.append(f"| {r['name']} | {r['kind']} | {cells} | {r['detail']} |")
        os.makedirs(os.path.dirname(os.path.abspath(a.out)) or ".", exist_ok=True)
        with open(a.out, "w", encoding="utf-8") as f:
            f.write("\n".join(lines) + "\n")
        print(f"矩阵已写入 {a.out}")

    if a.annotate:
        for r in rows:
            meta = lib["templates"].get(r["name"])
            if not meta:
                continue
            note = (meta.get("note") or "")
            note = note.split(TAG)[0].rstrip("；; ")
            meta["note"] = f"{note}；{TAG}{r['kind']}：{r['detail']}" if note else f"{TAG}{r['kind']}：{r['detail']}"
        icon_match.save_lib(lib)
        print(f"已把跨帧行为写回 {len(rows)} 条模板的 note")
    return 0


if __name__ == "__main__":
    sys.exit(main())
