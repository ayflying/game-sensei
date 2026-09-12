#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""让老师 VLM 看真机截图，识别洛克王国：世界的界面元素。

Phase 2 前置：先确认老师能不能「看懂」这个游戏，
再谈让它产出示范轨迹。看不懂就没法教。

用法:
  python tools/nrc_probe.py --image shot.png [--model qwen3.5:9b] [--width 1024]
  python tools/nrc_probe.py --dir .workbuddy/nrc-shots --model qwen3.5:9b
"""
import argparse
import base64
import io
import json
import os
import sys
import time
import urllib.request

# 老师地址：环境变量 GAME_SENSEI_TEACHER_URL 优先（如 http://100.66.1.2:11434），
# 未设置则用本机项目自带实例
DEFAULT_URL = os.environ.get("GAME_SENSEI_TEACHER_URL",
                             "http://127.0.0.1:11435").rstrip("/") + "/api/chat"

# 洛克王国：世界 的界面元素先验，写进提示词让模型有的放矢
GAME_PRIOR = """你在看手机游戏《洛克王国：世界》(Roco Kingdom: World) 的横屏截图。
这是腾讯魔方工作室的 3D 开放世界精灵收集游戏，界面元素大致有：
- 左下角：虚拟摇杆（圆形，用于移动角色）
- 右下角：若干圆形按钮（跳跃、奔跑、交互/捕捉、技能等），最上一个常是「狼头」面板按钮
- 顶部：任务追踪、小地图/坐标、体力或货币
- 中央：角色本体、周围精灵、可交互目标（NPC、采集物、野生精灵）
- 画面里可能出现的状态：开放世界探索 / 对话 / 战斗 / 背包或菜单 / 加载中
"""

PROMPT = GAME_PRIOR + """
请仔细观察这张截图，严格按下面格式回答，每行一条，不要写别的：

场景: <开放世界探索 | 对话中 | 战斗中 | 菜单/背包 | 加载中 | 其他，并一句话说明依据>
摇杆: <能看清就在 x=0.xxx y=0.xxx；看不清或不存在写「无」>  （归一化坐标，相对整张图，左上角为 0,0）
按钮: <从右上到左下依次列出你能辨认的圆形按钮，格式 名称(x=0.xxx,y=0.xxx)；看不清写「无」>
中央: <画面正中央有什么，一句话>
精灵: <画面里有没有精灵/宠物/NPC？有就描述外观和位置，没有写「无」>
可交互: <当前有没有可点击的交互目标？有就给出它的大致归一化坐标>
建议: <作为一个新手，下一步最该做什么动作？给出一条具体的、可用归一化坐标表达的动作>
评分: <0-10 分，评估当前画面「是否适合新手学习基本操作」，10 表示非常适合>
"""


def load_b64(path: str, width: int) -> tuple:
    from PIL import Image
    im = Image.open(path)
    orig = im.size
    if im.mode not in ("RGB", "L"):
        im = im.convert("RGB")
    if width and im.size[0] > width:
        h = round(im.size[1] * width / im.size[0])
        im = im.resize((width, h), Image.LANCZOS)
    buf = io.BytesIO()
    im.save(buf, format="JPEG", quality=88)
    return base64.b64encode(buf.getvalue()).decode(), orig, im.size


def ask(url: str, model: str, b64: str, timeout: int,
        num_predict: int = 4000, no_think: bool = False) -> dict:
    prompt = PROMPT
    if no_think:
        # qwen3 系支持 /no_think 触发词；比 think:false 参数可靠
        prompt = prompt + "\n/no_think\n直接输出上面 8 行答案，不要写分析过程。\n"
    body = {
        "model": model,
        "messages": [{"role": "user", "content": prompt, "images": [b64]}],
        "stream": False,
        "options": {"num_predict": num_predict, "temperature": 0.2},
    }
    req = urllib.request.Request(
        url, data=json.dumps(body).encode("utf-8"),
        headers={"Content-Type": "application/json"}, method="POST",
    )
    t0 = time.time()
    with urllib.request.urlopen(req, timeout=timeout) as resp:
        data = json.loads(resp.read().decode("utf-8"))
    data["_elapsed"] = time.time() - t0
    return data


FIELDS = ["场景", "摇杆", "按钮", "中央", "精灵", "可交互", "建议", "评分"]


def salvage(text: str) -> str:
    """从思考链里抢救结构化答案。

    qwen3 系常把 8 行答案散落在 thinking 里（带 markdown 列表符号、全角冒号、
    甚至粗体标记），content 却是空的。这里做一次宽松提取。
    """
    import re
    found = {}
    for raw in text.splitlines():
        line = raw.strip()
        # 去掉列表符号 / 粗体 / 井号
        line = re.sub(r"^[\*\-#>\s]+", "", line)
        line = line.replace("**", "")
        for f in FIELDS:
            if f in found:
                continue
            m = re.match(rf"^{f}\s*[:：]\s*(.+)$", line)
            if m and m.group(1).strip():
                found[f] = m.group(1).strip()
                break
    if len(found) < 3:
        return ""
    return "\n".join(f"{f}: {found[f]}" for f in FIELDS if f in found)


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--image")
    ap.add_argument("--dir")
    ap.add_argument("--url", default=DEFAULT_URL)
    ap.add_argument("--model", default="qwen3.5:9b")
    ap.add_argument("--width", type=int, default=1024)
    ap.add_argument("--timeout", type=int, default=600)
    ap.add_argument("--num-predict", type=int, default=4000)
    ap.add_argument("--no-think", action="store_true", help="提示词里加 /no_think，压制思考链")
    a = ap.parse_args()

    if a.dir:
        files = sorted(
            os.path.join(a.dir, f) for f in os.listdir(a.dir)
            if f.lower().endswith((".png", ".jpg", ".jpeg"))
        )
    elif a.image:
        files = [a.image]
    else:
        ap.error("需要 --image 或 --dir")
        return

    for path in files:
        print("=" * 72)
        print(f"图片: {os.path.basename(path)}")
        b64, orig, sent = load_b64(path, a.width)
        print(f"原始 {orig[0]}x{orig[1]} → 送审 {sent[0]}x{sent[1]} | base64 {len(b64)//1024}KB")
        try:
            r = ask(a.url, a.model, b64, a.timeout, a.num_predict, a.no_think)
        except Exception as e:
            print(f"❌ 请求失败: {type(e).__name__}: {e}")
            continue

        msg = r.get("message", {}) or {}
        content = (msg.get("content") or "").strip()
        thinking = (msg.get("thinking") or "").strip()
        print(f"耗时 {r['_elapsed']:.1f}s | "
              f"eval {r.get('eval_count', 0)}tok @ {r.get('eval_count', 0)/max(r['_elapsed'],1e-6):.0f}tok/s | "
              f"thinking {len(thinking)}字 | done_reason={r.get('done_reason')}")
        if not content:
            print("⚠️ content 为空，尝试从 thinking 里抢救结构化答案：")
            salvaged = salvage(thinking)
            if salvaged:
                print("-" * 72)
                print(salvaged)
            else:
                print("（thinking 里也没有可用答案，打印尾部）")
                print(thinking[-600:])
        else:
            print("-" * 72)
            print(content)
    print("=" * 72)


if __name__ == "__main__":
    main()
