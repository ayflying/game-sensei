#!/usr/bin/env python3
# -*- coding: utf-8 -*-
"""把教学视频判读产物（clips.jsonl）转换成学生训练格式（trajectory.jsonl + frames）。

背景：视频判读产出的是自然语言（局面/操作/策略），训练管线吃的是结构化动作
（up/down/left/right/tap/press/wait/none + tap 坐标）。本脚本做对齐，并统计映射率。

⚠️ 实测结论（2026-09-13，详见 outputs/game-sensei-视频训练可行性实测.md）：
    用本脚本产出的数据集训练学生模型会 **mode collapse**——
    两次实验 val_acc 分别恒定 63.49% / 52.36%（等于最多数类占比），
    50 个 epoch 毫无提升，模型只会"无论看到什么都输出同一类"。
    对照：真机示范仅 60 个样本就有 66.67%。
    根因是视频判读的动作栏是**推断的弱标签**（判读报告自己声明"不可作为训练真值"），
    且段长 19.4 秒 ≫ 训练样本的单帧粒度，35.5% 的段是复合行为。
    本脚本保留为**实验记录与未来改进的起点**——若将来改成「帧对逆动力学」级的
    细粒度标注（相邻帧 → 位移方向），可复用它做格式转换与映射率统计。

用法：
    python video_to_dataset.py --src E:/game-sensei-videos/out --out E:/game-sensei-videos/dataset_v2
"""
import argparse
import json
import os
import re
import sys
from collections import Counter
from pathlib import Path

# 学生动作头类别（必须与 trainer/train.py 的 CLS 一致）
CLS = ["up", "down", "left", "right", "tap", "press", "wait", "none"]

# nrc 档案按钮别名 -> 归一化坐标（tap 类要用）
BUTTONS = {
    "star": ([0.79, 0.84], ["星形", "星星", "魔法", "手掌", "交互"]),
    "wolf": ([0.72, 0.72], ["狼头", "面板", "菜单"]),
    "run": ([0.87, 0.75], ["奔跑", "跑步", "冲刺"]),
    "jump": ([0.87, 0.91], ["跳跃", "跳"]),
    "mount": ([0.71, 0.91], ["坐骑", "切换形态"]),
    "battle_catch": ([0.786, 0.906], ["捕捉", "抓", "投球", "咕噜球"]),
    "battle_switch": ([0.847, 0.906], ["更换", "换宠", "切换精灵"]),
    "battle_bag": ([0.728, 0.904], ["背包", "战斗背包", "道具"]),
    "battle_flee": ([0.664, 0.906], ["逃跑", "逃离战斗", "撤退"]),
    "battle_energy": ([0.085, 0.885], ["聚能", "聚气", "攒能量"]),
    "battle_skill": ([0.908, 0.9], ["技能", "战斗技能"]),
}

# 单一方向词 -> 4 类方向（斜向词在 DIAG_WORDS 里单独隔离，不在此表）
DIR_WORDS = [
    ("up", ["向前", "前方", "朝前", "向上", "上方", "前进"]),
    ("down", ["向后", "后方", "向下", "下方", "后退"]),
    ("left", ["左侧", "向左", "左边", "朝左"]),
    ("right", ["右侧", "向右", "右边", "朝右"]),
]


def match_button(text):
    """文本里是否提到某个按钮，返回 (name, pos) 或 None。"""
    best = None
    for name, (pos, aliases) in BUTTONS.items():
        for a in aliases:
            if a in text:
                # 越具体的按钮优先（battle_* 先于 star 这类泛别名）
                if best is None or len(a) > len(best[2]):
                    best = (name, pos, a)
    return (best[0], best[1]) if best else None


# 斜向词：学生动作头（4 方向）表达不了，单独隔离不混进训练
DIAG_WORDS = ["右上方", "左上方", "右下方", "左下方", "右前方", "左前方", "右后方", "左后方",
              "斜向", "西北", "东北", "西南", "东南", "北偏东", "北偏西"]


def classify(seg):
    """把一段判读映射成 (train_kind, nx, ny, reason, dir_hint)。

    注意：train_kind 必须是 trainer/train.py 认识的**原始轨迹 kind**
    （tap/hold/key/joy/move/swipe/zoom/wait/none），不是最终类别。
    train.py 会自己把 move+raw / key / tap 换算成 8 类。
    早先直接输出 up/press 这类目标类别，导致 631 条被 map_action 静默丢弃。
    """
    action = (seg.get("action") or "")
    strategy = (seg.get("strategy") or "")

    # 1) 显式无操作
    if any(k in action for k in ["无操作", "保持原地不动", "未使用虚拟摇杆", "原地不动", "待机状态"]):
        return ("wait", 0.0, 0.0, "显式无操作", None)

    # 2) 点击类优先于「无操作可学」判定——原文常写「TAP 右下角按钮」，
    #    早先因为只查「点击」二字、漏了「TAP」而被错分成 none。
    is_click = any(k in action for k in ["点击", "TAP", "按钮", "按 "])
    if is_click:
        b = match_button(action)
        if b:
            return ("tap", b[1][0], b[1][1], "按钮:" + b[0], "tap")
        return ("tap", 0.0, 0.0, "点击(无具体按钮)", "tap")

    if "本段无操作可学" in strategy:
        return ("none", 0.0, 0.0, "标记为无可学", None)

    # 3) 移动类：斜向先隔离（4 方向头表达不了斜向）
    if any(k in action for k in ["摇杆", "移动", "奔跑", "前进"]):
        if any(w in action for w in DIAG_WORDS):
            return ("__diag__", 0.0, 0.0, "斜向(隔离)", None)
        for kind, words in DIR_WORDS:
            if any(w in action for w in words):
                return ("move", 0.0, 0.0, "方向:" + kind, kind)

    # 4) 战斗技能 / 对话 / 拾取 -> key（train.py 归为 press 类）
    if any(k in action for k in ["技能", "攻击", "释放", "对话", "拾取", "采集"]):
        return ("key", 0.0, 0.0, "技能/对话/拾取", "press")

    return None


def is_compound(seg):
    """判断一段是否包含多个动作（复合行为）。"""
    action = seg.get("action") or ""
    markers = 0
    if any(k in action for k in ["摇杆", "移动"]):
        markers += 1
    if any(k in action for k in ["点击", "TAP"]):
        markers += 1
    if any(k in action for k in ["切换", "打开", "关闭"]):
        markers += 1
    return markers >= 2


def main():
    ap = argparse.ArgumentParser()
    ap.add_argument("--src", default="E:/game-sensei-videos/out")
    ap.add_argument("--out", default="E:/game-sensei-videos/dataset_v1")
    ap.add_argument("--max-per-clip", type=int, default=1,
                    help="每段取几个代表帧做样本（段是多秒行为，取1帧代表主动作）")
    args = ap.parse_args()

    src = Path(args.src)
    out = Path(args.out)
    out.mkdir(parents=True, exist_ok=True)
    (out / "frames").mkdir(exist_ok=True)

    stat = Counter()
    compound_n = 0
    total = 0
    rows = []

    clips = sorted(src.glob("P*/clips.jsonl"))
    for cl in clips:
        ep = cl.parent.name
        for line in cl.read_text(encoding="utf-8").splitlines():
            line = line.strip()
            if not line:
                continue
            seg = json.loads(line)
            total += 1
            if is_compound(seg):
                compound_n += 1
            r = classify(seg)
            if r is None:
                stat["unmapped"] += 1
                continue
            train_kind, nx, ny, reason, dir_hint = r
            if train_kind == "__diag__":
                stat["diag_isolated"] += 1
                continue
            stat["mapped"] += 1
            stat[dir_hint or train_kind] += 1

            reps = seg.get("reps") or []
            if not reps:
                stat["no_frame"] += 1
                continue
            frame_rel = reps[len(reps) // 2]
            src_frame = cl.parent / frame_rel
            if not src_frame.exists():
                stat["missing_frame"] += 1
                continue
            dst_name = "%s_%04d.jpg" % (ep, seg.get("index", 0))
            dst = out / "frames" / dst_name
            if not dst.exists():
                try:
                    os.link(src_frame, dst)
                except OSError:
                    import shutil
                    shutil.copy2(src_frame, dst)

            # raw 里带方向名供 train.py 的 move 分支解析；另存 note 保留映射依据
            raw = ("move:" + dir_hint) if train_kind == "move" else train_kind
            rows.append({
                "index": seg.get("index", 0),
                "at": "2026-09-11T00:00:00Z",
                "action": "%s:%.3f,%.3f" % (train_kind, nx, ny) if train_kind == "tap" else train_kind,
                "kind": train_kind,
                "nx": nx if train_kind == "tap" else 0.0,
                "ny": ny if train_kind == "tap" else 0.0,
                "dur_ms": 0,
                "parsed": True,
                "latency_ms": 0,
                "out_tokens": 0,
                "raw": raw,
                "note": reason,
                "frame_gray": "frames/" + dst_name,
                "src_episode": ep,
                "src_seconds": seg.get("start_at"),
            })

    with open(out / "trajectory.jsonl", "w", encoding="utf-8") as f:
        for r in rows:
            f.write(json.dumps(r, ensure_ascii=False) + "\n")

    print("=" * 62)
    print("总段数: %d" % total)
    print("可映射: %d (%.1f%%)" % (stat["mapped"], 100.0 * stat["mapped"] / max(1, total)))
    print("未映射: %d (%.1f%%)" % (stat["unmapped"], 100.0 * stat["unmapped"] / max(1, total)))
    print("斜向隔离: %d (%.1f%%)" % (stat["diag_isolated"], 100.0 * stat["diag_isolated"] / max(1, total)))
    print("复合行为段: %d (%.1f%%)" % (compound_n, 100.0 * compound_n / max(1, total)))
    print("实际写出样本: %d" % len(rows))
    print("-" * 62)
    print("类别分布（train.py 口径）:")
    for c in CLS:
        n = stat.get(c, 0)
        if n:
            print("  %-6s %5d (%.1f%%)" % (c, n, 100.0 * n / max(1, stat["mapped"])))
    print("-" * 62)
    print("输出目录: %s" % out)
    print("=" * 62)


if __name__ == "__main__":
    main()
