#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""给图标候选命名，产出「图标视觉特征库」。

## 与 icon_extract.py 的分工

    icon_extract.py  →  **在哪**（像素算法，坐标可靠，但不知道是什么）
    icon_annotate.py →  **是什么**（本地 VLM 语义，坐标不可靠但会命名）
    两者相加 = 可靠的图标档案。这正是视频批量学习缺的那一层：
    此前只产出了「文字 → 坐标」，没产出「图标 → 名称 + 视觉特征」。

## 用法

  python tools/icon_annotate.py <dir>/icons.json [--top 30] [--model qwen3.5:9b]

输出（写在 icons.json 同目录）：
  icon_atlas.json   带 name / desc 的档案（可被后续匹配工具复用）
  icon_atlas.md     人读版清单
支持断点续跑：已命名的条目会跳过。
"""
import argparse
import base64
import json
import os
import sys
import time
import urllib.request

from PIL import Image, ImageDraw


def make_context_crop(src_im, c, half=130, zoom=2):
    """带上下文的裁剪 + 红框标出目标图标。

    实测教训：把图标单独放大交给 9B 模型命名，会大量幻觉
    （同一批 30 个里出现「任务标记」x4、「角色头像」x4 和「减号」这类噪声）。
    原因是孤立图标没有上下文，模型只能猜。
    带上周边地图 + 红框指示后，模型能结合地形与邻近文字判断。
    """
    x0 = max(0, c["cx"] - half)
    y0 = max(0, c["cy"] - half)
    x1 = min(src_im.width, c["cx"] + half)
    y1 = min(src_im.height, c["cy"] + half)
    crop = src_im.crop((x0, y0, x1, y1))
    crop = crop.resize((crop.width * zoom, crop.height * zoom), Image.LANCZOS)
    d = ImageDraw.Draw(crop)
    d.rectangle(
        [(c["x0"] - x0) * zoom, (c["y0"] - y0) * zoom,
         (c["x1"] - x0) * zoom, (c["y1"] - y0) * zoom],
        outline=(255, 0, 0), width=3)
    return crop

QUESTION = (
    "这是一款手机游戏《洛克王国：世界》地图界面的局部截图，"
    "图中用**红色矩形框**标出了一个图标。"
    "请结合框内图案、框的位置以及周围的地形和文字，"
    "只用 2-6 个汉字回答红框里的图标最可能是什么，例如：传送点、"
    "宝箱、任务标记、营地、建筑、地名标注、装饰图案、地形纹理。"
    "看不出就回答「不确定」。只输出这几个字，不要任何解释、不要标点。"
)


def ask(host, model, path, question, timeout=240, tries=2):
    with open(path, "rb") as f:
        b64 = base64.b64encode(f.read()).decode()
    body = json.dumps({
        "model": model,
        "messages": [{"role": "user", "content": question, "images": [b64]}],
        "stream": False,
        "think": False,
    }).encode()
    last = ""
    for _ in range(tries):
        try:
            req = urllib.request.Request(
                f"http://{host}/api/chat", data=body,
                headers={"Content-Type": "application/json"})
            with urllib.request.urlopen(req, timeout=timeout) as r:
                d = json.loads(r.read())
            return d.get("message", {}).get("content", "").strip()
        except Exception as e:      # noqa: BLE001
            last = str(e)
            time.sleep(2)
    return f"ERR {last}"


def clean(text):
    """VLM 常带解释或标点，取首行前 12 字，去掉标点空白。"""
    if not text:
        return "不确定"
    line = text.strip().splitlines()[0]
    for ch in "：:。，,、!！?？《》\"'（）()【】[]":
        line = line.replace(ch, "")
    line = line.strip()
    if not line or len(line) > 12:
        return line[:12] if line else "不确定"
    return line


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("icons_json")
    ap.add_argument("--top", type=int, default=30, help="只命名面积最大的前 N 个")
    ap.add_argument("--model", default="qwen3.5:9b")
    ap.add_argument("--host", default="127.0.0.1:11435")
    ap.add_argument("--min-area", type=int, default=0)
    a = ap.parse_args()

    with open(a.icons_json, encoding="utf-8") as f:
        meta = json.load(f)
    outdir = os.path.dirname(os.path.abspath(a.icons_json))
    atlas_path = os.path.join(outdir, "icon_atlas.json")

    atlas = {}
    if os.path.exists(atlas_path):
        with open(atlas_path, encoding="utf-8") as f:
            for item in json.load(f).get("icons", []):
                atlas[item["id"]] = item

    icons = [c for c in meta["icons"] if c["area"] >= a.min_area]
    icons.sort(key=lambda c: -c["area"])
    todo = [c for c in icons[:a.top] if c["id"] not in atlas or
            atlas[c["id"]].get("name", "").startswith("ERR") or
            atlas[c["id"]].get("name") == "不确定"]

    print(f"候选 {len(meta['icons'])} 个，本次待命名 {len(todo)} 个（模型 {a.model}）", flush=True)
    src_im = Image.open(meta["source"]).convert("RGB")
    ctxdir = os.path.join(outdir, "ctx")
    os.makedirs(ctxdir, exist_ok=True)
    t0 = time.time()
    for i, c in enumerate(todo, 1):
        cpath = os.path.join(ctxdir, f"{c['id']}.png")
        make_context_crop(src_im, c).save(cpath)
        ans = clean(ask(a.host, a.model, cpath, QUESTION))
        item = dict(c)
        item["name"] = ans
        item["ctx_crop"] = cpath.replace("\\", "/")
        atlas[c["id"]] = item
        print(f"  [{i}/{len(todo)}] {c['id']} ({c['cx']},{c['cy']}) "
              f"{c['w']}x{c['h']} {c['color']} → {ans}   ({time.time()-t0:.0f}s)", flush=True)

    merged = {"source": meta["source"], "size": meta["size"],
              "count": len(atlas),
              "icons": sorted(atlas.values(), key=lambda c: -c["area"])}
    with open(atlas_path, "w", encoding="utf-8") as f:
        json.dump(merged, f, ensure_ascii=False, indent=1)

    # 人读版
    lines = [f"# 图标档案 — {os.path.basename(meta['source'])}", "",
             f"来源截图：`{meta['source']}`（{meta['size'][0]}x{meta['size'][1]}）",
             f"候选数：{len(merged['icons'])}", "",
             "| 编号 | 名称 | 中心坐标 | 尺寸 | 色系 | 填充率 | 边缘密度 | 面积 |",
             "|---|---|---|---|---|---|---|---|"]
    for c in merged["icons"]:
        lines.append(f"| {c['id']} | {c['name']} | ({c['cx']},{c['cy']}) | "
                     f"{c['w']}x{c['h']} | {c['color']} | {c['fill']} | "
                     f"{c.get('edge','-')} | {c['area']} |")
    md_path = os.path.join(outdir, "icon_atlas.md")
    with open(md_path, "w", encoding="utf-8") as f:
        f.write("\n".join(lines) + "\n")

    print(f"\n档案 → {atlas_path}")
    print(f"清单 → {md_path}")

    # 按名称聚合，方便一眼看出有哪些种类
    agg = {}
    for c in merged["icons"]:
        agg.setdefault(c["name"], []).append(c)
    print("\n=== 名称聚合 ===")
    for name, cs in sorted(agg.items(), key=lambda kv: -len(kv[1])):
        coords = " ".join(f"({c['cx']},{c['cy']})" for c in cs[:4])
        print(f"  {name:8s} x{len(cs):3d}   {coords}")


if __name__ == "__main__":
    main()
