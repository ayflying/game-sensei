"""game-sensei 学生模型训练器（模仿学习 / BC，torch 实现）。

从 internal/dataset 落盘的老师示范（trajectory.jsonl + frames/）训练一个
轻量学生策略网络，导出**纯权重文件**（.weights.json）供 Go 运行时
internal/student 纯 Go 前向传播加载——零 CGO、零外部依赖。

为什么训练用 torch、推理用纯 Go：
  - 训练要 autograd，torch 最稳；推理只要前向，网络极小（3 层 CNN），
    纯 Go <1ms，且不用给 Go 运行时分发 onnxruntime.dll。
  - 权重文件是唯一接口，Python 侧导出顺序与 Go 侧加载顺序一一对应。

学生输出两层（与 L1 动作空间对齐，跨游戏通用）：
  - 分类头：8 类 = up/down/left/right/tap/press/wait/none
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
from pathlib import Path

import numpy as np

CLS = ["up", "down", "left", "right", "tap", "press", "wait", "none"]

# 类别权重：老师示范里 none/wait 常占大头，不加权学生会学会「永远不动」
CLASS_WEIGHTS = np.array([0.3, 0.3, 0.3, 0.3, 1.5, 1.5, 0.5, 0.5], dtype=np.float32)


# ---------------------------------------------------------------------------
# 数据：读取 demo 数据集
# ---------------------------------------------------------------------------

def load_demos(data_dir: Path, recursive: bool) -> list[dict]:
    """读一个或多个示范目录，返回样本列表。

    每个样本 = {"frame": float32[down_h,down_w] 0~1, "cls": int, "x": float, "y": float}
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
                frame_path = root / s["frame_gray"]
                if not frame_path.exists():
                    continue
                kind, x, y = map_action(s)
                if kind is None:
                    continue
                img = Image.open(frame_path).convert("L")
                arr = center_crop_resize(img, 64, 48)
                samples.append({"frame": arr, "cls": CLS.index(kind), "x": x, "y": y})
                n_ok += 1
        print(f"  {tj.parent.name}: {n_ok} 个有效样本")
    print(f"共加载 {len(samples)} 个样本")
    if len(samples) < 20:
        sys.exit("样本太少，先采集更多示范（-demo-steps 建议 60+）")
    return samples


def map_action(s: dict) -> tuple[str | None, float, float]:
    """把 trajectory.jsonl 的一行映射到 (类别, tap_x, tap_y)。"""
    kind = s.get("kind", "")
    if kind in ("tap", "hold"):
        return "tap", float(s.get("nx", 0.5)), float(s.get("ny", 0.5))
    if kind == "key":
        return "press", 0.0, 0.0
    if kind == "joy":
        dx = float(s.get("nx2", 0.5)) - float(s.get("nx", 0.5))
        dy = float(s.get("ny2", 0.5)) - float(s.get("ny", 0.5))
        return dir_of(dx, dy), 0.0, 0.0
    if kind == "move":
        raw = s.get("raw", "")
        for d in ("up", "down", "left", "right"):
            if d in raw:
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
    if abs(dx) >= abs(dy):
        return "right" if dx > 0 else "left"
    return "down" if dy > 0 else "up"


def center_crop_resize(img, w: int, h: int) -> np.ndarray:
    """居中裁剪到目标长宽比再缩放——竖屏/横屏画面落到同一坐标系（通用性关键）。"""
    W, H = img.size
    target_ratio = w / h
    if W / H > target_ratio:
        nw = int(H * target_ratio)
        x0 = (W - nw) // 2
        img = img.crop((x0, 0, x0 + nw, H))
    else:
        nh = int(W / target_ratio)
        y0 = (H - nh) // 2
        img = img.crop((0, y0, W, y0 + nh))
    img = img.resize((w, h))
    return np.asarray(img, dtype=np.float32) / 255.0


# ---------------------------------------------------------------------------
# 网络定义（结构必须与 Go 侧 internal/student 前向完全一致）
# ---------------------------------------------------------------------------

def build_model(hidden: int = 64):
    import torch
    import torch.nn as nn

    class StudentNet(nn.Module):
        """conv1(1->8,3x3)+relu+pool2 -> conv2(8->16,3x3)+relu+pool2
        -> conv3(16->24,3x3)+relu+GAP -> fc(24->hidden)+relu
        -> cls_head(hidden->8) / coord_head(hidden->2)+sigmoid"""

        def __init__(self):
            super().__init__()
            self.conv1 = nn.Conv2d(1, 8, 3)
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

    # JSON 键名 -> (state_dict 键名, 形状)，与 export_weights 的导出顺序一一对应
    shapes = {
        "conv1_w": ("conv1.weight", (8, 1, 3, 3)), "conv1_b": ("conv1.bias", (8,)),
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
    args = ap.parse_args()

    torch.manual_seed(args.seed)
    np.random.seed(args.seed)

    samples = load_demos(Path(args.data), args.recursive)

    idx = np.random.permutation(len(samples))
    n_val = max(4, int(len(samples) * 0.15))
    val_idx, tr_idx = idx[:n_val], idx[n_val:]

    frames = torch.tensor(np.stack([samples[i]["frame"] for i in range(len(samples))]))
    frames = frames.unsqueeze(1)  # [N,1,H,W]
    cls = torch.tensor([samples[i]["cls"] for i in range(len(samples))], dtype=torch.long)
    xy = torch.tensor([[samples[i]["x"], samples[i]["y"]] for i in range(len(samples))],
                      dtype=torch.float32)
    # 只有 tap 样本有坐标监督
    xy_mask = torch.tensor([CLS[c] == "tap" for c in cls.tolist()], dtype=torch.float32)

    cls_w = torch.tensor(CLASS_WEIGHTS)

    model = build_model(args.hidden)
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
    cls_loss_fn = nn.CrossEntropyLoss(weight=cls_w)
    coord_loss_fn = nn.SmoothL1Loss(reduction="none")

    print(f"\n训练: {len(tr_idx)} 训练 / {n_val} 验证 | {args.epochs} epochs")
    best_acc = 0.0
    best_state = None
    for ep in range(args.epochs):
        model.train()
        perm = torch.randperm(len(tr_idx))
        total_loss = 0.0
        for b0 in range(0, len(perm), args.batch):
            bi = perm[b0:b0 + args.batch]
            logits, coords = model(frames[bi])
            l_cls = cls_loss_fn(logits, cls[bi])
            if xy_mask[bi].sum() > 0:
                lc = coord_loss_fn(coords, xy[bi]).mean(dim=1)
                l_xy = (lc * xy_mask[bi]).sum() / xy_mask[bi].sum()
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
        if ep % 10 == 9 or ep == 0:
            print(f"  epoch {ep+1:3d}  loss={total_loss/len(tr_idx):.4f}  "
                  f"val_acc={acc:.2%}  tap坐标误差={coord_err:.3f}")
        if acc >= best_acc:
            best_acc = acc
            best_state = {k: v.clone() for k, v in model.state_dict().items()}

    if best_state:
        model.load_state_dict(best_state)
    print(f"\n最佳验证准确率: {best_acc:.2%}")

    meta = {
        "trained_on": str(Path(args.data).resolve()),
        "samples": len(samples),
        "val_acc": round(best_acc, 4),
        "down_w": 64,
        "down_h": 48,
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
