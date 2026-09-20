#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""图标提取回归判据：量化 `tools/icon_extract.py` 对**已知图标**的覆盖率与 bbox 完整度。

## 为什么需要它

改 `icon_extract` 的判据（颜色阈值、连通域切分、去重策略）时，改动很容易"修一个漏一个"——
2026-09-20 加「局部对比度」第四路时，覆盖率 8/10 → 10/10，但 texture 的松 bbox
一度把颜色路的精确 bbox 挤掉（去重按面积排的副作用），当时若不量化就会漏掉。
**这个脚本就是"能捕获败局"的判据**：真值点取自人工确认过的图标中心（library.json 的 core_bbox）。

## 判据

对每个用例（帧 + 图标名 + 真值中心 + 真值尺寸）：
  - **覆盖**：有候选 bbox（含容差）盖住真值中心 → 记命中
  - **尺寸达标**：命中候选的 w/h 与真值之比都落在 [0.7, 1.6] → 记达标
  - 取"尺寸最接近真值"的那个候选参与尺寸判定（多候选时）

## 用法

  python tools/icon_regression.py                                   # 用默认真值文件
  python tools/icon_regression.py --truth <path> [--tol 10] [--min-cover 1.0] [--min-size 0.6]
  python tools/icon_regression.py --no-texture                      # 未知参数原样透传给 icon_extract

退出码：覆盖率达 --min-cover 且尺寸达标率达 --min-size 时回 0，否则回 1（便于串进脚本）。

真值文件（本地资产，`.workbuddy/` 不入版本库）格式：

  {
    "shots_dir": ".workbuddy/tmp/screenshots/nrc-20260918",
    "cases": [
      {"frame": "k0", "name": "眠枭庇护所", "center": [861, 299], "truth_size": [41, 27]}
    ]
  }
"""
import argparse
import json
import os
import subprocess
import sys

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
DEFAULT_TRUTH = os.path.join(ROOT, ".workbuddy", "nrc", "icon_atlas", "regression.json")
WORK = os.path.join(ROOT, ".workbuddy", "tmp", "icx_regress")


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--truth", default=DEFAULT_TRUTH, help="真值 JSON 路径")
    ap.add_argument("--extract", default=os.path.join("tools", "icon_extract.py"))
    ap.add_argument("--extra-args", default="", help="额外传给 icon_extract 的参数（空格分隔）")
    ap.add_argument("--tol", type=int, default=10, help="中心点容差 px")
    ap.add_argument("--lo", type=float, default=0.7, help="尺寸比下限")
    ap.add_argument("--hi", type=float, default=1.6, help="尺寸比上限")
    ap.add_argument("--min-cover", type=float, default=1.0, help="覆盖率门槛（默认全命中）")
    ap.add_argument("--min-size", type=float, default=0.6, help="尺寸达标率门槛")
    # 未知参数（如 --no-texture、--tex-k 0.8）原样透传给 icon_extract，
    # 便于直接对比不同判据组合，无需 --extra-args="..." 绕一圈
    a, unknown = ap.parse_known_args()
    truth = json.load(open(a.truth, encoding="utf-8"))
    shots = truth["shots_dir"]
    if not os.path.isabs(shots):
        shots = os.path.join(ROOT, shots)
    extra = list(unknown)
    if a.extra_args:
        extra += a.extra_args.split()

    by_frame = {}
    for c in truth["cases"]:
        by_frame.setdefault(c["frame"], []).append(c)

    tot = hit = sized = 0
    fails = []
    for frame, cases in by_frame.items():
        png = os.path.join(shots, f"{frame}.png")
        if not os.path.exists(png):
            print(f"[{frame}] 截图缺失，跳过：{png}")
            continue
        out = os.path.join(WORK, frame)
        r = subprocess.run([sys.executable, a.extract, png, "--out", out] + extra,
                           capture_output=True, text=True, encoding="utf-8", errors="replace")
        if r.returncode != 0:
            print(f"[{frame}] 提取失败 rc={r.returncode}\n{(r.stdout + r.stderr)[-500:]}")
            fails.append(f"{frame}: 提取失败")
            continue
        icons = json.load(open(os.path.join(out, "icons.json"), encoding="utf-8"))["icons"]
        print(f"\n[{frame}] 候选 {len(icons)} 个")
        for c in cases:
            px, py = c["center"]
            tw, th = c["truth_size"]
            tot += 1
            cands = [ic for ic in icons
                     if ic["x0"] - a.tol <= px <= ic["x1"] + a.tol
                     and ic["y0"] - a.tol <= py <= ic["y1"] + a.tol]
            if not cands:
                print(f"   ✗ {c['name']}({px},{py}) 无候选覆盖   真值 {tw}x{th}")
                fails.append(f"{frame}/{c['name']}: 无覆盖")
                continue
            hit += 1
            cands.sort(key=lambda ic: abs(ic["w"] / tw - 1) + abs(ic["h"] / th - 1))
            ic = cands[0]
            dw, dh = ic["w"] / tw, ic["h"] / th
            ok = a.lo <= dw <= a.hi and a.lo <= dh <= a.hi
            sized += ok
            print(f"   {'✓' if ok else '~'} {c['name']}({px},{py}) → {ic['id']} {ic['w']}x{ic['h']} "
                  f"@({ic['cx']},{ic['cy']}) {ic['color']} fill={ic['fill']} | 真值 {tw}x{th} "
                  f"比 {dw:.2f}/{dh:.2f}" + ("" if ok else "  ← 尺寸不匹配"))
            if not ok:
                fails.append(f"{frame}/{c['name']}: 尺寸 {ic['w']}x{ic['h']} vs 真值 {tw}x{th}")

    cov = hit / tot if tot else 0.0
    szr = sized / tot if tot else 0.0
    print(f"\n=== 覆盖 {hit}/{tot} ({cov:.0%})   尺寸达标 {sized}/{tot} ({szr:.0%}) ===")
    if fails:
        print("未达标项：")
        for f in fails:
            print(f"   - {f}")
    ok = cov >= a.min_cover and szr >= a.min_size
    print(f"判据：覆盖 ≥{a.min_cover:.0%} 且 尺寸达标 ≥{a.min_size:.0%} → {'通过' if ok else '不通过'}")
    return 0 if ok else 1


if __name__ == "__main__":
    sys.exit(main())
