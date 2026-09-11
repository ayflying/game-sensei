// Package android 通过 ADB 把 Android 设备接入 game-sensei 的两条回路。
//
// 与 internal/capture（Windows GDI 截屏）和 internal/input（Windows SendInput）
// 并列，这是另一种「后端」：程序仍跑在 PC 上，通过 USB / 网络调试遥控手机上的游戏。
//
// 依赖仅限标准库（os/exec），因此本包可跨平台编译，便于在 PC 上开发调试。
// 所有 adb 进程调用由 Device.mu 串行化：adb 自身虽可并发，但截图与触摸并发
// 会互相拖慢延迟，串行化换取可预测的时序。
package android

import (
	"bytes"
	"context"
	"fmt"
	"image"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync"
	"time"
)

// 默认超时：单条 shell 命令与二进制拉取（截图较大，给得宽些）。
const (
	defaultShellTimeout = 15 * time.Second
	defaultBinTimeout   = 30 * time.Second
)

// Info 描述一台 ADB 设备。
type Info struct {
	Serial  string
	State   string // device / offline / unauthorized
	Model   string
	Product string
}

// Device 是一台已连接设备的操作句柄。方法可被多个 goroutine 调用。
type Device struct {
	adb    string
	serial string

	mu sync.Mutex // 串行化 adb 调用
	// size 是最近一次成功截图得到的像素尺寸，即「当前方向的输入坐标系」。
	// 横竖屏切换后 wm size 仍报物理尺寸，故以截图为准。
	size image.Point
	// color 缓存最近一次截图的彩色原图，便于抓一次同时供决策（灰度）与送审（彩色存档）。
	color image.Image
}

// FindADB 依次在环境变量、PATH、常见安装位置查找 adb 可执行文件。
func FindADB() (string, error) {
	name := "adb"
	if runtime.GOOS == "windows" {
		name = "adb.exe"
	}

	// 1) 环境变量指向的 SDK
	for _, env := range []string{"ANDROID_HOME", "ANDROID_SDK_ROOT"} {
		if dir := os.Getenv(env); dir != "" {
			p := filepath.Join(dir, "platform-tools", name)
			if fileExists(p) {
				return p, nil
			}
		}
	}

	// 2) PATH（含 PATHEXT 解析）
	if p, err := exec.LookPath("adb"); err == nil {
		return p, nil
	}

	// 3) 常见安装位置
	var candidates []string
	if home, err := os.UserHomeDir(); err == nil {
		candidates = append(candidates,
			filepath.Join(home, "AppData", "Local", "Android", "Sdk", "platform-tools", name),
			filepath.Join(home, "Library", "Android", "sdk", "platform-tools", name),
			filepath.Join(home, "Android", "Sdk", "platform-tools", name),
		)
	}
	candidates = append(candidates,
		filepath.Join(`C:\`, "platform-tools", name),
		filepath.Join(`C:\`, "Android", "platform-tools", name),
		"/usr/lib/android-sdk/platform-tools/adb",
		"/usr/local/bin/adb",
	)
	for _, p := range candidates {
		if fileExists(p) {
			return p, nil
		}
	}
	return "", fmt.Errorf("android: 未找到 adb，可用 -adb 显式指定路径（或设置 ANDROID_HOME）")
}

// Devices 列出当前所有 ADB 设备。
func Devices(adbPath string) ([]Info, error) {
	adbPath, err := resolveADB(adbPath)
	if err != nil {
		return nil, err
	}
	ctx, cancel := context.WithTimeout(context.Background(), defaultShellTimeout)
	defer cancel()

	cmd := newCommand(ctx, adbPath, "devices", "-l")
	var out bytes.Buffer
	cmd.Stdout = &out
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("android: 执行 adb devices 失败: %w", err)
	}

	var list []Info
	for _, line := range strings.Split(out.String(), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "List of devices") || strings.HasPrefix(line, "*") {
			continue
		}
		fields := strings.Fields(line)
		if len(fields) < 2 {
			continue
		}
		info := Info{Serial: fields[0], State: fields[1]}
		for _, f := range fields[2:] {
			switch {
			case strings.HasPrefix(f, "model:"):
				info.Model = strings.TrimPrefix(f, "model:")
			case strings.HasPrefix(f, "product:"):
				info.Product = strings.TrimPrefix(f, "product:")
			}
		}
		list = append(list, info)
	}
	return list, nil
}

// Open 打开设备：adbPath 为空则自动查找；serial 为空则取唯一在线设备。
//
// 多台设备且未指定 serial 时返回错误并列出候选，避免误操作到别的手机。
func Open(adbPath, serial string) (*Device, error) {
	adbPath, err := resolveADB(adbPath)
	if err != nil {
		return nil, err
	}

	list, err := Devices(adbPath)
	if err != nil {
		return nil, err
	}

	if serial == "" {
		var online []Info
		for _, d := range list {
			if d.State == "device" {
				online = append(online, d)
			}
		}
		switch len(online) {
		case 0:
			if len(list) > 0 {
				return nil, fmt.Errorf("android: 有 %d 台设备但均未就绪（state=%s），请在手机上确认 USB 调试授权",
					len(list), list[0].State)
			}
			return nil, fmt.Errorf("android: 未发现设备，请确认 USB 已连接且已开启「USB 调试」")
		}
		if len(online) > 1 {
			var names []string
			for _, d := range online {
				names = append(names, d.Serial)
			}
			return nil, fmt.Errorf("android: 检测到多台在线设备 %v，请用 -serial 指定", names)
		}
		serial = online[0].Serial
	}

	d := &Device{adb: adbPath, serial: serial}
	if _, err := d.Shell("echo ok"); err != nil {
		return nil, fmt.Errorf("android: 设备 %s 不可用: %w", serial, err)
	}
	return d, nil
}

// Serial 返回设备序列号。
func (d *Device) Serial() string { return d.serial }

// Describe 返回用于日志的一行标识。
func (d *Device) Describe() string { return "android:" + d.serial }

// Shell 执行一条设备端 shell 命令并返回其标准输出（含管道等 shell 语法）。
func (d *Device) Shell(cmd string) (string, error) {
	return d.shellTimeout(cmd, defaultShellTimeout)
}

// ShellTimeout 同 Shell，但可指定超时（长按等操作需要更宽裕的时间）。
func (d *Device) ShellTimeout(cmd string, timeout time.Duration) (string, error) {
	return d.shellTimeout(cmd, timeout)
}

func (d *Device) shellTimeout(cmd string, timeout time.Duration) (string, error) {
	d.mu.Lock()
	defer d.mu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	c := newCommand(ctx, d.adb, "-s", d.serial, "shell", cmd)
	var stdout, stderr bytes.Buffer
	c.Stdout = &stdout
	c.Stderr = &stderr
	if err := c.Run(); err != nil {
		msg := strings.TrimSpace(stderr.String())
		if msg == "" {
			msg = strings.TrimSpace(stdout.String())
		}
		if ctx.Err() != nil {
			return "", fmt.Errorf("android: 命令超时（%s）: %s", timeout, cmd)
		}
		if msg != "" {
			return "", fmt.Errorf("android: 命令失败: %s: %w", msg, err)
		}
		return "", fmt.Errorf("android: 命令失败: %s: %w", cmd, err)
	}
	return stdout.String(), nil
}

// execOut 拉取二进制输出（截图用）。exec-out 不做换行转换，可安全承载二进制。
func (d *Device) execOut(args ...string) ([]byte, error) {
	d.mu.Lock()
	defer d.mu.Unlock()

	ctx, cancel := context.WithTimeout(context.Background(), defaultBinTimeout)
	defer cancel()

	full := append([]string{"-s", d.serial, "exec-out"}, args...)
	c := newCommand(ctx, d.adb, full...)
	var stdout, stderr bytes.Buffer
	c.Stdout = &stdout
	c.Stderr = &stderr
	if err := c.Run(); err != nil {
		if ctx.Err() != nil {
			return nil, fmt.Errorf("android: exec-out 超时: %v", args)
		}
		return nil, fmt.Errorf("android: exec-out 失败: %s: %w", strings.TrimSpace(stderr.String()), err)
	}
	if stdout.Len() == 0 {
		return nil, fmt.Errorf("android: exec-out 无输出: %v", args)
	}
	return stdout.Bytes(), nil
}

func resolveADB(p string) (string, error) {
	if p != "" {
		if !fileExists(p) {
			return "", fmt.Errorf("android: adb 路径不存在: %s", p)
		}
		return p, nil
	}
	return FindADB()
}

func fileExists(p string) bool {
	st, err := os.Stat(p)
	return err == nil && !st.IsDir()
}
