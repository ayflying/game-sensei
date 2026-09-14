# 9. 快速开始（Phase 0）

> 本文是 [game-sensei](../README.md) 的文档分册：收录原 §9 前言、§9.1 教师模型环境、§9.2 异步教学回路。
> **章节编号沿用原 `README.md` 编号**，旧引用（如「README §11.16」）的对应关系见 [README §9 文档索引](../README.md#9-文档索引)。

---

```bash
# 构建并运行占位骨架
cd d:/git/game-sensei
go build ./...
go run ./cmd/helper
```

后续按 `internal/` 各包与 Phase 0→3 路线图逐步填充实现。

---

## 9.1 教师模型环境（项目自带 Ollama）

teacher 用的 VLM 默认走**项目内自带的 Ollama 实例**（独立端口 11435、模型库在 `.ollama/models`），
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

**远程老师（本机不跑 Ollama 时）：** 设置环境变量 `GAME_SENSEI_TEACHER_URL` 指向局域网内
另一台跑 Ollama 的机器，`helper`、`video` 与 `tools/` 下全部工具会自动跟随，无需逐个传参：

```bash
# PowerShell（当前会话有效）
$env:GAME_SENSEI_TEACHER_URL = "http://100.66.1.2:11434"

# bash
export GAME_SENSEI_TEACHER_URL=http://100.66.1.2:11434
```

优先级：**命令行显式传参（`-teacher-url` / `--url` / `--host`）> 环境变量 `GAME_SENSEI_TEACHER_URL` > 内置默认 `http://127.0.0.1:11435`**。

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
- **`think` 开关必须放在请求顶层，不能放进 `options`**（见 §9.4）。早前记录的
  「`think=false` 在本机 Ollama 0.34.0 上无效」是**错误结论**——原因是当时写成了
  `options.think`，Ollama 会直接忽略它。放对位置后效果立竿见影：
  同图同提示词从 **65s / 正文为空** 变成 **1.3s / 正文正常**。
- 同一模型在不同机器上速度差异极大（`qwen3-vl:2b` 远程 16~27 tok/s vs 本机 147 tok/s），
  **teacher 选型必须以目标机器实测为准**。

---

## 9.2 老师（异步教学回路）

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
| `-teacher-url` / `-teacher-model` | 老师地址与模型。地址默认取环境变量 `GAME_SENSEI_TEACHER_URL`（见 §9.1），未设置则为本机 `11435`；模型默认 `qwen3.5:9b` |
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

> 分册全目录见 [README §9 文档索引](../README.md#9-文档索引)。
