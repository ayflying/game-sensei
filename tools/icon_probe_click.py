#!/usr/bin/env python
"""大地图图标「点击探针」—— 点一下看有没有提示，有提示的才是真图标。

来源（安酱 2026-09-20 的关键指正）：
  「大地图上面的图标，点上去就会有提示的，会告诉你是什么东西，
    点击没反应的，就不是按钮，没有意义」

⇒ 图标识别不必靠视觉猜测（9B 本地 VLM 命名小图标已实测不可用），
  **点击后的反应本身就是最强判据**；而且提示文字可以直接当图标名，
  一举解决「过滤地形噪声」与「自动命名」两件事。

三种反应（2026-09-20 真机实测，MIX3 横屏 2340x1080）：
  icon   点后弹出带名称的面板/提示（如「大型眠枭庇护所」+ 放入果实说明 + 传送）
         ⇒ 真图标；名称 = 相对基准帧**新增**的文字里最长的一条
  blank  点后进入「标记（点击修改名称）」编辑态（底部出现「标记」按钮、
         上方「常规标记数 N/270」）⇒ 空白/地形/已有标记，**非图标**
  dead   点后帧差 < --dead-thr ⇒ 完全无反应（水体高光、渐变色块）

⚠️ 前置：真机必须停在**干净的大地图**（无面板、无标记编辑态）。
   实测（2026-09-20）：点空白会进入「标记」编辑态，但**点右上 ✕ 是无损退出**
   （标记数 8→8、退出后帧与退出前逐像素一致 meandiff 0.00）⇒ 全程用 ✕，不污染玩家地图。
   另一个实测坑：**点击某些地图元素会让地图自己平移**（视口漂移，证据：探测帧出现
   基准帧上根本不存在的「聆风镇/西区工厂/隐秘滩涂」）⇒ 后续候选坐标全部失准。
   探针会检测地图顶部地名变化，自动复位视口并重设基准帧。

用法：
  python tools/icon_probe_click.py --fresh --top 12          # 抓当前帧，探面积最大的 12 个
  python tools/icon_probe_click.py --shot t3 --top 20
  python tools/icon_probe_click.py --shot t3 --at "1118,910;1009,686"
  python tools/icon_probe_click.py --shot t3 --top 12 --dry-run   # 只列候选，不碰真机

产出（--out 目录）：
  probes.json  逐候选的完整记录（坐标/反应/新增文字/帧差/耗时）
  report.md    人读表格，`icon` 行即"确认可用的图标 + 名称"
"""
import argparse
import json
import os
import re
import subprocess
import sys
import time

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
sys.path.insert(0, os.path.join(ROOT, ".workbuddy", "nrc"))
sys.path.insert(0, os.path.join(ROOT, "tools"))

import drv            # noqa: E402  真机驱动（tapx / grab）
import icon_extract   # noqa: E402  候选提取（复用 CLI 判据）
import icon_match     # noqa: E402  读图（中文路径安全）

OCR = os.path.join(ROOT, ".workbuddy", "bin", "ocr.exe")
SHOTDIR = drv.SHOTDIR
PY = sys.executable

# 面板关闭按钮（地图界面右上 ✕）。分层关闭：面板态关面板、编辑态退编辑、干净地图态才关地图。
CLOSE_XY = (2169, 49)
# 大世界右上角「地图」按钮（首点常被吞，需连点两次）
MAP_BTN = (2127, 150)


def ocr_items(png, min_conf=0.45):
    """调 ocr.exe，解析成 [(x, y, text, conf)]。"""
    p = subprocess.run([OCR, "-in", png, "-min", str(min_conf)],
                       capture_output=True)
    out = (p.stdout or b"").decode("utf-8", "ignore")
    items = []
    for line in out.splitlines():
        m = re.match(r"\(\s*(\d+),\s*(\d+)\)\s+(.+?)\s+\[([\d.]+)\]\s*$", line.strip())
        if m:
            items.append((int(m.group(1)), int(m.group(2)),
                          m.group(3).strip(), float(m.group(4))))
    return items


def norm_text(t):
    """归一化 OCR 文本，用于跨帧"新增文字"比较（OCR 会漂移个别字符）。"""
    return re.sub(r"[\s:：,，.。·\-—|]", "", t)


def mean_diff(pa, pb):
    """两帧平均灰度差（与 drv.diff 同口径，但不走子进程）。"""
    import numpy as np
    from PIL import Image
    a = np.asarray(Image.open(pa).convert("L"), dtype=np.float32)
    b = np.asarray(Image.open(pb).convert("L"), dtype=np.float32)
    return float(abs(a - b).mean())


def is_blank(items):
    """是否处于「标记」编辑态。"""
    return any("修改名称" in t or "常规标记数" in t for _, _, t, _ in items)


def on_map(items):
    """是否停在大地图界面。

    ⚠ 不能用 drv.py 的 judge：它只看右上亮像素，会把地图界面判成 WORLD
    （地图右上同样是大片亮区），实测已确认。
    """
    return any("卡洛西亚大陆" in t or "精灵踪迹" in t for _, _, t, _ in items)


def viewport_sig(items):
    """视口指纹：地图**顶部区域**的地名（地图顶部中央显示当前视口位置名）。

    用途：检测"点了某个元素后地图自己平移了"。
    实测证据（2026-09-20 run2）：点 (2150,993) 后探测帧出现「聆风镇 / 西区工厂 / 隐秘滩涂」，
    而基准帧用 min_conf 0.25 也**根本没有**这些文字 ⇒ 不是 OCR 漏识，是地图真的移动了。
    ⇒ 视口一变，后续候选坐标全部失准，必须复位。
    """
    return "|".join(sorted(t for x, y, t, _ in items if y < 140))


def reset_view():
    """关掉地图再打开 ⇒ 视口回到"以角色为中心"的默认位置，并返回新基准帧路径。"""
    drv.tapx(CLOSE_XY[0], CLOSE_XY[1], 2400)
    items = ocr_items(drv.grab("rst1"), 0.40)
    if on_map(items) or is_blank(items):        # 首点只退了一层，再点一次才关地图
        drv.tapx(CLOSE_XY[0], CLOSE_XY[1], 2400)
    drv.tapx(MAP_BTN[0], MAP_BTN[1], 1500)      # 重开（首点常被吞，连点两次）
    drv.tapx(MAP_BTN[0], MAP_BTN[1], 2600)
    p = drv.grab("rst_reopen")
    if is_blank(ocr_items(p, 0.40)):
        drv.tapx(CLOSE_XY[0], CLOSE_XY[1], 2400)
        p = drv.grab("rst_reopen2")
    return p


def find_text_xy(items, pred, y_min=0):
    for x, y, t, _ in items:
        if y >= y_min and pred(t):
            return x, y
    return None


def new_texts(base_items, items):
    """相对基准帧新增的文字，按 **y 升序**（最靠上的在前）。

    为什么按 y 而不是按长度排：面板**标题在顶部**（眠枭的「大型眠枭庇护所」在 y=66），
    而最长的往往是一句描述（家园的「温暖的港湾。总有人在期盼着你回家。」），
    按长度取会把描述当名称。
    """
    seen = {norm_text(t) for _, _, t, _ in base_items}
    out, used = [], set()
    for x, y, t, c in items:
        n = norm_text(t)
        if not n or n in seen or n in used:
            continue
        used.add(n)
        out.append({"x": x, "y": y, "text": t, "conf": c})
    out.sort(key=lambda d: d["y"])
    return out


def pick_name(nts):
    """从新增文字里挑一个"最像图标名"的：取最靠上的，但跳过地名与长句。

    实测：真图标标题 ≤14 字（「大型眠枭庇护所」「炼金釜」「『露天对战丹尼」），
    描述句通常 >15 字（「旅行的魔法师留下的炼金釜，可进行炼金制造和能力提升。」）。
    另外**地图顶部中央 (y<50) 是"当前视口地名"**（如「聆风镇」），点任何东西都可能
    因地图渲染刷新而被 OCR 读出来，不是图标名 ⇒ 跳过。
    """
    for d in nts:
        if d["y"] < 50:
            continue
        t = d["text"].strip()
        if 1 <= len(t) <= 14 and not re.search(r"\d+米|^[0-9:/\s]+$", t):
            return t
    return nts[0]["text"] if nts else ""


def is_mark(nts):
    """是否是"玩家自己放的地图标记"的详情面板（不是图标）。

    实测特征：新增文字里带**距离**（「1125米」）或「前往XXX」——
    标记详情面板的内容是「前往魔法师之家 / 1125米 / 触碰」。
    这类东西点了也有反应，但它是玩家标记、不是游戏图标。
    """
    for d in nts:
        if re.search(r"\d+\s*米", d["text"]) or d["text"].startswith("前往"):
            return True
    return False


def extract_candidates(shot_path, out_dir, extra):
    """复用 icon_extract CLI 拿候选（不复制提取逻辑）。"""
    metap = os.path.join(out_dir, "_extract", "icons.json")
    cmd = [PY, os.path.join(ROOT, "tools", "icon_extract.py"), shot_path,
           "--out", os.path.join(out_dir, "_extract")] + list(extra)
    subprocess.run(cmd, capture_output=True)
    if not os.path.exists(metap):
        return []
    with open(metap, encoding="utf-8") as f:
        return json.load(f)["icons"]


def tap_twice_if_needed(x, y, base_png, tag, dead_thr, out_dir, verbose=True):
    """点一下→抓帧判定；没反应再点第二次（首点被吞极常见）。

    返回 (帧差, 探测帧路径)。为什么要"按需第二次"：
    地图面板底部常有按钮（如眠枭面板「传送」在 1869,1006），
    盲目连点两次有踩中面板按钮的风险。
    """
    drv.tapx(x, y, 900)
    p1 = drv.grab(f"{tag}_a")
    d1 = mean_diff(base_png, p1)
    if d1 >= dead_thr:
        return d1, p1
    drv.tapx(x, y, 2200)
    p2 = drv.grab(f"{tag}_b")
    d2 = mean_diff(base_png, p2)
    return d2, p2


def recover(shot_name, kind, verbose=True):
    """把界面恢复到干净大地图。返回 (是否恢复, 帧路径)。

    实测（2026-09-20）：标记编辑态点右上 ✕ 是**无损退出**
    （标记数 8→8、退出后帧与退出前逐像素一致 meandiff 0.00）；
    点底部「标记」按钮则会**保存**标记 ⇒ 每退一次地图上多一个垃圾标记。
    ⇒ 一律用 ✕ 退出。✕ 是分层关闭：面板态关面板、编辑态退编辑、干净地图态才关地图，
      所以退出后要确认"还在地图上"，被关掉就重新打开。
    """
    p = None
    for attempt in range(3):
        drv.tapx(CLOSE_XY[0], CLOSE_XY[1], 2400)
        p = drv.grab(f"{shot_name}_chk{attempt}")
        items = ocr_items(p, 0.40)
        if is_blank(items):
            continue                       # 还在编辑态，再点一次
        if not on_map(items):              # 整个地图被关掉了 ⇒ 重新打开
            drv.tapx(MAP_BTN[0], MAP_BTN[1], 1500)   # 首点常被吞，连点两次
            drv.tapx(MAP_BTN[0], MAP_BTN[1], 2600)
            p = drv.grab(f"{shot_name}_map{attempt}")
            items = ocr_items(p, 0.40)
            if is_blank(items):            # 开图后可能停在编辑态
                drv.tapx(CLOSE_XY[0], CLOSE_XY[1], 2400)
                p = drv.grab(f"{shot_name}_map2{attempt}")
                items = ocr_items(p, 0.40)
        if on_map(items) and not is_blank(items):
            if verbose:
                print("      ↳ 已回到干净地图")
            return True, p
    return False, p


def apply_icons(results, pad=2, verbose=True):
    """把探测到的真图标建模板入库（复用 icon_match CLI，重名跳过）。

    ⚠ 名称来自 OCR，可能有个别字误识（实测「眠枭庇护所」被读成「眠底护所」）
    ⇒ 默认不自动入库，只有显式 --apply 才执行，且入库后仍建议人工核一遍名称。
    模板从**基准帧**裁，不从探测帧裁 —— 探测帧上图标位置可能被弹出的面板盖住。
    """
    lib = icon_match.load_lib()
    names = set(lib.get("templates", {}))
    made, skipped, failed = [], [], []
    for r in results:
        if r["kind"] != "icon":
            continue
        name = (r.get("name") or "").strip()
        if not name or name.startswith("("):
            continue
        if name in names:
            skipped.append(name)
            continue
        png = os.path.join(SHOTDIR, r.get("base_shot") or r["shot"])
        if not os.path.exists(png) or r["w"] <= 0 or r["h"] <= 0:
            failed.append(name)
            continue
        x0, y0 = r["x"] - r["w"] // 2, r["y"] - r["h"] // 2
        x1, y1 = r["x"] + r["w"] // 2, r["y"] + r["h"] // 2
        note = (f"点击探针自动建模板：点 ({r['x']},{r['y']}) 弹出提示，OCR 读出「{name}」；"
                f"来源帧 {r.get('base_shot')}；帧差 {r['diff']}")
        p = subprocess.run([PY, os.path.join(ROOT, "tools", "icon_match.py"),
                            "--make", name, "--from-shot", png,
                            "--bbox", f"{x0},{y0},{x1},{y1}",
                            "--pad", str(pad), "--note", note],
                           capture_output=True)
        if p.returncode == 0:
            made.append(name)
            names.add(name)
        else:
            failed.append(name)
    if verbose:
        print(f"\n入库：新建 {len(made)}、跳过(已存在) {len(skipped)}、失败 {len(failed)}")
        if made:
            print("  新建：" + "、".join(made))
        if skipped:
            print("  已存在：" + "、".join(skipped))
        if failed:
            print("  失败：" + "、".join(failed))
    return made


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--shot", default="", help=f"基准帧名（{SHOTDIR} 下，不含 .png）或绝对路径")
    ap.add_argument("--fresh", action="store_true", help="先抓一帧作为基准（真机须在干净大地图）")
    ap.add_argument("--top", type=int, default=12, help="按面积取前 N 个候选")
    ap.add_argument("--min-area", type=int, default=250, help="候选面积下限")
    ap.add_argument("--at", default="", help='直接指定坐标 "x,y;x,y"（跳过候选提取）')
    ap.add_argument("--out", default="", help="产出目录")
    ap.add_argument("--dead-thr", type=float, default=3.0, help="帧差低于此值判为无反应")
    ap.add_argument("--dry-run", action="store_true", help="只列候选，不点击")
    ap.add_argument("--max-fail", type=int, default=3, help="连续恢复失败几次就停")
    ap.add_argument("--min-conf", type=float, default=0.45, help="OCR 置信度下限")
    ap.add_argument("--apply", action="store_true",
                    help="把探测到的真图标自动建模板入库（重名跳过）")
    ap.add_argument("--apply-pad", type=int, default=2, help="--apply 建模板时的四向留边")
    a = ap.parse_args()

    if not a.shot and not a.fresh:
        print("需要 --shot 或 --fresh")
        return 1

    out_dir = a.out or os.path.join(ROOT, ".workbuddy", "nrc", "icon_atlas", "probe")
    os.makedirs(out_dir, exist_ok=True)

    # 基准帧
    if a.fresh:
        base = drv.grab("probe_base")
        base_name = "probe_base"
    elif os.path.isabs(a.shot):
        base, base_name = a.shot, os.path.splitext(os.path.basename(a.shot))[0]
    else:
        base_name = a.shot
        base = os.path.join(SHOTDIR, a.shot + ".png")
    if not os.path.exists(base):
        print(f"基准帧不存在：{base}")
        return 1

    base_items = ocr_items(base, a.min_conf)
    if is_blank(base_items):
        print("⚠ 基准帧本身就处于「标记」编辑态，请先退出后再探测。")
        return 1
    print(f"基准帧 {base_name}  文字 {len(base_items)} 条")
    print("  " + " | ".join(t for _, _, t, _ in base_items[:8]))
    # 每次探测都以"当下有效的基准帧"为准（视口漂移后会换新基准）
    cur_base, cur_base_items = base, base_items
    base_sig = viewport_sig(base_items)

    # 候选
    if a.at:
        cands = []
        for i, pt in enumerate(a.at.split(";")):
            pt = pt.strip()
            if not pt:
                continue
            x, y = pt.split(",")
            cands.append({"id": f"A{i:03d}", "cx": int(x), "cy": int(y),
                          "w": 0, "h": 0, "area": 0, "color": "-"})
    else:
        ics = extract_candidates(base, out_dir, [])
        ics = [c for c in ics if c["area"] >= a.min_area]
        ics.sort(key=lambda c: -c["area"])
        cands = ics[:a.top]

    if not cands:
        print("没有候选（可降低 --min-area）")
        return 1
    print(f"\n待探测 {len(cands)} 个候选：")
    for c in cands:
        print(f"  {c['id']:5s} ({c['cx']:4d},{c['cy']:4d})  {c['w']}x{c['h']} area={c['area']}")
    if a.dry_run:
        print("\n(--dry-run，未点击)")
        return 0

    # 探测
    results = []
    fails = 0
    drifts = 0
    for i, c in enumerate(cands):
        tag = f"pr{i:03d}"
        t0 = time.time()
        print(f"\n[{i + 1}/{len(cands)}] {c['id']} ({c['cx']},{c['cy']})")
        d, png = tap_twice_if_needed(c["cx"], c["cy"], cur_base, tag,
                                     a.dead_thr, out_dir)
        items = ocr_items(png, a.min_conf)
        nts = new_texts(cur_base_items, items)
        if is_blank(items):
            kind, name = "blank", ""
        elif d < a.dead_thr and not nts:
            # 帧差极小 **且** 没有任何新文字，才算"无反应"。
            # 只看帧差会误杀"轻量提示"：实测点地图小图标只弹一个小名称标签，
            # 帧差仅 3.18~3.36（贴着阈值 3.0），但确实有反应、有名称。
            kind, name = "dead", ""
        elif is_mark(nts):
            kind, name = "mark", ""
        else:
            kind = "icon"
            name = pick_name(nts) or "(文字未识别)"
        rec = {"id": c["id"], "x": c["cx"], "y": c["cy"], "w": c["w"], "h": c["h"],
               "area": c["area"], "color": c["color"], "kind": kind, "name": name,
               "diff": round(d, 2), "shot": os.path.basename(png),
               # 建模板要从**基准帧**裁：探测帧上图标位置可能被弹出的面板盖住
               "base_shot": os.path.basename(cur_base),
               "new_texts": nts[:6], "secs": round(time.time() - t0, 1)}
        results.append(rec)
        label = {"icon": "★真图标", "blank": "空白(标记态)", "dead": "无反应",
                 "mark": "玩家标记"}[kind]
        print(f"   → {label}  帧差 {d:.2f}" + (f"  名称「{name}」" if name else ""))
        if kind == "icon" and nts:
            print("     新增文字：" + " / ".join(x["text"] for x in nts[:4]))

        ok, p_after = recover(f"{tag}_rec", kind)
        if not ok:
            fails += 1
            print(f"      ⚠ 恢复失败（连续 {fails} 次）")
            if fails >= a.max_fail:
                print("      连续恢复失败，停止探测以免污染后续。")
                break
        else:
            fails = 0
            # 视口漂移检测：地图顶部地名变了 ⇒ 复位并换基准，否则后续候选坐标全部失准
            if viewport_sig(ocr_items(p_after, a.min_conf)) != base_sig:
                drifts += 1
                rec["drift"] = True
                print("      ↳ 视口漂移（点击让地图自己平移了），复位视口…")
                cur_base = reset_view()
                cur_base_items = ocr_items(cur_base, a.min_conf)
                print(f"      新基准视口：{viewport_sig(cur_base_items) or '(未识别)'}")

    # 产出
    with open(os.path.join(out_dir, "probes.json"), "w", encoding="utf-8") as f:
        json.dump({"base": base_name, "results": results}, f,
                  ensure_ascii=False, indent=1)

    icons = [r for r in results if r["kind"] == "icon"]
    blanks = [r for r in results if r["kind"] == "blank"]
    deads = [r for r in results if r["kind"] == "dead"]
    marks = [r for r in results if r["kind"] == "mark"]
    lines = [
        "# 大地图图标点击探针报告",
        "",
        f"- 基准帧：`{base_name}`",
        f"- 探测 {len(results)} 个候选：**真图标 {len(icons)}**、"
        f"空白(地图标记态) {len(blanks)}、无反应 {len(deads)}、玩家标记 {len(marks)}",
        f"- 点空白 {len(blanks)} 次（✕ 无损退出，标记数不增 —— 已实测 8→8）",
        f"- 视口漂移 {drifts} 次（点击后地图自行平移，已自动复位并重设基准帧）",
        "",
        "## 确认为图标（可入库）",
        "",
        "| 坐标 | 名称 | 帧差 | 新增文字（前3条） |",
        "|---|---|---|---|",
    ]
    for r in icons:
        lines.append(f"| {r['x']},{r['y']} | **{r['name']}** | {r['diff']} | "
                     + " / ".join(x["text"] for x in r["new_texts"][:3]) + " |")
    lines += ["", "## 排除（空白 / 无反应 / 玩家标记）", "",
              "| 坐标 | 反应 | 帧差 | 新增文字（前3条） |", "|---|---|---|---|"]
    for r in blanks + deads + marks:
        lab = {"blank": "空白", "dead": "无反应", "mark": "玩家标记"}[r["kind"]]
        lines.append(f"| {r['x']},{r['y']} | {lab} | {r['diff']} | "
                     + " / ".join(x["text"] for x in r["new_texts"][:3]) + " |")
    with open(os.path.join(out_dir, "report.md"), "w", encoding="utf-8") as f:
        f.write("\n".join(lines) + "\n")

    print(f"\n=== 汇总 ===  真图标 {len(icons)} / 空白 {len(blanks)} / "
          f"无反应 {len(deads)} / 玩家标记 {len(marks)}")
    for r in icons:
        print(f"  ★ {r['name']:24s} @({r['x']},{r['y']})  {r['w']}x{r['h']}  diff={r['diff']}")
    print(f"产出：{out_dir}/probes.json、report.md")
    if a.apply:
        apply_icons(results, a.apply_pad)
    return 0


if __name__ == "__main__":
    sys.exit(main())
