# game-sensei

> **泛型游戏辅助框架**：大模型当「老师」教学，量化小模型当「学生」自学习并实时操作。

一个**游戏无关**的辅助框架——靠纯视觉感知 + 模拟输入（**不读内存、不接封包**），由本地量化小模型实时操作，由大模型周期性评估教学，形成「学生操作 → 老师查岗 → 学生再学」的自学习飞轮。

> **本页只保留项目介绍。** 快速开始、去 CGO 化方案、确定性执行计划（`-plan`）等正文
> 已拆分为 [`docs/`](docs/) 下的分册，见 [§9 文档索引](#9-文档索引)。
> 下文出现的 `§9.x / §11.x / §12.x` 编号**沿用拆分前的原 README 编号**，对照关系同样见该索引。

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
├── cmd/helper/backend.go     # 后端抽象 + 档案挂载：L1 语义动作 → L2 平台动作
├── cmd/helper/demo.go        # Phase 2 在线示范回路（老师出动作 + 采集数据集）
├── cmd/video/main.go         # 教学视频：抽帧 → 去重 → 切段 → 老师判读（离线工具）
├── internal/
│   ├── capture/              # 截屏（GDI BitBlt → 灰度降采样 / 彩色图，纯 syscall）
│   ├── android/              # 安卓 ADB 后端：截图 / 触控 / 滑动 / 摇杆 / 按键 / 拉起应用
│   ├── agent/                # 动作空间（L1/L2）+ 容错解析器 + Actor 接口 + 规则学生
│   │   ├── agent.go          # Action / ActionKind / RuleActor
│   │   ├── dir.go            # Dir 方向体系（8 向 + 中英文别名 + 向量）
│   │   └── parse.go          # 老师文本 → 动作（含 ActionProtocol 提示词）
│   ├── game/                 # 游戏档案：换游戏只换一份 JSON（见 §9.5）
│   │   ├── profile.go        # 档案结构 / 加载 / 校验 / 按钮查找
│   │   ├── resolve.go        # L1 → L2 展开（MOVE→摇杆/按键，PRESS→点击）
│   │   └── profiles/*.json   # 内置档案：nrc(手游) / nrc_pc(键鼠) / mobile_generic / pc_generic
│   ├── input/                # 键鼠（SendInput + SetCursorPos + mouse_event，纯 syscall）
│   ├── vision/               # 区域平均降采样 + JPEG 编码（老师判读用）
│   ├── dataset/              # 示范数据集落盘（meta.json / trajectory.jsonl / frames）
│   ├── video/                # 教学视频：ffmpeg 抽帧 / dHash 去重 / 动作段切分 / 判读
│   ├── overlay/              # PC 右下角置顶日志浮窗（截屏不可见，见 §9.3）
│   ├── gamewin/              # 游戏窗口控制：查询/还原/置顶/最小化/前台检查
│   ├── teacher/              # Ollama 客户端 + 轨迹评估器 + 动作示范器（Phase 1/2）
│   ├── student/              # 量化学生：纯 Go CNN 前向，加载 .weights.json（见 §11.2.1）
│   ├── memory/               # 轨迹缓冲
│   └── config/               # 帧率/降采样/动作集/目标平台/游戏档案（按游戏可配）
├── models/                   # *.weights.json（trainer 导出的学生权重，约 71 KB/个，入库）
├── tools/                    # 本地测试工具（非运行时依赖）
│   ├── serve_ollama.sh       # 启动项目自带的 Ollama 实例（独立端口 11435）
│   ├── grid_overlay.py       # 截图打归一化网格，用于校准新游戏的摇杆/按钮坐标
│   ├── screenshot.py         # 抓一张截图供 VLM 评测
│   └── vlm_bench.py          # VLM 选型评测：延迟 / 速度 / 输出质量
├── .ollama/                  # 项目内 Ollama 模型库（体积大，不入库）
├── trainer/                  # Python 离线训练工具（非运行时）
│   ├── train.py
│   └── requirements.txt
└── docs/                     # 文档分册：原 README 的 §9 快速开始 / §11 去 CGO 化 / §12 -plan（见 §9 文档索引）
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
- **Phase 2.5 ✅ 已落地**：**教学视频学习**——新增 `cmd/video` + `internal/video`，把人类高手的
  录像抽帧、去重、切成动作段后交给老师逐段判读，产出可读的策略清单，补上冷启动阶段
  「老师只能靠自己瞎猜」的短板；详见 §9.6。
- **Phase 3**：自动化飞轮——老师定期评估 + 选优轨迹 + 触发微调 + 回灌，形成自学习；**泛型化**（换游戏只需换目标定义 + 初始示范）。
- **Phase 0.5 ✅ 已落地**：**目标环境泛化**——新增安卓 ADB 后端（`internal/android` + `cmd/helper/backend.go`），
  使同一套实时/教学回路既能操作 PC 桌面窗口，也能操作真机手游（截图 → 触控/滑动/摇杆/按键）。
  动作空间升级为归一化坐标，跨分辨率复用；详见 §9.3。

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

## 9. 文档索引

> 为保持本页可读，原 README 的三大块正文已拆分为 `docs/` 下的分册。
> **分册里的章节编号沿用原 README 编号**，因此代码注释、`AGENTS.md`、`DEVELOPMENT_PLAN.md`
> 里「README §11.16」这类旧引用，按下表对应到新文件即可。

| 分册 | 收录原章节 | 内容 |
|---|---|---|
| [`docs/quickstart.md`](docs/quickstart.md) | §9 前言、§9.1、§9.2 | 构建与运行骨架、教师模型环境、异步教学回路 |
| [`docs/backends.md`](docs/backends.md) | §9.3、§9.3.1~§9.3.3 | 双后端（PC / 安卓 ADB）、PC 档案、输入注入被反作弊拦截、扫描码坑 |
| [`docs/action-layer.md`](docs/action-layer.md) | §9.5 | L1/L2 动作分层、档案字段、内置档案与换游戏流程、**设备省电与息屏**（允许休眠 + 抓帧自动唤醒解锁、亮度值域坑、熄屏致掉线） |
| [`docs/teacher-demo-video.md`](docs/teacher-demo-video.md) | §9.3、§9.4、§9.6、§9.7 | 独立图片理解 `cmd/vlm`（配置渠道优先、Ollama 兜底）、老师在线示范 `-demo`、教学视频判读 `cmd/video`、本地 OCR `cmd/ocr`（确定性读屏文字） |
| [`docs/cgo-free.md`](docs/cgo-free.md) | §11 前言、§11.1~§11.4 | 纯 Go 截屏/键鼠 syscall、去 CGO 学生推理、推荐组合、发布形态 |
| [`docs/runtime-notes.md`](docs/runtime-notes.md) | §11.5~§11.8 | 锁屏致 GDI 失效、窗口感知域、浮窗挂死、9B 老师复读机 |
| [`docs/student-training.md`](docs/student-training.md) | §11.9、§11.14~§11.16 | 训练与增量训练、nrc 首次加训、动作空间对齐 L1 的 8 向 |
| [`docs/nrc-field-notes.md`](docs/nrc-field-notes.md) | §11.10~§11.13、§11.17 | 洛克王国实战复盘：单向死撞、Δ 闸门、战斗内死锁、脱困传送 |
| [`docs/plan-engine.md`](docs/plan-engine.md) | §12 前言、§12.1~§12.5 | `-plan` 动机、用法、档案格式、实测数据与坑、测试覆盖 |
| [`docs/plan-sparkle.md`](docs/plan-sparkle.md) | §12.6~§12.10 | Sparkle 实战：胜负判定、装备与胜率、跑批实测、远程 ADB、假成功翻案 |
| [`docs/plan-yoyastar.md`](docs/plan-yoyastar.md) | §12.11~§12.16 | YoYa Star 实战：结算页拒输入、五个判据标定、**广告色块致假判负的事故复盘**、跑批口径、离线设计验证矩阵、**主界面全入口与玩法覆盖清单** |
| [`docs/dressup-template.md`](docs/dressup-template.md) | §13 | 换装类通用玩法模板 `profiles/dressup_common.json`：14 步 PK 主循环骨架、五段结构、判据默认值表、新游戏复用流程 |
| [`docs/reference.md`](docs/reference.md) | §10 | 参考链接 |

仓库根的其他文档：`AGENTS.md`（AI 协作须知）、`DEVELOPMENT_PLAN.md`（主约束与路线图）、
`outputs/`（任务书、报告与知识库；换装类新游戏上手照 `outputs/游戏类型-换装类上手手册.md` 的 SOP 七步执行）。

**想直接跑起来** → [`docs/quickstart.md`](docs/quickstart.md) 与
[`docs/action-layer.md`](docs/action-layer.md)（加新游戏只写一份 JSON 档案）。
