// cmd/video 把教学视频拆成「关键帧 + 动作段」，可选交给老师逐段判读。
//
// 这是离线工具，不参与实时回路：
//
//	# 只抽帧切段（快，不烧算力）
//	go run ./cmd/video -in 教学视频.mp4 -out .workbuddy/videos/p01
//
//	# 抽帧 + 老师判读（需要本地 Ollama 教师在跑）
//	go run ./cmd/video -in 教学视频.mp4 -out .workbuddy/videos/p01 \
//	  -annotate -game nrc -goal "学会移动与捕捉精灵"
//
// 产出目录：
//
//	<out>/meta.json      源信息、参数、统计、关键帧清单
//	<out>/clips.jsonl    每个动作段一行（含老师判读结果）
//	<out>/frames/        去重后的关键帧，保留完整时序
//	<out>/report.md      人读汇总
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/ayflying/game-sensei/internal/game"
	"github.com/ayflying/game-sensei/internal/teacher"
	"github.com/ayflying/game-sensei/internal/video"
)

func main() {
	var (
		in      = flag.String("in", "", "输入视频路径（必填）")
		out     = flag.String("out", "", "输出目录（默认 .workbuddy/videos/<时间戳>）")
		rate    = flag.Float64("rate", 2, "抽帧频率（帧/秒）")
		width   = flag.Int("width", 640, "抽帧宽度，等比缩放；0 = 保持原始分辨率")
		quality = flag.Int("q", 3, "JPEG 质量（2~31，越小越好）")

		// 下面三个阈值的默认值来自《洛克王国：世界》38 分钟录屏的实测标定。
		// 游戏画面是持续运动的（草地、云、UI 呼吸动画、镜头跟随），相邻帧的
		// dHash 距离天然就有 8~20，且距离分布是连续单峰、**没有天然分界点**。
		// 这意味着纯视觉手段切不出「玩家在做什么」的语义边界，只能按
		// 「一屏操作的天然时长约 15 秒」来标定粒度。
		// 默认值只对运动画面成立；静止画面为主的演示类视频应显著调低。
		dedup     = flag.Int("dedup", 10, "去重阈值（dHash 汉明距离）；负数 = 不去重")
		segThresh = flag.Int("seg", 26, "切段阈值（dHash 汉明距离，应大于去重阈值）")
		minSeg    = flag.Int("min-seg", 8, "段最小帧数（用于合并碎片）")

		maxFrames = flag.Int("max-frames", 0, "抽帧上限，0 = 不限")
		startAt   = flag.Duration("start", 0, "起始时间，如 90s / 1m30s")
		endAt     = flag.Duration("end", 0, "结束时间，0 = 到结尾")
		keepRaw   = flag.Bool("keep-raw", false, "保留未去重的原始帧")
		ffmpegIn  = flag.String("ffmpeg", "", "ffmpeg 路径（空则自动查找）")
		ffprobeIn = flag.String("ffprobe", "", "ffprobe 路径（空则自动查找）")
		quiet     = flag.Bool("quiet", false, "不显示 ffmpeg 抽帧进度")

		annotate     = flag.Bool("annotate", false, "抽帧后交给老师 VLM 逐段判读")
		teacherURL   = flag.String("teacher-url", "http://127.0.0.1:11435", "老师地址（Ollama）")
		teacherModel = flag.String("teacher-model", "qwen3.5:9b", "老师模型")
		annLimit     = flag.Int("annotate-limit", 0, "最多判读多少段（0=全部），用于先试水")
		gameSpec     = flag.String("game", "", "游戏档案名或路径，提供按钮清单与界面先验")
		goal         = flag.String("goal", "", "游戏目标，写进判读提示词")
		think        = flag.Bool("think", false, "让老师开启思考链（默认关：实测开着会打满预算还拿不到正文）")
	)
	flag.Parse()

	if err := run(runConfig{
		in: *in, out: *out, rate: *rate, width: *width, quality: *quality,
		dedup: *dedup, segThresh: *segThresh, minSeg: *minSeg, maxFrames: *maxFrames,
		start: *startAt, end: *endAt, keepRaw: *keepRaw,
		ffmpeg: *ffmpegIn, ffprobe: *ffprobeIn, quiet: *quiet,
		annotate: *annotate, teacherURL: *teacherURL, teacherModel: *teacherModel,
		annLimit: *annLimit, game: *gameSpec, goal: *goal, think: *think,
	}); err != nil {
		fmt.Fprintf(os.Stderr, "\n错误：%v\n", err)
		os.Exit(1)
	}
}

type runConfig struct {
	in           string
	out          string
	rate         float64
	width        int
	quality      int
	dedup        int
	segThresh    int
	minSeg       int
	maxFrames    int
	start        time.Duration
	end          time.Duration
	keepRaw      bool
	ffmpeg       string
	ffprobe      string
	quiet        bool
	annotate     bool
	teacherURL   string
	teacherModel string
	annLimit     int
	game         string
	goal         string
	think        bool
}

func run(cfg runConfig) error {
	if strings.TrimSpace(cfg.in) == "" {
		return fmt.Errorf("必须用 -in 指定输入视频")
	}
	if _, err := os.Stat(cfg.in); err != nil {
		return fmt.Errorf("读不到输入视频 %s: %w", cfg.in, err)
	}
	if cfg.rate <= 0 {
		return fmt.Errorf("-rate 必须大于 0，收到 %v", cfg.rate)
	}

	ffmpeg, ffprobe, err := locateTools(cfg)
	if err != nil {
		return err
	}

	outDir := cfg.out
	if outDir == "" {
		outDir = filepath.Join(".workbuddy", "videos", time.Now().Format("20060102-150405"))
	}

	startedAt := time.Now()

	info, err := video.Probe(ffprobe, cfg.in)
	if err != nil {
		return err
	}
	fmt.Printf("源视频  %s\n", filepath.Base(cfg.in))
	fmt.Printf("        %dx%d @ %.2ffps  %s  时长 %s  体积 %.1f MB\n",
		info.Width, info.Height, info.FPS, info.Codec,
		hhmmss(info.DurationMs), float64(info.SizeBytes)/(1<<20))

	rawDir := filepath.Join(outDir, "raw")
	var progress io.Writer
	if !cfg.quiet {
		progress = os.Stderr
	}
	fmt.Printf("抽帧    %.2f fps，宽 %d…\n", cfg.rate, cfg.width)
	rawFiles, err := video.Extract(ffmpeg, cfg.in, rawDir, video.ExtractOptions{
		Rate:      cfg.rate,
		Width:     cfg.width,
		Quality:   cfg.quality,
		MaxFrames: cfg.maxFrames,
		Start:     cfg.start,
		End:       cfg.end,
		Stderr:    progress,
	})
	if err != nil {
		return err
	}

	fmt.Printf("指纹    计算 %d 帧的 dHash…\n", len(rawFiles))
	rawFrames, err := video.Frames(rawFiles, cfg.rate, cfg.start)
	if err != nil {
		return err
	}

	keyFrames := video.Renumber(video.Collapse(rawFrames, cfg.dedup))
	framesDir := filepath.Join(outDir, "frames")
	if err := os.MkdirAll(framesDir, 0o755); err != nil {
		return fmt.Errorf("创建 frames 目录失败: %w", err)
	}
	for i := range keyFrames {
		name := fmt.Sprintf("k_%06d.jpg", keyFrames[i].Index)
		if err := moveFile(keyFrames[i].File, filepath.Join(framesDir, name)); err != nil {
			return fmt.Errorf("归档关键帧失败: %w", err)
		}
		keyFrames[i].File = filepath.ToSlash(filepath.Join("frames", name))
	}
	if !cfg.keepRaw {
		if err := os.RemoveAll(rawDir); err != nil {
			return fmt.Errorf("清理原始帧失败: %w", err)
		}
	}

	segs := video.SegmentFrames(keyFrames, cfg.segThresh, cfg.minSeg)

	dedupRatio := 0.0
	if len(rawFrames) > 0 {
		dedupRatio = 1 - float64(len(keyFrames))/float64(len(rawFrames))
	}

	// 组装段清单（含送审帧路径与其相对段首的时间偏移）
	clips := make([]clip, 0, len(segs))
	for _, s := range segs {
		c := clip{
			Index:   s.Index,
			StartMs: s.StartMs,
			EndMs:   s.EndMs,
			Start:   hhmmss(s.StartMs),
			End:     hhmmss(s.EndMs),
			DurMs:   s.EndMs - s.StartMs,
			KeyFrom: keyFrames[s.From].Index,
			KeyTo:   keyFrames[s.To].Index,
			Motion:  s.Motion,
		}
		for _, idx := range s.Reps(repsPerClip) {
			c.Reps = append(c.Reps, keyFrames[idx].File)
			c.FrameOffsets = append(c.FrameOffsets,
				float64(keyFrames[idx].AtMs-s.StartMs)/1000)
		}
		clips = append(clips, c)
	}

	// 老师判读（可选）
	var gameName string
	if cfg.annotate {
		gameName, err = annotateClips(cfg, clips, outDir)
		if err != nil {
			return err
		}
	}

	finishedAt := time.Now()
	ann := summarizeAnnotations(clips)

	meta := metaFile{
		Source:       info.Path,
		Video:        info,
		OutDir:       outDir,
		Rate:         cfg.rate,
		Width:        cfg.width,
		Quality:      cfg.quality,
		StartMs:      cfg.start.Milliseconds(),
		EndMs:        cfg.end.Milliseconds(),
		DedupThresh:  cfg.dedup,
		SegThresh:    cfg.segThresh,
		MinSeg:       cfg.minSeg,
		RawFrames:    len(rawFrames),
		KeyFrames:    len(keyFrames),
		Segments:     len(segs),
		DedupRatio:   dedupRatio,
		KeepRaw:      cfg.keepRaw,
		ElapsedMs:    finishedAt.Sub(startedAt).Milliseconds(),
		StartedAt:    startedAt,
		FinishedAt:   finishedAt,
		Frames:       keyFrames,
		Game:         gameName,
		Goal:         cfg.goal,
		TeacherURL:   cfg.teacherURL,
		TeacherModel: cfg.teacherModel,
		Annotated:    ann.Total,
		AnnOK:        ann.OK,
		AnnAvgMs:     ann.AvgMs,
	}
	if err := writeJSONL(filepath.Join(outDir, "clips.jsonl"), clips); err != nil {
		return err
	}
	if err := writeJSON(filepath.Join(outDir, "meta.json"), meta); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(outDir, "report.md"),
		[]byte(renderReport(info, meta, clips)), 0o644); err != nil {
		return err
	}

	fmt.Printf("\n完成    %s\n", outDir)
	fmt.Printf("        原始帧 %d  →  关键帧 %d（去重 %.1f%%）\n",
		len(rawFrames), len(keyFrames), dedupRatio*100)
	fmt.Printf("        动作段 %d 个，平均 %.1f 秒/段，耗时 %.1fs\n",
		len(segs), avgSegSeconds(clips), finishedAt.Sub(startedAt).Seconds())
	if ann.Total > 0 {
		fmt.Printf("        判读 %d 段，成功 %d 段，平均 %.1fs/段\n",
			ann.Total, ann.OK, ann.AvgMs/1000)
	}
	return nil
}

// repsPerClip 是每个段送审老师的代表帧数。
//
// 3 张（首/中/尾）是实测下来的平衡点：1 张看不出动作过程，
// 5 张以上对 9B 级 VLM 反而是噪声——它会把注意力花在比较帧间细节上，
// 而不是判断「玩家在干什么」。
const repsPerClip = 3

type clip struct {
	Index   int      `json:"index"`
	StartMs int64    `json:"start_ms"`
	EndMs   int64    `json:"end_ms"`
	Start   string   `json:"start_at"`
	End     string   `json:"end_at"`
	DurMs   int64    `json:"dur_ms"`
	KeyFrom int      `json:"key_from"`
	KeyTo   int      `json:"key_to"`
	Motion  int      `json:"motion"`
	Reps    []string `json:"reps"`
	// FrameOffsets 是送审帧相对**段首**的秒数，与 Reps 一一对应。
	FrameOffsets []float64 `json:"frame_offsets_s,omitempty"`

	// 以下是老师判读结果（-annotate 才有）
	Observation string  `json:"observation,omitempty"`
	Action      string  `json:"action,omitempty"`
	Strategy    string  `json:"strategy,omitempty"`
	WeakLabel   bool    `json:"weak_label,omitempty"`
	Model       string  `json:"model,omitempty"`
	LatencyMs   float64 `json:"latency_ms,omitempty"`
	Tokens      int     `json:"tokens,omitempty"`
	Raw         string  `json:"annotation_raw,omitempty"`
	Error       string  `json:"error,omitempty"`
}

type metaFile struct {
	Source       string        `json:"source"`
	Video        video.Info    `json:"video"`
	OutDir       string        `json:"out_dir"`
	Rate         float64       `json:"rate"`
	Width        int           `json:"width"`
	Quality      int           `json:"quality"`
	StartMs      int64         `json:"start_ms"`
	EndMs        int64         `json:"end_ms"`
	DedupThresh  int           `json:"dedup_threshold"`
	SegThresh    int           `json:"seg_threshold"`
	MinSeg       int           `json:"min_seg_frames"`
	RawFrames    int           `json:"raw_frames"`
	KeyFrames    int           `json:"key_frames"`
	Segments     int           `json:"segments"`
	DedupRatio   float64       `json:"dedup_ratio"`
	KeepRaw      bool          `json:"keep_raw"`
	ElapsedMs    int64         `json:"elapsed_ms"`
	StartedAt    time.Time     `json:"started_at"`
	FinishedAt   time.Time     `json:"finished_at"`
	Frames       []video.Frame `json:"key_frame_list"`
	Game         string        `json:"game,omitempty"`
	Goal         string        `json:"goal,omitempty"`
	TeacherURL   string        `json:"teacher_url,omitempty"`
	TeacherModel string        `json:"teacher_model,omitempty"`
	Annotated    int           `json:"annotated"`
	AnnOK        int           `json:"annotated_ok"`
	AnnAvgMs     float64       `json:"annotate_avg_ms,omitempty"`
}

// annSummary 是判读阶段的汇总统计。
type annSummary struct {
	Total int
	OK    int
	AvgMs float64
}

// annotateClips 逐段调用老师判读，结果就地填进 clips，返回游戏名。
//
// 串行而不是并发：Ollama 默认单并发（OLLAMA_NUM_PARALLEL=1），
// 并发请求只会在服务端排队，还会让显存峰值更难控。
func annotateClips(cfg runConfig, clips []clip, outDir string) (string, error) {
	var gameName string
	var buttons, hints []string
	if cfg.game != "" {
		prof, err := game.Load(cfg.game)
		if err != nil {
			return "", err
		}
		gameName = prof.Name
		for _, b := range prof.Buttons {
			label := b.Name
			if b.Note != "" {
				label += "(" + b.Note + ")"
			}
			buttons = append(buttons, label)
		}
		hints = append(hints, prof.Hints...)
	} else {
		fmt.Println("提示    未指定 -game，判读时拿不到按钮名，产出的策略可能无法直接执行")
	}

	opts := teacher.DefaultOptions()
	if !cfg.think {
		// 关思考是判读能跑起来的前提：实测开着思考单次 15~74s，
		// 而且约三分之一会打满预算、正文为空（README §9.1）。
		opts.Think = teacher.NoThink()
		opts.MaxTokens = 600
		opts.Timeout = 120 * time.Second
	}
	client := teacher.NewClient(cfg.teacherURL, cfg.teacherModel, opts)

	limit := len(clips)
	if cfg.annLimit > 0 && cfg.annLimit < limit {
		limit = cfg.annLimit
	}

	ctx, cancel := context.WithTimeout(context.Background(), annTimeout(limit))
	defer cancel()

	ver, err := client.Ping(ctx)
	if err != nil {
		return "", fmt.Errorf("老师不可达（%s）：%w\n先启动项目自带实例：bash tools/serve_ollama.sh", cfg.teacherURL, err)
	}
	fmt.Printf("老师    %s @ %s（Ollama %s）\n", cfg.teacherModel, cfg.teacherURL, ver)
	fmt.Printf("判读    %s\n", progressSummary(len(clips), limit))

	for i := 0; i < limit; i++ {
		c := &clips[i]
		spec := video.ClipSpec{
			Index:   c.Index,
			DurSec:  float64(c.DurMs) / 1000,
			Game:    gameName,
			Goal:    cfg.goal,
			Buttons: buttons,
			Hints:   hints,
		}
		for j, rel := range c.Reps {
			off := 0.0
			if j < len(c.FrameOffsets) {
				off = c.FrameOffsets[j]
			}
			spec.Frames = append(spec.Frames, video.FrameRef{
				Path:      filepath.Join(outDir, filepath.FromSlash(rel)),
				OffsetSec: off,
			})
		}

		a := video.Annotate(ctx, client, spec)
		c.Observation = a.Observation
		c.Action = a.Action
		c.Strategy = a.Strategy
		c.Model = a.Model
		c.LatencyMs = a.LatencyMs
		c.Tokens = a.Tokens
		c.Raw = a.Raw
		c.Error = a.Error
		// 操作字段是从画面推断出来的**弱标签**，标记出来防止被当成真值用
		c.WeakLabel = a.Action != ""

		status := "OK"
		detail := clipText(a.Strategy, 36)
		if a.Strategy == "" {
			detail = clipText(a.Observation, 36)
		}
		if a.Error != "" {
			status = "失败"
			detail = clipText(a.Error, 60)
		}
		fmt.Printf("  [%3d/%3d] %s-%s %s %5.1fs  %s\n",
			i+1, limit, c.Start, c.End, status, a.LatencyMs/1000, detail)
	}
	return gameName, nil
}

// annTimeout 按段数估算判读的整体超时预算。
//
// 给得宽松但有限：它只是兜住「老师卡死不返回」这种情况，
// 正常跑完远用不到这个上限（单段实测约 2s，120 段约 4 分钟）。
func annTimeout(n int) time.Duration {
	d := time.Duration(n) * 90 * time.Second
	if d < 10*time.Minute {
		d = 10 * time.Minute
	}
	return d
}

func progressSummary(total, limit int) string {
	if limit < total {
		return fmt.Sprintf("%d 段中的前 %d 段（-annotate-limit 限制）", total, limit)
	}
	return fmt.Sprintf("%d 段", total)
}

// summarizeAnnotations 统计判读结果。
func summarizeAnnotations(clips []clip) annSummary {
	var s annSummary
	var totalMs float64
	for _, c := range clips {
		if c.Model == "" && c.Error == "" {
			continue // 这段没判读过
		}
		s.Total++
		if c.Error == "" && (c.Observation != "" || c.Strategy != "") {
			s.OK++
		}
		totalMs += c.LatencyMs
	}
	if s.Total > 0 {
		s.AvgMs = totalMs / float64(s.Total)
	}
	return s
}

// locateTools 定位 ffmpeg / ffprobe，允许命令行显式覆盖。
func locateTools(cfg runConfig) (ffmpeg, ffprobe string, err error) {
	ffmpeg = cfg.ffmpeg
	if ffmpeg == "" {
		if ffmpeg, err = video.FindTool("ffmpeg"); err != nil {
			return "", "", err
		}
	}
	ffprobe = cfg.ffprobe
	if ffprobe == "" {
		if ffprobe, err = video.FindTool("ffprobe"); err != nil {
			return "", "", err
		}
	}
	return ffmpeg, ffprobe, nil
}

// moveFile 移动文件，跨卷时退化为复制 + 删除。
func moveFile(src, dst string) error {
	if err := os.Rename(src, dst); err == nil {
		return nil
	}
	in, err := os.Open(src)
	if err != nil {
		return err
	}
	defer in.Close()

	out, err := os.Create(dst)
	if err != nil {
		return err
	}
	if _, err := io.Copy(out, in); err != nil {
		out.Close()
		return err
	}
	if err := out.Close(); err != nil {
		return err
	}
	return os.Remove(src)
}

func writeJSON(path string, v any) error {
	raw, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return fmt.Errorf("序列化 %s 失败: %w", filepath.Base(path), err)
	}
	return os.WriteFile(path, raw, 0o644)
}

func writeJSONL(path string, rows []clip) error {
	var b strings.Builder
	enc := json.NewEncoder(&b)
	for _, r := range rows {
		if err := enc.Encode(r); err != nil {
			return fmt.Errorf("序列化 %s 失败: %w", filepath.Base(path), err)
		}
	}
	return os.WriteFile(path, []byte(b.String()), 0o644)
}

// hhmmss 把毫秒格式化成 HH:MM:SS，供报告与清单阅读。
func hhmmss(ms int64) string {
	d := time.Duration(ms) * time.Millisecond
	h := int(d.Hours())
	m := int(d.Minutes()) % 60
	s := int(d.Seconds()) % 60
	return fmt.Sprintf("%02d:%02d:%02d", h, m, s)
}

func avgSegSeconds(clips []clip) float64 {
	if len(clips) == 0 {
		return 0
	}
	var total int64
	for _, c := range clips {
		total += c.DurMs
	}
	return float64(total) / float64(len(clips)) / 1000
}

// clipText 按字符数截断文本。按 rune 切而不是按字节，
// 否则中文会被切出半个字，终端上显示成乱码。
func clipText(s string, n int) string {
	r := []rune(strings.TrimSpace(s))
	if len(r) <= n {
		return string(r)
	}
	return string(r[:n]) + "…"
}

// renderReport 生成人读汇总。
func renderReport(info video.Info, meta metaFile, clips []clip) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# 教学视频抽帧报告\n\n")
	fmt.Fprintf(&b, "- 源视频：`%s`\n", info.Path)
	fmt.Fprintf(&b, "- 分辨率：%dx%d @ %.2f fps（%s）\n", info.Width, info.Height, info.FPS, info.Codec)
	fmt.Fprintf(&b, "- 时长：%s，体积 %.1f MB\n", hhmmss(info.DurationMs), float64(info.SizeBytes)/(1<<20))
	fmt.Fprintf(&b, "- 抽帧：%.2f fps，宽 %d，JPEG q%d\n", meta.Rate, meta.Width, meta.Quality)
	fmt.Fprintf(&b, "- 去重：阈值 %d，原始帧 %d → 关键帧 %d（去重 %.1f%%）\n",
		meta.DedupThresh, meta.RawFrames, meta.KeyFrames, meta.DedupRatio*100)
	fmt.Fprintf(&b, "- 切段：阈值 %d，最小 %d 帧 → %d 个动作段，平均 %.1f 秒/段\n",
		meta.SegThresh, meta.MinSeg, meta.Segments, avgSegSeconds(clips))
	if meta.Annotated > 0 {
		fmt.Fprintf(&b, "- 判读：%s @ %s，%d 段（成功 %d），平均 %.1fs/段\n",
			meta.TeacherModel, meta.TeacherURL, meta.Annotated, meta.AnnOK, meta.AnnAvgMs/1000)
	}
	b.WriteString("\n")

	if meta.Annotated > 0 {
		renderAnnotationSection(&b, clips)
	}

	fmt.Fprintf(&b, "## 动作段清单\n\n")
	fmt.Fprintf(&b, "| # | 时间范围 | 时长 | 关键帧 | 变化量 | 代表帧 |\n")
	fmt.Fprintf(&b, "|---|---|---|---|---|---|\n")
	for _, c := range clips {
		names := make([]string, 0, len(c.Reps))
		for _, r := range c.Reps {
			names = append(names, filepath.Base(r))
		}
		fmt.Fprintf(&b, "| %d | %s - %s | %.0fs | %d-%d | %d | %s |\n",
			c.Index+1, c.Start, c.End, float64(c.DurMs)/1000, c.KeyFrom, c.KeyTo, c.Motion,
			strings.Join(names, " "))
	}
	return b.String()
}

// renderAnnotationSection 输出老师判读正文。
//
// 明确标注「操作」是弱标签：它是模型从画面**推断**的，不是录像里
// 真实记录的操作。不标清楚，这些字段很容易被下游当成训练真值。
func renderAnnotationSection(b *strings.Builder, clips []clip) {
	b.WriteString("## 老师判读\n\n")
	b.WriteString("> 「操作」一栏是从画面**推断**出来的弱标签，不是录像中真实记录的操作，\n")
	b.WriteString("> 不可作为训练真值使用；「局面」「策略」是本阶段的主要产出。\n\n")

	for _, c := range clips {
		if c.Error == "" && c.Observation == "" && c.Strategy == "" && c.Action == "" {
			continue
		}
		fmt.Fprintf(b, "### %d. %s - %s（%.0f 秒）\n\n", c.Index+1, c.Start, c.End, float64(c.DurMs)/1000)
		if c.Error != "" {
			fmt.Fprintf(b, "- 判读失败：%s\n\n", c.Error)
			continue
		}
		if c.Observation != "" {
			fmt.Fprintf(b, "- **局面**：%s\n", c.Observation)
		}
		if c.Action != "" {
			fmt.Fprintf(b, "- **操作**（弱标签）：%s\n", c.Action)
		}
		if c.Strategy != "" {
			fmt.Fprintf(b, "- **策略**：%s\n", c.Strategy)
		}
		b.WriteString("\n")
	}
}
