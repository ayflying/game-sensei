"""game-sensei 离线训练工具（非运行时）。

职责：
  1. 从 memory 导出的轨迹（state, action, result）中筛选优质样本；
  2. 用大模型 teacher 产出的示范（demonstration）做模仿学习 / DAgger；
  3. 训练学生策略网络（PyTorch），导出 ONNX，并量化为 int8 供 Go 运行时 onnxruntime-go 加载。

注意：本文件为 Phase 2+ 的占位骨架，仅给出流程与接口约定，不含实现。
运行依赖见 requirements.txt。

典型命令（设计确定后实现）：
  python train.py --data replays/ --demo demos.json --out ../models/student.onnx
"""

from dataclasses import dataclass


@dataclass
class TrainConfig:
    data_dir: str = "replays/"
    demo_path: str = "demos.json"
    out_onnx: str = "../models/student.onnx"
    epochs: int = 20
    lr: float = 1e-3


def main() -> None:
    cfg = TrainConfig()
    # TODO(Phase 2):
    #   1. 加载轨迹与示范数据
    #   2. 定义学生网络（CNN + LSTM / 轻量 Transformer，输入降采样帧序列，输出动作分布）
    #   3. 模仿学习（BC）+ 老师筛选的 DAgger 数据训练
    #   4. torch.onnx.export -> onnxruntime.quantization.quantize_dynamic(int8)
    #   5. 写出 cfg.out_onnx，Go 运行时检测文件变化热重载
    raise NotImplementedError("trainer 在 Phase 2 实现；当前为占位骨架")


if __name__ == "__main__":
    main()
