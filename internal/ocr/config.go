package ocr

import (
	"bufio"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"
)

// 配置键名。全部可选：OCR 不配也能跑（解释器自动探测、阈值用引擎默认），
// 这与 VLM 渠道那套「必须配密钥」的配置不同——OCR 完全在本地，
// 没有任何密钥或远端地址，所以 .env 缺失不算错误。
const (
	EnvPython    = "OCR_PYTHON"     // 解释器路径
	EnvTimeout   = "OCR_TIMEOUT"    // 单次识别超时，如 30s
	EnvIdle      = "OCR_IDLE"       // 空闲多久关掉子进程，如 5m；0 表示不自动关
	EnvBoxThresh = "OCR_BOX_THRESH" // 检测框阈值，如 0.5
	EnvTextScore = "OCR_TEXT_SCORE" // 文字置信度阈值，如 0.5
)

// EnvPythonOverride 是环境变量形式的解释器覆盖，优先于 .env。
//
// 存在两个入口是因为使用场景不同：.env 是「这台机器上 python 在哪」的长期约定，
// 环境变量是「这次临时换个解释器试试」——后者不该逼人去改共享的配置文件。
const EnvPythonOverride = "GAME_SENSEI_PYTHON"

// LoadConfig 读取 OCR 配置。
//
// path 为空时自动定位项目根目录下的 .env；文件不存在时返回全默认配置
// （不报错，理由见上面的键名注释）。
func LoadConfig(path string) (Config, error) {
	var c Config
	if path == "" {
		root, err := findRoot()
		if err != nil {
			return c, err
		}
		path = filepath.Join(root, ".env")
	}
	values, err := readEnvFile(path)
	if err != nil {
		return c, err
	}
	c.Python = values[EnvPython]
	if v := values[EnvTimeout]; v != "" {
		d, err := time.ParseDuration(v)
		if err != nil || d <= 0 {
			return c, fmt.Errorf("%s 必须是正时长，例如 30s", EnvTimeout)
		}
		c.Timeout = d
	}
	if v := values[EnvIdle]; v != "" {
		d, err := time.ParseDuration(v)
		if err != nil || d < 0 {
			return c, fmt.Errorf("%s 必须是非负时长，例如 5m 或 0", EnvIdle)
		}
		// 显式写 0 表示「一直留着，不自动关」；未配置则用 NewEngine 的默认 5 分钟。
		if d == 0 {
			c.Idle = -1
		} else {
			c.Idle = d
		}
	}
	if v := values[EnvBoxThresh]; v != "" {
		f, err := strconv.ParseFloat(v, 64)
		if err != nil || f <= 0 || f > 1 {
			return c, fmt.Errorf("%s 必须是 0~1 之间的小数", EnvBoxThresh)
		}
		c.BoxThresh = f
	}
	if v := values[EnvTextScore]; v != "" {
		f, err := strconv.ParseFloat(v, 64)
		if err != nil || f <= 0 || f > 1 {
			return c, fmt.Errorf("%s 必须是 0~1 之间的小数", EnvTextScore)
		}
		c.TextScore = f
	}
	return c, nil
}

// DefaultPython 返回可用的 python 解释器路径，找不到返回 ""。
//
// 探测顺序：环境变量覆盖 → PATH 里的 python/python3 → 本机已装的受管 venv。
// **逐个用 `import rapidocr_onnxruntime` 验证**，而不是只挑第一个存在的：
// 系统 python 通常没装 RapidOCR，直接用它会让引擎在启动时炸掉；
// 验证一次约 0.4s，只做一遍（结果缓存在进程内），比事后排查便宜得多。
//
// 全部候选都没装 RapidOCR 时返回第一个候选——让它去启动并打出真实的
// ImportError，比在这里编一句「找不到 python」准确。
func DefaultPython() string {
	pythonOnce.Do(func() { pythonPath = probePython(pythonCandidates()) })
	return pythonPath
}

var (
	pythonOnce sync.Once
	pythonPath string
)

func pythonCandidates() []string {
	var out []string
	if v := strings.TrimSpace(os.Getenv(EnvPythonOverride)); v != "" {
		out = append(out, v)
	}
	for _, name := range []string{"python", "python3"} {
		if p, err := exec.LookPath(name); err == nil {
			out = append(out, p)
		}
	}
	out = append(out, venvPythons()...)
	return out
}

// venvPythons 枚举本机受管 python 环境（~/.workbuddy/binaries/python/envs/<名>）。
//
// 项目训练侧用的就是这里的 default 环境，RapidOCR 也装在同一处；
// 不写死绝对路径，是为了换机器 / 多环境时仍能自动找到。
func venvPythons() []string {
	home, err := os.UserHomeDir()
	if err != nil {
		return nil
	}
	base := filepath.Join(home, ".workbuddy", "binaries", "python", "envs")
	entries, err := os.ReadDir(base)
	if err != nil {
		return nil
	}
	var out []string
	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		dir := filepath.Join(base, e.Name())
		if runtime.GOOS == "windows" {
			out = append(out, filepath.Join(dir, "Scripts", "python.exe"))
		} else {
			out = append(out, filepath.Join(dir, "bin", "python"))
		}
	}
	return out
}

func probePython(cands []string) string {
	var first string
	for _, py := range cands {
		if py == "" {
			continue
		}
		if st, err := os.Stat(py); err != nil || st.IsDir() {
			continue
		}
		if first == "" {
			first = py
		}
		if HasRapidOCR(py) {
			return py
		}
	}
	return first
}

// HasRapidOCR 验证解释器能 import RapidOCR（不加载模型，约 0.4s）。
//
// 导出给命令层做诊断用（`ocr -check`）：同一个判断出现在两处时，
// 一旦口径变了就会互相打架。
func HasRapidOCR(py string) bool {
	cmd := exec.Command(py, "-c", "import rapidocr_onnxruntime")
	cmd.Env = append(os.Environ(), "PYTHONIOENCODING=utf-8", "PYTHONUTF8=1")
	cmd.Stdout, cmd.Stderr = nil, nil
	return cmd.Run() == nil
}

// findRoot 从可执行文件所在目录与当前目录向上找 go.mod，返回项目根。
//
// 与 internal/imagejudge 的同名逻辑同源：两处都要在「go run 的临时二进制」
// 与「编译后拷走的二进制」两种形态下找到根目录的 .env。等第三处需要时
// 再抽成公共包，现在抽只会把已验证的读图链路一起卷进来。
func findRoot() (string, error) {
	exe, _ := os.Executable()
	cwd, _ := os.Getwd()
	for _, start := range []string{filepath.Dir(exe), cwd} {
		if start == "" {
			continue
		}
		for dir := start; ; dir = filepath.Dir(dir) {
			if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
				return dir, nil
			}
			if filepath.Dir(dir) == dir {
				break
			}
		}
	}
	return "", fmt.Errorf("找不到项目根目录（未见 go.mod），请用 -env 指定配置文件")
}

// readEnvFile 解析 .env；文件不存在返回空 map（不是错误）。
//
// 支持 `KEY=VALUE`、`export KEY=VALUE`、`#` 注释、值两侧的单双引号，
// 与 imagejudge 的解析口径一致，免得同一份 .env 在两个包里读出不同结果。
func readEnvFile(path string) (map[string]string, error) {
	values := map[string]string{}
	f, err := os.Open(path)
	if err != nil {
		if os.IsNotExist(err) {
			return values, nil
		}
		return nil, fmt.Errorf("无法读取配置文件 %s", path)
	}
	defer f.Close()

	s := bufio.NewScanner(f)
	line := 0
	for s.Scan() {
		line++
		text := strings.TrimSpace(strings.TrimPrefix(s.Text(), "\ufeff"))
		if text == "" || strings.HasPrefix(text, "#") {
			continue
		}
		text = strings.TrimPrefix(text, "export ")
		k, v, ok := strings.Cut(text, "=")
		if !ok {
			return nil, fmt.Errorf("配置第 %d 行缺少等号", line)
		}
		k = strings.TrimSpace(k)
		v = strings.TrimSpace(v)
		if v != "" && (v[0] == '"' || v[0] == '\'') {
			q := v[0]
			end := strings.IndexByte(v[1:], q)
			if end < 0 {
				return nil, fmt.Errorf("配置第 %d 行引号未闭合", line)
			}
			end++
			if rest := strings.TrimSpace(v[end+1:]); rest != "" && !strings.HasPrefix(rest, "#") {
				return nil, fmt.Errorf("配置第 %d 行引号后有多余内容", line)
			}
			v = v[1:end]
		} else if i := strings.Index(v, " #"); i >= 0 {
			v = strings.TrimSpace(v[:i])
		}
		values[k] = v
	}
	if err := s.Err(); err != nil {
		return nil, fmt.Errorf("配置读取失败: %w", err)
	}
	return values, nil
}
