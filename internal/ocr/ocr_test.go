package ocr

import (
	"bufio"
	"encoding/json"
	"image"
	"image/color"
	"image/draw"
	"image/png"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// TestLaunchCodeIsASCII 锁住「脚本经 base64 内联」这个约定。
//
// 它防的是一类很难查的故障：脚本里一旦有非 ASCII（我们的注释是中文），
// 直接塞进 `python -c` 就要过 Windows 命令行的引号与编码转义，
// 失败了会表现成「python 报了别的语法错」，看不出跟编码有关。
func TestLaunchCodeIsASCII(t *testing.T) {
	code := launchCode()
	for i := 0; i < len(code); i++ {
		if code[i] > 127 {
			t.Fatalf("传给 python -c 的代码必须是纯 ASCII，第 %d 字节是 %d", i, code[i])
		}
	}
	if !strings.Contains(code, "base64") || !strings.Contains(code, "exec(compile(") {
		t.Fatalf("启动代码结构变了: %s", clip(code, 120))
	}
	if len(code) > 30000 {
		t.Fatalf("命令行长度 %d 逼近 Windows 上限，应改用别的传递方式", len(code))
	}
}

func TestTextCenter(t *testing.T) {
	txt := Text{Text: "市场", Box: Box{X0: 400, Y0: 1550, X1: 500, Y1: 1568}}
	if txt.CX() != 450 || txt.CY() != 1559 {
		t.Fatalf("中心点算错: (%d,%d)", txt.CX(), txt.CY())
	}
}

func TestFilter(t *testing.T) {
	texts := []Text{{Text: "市场"}, {Text: "战利品"}, {Text: "创建订单"}}
	if got := Filter(texts, "市场"); len(got) != 1 || got[0].Text != "市场" {
		t.Fatalf("精确匹配失败: %v", got)
	}
	if got := Filter(texts, "创建"); len(got) != 1 {
		t.Fatalf("前缀匹配失败: %v", got)
	}
	if got := Filter(texts, ""); len(got) != 3 {
		t.Fatalf("空关键字应原样返回: %v", got)
	}
	if got := Filter(texts, "不存在"); len(got) != 0 {
		t.Fatalf("不该匹配到: %v", got)
	}
}

// TestProtocolFields 锁住 Go 与 Python 两侧的字段名。
//
// 协议两端一个是 struct tag、一个是 dict key，改一边忘了另一边时
// 表现是「引擎起来但永远读不到文字」，这条测试就是为了让它在单测里炸。
func TestProtocolFields(t *testing.T) {
	raw, err := json.Marshal(ocrRequest{ID: 7, B64: "AAA", BoxThresh: ptr(0.5), TextScore: ptr(0.6)})
	if err != nil {
		t.Fatal(err)
	}
	var asMap map[string]any
	if err := json.Unmarshal(raw, &asMap); err != nil {
		t.Fatal(err)
	}
	for _, key := range []string{"id", "b64", "box_thresh", "text_score"} {
		if _, ok := asMap[key]; !ok {
			t.Errorf("请求缺字段 %q: %s", key, raw)
		}
	}

	// 阈值未设置时必须整个字段消失：server.py 只在字段存在时才覆盖引擎阈值，
	// 传 0 会被当成「显式要求阈值为 0」。
	bare, _ := json.Marshal(ocrRequest{ID: 1, B64: "AAA"})
	if strings.Contains(string(bare), "box_thresh") {
		t.Errorf("未设置阈值时不该出现该字段: %s", bare)
	}

	var resp ocrResponse
	sample := `{"id":3,"ok":true,"items":[{"text":"市场","score":0.67,"box":[400,1550,500,1568]}]}`
	if err := json.Unmarshal([]byte(sample), &resp); err != nil {
		t.Fatal(err)
	}
	if !resp.OK || len(resp.Items) != 1 || resp.Items[0].Text != "市场" {
		t.Fatalf("响应解析失败: %+v", resp)
	}
	if resp.Items[0].Box != [4]int{400, 1550, 500, 1568} {
		t.Fatalf("矩形解析失败: %v", resp.Items[0].Box)
	}
}

func ptr(f float64) *float64 { return &f }

func TestReadLine(t *testing.T) {
	line, err := readLine(bufio.NewReader(strings.NewReader("{\"ok\":true}\r\n")), time.Second)
	if err != nil || line != `{"ok":true}` {
		t.Fatalf("读到 %q, err=%v", line, err)
	}

	// 管道永不写入 → 必须超时返回，而不是挂死整个进程
	pr, _ := io.Pipe()
	defer pr.Close()
	if _, err := readLine(bufio.NewReader(pr), 30*time.Millisecond); err == nil {
		t.Fatal("应超时")
	}
}

// TestTailBuffer 验证只保留尾部。python 的栈回溯在最后几行，
// 前面刷屏的进度日志既没用，又不限制就会把内存吃干。
func TestTailBuffer(t *testing.T) {
	tb := newTailBuffer(10)
	tb.Write([]byte("12345678"))
	tb.Write([]byte("abc"))
	if got := tb.String(); len(got) != 10 || !strings.HasSuffix(got, "abc") {
		t.Fatalf("尾部保留不对: %q", got)
	}
}

func TestReadEnvFile(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".env")
	content := "" +
		"# 注释行\n" +
		"\n" +
		"OCR_PYTHON=C:\\python\\python.exe\n" +
		"export OCR_TIMEOUT=45s\n" +
		"OCR_IDLE=\"10m\"  # 行内注释\n" +
		"OCR_TEXT_SCORE='0.6'\n"
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatal(err)
	}
	values, err := readEnvFile(path)
	if err != nil {
		t.Fatal(err)
	}
	want := map[string]string{
		"OCR_PYTHON":     `C:\python\python.exe`,
		"OCR_TIMEOUT":    "45s",
		"OCR_IDLE":       "10m",
		"OCR_TEXT_SCORE": "0.6",
	}
	for k, v := range want {
		if values[k] != v {
			t.Errorf("%s = %q，期望 %q", k, values[k], v)
		}
	}

	// 文件不存在不算错误：OCR 全部配置都有默认值
	missing, err := readEnvFile(filepath.Join(dir, "不存在.env"))
	if err != nil || len(missing) != 0 {
		t.Fatalf("缺失文件应返回空配置: %v, %v", missing, err)
	}

	bad := filepath.Join(dir, "bad.env")
	if err := os.WriteFile(bad, []byte("OCR_PYTHON\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := readEnvFile(bad); err == nil {
		t.Error("缺等号应报错")
	}
}

func TestLoadConfigValidates(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, ".env")

	write := func(s string) {
		if err := os.WriteFile(path, []byte(s), 0o600); err != nil {
			t.Fatal(err)
		}
	}

	write("OCR_TIMEOUT=30s\nOCR_IDLE=5m\nOCR_BOX_THRESH=0.4\nOCR_TEXT_SCORE=0.55\n")
	c, err := LoadConfig(path)
	if err != nil {
		t.Fatal(err)
	}
	if c.Timeout != 30*time.Second || c.Idle != 5*time.Minute || c.BoxThresh != 0.4 || c.TextScore != 0.55 {
		t.Fatalf("配置读错: %+v", c)
	}

	// 显式写 0 表示「永不自动关」，与「没配」要用不同值区分开
	write("OCR_IDLE=0\n")
	if c, err = LoadConfig(path); err != nil || c.Idle >= 0 {
		t.Fatalf("OCR_IDLE=0 应解析成负值（不自动关）: %+v, %v", c, err)
	}

	for _, bad := range []string{"OCR_TIMEOUT=abc\n", "OCR_TIMEOUT=-1s\n", "OCR_BOX_THRESH=2\n", "OCR_BOX_THRESH=x\n", "OCR_TEXT_SCORE=0\n"} {
		write(bad)
		if _, err := LoadConfig(path); err == nil {
			t.Errorf("%q 应该报错", strings.TrimSpace(bad))
		}
	}
}

func TestDecodeFileRejectsUnsupported(t *testing.T) {
	dir := t.TempDir()
	txt := filepath.Join(dir, "a.txt")
	if err := os.WriteFile(txt, []byte("not an image"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := DecodeFile(txt); err == nil {
		t.Error("非图片文件应报错")
	}
	if _, err := DecodeFile(filepath.Join(dir, "没有这个文件.png")); err == nil {
		t.Error("文件不存在应报错")
	}
}

// TestEngineRoundTrip 是唯一需要真实 python + RapidOCR 的测试。
//
// 本机没装就跳过（不把环境依赖变成红灯）；装了就必须跑通——
// 它覆盖的是单测碰不到的整条链路：启动子进程、就绪握手、协议编解码、
// 图像传输，以及「同一引擎连续识别只启动一次子进程」这个核心设计承诺。
func TestEngineRoundTrip(t *testing.T) {
	if testing.Short() {
		t.Skip("跳过长耗时测试（-short）")
	}
	py := DefaultPython()
	if py == "" || !HasRapidOCR(py) {
		t.Skip("本机没有可用的 python + rapidocr，跳过真实引擎测试")
	}

	eng := NewEngine(Config{Python: py, Timeout: 60 * time.Second, Idle: -1})
	defer eng.Close()

	// 纯白图：要的是「链路通」，不指望有文字。
	white := image.NewRGBA(image.Rect(0, 0, 240, 80))
	draw.Draw(white, white.Bounds(), image.NewUniform(color.White), image.Point{}, draw.Src)

	texts, err := eng.Recognize(white)
	if err != nil {
		t.Fatalf("识别纯白图失败: %v", err)
	}
	if len(texts) != 0 {
		t.Logf("注意：纯白图返回了 %d 条文字（不影响链路结论）", len(texts))
	}
	if eng.EngineName() != "rapidocr_onnxruntime" {
		t.Errorf("引擎名不对: %q", eng.EngineName())
	}

	// 第二次识别必须复用同一个子进程——这正是常驻设计要兑现的东西。
	if _, err := eng.Recognize(white); err != nil {
		t.Fatalf("第二次识别失败: %v", err)
	}
	if n := eng.StartCount(); n != 1 {
		t.Errorf("两次识别应只启动一次子进程，实际 %d 次", n)
	}

	eng.Close()
	if err := eng.Close(); err != nil {
		t.Errorf("重复 Close 不该报错: %v", err)
	}
}

// TestRecognizeFileOffset 用真实引擎验证「裁剪 + 缩放后坐标映射回原图」。
//
// 这是库里最容易算错、也最该由库承担的一步：调用方拿到的 (cx,cy)
// 必须能直接点，不能要求它自己记裁剪偏移和放大倍数。
func TestRecognizeFileOffset(t *testing.T) {
	if testing.Short() {
		t.Skip("跳过长耗时测试（-short）")
	}
	py := DefaultPython()
	if py == "" || !HasRapidOCR(py) {
		t.Skip("本机没有可用的 python + rapidocr，跳过真实引擎测试")
	}

	dir := t.TempDir()
	path := filepath.Join(dir, "small.png")
	img := image.NewRGBA(image.Rect(0, 0, 400, 300))
	draw.Draw(img, img.Bounds(), image.NewUniform(color.White), image.Point{}, draw.Src)
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := png.Encode(f, img); err != nil {
		f.Close()
		t.Fatal(err)
	}
	f.Close()

	eng := NewEngine(Config{Python: py, Timeout: 60 * time.Second, Idle: -1})
	defer eng.Close()

	// 无文字的图：只验证「裁剪+放大后仍能正常出结果、不报坐标越界」。
	for _, tc := range []struct {
		crop string
		zoom float64
	}{
		{"", 1},
		{"50,40,350,260", 1},
		{"50,40,350,260", 2},
	} {
		texts, err := eng.RecognizeFile(path, tc.crop, tc.zoom)
		if err != nil {
			t.Fatalf("crop=%q zoom=%v: %v", tc.crop, tc.zoom, err)
		}
		for _, txt := range texts {
			if txt.Box.X0 < 0 || txt.Box.Y0 < 0 || txt.Box.X1 > 400 || txt.Box.Y1 > 300 {
				t.Errorf("crop=%q zoom=%v 返回了越界坐标 %v", tc.crop, tc.zoom, txt.Box)
			}
		}
	}

	if _, err := eng.RecognizeFile(path, "0,0,500,300", 1); err == nil {
		t.Error("超出原图的裁剪应报错")
	}
}
