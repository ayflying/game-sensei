"""game-sensei 学生模型训练器（模仿学习 / BC，torch 实现）。

从 internal/dataset 落盘的老师示范（trajectory.jsonl + frames/）训练一个
轻量学生策略网络，导出**纯权重文件**（.weights.json）供 Go 运行时
internal/student 纯 Go 前向传播加载——零 CGO、零外部依赖。

为什么训练用 torch、推理用纯 Go：
  - 训练要 autograd，torch 最稳；推理只要前向，网络极小（3 层 CNN），
    纯 Go <1ms，且不用给 Go 运行时分发 onnxruntime.dll。
  - 权重文件是唯一接口，Python 侧导出顺序与 Go 侧加载顺序一一对应。

学生输出两层（与 L1 动作空间对齐，跨游戏通用）：
  - 分类头：12 类 = 8 向（与 internal/agent.AllDirs 同序）+ tap/press/wait/none
  - 回归头：tap 的归一化坐标 (x, y)

用法：
  C:/Users/ay/.workbuddy/binaries/python/envs/default/Scripts/python.exe \
      trainer/train.py --data .workbuddy/demos/jieyou_01 --out models/jieyou_v1.weights.json

增量训练（后续真机示范到了，在已有模型上继续训）：
  C:/Users/ay/.workbuddy/binaries/python/envs/default/Scripts/python.exe \
      trainer/train.py --data .workbuddy/demos/nrc_real_01 \
      --init models/video_v1.weights.json --freeze-backbone \
      --epochs 30 --lr 3e-4 --out models/video_v1_real_v1.weights.json

  --init 从已有 .weights.json 热启动（载入全部权重再继续训），
  --freeze-backbone 额外冻结三层卷积、只训分类/坐标头——
  真机样本往往只有几十条，全量微调会把预训练视觉特征冲掉（灾难性遗忘），
  冻结骨干只让「决策头」适配新数据更稳。产出的 meta 会记录 parent 形成血缘链。
"""

from __future__ import annotations

import argparse
import json
import subprocess
import sys
from collections import Counter
from pathlib import Path

import numpy as np

# 类别顺序必须与 Go 侧 internal/student.Classes 逐项一致（索引即标签）。
#
# 为什么是 8 向而不是 4 向：L1 动作空间本来就是 8 向（internal/agent.AllDirs），
# 老师用的大模型天然输出 8 向（实测 nrc 真机示范 35 条移动**全是斜向**：
# up_right 11 / down_right 23 / up_left 1，没有一条正向前后左右）。
# 学生头若只留 4 向，这些斜向示范就只能被硬塞进 up/down，
# 等于用**错标的样本**训练——模型学不会方向，而且全程不报错。
CLS = [
    "up", "down", "left", "right",
    "up_left", "up_right", "down_left", "down_right",
    "tap", "press", "wait", "none",
]

# 方向名集合（与 internal/agent.AllDirs 同集合；顺序见 CLS）。
DIRS = tuple(CLS[:8])

# 类别权重：老师示范里 none/wait 常占大头，不加权学生会学会「永远不动」。
# 权重只对**出现过的类**起作用；缺失类会被 --allow-missing-classes 显式点名。
CLASS_WEIGHTS = np.array([0.3] * 8 + [1.5, 1.5, 0.5, 0.5], dtype=np.float32)
assert len(CLASS_WEIGHTS) == len(CLS), "CLASS_WEIGHTS 长度必须与 CLS 一致"


# ---------------------------------------------------------------------------
# 数据：读取 demo 数据集
# ---------------------------------------------------------------------------

def load_demos(data_dir: Path, recursive: bool, rgb: bool = False,
               in_h: int = 48) -> tuple[list[dict], dict]:
    """读一个或多个示范目录，返回 (样本列表, 普查统计)。

    每个样本 = {"frame": float32[C,H,W]（rgb）或 [H,W]（灰度）, "cls": int,
                "x": float, "y": float, "pref": str(局级 label)}

    另返回 stats 供**类别普查**使用（见 main 的空类检查）：
      - raw_kinds: 原始 kind 计数（none/move/press...）
      - mapped:    映射到的学生类计数
      - dropped:   map_action 返回 None 而丢掉的条数（按 kind 计）
    """
    from PIL import Image

    if recursive:
        tjs = sorted(data_dir.rglob("trajectory.jsonl"))
    else:
        tjs = [data_dir / "trajectory.jsonl"]
    tjs = [p for p in tjs if p.exists()]
    if not tjs:
        sys.exit(f"在 {data_dir} 没找到 trajectory.jsonl")

    samples: list[dict] = []
    raw_kinds: Counter = Counter()
    mapped: Counter = Counter()
    dropped: Counter = Counter()
    for tj in tjs:
        root = tj.parent
        n_ok = 0
        with open(tj, encoding="utf-8") as f:
            for line in f:
                line = line.strip()
                if not line:
                    continue
                s = json.loads(line)
                if not s.get("parsed", False):
                    continue  # 老师的废输出不是可模仿行为
                raw_kinds[str(s.get("kind", ""))] += 1
                frame_path = root / s["frame_gray"]
                if not frame_path.exists():
                    continue
                kind, x, y = map_action(s)
                if kind is None:
                    dropped[str(s.get("kind", ""))] += 1
                    continue
                img = Image.open(frame_path).convert("RGB" if rgb else "L")
                arr = center_crop_resize(img, 64, in_h)
                if rgb:
                    arr = np.transpose(arr, (2, 0, 1))  # [H,W,3] -> [3,H,W]
                samples.append({"frame": arr, "cls": CLS.index(kind), "x": x, "y": y,
                                "pref": s.get("label") or ""})
                mapped[kind] += 1
                n_ok += 1
        print(f"  {tj.parent.name}: {n_ok} 个有效样本")
    print(f"共加载 {len(samples)} 个样本")
    if len(samples) < 20:
        sys.exit("样本太少，先采集更多示范（-demo-steps 建议 60+）")
    return samples, {"raw_kinds": raw_kinds, "mapped": mapped, "dropped": dropped}


def parse_dir(s: str) -> str | None:
    """把方向写法归一化成 DIRS 之一；无法识别返回 None。

    与 Go 侧 internal/agent.ParseDir 的**语义**对齐（大小写不敏感，
    `-`/空格/`/` 当作 `_`）。

    ⚠️ 刻意不做子串匹配。原实现是
        for d in ("up","down","left","right"):
            if d in raw: return d
    于是 raw="ACTION MOVE dir=up_right" 会被 "up" 先命中 → up_right/up_left
    被静默塞成 up。这不是「粗略但可用」，而是**把斜向示范错标成正向**：
    方向相反的两条示范拿到同一个标签，学生永远学不会方向，且没有任何报错。
    """
    k = (s or "").strip().lower()
    for ch in ("-", " ", "\t", "/"):
        k = k.replace(ch, "_")
    while "__" in k:
        k = k.replace("__", "_")
    k = k.strip("_")
    return k if k in DIRS else None


def map_action(s: dict) -> tuple[str | None, float, float]:
    """把 trajectory.jsonl 的一行映射到 (类别, tap_x, tap_y)。

    ⚠️ 这里认的是**原始轨迹 kind**（move/key/joy/press...），不是目标类别
    （up/press），按直觉输出会静默丢样本。kind 的取值由 Go 侧
    internal/dataset.KindName 决定，加新动作类型时要两边一起改。
    """
    kind = s.get("kind", "")
    if kind in ("tap", "hold"):
        return "tap", float(s.get("nx", 0.5)), float(s.get("ny", 0.5))
    if kind in ("key", "press"):
        # key = 系统键；press = 按「命名按钮」（游戏档案里的按钮/宏）。
        # 两者都落进学生的 press 类——学生头目前只输出「按一下」这个意图，
        # 不表达按的是哪个按钮（按钮名在字段 action 里）。
        # ⚠️ 已知天花板：cmd/helper/student.go 的适配层把 press 一律翻译成
        # 「按档案第一个按钮」，所以老师的战斗宏（聚能/赫突/逃跑）即便被学生
        # 学会了「此刻该按」，也按不对按钮。要真正复现战斗策略，得给学生加一个
        # 「按钮头」（对档案 Buttons 做多分类）——那是下一版的事，不是数据问题。
        return "press", 0.0, 0.0
    if kind == "joy":
        dx = float(s.get("nx2", 0.5)) - float(s.get("nx", 0.5))
        dy = float(s.get("ny2", 0.5)) - float(s.get("ny", 0.5))
        return dir_of(dx, dy), 0.0, 0.0
    if kind == "move":
        # 方向来自结构化字段 action（"move:up_right/1200ms"）或
        # raw（"ACTION MOVE dir=up_right dur=1200"）。两条都按 token 扫，
        # 不靠子串命中——理由见 parse_dir 的注释。
        for src in (s.get("action", ""), s.get("raw", "")):
            text = str(src).replace("=", " ").replace(":", " ").replace("/", " ")
            for tok in text.split():
                d = parse_dir(tok)
                if d:
                    return d, 0.0, 0.0
        return None, 0.0, 0.0
    if kind == "swipe":
        # 学生头暂无 swipe 类：拖拽平移示范降级成 wait，不模仿成「点起点」
        # （那是错误行为——拖拽要按住移动，点一下起点什么都不会发生）。
        # 后续扩学生动作头时（swipe 方向类）再收回。
        return "wait", 0.0, 0.0
    if kind == "zoom":
        # 学生头暂无 zoom 类：缩放示范降级成 wait（不模仿成乱点）。
        # 后续扩学生动作头时（zoom in/out 独立类）再收回。
        return "wait", 0.0, 0.0
    if kind == "wait":
        return "wait", 0.0, 0.0
    if kind == "none":
        return "none", 0.0, 0.0
    return None, 0.0, 0.0


def dir_of(dx: float, dy: float) -> str:
    """摇杆位移 → 8 向之一（与 CLS 的方向集合一致）。

    斜向判据：两个分量同量级（小的 ≥ 大的 40%）才算斜向——
    推杆时的天然抖动不该把「向前」判成「右前」。
    """
    ax, ay = abs(dx), abs(dy)
    if ax == 0 and ay == 0:
        return "up"  # 零位移：摇杆示范里不存在，退化为最保守的前
    diag = min(ax, ay) >= 0.4 * max(ax, ay)
    v = "up" if dy < 0 else "down"
    h = "right" if dx > 0 else "left"
    if diag:
        return f"{v}_{h}"
    return h if ax > ay else v


def center_crop_resize(img, w: int, h: int) -> np.ndarray:
    """居中裁剪到目标长宽比再缩放——竖屏/横屏画面落到同一坐标系（通用性关键）。

    ⚠️ 重采样**必须显式 NEAREST**，与 Go 侧 internal/student/preprocess 的最近邻
    逐像素对齐（2026-09-16 实测：PIL resize 省略 resample 时对 RGB/L 默认 BICUBIC，
    与 Go 的 NEAREST 是**训练/推理预处理错配**——边界帧的 logits 会翻转，
    如 v6 的 pick01：BICUBIC 判 none(4.99/4.87) vs NEAREST 判 tap(5.24/5.49)）。
    """
    W, H = img.size
    target_ratio = w / h
    from PIL import Image  # 延迟导入与 load_demos 一致（torch/PIL 不做顶层依赖）
    if W / H > target_ratio:
        nw = int(H * target_ratio)
        x0 = (W - nw) // 2
        img = img.crop((x0, 0, x0 + nw, H))
    else:
        nh = int(W / target_ratio)
        y0 = (H - nh) // 2
        img = img.crop((0, y0, W, y0 + nh))
    img = img.resize((w, h), Image.NEAREST)
    return np.asarray(img, dtype=np.float32) / 255.0


# ---------------------------------------------------------------------------
# 网络定义（结构必须与 Go 侧 internal/student 前向完全一致）
# ---------------------------------------------------------------------------

def build_model(hidden: int = 64, in_ch: int = 1):
    import torch
    import torch.nn as nn

    class StudentNet(nn.Module):
        """conv1(in_ch->8,3x3)+relu+pool2 -> conv2(8->16,3x3)+relu+pool2
        -> conv3(16->24,3x3)+relu+GAP -> fc(24->hidden)+relu
        -> cls_head(hidden->8) / coord_head(hidden->2)+sigmoid"""

        def __init__(self):
            super().__init__()
            self.conv1 = nn.Conv2d(in_ch, 8, 3)
            self.conv2 = nn.Conv2d(8, 16, 3)
            self.conv3 = nn.Conv2d(16, 24, 3)
            self.fc = nn.Linear(24, hidden)
            self.cls_head = nn.Linear(hidden, len(CLS))
            self.coord_head = nn.Linear(hidden, 2)

        def forward(self, x):
            # x: [N,1,H,W]
            x = torch.relu(self.conv1(x))
            x = torch.max_pool2d(x, 2)
            x = torch.relu(self.conv2(x))
            x = torch.max_pool2d(x, 2)
            x = torch.relu(self.conv3(x))
            x = x.mean(dim=(2, 3))            # GAP
            x = torch.relu(self.fc(x))
            return self.cls_head(x), torch.sigmoid(self.coord_head(x))

    return StudentNet()


def export_weights(model, hidden: int, out_path: Path, meta: dict):
    """把 torch 权重导出为 JSON（float32 数组），Go 侧按同名键加载。

    Conv2d 权重布局 [O,C,kh,kw] 按行主序展平；Linear [out,in]。
    Go 侧卷积同样按 OCHW 行主序读取。
    """
    def flat(t) -> list[float]:
        return [round(float(v), 6) for v in t.detach().cpu().numpy().ravel()]

    doc = {
        "format": "game-sensei-student-weights",
        "version": 1,
        "meta": meta,
        "arch": {"hidden": hidden, "classes": CLS},
        "weights": {
            "conv1_w": flat(model.conv1.weight),
            "conv1_b": flat(model.conv1.bias),
            "conv2_w": flat(model.conv2.weight),
            "conv2_b": flat(model.conv2.bias),
            "conv3_w": flat(model.conv3.weight),
            "conv3_b": flat(model.conv3.bias),
            "fc_w": flat(model.fc.weight),
            "fc_b": flat(model.fc.bias),
            "cls_w": flat(model.cls_head.weight),
            "cls_b": flat(model.cls_head.bias),
            "coord_w": flat(model.coord_head.weight),
            "coord_b": flat(model.coord_head.bias),
        },
    }
    out_path.parent.mkdir(parents=True, exist_ok=True)
    out_path.write_text(json.dumps(doc), encoding="utf-8")
    size_kb = out_path.stat().st_size / 1024
    print(f"权重已导出: {out_path} ({size_kb:.0f} KB)")


# ---------------------------------------------------------------------------
# 权重导入（增量训练 --init 用）
# ---------------------------------------------------------------------------

def import_weights_json(path: Path, hidden: int) -> tuple[dict, dict]:
    """读 .weights.json 还原成 torch state_dict，供 --init 热启动。

    返回 (state_dict, meta)。格式/版本/hidden/类别/字段长度任一不一致都直接报错退出——
    **静默用随机权重继续训**是最坏的结果：看着像「在已有模型上加训」，
    实际是重训，而且不会有人发现。
    """
    import torch

    doc = json.loads(Path(path).read_text(encoding="utf-8"))
    if doc.get("format") != "game-sensei-student-weights":
        sys.exit(f"--init 不是学生权重文件: {path}")
    if doc.get("version") != 1:
        sys.exit(f"--init 权重版本不支持: {doc.get('version')}")

    arch = doc.get("arch") or {}
    src_hidden = int(arch.get("hidden") or 0)
    if src_hidden != hidden:
        sys.exit(f"--init 权重 hidden={src_hidden} 与本次 --hidden={hidden} 不一致")
    if arch.get("classes") and list(arch["classes"]) != CLS:
        sys.exit("--init 权重的类别顺序与当前 CLS 不一致，拒绝加载")

    # JSON 键名 -> (state_dict 键名, 形状)，与 export_weights 的导出顺序一一对应。
    # 输入通道数由 conv1_w 长度自然携带（72=灰度 / 216=RGB），与 Go 侧 Load 同一约定。
    c1_len = len((doc.get("weights") or {}).get("conv1_w") or [])
    if c1_len not in (72, 216):
        sys.exit(f"--init conv1_w 长度 {c1_len} 无法推导输入通道（支持 72=1ch / 216=3ch）")
    in_ch = c1_len // 72
    shapes = {
        "conv1_w": ("conv1.weight", (8, in_ch, 3, 3)), "conv1_b": ("conv1.bias", (8,)),
        "conv2_w": ("conv2.weight", (16, 8, 3, 3)), "conv2_b": ("conv2.bias", (16,)),
        "conv3_w": ("conv3.weight", (24, 16, 3, 3)), "conv3_b": ("conv3.bias", (24,)),
        "fc_w": ("fc.weight", (hidden, 24)), "fc_b": ("fc.bias", (hidden,)),
        "cls_w": ("cls_head.weight", (len(CLS), hidden)), "cls_b": ("cls_head.bias", (len(CLS),)),
        "coord_w": ("coord_head.weight", (2, hidden)), "coord_b": ("coord_head.bias", (2,)),
    }
    w = doc.get("weights") or {}
    sd = {}
    for jkey, (skey, shape) in shapes.items():
        v = w.get(jkey)
        n = 1
        for d in shape:
            n *= d
        if v is None or len(v) != n:
            got = "缺失" if v is None else len(v)
            sys.exit(f"--init 权重字段 {jkey} 长度 {got} != 期望 {n}")
        sd[skey] = torch.tensor(v, dtype=torch.float32).reshape(shape)
    return sd, (doc.get("meta") or {})


def git_short_commit() -> str:
    """当前 HEAD 短哈希（模型留档用）；不在 git 仓库时返回 unknown。"""
    try:
        out = subprocess.run(["git", "rev-parse", "--short", "HEAD"],
                             capture_output=True, text=True, timeout=5)
        return out.stdout.strip() or "unknown"
    except Exception:
        return "unknown"


# ---------------------------------------------------------------------------
# 类别普查与切分（防「静默白干」）
# ---------------------------------------------------------------------------

def print_census(stats: dict, label_counts: list[int]) -> None:
    """打印原始 kind → 学生类的映射结果与每类样本数。

    这一步存在的理由：训练器最危险的失败不是报错，而是**不报错**。
    已经踩过两次（战斗按钮被记成 kind=none、斜向被塞成正向），都是
    「跑完了、数字看着还行、但学到的是错的东西」。所以每次训练先把映射摊开。
    """
    print("\n== 类别普查 ==")
    print(f"  原始 kind: {dict(stats['raw_kinds'].most_common())}")
    if stats["dropped"]:
        print(f"  ⚠️ 被丢弃（映射不到学生类）: {dict(stats['dropped'])}")
    print(f"  {'类别':<12}{'样本':>6}")
    for i, name in enumerate(CLS):
        n = label_counts[i]
        print(f"  {name:<12}{n:>6}{'   ❌ 空类' if n == 0 else ''}")


def check_missing_classes(label_counts: list[int], allow: bool) -> list[str]:
    """空类检查：CLS 里任何一类 0 样本，对应 head 就永远拿不到训练信号。

    默认**直接退出**——「12 类里 7 类是空的」这种模型，val_acc 无论多高都
    不能解释成能力，继续训只会产出一个看着能用的废模型。
    确需先跑通链路时加 --allow-missing-classes，空类清单会如实写进 meta。
    """
    missing = [CLS[i] for i, n in enumerate(label_counts) if n == 0]
    if not missing:
        return []
    msg = (f"以下 {len(missing)}/{len(CLS)} 个类别在本数据集里 0 样本，"
           f"对应 head 不会得到任何训练信号:\n    {missing}\n"
           f"  → 该模型不能作为能力交付。补采这些类的示范，或明确加 "
           f"--allow-missing-classes 只求跑通链路（空类清单会写进 meta）。")
    if not allow:
        sys.exit(msg)
    print("\n⚠️  " + msg.replace("\n", "\n  "))
    return missing


def stratified_split(label_ids: list[int], val_frac: float, seed: int
                     ) -> tuple[list[int], list[int], list[str]]:
    """按类别分层切分 train/val，返回 (train_idx, val_idx, 告警)。

    为什么不能随机切：样本量小时随机切分很容易让某一类**整个落进训练集**
    或**整个落进验证集**。后者让该类在 val 上必然算错（拉低数字且无从解释），
    前者让 val 完全测不到该类。原实现还额外用 `max(4, ...)` 硬留 4 条 val，
    在 59 样本时等于吃掉 7% 训练数据。

    每类至少留 1 条在训练集；只有 1 条的类无法同时进两边，记入告警。
    """
    from collections import defaultdict
    rng = np.random.RandomState(seed)
    by_class: dict[int, list[int]] = defaultdict(list)
    for i, c in enumerate(label_ids):
        by_class[c].append(i)

    tr: list[int] = []
    va: list[int] = []
    warn: list[str] = []
    for c in sorted(by_class):
        idxs = list(by_class[c])
        rng.shuffle(idxs)
        if len(idxs) == 1:
            warn.append(f"{CLS[c]} 只有 1 个样本，只能进训练集，验证集测不到它")
            tr += idxs
            continue
        n_v = max(1, min(int(round(len(idxs) * val_frac)), len(idxs) - 1))
        va += idxs[:n_v]
        tr += idxs[n_v:]
    tr.sort()
    va.sort()
    return tr, va, warn


def per_class_report(truth: list[int], pred: list[int]) -> tuple[dict, dict]:
    """返回 (各类召回, 预测分布)——用来一眼看出 mode collapse。"""
    from collections import Counter as _C
    recall: dict[str, dict] = {}
    for i, name in enumerate(CLS):
        tot = sum(1 for t in truth if t == i)
        if tot:
            hit = sum(1 for t, p in zip(truth, pred) if t == i and p == i)
            recall[name] = {"n": tot, "recall": round(hit / tot, 3)}
    return recall, dict(_C(CLS[p] for p in pred))


# ---------------------------------------------------------------------------
# 训练主流程
# ---------------------------------------------------------------------------

def main() -> None:
    import torch
    import torch.nn as nn

    ap = argparse.ArgumentParser(description="game-sensei 学生模型训练")
    ap.add_argument("--data", required=True, help="示范数据目录（含 trajectory.jsonl）")
    ap.add_argument("--recursive", action="store_true", help="递归扫描子目录（多游戏混合训练）")
    ap.add_argument("--out", required=True, help="输出权重文件路径（.weights.json）")
    ap.add_argument("--hidden", type=int, default=64)
    ap.add_argument("--epochs", type=int, default=60)
    ap.add_argument("--batch", type=int, default=16)
    ap.add_argument("--lr", type=float, default=1e-3)
    ap.add_argument("--seed", type=int, default=42)
    ap.add_argument("--init", default="", help="从已有 .weights.json 热启动（增量训练）")
    ap.add_argument("--freeze-backbone", action="store_true",
                    help="冻结三层卷积、只训决策头（真机小样本加训防灾难性遗忘）")
    ap.add_argument("--note", default="", help="写入 meta.note 的标注（如标签质量说明）")
    ap.add_argument("--allow-missing-classes", action="store_true",
                    help="允许 CLS 里存在 0 样本的类（只求跑通链路时用；空类清单会写进 meta）")
    ap.add_argument("--override-class-weights", default="",
                    help="覆盖 CLASS_WEIGHTS 单项，格式 name=value 逗号分隔（如 tap=2.0,none=1.0）。"
                         "默认权重按「老师示范 none 占大头」设计；跑批采集的分布相反时必须覆盖，"
                         "否则学生会塌缩到高权重类（2026-09-16 实测：tap1.5/none0.5 配 11:22 样本 → 全猜 tap）。")
    ap.add_argument("--preference-weights", default="",
                    help="按示范行的局级 label 加权样本，格式 LABEL=value 逗号分隔（如 WIN=1.0,FAIL=0.3）。"
                         "离线偏好学习：胜局动作示范加权、败局示范降权。未列出的 label 与缺省行权重 1.0。"
                         "不传该参数时行为与旧版完全一致。")
    ap.add_argument("--coord-only", action="store_true",
                    help="只训坐标回归：跳过分类损失（分类头不训练），选优看 val 坐标误差最小。"
                         "用于「场景识别由外部像素判据负责、学生只出坐标」的工作模式（2026-09-16 v5 起）。")
    ap.add_argument("--rgb", action="store_true",
                    help="三通道彩色输入（conv1 in_channels=3，采集帧需为 RGB）。"
                         "权重文件由 conv1_w 形状自然携带通道数（[8,3,3,3] vs [8,1,3,3]）。")
    ap.add_argument("--in-h", type=int, default=48,
                    help="观测输入高度（宽固定 64）。默认 48 = 4:3 横版窗口；竖屏手游的关键 UI "
                         "多在中下部（YoYa Star 卡片带 y≈0.75~0.85），4:3 中心裁剪会把它裁掉——"
                         "实测 v8 学生视野 y∈[0.33,0.67] 看不见卡片。竖屏游戏用 96（2:3）及以上")
    ap.add_argument("--val-frac", type=float, default=0.2,
                    help="分层切分的验证集比例（按类别内比例取，每类至少留 1 条在训练集）")
    args = ap.parse_args()

    torch.manual_seed(args.seed)
    np.random.seed(args.seed)

    samples, stats = load_demos(Path(args.data), args.recursive, rgb=args.rgb)

    # 类别普查 + 空类检查（默认拒绝训练），再做分层切分。
    label_counts = [0] * len(CLS)
    for s in samples:
        label_counts[s["cls"]] += 1
    print_census(stats, label_counts)
    missing_classes = check_missing_classes(label_counts, args.allow_missing_classes)

    tr_idx, val_idx, split_warn = stratified_split(
        [s["cls"] for s in samples], args.val_frac, args.seed)
    for w in split_warn:
        print(f"  ⚠️ 切分: {w}")
    if not val_idx:
        sys.exit("分层切分后验证集为空——样本太少，先补采示范")
    n_val = len(val_idx)
    print(f"  分层切分: {len(tr_idx)} 训练 / {n_val} 验证（val_frac={args.val_frac}）")

    frames = torch.tensor(np.stack([samples[i]["frame"] for i in range(len(samples))]))
    if not args.rgb:
        frames = frames.unsqueeze(1)  # 灰度 [N,H,W] -> [N,1,H,W]；RGB 已是 [N,3,H,W]
    cls = torch.tensor([samples[i]["cls"] for i in range(len(samples))], dtype=torch.long)
    xy = torch.tensor([[samples[i]["x"], samples[i]["y"]] for i in range(len(samples))],
                      dtype=torch.float32)
    # 只有 tap 样本有坐标监督
    xy_mask = torch.tensor([CLS[c] == "tap" for c in cls.tolist()], dtype=torch.float32)

    cls_w = torch.tensor(CLASS_WEIGHTS)
    if args.override_class_weights:
        for kv in args.override_class_weights.split(","):
            name, _, val = kv.partition("=")
            name = name.strip()
            if name not in CLS or not val:
                sys.exit(f"--override-class-weights 项无效: {kv!r}（应为 name=value，name ∈ CLS）")
            cls_w[CLS.index(name)] = float(val)
        print("  类别权重覆盖: " + ", ".join(
            f"{CLS[i]}={cls_w[i]:g}" for i in range(len(CLS)) if cls_w[i] != CLASS_WEIGHTS[i]))

    # 离线偏好学习：按示范行的局级 label（WIN/FAIL/...）加权每个样本。
    # 未列出/缺省 label 权重 1.0 ⇒ 不传 --preference-weights 时与旧版数值完全等价。
    pref_w = torch.ones(len(samples))
    if args.preference_weights:
        pw_map: dict[str, float] = {}
        for kv in args.preference_weights.split(","):
            name, _, val = kv.partition("=")
            name, val = name.strip(), val.strip()
            if not name or not val:
                sys.exit(f"--preference-weights 项无效: {kv!r}（应为 LABEL=value，如 WIN=1.0,FAIL=0.3）")
            pw_map[name] = float(val)
        n_by_label: Counter = Counter()
        for i, s in enumerate(samples):
            lab = str(s.get("pref") or "")
            pref_w[i] = pw_map.get(lab, 1.0)
            n_by_label[lab or "(缺省)"] += 1
        print("  偏好加权: " + ", ".join(f"{k}={v:g}" for k, v in pw_map.items())
              + " | 样本分布: " + ", ".join(f"{k}={n}" for k, n in sorted(n_by_label.items())))

    model = build_model(args.hidden, in_ch=3 if args.rgb else 1)
    parent_meta: dict = {}
    if args.init:
        sd, parent_meta = import_weights_json(Path(args.init), args.hidden)
        model.load_state_dict(sd)
        print(f"热启动: 已从 {args.init} 载入权重 "
              f"(parent val_acc={parent_meta.get('val_acc')}, samples={parent_meta.get('samples')})")
    if args.freeze_backbone:
        if not args.init:
            print("提示: --freeze-backbone 在无 --init 时无实际意义（随机骨干本就没有可保留的特征）")
        for m in (model.conv1, model.conv2, model.conv3):
            for prm in m.parameters():
                prm.requires_grad = False
        print("已冻结三层卷积，本轮只训 fc / cls_head / coord_head")
    # 只把需要梯度的参数交给优化器（冻结骨干时不更新卷积）
    opt = torch.optim.Adam([p for p in model.parameters() if p.requires_grad], lr=args.lr)
    coord_loss_fn = nn.SmoothL1Loss(reduction="none")

    # 多数类基线：val 上最大类占比。val_acc 不显著高于它 = 没有决策能力。
    val_truth = cls[val_idx].tolist()
    val_majority = max(Counter(val_truth).values()) / len(val_truth)
    print(f"  多数类基线 {val_majority:.2%}（val_acc 不高于它，说明模型只是在猜最多数的类）")

    print(f"\n训练: {len(tr_idx)} 训练 / {n_val} 验证 | {args.epochs} epochs"
          + ("  [coord-only：只训坐标回归]" if args.coord_only else ""))
    best_acc = 0.0
    best_state = None
    final_acc = 0.0
    best_coord_err = float("inf")
    for ep in range(args.epochs):
        model.train()
        perm = torch.randperm(len(tr_idx))
        total_loss = 0.0
        for b0 in range(0, len(perm), args.batch):
            bi = perm[b0:b0 + args.batch]
            logits, coords = model(frames[bi])
            # 逐样本加权（类权重 × 偏好权重），按权重和归一 ⇒ 等价于旧的加权平均语义
            if args.coord_only:
                l_cls = torch.tensor(0.0)  # 只训坐标：分类头与 cls 损失完全隔离
            else:
                l_cls_vec = nn.functional.cross_entropy(logits, cls[bi], weight=cls_w, reduction="none")
                l_cls = (l_cls_vec * pref_w[bi]).sum() / pref_w[bi].sum().clamp_min(1e-8)
            if xy_mask[bi].sum() > 0:
                lc = coord_loss_fn(coords, xy[bi]).mean(dim=1)
                w = xy_mask[bi] * pref_w[bi]
                l_xy = (lc * w).sum() / w.sum().clamp_min(1e-8)
            else:
                l_xy = torch.tensor(0.0)
            loss = l_cls + 0.3 * l_xy
            opt.zero_grad()
            loss.backward()
            opt.step()
            total_loss += float(loss) * len(bi)

        model.eval()
        with torch.no_grad():
            logits, coords = model(frames[val_idx])
            pred = logits.argmax(dim=1)
            acc = float((pred == cls[val_idx]).float().mean())
            t_mask = xy_mask[val_idx] > 0
            coord_err = 0.0
            if t_mask.sum() > 0:
                coord_err = float((coords[t_mask] - xy[val_idx][t_mask]).abs().mean())
        final_acc = acc
        if ep % 10 == 9 or ep == 0:
            # 同时报训练集准确率：只有 train 上不去才是「欠训」，
            # train 高而 val 低才是「泛化/样本量」问题——两者对策完全不同。
            with torch.no_grad():
                tr_acc = float((model(frames[tr_idx])[0].argmax(dim=1)
                                == cls[tr_idx]).float().mean())
            print(f"  epoch {ep+1:3d}  loss={total_loss/len(tr_idx):.4f}  "
                  f"train_acc={tr_acc:.2%}  val_acc={acc:.2%}  tap坐标误差={coord_err:.3f}")
        if args.coord_only:
            # coord-only 时分类头无训练信号，val_acc 无意义 ⇒ 按 val 坐标误差最小选优
            if coord_err < best_coord_err:
                best_coord_err = coord_err
                best_state = {k: v.clone() for k, v in model.state_dict().items()}
        elif acc >= best_acc:
            best_acc = acc
            best_state = {k: v.clone() for k, v in model.state_dict().items()}

    if best_state:
        model.load_state_dict(best_state)

    # 用选中的模型在 val 上出完整报告（召回 + 预测分布 → 一眼看 mode collapse）
    model.eval()
    with torch.no_grad():
        val_pred = model(frames[val_idx])[0].argmax(dim=1).tolist()
    recall, pred_dist = per_class_report(val_truth, val_pred)

    if args.coord_only:
        print(f"\n选优：val 坐标误差 ={best_coord_err:.4f}（{args.epochs} epoch 里的最小值）")
        print(f"末轮 val_acc={final_acc:.2%}（coord-only：分类头无训练信号，仅作参考）")
    else:
        print(f"\n选优 val_acc={best_acc:.2%}（{args.epochs} epoch 里的最大值，"
              f"受 {n_val} 样本粒度影响，偏乐观）")
        print(f"末轮 val_acc={final_acc:.2%}   多数类基线={val_majority:.2%}")
    print(f"验证集预测分布: {pred_dist}")
    print("各类召回（验证集）:")
    for name, r in recall.items():
        print(f"  {name:<12} n={r['n']:<3} recall={r['recall']:.2f}")

    meta = {
        "trained_on": str(Path(args.data).resolve()),
        "samples": len(samples),
        "coord_only": bool(args.coord_only),
        "rgb": bool(args.rgb),
        "val_acc": round(best_acc, 4),
        "val_acc_final": round(final_acc, 4),
        "val_majority_baseline": round(val_majority, 4),
        "val_samples": n_val,
        "class_counts": {CLS[i]: n for i, n in enumerate(label_counts)},
        "missing_classes": missing_classes,
        "val_recall": recall,
        "val_pred_dist": pred_dist,
        # val_acc 是「若干 epoch 里在 val 上的最大值」，小样本下偏乐观。
        # 别把它当能力指标：真正的门槛是「显著高于多数类基线 + 各类召回非零」。
        "val_acc_note": "best-over-epochs on a tiny val split; optimistic. "
                        "需显著高于 val_majority_baseline 且各类召回非零，才算有决策能力。",
        "coord_search": "选优依据：coord_only 时=val 坐标误差最小；否则=val_acc 最大。",
        "val_coord_err": round(best_coord_err, 4) if args.coord_only else None,
        "down_w": 64,
        "down_h": args.in_h,
        "trained_at": __import__("datetime").datetime.now().isoformat(timespec="seconds"),
        # 训练配置与代码版本（DEVELOPMENT_PLAN §7：模型必须可追溯到数据集/配置/提交）
        "epochs": args.epochs,
        "lr": args.lr,
        "batch": args.batch,
        "seed": args.seed,
        "hidden": args.hidden,
        "backbone_frozen": bool(args.freeze_backbone),
        "git_commit": git_short_commit(),
    }
    if args.init:
        # 血缘：本模型由哪个模型加训而来（增量训练可追溯）
        meta["parent"] = str(Path(args.init).resolve())
        meta["parent_val_acc"] = parent_meta.get("val_acc")
        meta["parent_samples"] = parent_meta.get("samples")
    if args.note:
        meta["note"] = args.note
    export_weights(model, args.hidden, Path(args.out), meta)


if __name__ == "__main__":
    main()
