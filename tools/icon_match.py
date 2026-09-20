# -*- coding: utf-8 -*-
"""图标模板匹配：用一次人工确认的图标样本，在任意截图上自动定位同类图标。

背景（见 docs/nrc-icon-atlas-method.md）：
- 批量学习产出的坐标图谱是「文字 → 坐标」，没有「图标长什么样 → 怎么认出来」这一层；
- 实测本地 9B VLM **无法**可靠分类小尺寸图标（已知蓝色传送锚点被答成"建筑/营地"），
  因此图标命名只能靠「人工看一眼确认一次」，之后靠**模板匹配复用**。
- 本工具就是那个"复用"环节：人工确认的样本 → 模板 → 在多张截图上确定性定位。

用法：
    # 1) 用一次确认的图标样本建模板（也可 --crop 直接从截图裁）
    python tools/icon_match.py --make 眠枭庇护所 --from-shot v05.png --bbox 1544,122,1585,149
    # 2) 在目标截图上搜同名模板
    python tools/icon_match.py --find 眠枭庇护所 --shot v11.png
    # 3) 一次搜全部模板
    python tools/icon_match.py --find-all --shot v11.png --min-score 0.85
    # 4) 机器可读：只回一行「名称 分数 x y」，便于 shell 取坐标
    python tools/icon_match.py --find 眠枭庇护所 --shot v11.png --at
    # 5) 直接点击：命中最佳位置后调 drv.py 点掉（默认点两次，首点常被吞）
    python tools/icon_match.py --find 眠枭庇护所 --shot v11.png --tap

输出纯文字（不读图），供主对话直接消费。
"""
import argparse
import json
import os
import subprocess
import sys
import tempfile
import time

import cv2
import numpy as np

ROOT = os.path.dirname(os.path.dirname(os.path.abspath(__file__)))
LIB_DIR = os.path.join(ROOT, ".workbuddy", "nrc", "icon_atlas", "library")
LIB_JSON = os.path.join(LIB_DIR, "library.json")
DRV = os.path.join(ROOT, ".workbuddy", "nrc", "drv.py")   # 真机驱动（点击链路）


def load_lib():
    if os.path.exists(LIB_JSON):
        with open(LIB_JSON, encoding="utf-8") as f:
            return json.load(f)
    return {"templates": {}}


def save_lib(lib):
    os.makedirs(LIB_DIR, exist_ok=True)
    with open(LIB_JSON, "w", encoding="utf-8") as f:
        json.dump(lib, f, ensure_ascii=False, indent=1)


def imread(path):
    data = np.fromfile(path, dtype=np.uint8)          # 兼容中文路径
    return cv2.imdecode(data, cv2.IMREAD_COLOR)


def describe_name(name):
    """把图标名转成模板文件名（去掉路径非法字符）。"""
    for ch in '\\/:*?"<>|':
        name = name.replace(ch, "_")
    return name


def cmd_make(a):
    lib = load_lib()
    shot = imread(a.from_shot)
    if shot is None:
        print(f"读图失败：{a.from_shot}")
        return 1
    x0, y0, x1, y1 = map(int, a.bbox.split(","))
    # bbox 是图标本体；四向各留 pad 像素邻域，提高匹配鲁棒性
    p = a.pad
    h, w = shot.shape[:2]
    bx0, by0 = max(0, x0 - p), max(0, y0 - p)
    bx1, by1 = min(w, x1 + p + 1), min(h, y1 + p + 1)
    tmpl = shot[by0:by1, bx0:bx1]
    os.makedirs(LIB_DIR, exist_ok=True)
    tpath = os.path.join(LIB_DIR, describe_name(a.make) + ".png")
    # 注意：cv2.imwrite 走 C++ 文件 API，**中文路径会静默写失败/写成乱码名**，
    # 必须 imencode + tofile（实测：imwrite 把「眠枭庇护所.png」写成 GBK 乱码名）。
    ok, buf = cv2.imencode(".png", tmpl)
    buf.tofile(tpath)
    lib["templates"][a.make] = {
        "file": os.path.basename(tpath),
        "size": [int(bx1 - bx0), int(by1 - by0)],
        "core_bbox": [x0, y0, x1, y1],
        "core_size": [int(x1 - x0), int(y1 - y0)],
        "pad": p,
        "from_shot": os.path.basename(a.from_shot),
        "source_center": [int((x0 + x1) / 2), int((y0 + y1) / 2)],
        "note": a.note,
    }
    save_lib(lib)
    print(f"模板已建：{a.make}  尺寸 {bx1-bx0}x{by1-by0}（本体 {x1-x0}x{y1-y0} + pad {p}）")
    print(f"  文件 → {tpath}")
    print(f"  来源 → {os.path.basename(a.from_shot)} 中心 ({int((x0+x1)/2)},{int((y0+y1)/2)})")
    return 0


def match_one(shot, tmpl, scales, min_score):
    """多尺度模板匹配，返回 [(score, cx, cy, w, h), ...] 按分数降序。"""
    hits = []
    th, tw = tmpl.shape[:2]
    for s in scales:
        nw, nh = max(3, int(round(tw * s))), max(3, int(round(th * s)))
        if nw >= shot.shape[1] or nh >= shot.shape[0]:
            continue
        r = cv2.resize(tmpl, (nw, nh), interpolation=cv2.INTER_AREA if s < 1 else cv2.INTER_CUBIC)
        res = cv2.matchTemplate(shot, r, cv2.TM_CCOEFF_NORMED)
        _, mx, _, mxloc = cv2.minMaxLoc(res)
        if mx < min_score:
            continue
        hits.append({
            "score": float(mx), "scale": float(s),
            "cx": int(mxloc[0] + nw / 2), "cy": int(mxloc[1] + nh / 2),
            "w": nw, "h": nh,
        })
    hits.sort(key=lambda h: -h["score"])
    # 抑制重叠命中（同一图标在多个尺度上重复报）
    keep = []
    for h in hits:
        if any(abs(h["cx"] - k["cx"]) < 14 and abs(h["cy"] - k["cy"]) < 14 for k in keep):
            continue
        keep.append(h)
    return keep


def _adb_grab(adb, serial, dst):
    """adb 直连抓一帧到本地（exec-out screencap -p）。成功返回 True。"""
    try:
        p = subprocess.run([adb, "-s", serial, "exec-out", "screencap", "-p"],
                           capture_output=True, timeout=60)
    except Exception:
        return False
    if not p.stdout or len(p.stdout) < 2000:
        return False
    try:
        with open(dst, "wb") as f:
            f.write(p.stdout)
    except OSError:
        return False
    return True


def _frame_diff(a, b):
    """两帧灰度降采样后的平均绝对差（与 drv.py diff 同口径）。失败返回 None。"""
    try:
        ia = cv2.imread(a, cv2.IMREAD_GRAYSCALE)
        ib = cv2.imread(b, cv2.IMREAD_GRAYSCALE)
        if ia is None or ib is None:
            return None
        ia = cv2.resize(ia, (320, 148)).astype(float)
        ib = cv2.resize(ib, (320, 148)).astype(float)
        return float(np.abs(ia - ib).mean())
    except Exception:
        return None


def tap_hit(hit, a):
    """把匹配到的图标直接点掉 —— 把「认图标」串进点击链路。

    ⚠️ **别盲点两次**：本作地图上的图标有相当一部分是 **toggle**
    （点一次开面板、再点一次关面板）。盲点两次时若两次都生效 ⇒ 开+关
    ⇒ **逐像素回到原状（帧差 0.00）**，看起来和"点击根本没发出去"完全一样。
    所以 `--tap-times` 默认已改为 **1**；需要"点一下没反应就再点一下"时，
    用 **`--tap-verify`**（点后抓帧算帧差，与基准帧几乎无变化才补点一次）——
    自适应补点让"首点被吞"和"toggle 已生效"两种情况都收敛到"已打开"。

    ⚠️ 这里曾写过一个**错误结论**，一并更正（2026-09-21）：当时观察到
    "python 内 subprocess 调 drv.py 点不动、命令行直接调就有效"，便断言
    "沙箱会静默拦掉经 ≥2 层 python 的 input 注入"，并据此改了三处代码。
    **因果推断是错的** —— 真相是那批对照实验里两次的**点击次数不同**
    （一次 vs 两次），撞上了上面的 toggle 行为。判定实验（同一脚本内交替点、
    每步只点一次）：探针点地图中部 23.19、点目标 18.42、再点一次又 18.42（与起点同画面）。
    教训：**拿"点不动"当结论前先固定点击次数做对照**；
    诊断脚本要**一次只变一个变量**。
    直连 adb 仍然保留，但理由只是"少一层进程、更快"。
    """
    adb = os.environ.get("NRC_ADB",
                         r"C:/Users/ay/AppData/Local/Android/Sdk/platform-tools/adb.exe")
    serial = os.environ.get("NRC_SERIAL", "ecbff3a5")
    if not os.path.exists(adb):
        print(f"  未找到 adb：{adb}（可用 NRC_ADB 环境变量指定）", file=sys.stderr)
        return
    devnull = open(os.devnull, "wb")
    try:
        def _tap(tag=""):
            subprocess.run([adb, "-s", serial, "shell", "input", "tap",
                            str(int(hit["cx"])), str(int(hit["cy"]))],
                           stdout=devnull, stderr=devnull)
            print(f"  tap#{tag}({hit['cx']},{hit['cy']}) 等待 {a.tap_wait}ms", file=sys.stderr)
            time.sleep(a.tap_wait / 1000.0)

        for i in range(a.tap_times):
            _tap(str(i + 1))

        if a.tap_verify:
            tmp = os.path.join(tempfile.gettempdir(), "_icon_match_verify.png")
            for _ in range(2):
                if not _adb_grab(adb, serial, tmp):
                    print("  验证：抓帧失败，跳过", file=sys.stderr)
                    break
                d = _frame_diff(a.shot, tmp)
                if d is None:
                    break
                if d >= a.verify_min:
                    print(f"  验证：帧差 {d:.2f} ⇒ 已生效", file=sys.stderr)
                    break
                print(f"  验证：帧差 {d:.2f} < {a.verify_min} ⇒ 疑首点被吞，补点一次", file=sys.stderr)
                _tap("补")
            try:
                os.remove(tmp)
            except OSError:
                pass
    finally:
        devnull.close()


def cmd_find(a):
    lib = load_lib()
    shot = imread(a.shot)
    if shot is None:
        print(f"读图失败：{a.shot}")
        return 1
    names = [a.find] if a.find and a.find != "*" else list(lib["templates"])
    if not names:
        print("图标库为空。先用 --make 建模板。")
        return 1
    scales = [float(x) for x in a.scales.split(",")]
    # --at / --tap 是机器可读模式：只回一行结果，不打人读标题（便于 shell 取值）
    machine = a.at or a.tap
    if not machine:
        print(f"目标截图 {os.path.basename(a.shot)}  {shot.shape[1]}x{shot.shape[0]}   尺度 {scales}")
    total = 0
    best = None
    rows = []          # 每模板命中，供 --at 多模板时逐行输出（一条命令巡检全库）
    for n in names:
        meta = lib["templates"].get(n)
        if not meta:
            if not machine:
                print(f"  未找到模板：{n}")
            continue
        tmpl = imread(os.path.join(LIB_DIR, meta["file"]))
        if tmpl is None:
            if not machine:
                print(f"  模板图读取失败：{meta['file']}")
            continue
        hits = match_one(shot, tmpl, scales, a.min_score)
        rows.append((n, hits))
        total += len(hits)
        for h in hits:
            h["name"] = n
            if best is None or h["score"] > best["score"]:
                best = h
        if not machine:
            print(f"\n【{n}】命中 {len(hits)} 处（阈值 {a.min_score}）")
            for h in hits[:a.top]:
                print(f"   分数 {h['score']:.3f}  中心 ({h['cx']},{h['cy']})  "
                      f"{h['w']}x{h['h']}  尺度 {h['scale']}")
    if machine:
        if a.tap:
            # --tap 只点**全局最佳**一处：同时点多个模板没有意义（面板会被第一个点开）
            if best is None:
                print(f"MISS 阈值 {a.min_score}  {os.path.basename(a.shot)}")
                return 1
            print(f"{best['name']} {best['score']:.3f} {best['cx']} {best['cy']}")
            tap_hit(best, a)
            return 0
        if len(names) > 1:
            # 多模板巡检：**每个模板一行**（未命中回 MISS），rc=0 只要有一个命中。
            # 单模板（--find X --at）保持原有单行语义不变，便于 shell 取值。
            any_hit = False
            for n, hits in rows:
                if not hits:
                    print(f"{n} MISS")
                    continue
                any_hit = True
                for h in hits[:a.top]:
                    print(f"{n} {h['score']:.3f} {h['cx']} {h['cy']} {h['w']}x{h['h']}")
            return 0 if any_hit else 1
        if best is None:
            # 无命中回 MISS + 返回码 1，外层脚本据此走「先确认图标在屏」分支
            print(f"MISS 阈值 {a.min_score}  {os.path.basename(a.shot)}")
            return 1
        print(f"{best['name']} {best['score']:.3f} {best['cx']} {best['cy']}")
        return 0
    print(f"\n合计命中 {total} 处")
    return 0


def cmd_list(a):
    lib = load_lib()
    print(f"图标库 {LIB_JSON}   模板 {len(lib['templates'])} 个")
    for n, m in lib["templates"].items():
        print(f"   {n:12s} {m['size'][0]}x{m['size'][1]}  来源 {m['from_shot']} "
              f"中心 {m['source_center']}  备注 {m.get('note') or '-'}")
    return 0


def cmd_rename(a):
    """改名：模板文件名 + library.json 键一起改，避免手工改 JSON 后文件名对不上。

    用途：点击探针按 OCR 文本自动命名，个别字会误识（实测「眠枭庇护所」读成「眠底护所」）
    ⇒ 人工核对后一条命令改正。库内顺序保持（重建 dict），不把条目挪到末尾。
    """
    lib = load_lib()
    tmpl = lib["templates"]
    if "=" not in a.rename:
        print("--rename 需要「旧名=新名」格式，例如 --rename 眠底护所=眠枭庇护所")
        return 1
    old, new = (s.strip() for s in a.rename.split("=", 1))
    if not old or not new:
        print("--rename 需要「旧名=新名」格式")
        return 1
    if old not in tmpl:
        print(f"库中没有「{old}」")
        return 1
    if new in tmpl and new != old:
        print(f"「{new}」已存在，拒绝覆盖（如确要替换先 --drop {new}）")
        return 1
    meta = tmpl[old]
    old_path = os.path.join(LIB_DIR, meta["file"])
    ext = os.path.splitext(meta["file"])[1] or ".png"
    new_file = new + ext
    new_path = os.path.join(LIB_DIR, new_file)
    if os.path.exists(old_path):
        os.replace(old_path, new_path)
    meta["file"] = new_file
    meta["note"] = ((meta.get("note") or "")
                    + f"；改名：「{old}」→「{new}」" + (f"（{a.note}）" if a.note else "")).lstrip("；")
    lib["templates"] = {new if k == old else k: (meta if k == old else v)
                        for k, v in tmpl.items()}
    save_lib(lib)
    print(f"已改名：「{old}」→「{new}」  模板文件 {new_file}")
    return 0


def cmd_drop(a):
    """下架条目：模板文件移入 library/_dropped/（不真删，留可恢复），并从清单摘除。

    用途：误建的重复/噪声条目。留 _dropped/ 是因为模板可复现性依赖来源帧，
    而来源帧常是临时截图、会被清理 —— 移走比删除安全。
    """
    lib = load_lib()
    tmpl = lib["templates"]
    names = [n.strip() for n in a.drop.split(",") if n.strip()]
    if not names:
        print("--drop 需要模板名（多个用逗号分隔）")
        return 1
    gone, miss = [], []
    trash = os.path.join(LIB_DIR, "_dropped")
    for n in names:
        if n not in tmpl:
            miss.append(n)
            continue
        meta = tmpl.pop(n)
        p = os.path.join(LIB_DIR, meta["file"])
        if os.path.exists(p):
            os.makedirs(trash, exist_ok=True)
            os.replace(p, os.path.join(trash, meta["file"]))
        gone.append(n)
    if gone:
        save_lib(lib)
    print(f"已下架 {len(gone)} 条：{'、'.join(gone) or '-'}"
          + (f"（模板文件移入 {os.path.relpath(trash, ROOT)}）" if gone else ""))
    if miss:
        print(f"库中没有：{'、'.join(miss)}")
    return 0 if gone else 1


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--make", default="", help="新建/覆盖模板名")
    ap.add_argument("--from-shot", default="", help="模板来源截图")
    ap.add_argument("--bbox", default="", help="图标本体像素框 x0,y0,x1,y1")
    ap.add_argument("--pad", type=int, default=3, help="四向额外留边")
    ap.add_argument("--note", default="", help="备注")
    ap.add_argument("--find", default="", help="要搜的模板名；传 * 搜全部")
    ap.add_argument("--find-all", action="store_true", help="搜全部模板")
    ap.add_argument("--shot", default="", help="目标截图")
    # 默认只跑尺度 1.0：实测（09-21）MIX3 2340×1080 上全库 22 条命中**全部落在尺度 1.0**，
    # 游戏 UI 图标是固定像素尺寸、不随视口缩放。原默认 7 个尺度（0.85~1.15）让
    # 「24 模板 × 7 尺度 = 168 次全图 matchTemplate」白跑 6/7，单次全库定位 20.6s；
    # 砍到单尺度后 ≈3s。需要容忍缩放的场景（跨分辨率复用模板）显式传 --scales 即可。
    ap.add_argument("--scales", default="1.0", help="多尺度列表。默认 1.0（游戏 UI 图标不缩放）；"
                                                   "跨分辨率复用模板时才需要如 0.9,0.95,1.0,1.05,1.1")
    ap.add_argument("--min-score", type=float, default=0.85,
                    help="匹配分下限。实测：真命中 ≥0.95（同图标跨视口 0.951~0.986），"
                         "0.55~0.65 是水体/地形误报——2026-09-20 用 0.55 在风息山口帧上"
                         "搜出 3 处，只有 0.986 那处点的出面板，另两处点开的是「标记」面板")
    ap.add_argument("--top", type=int, default=10, help="每个模板最多报几处")
    ap.add_argument("--at", action="store_true",
                    help="机器可读：只回一行「名称 分数 x y」（无命中回 MISS 且返回码 1），便于 shell 取坐标")
    ap.add_argument("--tap", action="store_true",
                    help="命中最佳位置后直接点击（隐含 --at；默认点 1 次，见 --tap-verify）")
    ap.add_argument("--tap-times", type=int, default=1,
                    help="--tap 的点击次数（默认 1）。⚠️ 别盲目调大：本作图标多为 toggle，"
                         "点两次会「开了又关」、帧差 0.00，看起来像点不动。想容错请用 --tap-verify")
    ap.add_argument("--tap-wait", type=int, default=1500, help="--tap 每次点击后等待毫秒")
    ap.add_argument("--tap-verify", action="store_true",
                    help="点击后抓帧算帧差，与基准帧（--shot）几乎无变化才补点一次（自适应，推荐）")
    ap.add_argument("--verify-min", type=float, default=3.0,
                    help="--tap-verify 的「已生效」帧差阈值（默认 3.0）")
    ap.add_argument("--list", action="store_true", help="列出图标库")
    ap.add_argument("--rename", default="",
                    help="改名：「旧名=新名」（模板文件名与清单键一起改，可配 --note 记原因）")
    ap.add_argument("--drop", default="", help="下架条目：模板名，多个逗号分隔（文件移入 _dropped/）")
    a = ap.parse_args()

    if a.list:
        return cmd_list(a)
    if a.rename:
        return cmd_rename(a)
    if a.drop:
        return cmd_drop(a)
    if a.make:
        if not (a.from_shot and a.bbox):
            print("--make 需要 --from-shot 与 --bbox")
            return 1
        return cmd_make(a)
    if a.find or a.find_all or a.at or a.tap:
        if not a.shot:
            print("--find/--at/--tap 需要 --shot")
            return 1
        return cmd_find(a)
    ap.print_help()
    return 0


if __name__ == "__main__":
    sys.exit(main())
