#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""ADB 探针：给真机发 tap / drag / key，并抓截图落盘。

用途：标定《洛克王国：世界》的地图脱困脚本坐标。
真机横屏分辨率 3200x2136（物理 2136x3200 竖屏旋转而来）。

用法（坐标一律是设备像素）：
  python tools/esc_probe.py shot out.png
  python tools/esc_probe.py tap 1600 1068
  python tools/esc_probe.py drag 1600 1068 2600 1068 800
  python tools/esc_probe.py key BACK
  python tools/esc_probe.py sleep 1500

ADB 路径可用环境变量 GAME_SENSEI_ADB 覆盖。
"""
import os
import subprocess
import sys
import time

ADB = os.environ.get("GAME_SENSEI_ADB") or os.path.join(
    os.environ.get("LOCALAPPDATA", ""),
    r"Microsoft\WinGet\Packages"
    r"\Google.PlatformTools_Microsoft.Winget.Source_8wekyb3d8bbwe"
    r"\platform-tools\adb.exe",
)


def run(*args, binary=False):
    r = subprocess.run([ADB, *args], capture_output=True)
    if r.returncode != 0:
        sys.stderr.write(r.stderr.decode("utf-8", "replace"))
        raise SystemExit(f"adb {' '.join(args)} failed rc={r.returncode}")
    return r.stdout if binary else r.stdout.decode("utf-8", "replace")


def shot(out):
    data = run("exec-out", "screencap", "-p", binary=True)
    # 某些设备把 \n 转换成 \r\n，会破坏 PNG 数据；按魔数兜底修复。
    if not data.startswith(b"\x89PNG"):
        data = data.replace(b"\r\n", b"\n")
    if not data.startswith(b"\x89PNG"):
        raise SystemExit("screencap 返回的不是 PNG 数据")
    with open(out, "wb") as f:
        f.write(data)
    print(f"shot -> {out} ({len(data)} bytes)")


def main():
    if len(sys.argv) < 2:
        print(__doc__)
        return
    cmd = sys.argv[1]
    if cmd == "shot":
        shot(sys.argv[2] if len(sys.argv) > 2 else "shot.png")
    elif cmd == "tap":
        run("shell", "input", "tap", sys.argv[2], sys.argv[3])
        print(f"tap {sys.argv[2]} {sys.argv[3]}")
    elif cmd == "drag":
        ms = sys.argv[6] if len(sys.argv) > 6 else "600"
        run("shell", "input", "swipe", *sys.argv[2:6], ms)
        print(f"drag {sys.argv[2]},{sys.argv[3]} -> {sys.argv[4]},{sys.argv[5]} {ms}ms")
    elif cmd == "key":
        run("shell", "input", "keyevent", sys.argv[2])
        print(f"key {sys.argv[2]}")
    elif cmd == "sleep":
        time.sleep(int(sys.argv[2]) / 1000.0)
        print("slept")
    else:
        print(__doc__)


if __name__ == "__main__":
    main()
