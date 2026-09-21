// Command ocr 是界面文字识别的正式入口（PP-OCR / RapidOCR 引擎）。
//
// 为什么需要它：读屏文字是游戏辅助里最高频的一类观测——找按钮、读数值、
// 核对库存与价签、确认界面态。此前这些都在 `.workbuddy/tmp` 里现写 python
// 脚本（fs_ocr1.py 之类），既不可复用也不能回归，任务结束就随临时目录清掉；
// AGENTS.md §2 要求「常用能力必须正式化」，本命令即该能力的正式入口，
// 实现见 `internal/ocr`（其他 Go 代码可直接复用同一个常驻引擎）。
//
// 引擎是常驻子进程：进程内多次识别复用同一个 python，冷启动（加载三个
// onnx 模型）只付一次。命令行每次调用仍是独立进程，所以高频批量识别
// 优先用 `-dir` 一次跑完、用 `-serve` 常驻，或在 Go 代码里 `NewEngine` 复用。
//
// 用法：
//
//	ocr -in shot.png                                     # 全图识别
//	ocr -in shot.png -crop 100,690,830,1020              # 只认表单区（坐标已映射回原图）
//	ocr -in shot.png -crop 330,690,590,980 -zoom 2       # 小字先放大再认
//	ocr -in shot.png -find 制作                          # 只列匹配项，直接拿坐标去点
//	ocr -in shot.png -min 0.6 -json                      # 过滤低置信度，输出 JSON
//	ocr -dir .workbuddy/tmp/screenshots -glob "mk*.png"  # 批量
//	ocr -serve                                           # 常驻：stdin 收 JSON 行、stdout 回 JSON 行
//	ocr -check                                           # 只看引擎可用性
//
// 输出默认与旧脚本同格式，便于替换后逐行对照：
//
//	( 788,1385)  探险   [0.67]
package main

import (
	"bufio"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/ayflying/game-sensei/internal/ocr"
)

func main() {
	var inputs multiFlag
	flag.Var(&inputs, "in", "图片路径（PNG/JPEG），可重复指定")
	crop := flag.String("crop", "", "只识别该像素区域 x0,y0,x1,y1（坐标会映射回原图）")
	zoom := flag.Float64("zoom", 1, "识别前放大倍数；小字建议 2~3")
	find := flag.String("find", "", "只输出包含该子串的文字")
	minScore := flag.Float64("min", 0, "过滤掉置信度低于该值的结果")
	asJSON := flag.Bool("json", false, "以 JSON 输出")
	dir := flag.String("dir", "", "批量识别该目录下的图片")
	pattern := flag.String("glob", "*.png", "配合 -dir 的文件通配")
	envPath := flag.String("env", "", "配置文件路径；默认项目根目录 .env")
	python := flag.String("python", "", "python 解释器路径；覆盖 .env 与自动探测")
	check := flag.Bool("check", false, "只检查引擎可用性（可配合 -in 试跑一次）")
	serve := flag.Bool("serve", false, "常驻服务模式：从 stdin 逐行读 JSON 请求、向 stdout 逐行回 JSON（见 runServe）")
	verbose := flag.Bool("v", false, "把诊断信息打到 stderr")
	flag.Parse()

	cfg, err := ocr.LoadConfig(*envPath)
	if err != nil {
		fail(err)
	}
	if *python != "" {
		cfg.Python = *python
	}

	if *check {
		reportEngine(cfg, *verbose)
	}

	if *serve {
		runServe(cfg)
		return
	}

	files, err := collect(inputs, *dir, *pattern)
	if err != nil {
		fail(err)
	}
	if len(files) == 0 {
		if *check {
			return
		}
		fail(fmt.Errorf("没有输入图片：用 -in <图片> 或 -dir <目录>"))
	}

	engine := ocr.NewEngine(cfg)
	defer engine.Close()

	results := make([]fileResult, 0, len(files))
	started := time.Now()
	failed := 0
	for _, path := range files {
		t0 := time.Now()
		texts, err := engine.RecognizeFile(path, *crop, *zoom)
		elapsed := time.Since(t0)
		res := fileResult{File: path, Elapsed: elapsed}
		if err != nil {
			failed++
			res.Err = err.Error()
		} else {
			res.Items = filter(texts, *find, *minScore)
		}
		results = append(results, res)

		if *verbose {
			fmt.Fprintf(os.Stderr, "[%s] 识别 %d 条，耗时 %v\n", filepath.Base(path), len(res.Items), elapsed.Round(time.Millisecond))
		}
		if !*asJSON {
			printResult(res, len(files) > 1)
		}
	}

	if *asJSON {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(results); err != nil {
			fail(err)
		}
	}
	if *verbose {
		fmt.Fprintf(os.Stderr, "[合计] %d 张，用时 %v，子进程启动 %d 次\n",
			len(files), time.Since(started).Round(time.Millisecond), engine.StartCount())
	}
	// 批量时单张失败不影响其余；全部失败才以非零码退出，便于脚本判断。
	if failed == len(files) {
		os.Exit(1)
	}
}

// runServe 是常驻服务模式：一个进程连续处理多帧，模型只加载一次。
//
// 为什么需要它：`internal/ocr` 内部已是「常驻 python 子进程」，但**命令行每次
// 调用仍是一个新进程**，冷启动（import onnxruntime + 加载 det/rec/cls 三个模型）
// 实测约 2.2s。跑批/巡检这类要连续识别几十帧的场景，每帧都付 2.2s 占比过高。
// 同一台机器、同一张 2340x1080 帧实测：
//
//	两次独立调用（各一张）  = 8.28s
//	一次调用两张            = 6.06s   （省掉一次冷启动）
//	常驻模式（两张）        = 约 3.8s  （连首次冷启动整批也只付一次）
//
// 协议与 `internal/ocr` 内部用的 JSON 行协议同构，脚本侧可统一处理：
//
//	请求 {"file":"a.png"}  或 {"file":"a.png","crop":"x0,y0,x1,y1","zoom":2}
//	     {"cmd":"exit"}
//	应答 {"file":"a.png","items":[{"text":..,"score":..,"cx":..,"cy":..,"box":[..]}]}
//	     {"file":"a.png","error":"..."}
//
// 行内用 JSON 而不是自定义分隔：路径含中文/空格时无需额外转义约定，
// Go 与 python 两侧都只用标准库。
func runServe(cfg ocr.Config) {
	engine := ocr.NewEngine(cfg)
	defer engine.Close()

	in := bufio.NewScanner(os.Stdin)
	// 默认 Scanner 单行上限 64KB，放路径够用；放宽到 1MB 是为了将来支持
	// 把图片 base64 直接塞进请求行时不至于撞上限。
	in.Buffer(make([]byte, 0, 1<<16), 1<<20)
	out := bufio.NewWriter(os.Stdout)
	defer out.Flush()
	enc := json.NewEncoder(out)

	for in.Scan() {
		line := strings.TrimSpace(in.Text())
		if line == "" {
			continue
		}
		var req struct {
			Cmd  string  `json:"cmd"`
			File string  `json:"file"`
			Crop string  `json:"crop"`
			Zoom float64 `json:"zoom"`
		}
		if err := json.Unmarshal([]byte(line), &req); err != nil {
			enc.Encode(serveResp{Err: "请求不是合法 JSON: " + err.Error()})
			out.Flush()
			continue
		}
		if req.Cmd == "exit" {
			return
		}
		if req.File == "" {
			enc.Encode(serveResp{Err: "请求缺少 file"})
			out.Flush()
			continue
		}
		zoom := req.Zoom
		if zoom <= 0 {
			zoom = 1
		}
		resp := serveResp{File: req.File}
		if texts, err := engine.RecognizeFile(req.File, req.Crop, zoom); err != nil {
			resp.Err = err.Error()
		} else {
			resp.Items = filter(texts, "", 0)
		}
		enc.Encode(resp)
		// 必须逐条 flush：对面（脚本）正阻塞在这一行上等结果，攒着不写
		// 会让它一直等到超时，表现为「引擎没反应」。
		out.Flush()
	}
	if err := in.Err(); err != nil {
		fmt.Fprintf(os.Stderr, "ocr -serve: 读取 stdin 失败: %v\n", err)
	}
}

// serveResp 是 -serve 模式的一行应答。
type serveResp struct {
	File  string `json:"file"`
	Items []item `json:"items,omitempty"`
	Err   string `json:"error,omitempty"`
}

// fileResult 是一张图的识别结果（JSON 输出与错误汇总共用）。
type fileResult struct {
	File    string        `json:"file"`
	Elapsed time.Duration `json:"-"`
	Items   []item        `json:"items"`
	Err     string        `json:"error,omitempty"`
}

// item 是 JSON 输出里的一条文字：既给外接矩形，也给中心点——
// 「点哪里」用得最多，让调用方自己算中心点迟早会算错一处。
type item struct {
	Text  string  `json:"text"`
	Score float64 `json:"score"`
	CX    int     `json:"cx"`
	CY    int     `json:"cy"`
	Box   [4]int  `json:"box"`
}

func filter(texts []ocr.Text, find string, minScore float64) []item {
	texts = ocr.Filter(texts, find)
	out := make([]item, 0, len(texts))
	for _, t := range texts {
		if t.Score < minScore {
			continue
		}
		out = append(out, item{
			Text:  t.Text,
			Score: t.Score,
			CX:    t.CX(),
			CY:    t.CY(),
			Box:   [4]int{t.Box.X0, t.Box.Y0, t.Box.X1, t.Box.Y1},
		})
	}
	return out
}

// printResult 打印人类可读结果。多图时加文件名抬头，单图不加（保持机器可读）。
func printResult(res fileResult, withHeader bool) {
	if withHeader {
		fmt.Printf("== %s ==\n", res.File)
	}
	if res.Err != "" {
		fmt.Printf("识别失败：%s\n", res.Err)
		return
	}
	if len(res.Items) == 0 {
		fmt.Println("（无匹配文字）")
		return
	}
	for _, it := range res.Items {
		fmt.Printf("(%4d,%4d)  %s   [%.2f]\n", it.CX, it.CY, it.Text, it.Score)
	}
}

// reportEngine 打印引擎可用性诊断：解释器在哪、有没有 RapidOCR。
//
// 这一步存在的意义是「别让引擎在第一次识别时才失败」——排查
// 「OCR 不工作」时，先看清它到底选了哪个 python，比看栈回溯快。
func reportEngine(cfg ocr.Config, verbose bool) {
	py := cfg.Python
	source := ".env " + ocr.EnvPython
	if py == "" {
		py = ocr.DefaultPython()
		source = "自动探测"
	}
	if py == "" {
		fmt.Fprintln(os.Stderr, "ocr: 找不到 python 解释器：在根目录 .env 设 "+ocr.EnvPython+"，或把 python 加入 PATH")
		os.Exit(1)
	}
	fmt.Printf("[解释器] %s（来源：%s）\n", py, source)
	if !ocr.HasRapidOCR(py) {
		fmt.Fprintf(os.Stderr, "ocr: 解释器 %s 里没有 rapidocr_onnxruntime；请换一个装了它的解释器并写进 .env 的 %s\n", py, ocr.EnvPython)
		os.Exit(1)
	}
	fmt.Println("[依赖] rapidocr_onnxruntime 可用")
	if verbose {
		fmt.Fprintf(os.Stderr, "[提示] 引擎为常驻子进程，首次识别含模型加载（约 2s），之后单帧百毫秒级\n")
	}
}

// collect 汇总本次要识别的文件：-in 逐个加，-dir 按通配展开。
func collect(inputs []string, dir, pattern string) ([]string, error) {
	files := append([]string{}, inputs...)
	if dir != "" {
		matches, err := filepath.Glob(filepath.Join(dir, pattern))
		if err != nil {
			return nil, fmt.Errorf("-glob 通配无效: %w", err)
		}
		files = append(files, matches...)
	}
	var out []string
	for _, f := range files {
		if f = strings.TrimSpace(f); f == "" {
			continue
		}
		if st, err := os.Stat(f); err != nil || st.IsDir() {
			return nil, fmt.Errorf("图片不可读: %s", f)
		}
		out = append(out, f)
	}
	return out, nil
}

// multiFlag 让 -in 可以重复出现（一次命令识别多张图，复用同一个常驻引擎）。
type multiFlag []string

func (m *multiFlag) String() string { return strings.Join(*m, ",") }

func (m *multiFlag) Set(v string) error {
	*m = append(*m, v)
	return nil
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "ocr:", err)
	os.Exit(1)
}
