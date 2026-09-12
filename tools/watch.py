#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""点一下、然后连着看几帧：判断「这一下是否触发了持续行为」。

用途举例：
  · 点右侧任务追踪，看角色会不会自己走起来（游戏自带寻路）
  · 点某个按钮，看是否弹出面板并保持

用法：
    python tools/watch.py tap  <x> <y> [frames] [interval_s]
    python tools/watch.py key  <name>  [frames] [interval_s]
    python tools/watch.py none         [frames] [interval_s]

x/y 是**设备像素**（本机平板 3200x2136）。每帧都会存成 watch_1.png、watch_2.png …，
方便肉眼核对。输出为相邻两帧的画面变化量 Δ（与 helper 同口径）。
"""
import os
import subprocess
import sys
import time

from PIL import Image, ImageChops

ADB = os.path.expandvars(
    r"%LOCALAPPDATA%\Microsoft\WinGet\Packages"
    r"\Google.PlatformTools_Microsoft.Winget.Source_8wekyb3d8bbwe\platform-tools\adb.exe"
)
if not os.path.exists(ADB):
    ADB = "adb"


def run(*args: str) -> str:
    r = subprocess.run([ADB, *args], capture_output=True)
    return (r.stdout or b"").decode("utf-8", "replace")


def grab(path: str) -> Image.Image:
    run("shell", "screencap", "-p", "/sdcard/_w.png")
    run("pull", "/sdcard/_w.png", path)
    return Image.open(path).convert("RGB")


def frame_diff(a: Image.Image, b: Image.Image, width: int = 160) -> float:
    h = max(1, round(a.height * width / a.width))
    ga = a.convert("L").resize((width, h))
    gb = b.convert("L").resize((width, h))
    hist = ImageChops.difference(ga, gb).histogram()
    return sum(i * c for i, c in enumerate(hist)) / (width * h)


def main() -> None:
    if len(sys.argv) < 2:
        print(__doc__)
        return
    mode = sys.argv[1]
    rest = sys.argv[2:]
    if mode == "tap":
        x, y = int(rest[0]), int(rest[1])
        rest = rest[2:]
    elif mode == "key":
        keyname = rest[0]
        rest = rest[1:]
    frames = int(rest[0]) if rest else 4
    interval = float(rest[1]) if len(rest) > 1 else 2.0

    prev = grab("watch_0.png")
    if mode == "tap":
        run("shell", "input", "tap", str(x), str(y))
        print(f"已点 ({x},{y})")
    elif mode == "key":
        run("shell", "input", "keyevent", keyname)
        print(f"已按键 {keyname}")
    else:
        print("不操作，只看画面")

    print(f"\n{'帧':>4s}  {'Δ(与上一帧)':>12s}   判定")
    for i in range(1, frames + 1):
        time.sleep(interval)
        cur = grab(f"watch_{i}.png")
        d = frame_diff(prev, cur)
        judge = "画面在变（有持续行为）" if d >= 6.0 else "画面基本静止"
        print(f"{i:>4d}  {d:12.2f}   {judge}")
        prev = cur


if __name__ == "__main__":
    main()
