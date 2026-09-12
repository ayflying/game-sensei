#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""老师出动作：提示词调优 + 多帧准确率对照。

核心前提（tools/think_test.py 实测）：Ollama /api/chat 的**顶层** `think: false`
对 qwen3.5:9b 有效（1.3s、零 thinking）；写在 options 里则无效。
所以这里统一用顶层 think=false，把输出预算全留给动作本身。

用法:
  python tools/teacher_probe.py --dir .workbuddy/nrc-shots --variant all
"""
import argparse
import base64
import io
import json
import os
import re
import time
import urllib.request

# 老师地址：环境变量 GAME_SENSEI_TEACHER_URL 优先，未设置则用本机项目自带实例
URL = os.environ.get("GAME_SENSEI_TEACHER_URL",
                     "http://127.0.0.1:11435").rstrip("/") + "/api/chat"

# 界面先验：必须给，否则模型会乱猜（实测长提示退化成坐标数组就是先验太弱）
PRIOR = """你是手机游戏《洛克王国：世界》的实时操作助手。这是一款 3D 开放世界精灵收集游戏。
界面布局常识：
- 左下角半透明圆形区域 = 虚拟摇杆，按住推向某方向可移动角色
- 右下角有若干圆形按钮，一般包括：精灵切换、奔跑、跳跃、交互/捕捉技能
- 顶部有任务追踪文字与坐标；画面中央是玩家角色
- 若出现对话气泡或按钮列表，说明正处于对话/菜单中，需要点击选项
"""

TAIL = """
坐标必须是 0~1 的归一化值（左上角 0,0；右下角 1,1）。
只输出一行动作，不要解释、不要输出多行。"""

ACTION_SPEC = """
可用动作：
ACTION TAP x=<0~1> y=<0~1>                              点击某点
ACTION JOYSTICK cx=<0~1> cy=<0~1> tx=<0~1> ty=<0~1> dur=<毫秒>   摇杆：中心推到目标点
ACTION SWIPE x=<0~1> y=<0~1> x2=<0~1> y2=<0~1> dur=<毫秒>        滑动（转视角）
ACTION KEY code=back                                     返回键
"""

VARIANTS = {
    # 极简：只给一句身份，看基线准确度
    "min": "你是手机游戏《洛克王国：世界》的操作助手。看截图，输出下一步动作，只输出一行。\n" + ACTION_SPEC + TAIL,
    # 带界面先验：预期准确度最高
    "prior": PRIOR + "看这张截图，判断下一步最该做什么。\n" + ACTION_SPEC + TAIL,
    # 先验 + 强制先描述再动作（想换来更高准确度，但要小心触发思考）
    "cot": PRIOR + """看这张截图。先在心里判断：画面处于什么场景？主角在哪？有没有可交互目标？
然后只输出一行动作（不要输出你的判断过程）。
""" + ACTION_SPEC + TAIL,
}

ACTION_RE = re.compile(
    r"ACTION\s+(TAP|JOYSTICK|SWIPE|HOLD|KEY|WAIT)\b[^\n]*", re.I)


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


def ask(prompt, b64, model, num_predict, timeout):
    body = {
        "model": model,
        "messages": [{"role": "user", "content": prompt, "images": [b64]}],
        "stream": False,
        "think": False,  # 顶层！写进 options 里无效
        "options": {"num_predict": num_predict, "temperature": 0.1},
    }
    req = urllib.request.Request(
        URL, data=json.dumps(body).encode("utf-8"),
        headers={"Content-Type": "application/json"}, method="POST")
    t0 = time.time()
    with urllib.request.urlopen(req, timeout=timeout) as r:
        d = json.loads(r.read().decode("utf-8"))
    return d, time.time() - t0


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--dir", default=".workbuddy/nrc-shots")
    ap.add_argument("--image")
    ap.add_argument("--model", default="qwen3.5:9b")
    ap.add_argument("--width", type=int, default=1024)
    ap.add_argument("--num-predict", type=int, default=200)
    ap.add_argument("--timeout", type=int, default=180)
    ap.add_argument("--variant", default="all", help="min|prior|cot|all")
    a = ap.parse_args()

    if a.image:
        images = [a.image]
    else:
        images = sorted(os.path.join(a.dir, f) for f in os.listdir(a.dir)
                        if f.lower().endswith((".png", ".jpg", ".jpeg")))

    variants = list(VARIANTS) if a.variant == "all" else [a.variant]

    for vname in variants:
        prompt = VARIANTS[vname]
        print("=" * 74)
        print(f"变体 [{vname}]  | 模型 {a.model} | 图片 {len(images)} 张")
        print("=" * 74)
        ok = 0
        total_t = 0.0
        for path in images:
            b64 = load_b64(path, a.width)
            try:
                d, el = ask(prompt, b64, a.model, a.num_predict, a.timeout)
            except Exception as e:
                print(f"  {os.path.basename(path)}: ❌ {type(e).__name__}: {e}")
                continue
            total_t += el
            msg = d.get("message", {}) or {}
            content = (msg.get("content") or "").strip()
            thinking = (msg.get("thinking") or "").strip()
            m = ACTION_RE.search(content)
            mark = "✅" if m else "❌"
            if m:
                ok += 1
            head = content.splitlines()[0] if content else "(空)"
            print(f"  {mark} {os.path.basename(path):<12} {el:5.1f}s "
                  f"tok={d.get('eval_count',0):<4} done={d.get('done_reason'):<7} "
                  f"think={len(thinking):<5} → {head[:80]}")
            if not m and content:
                print(f"       原始输出: {content[:200]!r}")
        n = len(images)
        if n:
            print(f"\n  动作格式合规: {ok}/{n} | 平均耗时 {total_t/max(n,1):.1f}s\n")


if __name__ == "__main__":
    main()
