// Package video 把「教学视频」拆成可送审 VLM 的画面段。
//
// # 定位
//
// 这是**离线工具链**，不进实时回路：录像 → 抽帧 → 去重 → 切段 →
// 交给老师逐段判读。目的是把「人已经玩明白的操作」变成机器能读的
// 策略知识，补上冷启动阶段老师只能靠自己瞎猜的短板。
//
// # 为什么不直接用 ffmpeg 的场景检测
//
// ffmpeg 的 `select='gt(scene,X)'` 看起来正合适，但实测有两个问题：
//
//   - 录屏经过有损压缩，scene 分数抖动大，同一类变化有时 0.4 有时 0.1，
//     阈值定在哪里都能挑出反例；
//   - 它的语义是「变化开启」而不是「去重」，一段静止的讲解画面会被
//     整段漏掉——而对操作教学，「停在某个界面不动」往往正是重点。
//
// 所以主路径改成「ffmpeg 定频抽帧 + Go 侧 dHash 去重 + 切段」：
// 结果可复现（同参数同输入必然同输出）、可单测、去重率本身还能当指标看。
package video

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"image"
	_ "image/jpeg" // 注册 JPEG 解码器：抽帧产物是 jpg
	_ "image/png"  // 注册 PNG 解码器：允许直接喂 png 截图做实验
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"time"
)

// extractTimeout 是单次抽帧的超时上限。
//
// 给得很宽：38 分钟的 720p 录像抽 2fps 大约 1~3 分钟，但用户可能拿
// 两小时的直播录像来抽，不能因为超时太紧把正常长任务砍掉。
// 设这个上限只是为了不让进程在 ffmpeg 卡死时永远挂着。
const extractTimeout = 60 * time.Minute

// Info 是 ffprobe 探测出的视频元信息。
type Info struct {
	Path       string  `json:"path"`
	DurationMs int64   `json:"duration_ms"`
	Width      int     `json:"width"`
	Height     int     `json:"height"`
	FPS        float64 `json:"fps"`
	Codec      string  `json:"codec"`
	SizeBytes  int64   `json:"size_bytes"`
}

// Duration 返回视频时长。
func (i Info) Duration() time.Duration { return time.Duration(i.DurationMs) * time.Millisecond }

// Frame 是抽帧序列中的一帧。
type Frame struct {
	// Index 在本序列中的序号，从 1 开始。
	// 未去重时等于抽帧序号；去重后由 Renumber 重排为连续编号（此时原序号见 Raw）。
	Index int `json:"index"`
	// Raw 原始抽帧序号（去重后仍可回溯到抽帧产物）；未重编号时为空。
	Raw int `json:"raw,omitempty"`
	// File 帧文件路径。
	File string `json:"file"`
	// AtMs 该帧在**源视频**中的时间（已含抽帧起点的偏移）。
	AtMs int64 `json:"at_ms"`
	// Hash 该帧的 dHash 指纹。
	Hash uint64 `json:"hash"`
}

// ExtractOptions 是抽帧参数。
type ExtractOptions struct {
	// Rate 抽帧频率（帧/秒），必须 > 0。录屏类素材 2fps 通常够用：
	// 再密只是把同一动作拆成更多近重复帧，去重后剩不下多少。
	Rate float64
	// Width 输出宽度（等比缩放）；<=0 保持原始分辨率。
	Width int
	// Quality JPEG 质量（2~31，越小越好）；不在区间内时用默认 3。
	Quality int
	// MaxFrames 抽帧上限；<=0 表示不限。
	MaxFrames int
	// Start / End 截取的时间区间（End 为零表示到结尾）。
	Start time.Duration
	End   time.Duration
	// Stderr 进度与警告输出；nil 则静默，只在失败时返回错误。
	Stderr io.Writer
}

// FindTool 定位 ffmpeg / ffprobe 可执行文件。
//
// 先查 PATH，再兜几个 Windows 常见安装位置。找不到时把尝试过的位置
// 一并写进错误——比只回一句「找不到 ffmpeg」省一轮排查。
func FindTool(name string) (string, error) {
	if p, err := exec.LookPath(name); err == nil {
		return p, nil
	}
	var tried []string
	if runtime.GOOS == "windows" {
		exe := name + ".exe"
		for _, dir := range []string{
			`C:\ffmpeg\bin`,
			`C:\Program Files\ffmpeg\bin`,
			`C:\Program Files (x86)\ffmpeg\bin`,
		} {
			p := filepath.Join(dir, exe)
			if _, err := os.Stat(p); err == nil {
				return p, nil
			}
			tried = append(tried, p)
		}
	}
	return "", fmt.Errorf("video: 找不到 %s（不在 PATH，也没在 %s 里）", name, strings.Join(tried, "、"))
}

// Probe 读取视频元信息。
func Probe(ffprobe, path string) (Info, error) {
	abs, err := filepath.Abs(path)
	if err != nil {
		abs = path
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	cmd := newCommand(ctx, ffprobe,
		"-v", "error",
		"-select_streams", "v:0",
		"-show_entries", "stream=width,height,avg_frame_rate,codec_name",
		"-show_entries", "format=duration,size",
		"-of", "json",
		path,
	)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return Info{}, fmt.Errorf("video: ffprobe 读取 %s 失败: %w（%s）",
			filepath.Base(path), err, tail(stderr.String(), 300))
	}

	var raw struct {
		Streams []struct {
			CodecName    string `json:"codec_name"`
			Width        int    `json:"width"`
			Height       int    `json:"height"`
			AvgFrameRate string `json:"avg_frame_rate"`
		} `json:"streams"`
		Format struct {
			Duration string `json:"duration"`
			Size     string `json:"size"`
		} `json:"format"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &raw); err != nil {
		return Info{}, fmt.Errorf("video: 解析 ffprobe 输出失败: %w", err)
	}
	if len(raw.Streams) == 0 {
		return Info{}, fmt.Errorf("video: %s 中没有视频流", filepath.Base(path))
	}

	s := raw.Streams[0]
	info := Info{
		Path:   abs,
		Width:  s.Width,
		Height: s.Height,
		FPS:    parseFrameRate(s.AvgFrameRate),
		Codec:  s.CodecName,
	}
	if d, err := strconv.ParseFloat(strings.TrimSpace(raw.Format.Duration), 64); err == nil && d > 0 {
		info.DurationMs = int64(d * 1000)
	}
	if n, err := strconv.ParseInt(strings.TrimSpace(raw.Format.Size), 10, 64); err == nil {
		info.SizeBytes = n
	}
	return info, nil
}

// parseFrameRate 解析 ffprobe 的分数帧率："30/1"、"30000/1001"，以及无意义的 "0/0"。
func parseFrameRate(s string) float64 {
	s = strings.TrimSpace(s)
	if s == "" {
		return 0
	}
	num, den, ok := strings.Cut(s, "/")
	if !ok {
		v, err := strconv.ParseFloat(s, 64)
		if err != nil {
			return 0
		}
		return v
	}
	n, err1 := strconv.ParseFloat(strings.TrimSpace(num), 64)
	d, err2 := strconv.ParseFloat(strings.TrimSpace(den), 64)
	if err1 != nil || err2 != nil || d == 0 {
		return 0
	}
	return n / d
}

// Extract 把 src 按固定频率抽帧到 outDir，返回按时间排序的帧文件路径。
//
// 输出命名为 raw_000001.jpg，序号即时间序：ffmpeg 的 image2 复用器按
// 输出顺序递增编号，所以文件名字典序 == 视频时间序。后续算时间戳只需要
// 「序号 + 抽帧频率」，不必另维护一张时间表，也不会因为文件名排序
// 在不同平台上表现不一致而错位。
func Extract(ffmpeg, src, outDir string, opt ExtractOptions) ([]string, error) {
	if opt.Rate <= 0 {
		return nil, fmt.Errorf("video: 抽帧频率必须大于 0，收到 %v", opt.Rate)
	}
	if opt.Quality <= 0 || opt.Quality > 31 {
		opt.Quality = 3
	}
	if err := os.MkdirAll(outDir, 0o755); err != nil {
		return nil, fmt.Errorf("video: 创建抽帧目录失败: %w", err)
	}

	args := []string{"-hide_banner", "-nostdin", "-y"}
	if opt.Stderr == nil {
		args = append(args, "-loglevel", "error")
	} else {
		args = append(args, "-loglevel", "warning", "-stats")
	}
	if opt.Start > 0 {
		args = append(args, "-ss", formatSeconds(opt.Start))
	}
	args = append(args, "-i", src)
	if opt.End > opt.Start {
		args = append(args, "-t", formatSeconds(opt.End-opt.Start))
	}

	vf := "fps=" + strconv.FormatFloat(opt.Rate, 'f', -1, 64)
	if opt.Width > 0 {
		// 高度用 -2 取偶数：多数编码器不接受奇数高度，奇数会被 ffmpeg 报错
		vf += fmt.Sprintf(",scale=%d:-2", opt.Width)
	}
	args = append(args, "-vf", vf, "-q:v", strconv.Itoa(opt.Quality), "-f", "image2")
	if opt.MaxFrames > 0 {
		args = append(args, "-frames:v", strconv.Itoa(opt.MaxFrames))
	}
	args = append(args, filepath.Join(outDir, "raw_%06d.jpg"))

	ctx, cancel := context.WithTimeout(context.Background(), extractTimeout)
	defer cancel()

	cmd := newCommand(ctx, ffmpeg, args...)
	var stderr bytes.Buffer
	if opt.Stderr != nil {
		cmd.Stderr = io.MultiWriter(&stderr, opt.Stderr)
	} else {
		cmd.Stderr = &stderr
	}
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("video: ffmpeg 抽帧失败: %w（%s）", err, tail(stderr.String(), 500))
	}

	files, err := filepath.Glob(filepath.Join(outDir, "raw_*.jpg"))
	if err != nil {
		return nil, fmt.Errorf("video: 枚举抽帧产物失败: %w", err)
	}
	sort.Strings(files)
	if len(files) == 0 {
		return nil, fmt.Errorf("video: ffmpeg 没抽出任何帧（检查时间区间是否超出视频长度）")
	}
	return files, nil
}

// Frames 把抽出的帧文件组装成带时间戳与 dHash 指纹的序列。
//
// 时间戳由「序号 + 抽帧频率」推算，而不是读文件的修改时间：
// 文件时间戳会被复制、解压、网盘同步等操作改掉，序号才是视频时间序的
// 唯一真相。start 是抽帧起点（对应 Extract 的 Start）。
func Frames(files []string, rate float64, start time.Duration) ([]Frame, error) {
	if rate <= 0 {
		return nil, fmt.Errorf("video: 抽帧频率必须大于 0")
	}
	out := make([]Frame, 0, len(files))
	for i, f := range files {
		img, err := openImage(f)
		if err != nil {
			return nil, fmt.Errorf("video: 读取帧 %s 失败: %w", filepath.Base(f), err)
		}
		out = append(out, Frame{
			Index: i + 1,
			File:  f,
			AtMs:  start.Milliseconds() + int64(float64(i)*1000/rate),
			Hash:  dHash(img),
		})
	}
	return out, nil
}

// openImage 解码一帧图片。
func openImage(path string) (image.Image, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	img, _, err := image.Decode(f)
	return img, err
}

// formatSeconds 把时长格式化成 ffmpeg 接受的秒数字符串。
func formatSeconds(d time.Duration) string {
	return strconv.FormatFloat(d.Seconds(), 'f', 3, 64)
}

// tail 截取字符串末尾 n 个字符，避免把 ffmpeg 的整段输出塞进错误信息。
func tail(s string, n int) string {
	s = strings.TrimSpace(s)
	if len(s) <= n {
		return s
	}
	return "..." + s[len(s)-n:]
}
