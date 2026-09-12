#!/usr/bin/env python3
"""VLM 评测脚本：测本地/远程 Ollama 视觉模型的看图延迟、生成速度与输出质量。

用途：为 game-sensei 的 teacher（异步教学回路）选型，比较不同 VLM 在
"看一张游戏截屏并给出结构化描述" 任务上的表现。

用法：
    python tools/vlm_bench.py --image shot.png --models qwen3.5:2b qwen3-vl:2b
    python tools/vlm_bench.py --image shot.png --host http://127.0.0.1:11435 --rounds 2

依赖：仅标准库（urllib）。截图可先用 tools/screenshot.py 生成。
"""

from __future__ import annotations

import argparse
import base64
import json
import os
import time
import urllib.error
import urllib.request

DEFAULT_PROMPT = (
    "这是一张电脑屏幕截图。请指出：1)主窗口标题 2)画面中央主要内容 "
    "3)是否有明显可点击的按钮区域。每条不超过15字。"
)


def ask(base_url: str, model: str, image_b64: str, prompt: str,
        max_tokens: int, timeout: float, think: bool | None = None) -> dict:
    """发一次流式请求，返回耗时与输出统计。

    Ollama 的 qwen3 系列会把推理过程写在 message.thinking、正文写在
    message.content，因此两个字段都要收集（见项目日志里的踩坑记录）。
    think=False 尝试关闭思考；部分模型/版本会忽略该参数，实测以输出为准。
    """
    options: dict = {"temperature": 0, "num_predict": max_tokens}
    if think is not None:
        options["think"] = think
    body = json.dumps({
        "model": model,
        "messages": [{"role": "user", "content": prompt, "images": [image_b64]}],
        "stream": True,
        "options": options,
    }).encode()
    req = urllib.request.Request(
        base_url.rstrip("/") + "/api/chat",
        data=body,
        headers={"Content-Type": "application/json"},
    )

    t0 = time.time()
    first_token_at: float | None = None
    thinking: list[str] = []
    content: list[str] = []
    stats: dict = {}

    with urllib.request.urlopen(req, timeout=timeout) as resp:
        for line in resp:
            if not line.strip():
                continue
            d = json.loads(line)
            if d.get("done"):
                stats = {
                    "total_s": time.time() - t0,
                    "prompt_tokens": d.get("prompt_eval_count", 0),
                    "output_tokens": d.get("eval_count", 0),
                    "prompt_eval_s": d.get("prompt_eval_duration", 0) / 1e9,
                    "eval_s": d.get("eval_duration", 0) / 1e9,
                }
                break
            msg = d.get("message", {})
            piece = msg.get("content") or ""
            think = msg.get("thinking") or ""
            if (piece or think) and first_token_at is None:
                first_token_at = time.time() - t0
            if piece:
                content.append(piece)
            if think:
                thinking.append(think)

    stats["first_token_s"] = first_token_at if first_token_at is not None else -1.0
    stats["tok_per_s"] = stats.get("output_tokens", 0) / max(stats.get("eval_s", 0), 1e-9)
    stats["content"] = "".join(content).strip()
    stats["thinking"] = "".join(thinking).strip()
    return stats


def main() -> None:
    ap = argparse.ArgumentParser(description="Ollama VLM 评测")
    ap.add_argument("--host", default=os.environ.get("GAME_SENSEI_TEACHER_URL",
                                                     "http://127.0.0.1:11435"),
                    help="Ollama 地址（默认取环境变量 GAME_SENSEI_TEACHER_URL）")
    ap.add_argument("--image", required=True, help="测试截图路径")
    ap.add_argument("--models", nargs="+", required=True, help="模型名，可多个")
    ap.add_argument("--prompt", default=DEFAULT_PROMPT)
    ap.add_argument("--rounds", type=int, default=1, help="每个模型跑几轮（第 1 轮含冷加载）")
    ap.add_argument("--max-tokens", type=int, default=400)
    ap.add_argument("--timeout", type=float, default=300.0)
    ap.add_argument("--no-think", action="store_true",
                    help="尝试关闭思考（think=false），对比正文产出速度")
    args = ap.parse_args()

    think = False if args.no_think else None
    image_b64 = base64.b64encode(open(args.image, "rb").read()).decode()

    for model in args.models:
        print(f"\n{'=' * 62}\n模型: {model}"
              f"{'（think=false）' if args.no_think else ''}\n{'=' * 62}")
        for i in range(args.rounds):
            try:
                r = ask(args.host, model, image_b64, args.prompt,
                        args.max_tokens, args.timeout, think)
            except (urllib.error.URLError, TimeoutError) as e:
                print(f"  run{i + 1}: 请求失败 {e}")
                break
            print(
                f"  run{i + 1} | 总{r['total_s']:.1f}s | 首token{r['first_token_s']:.1f}s "
                f"| 编码{r['prompt_eval_s']:.1f}s | 生成{r['eval_s']:.1f}s "
                f"({r['tok_per_s']:.0f}tok/s) | 输入{r['prompt_tokens']}tok "
                f"输出{r['output_tokens']}tok"
            )
            if r["content"]:
                print(f"    正文: {r['content'][:200].replace(chr(10), ' ')}")
            else:
                tail = r["thinking"][-160:].replace(chr(10), " ")
                print(f"    ⚠️ 正文为空，thinking 尾部: {tail}")


if __name__ == "__main__":
    main()
