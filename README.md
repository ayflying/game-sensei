# game-sensei

> **泛型游戏辅助框架**：大模型当「老师」教学，量化小模型当「学生」自学习并实时操作。

一个**游戏无关**的辅助框架——靠纯视觉感知 + 模拟输入（**不读内存、不接封包**），由本地量化小模型实时操作，由大模型周期性评估教学，形成「学生操作 → 老师查岗 → 学生再学」的自学习飞轮。

---

## 1. 定位与愿景

- **泛型**：不绑定具体游戏，靠截图 + 键鼠模拟工作，换游戏只需换「目标定义 + 初始示范」。
- **自学习**：学生（量化模型）实时玩，老师（大模型）定期看回放、诊断错误、产出示范，回灌微调学生。
- **低延迟**：实时操作必须由本地量化小模型承担；大模型只做慢节奏的异步教学，不参与实时回路。

---

## 2. 自学习闭环（核心）

```
游戏画面截屏
   → 感知编码（CNN / VLM 视觉塔）
   → 量化学生 Actor（策略网络，int8 本地部署，实时低延迟）
   → 动作输出（键鼠 / 触控）
   → 游戏环境反馈（得分 / 胜负 / 状态）
   → 轨迹缓冲（状态 · 动作 · 结果）
        ↘（周期性）
   大模型教师（VLM / LLM）：检查回放 · 评估错误 · 产出示范 · 蒸馏微调学生
        ↘（示范数据 → 微调重装 onnx）
   学生变强 → 回到开头
```

- **实时执行回路**：`capture → agent → input`，必须几十 ms 级，学生必须是本地量化小模型。
- **异步教学回路**：`teacher` 每 N 局或 M 分钟触发一次，看一段回放 + 轨迹 JSON，诊断错误并产出正确示范动作序列。
- **蒸馏回灌**：老师筛出的「优质轨迹 + 示范」用于微调学生（模仿学习 BC + DAgger），导出新 onnx，Go 运行时热重载。

---

## 3. 角色定义

| 角色 | 实现 | 职责 |
|---|---|---|
| **学生 Actor** | 量化策略网络（onnxruntime-go, int8） | 实时感知 → 输出动作，低延迟、稳定、可复现 |
| **老师 Teacher** | 大模型 VLM（本地 Ollama / OpenAI 兼容） | 周期性评估回放、诊断错误、产出示范、定义评分标准 |
| **轨迹 Trajectory** | `(state, action, result)` 序列 | 自学习的数据飞轮燃料，存于 `memory` |
| **奖励 Reward** | 由老师按游戏类型生成的「评分标准」 | 泛型游戏无统一 reward，需可计算指标或老师 critic 打分 |

---

## 4. 技术栈

### 运行时（Go 单二进制）

| 模块 | Go 库 | 说明 |
|---|---|---|
| 截屏 `capture` | `github.com/kbinani/screenshot` | 跨平台抓屏；Windows 抓指定窗口可用 `github.com/AllenDang/w32` |
| 键鼠 `input` | `github.com/go-vgo/robotgo` | 跨平台模拟键鼠/触控（需 CGO） |
| 学生推理 `agent` | `github.com/yalue/onnxruntime_go` | 加载量化 int8 ONNX，仅需 onnxruntime 动态库 |
| 老师 `teacher` | 标准 `net/http` | 调本地 Ollama `/api/chat` 或 OpenAI 兼容接口 |
| 轨迹 `memory` | `chan` / ring buffer | goroutine 间解耦共享 |
| 配置 `config` | YAML/JSON | 目标 · 评分标准（按游戏可配） |

### 离线训练（Python 工具，`trainer/`）

- PyTorch 训学生网络 → `torch.onnx.export` → `onnxruntime.quantization` 量化为 int8。
- 这是一次性工具，**不进运行时**；Go 只加载产物 onnx 做前向推理。
- 关键边界：**Go 不能训练神经网络**，训练侧保留 Python 工具链。

### 感知端选择（Go 生态现实）

- **方案 A（推荐，实时）**：学生网络端到端——截图降采样直接进小 CNN → 输出动作，整体为 onnx，Go 仅推理，不依赖 CLIP 视觉塔。
- **方案 B（老师辅助）**：用大模型 VLM 做感知（老师既感知又教学），学生只学老师动作策略；简单但延迟高、成本高，仅适合慢节奏。
- 落地建议：**A 做实时、B 做老师辅助判断**。

---

## 5. 目录结构

```
game-sensei/
├── cmd/helper/main.go        # 编排：实时回路 + 延迟统计 + 信号退出（Phase 0 已实现）
├── internal/
│   ├── capture/              # 截屏（GDI BitBlt → 灰度降采样，纯 syscall）
│   ├── agent/                # Actor 接口 + 规则学生（后续换量化 ONNX 实现）
│   ├── input/                # 键鼠（user32!SendInput，纯 syscall）
│   ├── teacher/              # Ollama 客户端 + 轨迹评估器 + 提示词（Phase 1 已落地）
│   ├── memory/               # 轨迹缓冲
│   └── config/               # 帧率/降采样/动作集（按游戏可配）
├── models/                   # *.onnx（trainer 导出，运行时热载，不入库）
├── tools/                    # 本地测试工具（非运行时依赖）
│   ├── serve_ollama.sh       # 启动项目自带的 Ollama 实例（独立端口 11435）
│   ├── screenshot.py         # 抓一张截图供 VLM 评测
│   └── vlm_bench.py          # VLM 选型评测：延迟 / 速度 / 输出质量
├── .ollama/                  # 项目内 Ollama 模型库（体积大，不入库）
└── trainer/                  # Python 离线训练工具（非运行时）
    ├── train.py
    └── requirements.txt
```

构建注意：核心程序**零 CGO**（`CGO_ENABLED=0 go build`）。`robotgo` / `onnxruntime_go` 已从核心移除，推理外挂方案见 §11；系统 DLL（user32/gdi32/d3d11）随 Windows 自带，不需打包。

---

## 6. 阶段路线图

- **Phase 0**：单游戏打通「截屏 → 学生 stub 决策 → 键鼠」实时回路（先用规则驱动验证延迟与输入链路）。见 `cmd/helper/main.go` 占位。
- **Phase 1**：接 `teacher`，先做**离线评估 + 文字反馈**，人工看反馈改进提示词，验证老师判断力。
  ✅ **第一版已落地**：`internal/teacher` 提供 Ollama `/api/chat` 客户端（多图送审、content/thinking 双字段兜底）、
  轨迹评估器（结构化反馈 + 评分解析 + Markdown 报告）与提示词构造；`cmd/helper -teacher` 开启异步教学回路，
  抽样抓帧不阻塞实时回路。已用本机 `qwen3.5:2b` 端到端验证通过（详见 §9.2）。
- **Phase 2**：老师产出**示范轨迹**，自动用示范微调学生（蒸馏 / bootstrapping），打通 `trainer/`。
- **Phase 3**：自动化飞轮——老师定期评估 + 选优轨迹 + 触发微调 + 回灌，形成自学习；**泛型化**（换游戏只需换目标定义 + 初始示范）。

---

## 7. 关键设计决策与待定项（设计阶段重点）

| 待设计项 | 说明 | 建议 |
|---|---|---|
| **状态表征** | 截帧如何编码进网络（降采样尺寸、帧序列长度、是否含 OCR 文本） | 端到端小 CNN，先定输入分辨率与帧栈 |
| **动作空间** | 离散键位 / 连续移动 / 组合技如何建模 | 先定最小动作集，再扩展 |
| **奖励/评分标准统一** | 泛型游戏无统一 reward | 老师按游戏类型生成可计算指标；或纯老师 critic 打分 |
| **冷启动** | 学生初始啥也不会 | 老师先示范 bootstrapping，学生纯模仿再探索 |
| **灾难性遗忘** | 小模型易过拟合某场景 | 老师定期查岗纠偏 + 保留历史优质轨迹回放训练 |
| **模型热重载** | 微调后不重启生效 | `agent` 检测 onnx 文件 mtime 变化，原子替换 |

---

## 8. 合规声明

- 本框架仅使用**视觉感知 + 模拟输入**，不读取游戏内存、不拦截/篡改网络封包。
- 但纯模拟输入仍可能违反多数联网游戏的**服务条款（ToS）**，存在封号风险。
- 建议仅用于**单机游戏、自研游戏、或个人研究**场景；请勿用于联网竞技或违反游戏方规定的用途。
- 使用者须自行评估并承担合规风险。

---

## 9. 快速开始（Phase 0）

```bash
# 构建并运行占位骨架
cd d:/git/game-sensei
go build ./...
go run ./cmd/helper
```

后续按 `internal/` 各包与 Phase 0→3 路线图逐步填充实现。

### 9.1 教师模型环境（项目自带 Ollama）

teacher 用的 VLM 走**项目内自带的 Ollama 实例**（独立端口 11435、模型库在 `.ollama/models`），
与系统安装的 Ollama 隔离，不依赖任何远程设备：

```bash
# 1. 启动项目自带实例（模型库落在 .ollama/models，已 gitignore）
bash tools/serve_ollama.sh

# 2. 另一个终端：拉模型（也可从别处拷贝 .ollama/models 整目录）
curl -X POST http://127.0.0.1:11435/api/pull -d '{"model":"qwen3.5:2b"}'

# 3. 评测：抓一张截图，跑延迟/速度/质量对比
python tools/screenshot.py -o shot.png
python tools/vlm_bench.py --image shot.png --models qwen3.5:2b --rounds 2
```

**实测选型（RTX 3060 12GB，1920×1080 截图，同一提示词，均为本机 11435 实例）：**

| 模型 | 体积 | 生成速度 | 单次评估 | 判定 |
|---|---|---|---|---|
| `qwen3.5:9b` Q4_K_M | 6.59GB | 40 tok/s | 22.5s（含 913tok 思考） | ✅✅ teacher 首选：窗口标题 / 中央内容 / 底部输入框+输入法全认对 |
| `qwen3.5:2b` Q8_0 | 2.74GB | 105 tok/s | 10.1s（含 975tok 思考） | ✅ 快速档：中央内容正确，窗口标题略偏 |
| `qwen3-vl:2b` Q4_K_M | 1.89GB | 147 tok/s | 14s+（2000tok）仍无正文 | ❌ 思考链死循环，不采用 |

同一个 `qwen3.5:9b` 在远程机器上实测 106 tok/s、本机 3060 只有 40 tok/s——
**模型速度强依赖机器，选型与延迟预估都必须以目标机器实测为准**。

**已知坑（teacher 客户端必须处理）：**

- qwen3 系列把推理写在 `message.thinking`、正文写在 `message.content`，**两个字段都要兜底解析**；
  400 tok 预算常被思考吃光，`num_predict` 需给到 **≥1500**，或提示词里明确要求直接给答案。
- `think=false` 参数在本机 Ollama 0.34.0 上对这两个模型**无效**，压不住思考。
- 同一模型在不同机器上速度差异极大（`qwen3-vl:2b` 远程 16~27 tok/s vs 本机 147 tok/s），
  **teacher 选型必须以目标机器实测为准**。

### 9.2 老师（异步教学回路）

实时回路只管跑，老师在**另一个协程**里按抽样节奏干活：

```bash
# 边跑边让老师查岗（dry-run，不发送真实键鼠）
go run ./cmd/helper -frames 3000 -teacher \
  -teacher-model qwen3.5:9b \
  -eval-every 300 -eval-frames 6 -eval-width 640 \
  -goal "把方块推到右侧终点" \
  -eval-out .workbuddy/eval-reports
```

| 参数 | 说明 |
|---|---|
| `-teacher` | 启用异步教学回路（默认关，避免无人值守时白烧算力） |
| `-teacher-url` / `-teacher-model` | 老师地址与模型，默认本机 `11435` + `qwen3.5:9b` |
| `-eval-every` | 每 N 帧抽一帧；30FPS 下 `300` ≈ 每 10 秒一帧 |
| `-eval-frames` | 攒够多少帧送审一次（`6` ≈ 覆盖 1 分钟） |
| `-eval-width` | 送审帧降采样宽度（`640`，比学生输入的 160 宽，让老师看清界面） |
| `-goal` | 游戏目标，写进提示词供老师判断动作合理性 |
| `-eval-out` | 报告落盘目录（Markdown，含评分与性能数据）；空则只打印控制台 |

**设计要点：**

- 抽帧与推理全在**教学协程**内完成（`capture.Grab` 无共享状态，可跨协程调用），
  实时回路每帧只是往缓冲通道做一次**非阻塞投递**，教学回路忙时直接丢帧——
  实测 40 帧运行中实时回路平均延迟 31.0ms，与不启用老师时一致。
- 老师不可达时**优雅降级**：启动自检失败只打印警告，实时回路照跑。
- 送审的是**学生自己的观测**（降采样灰度图），不是原始彩屏——评估的是
  「学生在它看到的世界里做得对不对」。
- 反馈要求结构化四行（局面 / 评价 / 建议 / 评分），评分会被解析出来便于 Phase 2 自动选优。

**端到端验证（本机 `qwen3.5:2b`，workbuddy 桌面为「游戏画面」）：** 老师 12.7s 返回，
正确指出「画面是聊天界面而非游戏主画面」，给出关闭窗口→进入游戏→再推方块的建议，评分 0；
报告落盘正常。`internal/teacher` 另有 mock 单测覆盖双字段兜底、图片编码、超时与错误路径。

---

## 10. 参考

- 架构闭环与 Go 模块结构见设计讨论（项目初始化时由 AI 助手绘制的架构图）。
- 大模型教师实测选型与本地 Ollama 环境见 §9.1（推荐 `qwen3.5:9b` 主力 + `qwen3.5:2b` 快速档）。

---

## 11. 去 CGO 化方案（Windows 专属）

项目仅运行在 Windows，因此可彻底去掉 CGO 依赖：`go build` 秒级编译、单 exe 发布、无 C 工具链。原方案中 `robotgo`（键鼠）与 `onnxruntime_go`（推理）是仅有的两处 CGO 依赖，替代如下。

### 11.1 截屏与键鼠：纯 Go syscall（必做）

- **截屏**：用标准库 `syscall` 直接调 Windows API，不引入 robotgo。
  - 整机/窗口抓屏：`PrintWindow` / `BitBlt`（gdi32）——简单通用。
  - 游戏实时抓屏首选 **DXGI Desktop Duplication**（d3d11），纯 Go 经 `syscall.NewLazyDLL("d3d11.dll")` 调用，比 GDI 更快、不吃游戏性能。
- **键鼠**：`syscall.NewLazyDLL("user32.dll").NewProc("SendInput")` 模拟键鼠，跨进程通用，无需 robotgo。
- 收益：核心程序彻底零 CGO，去掉 robotgo 的 C 依赖与编译负担。

### 11.2 学生推理：三种去 CGO 选项

| 选项 | 做法 | 优点 | 代价 |
|---|---|---|---|
| A 纯 Go 自写前向 | 自己用 Go 实现小 CNN/LSTM/MLP 前向（整型运算） | 零依赖、零 CGO、编译最快 | 只能跑自定义网络结构，不能加载任意 ONNX |
| B 挂 onnxruntime.dll | 运行时 `syscall.NewLazyDLL("onnxruntime.dll")` 调其 C ABI | 保留 ONNX 生态、能加载训练好的量化模型、核心仍零 CGO | 需自写一层纯 Go 的 onnxruntime C API 绑定（中等工作量） |
| C 独立推理子进程 | `cmd/inferd` 单独编译（可用 CGO 或 Python），核心经命名管道/IPC 收发「帧→动作」 | 核心纯 Go、推理组件热替换、崩溃隔离 | 多一个进程、需 IPC 协议 |

### 11.3 推荐组合

- **截屏 / 键鼠**：纯 Go syscall（必做，去 robotgo）。
- **推理**：主推 **B（LazyDLL 挂 onnxruntime.dll）**——同时满足「零 CGO 核心」与「能加载量化 ONNX」；若连 DLL 都不想带，退回 **A（纯 Go 自写前向）**，最干净。
- **老师**：本来就是 `net/http` 调 Ollama，无 CGO 问题。
- **子进程 C** 作为进阶选项：当学生网络变复杂、想用 Python 训练链直接服务时，把推理拆出去。

### 11.4 发布形态

- 纯 Go 核心 → 单 `helper.exe`，`CGO_ENABLED=0 go build`。
- 外挂原生件随包放置：`onnxruntime.dll`（B 方案）/ 或 `inferd.exe`（C 方案）；系统 DLL 不需打包。
- `robotgo` 从依赖中移除，`go.mod` 不再需要 CGO 工具链。
