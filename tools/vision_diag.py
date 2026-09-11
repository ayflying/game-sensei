#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""诊断：think=false 时，模型到底有没有在「看」图？

背景：teacher_probe 用 think=false 跑 6 张差别很大的游戏截图，
输出却几乎一模一样（都是 TAP x≈0.86 y≈0.32、固定 16 token）——
高度怀疑关闭思考的同时把视觉通路也短路了。

判定方法：同一提示词喂两张**完全不同**的图（游戏截图 vs 桌面截图），
若答案几乎一致 → 说明模型根本没读图，think=false 不可用于视觉任务。

用法: python tools/vision_diag.py
"""
import argparse
import base64
import io
import json
import time
import urllib.request

URL = "http://127.0.0.1:11435/api/chat"
MODEL = "qwen3.5:9b"

QUESTIONS = [
    # 必须依赖图像才能回答
    ("这是什么画面？只回答一个词：游戏/办公软件/网页/其他",
     "游戏"),
    ("画面右下角有没有一排圆形按钮？只回答：有/没有", "有"),
    ("画面上方有没有文字？只回答：有/没有", "有"),
]


def load_b64(path, width=1024):
    from PIL import Image
    im = Image.open(path)
    if im.mode not in ("RGB", "L"):
        im = im.convert("RGB")
    if im.size[0] > width:
        im = im.resize((width, round(im.size[1] * width / im.size[0])), Image.LANCZOS)
    buf = io.BytesIO()
    im.save(buf, format="JPEG", quality=88)
    return base64.b64encode(buf.getvalue()).decode()


def ask(prompt, b64, think, num_predict=300, timeout=300):
    body = {
        "model": MODEL,
        "messages": [{"role": "user", "content": prompt, "images": [b64]}],
        "stream": False,
        "options": {"num_predict": num_predict, "temperature": 0.1},
    }
    if think is not None:
        body["think"] = think
    req = urllib.request.Request(
        URL, data=json.dumps(body).encode("utf-8"),
        headers={"Content-Type": "application/json"}, method="POST")
    t0 = time.time()
    with urllib.request.urlopen(req, timeout=timeout) as r:
        d = json.loads(r.read().decode("utf-8"))
    el = time.time() - t0
    m = d.get("message", {}) or {}
    return (m.get("content") or "").strip(), (m.get("thinking") or "").strip(), el, d


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--a", default=".workbuddy/nrc-shots/shot1.png", help="游戏截图")
    ap.add_argument("--b", default=".workbuddy/vlm-bench.png", help="对照图（桌面截图）")
    ap.add_argument("--width", type=int, default=1024)
    a = ap.parse_args()

    A = load_b64(a.a, a.width)
    B = load_b64(a.b, a.width)
    print(f"图A(游戏) {a.a}")
    print(f"图B(对照) {a.b}\n")

    for question, expect in QUESTIONS:
        print("=" * 72)
        print(f"问题: {question}")
        print(f"游戏图应有答案: {expect}")
        print("-" * 72)
        for label, b64 in (("图A-游戏", A), ("图B-对照", B)):
            for think in (False, True):
                tag = "think=false" if think is False else "think=默认"
                try:
                    content, thinking, el, d = ask(question, b64, think)
                except Exception as e:
                    print(f"  {label:<10} {tag:<12} ❌ {type(e).__name__}: {e}")
                    continue
                show = content.replace("\n", " ")[:70] if content else "(空)"
                print(f"  {label:<10} {tag:<12} {el:5.1f}s "
                      f"tok={d.get('eval_count',0):<4} think={len(thinking):<5} → {show}")
        print()


if __name__ == "__main__":
    main()
