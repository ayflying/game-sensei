package imagejudge

import (
	"bytes"
	"context"
	"encoding/json"
	"image"
	"image/color"
	"image/png"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestFallback(t *testing.T) {
	var calls []string
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req map[string]any
		json.NewDecoder(r.Body).Decode(&req)
		m := req["model"].(string)
		calls = append(calls, m)
		switch m {
		case "bad":
			w.WriteHeader(500)
		case "empty":
			w.Write([]byte(`{"choices":[{"message":{"content":"","reasoning_content":"不算答案"}}]}`))
		case "cut":
			w.Write([]byte(`{"choices":[{"finish_reason":"length","message":{"content":"截断"}}]}`))
		case "local":
			if req["think"] != false || r.Header.Get("Authorization") != "" {
				t.Error("本地请求字段错误")
			}
			w.Write([]byte(`{"done":true,"done_reason":"stop","message":{"content":"左红右蓝"}}`))
		default:
			w.Write([]byte(`{"choices":[{"finish_reason":"stop","message":{"content":"成功"}}]}`))
		}
	}))
	defer s.Close()
	c := Config{ChannelURL: s.URL, APIKey: "secret-value", Models: []string{"bad", "empty", "cut"}, OllamaURL: s.URL, OllamaModel: "local", Timeout: time.Second}
	p := Picture{[]byte("picture-bytes"), "image/png"}
	out, err := Judge(context.Background(), c, p, "颜色？", "auto")
	if err != nil || out.Backend != "ollama" || out.Text != "左红右蓝" {
		t.Fatalf("%+v %v", out, err)
	}
	if !reflect.DeepEqual(calls, []string{"bad", "empty", "cut", "local"}) {
		t.Fatal(calls)
	}
	calls = nil
	c.Models = []string{"good"}
	out, err = Judge(context.Background(), c, p, "颜色？", "auto")
	if err != nil || len(calls) != 1 || out.Backend != "aiferry" {
		t.Fatalf("%+v %v", out, err)
	}
}

func TestFailures(t *testing.T) {
	for _, body := range []string{`{"done":false,"message":{"content":"不完整"}}`, `{"done":true,"done_reason":"length","message":{"content":"截断"}}`, `{"done":true,"message":{"thinking":"仅思考"}}`, `not-json`, `{"done":true,"message":{"content":"secret-value"}}`} {
		t.Run(body, func(t *testing.T) {
			s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.Write([]byte(body)) }))
			defer s.Close()
			c := Config{OllamaURL: s.URL, OllamaModel: "local", APIKey: "secret-value", Timeout: time.Second}
			out, e := Judge(context.Background(), c, Picture{[]byte("image"), "image/png"}, "问题", "ollama")
			if e == nil || out.Text != "" {
				t.Fatal("错误响应被接受")
			}
		})
	}
}

func TestTimeoutAndRedirect(t *testing.T) {
	s := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { time.Sleep(60 * time.Millisecond); w.WriteHeader(500) }))
	defer s.Close()
	c := Config{ChannelURL: s.URL, APIKey: "secret-value", Models: []string{"m"}, Timeout: 5 * time.Millisecond}
	r, e := Judge(context.Background(), c, Picture{[]byte("img"), "image/png"}, "问题", "aiferry")
	if e == nil || len(r.Attempts) != 1 || !strings.Contains(r.Attempts[0].Status, "超时") {
		t.Fatalf("%+v %v", r, e)
	}
	hits := 0
	target := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { hits++ }))
	defer target.Close()
	redir := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { http.Redirect(w, r, target.URL, 302) }))
	defer redir.Close()
	c.ChannelURL = redir.URL
	c.Timeout = time.Second
	_, e = Judge(context.Background(), c, Picture{[]byte("img"), "image/png"}, "问题", "aiferry")
	if e == nil || hits != 0 {
		t.Fatal("重定向没有被拦截")
	}
}

func TestConfig(t *testing.T) {
	d := t.TempDir()
	path := filepath.Join(d, ".env")
	text := "export AIFERRY_BASE_URL='https://example.test/v1'\nAIFERRY_API_KEY=\"a=b\" # 注释\nAIFERRY_MODELS=first, second\nOLLAMA_PORT=12345\nOLLAMA_MODEL=vision\nVLM_TIMEOUT=2s\n"
	os.WriteFile(path, []byte(text), 0600)
	c, e := LoadConfig(path)
	if e != nil {
		t.Fatal(e)
	}
	if c.ChannelURL != "https://example.test/v1/chat/completions" || c.APIKey != "a=b" || c.OllamaURL != "http://127.0.0.1:12345" || c.Timeout != 2*time.Second || len(c.Models) != 2 {
		t.Fatal("配置解析不符合预期")
	}
	for _, bad := range []string{"VLM_TIMEOUT=0s", "OLLAMA_PORT=99999", "AIFERRY_API_KEY='secret", "AIFERRY_BASE_URL=https://user:password@example.test"} {
		os.WriteFile(path, []byte(bad), 0600)
		if _, e := LoadConfig(path); e == nil {
			t.Fatal("错误配置未拒绝")
		}
	}
}

func TestPicture(t *testing.T) {
	d := t.TempDir()
	path := filepath.Join(d, "test.png")
	im := image.NewNRGBA(image.Rect(0, 0, 4, 2))
	for y := 0; y < 2; y++ {
		for x := 0; x < 4; x++ {
			im.Set(x, y, color.NRGBA{R: 255, A: 255})
		}
	}
	var b bytes.Buffer
	png.Encode(&b, im)
	os.WriteFile(path, b.Bytes(), 0600)
	p, e := LoadPicture(path, "")
	if e != nil || !bytes.Equal(p.Data, b.Bytes()) {
		t.Fatal("原始字节改变")
	}
	p, e = LoadPicture(path, "1,0,3,2")
	if e != nil {
		t.Fatal(e)
	}
	decoded, _, e := image.Decode(bytes.NewReader(p.Data))
	if e != nil || decoded.Bounds().Dx() != 2 || color.NRGBAModel.Convert(decoded.At(0, 0)) != color.NRGBAModel.Convert(im.At(1, 0)) {
		t.Fatal("裁剪未无损保留像素")
	}
	for _, crop := range []string{"-1,0,2,2", "1,0,1,2", "0,0,9,2", "a,b,c,d"} {
		if _, e := LoadPicture(path, crop); e == nil {
			t.Fatal("无效裁剪被接受")
		}
	}
	os.WriteFile(path, b.Bytes()[:20], 0600)
	if _, e = LoadPicture(path, ""); e == nil {
		t.Fatal("截断图片被接受")
	}
}
