#!/usr/bin/env python3
"""抓一张主屏截图，供 tools/vlm_bench.py 评测 VLM 使用。

用法：
    python tools/screenshot.py -o shot.png

依赖：Pillow（`pip install pillow`）。纯 Windows 环境用 PIL.ImageGrab
的多屏抓法即可；本项目 Go 侧的实时抓屏另走 GDI/DXGI，与此脚本无关。
"""

from __future__ import annotations

import argparse
import sys


def main() -> None:
    ap = argparse.ArgumentParser(description="主屏截图")
    ap.add_argument("-o", "--out", default="shot.png", help="输出文件")
    args = ap.parse_args()

    try:
        from PIL import ImageGrab
    except ImportError:
        sys.exit("缺少 Pillow，请先 pip install pillow")

    img = ImageGrab.grab(all_screens=True)
    img.save(args.out)
    print(f"已保存 {args.out} ({img.width}x{img.height})")


if __name__ == "__main__":
    main()
