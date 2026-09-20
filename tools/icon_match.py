# -*- coding: utf-8 -*-
"""图标模板匹配：用一次人工确认的图标样本，在任意截图上自动定位同类图标。

背景（见 docs/nrc-icon-atlas-method.md）：
- 批量学习产出的坐标图谱是「文字 → 坐标」，没有「图标长什么样 → 怎么认出来」这一层；
- 实测本地 9B VLM **无法**可靠分类小尺寸图标（已知蓝色传送锚点被答成"建筑/营地"），
  因此图标命名只能靠「人工看一眼确认一次」，之后靠**模板匹配复用**。
- 本工具就是那个"复用"环节：人工确认的样本 → 模板 → 在多张截图上确定性定位。

用法：
    # 1) 用一次确认的图标样本建模板（也可 --crop 直接从截图裁）
    python tools/icon_match.py --make 眠枭庇护所 --from-shot v05.png --bbox 1544,122,1585,149
    # 2) 在目标截图上搜同名模板
    python tools/icon_match.py --find 眠枭庇护所 --shot v11.png
    # 3) 一次搜全部模板
    python tools/icon_match.py --find-all --shot v11.png --min-score 0.85

输出纯文字（不读图），供主对话直接消费。
"""
import argparse
import json
import os
import sys

import cv2
import numpy as np

LIB_DIR = os.path.join(os.path.dirname(os.path.dirname(os.path.abspath(__file__))),
                       ".workbuddy", "nrc", "icon_atlas", "library")
LIB_JSON = os.path.join(LIB_DIR, "library.json")


def load_lib():
    if os.path.exists(LIB_JSON):
        with open(LIB_JSON, encoding="utf-8") as f:
            return json.load(f)
    return {"templates": {}}


def save_lib(lib):
    os.makedirs(LIB_DIR, exist_ok=True)
    with open(LIB_JSON, "w", encoding="utf-8") as f:
        json.dump(lib, f, ensure_ascii=False, indent=1)


def imread(path):
    data = np.fromfile(path, dtype=np.uint8)          # 兼容中文路径
    return cv2.imdecode(data, cv2.IMREAD_COLOR)


def describe_name(name):
    """把图标名转成模板文件名（去掉路径非法字符）。"""
    for ch in '\\/:*?"<>|':
        name = name.replace(ch, "_")
    return name


def cmd_make(a):
    lib = load_lib()
    shot = imread(a.from_shot)
    if shot is None:
        print(f"读图失败：{a.from_shot}")
        return 1
    x0, y0, x1, y1 = map(int, a.bbox.split(","))
    # bbox 是图标本体；四向各留 pad 像素邻域，提高匹配鲁棒性
    p = a.pad
    h, w = shot.shape[:2]
    bx0, by0 = max(0, x0 - p), max(0, y0 - p)
    bx1, by1 = min(w, x1 + p + 1), min(h, y1 + p + 1)
    tmpl = shot[by0:by1, bx0:bx1]
    os.makedirs(LIB_DIR, exist_ok=True)
    tpath = os.path.join(LIB_DIR, describe_name(a.make) + ".png")
    # 注意：cv2.imwrite 走 C++ 文件 API，**中文路径会静默写失败/写成乱码名**，
    # 必须 imencode + tofile（实测：imwrite 把「眠枭庇护所.png」写成 GBK 乱码名）。
    ok, buf = cv2.imencode(".png", tmpl)
    buf.tofile(tpath)
    lib["templates"][a.make] = {
        "file": os.path.basename(tpath),
        "size": [int(bx1 - bx0), int(by1 - by0)],
        "core_bbox": [x0, y0, x1, y1],
        "core_size": [int(x1 - x0), int(y1 - y0)],
        "pad": p,
        "from_shot": os.path.basename(a.from_shot),
        "source_center": [int((x0 + x1) / 2), int((y0 + y1) / 2)],
        "note": a.note,
    }
    save_lib(lib)
    print(f"模板已建：{a.make}  尺寸 {bx1-bx0}x{by1-by0}（本体 {x1-x0}x{y1-y0} + pad {p}）")
    print(f"  文件 → {tpath}")
    print(f"  来源 → {os.path.basename(a.from_shot)} 中心 ({int((x0+x1)/2)},{int((y0+y1)/2)})")
    return 0


def match_one(shot, tmpl, scales, min_score):
    """多尺度模板匹配，返回 [(score, cx, cy, w, h), ...] 按分数降序。"""
    hits = []
    th, tw = tmpl.shape[:2]
    for s in scales:
        nw, nh = max(3, int(round(tw * s))), max(3, int(round(th * s)))
        if nw >= shot.shape[1] or nh >= shot.shape[0]:
            continue
        r = cv2.resize(tmpl, (nw, nh), interpolation=cv2.INTER_AREA if s < 1 else cv2.INTER_CUBIC)
        res = cv2.matchTemplate(shot, r, cv2.TM_CCOEFF_NORMED)
        _, mx, _, mxloc = cv2.minMaxLoc(res)
        if mx < min_score:
            continue
        hits.append({
            "score": float(mx), "scale": float(s),
            "cx": int(mxloc[0] + nw / 2), "cy": int(mxloc[1] + nh / 2),
            "w": nw, "h": nh,
        })
    hits.sort(key=lambda h: -h["score"])
    # 抑制重叠命中（同一图标在多个尺度上重复报）
    keep = []
    for h in hits:
        if any(abs(h["cx"] - k["cx"]) < 14 and abs(h["cy"] - k["cy"]) < 14 for k in keep):
            continue
        keep.append(h)
    return keep


def cmd_find(a):
    lib = load_lib()
    shot = imread(a.shot)
    if shot is None:
        print(f"读图失败：{a.shot}")
        return 1
    names = [a.find] if a.find and a.find != "*" else (list(lib["templates"]) if not a.find_all else list(lib["templates"]))
    if not names:
        print("图标库为空。先用 --make 建模板。")
        return 1
    scales = [float(x) for x in a.scales.split(",")]
    print(f"目标截图 {os.path.basename(a.shot)}  {shot.shape[1]}x{shot.shape[0]}   尺度 {scales}")
    total = 0
    for n in names:
        meta = lib["templates"].get(n)
        if not meta:
            print(f"  未找到模板：{n}")
            continue
        tmpl = imread(os.path.join(LIB_DIR, meta["file"]))
        if tmpl is None:
            print(f"  模板图读取失败：{meta['file']}")
            continue
        hits = match_one(shot, tmpl, scales, a.min_score)
        total += len(hits)
        print(f"\n【{n}】命中 {len(hits)} 处（阈值 {a.min_score}）")
        for h in hits[:a.top]:
            print(f"   分数 {h['score']:.3f}  中心 ({h['cx']},{h['cy']})  "
                  f"{h['w']}x{h['h']}  尺度 {h['scale']}")
    print(f"\n合计命中 {total} 处")
    return 0


def cmd_list(a):
    lib = load_lib()
    print(f"图标库 {LIB_JSON}   模板 {len(lib['templates'])} 个")
    for n, m in lib["templates"].items():
        print(f"   {n:12s} {m['size'][0]}x{m['size'][1]}  来源 {m['from_shot']} "
              f"中心 {m['source_center']}  备注 {m.get('note') or '-'}")
    return 0


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--make", default="", help="新建/覆盖模板名")
    ap.add_argument("--from-shot", default="", help="模板来源截图")
    ap.add_argument("--bbox", default="", help="图标本体像素框 x0,y0,x1,y1")
    ap.add_argument("--pad", type=int, default=3, help="四向额外留边")
    ap.add_argument("--note", default="", help="备注")
    ap.add_argument("--find", default="", help="要搜的模板名；传 * 搜全部")
    ap.add_argument("--find-all", action="store_true", help="搜全部模板")
    ap.add_argument("--shot", default="", help="目标截图")
    ap.add_argument("--scales", default="0.85,0.9,0.95,1.0,1.05,1.1,1.15", help="多尺度列表")
    ap.add_argument("--min-score", type=float, default=0.85,
                    help="匹配分下限。实测：真命中 ≥0.95（同图标跨视口 0.951~0.986），"
                         "0.55~0.65 是水体/地形误报——2026-09-20 用 0.55 在风息山口帧上"
                         "搜出 3 处，只有 0.986 那处点的出面板，另两处点开的是「标记」面板")
    ap.add_argument("--top", type=int, default=10, help="每个模板最多报几处")
    ap.add_argument("--list", action="store_true", help="列出图标库")
    a = ap.parse_args()

    if a.list:
        return cmd_list(a)
    if a.make:
        if not (a.from_shot and a.bbox):
            print("--make 需要 --from-shot 与 --bbox")
            return 1
        return cmd_make(a)
    if a.find or a.find_all:
        if not a.shot:
            print("--find 需要 --shot")
            return 1
        return cmd_find(a)
    ap.print_help()
    return 0


if __name__ == "__main__":
    sys.exit(main())
