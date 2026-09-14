# 9.3 双后端：PC 桌面 / 安卓 ADB

> 本文是 [game-sensei](../README.md) 的文档分册：收录原 §9.3 双后端与输入注入（含 §9.3.1~§9.3.3）。
> **章节编号沿用原 `README.md` 编号**，旧引用（如「README §11.16」）的对应关系见 [README §9 文档索引](../README.md#9-文档索引)。

---

`backend` 接口统一了两类目标环境，实时回路与教学回路都只依赖接口：

```go
type backend interface {
    Grab(width int) (image.Image, error)   // 当前画面（灰度降采样）
    Size() (int, int)                      // 当前画面尺寸
    Describe() string                      // 人类可读的目标描述（进提示词 / 报告）
    Live() bool                            // 目标是否仍可操作
    Apply(act agent.Action) error          // 执行动作
}
```

| 后端 | 实现 | 画面来源 | 动作执行 |
|---|---|---|---|
| `pc`（默认） | `pcBackend` | `internal/capture` GDI BitBlt | `internal/input` user32!SendInput |
| `android` | `adbBackend` | `internal/android` `exec-out screencap -p` | `internal/android` `input tap/swipe/keyevent` |

**安卓后端能力（`internal/android`）：**

| 方法 | 说明 |
|---|---|
| `FindADB()` / `Devices()` | 定位 `adb.exe`（查 `ANDROID_HOME` / `ANDROID_SDK_ROOT` / PATH / 常见安装路径）；列出设备 |
| `Open(adbPath, serial)` | 连接指定设备；多设备且未指定 serial 时报错，不做隐式选择 |
| `Screenshot()` / `SavePNG()` | `exec-out screencap -p` 取 PNG 并解码，同时缓存画面尺寸 |
| `ScreenSize()` | **优先用缓存截图尺寸**（反映当前坐标系，横屏下为 2608×1200 而非物理 1200×2608）；退化时用 `wm size` + `rotationDegrees` 交换宽高 |
| `Tap` / `TapNorm` | 绝对坐标 / 归一化坐标（0~1）点击 |
| `Swipe` / `SwipeNorm` / `LongPress` / `LongPressNorm` | 滑动、长按（长按=原地 swipe） |
| `Joystick(cx,cy,dx,dy,dur)` / `JoystickNorm` | 虚拟摇杆：按住中心后推偏移量并保持 dur，再抬起 |
| `Key(code)` | 按键，键名归一化（`home`/`back`/`enter` 等 → `KEYCODE_*`） |
| `Launch(pkg)` | `resolve-activity` + `am start`，失败回退 `monkey -p` |
| `Foreground()` / `IsForeground()` / `WaitForeground()` | 前台包名解析（`mCurrentFocus` / `mFocusedApp` / `mResumedActivity` 三种输出格式都兼容） |

**关键约束与实测数据：**

- **横屏判定**：安卓游戏多为横屏，但物理分辨率是竖屏值。`ScreenSize()` 必须以当前截图尺寸为准，
  否则归一化坐标会整体错位（这是早期一次踩坑，已修复并有 `parseForegroundPkg` / `rotationDegrees` 单测覆盖）。
- **零 CGO 保持**：ADB 后端全部通过 `os/exec` 调用 `adb.exe`，Windows 下带 `CREATE_NO_WINDOW`
  （`0x08000000`）+ `HideWindow`，不弹黑窗口；核心仍然 `CGO_ENABLED=0`。
- **动作空间分层**：`agent.Action` 分 L1 语义层（`ActionMove` / `ActionPress`，
  跨游戏通用）与 L2 执行层（`ActionTap` / `ActionSwipe` / `ActionLongPress` /
  `ActionJoystick` / `ActionKey` / `ActionMouseMove`，平台相关），坐标一律归一化
  （0~1），由游戏档案负责两层之间的翻译。详见 §9.5。
- **安卓默认帧率自动降为 5 FPS**（`screencap` 约 0.94s/帧、`input` 约 0.3~0.6s/次），
  未显式指定 `-fps` 时由 `openBackend` 自动设置——**PC 的 30FPS 假设在 ADB 上不成立**。

**用法：**

```bash
# PC 后端（默认，与 Phase 0 行为一致）
go run ./cmd/helper

# 安卓后端：连上手机，拉起游戏并接管
go run ./cmd/helper -target android -app com.tencent.nrc -launch

# 多设备时指定序列号；-adb 可显式指定 adb.exe
go run ./cmd/helper -target android -serial 1cd89cd4 -adb "D:/sdk/platform-tools/adb.exe" -app com.tencent.nrc

# 安卓 + 老师查岗（干跑，不发送真实触控）
go run ./cmd/helper -target android -app com.tencent.nrc -launch -dry-run -teacher \
  -goal "在洛克王国：世界中学会移动与捕捉精灵" -eval-out .workbuddy/eval-reports
```

| 参数 | 说明 |
|---|---|
| `-target` | `pc`（默认）/ `android` |
| `-adb` | `adb.exe` 路径；空则自动查找 |
| `-serial` | 设备序列号；单设备可省，多设备必填 |
| `-app` | 安卓包名（如 `com.tencent.nrc`），仅安卓后端用；`-game` 的档案带包名时可省 |
| `-launch` | 启动 `-app` 指定的应用并等待其进入前台 |
| `-game` | 游戏档案（档案名或 JSON 路径），决定动作怎么落到设备上，见 §9.5 |
| `-overlay` | PC 模式默认开：屏幕右下角置顶日志浮窗（见下） |
| `-minimize-on-exit` | PC 模式默认开：运行结束时把游戏窗口最小化（见下） |
| `-focus-on-start` | PC 模式默认开：启动时把游戏窗口还原并切到前台（见下） |

---

## 9.3.1 PC 档案：洛克王国：世界（nrc_pc）

`internal/game/profiles/nrc_pc.json`——PC 键鼠版的洛克王国档案，与手游版 `nrc` 的差异：

- **移动走键盘**：`move.mode = keys`（WASD）；斜向由框架自动组合双键
  （右下 = 同时按住 S+D），`MOVE dir=down_right` 展开为 `keys:s+d/500ms`，实测解析正确。
- **按钮是键盘键不是坐标**：`Button` 新增 `key` 字段，PRESS 落成按键而非点击。
  键位来自游戏内标注与官方键位表（F 交互 / E 精灵球 / Q 星星魔法 / 空格跳跃·投球 /
  R 坐骑飞翔 / C 下降 / Shift 冲刺 / M 地图 / ESC 菜单 / X 取消 / F4 任务 / F5 精灵）。
- 档案 `name` 保持「洛克王国：世界」与窗口标题一致——`-focus-on-start` /
  `-minimize-on-exit` 靠它匹配窗口。
- 用法：`go run ./cmd/helper -game nrc_pc -live -demo`。

---

## 9.3.2 ⚠️ 实测结论：PC 版注入输入被游戏反作弊拦截（2026-09-12）

**PC 版洛克王国：世界（腾讯）对一切用户态模拟输入免疫**，完整的实验证据链：

| 实验 | 结果 |
|---|---|
| `GetForegroundWindow` | ✓ 游戏就在前台 |
| 进程完整性级别 | 双方都是 elevated（管理员），UIPI 排除 |
| `SendInput` 注入 W（带扫描码）| `GetAsyncKeyState(W)=true`——注入进了系统输入流，**角色不动** |
| `PostMessage` / `SendMessage` `WM_KEYDOWN` | 角色不动 |
| 鼠标 `SendInput` 拖动转视角 | 视角不动（同时段任务距离 7米→10米，游戏本身活着）|

结论：游戏反作弊（腾讯 ACE 类内核驱动）在输入到达游戏前丢弃了带 `LLKHF_INJECTED`
标志的键盘/鼠标事件，且窗口消息路径也被过滤。**修扫描码仍要保留**——那是 UE4/
DirectInput 类游戏的标准要求（见 §9.3.3），但对这个游戏不够。

可行的后续路线（按侵入度排序）：

1. **安卓版 + ADB 后端**（推荐，零额外成本）：`adb shell input` 在系统框架层注入，
   游戏进程侧无法区分，一般不受游戏反作弊影响；`nrc` 档案与安卓后端均已就绪。
2. **驱动级注入**：Interception 等过滤驱动——需要装驱动，复杂度与签名成本高。
3. **硬件级**：Arduino/CH552 模拟 USB HID 键盘——最接近真人输入，但要硬件。

---

## 9.3.3 注入必须带扫描码（SendInput 的坑）

`keyDown/keyUp` 现在带 `KEYEVENTF_SCANCODE` + `MapVirtualKeyW` 换算的扫描码：
只给 VK 的注入事件，系统消息循环（WM_KEYDOWN）认，但 UE4/DirectInput/Raw Input
这类**按扫描码轮询键盘的引擎直接忽略**。Vk 字段保留，两类消费者各取所需。
（用 `go run ./cmd/win press w 1000` 可随时自测注入链路。）


**PC 实测体验（`internal/overlay` + `internal/gamewin`，Windows only，均默认开启）：**

- **启动置顶（`-focus-on-start`）**：PC 实时控制的前提是游戏窗口**在前台且未最小化**——
  最小化的窗口 GDI 抓不到内容（截出来是桌面），后台窗口收不到键盘焦点（`SendInput`
  的按键会落到别的程序上）。第一版只有"退出最小化"没有"启动还原"，于是第二次跑
  Agent 就在对着桌面盲操作（实测：游戏明明在主屏，抓屏却只看到桌面）。现在启动时按
  档案名还原并置顶，与退出最小化配成一对。置顶绕开了 Windows 的**前台锁**：
  ① 先 `keybd_event` 补一次 Alt 按下+抬起，让系统认为"用户刚按过键"；
  ② 再 `AttachThreadInput` 把自身消息队列接到当前前台线程上借其前台权
  （`AttachThreadInput` 在 **user32.dll**，不在 kernel32——第一版挂错 DLL 直接 panic）。
  只作用于可见/最小化的窗口：同款游戏常有多个同名顶层窗（启动器、反作弊壳、隐藏消息窗），
  对隐藏窗口置顶无意义还可能抢到空壳上。
- **日志浮窗**：屏幕右下角一块置顶半透明黑框，实时滚动最近的运行日志（每步动作、
  执行结果、警告）。关键性质与踩过的坑：
  1. **游戏全屏也可见**——`WS_EX_TOPMOST` + 每 2 秒重新钉顶（防全屏切换后掉下去）；
  2. **抓屏时临时隐藏**——`WDA_EXCLUDEFROMCAPTURE` 对 Windows.Graphics.Capture 是
     「窗口消失」，但对 **GDI BitBlt 是「该区域变黑」**：实测截屏里留一块纯黑矩形，
     老师 VLM 每帧都看得到，感知照样被污染。所以 `pcBackend` 每次抓屏前 `Hide()`、
     抓完立刻 `Show()`，截屏完全干净（demo/教学秒级抓屏频率下人眼无感）；
  3. **不抢焦点不抢键盘**——`WS_EX_NOACTIVATE | WS_EX_TOOLWINDOW`；
  4. **渲染三坑**（2026-09-12 实拍「左上角黑块」的完整归因）：
     a. `UpdateLayeredWindow` 的 `pptDst` 参数传 `(0,0)` 不是「忽略位置」而是
        **把窗口移到 (0,0)**——浮窗跑到了左上角。必须传 NULL；
     b. `CreateFontW` 的返回值必须接住再 `SelectObject`——没接住选进去的是 NULL
        字体，浮窗只剩黑底没有字；
     c. **GDI（DrawTextW）不写 alpha 字节**：只改 RGB，alpha 保持底色值。
        半透明要靠 ULW 的整窗 `SrcConstantAlpha`（170≈67%），DIB 本身保持不透明，
        别指望逐像素 alpha。
  5. 关闭用 `PostThreadMessageW(WM_QUIT)` 向消息泵线程投递——
     **`PostQuitMessage` 只对调用线程生效**，第一版从主线程调它导致消息泵永远阻塞、
     进程退出不去（已修复并实测进程自动退出）。
     ⚠️ 若进程异常被杀，浮窗窗体不会自己消失（僵尸浮窗会一直糊在屏幕上并污染
     抓屏）——`tasklist` 找残留 `helper.exe` 杀掉即可，`go run ./cmd/win list` 能看到它。
- **退出最小化**：程序结束时按游戏档案名（如「洛克王国：世界」）枚举顶层窗口，
  `ShowWindowAsync(SW_MINIMIZE)` 最小化游戏，让用户立刻看到终端里的评估/对话输出。
  `ShowWindowAsync` 而非 `ShowWindow`：不等待游戏主循环响应，不卡退出流程。
- 浮窗可视化自测工具：`go run ./cmd/overlay-test -seconds 15`（打印示例日志 15 秒）。
- **窗口探照灯 `cmd/win`**：跑 Agent 前先看清"Agent 眼中的窗口是什么状态"。
  `go run ./cmd/win list [关键词]` 列出顶层窗口（含 hwnd/pid/可见性/最小化/矩形/标题），
  `go run ./cmd/win focus <关键词>` 手动还原+置顶，
  `go run ./cmd/win shot out.png` 用**与实时回路同一条 GDI 抓屏链路**存一张图——
  用它确认感知画面正常再起 LIVE，别对着桌面盲操作。
  注意 `list` 会先 `SetProcessDPIAware`：缩放 125%/150% 的屏上不调它，
  `GetWindowRect` 与抓到的图都是被虚拟化的"逻辑像素"，坐标会算错。


---

> 分册全目录见 [README §9 文档索引](../README.md#9-文档索引)。
