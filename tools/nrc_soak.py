#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""nrc_soak.py —— 洛克王国:世界 真机多轮巡检（SOAK）。

目的：不是「能不能做一次」，而是「连着跑 N 轮稳不稳、每步要等多久」。
把图标库 + 点击链路放进真实操作闭环里反复压，产出一份可复算的稳定性报告。

一轮做什么（刻意只用**已验证安全**的动作，不破坏游戏状态）：

  1) 抓帧                      —— 量抓帧耗时（真机流畅度指标）
  2) 全库定位（24 条模板）      —— 量「定位」这一步命中几条、耗时多少
  3) 点「眠枭庇护所(地图UI)」   —— 固定 UI、点开区域进度面板、右上 ✕ 无损可关
  4) 抓帧验证反应              —— 帧差 + OCR 新增文字，判 panel/blank/none
  5) 复位                      —— OCR 状态机：每点一次 ✕ 就抓帧判态，回到 map 即停

⚠️ **点击一律「点一次 → 抓帧 → 没变化才补点」**，绝不盲点两次：这个图标是
   **toggle**（点一次开面板、再点一次关面板），盲点两次会「开了又关」、
   逐像素回到原状（帧差 0.00），看起来跟"点不动"一模一样。详见 tap_once_verified。

⚠️ 只点这一个图标是有意的：家园会切地图层级、皮卡月刊是资讯入口，
   它们的退出路径未经实测，拿它们做压测会把「链路不稳」和「状态被搞乱」混在一起。

用法：
  python tools/nrc_soak.py --rounds 10                 # 默认 10 轮
  python tools/nrc_soak.py --rounds 3 --dry-run        # 只抓帧+定位，不点击
  python tools/nrc_soak.py --rounds 10 --out .workbuddy/evidence/nrc/20260921-soak
"""

import argparse
import atexit
import json
import os
import re
import shutil
import statistics
import struct
import subprocess
import sys
import threading
import time

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
PY = sys.executable
DRV = os.path.join(ROOT, ".workbuddy", "nrc", "drv.py")
ICON_MATCH = os.path.join(ROOT, "tools", "icon_match.py")
OCR_EXE = os.path.join(ROOT, ".workbuddy", "bin", "ocr.exe")
SHOTDIR = os.path.join(ROOT, ".workbuddy", "tmp", "screenshots", "nrc-20260918")
# 过渡帧样本目录（坑⑥ 现场那类帧：地图文字还在、图标已消失 ⇒ OCR 会判 map）。
# 复位确认分支一旦判出"隔一帧后已不是 map"，就顺手把那一帧存到这里 —— 帧已在盘上，
# **零额外成本**；攒够就能补进离线回归集（tools/nrc_state_regress.py 里那一格还空着）。
TRANS_DIR = os.path.join(ROOT, ".workbuddy", "evidence", "nrc", "regress-frames", "candidates")
# 点击直连 adb：少一层 python 进程、更快，也让耗时统计干净（见 tap_px 注释）
ADB = os.environ.get("NRC_ADB",
                     r"C:/Users/ay/AppData/Local/Android/Sdk/platform-tools/adb.exe")
SERIAL = os.environ.get("NRC_SERIAL", "ecbff3a5")

# 跑批点击目标：全部是**固定 UI**（钉死坐标、不随视口移动），退出路径已逐个实测。
# 依据 docs/nrc-icon-atlas-method.md §5.9 的「多图标反应/退出对照」：
#
#   目标                   点击反应          实测帧差   退出方式
#   眠枭庇护所(地图UI)      区域进度面板       18.4      ✕ ×1
#   皇家招待所             标记编辑态         21.8      ✕ ×1
#   家园                  家园信息浮层       46.4      ✕ ×1
#   皮卡月刊              只有名字标签        2.04      再点同位置一次
#
#   exit   "close" = 点右上 ✕ 一次退出；"retap" = 再点同位置一次退出
#          ⚠️ 皮卡月刊必须用 retap —— 它是 toggle 名字标签（不是按钮），
#          在标签态点 ✕ 会把地图一起关掉、落回大世界（实测）。
#   min_diff 判"确实有反应"的帧差门槛：面板类 8.0；标签类特征就是小（实测 2.04），
#          若沿用 8.0 会被误判成"点了没反应"。
#   verify   点击后是否做"首点被吞就补点一次"（默认 True）。**标签类必须 False** ——
#          它的反应只有 2.04，补点门槛一过就把标签收回去，最终帧差 0.00、判成"无反应"。
TARGETS = [
    {"name": "眠枭庇护所(地图UI)", "exit": "close", "min_diff": 8.0, "expect": "区域进度面板"},
    {"name": "皇家招待所", "exit": "close", "min_diff": 8.0, "expect": "标记编辑态"},
    {"name": "家园", "exit": "close", "min_diff": 8.0, "expect": "家园信息浮层"},
    {"name": "皮卡月刊", "exit": "retap", "min_diff": 1.5, "verify": False,
     "expect": "名字标签"},
]
TAP_TARGET = TARGETS[0]["name"]        # 兼容：单目标模式下默认点第一个
# 地图界面右上角 ✕（点一次即退出面板/标记态；连点两次会连地图一起关掉、落回大世界）
CLOSE_X, CLOSE_Y = 2169, 49
# 大世界里开地图的右上导航圆钮（单点即可，被吞再点）
NAV_X, NAV_Y = 2127, 150
# 面板弹出判据：帧差阈值（点开区域进度面板实测远大于此）
PANEL_DIFF = 8.0
# 「首点被吞」判据：点击后帧差低于此值 ⇒ 认为这一下没生效，补点一次
SWALLOW_DIFF = 3.0
# 「同一视口、同一界面」判据（用于"是否已回到地图"）：比 SWALLOW_DIFF 严得多，
# 真回到同一张地图时实测帧差 0.0，而"地图+标记点"这类近似画面必须排除
MAP_SAME_DIFF = 1.0
# 单次点击后的等待（毫秒）：面板弹出实测 <1s，留 1.2s 余量
TAP_WAIT = 1200
# 抓帧兜底（2026-09-21 长测踩到）：单次抓帧正常 ~2.1s，实测偶发一次 **39.2s**
# （adb/设备层卡顿），而且那帧内容不可信（帧差 60.64 vs 同目标其他轮 30.98
# —— 慢帧往往抓在**画面过渡态**上）。不加兜底会直接把"点击反应"判歪。
SHOT_TIMEOUT = 60       # 单次抓帧子进程超时（秒）；正常 2s，给 30 倍余量
SHOT_SLOW_MS = 8000     # 超过此耗时 ⇒ 判为可疑慢帧，丢弃重抓。
#                         两条通道的正常值都远低于它：raw ~1.3s、PNG 回落 ~3.5s
#                         ⇒ 8000 对两者都是"纯保险"线，不用跟着通道调。
SHOT_MAX_TRY = 2        # 最多抓几次（1 次正常 + 1 次重抓）
# 抓帧通道（2026-09-21 新增）：
#   "raw" = `exec-out screencap`（**不带 -p**）取 RGBA 原始缓冲，本地编码 PNG；
#   "png" = 旧路径（`screencap -p`，设备端 PNG 压缩；失败还会回落 drv.py shotq）。
# ⚠️ 为什么改 raw —— 瓶颈是**设备端 PNG 压缩**，不是链路、也不是传输量。
#    同一静止画面实测（MIX3 2340×1080）：
#      shell echo                   86 ms                    ← 链路本身不慢
#      exec-out screencap -p      3468 / 3724 ms   2.93 MB   ← 2.3s 花在设备 CPU 编码
#      exec-out screencap (raw)   1166 / 1278 ms   9.64 MB   ← 传 3 倍量反而快 3 倍
#      shell screencap + pull     4515 + 714 ms              ← 更慢（多写一次文件）
#    **等价性已验到像素级**：raw 帧与 PNG 帧的逐像素帧差 = **0.0**
#    （同通道两次抓帧的噪声基线同样 0.0 ⇒ 不是"差异小"，是完全一致）。
SHOT_CHANNEL = "raw"
# 地图界面的「固定 UI 锚点」：这两条同时命中 ⇒ 必在地图界面（免整帧 OCR 判态）
# 依据：icon_crossframe.py 实测二者都是多帧同坐标的固定 UI（家园 (2152,1029)、
# 眠枭庇护所(地图UI) (188,187)），且只在地图界面存在。
MAP_ANCHORS = {"家园", "眠枭庇护所(地图UI)"}


def run(args, timeout=180):
    p = subprocess.run(args, cwd=ROOT, capture_output=True, timeout=timeout)
    out = p.stdout.decode("utf-8", "replace")
    err = p.stderr.decode("utf-8", "replace")
    return p.returncode, out, err


def _grab_raw(tag):
    """raw 通道抓帧：`exec-out screencap`（**不带 -p**）取原始缓冲，本地存成 PNG。

    返回帧路径；返回 None 表示这条通道这次不可用（调用方回落 PNG 通道）。

    帧布局（Android screencap 原生输出，小端）：
        16 字节头 = width, height, format, colorspace
        之后 = width*height*4 字节 RGBA8888（format=1）

    ⚠️ **长度必须精确等于 16+w*h*4**：不等就说明是半截或 CRLF 污染，宁可判失败走
    回落，也不能把错位数据当帧 —— 那会让后面所有判据建在垃圾上（同"旧帧冒充新帧"的病根）。
    Windows 下实测这条通道是二进制安全的（len 精确匹配、像素与 PNG 通道逐点全同），
    但仍保留这条长度校验，因为它同时兜住"设备换分辨率""fmt 变更"两类变化。

    ⚠️ 本地重新编码 PNG 是**刻意的**：判据侧（diff_two / 定位 / OCR）都按 *.png 读，
    保持"抓帧产物就是 PNG"这条不变，等于**判据口径零改动**，只有取帧方式变了。
    """
    path = os.path.join(SHOTDIR, tag + ".png")
    try:
        p = subprocess.run([ADB, "-s", SERIAL, "exec-out", "screencap"],
                           capture_output=True, timeout=SHOT_TIMEOUT)
    except (subprocess.TimeoutExpired, OSError):
        return None
    raw = p.stdout
    if not raw or len(raw) < 16:
        return None
    try:
        w, h, fmt, _cs = struct.unpack("<4I", raw[:16])
    except struct.error:
        return None
    if fmt != 1 or w <= 0 or h <= 0 or len(raw) != 16 + w * h * 4:
        return None
    try:
        import numpy as np
        from PIL import Image
    except Exception:
        return None
    try:
        arr = np.frombuffer(raw[16:], dtype=np.uint8).reshape(h, w, 4)
        Image.fromarray(arr, "RGBA").save(path)
    except Exception:
        return None
    return path


def shot(tag, rec):
    """抓一帧，返回帧绝对路径。顺带记录耗时（真机流畅度指标）。

    取帧有两条通道（见 SHOT_CHANNEL）：默认 **raw**（快 ~2s/帧），失败回落
    drv.py shotq（PNG 通道，内部还有 exec-out -p → shell+pull 两级兜底）。
    无论走哪条，产物都是 `<tag>.png` ⇒ **下游判据完全不感知通道差异**。

    用 drv.py 的 shotq（只抓帧、不跑 judge）：一审 judge 要读图算三个指标，
    本轮巡检并不需要界面判断（而且 judge 对地图界面本来就误判成 WORLD）。

    ⚠️ **超时 + 慢帧重抓**（2026-09-21 长测踩到，见 SHOT_* 常量注释）：
      16 轮里有一次"点击后抓帧"花了 39.2s（同轮其他抓帧 2.1s），且那帧
      帧差 60.64、与同目标其他轮的 30.98 明显不同 —— 慢帧通常抓在画面
      过渡态上，内容不可信，会把"点击反应"的判定直接带偏。
    记账口径：**最终采用那次**的耗时进 shot_ms（汇总统计不变）；
    被丢弃的慢帧/失败尝试进 shot_retry_ms；重抓次数累加进 shot_tries。
    """
    path = None
    last_err = ""
    for attempt in range(1, SHOT_MAX_TRY + 1):
        t0 = time.time()
        cand = None
        if SHOT_CHANNEL == "raw":
            cand = _grab_raw(tag)
            if cand is None:
                # 记一笔回落次数：raw 通道若在某台设备上不适用，这个计数会立刻暴露
                rec["shot_fallback"] = rec.get("shot_fallback", 0) + 1
        if cand is None:
            # 回落旧通道：drv.py shotq（内部还有 exec-out -p → shell+pull 两级兜底）
            try:
                rc, out, err = run([PY, DRV, "shotq", tag], timeout=SHOT_TIMEOUT)
            except subprocess.TimeoutExpired:
                rc, out, err = -1, "", "抓帧子进程超时 >%ds" % SHOT_TIMEOUT
            m = re.search(r"([A-Za-z]:[^\s]*%s\.png)" % re.escape(tag), out)
            if m:
                cand = m.group(1)
            if cand is None or not os.path.exists(cand):
                c2 = os.path.join(SHOTDIR, tag + ".png")
                if os.path.exists(c2):
                    cand = c2
            if cand is None or not os.path.exists(cand):
                last_err = "rc=%d out=%r err=%r" % (rc, out[:200], err[:200])
        dt_ms = round((time.time() - t0) * 1000)
        if cand is None or not os.path.exists(cand):
            rec.setdefault("shot_retry_ms", []).append(dt_ms)
            continue
        # ⚠️ 新鲜度校验（离线单测暴露的真 bug）：SHOTDIR/<tag>.png 是**同名复用**的，
        # 抓帧失败时上一轮的旧帧还在那里 ⇒ 兜底会把它当成本次结果返回，
        # 于是"抓帧失败"被静默变成"拿到旧帧"（本轮点击/复位的判据就全建在旧画面上）。
        # 判据：文件必须比本次抓帧开始时间新。
        if os.path.getmtime(cand) < t0 - 0.5:
            last_err = "拿到旧帧（mtime 早于本次抓帧）：%s" % cand
            rec.setdefault("shot_retry_ms", []).append(dt_ms)
            continue
        # 慢帧：很可能抓在过渡态，丢弃重抓（最后一次仍慢就只能接受）
        if dt_ms > SHOT_SLOW_MS and attempt < SHOT_MAX_TRY:
            rec.setdefault("shot_retry_ms", []).append(dt_ms)
            rec["shot_slow"] = rec.get("shot_slow", 0) + 1
            # 把慢帧挪到 .png.slow，别让它继续占着 <tag>.png：
            # 否则下一次抓帧失败时，兜底会把这个被丢弃的慢帧当成本次结果返回。
            # 留 .slow 后缀是刻意的——不匹配 *.png，不会被当成正常帧，也便于事后查证。
            try:
                os.replace(cand, cand + ".slow")
            except OSError:
                pass
            continue
        path = cand
        rec.setdefault("shot_ms", []).append(dt_ms)
        rec["shot_tries"] = rec.get("shot_tries", 0) + attempt
        break
    if path is None or not os.path.exists(path):
        raise RuntimeError("抓帧失败 tag=%s 试了 %d 次 %s" % (tag, SHOT_MAX_TRY, last_err))
    return path


def locate(frame, rec, min_score=0.85, record=True):
    """全库定位：返回 {名称: (分数, x, y)}。记录耗时与命中数。

    `record=False` 专供**起点守卫的提前定位**：那次结果若被本轮复用，由调用方按
    "复用"口径记账；若因复位作废，它的耗时就不该算进本轮（会虚增定位开销）。
    """
    t0 = time.time()
    rc, out, err = run([PY, ICON_MATCH, "--find-all", "--shot", frame,
                        "--at", "--min-score", str(min_score)])
    dt = time.time() - t0
    if record:
        rec.setdefault("locate_ms", []).append(round(dt * 1000))
    hits = {}
    for line in out.splitlines():
        line = line.strip()
        # ⚠️ --at 的一行是 5 段：「名称 分数 x y WxH」（末尾还有尺寸段）。
        #    按 4 段 rsplit 会把分数切进名称里、整行被当解析失败丢掉（实测全库 0 命中就是这么来的）。
        #    无命中时输出「<名称> MISS」（MISS 在行尾，不在行首）。
        if not line or line.endswith(" MISS"):
            continue
        parts = line.rsplit(" ", 4)
        if len(parts) != 5:
            continue
        name, score, x, y, _size = parts
        try:
            hits[name] = (float(score), int(x), int(y))
        except ValueError:
            continue
    return hits, dt


# 帧级 OCR 缓存：同一张帧只识别一次。
#
# 为什么需要：整帧 OCR 实测 4.1s（2340x1080），而单轮里同一帧会被问多次
# （detect_state 判态要文字、before_txt 要文字）。缓存后重复问等于零成本。
# 键含文件大小与 mtime：抓帧会**覆盖同名文件**（reset_probeNN 之类），
# 只按路径做键会把旧结果当成新帧的，属于"自证式"错误。
_OCR_CACHE = {}


def _cache_key(frame, min_conf):
    try:
        st = os.stat(frame)
        return (os.path.abspath(frame), int(st.st_mtime_ns), st.st_size, min_conf)
    except OSError:
        return (os.path.abspath(frame), 0, 0, min_conf)


def ocr_lines(frame, min_conf=0.30):
    """返回 [(x, y, 文本)]；OCR 不可用时返回空表（不阻断巡检）。带帧级缓存。"""
    key = _cache_key(frame, min_conf)
    if key in _OCR_CACHE:
        return _OCR_CACHE[key]
    lines = ocr_lines_many([frame], min_conf).get(os.path.abspath(frame), [])
    _OCR_CACHE[key] = lines
    return lines


# ---- 常驻 OCR 通道（ocr.exe -serve）----
#
# 为什么要常驻：`ocr.exe -in a -in b` 能把**一次调用内**的多张图合并（省一次冷启动），
# 但**跨调用**的冷启动省不掉 —— 每轮跑批各调一次，就等于每轮白付 2.2s。
# 实测同一台机器（2340x1080 帧）：
#
#     CLI 两次独立调用   = 8.28s（两次冷启动 + 两次识别）
#     CLI 一次调用两张   = 6.06s（一次冷启动 + 两次识别）
#     常驻模式两张       = 约 4.5s（整批只付一次冷启动；单帧纯识别 2.25s）
#
# 协议是 JSON 行：{"file":...} → {"file":...,"items":[...]}，见 `cmd/ocr -serve`。
#
# 失败策略：进程死了/管道断了就把它标死并**回落 CLI**，不让 OCR 成为单点。
_SERVER = None
_SERVER_DEAD = False
# 通道是**单条** stdin/stdout 协议：两个线程同时发请求会把请求行与应答行交错，
# 拿到的是别人的结果。预热线程与主线程都要用它，必须串行化。
_SERVER_LOCK = threading.Lock()


def _server():
    """懒启动常驻通道；不可用返回 None（调用方回落 CLI）。"""
    global _SERVER, _SERVER_DEAD
    if _SERVER is not None or _SERVER_DEAD:
        return _SERVER
    if not os.path.exists(OCR_EXE):
        _SERVER_DEAD = True
        return None
    try:
        _SERVER = subprocess.Popen(
            [OCR_EXE, "-serve"],
            stdin=subprocess.PIPE, stdout=subprocess.PIPE,
            stderr=subprocess.DEVNULL,
        )
    except Exception:
        _SERVER, _SERVER_DEAD = None, True
    return _SERVER


def close_server():
    """跑批结束时收掉常驻进程（不收会一直占着模型内存）。"""
    global _SERVER, _SERVER_DEAD
    p = _SERVER
    _SERVER, _SERVER_DEAD = None, True
    if p is None:
        return
    try:
        p.stdin.write(b'{"cmd":"exit"}\n')
        p.stdin.flush()
        p.wait(timeout=10)
    except Exception:
        try:
            p.kill()
        except Exception:
            pass


def _ask_server(frames, min_conf):
    """走常驻通道逐帧识别。返回 {绝对路径: lines}；通道不可用时返回 None。

    ⚠️ min_conf 在这边过滤：`-serve` 协议固定回全部结果（引擎侧阈值 0），
    过滤交给调用方 —— 这样同一个常驻进程能服务不同阈值的调用。
    """
    p = _server()
    if p is None:
        return None
    got = {}
    # 上锁后**再查一次缓存**：等锁期间别的线程（如预热线程）可能已经把同一帧
    # 算完了，这时直接取结果，不必再发一次请求。
    with _SERVER_LOCK:
        for f in frames:
            ap = os.path.abspath(f)
            key = _cache_key(f, min_conf)
            if key in _OCR_CACHE:
                got[ap] = _OCR_CACHE[key]
                continue
            try:
                p.stdin.write((json.dumps({"file": ap}) + "\n").encode("utf-8"))
                p.stdin.flush()
                line = p.stdout.readline()
            except Exception:
                line = b""
            if not line:
                # 进程没了或协议断了：剩下的帧交回调用方走 CLI，并把通道标死，
                # 免得后面每帧都白等一次。
                close_server()
                return None
            try:
                res = json.loads(line.decode("utf-8"))
            except Exception:
                continue
            lines = []
            for it in res.get("items") or []:
                if float(it.get("score", 1.0)) < min_conf:
                    continue
                lines.append((int(it.get("cx", 0)), int(it.get("cy", 0)), it.get("text", "")))
            got[ap] = lines
            _OCR_CACHE[key] = lines
    return got


def _ocr_cli(frames, min_conf):
    """回落路径：`ocr.exe -json -in a -in b`，一次调用识别多帧。"""
    out_map = {}
    args = [OCR_EXE, "-json", "-min", str(min_conf)]
    for f in frames:
        args += ["-in", f]
    try:
        rc, out, err = run(args, timeout=300)
        data = json.loads(out)
    except Exception:
        data = []
    for res in data:
        lines = []
        for it in res.get("items") or []:
            lines.append((int(it.get("cx", 0)), int(it.get("cy", 0)), it.get("text", "")))
        ap = os.path.abspath(res.get("file", ""))
        out_map[ap] = lines
        _OCR_CACHE[_cache_key(res.get("file", ""), min_conf)] = lines
    return out_map


def ocr_lines_many(frames, min_conf=0.30):
    """拿多帧的文字，返回 {绝对路径: [(x, y, 文本)]}。优先常驻通道，失败回落 CLI。"""
    frames = [f for f in frames if f]
    out_map = {}
    if not frames:
        return out_map
    # 先分流：已缓存的直接取，只把没算过的送进引擎（去重）
    todo, seen = [], set()
    for f in frames:
        ap = os.path.abspath(f)
        if ap in seen:
            continue
        seen.add(ap)
        key = _cache_key(f, min_conf)
        if key in _OCR_CACHE:
            out_map[ap] = _OCR_CACHE[key]
        else:
            todo.append(f)
    if todo:
        got = _ask_server(todo, min_conf)
        if got is None:
            got = _ocr_cli(todo, min_conf)
        out_map.update(got)
    # 补上没回结果的帧（识别失败等），保证调用方总能拿到键
    for f in frames:
        out_map.setdefault(os.path.abspath(f), [])
    return out_map


def ocr_texts(frame, min_conf=0.30):
    """返回帧上识别到的文字集合（用于判界面态与"有没有新文字"）。"""
    return {t for _x, _y, t in ocr_lines(frame, min_conf)}


def detect_state(frame, hits=None, ref=None):
    """判界面态。返回 map / panel / home_panel / marker_edit / world / unknown。

    为什么不用像素判据：drv.py 的 judge 在**地图界面**会误报成 WORLD
    （它只看右上亮像素，而地图界面右上同样是大片亮区，实测多次踩到）。
    界面态只能靠语义特征收口。

    判据来自 09-21 实测（同一设备同一分辨率，逐帧 OCR 对照）：
      map         含「精灵踪迹」或「卡洛西亚大陆」（地图界面固有元素）
      panel       含「风眠省」/「15/15」——点「眠枭庇护所(地图UI)」弹出的区域进度面板
      home_panel  含「舒适度」/「当前居住精灵」/「当前种植植物」——家园信息浮层
      marker_edit 含「标记（点击修改名称）」——误入标记编辑态时的提示语
      world       含「触碰」——野外落地才有的交互按钮

    ⚠️ **判据顺序 = 先"叠加层"后"底层"**（09-21 异常起点跑批踩到，很重要）：
      「家园信息浮层」是**盖在地图上**的浮层，地图固有词（「卡洛西亚大陆」×2、
      「精灵踪迹 11/14」）**依然在屏** ⇒ 靠 map 判据**根本分不开**。
      实测对照（同一批帧逐词 OCR）：
                      卡洛西亚大陆  精灵踪迹  舒适度  当前居住精灵  当前种植植物
        干净地图          ✓ ×2        ✓ 11/15    ✗        ✗            ✗
        家园浮层          ✓ ×2        ✓ 11/14    ✓        ✓            ✓
      ⇒ 后果不是"判错一次"，而是**连锁假绿**：起点守卫判 map ⇒ 不救；
        复位探测帧若落在浮层上 ⇒ 也判 map ⇒ **复位假成功**；而目标「家园」图标
        正被浮层盖住 ⇒ 未在屏 ⇒ 未决。整套指标里只有「目标在屏」露马脚，
        **成功率照样 100%**（未决不计入分母）。
      ⇒ 修法：浮层/编辑态这类**叠加层判据必须排在 map 之前**（marker_edit、panel
        本来就是这么排的，当时漏了家园浮层这一种）。

    ⚡ 两条**免 OCR 快路径**（整帧 OCR 实测 3.6s，是单轮最大的单项开销）：
      1) 传入 ref（一张已确认是地图的参考帧）且与它逐像素接近 ⇒ 地图界面。
         复位最后一步"回到 map"正是这种情况，帧差 ≈0，不必再 OCR 一次。
         ⚠️ 这条**可靠**（叠加层会让帧差变大），所以排在 OCR 之前。
      2) 传入 hits（本轮全库定位结果）里的两个固定 UI 锚点 ⇒ 像素级证据。
         ⚠️ 但它**只能当"必要条件"、不能当"充分条件"**，必须排在叠加层判据**之后**：
         区域进度面板打开时，地图左侧的「家园」「眠枭庇护所(地图UI)」**仍在屏**，
         锚点照样齐（2026-09-21 实测 panel 态命中 9 条、两个锚点都在）
         ⇒ 若把锚点快路径放最前，panel 会被判成 map（我当天就这么错了一次）。
         反过来，"OCR 见到地图词"也不充分（大世界 / 家园场景 / **地图关闭过渡帧**都有）。
         ⇒ 所以只有"锚点齐"才是可靠的地图证据；锚点不齐就退 unknown，交守卫复位。
    两条都不成立才落回 OCR 语义判态（宁可慢，不能误判）。
    """
    if ref is not None:
        d = diff_two(ref, frame)
        # 阈值用 MAP_SAME_DIFF(1.0) 而不是 SWALLOW_DIFF(3.0)：这里要的是
        # "**同一视口、同一界面**"（真回到地图时帧差实测 0.0）。放宽到 3.0 会把
        # "地图 + 几个标记点"这类近似画面（如标记编辑态）也放进来 —— 那已经不是
        # 干净的地图界面了。宁慢不错。
        if d is not None and d < MAP_SAME_DIFF:
            return "map"
    txt = ocr_texts(frame)
    joined = " ".join(txt)
    # ⚠️ 叠加层判据必须排在"地图"判据之前：它们都**盖在地图上**，地图固有词仍在屏。
    if "触碰" in joined:
        return "world"
    # ⚠️ 长词不够用：OCR 会把「点击修改名称」认成「点击修破名称」
    #（2026-09-21 离线回归集 s_now5_panel.png 实测：命中 0 条 ⇒ 漏判成 unknown）。
    # 「常规标记数」是标记界面独有词，与长词互补，一起用才收得住。
    if ("标记（点击修改名称）" in joined or "标记(点击修改名称)" in joined
            or "常规标记数" in joined):
        return "marker_edit"
    if "风眠省" in joined or "15/15" in joined:
        return "panel"
    # 这三个词是家园浮层独有（干净地图实测一个都不出现）。
    if "舒适度" in joined or "当前居住精灵" in joined or "当前种植植物" in joined:
        return "home_panel"
    # 到了这里才判"是不是地图"：有 hits 就**以锚点为准**（必要条件，见 docstring）。
    if hits is not None:
        return "map" if MAP_ANCHORS.issubset(hits) else "unknown"
    if "精灵踪迹" in joined or "卡洛西亚大陆" in joined:
        return "map"
    return "unknown"


def _keep_transition_sample(frame, rec):
    """把"被判成 map、隔一帧却不是 map"的帧存成过渡帧候选（坑⑥ 现场）。

    零额外成本（帧已经在盘上）。目的：那类帧一直没存档 ⇒ 离线回归集里
    「地图关闭过渡帧」这一格始终空着；跑批顺手攒样本，攒到就补进 CASES。
    """
    try:
        os.makedirs(TRANS_DIR, exist_ok=True)
        dst = os.path.join(TRANS_DIR, "transition_%s.png" % time.strftime("%Y%m%d-%H%M%S"))
        shutil.copyfile(frame, dst)
        rec.setdefault("transition_samples", []).append(os.path.basename(dst))
    except Exception:
        pass


def reset_to_map(rec, max_attempts=5, verbose=True, ref=None,
                 initial=None, initial_state=None, tag_prefix=None):
    """OCR 驱动的状态机复位：把界面稳定拉回「地图界面」。

    ⚠️ 这是本轮踩出来的坑，别再盲点两次 ✕：
      ARENA 记的「面板态分层关闭：第 1 次关面板、第 2 次关地图」是**真的**，
      所以连点两次 ✕ 会把地图也关掉、人落回**大世界**（实测复位后 OCR 出现
      「前往魔法师之家 / 触碰」），下一轮自然就"目标未在屏"。
      正确做法是**每点一次就抓帧判态**：面板/标记态 → 点一次 ✕；
      已经在大世界 → 点右上导航钮开地图；回到 map 就停。

    两个省时参数（不改判据，只省重复劳动）：
      initial / initial_state —— 复用「刚点击完抓的那一帧」及其状态。那一帧
        和复位第 1 步要抓的帧是同一画面，没必要再抓一次（省 1.9s）。
      ref —— 一张已确认是地图的参考帧（本轮 base）。复位末步"回到地图"时
        与它帧差 ≈0 ⇒ detect_state 走快路径，省一次整帧 OCR（3.6s）。

    ⚠️ **tag_prefix 必须传"本轮专属"的前缀**（如 `soak03_reset`）。
    帧名默认是 `reset_probeNN`，**不带轮次**⇒ 起点守卫与本轮复位会共用同一批
    文件名，后者抓帧会**覆盖前者**。而起点守卫常把"它判为 map 的那一帧"当作
    本轮的 base（ref）—— 一旦被覆盖，`detect_state(f, ref=base)` 就成了
    **同一个文件自己跟自己比 ⇒ 帧差恒 0 ⇒ 永远判"已回到地图"**：
    复位假成功、错误状态还被下一轮继承（自证式判据，2026-09-21 实测踩到）。
    命名隔离是第一道防线，diff_two 的"同文件返回 None"是第二道。
    """
    tag_prefix = tag_prefix or "reset"
    steps = []
    cur_f, cur_st = initial, initial_state
    f = None
    for i in range(max_attempts):
        if cur_f is not None:
            f = cur_f
            st = cur_st if cur_st else detect_state(f, ref=ref)
            cur_f, cur_st = None, None            # 只在第一步复用一次
        else:
            f = shot("%s_probe%02d" % (tag_prefix, i), rec)
            st = detect_state(f, ref=ref)
        steps.append(st)
        if st == "map":
            # ⚠️ 没有 ref 快路径时，"判 map"必须**再确认一帧**（2026-09-21 午踩到）。
            # 起点守卫那次调用**不传 ref**（起点界面未知，没有可信的地图参考帧），
            # 于是"是否回到地图"只能靠 OCR。而**地图正在关闭的过渡帧**里地图元素还在
            # （含「卡洛西亚大陆」「精灵踪迹」），只是图标已经消失 ⇒ OCR 照判 map
            # ⇒ **复位假成功**：那一轮定位只命中 7 条（正常 20~21 条）、且不含两个固定 UI
            # 锚点 ⇒ 目标"未在屏"⇒ 未决，而成功率照样显示 100%。
            # 过渡态隔 0.6s 必然继续变化 ⇒ "再抓一帧仍判 map"才算数。
            if ref is None:
                f2 = shot("%s_confirm%02d" % (tag_prefix, i), rec)
                if detect_state(f2) != "map":
                    steps[-1] = "map_unstable"      # 留痕：这一步曾误判成 map
                    _keep_transition_sample(f, rec)  # 顺手留样本（零成本）
                    continue
                f = f2
            rec["reset_attempts"] = i
            rec["reset_state_path"] = steps
            return True, f, steps
        if st == "world":
            tap_px(NAV_X, NAV_Y, 1500)            # 开地图（单点，被吞再点）
        else:                                      # panel / home_panel / marker_edit / unknown
            tap_px(CLOSE_X, CLOSE_Y, 900)          # 只点一次，点完立刻重判
    rec["reset_attempts"] = max_attempts
    rec["reset_state_path"] = steps
    return False, f, steps


def diff_two(a, b):
    """两帧灰度降采样后的平均绝对差（与 drv.py diff 同口径，这里自己算省一次进程）。

    ⚠️ 同一个文件返回 **None**（不是 0.0）。这是 2026-09-21 用血的教训换来的：
    复位过程的抓帧曾与基准帧**同名**（都叫 reset_probeNN），于是"把复位帧与
    基准帧比帧差"实际是**自己跟自己比 ⇒ 恒 0** ⇒ 帧差快路径永远判"已回到地图"，
    复位假的成功、错误状态还被下一轮当基准继续沿用（自证式判据）。
    返回 None 让调用方走真判据（OCR），宁慢不错。
    """
    try:
        if os.path.abspath(a) == os.path.abspath(b):
            return None
    except Exception:
        pass
    try:
        from PIL import Image
        import numpy as np
    except Exception:
        return None
    try:
        ia = Image.open(a).convert("L").resize((320, 148))
        ib = Image.open(b).convert("L").resize((320, 148))
        return float(np.abs(np.asarray(ia, dtype=float) - np.asarray(ib, dtype=float)).mean())
    except Exception:
        return None


def tap_px(x, y, wait_ms=1200):
    """点击一次（直连 adb）。

    直连 adb 而非经 drv.py 中转，理由只是「少一层进程、更快、耗时统计干净」。
    ⚠️ 这里曾写过一个**错误结论**并被我照着改了三处代码，一并更正：
       当时观察到「python 内 subprocess 调 drv.py 点不动、命令行直接调就有效」，
       于是推断"沙箱会静默拦掉经 ≥2 层 python 的 input 注入"。
       **这个推断是错的**。真相是那批对照实验里"有效"和"无效"两次的
       **点击次数不同**（一次 vs 两次），而这个图标是 toggle：
       点一次开面板、再点一次关面板 ⇒ 盲点两次 = 开了又关 = 帧差 0.00。
       判定实验（同一脚本内交替点，见 tap_min3.py）：探针点地图中部 23.19、
       目标点 (188,187) 18.42、再点一次又 18.42（与起点同画面）——链路一直是好的。
    ⇒ 教训：拿"点不动"当结论前，先固定**点击次数**做对照；别用跨状态、
      跨次数的观察去支撑因果推断。
    """
    devnull = open(os.devnull, "wb")
    try:
        subprocess.run([ADB, "-s", SERIAL, "shell", "input", "tap",
                        str(int(x)), str(int(y))], stdout=devnull, stderr=devnull)
    finally:
        devnull.close()
    time.sleep(wait_ms / 1000.0)
    return True


def tap_once_verified(x, y, base, tag, rec, wait_ms=TAP_WAIT,
                      min_diff=SWALLOW_DIFF, verify=True):
    """点一次 → 抓帧；只有「几乎没变化」才补点一次 → 再抓帧。

    返回 (帧路径, 帧差, 点击次数)。帧差以 `base` 为参照。

    为什么不能盲点两次：目标图标是 toggle。盲点两次时若两次都生效 ⇒ 开+关
    ⇒ 与 base 逐像素一致（帧差 0.00），会被判成"点击无效"；若首点恰好被吞
    ⇒ 吞+开 ⇒ 面板开着（帧差 ~18），同一份代码给出两种截然相反的结果。
    自适应补点后，两种情况都收敛到"面板开着"。

    ⚠️ 补点门槛 min_diff 必须按**目标类型**给（2026-09-21 多图标跑批实测踩到）：
    「皮卡月刊」是 toggle **名字标签**，反应本身只有 2.04 —— 沿用面板类的
    3.0 门槛，会把"确实弹了标签"误判成"首点被吞"，于是补点一次正好把标签
    收回，帧差回到 0.00，最终判成"点了没反应"。**同一个 toggle 陷阱换个马甲
    又踩一次**：门槛必须与被判对象的量级匹配；量级天然很小的反应直接关掉补点
    （verify=False），别指望靠调门槛擦边。
    """
    tap_px(x, y, wait_ms)
    times = 1
    f = shot(tag + "_a", rec)
    d = diff_two(base, f)
    if verify and d is not None and d < min_diff:
        tap_px(x, y, wait_ms)
        times = 2
        f = shot(tag + "_b", rec)
        d = diff_two(base, f)
    return f, d, times


def main():
    global SHOT_SLOW_MS, SHOT_CHANNEL   # 允许命令行收紧"慢帧"阈值 / 切抓帧通道
    ap = argparse.ArgumentParser()
    ap.add_argument("--rounds", type=int, default=10, help="跑多少轮")
    ap.add_argument("--out", default=".workbuddy/evidence/nrc/20260921-soak", help="证据目录")
    ap.add_argument("--min-score", type=float, default=0.85, help="定位阈值")
    ap.add_argument("--dry-run", action="store_true", help="只抓帧+定位，不点击")
    ap.add_argument("--pre-check", action="store_true",
                    help="点击前额外抓一帧复核状态（更保险，但每轮多花 1.9s；实测漂移恒为 0）")
    ap.add_argument("--verbose", action="store_true")
    ap.add_argument("--shot-slow-ms", type=int, default=SHOT_SLOW_MS,
                    help="单次抓帧超过此毫秒数即判为慢帧、丢弃重抓（默认 %d）。"
                         "调小可**强制触发重抓**，用来验证兜底逻辑在真机上的行为"
                         "（正常抓帧 ~2.1s，调成 500 会让每帧都重抓一次）" % SHOT_SLOW_MS)
    ap.add_argument("--shot-channel", choices=("raw", "png"), default=SHOT_CHANNEL,
                    help="抓帧通道（默认 %s）。raw = exec-out screencap 取原始缓冲、"
                         "本地编码 PNG（实测快 ~2s/帧，因为瓶颈是设备端 PNG 压缩）；"
                         "png = 旧路径。两者产物都是 <tag>.png，判据口径完全一致，"
                         "留这个开关是为了 A/B 对照与快速回退。" % SHOT_CHANNEL)
    ap.add_argument("--targets", default="",
                    help="点击目标（逗号分隔的图标名）；默认全部轮换。"
                         "传单个名即退化为单目标模式。可选："
                         + " / ".join(t["name"] for t in TARGETS))
    a = ap.parse_args()

    SHOT_SLOW_MS = a.shot_slow_ms
    SHOT_CHANNEL = a.shot_channel

    # 选目标：默认全部轮换（每轮一个，按序循环）
    if a.targets.strip():
        want = [s.strip() for s in a.targets.split(",") if s.strip()]
        unknown = [w for w in want if w not in {t["name"] for t in TARGETS}]
        if unknown:
            print("未知目标：%s（可选：%s）"
                  % (", ".join(unknown), " / ".join(t["name"] for t in TARGETS)))
            return 2
        targets = [t for t in TARGETS if t["name"] in want]
    else:
        targets = list(TARGETS)
    multi = len(targets) > 1

    out_dir = os.path.join(ROOT, a.out)
    os.makedirs(out_dir, exist_ok=True)

    rec = {"rounds": [], "started": time.strftime("%Y-%m-%d %H:%M:%S"),
           "dry_run": a.dry_run, "min_score": a.min_score,
           "targets": [t["name"] for t in targets]}
    # 兼容旧字段：单目标时仍写 target，便于既有报告脚本读取
    if not multi:
        rec["target"] = targets[0]["name"]

    print("=" * 68)
    print("洛克王国:世界 真机巡检  轮数=%d%s"
          % (a.rounds, "  [DRY-RUN]" if a.dry_run else ""))
    print("点击目标：%s" % ("轮换 " + " → ".join(t["name"] for t in targets) if multi
                            else targets[0]["name"]))
    print("=" * 68)

    prev_map = None            # 上一轮复位后的地图帧：给本轮起点守卫当 ref（省一次 OCR）
    for i in range(1, a.rounds + 1):
        r = {"round": i, "t0": time.strftime("%H:%M:%S")}
        t_round = time.time()
        tgt = targets[(i - 1) % len(targets)]      # 轮换：每轮点一个目标
        tname = tgt["name"]
        r["target"] = tname
        try:
            # ---- 0) 起点守卫：不在地图界面就先复位，别让上一轮的残留状态污染本轮统计 ----
            base = shot("soak%02d_base" % i, r)
            # 判态前先定位：定位结果是判态的**像素级证据**（两个固定 UI 锚点齐 ⇒ 必在地图），
            # 比 OCR 词可靠 —— 地图的词在大世界、家园浮层、以及**地图正在关闭的过渡帧**上
            # 都可能在屏（2026-09-21 午连踩两次）。定位本来第 2 步就要做，
            # 提前到判态之前**零额外成本**，而且能与 base 的 OCR 真正并行
            # （判态用的就是这份 OCR 结果，缓存命中 ⇒ 等于把判态那段识别挪进并行窗口）。
            warm = threading.Thread(target=lambda: ocr_lines_many([base]), daemon=True)
            warm.start()
            hits0, loc_dt0 = locate(base, r, a.min_score, record=False)
            warm.join(timeout=120)
            st0 = detect_state(base, hits=hits0, ref=prev_map)
            r["start_state"] = st0
            # 原始起点态单独记：下面复位成功后会覆盖 start_state，
            # 不单独留一份就看不出"这一轮是被守卫救回来的"。
            r["start_state_raw"] = st0
            if st0 != "map":
                # ref=prev_map：起点异常时若复位回到"上一轮那同一张地图"，帧差 ≈0 免 OCR。
                ok0, base, path0 = reset_to_map(r, verbose=a.verbose, ref=prev_map,
                                                tag_prefix="soak%02d_start" % i)
                r["start_reset_path"] = path0
                st0 = detect_state(base)
                r["start_state"] = st0
                hits0 = None            # base 已换 ⇒ 起点那次定位作废
                if not ok0:
                    r["verdict"] = "UNDECIDED_NOT_ON_MAP"
                    print("  轮 %2d  起点不在地图界面（%s）且复位失败 ⇒ 未决（不计入成功率）" % (i, st0))
                    r["round_sec"] = round(time.time() - t_round, 1)
                    rec["rounds"].append(r)
                    continue

            # ---- 1) 基准帧就绪 ----
            r["base"] = os.path.basename(base)

            # ---- 2) 全库定位 ----
            # 起点就在地图 ⇒ 直接复用守卫前那次定位（零额外成本）；
            # 复位过 ⇒ base 换了，必须重定位（此时再补一次 OCR 预热）。
            if hits0 is not None:
                hits, loc_dt = hits0, loc_dt0
                # ⚠️ 记进**轮记录 r**，不是全局 rec —— locate() 的第二个形参名叫 rec，
                # 实际接的是轮记录（r），汇总按轮遍历取 locate_ms。写错对象会让
                # "平均全库定位"变成 None（2026-09-21 午踩到，12 轮白跑才发现）。
                r.setdefault("locate_ms", []).append(round(loc_dt0 * 1000))
                r["locate_reused"] = True
            else:
                warm = threading.Thread(target=lambda: ocr_lines_many([base]), daemon=True)
                warm.start()
                hits, loc_dt = locate(base, r, a.min_score)
                warm.join(timeout=120)
            r["locate_hits"] = len(hits)
            r["locate_sec"] = round(loc_dt, 2)
            r["hits"] = {k: [round(v[0], 3), v[1], v[2]] for k, v in sorted(hits.items())}
            if a.verbose:
                for k, v in sorted(hits.items()):
                    print("      %-22s %.3f @(%d,%d)" % (k, v[0], v[1], v[2]))

            ok_locate = tname in hits
            r["target_found"] = ok_locate

            if a.dry_run:
                r["verdict"] = "LOCATE_ONLY"
                print("  轮 %2d  定位 %2d 条  目标%s  定位 %.1fs"
                      % (i, len(hits), "在屏" if ok_locate else "未在屏", loc_dt))
                r["round_sec"] = round(time.time() - t_round, 1)
                rec["rounds"].append(r)
                continue

            if not ok_locate:
                # 目标不在屏就不是链路的错，如实记为 UNDECIDED，不计入成功率分母
                r["verdict"] = "UNDECIDED_TARGET_ABSENT"
                print("  轮 %2d  目标未在屏 ⇒ 未决（不计入成功率）" % i)
                r["round_sec"] = round(time.time() - t_round, 1)
                rec["rounds"].append(r)
                continue

            score, tx, ty = hits[tname]
            r["tap_at"] = [tx, ty]
            r["tap_score"] = round(score, 3)

            # ---- 3) 点击前状态复核（默认复用 base 的判定，--pre-check 才额外抓帧）----
            # base 抓帧到此刻隔了「全库定位」≈3.2s，理论上状态可能自己变。
            # 但实测 3/3 轮 pre 帧与 base 帧逐像素一致（帧差 0.00）、状态恒为 map，
            # ⇒ 默认不再多抓这一帧（省 1.9s/轮）；需要更保险时加 --pre-check。
            if a.pre_check:
                pre = shot("soak%02d_pre" % i, r)
                r["pre_state"] = detect_state(pre, ref=base)
                r["pre_diff"] = None if diff_two(base, pre) is None else round(diff_two(base, pre), 2)
                r["pre_checked"] = True
            else:
                r["pre_state"] = st0
                r["pre_diff"] = 0.0
                r["pre_checked"] = False

            # ---- 3) 点击（坐标来自本轮模板匹配；点一次即验，没变化才补点）----
            t0 = time.time()
            after, d, times = tap_once_verified(
                tx, ty, base, "soak%02d_after" % i, r,
                # 补点门槛取「本目标的反应量级」与面板类默认值中的较小者：
                # panel 类 min(8.0,3.0)=3.0（面板反应 17~46，不会误补）；
                # label 类 min(1.5,3.0)=1.5，且 verify=False 根本不补点。
                min_diff=min(tgt.get("min_diff", SWALLOW_DIFF), SWALLOW_DIFF),
                verify=tgt.get("verify", True))
            r["tap_sec"] = round(time.time() - t0, 2)
            r["tap_times"] = times
            r["tap_out"] = "%s %.3f %d %d" % (tname, score, tx, ty)

            # ---- 4) 判定反应（帧差 + OCR 新增文字）----
            r["after"] = os.path.basename(after)
            r["diff"] = None if d is None else round(d, 2)
            # base 与 after 一次送进引擎：两次独立调用要各付一次冷启动（2.2s），
            # 合并后只付一次。判"新增了哪些文字"本来就要两张一起看，天然适合合并。
            both = ocr_lines_many([base, after])
            before_txt = {(x, y, t) for x, y, t in both.get(os.path.abspath(base), [])}
            after_lines = both.get(os.path.abspath(after), [])
            new_txt = [t for x, y, t in after_lines if (x, y, t) not in before_txt]
            r["new_texts"] = new_txt[:8]
            # 门槛按目标类型取：面板类 8.0、标签类 1.5（皮卡月刊实测帧差仅 2.04，
            # 沿用 8.0 会把"确实弹了名字标签"误判成"点了没反应"）
            thresh = tgt.get("min_diff", PANEL_DIFF)
            r["min_diff"] = thresh
            if d is not None and d >= thresh and new_txt:
                r["kind"] = "panel" if thresh >= PANEL_DIFF else "label"
            elif d is not None and d >= thresh:
                r["kind"] = "changed_no_text"
            else:
                r["kind"] = "none"
            r["verdict"] = "TAP_OK" if r["kind"] in ("panel", "label") else "TAP_WEAK"

            # ---- 5) 复位：OCR 状态机拉回地图界面 ----
            t0 = time.time()
            exit_kind = tgt.get("exit", "close")
            r["exit_kind"] = exit_kind
            if exit_kind == "retap":
                # toggle 名字标签（皮卡月刊）：再点一次同位置即收。
                # ⚠️ 不能走 ✕ —— 标签态点 ✕ 会把地图一起关掉、落回大世界（实测）。
                # 不复用 after 帧：它的状态是"标签态"，状态机认不出来，自己抓帧更稳。
                tap_px(tx, ty, 900)
                rst_ok, rst, path = reset_to_map(
                    r, verbose=a.verbose, ref=base,
                    tag_prefix="soak%02d_reset" % i)
            else:
                # 复用 after 帧当复位第 1 步（同一画面，省一次抓帧）；ref=base 让"回到 map"免 OCR。
                rst_ok, rst, path = reset_to_map(
                    r, verbose=a.verbose, ref=base, initial=after,
                    initial_state=("panel" if r.get("kind") == "panel" else None),
                    tag_prefix="soak%02d_reset" % i)
            r["reset_sec"] = round(time.time() - t0, 2)
            d2 = diff_two(base, rst)
            r["reset_diff"] = None if d2 is None else round(d2, 2)
            r["reset_ok"] = rst_ok
            # reset_texts 只用于报告展示。复位成功时跳过（整帧 OCR 4.1s）——
            # "是否真回到地图"由状态机（reset_ok）负责，不靠这段文字；
            # 失败时反而必须识别：那正是需要看"卡在什么界面"的时刻。
            r["reset_texts"] = ([t for x, y, t in ocr_lines(rst)][:8]
                                if (not rst_ok or a.verbose) else [])
            if rst_ok:
                prev_map = rst          # 给下一轮起点守卫当 ref

            print("  轮 %2d %-18s 定位%2d条/%.1fs  点(%d,%d)%.3f×%d  帧差%.2f 新增%d条 → %-6s  复位%s(%s)  本轮%.1fs"
                  % (i, tname, r["locate_hits"], loc_dt, tx, ty, score, times,
                     d if d is not None else -1, len(new_txt), r["kind"],
                     "OK" if rst_ok else "失败", "/".join(path),
                     time.time() - t_round))

        except Exception as e:
            r["verdict"] = "ERROR"
            r["error"] = str(e)[:300]
            print("  轮 %2d  异常：%s" % (i, e))

        r["round_sec"] = round(time.time() - t_round, 1)
        rec["rounds"].append(r)

    # ---------------- 汇总 ----------------
    rs = rec["rounds"]
    def vals(key):
        return [r[key] for r in rs if isinstance(r.get(key), (int, float))]

    def flat(key):
        # shot_ms / locate_ms 每轮记的是一个列表（一轮可能抓好几帧），统计前要摊平
        out = []
        for r in rs:
            v = r.get(key)
            if isinstance(v, list):
                out.extend(v)
            elif isinstance(v, (int, float)):
                out.append(v)
        return out

    decided = [r for r in rs if r.get("verdict") in ("TAP_OK", "TAP_WEAK", "TAP_FAIL")]
    ok = [r for r in decided if r.get("verdict") == "TAP_OK"]
    loc_found = [r for r in rs if r.get("target_found") is True]

    rec["summary"] = {
        "rounds_total": len(rs),
        "locate_rounds": len([r for r in rs if r.get("locate_hits") is not None]),
        "target_found_rounds": len(loc_found),
        "decided": len(decided),
        "tap_ok": len(ok),
        "tap_weak": len([r for r in decided if r.get("verdict") == "TAP_WEAK"]),
        "tap_fail": len([r for r in decided if r.get("verdict") == "TAP_FAIL"]),
        "undecided": len([r for r in rs if str(r.get("verdict", "")).startswith("UNDECIDED")]),
        "errors": len([r for r in rs if r.get("verdict") == "ERROR"]),
        "reset_ok": len([r for r in rs if r.get("reset_ok") is True]),
        "success_rate": round(len(ok) / len(decided), 3) if decided else None,
        "locate_hits_avg": round(statistics.mean([r["locate_hits"] for r in rs if "locate_hits" in r]), 1)
        if any("locate_hits" in r for r in rs) else None,
    }
    for key, label in (("shot_ms", "抓帧"), ("locate_ms", "全库定位")):
        v = flat(key)
        if v:
            rec["summary"][key + "_avg"] = round(statistics.mean(v))
            rec["summary"][key + "_max"] = max(v)
            rec["summary"][key + "_n"] = len(v)
    # 抓帧兜底统计：被丢弃的慢帧/失败尝试单独记，免得偶发卡顿被均值抹平
    retry_ms = flat("shot_retry_ms")
    rec["summary"]["shot_slow_rounds"] = len([r for r in rs if r.get("shot_slow")])
    rec["summary"]["shot_retry_n"] = len(retry_ms)
    # 抓帧通道与回落次数：raw 通道若在某台设备上不适用，这个计数会立刻暴露
    rec["summary"]["shot_channel"] = SHOT_CHANNEL
    rec["summary"]["shot_fallback_n"] = sum(r.get("shot_fallback", 0) for r in rs)
    if retry_ms:
        rec["summary"]["shot_retry_max_ms"] = max(retry_ms)
    for key, label in (("round_sec", "单轮"), ("tap_sec", "点击"), ("reset_sec", "复位")):
        v = vals(key)
        if v:
            rec["summary"][key + "_avg"] = round(statistics.mean(v), 1)

    # 分目标统计 —— 多图标模式的关键输出：哪个目标稳、哪个不稳（单图标模式也可看）
    per = {}
    for t in targets:
        sub = [r for r in rs if r.get("target") == t["name"]]
        if not sub:
            continue
        dec = [r for r in sub if r.get("verdict") in ("TAP_OK", "TAP_WEAK", "TAP_FAIL")]
        oks = [r for r in dec if r.get("verdict") == "TAP_OK"]
        diffs = [r["diff"] for r in sub if isinstance(r.get("diff"), (int, float))]
        per[t["name"]] = {
            "rounds": len(sub),
            "decided": len(dec),
            "tap_ok": len(oks),
            "reset_ok": len([r for r in sub if r.get("reset_ok") is True]),
            "diff_avg": round(statistics.mean(diffs), 2) if diffs else None,
            "expect": t.get("expect"),
        }
    if per:
        rec["summary"]["per_target"] = per

    # 写两份：soak.json 固定名（方便下游脚本直接读），外加一份带时间戳的存档。
    # ⚠️ 只写固定名会被**下一次跑批覆盖** —— 实测同一目录连跑 10/10/20 轮后，
    # 只剩最后一次的记录，前两次的证据没了。这是本项目**第三次**踩"同名文件覆盖"
    # （探针帧名 → 抓帧兜底旧帧 → 证据本身），病根都是"同名复用 + 无人校验新鲜度"。
    stamp = time.strftime("%Y%m%d-%H%M%S", time.localtime())
    rec["_archive"] = "soak-%s-%dr.json" % (stamp, len(rec["rounds"]))
    blob = json.dumps(rec, ensure_ascii=False, indent=2)
    with open(os.path.join(out_dir, "soak.json"), "w", encoding="utf-8") as f:
        f.write(blob)
    with open(os.path.join(out_dir, rec["_archive"]), "w", encoding="utf-8") as f:
        f.write(blob)

    s = rec["summary"]
    print("=" * 68)
    print("汇总（%d 轮）" % s["rounds_total"])
    print("  目标在屏        : %d/%d" % (s["target_found_rounds"], s["rounds_total"]))
    print("  有效轮（判了点击）: %d   成功 %d   弱 %d   失败 %d   未决 %d   异常 %d"
          % (s["decided"], s["tap_ok"], s["tap_weak"], s["tap_fail"], s["undecided"], s["errors"]))
    if s["success_rate"] is not None:
        print("  链路成功率      : %.1f%%" % (s["success_rate"] * 100))
    print("  复位成功        : %d/%d" % (s["reset_ok"], s["rounds_total"]))
    if s.get("per_target"):
        print("  ── 分目标 ──")
        for name, v in s["per_target"].items():
            print("    %-20s %d轮  成功 %d/%d  复位 %d/%d  平均帧差 %s  （预期：%s）"
                  % (name, v["rounds"], v["tap_ok"], v["decided"],
                     v["reset_ok"], v["rounds"],
                     v["diff_avg"] if v["diff_avg"] is not None else "-",
                     v.get("expect") or "-"))
    print("  全库定位命中均值 : %s 条/轮" % s["locate_hits_avg"])
    print("  平均抓帧        : %s ms（最慢 %s）· 通道 %s%s"
          % (s.get("shot_ms_avg"), s.get("shot_ms_max"), s.get("shot_channel"),
             ("（回落 %d 次）" % s["shot_fallback_n"]) if s.get("shot_fallback_n") else ""))
    if s.get("shot_retry_n"):
        print("  抓帧兜底        : %d 轮出现慢帧 / 丢弃重抓 %d 次（最慢被弃 %s ms）"
              % (s.get("shot_slow_rounds"), s.get("shot_retry_n"),
                 s.get("shot_retry_max_ms")))
    print("  平均全库定位    : %s ms（最慢 %s）" % (s.get("locate_ms_avg"), s.get("locate_ms_max")))
    print("  平均单轮        : %s s" % s.get("round_sec_avg"))
    print("证据：%s" % os.path.join(a.out, "soak.json"))
    if rec.get("_archive"):
        print("存档：%s（soak.json 会被下次跑批覆盖，带时间戳的这份不会）"
              % os.path.join(a.out, rec["_archive"]))
    print("=" * 68)
    return 0


if __name__ == "__main__":
    # 常驻 OCR 进程用 atexit 兜底关闭：正常结束、异常退出、Ctrl-C 都能收掉，
    # 不留一个占着模型内存的孤儿进程。
    atexit.register(close_server)
    sys.exit(main())
