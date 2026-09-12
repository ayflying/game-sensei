#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""移动手势受控实验（排查用，可删除）。

目的：验证「从摇杆中心平滑划到边缘」是否浪费了大部分推杆时长 ——
      如果成立，那 adb 手势应当改成「先在边缘按住、再保持」。

做法：在同一位置重复几种手势，逐一截图并算相邻两帧的画面变化量 Δ，
      与 helper 内置的 frameDiff 口径一致（灰度、降采样 160px 宽、逐像素平均绝对差）。

用法：
    python tools/movetest.py
"""
import os
import subprocess
import sys
import time

from PIL import Image, ImageChops

ADB_CANDIDATES = [
    os.path.expandvars(r"%LOCALAPPDATA%\Microsoft\WinGet\Packages"
                       r"\Google.PlatformTools_Microsoft.Winget.Source_8wekyb3d8bbwe\platform-tools\adb.exe"),
    "adb",
]
SHOT = "_movetest_shot.png"


def find_adb() -> str:
    for p in ADB_CANDIDATES:
        if p == "adb" or os.path.exists(p):
            return p
    sys.exit("找不到 adb")


ADB = find_adb()


def run(*args: str) -> str:
    r = subprocess.run([ADB, *args], capture_output=True)
    return (r.stdout or b"").decode("utf-8", "replace")


def tap(x: int, y: int) -> None:
    run("shell", "input", "tap", str(x), str(y))


def swipe(x1: int, y1: int, x2: int, y2: int, ms: int) -> None:
    run("shell", "input", "swipe", str(x1), str(y1), str(x2), str(y2), str(ms))


def grab(path: str = SHOT) -> Image.Image:
    """设备内截图后 pull，避免 exec-out 在 Windows 管道里出 0 字节。"""
    run("shell", "screencap", "-p", "/sdcard/_mt.png")
    run("pull", "/sdcard/_mt.png", path)
    return Image.open(path).convert("RGB")


def frame_diff(a: Image.Image, b: Image.Image, width: int = 160) -> float:
    """与 helper demo.go 的 frameDiff 同口径：灰度 → 缩到 width → 平均绝对差。"""
    h = max(1, round(a.height * width / a.width))
    ga = a.convert("L").resize((width, h))
    gb = b.convert("L").resize((width, h))
    hist = ImageChops.difference(ga, gb).histogram()
    n = width * h
    return sum(i * c for i, c in enumerate(hist)) / n


def main() -> None:
    size = run("shell", "wm", "size")
    print("屏幕:", size.strip())
    W, H = 3200, 2136
    cx, cy = round(0.21 * W), round(0.79 * H)   # 摇杆中心
    rx, ry = round(0.09 * W), round(0.08 * H)   # 推杆幅度
    print(f"摇杆中心 ({cx},{cy}) 幅度 ({rx},{ry})  右缘=({cx+rx},{cy})  下缘=({cx},{cy+ry})")

    prev = grab("_mt_s0.png")
    print("\n起始画面已截")

    # 每个手势连做 3 次，看稳定性（同样的手势结果是否一致）。
    cases = []
    for i in range(3):
        cases.append((f"右 中心→缘 2000ms #{i+1}", lambda: swipe(cx, cy, cx + rx, cy, 2000)))
    for i in range(2):
        cases.append((f"下 中心→缘 2000ms #{i+1}", lambda: swipe(cx, cy, cx, cy + ry, 2000)))
    for i in range(2):
        cases.append((f"上 中心→缘 2000ms #{i+1}", lambda: swipe(cx, cy, cx, cy - ry, 2000)))
    cases.append(("右 中心→缘 4000ms", lambda: swipe(cx, cy, cx + rx, cy, 4000)))
    cases.append(("右 中心→超远 2000ms", lambda: swipe(cx, cy, cx + rx * 3, cy, 2000)))

    print(f"\n{'手势':34s} {'Δ(画面变化量)':>12s}   判定")
    for name, fn in cases:
        fn()
        time.sleep(0.6)
        cur = grab()
        d = frame_diff(prev, cur)
        judge = "★ 在走" if d >= 6.0 else "· 几乎没动"
        print(f"{name:34s} {d:12.2f}   {judge}")
        prev = cur


if __name__ == "__main__":
    main()
