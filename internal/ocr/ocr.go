// Package ocr 提供确定性的界面文字识别：常驻 PP-OCR 子进程 + JSON 行协议。
//
// 为什么单独成包：读屏文字是跨游戏复用的基础能力（找按钮、读数值、核验界面态、
// 核对库存与价签），此前每次都在 `.workbuddy/tmp` 里现写 python 脚本，
// 既不可复用、也无法单测，还随任务结束被清掉。现在统一由本包提供，
// 命令入口 `cmd/ocr`，其他 Go 代码可直接 `NewEngine` 复用同一个常驻进程。
//
// 引擎形态选了「常驻子进程」而不是「每次 exec 一次」或「CGO 直挂 onnxruntime」：
//
//   - 冷启动一次要 import onnxruntime 并加载 det/rec/cls 三个模型，
//     实测约 2.0s；常驻之后单帧降到百毫秒级。读屏是高频操作，
//     每次都付 2s 会让「抓帧→识别→决策」的回路完全不可用。
//   - 项目核心零 CGO（AGENTS.md §2），不引 onnxruntime_go；而 PP-OCR 的
//     识别网络含 LSTM，纯 Go 手写算子不现实。子进程正是硬约束里
//     明确允许的推理落地方式。
//   - 子进程只认「PNG 字节 → 文字框」，与图像来源（GDI 抓屏 / ADB 截图 / 磁盘文件）
//     完全解耦，也不产生任何临时文件。
//
// 本包只做「图像 → 文字框」，不做语义判断：读到的文字对不对、
// 该点哪里，仍由调用方结合游戏档案决定。
package ocr

import (
	"bufio"
	"bytes"
	_ "embed"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"image"
	_ "image/jpeg" // 注册 JPEG 解码器：DecodeFile 声称支持 PNG/JPEG，缺这行时 jpeg 会报「不支持的格式」
	"image/png"
	"io"
	"os"
	"os/exec"
	"strings"
	"sync"
	"time"

	"github.com/ayflying/game-sensei/internal/vision"
)

// serverScript 是随包发布的 OCR 服务端脚本（internal/ocr/server.py）。
//
// 用 go:embed 内嵌而不是引用磁盘上的 .py：运行时不产生临时脚本文件，
// 也不依赖当前工作目录，`go run` 与编译后的二进制行为一致。
//
//go:embed server.py
var serverScript string

// maxPixels 是单次识别的像素上限（约 2000 万）。
//
// 超限直接报错而不是硬跑：PP-OCR 的检测阶段显存/内存占用随像素线性上涨，
// 一张 4K 截图能把它拖到几秒且结果未必更好。真要看局部就先 `-crop`，
// 这比让引擎悄悄变慢更好排查。
const maxPixels = 20_000_000

// startTimeout 是「等引擎就绪」的预算。
//
// 比单次识别超时宽松得多，因为这一步包含 python 冷启动 + 三个模型加载
// （首次读盘可能几秒）。解释器或依赖缺失时子进程会立刻退出、stdout 关闭，
// 读操作随即返回 EOF 而不必等满这个预算，所以给宽一点没有代价。
const startTimeout = 120 * time.Second

// Box 是文字框（原图像素坐标，左上原点）。
type Box struct {
	X0, Y0, X1, Y1 int
}

// Text 是一条识别结果。
type Text struct {
	Text  string
	Score float64
	Box   Box
}

// CX / CY 返回文字框中心，即「要点这个字该点的位置」。
func (t Text) CX() int { return (t.Box.X0 + t.Box.X1) / 2 }
func (t Text) CY() int { return (t.Box.Y0 + t.Box.Y1) / 2 }

// Config 是引擎配置；零值可用（全走默认）。
type Config struct {
	// Python 是解释器路径；空则按 .env 与自动探测决定（见 defaultPython）。
	Python string
	// Timeout 是单次识别超时；<=0 用 30s。
	Timeout time.Duration
	// Idle 是空闲多久自动关掉子进程；<=0 用 5 分钟，<0 表示不自动关。
	Idle time.Duration
	// BoxThresh / TextScore 是引擎阈值；<=0 用引擎自带默认。
	BoxThresh float64
	TextScore float64
}

// Engine 是一个常驻 OCR 引擎。并发安全：内部串行化，多个 goroutine 可共用。
type Engine struct {
	cfg Config

	mu      sync.Mutex
	cmd     *exec.Cmd
	stdin   io.WriteCloser
	stdout  *bufio.Reader
	stderr  *tailBuffer
	engine  string
	nextID  int
	timer   *time.Timer
	closed  bool
	started int // 子进程启动次数，用于诊断（重启是否频繁）
}

// NewEngine 创建引擎，此时**不启动**子进程；首次识别时才拉起。
//
// 懒启动的理由：`-check`、`-find` 这类调用可能因为图片不存在等原因
// 在识别前就返回，不该先付 2s 的启动成本。
func NewEngine(cfg Config) *Engine {
	if cfg.Timeout <= 0 {
		cfg.Timeout = 30 * time.Second
	}
	if cfg.Idle == 0 {
		cfg.Idle = 5 * time.Minute
	}
	return &Engine{cfg: cfg}
}

// Recognize 识别一张内存图像，返回**该图自身坐标系**的文字框。
func (e *Engine) Recognize(img image.Image) ([]Text, error) {
	if img == nil {
		return nil, fmt.Errorf("图像为空")
	}
	b := img.Bounds()
	if b.Dx() <= 0 || b.Dy() <= 0 {
		return nil, fmt.Errorf("图像尺寸为空")
	}
	if n := int64(b.Dx()) * int64(b.Dy()); n > maxPixels {
		return nil, fmt.Errorf("图像 %dx%d 共 %d 像素，超过 %d 上限；请先裁剪目标区域",
			b.Dx(), b.Dy(), n, maxPixels)
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return nil, fmt.Errorf("PNG 编码失败: %w", err)
	}

	e.mu.Lock()
	defer e.mu.Unlock()
	texts, err := e.requestLocked(base64.StdEncoding.EncodeToString(buf.Bytes()))
	if err != nil {
		return nil, err
	}
	e.touchIdleLocked()
	return texts, nil
}

// RecognizeFile 读文件、按 crop 裁剪、按 zoom 放大后识别。
//
// crop 为 "" 时用整图；zoom<=1 时不放大。**返回的坐标已映射回原图坐标系**
// （先除以 zoom 再补上裁剪偏移）——调用方拿到 (cx,cy) 就能直接点，
// 不必自己记裁剪偏移，这是最容易算错、也最该由库负责的一步。
func (e *Engine) RecognizeFile(path, crop string, zoom float64) ([]Text, error) {
	img, err := DecodeFile(path)
	if err != nil {
		return nil, err
	}
	ox, oy := 0, 0
	if crop != "" {
		b := img.Bounds()
		r, err := vision.ParseRect(crop, b.Dx(), b.Dy())
		if err != nil {
			return nil, err
		}
		img = vision.Crop(img, r)
		ox, oy = r.Min.X, r.Min.Y
	}
	img = vision.Zoom(img, zoom)

	texts, err := e.Recognize(img)
	if err != nil {
		return nil, err
	}
	inv := 1.0
	if zoom > 1 {
		inv = 1 / zoom
	}
	for i := range texts {
		b := &texts[i].Box
		b.X0 = int(float64(b.X0)*inv+0.5) + ox
		b.X1 = int(float64(b.X1)*inv+0.5) + ox
		b.Y0 = int(float64(b.Y0)*inv+0.5) + oy
		b.Y1 = int(float64(b.Y1)*inv+0.5) + oy
	}
	return texts, nil
}

// Close 关掉子进程。多次调用安全。
func (e *Engine) Close() error {
	e.mu.Lock()
	defer e.mu.Unlock()
	e.closed = true
	e.stopLocked()
	return nil
}

// Filter 返回 Text 里包含 sub 的条目（区分大小写的子串匹配）。
//
// 中文界面用不上大小写，但同一个函数也要能查英文包名/型号，所以不做归一化。
func Filter(texts []Text, sub string) []Text {
	if sub == "" {
		return texts
	}
	var out []Text
	for _, t := range texts {
		if strings.Contains(t.Text, sub) {
			out = append(out, t)
		}
	}
	return out
}

// EngineName 返回子进程自报的引擎名（未启动时为空），供诊断输出。
func (e *Engine) EngineName() string {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.engine
}

// StartCount 返回子进程被拉起的次数；>1 说明中途崩过，是排查线索。
func (e *Engine) StartCount() int {
	e.mu.Lock()
	defer e.mu.Unlock()
	return e.started
}

// ---- 内部实现 ----

type ocrRequest struct {
	ID        int      `json:"id"`
	B64       string   `json:"b64"`
	BoxThresh *float64 `json:"box_thresh,omitempty"`
	TextScore *float64 `json:"text_score,omitempty"`
}

type ocrResponse struct {
	Ready  bool   `json:"ready"`
	Engine string `json:"engine"`
	ID     int    `json:"id"`
	OK     bool   `json:"ok"`
	Error  string `json:"error"`
	Items  []struct {
		Text  string  `json:"text"`
		Score float64 `json:"score"`
		Box   [4]int  `json:"box"`
	} `json:"items"`
}

// requestLocked 发一次请求并读回响应。调用方必须已持有 e.mu。
func (e *Engine) requestLocked(b64 string) ([]Text, error) {
	if e.closed {
		return nil, fmt.Errorf("引擎已关闭")
	}
	if err := e.ensureLocked(); err != nil {
		return nil, err
	}
	e.nextID++
	req := ocrRequest{ID: e.nextID, B64: b64}
	if e.cfg.BoxThresh > 0 {
		v := e.cfg.BoxThresh
		req.BoxThresh = &v
	}
	if e.cfg.TextScore > 0 {
		v := e.cfg.TextScore
		req.TextScore = &v
	}
	raw, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("请求编码失败: %w", err)
	}
	raw = append(raw, '\n')
	if _, err := e.stdin.Write(raw); err != nil {
		e.killLocked()
		return nil, fmt.Errorf("向 OCR 子进程写入失败: %w；子进程输出：%s", err, e.stderrText())
	}
	line, err := readLine(e.stdout, e.cfg.Timeout)
	if err != nil {
		// 超时或管道断开都说明这条子进程不可信了：直接杀掉，下次调用重开。
		// 不在这里重试——重试会掩盖「模型卡住」这类持续性问题。
		e.killLocked()
		return nil, fmt.Errorf("OCR 识别失败: %w；子进程输出：%s", err, e.stderrText())
	}
	var resp ocrResponse
	if err := json.Unmarshal([]byte(line), &resp); err != nil {
		e.killLocked()
		return nil, fmt.Errorf("OCR 响应不是合法 JSON: %s", clip(line, 200))
	}
	if !resp.OK {
		// 引擎还活着（能回错误说明协议没断），只是这一帧失败，不杀进程。
		return nil, fmt.Errorf("OCR 引擎报错: %s", resp.Error)
	}
	out := make([]Text, 0, len(resp.Items))
	for _, it := range resp.Items {
		out = append(out, Text{
			Text:  it.Text,
			Score: it.Score,
			Box:   Box{X0: it.Box[0], Y0: it.Box[1], X1: it.Box[2], Y1: it.Box[3]},
		})
	}
	return out, nil
}

// ensureLocked 保证子进程活着并已完成就绪握手。
func (e *Engine) ensureLocked() error {
	if e.cmd != nil {
		return nil
	}
	py := e.cfg.Python
	if py == "" {
		py = DefaultPython()
	}
	if py == "" {
		return fmt.Errorf("找不到 python 解释器：在根目录 .env 里设 OCR_PYTHON，或把 python 加入 PATH")
	}
	cmd := exec.Command(py, "-c", launchCode())
	cmd.Env = append(os.Environ(),
		// 必须显式指定 UTF-8：Windows 下 python 默认按本地代码页（GBK）写 stdout，
		// 中文 JSON 会直接抛 UnicodeEncodeError，表现为「引擎起来就崩」。
		"PYTHONIOENCODING=utf-8",
		"PYTHONUTF8=1",
	)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return fmt.Errorf("创建 stdin 管道失败: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return fmt.Errorf("创建 stdout 管道失败: %w", err)
	}
	tail := newTailBuffer(4096)
	cmd.Stderr = tail
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("启动 OCR 子进程失败（%s）: %w", py, err)
	}
	e.cmd = cmd
	e.stdin = stdin
	e.stdout = bufio.NewReaderSize(stdout, 1<<20)
	e.stderr = tail
	e.started++

	line, err := readLine(e.stdout, startTimeout)
	if err != nil {
		msg := e.stderrText()
		e.killLocked()
		return fmt.Errorf("OCR 引擎未就绪（%v）；子进程输出：%s", err, msg)
	}
	var resp ocrResponse
	if err := json.Unmarshal([]byte(line), &resp); err != nil || !resp.Ready {
		msg := e.stderrText()
		e.killLocked()
		return fmt.Errorf("OCR 引擎就绪信号异常：%s；子进程输出：%s", clip(line, 200), msg)
	}
	e.engine = resp.Engine
	return nil
}

// launchCode 生成传给 `python -c` 的一行代码。
//
// 脚本先 base64 再内联，是为了让命令行参数**全 ASCII**：脚本里有中文注释，
// 直接塞进 -c 要经过 Windows 命令行的引号/换行转义，出问题时很难看出是转义
// 还是脚本本身的问题。base64 之后只可能因为长度超限失败，而本脚本约 2.5KB，
// 离 32KB 的命令行上限很远。
func launchCode() string {
	enc := base64.StdEncoding.EncodeToString([]byte(serverScript))
	return "import base64;exec(compile(base64.b64decode('" + enc +
		"').decode('utf-8'),'<game-sensei-ocr>','exec'))"
}

// touchIdleLocked 重置空闲计时器：到点自动关掉子进程，不长期占内存。
func (e *Engine) touchIdleLocked() {
	if e.cfg.Idle < 0 {
		return
	}
	if e.timer != nil {
		e.timer.Stop()
	}
	e.timer = time.AfterFunc(e.cfg.Idle, func() {
		e.mu.Lock()
		defer e.mu.Unlock()
		e.stopLocked()
	})
}

// stopLocked 优雅关停：先请它自己退出，超时才强杀。
func (e *Engine) stopLocked() {
	if e.timer != nil {
		e.timer.Stop()
		e.timer = nil
	}
	if e.cmd == nil {
		return
	}
	if e.stdin != nil {
		fmt.Fprintln(e.stdin, `{"cmd":"exit"}`)
		e.stdin.Close()
	}
	cmd := e.cmd
	done := make(chan struct{})
	go func() { cmd.Wait(); close(done) }()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		if cmd.Process != nil {
			cmd.Process.Kill()
		}
		<-done
	}
	e.cmd, e.stdin, e.stdout, e.stderr, e.engine = nil, nil, nil, nil, ""
}

// killLocked 强杀（异常路径用：超时、管道断了、握手失败）。
func (e *Engine) killLocked() {
	if e.cmd == nil {
		return
	}
	if e.stdin != nil {
		e.stdin.Close()
	}
	if e.cmd.Process != nil {
		e.cmd.Process.Kill()
	}
	e.cmd.Wait()
	e.cmd, e.stdin, e.stdout, e.stderr, e.engine = nil, nil, nil, nil, ""
}

func (e *Engine) stderrText() string {
	if e.stderr == nil {
		return ""
	}
	return clip(strings.TrimSpace(e.stderr.String()), 800)
}

// readLine 带超时读一行。
//
// 管道不支持 SetReadDeadline，只能用「后台 goroutine + select」模拟。
// 超时后那个 goroutine 仍阻塞在 Read 上，但调用方随即会 kill 子进程，
// 管道一关它就返回了——所以这里不额外做取消。
func readLine(r *bufio.Reader, timeout time.Duration) (string, error) {
	type result struct {
		line string
		err  error
	}
	ch := make(chan result, 1)
	go func() {
		line, err := r.ReadString('\n')
		ch <- result{line, err}
	}()
	select {
	case v := <-ch:
		if v.err != nil && v.line == "" {
			return "", v.err
		}
		return strings.TrimRight(v.line, "\r\n"), nil
	case <-time.After(timeout):
		return "", fmt.Errorf("等待响应超过 %v", timeout)
	}
}

// DecodeFile 读取并解码 PNG/JPEG。
func DecodeFile(path string) (image.Image, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, fmt.Errorf("打开图片失败: %w", err)
	}
	defer f.Close()
	img, format, err := image.Decode(f)
	if err != nil {
		return nil, fmt.Errorf("解码图片失败（仅支持 PNG/JPEG）: %w", err)
	}
	if format != "png" && format != "jpeg" {
		return nil, fmt.Errorf("不支持的图片格式 %q（仅支持 PNG/JPEG）", format)
	}
	return img, nil
}

func clip(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "...(截断)"
}

// tailBuffer 是只保留末尾若干字节的 writer，用来兜住子进程的报错输出。
//
// 为什么要留尾部：python 的栈回溯、缺包提示都在最后几行，前面的进度日志
// 没有价值；不设上限则一个刷屏的引擎能把内存吃干。
type tailBuffer struct {
	limit int
	buf   []byte
}

func newTailBuffer(limit int) *tailBuffer { return &tailBuffer{limit: limit} }

func (t *tailBuffer) Write(p []byte) (int, error) {
	t.buf = append(t.buf, p...)
	if len(t.buf) > t.limit {
		t.buf = t.buf[len(t.buf)-t.limit:]
	}
	return len(p), nil
}

func (t *tailBuffer) String() string { return string(t.buf) }
