#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""探测：怎样才能让 qwen3.5 系列「不思考、直接出动作」。

背景：nrc_probe.py 两次实测都是 eval 打满 num_predict、content 为空、
done_reason=length，答案全埋在 thinking 里。Phase 2 要的是「给图出一个动作」，
必须先把输出预算从思考链里抢回来。

用法: python tools/think_test.py --image shot.png
"""
import argparse
import base64
import io
import json
import time
import urllib.request

URL = "http://127.0.0.1:11435/api/chat"
MODEL = "qwen3.5:9b"

MIN_PROMPT = """你是手机游戏《洛克王国：世界》的操作助手。
看这张横屏截图，给出下一步动作，只输出一行，格式：
ACTION TAP x=0.50 y=0.80
坐标是 0~1 的归一化值（左上角为原点）。不要输出解释、不要输出多行。"""

LONG_PROMPT = """你在看手机游戏《洛克王国：世界》的横屏截图。
请严格按格式回答，每行一条：场景/摇杆/按钮/中央/精灵/可交互/建议/评分。
坐标用 0~1 归一化值。"""


def load_b64(path, width):
    from PIL import Image
    im = Image.open(path)
    if im.mode not in ("RGB", "L"):
        im = im.convert("RGB")
    if im.size[0] > width:
        im = im.resize((width, round(im.size[1] * width / im.size[0])), Image.LANCZOS)
    buf = io.BytesIO()
    im.save(buf, format="JPEG", quality=88)
    return base64.b64encode(buf.getvalue()).decode()


def run(name, prompt, b64, think, num_predict, fmt=None):
    body = {
        "model": MODEL,
        "messages": [{"role": "user", "content": prompt, "images": [b64]}],
        "stream": False,
        "options": {"num_predict": num_predict, "temperature": 0.1},
    }
    if think is not None:
        body["think"] = think
    if fmt:
        body["format"] = fmt

    req = urllib.request.Request(
        URL, data=json.dumps(body).encode("utf-8"),
        headers={"Content-Type": "application/json"}, method="POST")
    t0 = time.time()
    try:
        with urllib.request.urlopen(req, timeout=600) as r:
            d = json.loads(r.read().decode("utf-8"))
    except Exception as e:
        print(f"[{name}] ❌ {type(e).__name__}: {e}")
        return
    el = time.time() - t0
    msg = d.get("message", {}) or {}
    content = (msg.get("content") or "").strip()
    thinking = (msg.get("thinking") or "").strip()
    print(f"[{name}] {el:.1f}s | eval={d.get('eval_count',0)} "
          f"({d.get('eval_count',0)/max(el,1e-6):.0f}tok/s) | "
          f"done={d.get('done_reason')} | thinking={len(thinking)}字 | content={len(content)}字")
    print(f"    content: {content[:300]!r}")
    print()


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--image", required=True)
    ap.add_argument("--width", type=int, default=1024)
    a = ap.parse_args()
    b64 = load_b64(a.image, a.width)
    print(f"图片送审 base64 {len(b64)//1024}KB | 模型 {MODEL}\n")

    # 关键对照：think 开关 × 提示词长度
    run("A think=false + 短提示", MIN_PROMPT, b64, False, 400)
    run("B think=false + 长提示", LONG_PROMPT, b64, False, 800)
    run("C think=默认 + 短提示", MIN_PROMPT, b64, None, 400)
    run("D think=false + 短提示 + format=json", MIN_PROMPT, b64, False, 400,
        fmt={"type": "object",
             "properties": {"action": {"type": "string"}},
             "required": ["action"]})


if __name__ == "__main__":
    main()
