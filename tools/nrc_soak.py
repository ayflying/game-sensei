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
import json
import os
import re
import statistics
import subprocess
import sys
import time

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
PY = sys.executable
DRV = os.path.join(ROOT, ".workbuddy", "nrc", "drv.py")
ICON_MATCH = os.path.join(ROOT, "tools", "icon_match.py")
OCR_EXE = os.path.join(ROOT, ".workbuddy", "bin", "ocr.exe")
SHOTDIR = os.path.join(ROOT, ".workbuddy", "tmp", "screenshots", "nrc-20260918")
# 点击直连 adb：少一层 python 进程、更快，也让耗时统计干净（见 tap_px 注释）
ADB = os.environ.get("NRC_ADB",
                     r"C:/Users/ay/AppData/Local/Android/Sdk/platform-tools/adb.exe")
SERIAL = os.environ.get("NRC_SERIAL", "ecbff3a5")

# 本轮唯一被点击的图标（固定 UI、退出路径已实测无损）
TAP_TARGET = "眠枭庇护所(地图UI)"
# 地图界面右上角 ✕（点一次即退出面板/标记态；连点两次会连地图一起关掉、落回大世界）
CLOSE_X, CLOSE_Y = 2169, 49
# 大世界里开地图的右上导航圆钮（单点即可，被吞再点）
NAV_X, NAV_Y = 2127, 150
# 面板弹出判据：帧差阈值（点开区域进度面板实测远大于此）
PANEL_DIFF = 8.0
# 「首点被吞」判据：点击后帧差低于此值 ⇒ 认为这一下没生效，补点一次
SWALLOW_DIFF = 3.0
# 单次点击后的等待（毫秒）：面板弹出实测 <1s，留 1.2s 余量
TAP_WAIT = 1200
# 地图界面的「固定 UI 锚点」：这两条同时命中 ⇒ 必在地图界面（免整帧 OCR 判态）
# 依据：icon_crossframe.py 实测二者都是多帧同坐标的固定 UI（家园 (2152,1029)、
# 眠枭庇护所(地图UI) (188,187)），且只在地图界面存在。
MAP_ANCHORS = {"家园", "眠枭庇护所(地图UI)"}


def run(args, timeout=180):
    p = subprocess.run(args, cwd=ROOT, capture_output=True, timeout=timeout)
    out = p.stdout.decode("utf-8", "replace")
    err = p.stderr.decode("utf-8", "replace")
    return p.returncode, out, err


def shot(tag, rec):
    """抓一帧，返回帧绝对路径。顺带记录耗时（真机流畅度指标）。

    用 drv.py 的 shotq（只抓帧、不跑 judge）：一审 judge 要读图算三个指标，
    本轮巡检并不需要界面判断（而且 judge 对地图界面本来就误判成 WORLD）。
    """
    t0 = time.time()
    rc, out, err = run([PY, DRV, "shotq", tag])
    dt = time.time() - t0
    rec.setdefault("shot_ms", []).append(round(dt * 1000))
    path = None
    m = re.search(r"([A-Za-z]:[^\s]*%s\.png)" % re.escape(tag), out)
    if m:
        path = m.group(1)
    if path is None:
        cand = os.path.join(SHOTDIR, tag + ".png")
        if os.path.exists(cand):
            path = cand
    if path is None or not os.path.exists(path):
        raise RuntimeError("抓帧失败 tag=%s rc=%d out=%r err=%r" % (tag, rc, out[:300], err[:300]))
    return path


def locate(frame, rec, min_score=0.85):
    """全库定位：返回 {名称: (分数, x, y)}。记录耗时与命中数。"""
    t0 = time.time()
    rc, out, err = run([PY, ICON_MATCH, "--find-all", "--shot", frame,
                        "--at", "--min-score", str(min_score)])
    dt = time.time() - t0
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


def ocr_lines(frame, min_conf=0.30):
    """返回 [(x, y, 文本)]；OCR 不可用时返回空表（不阻断巡检）。"""
    if not os.path.exists(OCR_EXE):
        return []
    try:
        rc, out, err = run([OCR_EXE, "-in", frame, "-min", str(min_conf)], timeout=120)
    except Exception:
        return []
    lines = []
    for line in out.splitlines():
        m = re.match(r"\(\s*(\d+),\s*(\d+)\)\s+(.*?)\s+\[([\d.]+)\]$", line.strip())
        if m:
            lines.append((int(m.group(1)), int(m.group(2)), m.group(3)))
    return lines


def ocr_texts(frame, min_conf=0.30):
    """返回帧上识别到的文字集合（用于判界面态与"有没有新文字"）。"""
    return {t for _x, _y, t in ocr_lines(frame, min_conf)}


def detect_state(frame, hits=None, ref=None):
    """判界面态。返回 map / panel / marker_edit / world / unknown。

    为什么不用像素判据：drv.py 的 judge 在**地图界面**会误报成 WORLD
    （它只看右上亮像素，而地图界面右上同样是大片亮区，实测多次踩到）。
    界面态只能靠语义特征收口。

    判据来自 09-21 实测（同一设备同一分辨率，逐帧 OCR 对照）：
      map         含「精灵踪迹」或「卡洛西亚大陆」（地图界面固有元素）
      panel       含「风眠省」/「15/15」——点「眠枭庇护所(地图UI)」弹出的区域进度面板
      marker_edit 含「标记（点击修改名称）」——误入标记编辑态时的提示语
      world       含「触碰」——野外落地才有的交互按钮

    ⚡ 两条**免 OCR 快路径**（整帧 OCR 实测 3.6s，是单轮最大的单项开销）：
      1) 传入 hits（本轮全库定位结果）且命中多个「固定 UI 锚点」⇒ 必是地图界面。
         定位本来就要跑，这一步等于零成本。
      2) 传入 ref（一张已确认是地图的参考帧）且与它逐像素接近 ⇒ 地图界面。
         复位最后一步"回到 map"正是这种情况，帧差 ≈0，不必再 OCR 一次。
    两条都不成立才落回 OCR 语义判态（宁可慢，不能误判）。
    """
    if hits and MAP_ANCHORS.issubset(hits):
        return "map"
    if ref is not None:
        d = diff_two(ref, frame)
        if d is not None and d < SWALLOW_DIFF:
            return "map"
    txt = ocr_texts(frame)
    joined = " ".join(txt)
    if "触碰" in joined:
        return "world"
    if "标记（点击修改名称）" in joined or "标记(点击修改名称)" in joined:
        return "marker_edit"
    if "风眠省" in joined or "15/15" in joined:
        return "panel"
    if "精灵踪迹" in joined or "卡洛西亚大陆" in joined:
        return "map"
    return "unknown"


def reset_to_map(rec, max_attempts=5, verbose=True, ref=None,
                 initial=None, initial_state=None):
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
    """
    steps = []
    cur_f, cur_st = initial, initial_state
    f = None
    for i in range(max_attempts):
        if cur_f is not None:
            f = cur_f
            st = cur_st if cur_st else detect_state(f, ref=ref)
            cur_f, cur_st = None, None            # 只在第一步复用一次
        else:
            f = shot("reset_probe%02d" % i, rec)
            st = detect_state(f, ref=ref)
        steps.append(st)
        if st == "map":
            rec["reset_attempts"] = i
            rec["reset_state_path"] = steps
            return True, f, steps
        if st == "world":
            tap_px(NAV_X, NAV_Y, 1500)            # 开地图（单点，被吞再点）
        else:                                      # panel / marker_edit / unknown
            tap_px(CLOSE_X, CLOSE_Y, 900)          # 只点一次，点完立刻重判
    rec["reset_attempts"] = max_attempts
    rec["reset_state_path"] = steps
    return False, f, steps


def diff_two(a, b):
    """两帧灰度降采样后的平均绝对差（与 drv.py diff 同口径，这里自己算省一次进程）。"""
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


def tap_once_verified(x, y, base, tag, rec, wait_ms=TAP_WAIT):
    """点一次 → 抓帧；只有「几乎没变化」才补点一次 → 再抓帧。

    返回 (帧路径, 帧差, 点击次数)。帧差以 `base` 为参照。

    为什么不能盲点两次：目标图标是 toggle。盲点两次时若两次都生效 ⇒ 开+关
    ⇒ 与 base 逐像素一致（帧差 0.00），会被判成"点击无效"；若首点恰好被吞
    ⇒ 吞+开 ⇒ 面板开着（帧差 ~18），同一份代码给出两种截然相反的结果。
    自适应补点后，两种情况都收敛到"面板开着"。
    """
    tap_px(x, y, wait_ms)
    times = 1
    f = shot(tag + "_a", rec)
    d = diff_two(base, f)
    if d is not None and d < SWALLOW_DIFF:
        tap_px(x, y, wait_ms)
        times = 2
        f = shot(tag + "_b", rec)
        d = diff_two(base, f)
    return f, d, times


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--rounds", type=int, default=10, help="跑多少轮")
    ap.add_argument("--out", default=".workbuddy/evidence/nrc/20260921-soak", help="证据目录")
    ap.add_argument("--min-score", type=float, default=0.85, help="定位阈值")
    ap.add_argument("--dry-run", action="store_true", help="只抓帧+定位，不点击")
    ap.add_argument("--pre-check", action="store_true",
                    help="点击前额外抓一帧复核状态（更保险，但每轮多花 1.9s；实测漂移恒为 0）")
    ap.add_argument("--verbose", action="store_true")
    a = ap.parse_args()

    out_dir = os.path.join(ROOT, a.out)
    os.makedirs(out_dir, exist_ok=True)

    rec = {"rounds": [], "started": time.strftime("%Y-%m-%d %H:%M:%S"),
           "target": TAP_TARGET, "dry_run": a.dry_run, "min_score": a.min_score}

    print("=" * 68)
    print("洛克王国:世界 真机巡检  轮数=%d  点击目标=%s%s"
          % (a.rounds, TAP_TARGET, "  [DRY-RUN]" if a.dry_run else ""))
    print("=" * 68)

    prev_map = None            # 上一轮复位后的地图帧：给本轮起点守卫当 ref（省一次 OCR）
    for i in range(1, a.rounds + 1):
        r = {"round": i, "t0": time.strftime("%H:%M:%S")}
        t_round = time.time()
        try:
            # ---- 0) 起点守卫：不在地图界面就先复位，别让上一轮的残留状态污染本轮统计 ----
            base = shot("soak%02d_base" % i, r)
            st0 = detect_state(base, ref=prev_map)
            r["start_state"] = st0
            if st0 != "map":
                ok0, base, path0 = reset_to_map(r, verbose=a.verbose)
                r["start_reset_path"] = path0
                st0 = detect_state(base)
                r["start_state"] = st0
                if not ok0:
                    r["verdict"] = "UNDECIDED_NOT_ON_MAP"
                    print("  轮 %2d  起点不在地图界面（%s）且复位失败 ⇒ 未决（不计入成功率）" % (i, st0))
                    r["round_sec"] = round(time.time() - t_round, 1)
                    rec["rounds"].append(r)
                    continue

            # ---- 1) 基准帧就绪 ----
            r["base"] = os.path.basename(base)

            # ---- 2) 全库定位 ----
            hits, loc_dt = locate(base, r, a.min_score)
            r["locate_hits"] = len(hits)
            r["locate_sec"] = round(loc_dt, 2)
            r["hits"] = {k: [round(v[0], 3), v[1], v[2]] for k, v in sorted(hits.items())}
            if a.verbose:
                for k, v in sorted(hits.items()):
                    print("      %-22s %.3f @(%d,%d)" % (k, v[0], v[1], v[2]))

            ok_locate = TAP_TARGET in hits
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

            score, tx, ty = hits[TAP_TARGET]
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
            after, d, times = tap_once_verified(tx, ty, base, "soak%02d_after" % i, r)
            r["tap_sec"] = round(time.time() - t0, 2)
            r["tap_times"] = times
            r["tap_out"] = "%s %.3f %d %d" % (TAP_TARGET, score, tx, ty)

            # ---- 4) 判定反应（帧差 + OCR 新增文字）----
            r["after"] = os.path.basename(after)
            r["diff"] = None if d is None else round(d, 2)
            before_txt = {(x, y, t) for x, y, t in ocr_lines(base)}
            after_lines = ocr_lines(after)
            new_txt = [t for x, y, t in after_lines if (x, y, t) not in before_txt]
            r["new_texts"] = new_txt[:8]
            if d is not None and d >= PANEL_DIFF and new_txt:
                r["kind"] = "panel"
            elif d is not None and d >= PANEL_DIFF:
                r["kind"] = "changed_no_text"
            else:
                r["kind"] = "none"
            r["verdict"] = "TAP_OK" if r["kind"] == "panel" else "TAP_WEAK"

            # ---- 5) 复位：OCR 状态机拉回地图界面 ----
            # 复用 after 帧当复位第 1 步（同一画面，省一次抓帧）；ref=base 让"回到 map"免 OCR。
            t0 = time.time()
            rst_ok, rst, path = reset_to_map(
                r, verbose=a.verbose, ref=base, initial=after,
                initial_state=("panel" if r.get("kind") == "panel" else None))
            r["reset_sec"] = round(time.time() - t0, 2)
            d2 = diff_two(base, rst)
            r["reset_diff"] = None if d2 is None else round(d2, 2)
            r["reset_ok"] = rst_ok
            r["reset_texts"] = [t for x, y, t in ocr_lines(rst)][:8]
            if rst_ok:
                prev_map = rst          # 给下一轮起点守卫当 ref

            print("  轮 %2d  定位%2d条/%.1fs  点(%d,%d)%.3f×%d  点击前态=%s(帧差%s)  点击后帧差%.2f 新增%d条 → %s  复位%s(%s)  本轮%.1fs"
                  % (i, r["locate_hits"], loc_dt, tx, ty, score, times,
                     r["pre_state"], r["pre_diff"],
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
    for key, label in (("round_sec", "单轮"), ("tap_sec", "点击"), ("reset_sec", "复位")):
        v = vals(key)
        if v:
            rec["summary"][key + "_avg"] = round(statistics.mean(v), 1)

    with open(os.path.join(out_dir, "soak.json"), "w", encoding="utf-8") as f:
        json.dump(rec, f, ensure_ascii=False, indent=2)

    s = rec["summary"]
    print("=" * 68)
    print("汇总（%d 轮）" % s["rounds_total"])
    print("  目标在屏        : %d/%d" % (s["target_found_rounds"], s["rounds_total"]))
    print("  有效轮（判了点击）: %d   成功 %d   弱 %d   失败 %d   未决 %d   异常 %d"
          % (s["decided"], s["tap_ok"], s["tap_weak"], s["tap_fail"], s["undecided"], s["errors"]))
    if s["success_rate"] is not None:
        print("  链路成功率      : %.1f%%" % (s["success_rate"] * 100))
    print("  复位成功        : %d/%d" % (s["reset_ok"], s["rounds_total"]))
    print("  全库定位命中均值 : %s 条/轮" % s["locate_hits_avg"])
    print("  平均抓帧        : %s ms（最慢 %s）" % (s.get("shot_ms_avg"), s.get("shot_ms_max")))
    print("  平均全库定位    : %s ms（最慢 %s）" % (s.get("locate_ms_avg"), s.get("locate_ms_max")))
    print("  平均单轮        : %s s" % s.get("round_sec_avg"))
    print("证据：%s" % os.path.join(a.out, "soak.json"))
    print("=" * 68)
    return 0


if __name__ == "__main__":
    sys.exit(main())
