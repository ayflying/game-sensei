// Package uinject 是「uinput 虚拟触摸屏注入」的宿主侧控制层。
//
// 背景与适用场景见 internal/uinject/device 的包注释：当设备的 adb shell 失去
// INJECT_EVENTS 权限（实测 Redmi K50 / Android 14 / HyperOS V816），
// internal/android 的 `input tap/swipe/keyevent` 全线失效，此时改用本包——
// 在设备上跑一个 uinput 守护进程，宿主通过 `adb shell` 的分节 stdin/stdout 驱动它。
//
// 本包只负责「编译并推送设备侧程序、建立会话、发命令」；坐标换算在
// internal/uinject/coord（可单测），设备侧事件合成在 internal/uinject/device。
package uinject

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/ayflying/game-sensei/internal/android"
	"github.com/ayflying/game-sensei/internal/uinject/coord"
)

const (
	// RemoteBin 是设备侧守护进程在设备上的落盘路径。
	RemoteBin = "/data/local/tmp/uinject"
	// DevicePkg 是设备侧程序的包路径（相对仓库根目录）。
	DevicePkg = "./internal/uinject/device"
	// binDir 是宿主侧编译产物的临时目录（.workbuddy/tmp 已在 .gitignore 内）。
	binDir = ".workbuddy/tmp/uinject-device"
)

// Options 是建立注入会话所需的参数。
type Options struct {
	ADBPath string // adb 路径；空=自动查找
	Serial  string // 设备序列号；空=取唯一在线设备
	Port    int    // adb server 端口；0=默认
	RepoDir string // 仓库根目录（含 go.mod）；空=当前工作目录

	Rebuild  bool          // 强制重新编译并推送设备侧程序
	Rot      int           // 显示旋转角度；-1=按屏幕逻辑尺寸自动推断
	RX, RY   int           // 虚拟设备原始值域；0=按物理竖屏尺寸推断
	Keyboard bool          // 同时创建虚拟键盘（-kb，用于唤醒键）
	Timeout  time.Duration // 等待 READY 的超时；0=8s
}

// Session 是一条与设备侧守护进程的活连接。
//
// 注意：会话存续期间触摸设备一直存在；一旦宿主进程退出（或 Close），
// 设备侧进程终止，内核随之销毁虚拟设备，正在按住的触点会被抬起。
type Session struct {
	cmd  *exec.Cmd
	in   io.WriteCloser
	rd   *bufio.Reader
	info string
	dev  *android.Device

	need int // 已发送但未读完的回答条数
}

// Info 返回设备侧守护进程的 READY 行（含实际旋转与值域，便于留证据）。
func (s *Session) Info() string { return s.info }

// Device 暴露底层设备句柄（用于截图等旁路操作）。
func (s *Session) Device() *android.Device { return s.dev }

// Serial 返回设备序列号。
func (s *Session) Serial() string {
	if s.dev == nil {
		return ""
	}
	return s.dev.Serial()
}

// Command 发送一条设备命令并等待它的回答行。
func (s *Session) Command(line string) (string, error) {
	if _, err := io.WriteString(s.in, line+"\n"); err != nil {
		return "", fmt.Errorf("写入命令失败: %w", err)
	}
	s.need++
	reply, err := s.rd.ReadString('\n')
	if err != nil {
		return "", fmt.Errorf("读取回答失败: %w", err)
	}
	s.need--
	reply = strings.TrimSpace(reply)
	if strings.HasPrefix(reply, "ERR ") {
		return reply, errors.New(strings.TrimPrefix(reply, "ERR "))
	}
	return reply, nil
}

// Close 结束会话：先请守护进程自毁虚拟设备，再回收进程。
func (s *Session) Close() error {
	if s.cmd == nil {
		return nil
	}
	// 尽力而为：设备侧进程退出时内核会销毁虚拟设备。
	_, _ = io.WriteString(s.in, "quit\n")
	_ = s.in.Close()
	done := make(chan error, 1)
	go func() { done <- s.cmd.Wait() }()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		_ = s.cmd.Process.Kill()
		<-done
	}
	s.cmd = nil
	return nil
}

// Install 编译设备侧程序（GOOS=android GOARCH=arm64 CGO_ENABLED=0）并推送到设备。
//
// 返回本地产物路径与「是否重新编译了」。
func Install(opt Options) (local string, built bool, err error) {
	repo, err := repoRoot(opt.RepoDir)
	if err != nil {
		return "", false, err
	}
	adbPath, err := android.FindADB()
	if err != nil {
		return "", false, err
	}
	if opt.ADBPath != "" {
		adbPath = opt.ADBPath
	}
	d, err := android.OpenServer(adbPath, opt.Serial, opt.Port)
	if err != nil {
		return "", false, err
	}

	outDir := filepath.Join(repo, filepath.FromSlash(binDir))
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return "", false, fmt.Errorf("创建产物目录失败: %w", err)
	}
	local = filepath.Join(outDir, "uinject-android-arm64")

	if opt.Rebuild || !fileExists(local) {
		if err := buildDevice(repo, local); err != nil {
			return "", false, err
		}
		built = true
	}

	// 推送与授权：无论是否新编译都推一次，避免设备端残留旧版本。
	if _, err := adbRun(adbPath, d.Serial(), "push", local, RemoteBin); err != nil {
		return "", built, err
	}
	if _, err := d.Shell("chmod 755 " + RemoteBin); err != nil {
		return "", built, fmt.Errorf("chmod 设备侧程序失败: %w", err)
	}
	return local, built, nil
}

// buildDevice 交叉编译设备侧程序。
func buildDevice(repo, out string) error {
	cmd := newCommand("go", "build", "-trimpath", "-ldflags", "-s -w", "-o", out, DevicePkg)
	cmd.Dir = repo
	cmd.Env = append(os.Environ(), "GOOS=android", "GOARCH=arm64", "CGO_ENABLED=0")
	outBytes, err := cmd.CombinedOutput()
	if err != nil {
		return fmt.Errorf("编译设备侧程序失败（%v）:\n%s", err, strings.TrimSpace(string(outBytes)))
	}
	if !fileExists(out) {
		return fmt.Errorf("编译完成但没有产物: %s", out)
	}
	return nil
}

// Start 建立注入会话：确保设备侧程序就位，探测屏幕尺寸与旋转，拉起守护进程。
func Start(opt Options) (*Session, error) {
	repo, err := repoRoot(opt.RepoDir)
	if err != nil {
		return nil, err
	}
	adbPath, err := android.FindADB()
	if err != nil {
		return nil, err
	}
	if opt.ADBPath != "" {
		adbPath = opt.ADBPath
	}
	d, err := android.OpenServer(adbPath, opt.Serial, opt.Port)
	if err != nil {
		return nil, err
	}

	if _, _, err := Install(Options{
		ADBPath: adbPath, Serial: d.Serial(), Port: opt.Port,
		RepoDir: repo, Rebuild: opt.Rebuild,
	}); err != nil {
		return nil, err
	}

	// 以「截图实际尺寸」为准判断横竖屏（AGENTS §3.1 的横屏陷阱：
	// wm size 给的是物理竖屏尺寸，游戏横屏时触摸映射用的是旋转后的尺寸）。
	img, err := d.Screenshot()
	if err != nil {
		return nil, fmt.Errorf("截图探测屏幕尺寸失败: %w", err)
	}
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	if w <= 0 || h <= 0 {
		return nil, fmt.Errorf("截图尺寸异常: %dx%d", w, h)
	}

	rot := opt.Rot
	if rot < 0 {
		rot = coord.InferRot(w, h)
	}
	rx, ry := opt.RX, opt.RY
	if rx <= 0 || ry <= 0 {
		// 原始值域取物理竖屏尺寸：短边作 X、长边作 Y。
		if w > h {
			rx, ry = h, w
		} else {
			rx, ry = w, h
		}
	}

	args := []string{"-s", d.Serial(), "shell", RemoteBin,
		"-rot", strconv.Itoa(rot),
		"-w", strconv.Itoa(w), "-h", strconv.Itoa(h),
		"-rx", strconv.Itoa(rx), "-ry", strconv.Itoa(ry),
	}
	if opt.Keyboard {
		args = append(args, "-kb")
	}

	cmd := newCommand(adbPath, args...)
	stdin, err := cmd.StdinPipe()
	if err != nil {
		return nil, fmt.Errorf("创建 stdin 管道失败: %w", err)
	}
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return nil, fmt.Errorf("创建 stdout 管道失败: %w", err)
	}
	cmd.Stderr = os.Stderr
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("启动设备侧守护进程失败: %w", err)
	}

	timeout := opt.Timeout
	if timeout <= 0 {
		timeout = 8 * time.Second
	}
	rd := bufio.NewReader(stdout)
	type readResult struct {
		line string
		err  error
	}
	ch := make(chan readResult, 1)
	go func() {
		l, err := rd.ReadString('\n')
		ch <- readResult{l, err}
	}()
	select {
	case r := <-ch:
		if r.err != nil {
			_ = cmd.Process.Kill()
			return nil, fmt.Errorf("设备侧守护进程未就绪: %w", r.err)
		}
		info := strings.TrimSpace(r.line)
		if !strings.HasPrefix(info, "READY") {
			_ = cmd.Process.Kill()
			return nil, fmt.Errorf("设备侧守护进程返回异常: %s", info)
		}
		return &Session{cmd: cmd, in: stdin, rd: rd, info: info, dev: d}, nil
	case <-time.After(timeout):
		_ = cmd.Process.Kill()
		return nil, fmt.Errorf("等待设备侧守护进程 READY 超时（%s）；可加 -rot/-w/-h 明确参数重试", timeout)
	}
}

// Screencap 抓一张图到本地（脚本模式下用于「按住时取证」这类序列）。
func (s *Session) Screencap(localPath string) error {
	remote := "/sdcard/uinject-shot.png"
	if _, err := s.dev.Shell("screencap -p " + remote); err != nil {
		return fmt.Errorf("设备侧截图失败: %w", err)
	}
	if dir := filepath.Dir(localPath); dir != "" {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return fmt.Errorf("创建本地目录失败: %w", err)
		}
	}
	adbPath, err := android.FindADB()
	if err != nil {
		return err
	}
	if _, err := adbRun(adbPath, s.dev.Serial(), "pull", remote, localPath); err != nil {
		return err
	}
	_, _ = s.dev.Shell("rm -f " + remote)
	return nil
}

func repoRoot(dir string) (string, error) {
	if dir == "" {
		wd, err := os.Getwd()
		if err != nil {
			return "", err
		}
		dir = wd
	}
	if !fileExists(filepath.Join(dir, "go.mod")) {
		return "", fmt.Errorf("%s 下没有 go.mod；请在仓库根目录运行，或用 -repo 指定", dir)
	}
	return dir, nil
}

func adbRun(adbPath, serial string, args ...string) (string, error) {
	full := append([]string{"-s", serial}, args...)
	cmd := newCommand(adbPath, full...)
	out, err := cmd.CombinedOutput()
	text := strings.TrimSpace(string(out))
	if err != nil {
		return text, fmt.Errorf("adb %s 失败: %v: %s", strings.Join(args, " "), err, text)
	}
	return text, nil
}

func fileExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && !st.IsDir()
}

// ParseInts 把 "1 2 3" 这类参数串解析成整数切片，供 CLI 复用。
func ParseInts(spec string) ([]int, error) {
	fields := strings.Fields(spec)
	out := make([]int, 0, len(fields))
	for _, f := range fields {
		v, err := strconv.Atoi(f)
		if err != nil {
			return nil, fmt.Errorf("参数 %q 不是整数", f)
		}
		out = append(out, v)
	}
	return out, nil
}
