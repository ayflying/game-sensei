# 11. 去 CGO 化方案（Windows 专属）

> 本文是 [game-sensei](../README.md) 的文档分册：收录原 §11 前言、§11.1~§11.4（纯 Go 截屏/键鼠、去 CGO 学生推理、发布形态）。
> **章节编号沿用原 `README.md` 编号**，旧引用（如「README §11.16」）的对应关系见 [README §9 文档索引](../README.md#9-文档索引)。

---

项目仅运行在 Windows，因此可彻底去掉 CGO 依赖：`go build` 秒级编译、单 exe 发布、无 C 工具链。原方案中 `robotgo`（键鼠）与 `onnxruntime_go`（推理）是仅有的两处 CGO 依赖，替代如下。

---

## 11.1 截屏与键鼠：纯 Go syscall（必做）

- **截屏**：用标准库 `syscall` 直接调 Windows API，不引入 robotgo。
  - 整机/窗口抓屏：`PrintWindow` / `BitBlt`（gdi32）——简单通用。
  - 游戏实时抓屏首选 **DXGI Desktop Duplication**（d3d11），纯 Go 经 `syscall.NewLazyDLL("d3d11.dll")` 调用，比 GDI 更快、不吃游戏性能。
- **键鼠**：`syscall.NewLazyDLL("user32.dll").NewProc("SendInput")` 模拟键鼠，跨进程通用，无需 robotgo。
- 收益：核心程序彻底零 CGO，去掉 robotgo 的 C 依赖与编译负担。

---

## 11.2 学生推理：三种去 CGO 选项

| 选项 | 做法 | 优点 | 代价 |
|---|---|---|---|
| A 纯 Go 自写前向 | 自己用 Go 实现小 CNN/LSTM/MLP 前向（整型运算） | 零依赖、零 CGO、编译最快 | 只能跑自定义网络结构，不能加载任意 ONNX |
| B 挂 onnxruntime.dll | 运行时 `syscall.NewLazyDLL("onnxruntime.dll")` 调其 C ABI | 保留 ONNX 生态、能加载训练好的量化模型、核心仍零 CGO | 需自写一层纯 Go 的 onnxruntime C API 绑定（中等工作量） |
| C 独立推理子进程 | `cmd/inferd` 单独编译（可用 CGO 或 Python），核心经命名管道/IPC 收发「帧→动作」 | 核心纯 Go、推理组件热替换、崩溃隔离 | 多一个进程、需 IPC 协议 |

---

## 11.2.1 ✅ 已落地：A 方案（纯 Go 前向，2026-09-12）

实际选了 **A**：学生网络很小（3 层 CNN + 双头），48×64 输入下纯 Go 前向 <1ms，
不需要为它引入 dll 分发。链路：

```
老师示范(-demo)                    训练(CPU)                    学生实时操作
trajectory.jsonl + frames/  →  trainer/train.py  →  models/*.weights.json  →  helper -student
(internal/dataset 落盘)        torch BC 训练         纯 JSON 权重文件           internal/student 纯 Go 前向
```

- **网络结构**（Python 与 Go 两侧严格一致，tools/parity 校验误差 <1e-5）：
  `conv1(1→8,3x3)+relu+pool2 → conv2(8→16,3x3)+relu+pool2 → conv3(16→24,3x3)+relu
  → GAP → fc(24→64)+relu → cls_head(64→8) / coord_head(64→2)+sigmoid`
- **输出头**（跨游戏通用，不随游戏变）：8 类 = up/down/left/right/tap/press/wait/none
  + tap 归一化坐标回归。「学的是看画面选动作」这个能力本身，具体点哪/往哪走由动作空间和档案吸收。
- **权重文件** `models/*.weights.json`：JSON 格式（float32 数组），version=1；
  Go 侧 `internal/student.Load` 按字段名加载并做长度校验。
- **训练**：`C:/Users/ay/.workbuddy/binaries/python/envs/default/Scripts/python.exe
  trainer/train.py --data <demo目录> --out models/<名字>.weights.json`（依赖 torch+numpy+pillow）。
  类别加权（tap/press 权重高）防「永远不动」退化；解析失败的示范步自动剔除。
- **一致性校验**：`go run ./tools/parity`（合成数据冒烟：训练→导出→Go 前向，比对 torch 基准）。
- **接入实时回路**：`go run ./cmd/helper -game <档案> -live -student models/<名字>.weights.json`。

---

## 11.3 推荐组合

- **截屏 / 键鼠**：纯 Go syscall（必做，去 robotgo）。
- **推理**：✅ 已落地 **A（纯 Go 自写前向）**（§11.2.1）；学生网络变复杂（如要跑视觉
  Transformer 或加载现成量化 ONNX）时再启用 B（LazyDLL 挂 onnxruntime.dll）。
- **老师**：本来就是 `net/http` 调 Ollama，无 CGO 问题。
- **子进程 C** 作为进阶选项：当学生网络变复杂、想用 Python 训练链直接服务时，把推理拆出去。

---

## 11.4 发布形态

- 纯 Go 核心 → 单 `helper.exe`，`CGO_ENABLED=0 go build`。
- 学生权重 `models/*.weights.json` 是纯数据文件，随包放置即可（热载同 onnx 方案）。
- `robotgo` 从依赖中移除，`go.mod` 不再需要 CGO 工具链。


---

> 分册全目录见 [README §9 文档索引](../README.md#9-文档索引)。
