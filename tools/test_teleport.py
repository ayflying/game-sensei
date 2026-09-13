#!/usr/bin/env python3
"""teleport.py 纯函数单测。

为什么值得单独测：传送这条路**曾经「不报错但白干」**——点空白地图会弹出标记面板，
Δ 也有 15，看起来「有反应」，于是整轮操作变成「开地图→放标记→关地图」，
角色一步没动。真正区分「点中传送点」和「点空白」的只有两件事：
蓝色图标判据 + 底部黄色上下文按钮。这两条判据一旦漂移，故障会以
「角色没动」的形式静默出现，所以必须钉死。

跑法：
    python tools/test_teleport.py
"""
from __future__ import annotations

import io
import os
import sys

sys.path.insert(0, os.path.dirname(os.path.abspath(__file__)))

from find_icons import is_anchor  # noqa: E402
from teleport import (_blobs, is_teleport_yellow, pick_bottom_button,  # noqa: E402
                      shield_candidates)

try:
    from PIL import Image
except ImportError:
    sys.exit("缺少 Pillow")


def _png(px_rows):
    """用 [[(r,g,b), ...], ...] 造一张小图，返回 PNG 字节。"""
    h = len(px_rows)
    w = len(px_rows[0])
    im = Image.new("RGB", (w, h))
    im.putdata([c for row in px_rows for c in row])
    buf = io.BytesIO()
    im.save(buf, format="PNG")
    return buf.getvalue()


def _fill(w, h, color):
    return [[color for _ in range(w)] for _ in range(h)]


def _paint(rows, x0, y0, x1, y1, color):
    for y in range(y0, y1):
        for x in range(x0, x1):
            rows[y][x] = color


# 实测采样值（2026-09-13 真机截图）
SHIELD_ICON = (72, 78, 163)      # 盾牌图标：正蓝，R≈G
SHIELD_ICON2 = (76, 73, 133)
RIVER = (115, 183, 201)          # 青色河水：G 远高于 R
SNOW_TERRAIN = (72, 96, 149)     # 雪地/夜晚地图地形：G 比 R 高 17~26
DESERT = (198, 167, 106)         # 米黄沙漠地形
TELEPORT_YELLOW = (242, 198, 112)  # 面板底部黄色按钮


def test_图标判据认图标不认地形():
    assert is_anchor(*SHIELD_ICON), "正蓝图标应被判为锚点"
    assert is_anchor(*SHIELD_ICON2), "第二个实测图标样本应被判为锚点"
    assert not is_anchor(*RIVER), "青色河水不该被当成锚点"
    assert not is_anchor(*SNOW_TERRAIN), \
        "雪地地形（G 比 R 高 23）曾被旧判据误判，必须挡住"
    assert not is_anchor(*DESERT), "米黄地形不该被当成锚点"


def test_地形大片不会变成候选():
    """整片雪地地形 → 0 个候选（旧判据在这里会给出成百上千个）。"""
    png = _png(_fill(200, 140, SNOW_TERRAIN))
    assert shield_candidates(png) == []


def test_单个图标能被找到():
    rows = _fill(200, 140, DESERT)
    _paint(rows, 95, 65, 105, 75, SHIELD_ICON)   # 10x10 图标，面积 100
    cands = shield_candidates(_png(rows))
    assert len(cands) == 1, f"应恰好找到 1 个，实际 {len(cands)}"
    cx, cy = cands[0]["cx"], cands[0]["cy"]
    assert abs(cx - 0.5) < 0.02 and abs(cy - 0.5) < 0.02, f"中心偏离({cx:.3f},{cy:.3f})"


def test_成片蓝色地形被面积上限挡住():
    rows = _fill(200, 140, DESERT)
    _paint(rows, 20, 20, 120, 120, SHIELD_ICON)  # 100x100=10000 px 的大色块
    assert shield_candidates(_png(rows)) == [], "大色块是地形，不是图标"


def test_候选按离画面中心排序():
    rows = _fill(400, 260, DESERT)
    _paint(rows, 20, 20, 32, 32, SHIELD_ICON)        # 远离中心
    _paint(rows, 198, 128, 210, 140, SHIELD_ICON)    # 靠近中心
    cands = shield_candidates(_png(rows))
    assert len(cands) == 2
    d0 = (cands[0]["cx"] - .5) ** 2 + (cands[0]["cy"] - .5) ** 2
    d1 = (cands[1]["cx"] - .5) ** 2 + (cands[1]["cy"] - .5) ** 2
    assert d0 <= d1, "应按离中心由近到远排序（地图开在角色处，最近点最可能是附近传送点）"


def test_黄色按钮判据():
    assert is_teleport_yellow(*TELEPORT_YELLOW)
    assert not is_teleport_yellow(120, 120, 200), "蓝色按钮不该被判成黄色"
    assert not is_teleport_yellow(200, 200, 200), "灰白不该被判成黄色"


def test_只挑底部宽扁按钮():
    png = _png(_fill(400, 300, (0, 0, 0)))
    blobs = _blobs(png, lambda r, g, b: False, min_area=1)
    assert pick_bottom_button(blobs) is None, "没有黄色块时应返回 None"

    rows = _fill(400, 300, (0, 0, 0))
    _paint(rows, 120, 265, 280, 285, TELEPORT_YELLOW)  # 底部宽扁条
    _paint(rows, 20, 20, 60, 60, TELEPORT_YELLOW)      # 上方块状，不该被选
    blobs = _blobs(_png(rows), is_teleport_yellow, min_area=100, join_diag=False)
    btn = pick_bottom_button(blobs)
    assert btn is not None, "应找到底部宽扁按钮"
    assert btn["cy"] > 0.8, f"选中的按钮 cy={btn['cy']:.2f}，应在底部"
    assert btn["w"] > 0.3, f"选中的按钮宽={btn['w']:.2f}，应是宽条"


def main():
    tests = [(n, f) for n, f in sorted(globals().items()) if n.startswith("test_")]
    failed = 0
    for name, fn in tests:
        try:
            fn()
            print(f"  ok   {name}")
        except AssertionError as e:
            failed += 1
            print(f"  FAIL {name}: {e}")
    print(f"\n{len(tests) - failed}/{len(tests)} 通过")
    return 1 if failed else 0


if __name__ == "__main__":
    sys.exit(main())
