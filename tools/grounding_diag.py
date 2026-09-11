#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""诊断：关掉思考后，模型的决策是否真的依赖画面内容。

背景：live 示范 8 步，画面明显在变（角色在走），老师却给了 8 次逐字节
相同的「摇杆右推」。怀疑 think=false 让它退化成照抄提示词结构。

判定方法：对同一批帧问两类问题——
  (A) 「画面可读信息类」：答案必须从图里读出来（如任务追踪显示的距离），
      若答案随帧变化 → 模型确实在看图；
  (B) 「动作决策类」：若答案不随帧变化，说明决策没用到画面。

用法: python tools/grounding_diag.py --dir .workbuddy/demos/<会话>/color
"""
import argparse
import base64
import io
import json
import os
import time
import urllib.request

URL = "http://127.0.0.1:11435/api/chat"
MODEL = "qwen3.5:9b"

QUESTIONS = [
    ("A-读距离", "看截图右侧的任务追踪面板，最近的捕捉目标距离是多少米？只回答一个数字，不要单位不要解释。"),
    ("A-读场景", "画面里最显眼的地标是什么？只回答一个词（如：帐篷/树/石头/房子/水边）。"),
    ("B-出动作", "你是《洛克王国：世界》的操作助手。看截图，只输出下一步动作，格式 ACTION JOYSTICK cx=0.21 cy=0.69 tx=0.xx ty=0.xx dur=1200 或 ACTION TAP x=0.xx y=0.xx。不要解释。"),
]

# 给 B 类加界面先验，与正式 demo 提示词一致
PRIOR = ("你是手机游戏《洛克王国：世界》的实时操作助手。\n"
         "界面常识：左下角半透明圆形区域是虚拟摇杆；右下角是圆形按钮（奔跑/跳跃/交互）；"
         "右侧中部是任务追踪文字。\n"
         "屏幕 2608x1200，虚拟摇杆中心约在 x=0.21 y=0.69。\n")


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


def ask(prompt, b64, think, num_predict=200, timeout=300):
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
    m = d.get("message", {}) or {}
    return (m.get("content") or "").strip(), time.time() - t0, d.get("eval_count", 0)


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--dir", required=True)
    ap.add_argument("--think", action="store_true", help="开启思考（默认关）")
    ap.add_argument("--width", type=int, default=1024)
    a = ap.parse_args()

    files = sorted(f for f in os.listdir(a.dir)
                   if f.lower().endswith((".png", ".jpg", ".jpeg")))
    print(f"帧目录: {a.dir} | {len(files)} 帧 | think={'on' if a.think else 'off'}\n")

    think = True if a.think else False
    for tag, q in QUESTIONS:
        prompt = (PRIOR + q) if tag.startswith("B") else q
        print("=" * 72)
        print(f"[{tag}] {q[:50]}…")
        print("-" * 72)
        answers = []
        for f in files:
            b64 = load_b64(os.path.join(a.dir, f), a.width)
            try:
                content, el, tok = ask(prompt, b64, think)
            except Exception as e:
                print(f"  {f}: ❌ {type(e).__name__}: {e}")
                continue
            head = content.replace("\n", " ")[:60] if content else "(空)"
            answers.append(head)
            print(f"  {f}  {el:5.1f}s tok={tok:<4} → {head}")
        uniq = len(set(answers))
        verdict = "✅ 随画面变化" if uniq > 1 else "❌ 完全不变（未使用画面信息）"
        print(f"\n  不同答案数: {uniq}/{len(answers)} → {verdict}\n")


if __name__ == "__main__":
    main()
