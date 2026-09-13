# AGENTS.md — AI 协作须知（game-sensei）

> 面向所有在本仓库工作的 AI 智能体。**开始任何工作前必须先读本文，再读 `DEVELOPMENT_PLAN.md` 与 `README.md` 对应章节。**
> 本文只写“AI 容易搞错、且必须记住”的事项；完整规范以 `DEVELOPMENT_PLAN.md` 为准，二者冲突时以 `DEVELOPMENT_PLAN.md` 为准。

---

## 0. 三条最重要的红线

### 0.1 知识归档分界（最常搞错，务必分清）

学到的东西分两处放，**别放反**：

| 内容 | 去处 |
|---|---|
| **单一游戏**的玩法、界面状态机、坐标、攻略、数值、关卡 | **只进 ima 知识库**（「游戏」KB `7504129723208370` → 文件夹 `game-sensei` `folder_7504129807094803`）。**不写进代码仓库。** |
| **通用**的游戏类型共同规则/节奏/决策模式、可复用的操作与工程经验 | **进代码仓库** `outputs/game-sensei-游戏类型知识库.md`（按类型分节累积） |

**一句话判断标准：换一款同类游戏还能不能用 —— 能 → 仓库；不能 → 知识库。**

- 标题格式：`游戏名-文档名`（如 `洛克王国：世界-攻略合集`、`Sparkle-新手教程流程与界面实测笔记`）；项目级文档用 `game-sensei-文档名`。
- ⚠️ 别写错库：`folder_7504129186334348` 属于「微信用户的知识库」，是早期误存位置。
- **不要用 `import_urls` 存网址**——必须 WebFetch 提取正文、整理成 markdown 再入库。
- ima MCP **没有建文件夹/删除**的工具；文件夹由用户在 App 手动建，误存条目也要用户手动删。
- 玩新游戏前先上网找**最新攻略**，抓正文、整理、入库再开玩。
- 完整入库流程见用户级技能 `ima-kb-ingest`（含 COS 签名上传脚本）；中转文件放 `.workbuddy/ima-uploads/`（已 gitignore），**cred.json 用完立即删**。

### 0.2 绝不用 `git add -A` / `git add .`

安酱可能在别的会话/窗口**并发**改同一工作区（2026-09-11 出现过并发写入 `internal/video/`）。
**必须逐个列出本次涉及的文件路径暂存**。提交只带本次涉及文件，中文 commit，验证通过后当轮自动 push。

### 0.3 区分「已实现 / 已单测 / 已本地验证 / 已真机验证 / 已发布」

**不以代码存在代替功能完成**。结论必须来自真实实测输出；调试服、测试环境、正式环境必须分清。

---

## 1. 动工前必读

- `DEVELOPMENT_PLAN.md` —— 主约束文档，含 AI 执行总规则（先理解后修改 / 小步变更 / 不覆盖用户工作：**不得 reset、checkout、clean 或删除探测产物** / 完成后闭环）。
- `README.md` —— 定位、架构、安装、命令、操作手册；**已确认功能的接口、参数、实测数据、限制，必须同步更新 README**（当前到 §11，§9.3 是双后端说明）。
- `outputs/game-sensei-学习能力开发计划.md` —— 只看学习路线图（P0/P1/P2）时读它。

---

## 2. 硬约束（不可违反）

- **零 CGO**：核心零 CGO（`CGO_ENABLED=0`）。不引 robotgo / onnxruntime_go 到核心；推理走 LazyDLL 挂 dll 或独立子进程。
- **只视觉 + 模拟输入**：不读内存、不接封包、不做绕过反作弊。
- **学生模型跑实时回路；教师模型只能异步**，不得阻塞实时控制。
- **PC 与安卓必须走统一 backend 和动作协议**；游戏差异放进 profile，不在核心逻辑硬编码。
- 仓库根 `/*.png /*.jpg /*.txt /*.status /*.ls` 已 gitignore（真机标定产物随时可重跑）。
- `.workbuddy/` 已 gitignore（含实测截图、评测报告、ima 上传中转文件）。**不要删这个目录**。

---

## 3. 架构速查

### 3.1 双后端

`cmd/helper/backend.go` 的 `backend` 接口（`Grab/GrabColor/Resolve/Apply/Profile/Size/Describe/Live/Close`）统一 PC 与安卓。

- `pc`（默认）：`internal/capture`（GDI BitBlt）+ `internal/input`（SendInput），30 FPS。
- `android`：`internal/android`（`adb.exe`），**默认自动降为 5 FPS**（screencap ≈0.94s/帧）。
- **横屏陷阱**：`ScreenSize()` 必须以缓存截图尺寸为准，物理 1200×2608 横屏下坐标系是 2608×1200。

### 3.2 动作空间分层（最重要的设计约束）

**L1 语义层（跨游戏通用，模型输出）↔ L2 执行层（平台相关，档案展开）**，中间由 `internal/game` 的 GameProfile 翻译。**换游戏只换一份 JSON 档案，Go 代码不动。**

- L1：`MOVE dir=前` / `PRESS name=jump` / `TAP` / `SWIPE` / `HOLD` / `KEY` / `WAIT`
- L2：`JOYSTICK` / `KEY`（Dur>0 = 按住）/ `TAP`（像素）/ `MouseMove`
- **模型只做 8 选 1 的方向分类**；摇杆中心与幅度是设备常量，交给档案。
- `Profile.Resolve` **幂等**：已是 L2 的动作原样返回，后端可无脑「先 Resolve 再执行」。
- 方向别名里单字母 `w/a/s/d` 按 **WASD 键位语义**（w=前），不按罗盘缩写。
- 斜向推杆用 1/√2 分量，保证八向幅度等长。
- 档案字段名拼错**会立刻报错**（`DisallowUnknownFields`），不许静默退化。

### 3.3 加一款新游戏（无需改 Go 代码）

1. `python tools/grid_overlay.py shot.png --region x0,y0,x1,y1 --zoom 2 -o out.jpg` 读坐标
2. 写 `internal/game/profiles/<游戏名>.json`（或放 `./profiles/` 覆盖内置）
3. `go run ./cmd/helper -game <档案名> -frames 100` dry-run 验证
4. `-game` 会带出档案里的 `package`，可省 `-app`

内置档案：`nrc`（洛克王国）/ `mobile_generic` / `pc_generic`。

---

## 4. 已踩过的坑（别重复踩）

- **Ollama `think` 开关必须放 `/api/chat` 请求顶层，不能放进 `options`**。放错位置 Ollama 直接忽略，表现为「关不掉思考、正文为空、打满 num_predict」。这是本项目最容易误判的一个坑。
- qwen3 系推理在 `message.thinking`、正文在 `message.content`，**两个字段都要兜底**；`num_predict ≥ 1500`。
- **小模型会逐字节照抄提示词里的示例**。写动作协议示例必须用 `<占位符>`，绝不放具体数字。
- **浮窗挂死**：overlay Hide→BitBlt→Show 与 GDI 互斥挂死，`-overlay` 默认 false（README §11.7）。
- **锁屏坑**：LockApp（类名 `Windows.UI.Core.CoreWindow`）在前台时 SendInput 点击落锁屏，GDI 会话级失效。**锁屏下窗口位置设置仍生效，但置顶会被前台锁拒绝**——先跑 `cmd/focusdbg` 诊断，别盲目重试；用 `cmd/unlock-watch` 等解锁后自动置顶。
- **窗口域是动态跟踪**：`SetWindowRegion(rect, keyword)` 每帧现查客户区，`Actuator.OffsetFunc` 每帧现查偏移——窗口拖动/缩放不失效。别改回静态缓存。
- **退出必须调 `ReleaseAll()`**，否则键盘卡在按下状态。
- **MuMu 模拟器**：自带 adb 版本旧，用 Android SDK 版 `adb.exe`；模拟器内 adb 端口 `127.0.0.1:16384`。详见用户级技能 `mumu-emulator-control`。

---

## 5. 完成闭环（每次改动）

```bash
gofmt -w <本次修改的.go文件>
go test ./...
go vet ./...
go build ./...
git diff --check
git status
```

- 只暂存本次涉及文件（见 §0.2），中文 commit，push。
- 真实设备/安卓功能必须补真实环境验证。
- 最终报告列出：改动文件、测试结果、真实验证环境、指标、未解决问题、是否可发布。
- **AI 输出任务结论必须区分**：已完成（有证据）/ 已验证（测试/真机/端到端）/ 未完成 / 已知限制 / 下一步。

---

## 6. 记忆与工具

- 项目日志：`.workbuddy/memory/YYYY-MM-DD.md`（**append-only**，当天已完成的**事实**）。
- 长期项目约定：`.workbuddy/memory/MEMORY.md`（就地更新）。
- 调试工具集 `cmd/`：`winlist`（列窗口）、`winshot`（置顶+截全屏）、`winclick`（置顶+点击）、`focusdbg`（前台 Win32 类名诊断）、`unlock-watch`（等解锁后置顶）、`overlay-test`。
- 标定工具 `tools/`：`grid_overlay.py`（坐标读数）、`detect_state` / `runstats` / `watch` / `movetest`（洛王国真机跑批）。
- CodeGraph：`C:/Users/ay/AppData/Local/codegraph/current/bin/codegraph.cmd`，重建索引 `codegraph init -i`；`.codegraph/` 已 gitignore。
- 老师模型本地 Ollama 项目实例：端口 **11435**，模型库 `.ollama/models`，启动 `tools/serve_ollama.sh`。
