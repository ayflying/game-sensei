# -*- coding: utf-8 -*-
"""game-sensei 的 OCR 常驻服务（PP-OCR / RapidOCR 引擎）。

本文件由 internal/ocr 以 `python -c <base64 包裹的本文件内容>` 启动，
**不落任何临时脚本文件**；协议与错误处理见 internal/ocr/ocr.go。

协议：stdin 一行一个 JSON 请求，stdout 一行一个 JSON 响应（UTF-8）。

    启动就绪   {"ready": true, "engine": "rapidocr_onnxruntime"}
    识别请求   {"id": 1, "b64": "<PNG 字节的 base64>", "box_thresh": 0.5, "text_score": 0.5}
    识别响应   {"id": 1, "ok": true, "items": [{"text": "市场", "score": 0.67, "box": [x0,y0,x1,y1]}]}
    失败响应   {"id": 1, "ok": false, "error": "..."}
    退出请求   {"cmd": "exit"}

两个必须守住的约定：

1. **协议只走 stdout，日志一律改道 stderr**。第三方库（RapidOCR / onnxruntime）
   会往 stdout 打进度与警告，一旦混进协议流，Go 侧就会把日志当成响应解析。
   所以进程一起来就把 sys.stdout 指向 stderr，真正的协议通道另存一份。
2. **图像一律用 PIL 解码成 RGB 三通道再交给引擎**。RapidOCR 自己那条
   `load_img` 对「路径」走 PIL（RGB）、对「带 alpha 的数组」却走 cv2 的
   BGR 合并，两条入口通道序不一致；PP-OCR 识别网络对通道序敏感，
   与其依赖调用方传什么格式，不如在入口处收敛成唯一形态。
3. **必须关掉 `width_height_ratio` 这条「扁图跳检测」启发式**（见 main()）。
   它是给「整张图就是一行字」的截图用的，而游戏界面里条状区域恰恰相反——
   导航栏、标题栏、价签行都是「一条带里排着好几个词」。实测裁 900x100 的
   底部导航栏：默认参数 0.09s 返回空，关掉后 1.6s 正确认出 4 个按钮。
   注意关掉它不会牺牲「细条单行」场景：高度不足时引擎还有 min_height
   兜底，仍会整图当一行识别。
"""

import base64
import io
import json
import sys

import numpy as np
from PIL import Image
from rapidocr_onnxruntime import RapidOCR

# 协议通道：先把 stdout 抢下来，再把 sys.stdout 让给日志。
_channel = sys.stdout
sys.stdout = sys.stderr


def reply(obj):
    """把一条响应写进协议通道并立刻 flush（Go 侧是阻塞读，不 flush 会挂住）。"""
    _channel.write(json.dumps(obj, ensure_ascii=False) + "\n")
    _channel.flush()


def decode_image(b64):
    """把请求里的 PNG 字节解码成 RGB 三通道数组。"""
    raw = base64.b64decode(b64)
    return np.array(Image.open(io.BytesIO(raw)).convert("RGB"))


def run_ocr(engine, img, req):
    """跑一次识别，返回 [{text, score, box}]。

    阈值只在请求显式给出时才传：RapidOCR 收 kwargs 会**就地改掉引擎实例**
    的 box_thresh / text_score，一旦传过就永久生效，默认值不再恢复。
    """
    kwargs = {}
    if req.get("box_thresh") is not None:
        kwargs["box_thresh"] = float(req["box_thresh"])
    if req.get("text_score") is not None:
        kwargs["text_score"] = float(req["text_score"])

    result, _ = engine(img, **kwargs)
    items = []
    for entry in result or []:
        box, text, score = entry[0], entry[1], entry[2]
        xs = [float(p[0]) for p in box]
        ys = [float(p[1]) for p in box]
        items.append({
            "text": text,
            "score": float(score),
            # 四点多边形收敛成外接矩形：调用方要的是「点哪里」，不是倾斜框。
            "box": [int(min(xs)), int(min(ys)), int(max(xs)), int(max(ys))],
        })
    return items


def main():
    # 模型加载放在第一次响应之前，就绪信号发出时引擎已可用。
    #
    # width_height_ratio=-1 关掉「宽高比超过阈值就认定整图只有一行字、
    # 跳过检测」的启发式（默认阈值 8）。游戏界面里被裁成条状的区域
    # （导航栏 / 标题栏 / 列表行 / 价签行）宽高比普遍超过 8，若跳过检测，
    # 整条带子会被当成一行文字送进识别网络，结果是空或乱码。
    engine = RapidOCR(width_height_ratio=-1)
    reply({"ready": True, "engine": "rapidocr_onnxruntime"})

    for line in sys.stdin:
        line = line.strip()
        if not line:
            continue
        try:
            req = json.loads(line)
        except Exception as exc:  # 解析失败也要回一条，否则 Go 侧只能等超时
            reply({"ok": False, "error": "请求不是合法 JSON: %s" % exc})
            continue
        if req.get("cmd") == "exit":
            break
        try:
            items = run_ocr(engine, decode_image(req["b64"]), req)
            reply({"id": req.get("id"), "ok": True, "items": items})
        except Exception as exc:
            reply({"id": req.get("id"), "ok": False,
                   "error": "%s: %s" % (type(exc).__name__, exc)})


main()
