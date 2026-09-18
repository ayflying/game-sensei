// Package imagejudge 提供独立、无历史图片累积的视觉判读。
package imagejudge

import (
	"bufio"
	"fmt"
	"net/url"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"
)

type Config struct {
	ChannelURL, APIKey               string
	Models                           []string
	OllamaURL, OllamaModel, ProxyURL string
	Timeout                          time.Duration
}

// LoadConfig 仅读取指定文件或项目根目录配置，不污染进程环境。
func LoadConfig(path string) (Config, error) {
	var c Config
	c.Timeout = 120 * time.Second
	if path == "" {
		exe, _ := os.Executable()
		cwd, _ := os.Getwd()
		for _, start := range []string{filepath.Dir(exe), cwd} {
			for dir := start; dir != ""; dir = filepath.Dir(dir) {
				if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
					path = filepath.Join(dir, ".env")
					break
				}
				if filepath.Dir(dir) == dir {
					break
				}
			}
			if path != "" {
				break
			}
		}
		if path == "" {
			return c, fmt.Errorf("找不到项目根目录，请用 -env 指定配置")
		}
	}
	f, err := os.Open(path)
	if err != nil {
		return c, fmt.Errorf("无法读取配置文件")
	}
	defer f.Close()
	values := map[string]string{}
	s := bufio.NewScanner(f)
	line := 0
	for s.Scan() {
		line++
		text := strings.TrimSpace(strings.TrimPrefix(s.Text(), string(rune(0xFEFF))))
		if text == "" || strings.HasPrefix(text, "#") {
			continue
		}
		text = strings.TrimPrefix(text, "export ")
		k, v, ok := strings.Cut(text, "=")
		if !ok {
			return c, fmt.Errorf("配置第%d行缺少等号", line)
		}
		k = strings.TrimSpace(k)
		v = strings.TrimSpace(v)
		if strings.HasPrefix(v, "\"") || strings.HasPrefix(v, "'") {
			q := v[0]
			end := strings.IndexByte(v[1:], q)
			if end < 0 {
				return c, fmt.Errorf("配置第%d行引号未闭合", line)
			}
			end++
			rest := strings.TrimSpace(v[end+1:])
			if rest != "" && !strings.HasPrefix(rest, "#") {
				return c, fmt.Errorf("配置第%d行引号后有多余内容", line)
			}
			v = v[1:end]
		} else if i := strings.Index(v, " #"); i >= 0 {
			v = strings.TrimSpace(v[:i])
		}
		values[k] = v
	}
	if s.Err() != nil {
		return c, fmt.Errorf("配置读取失败")
	}
	c.ChannelURL = values["AIFERRY_BASE_URL"]
	c.APIKey = values["AIFERRY_API_KEY"]
	for _, m := range strings.Split(values["AIFERRY_MODELS"], ",") {
		if m = strings.TrimSpace(m); m != "" {
			c.Models = append(c.Models, m)
		}
	}
	if c.ChannelURL != "" {
		c.ChannelURL = strings.TrimRight(c.ChannelURL, "/")
		if !strings.HasSuffix(c.ChannelURL, "/chat/completions") {
			c.ChannelURL += "/chat/completions"
		}
	}
	c.OllamaURL = strings.TrimRight(values["OLLAMA_BASE_URL"], "/")
	c.OllamaModel = values["OLLAMA_MODEL"]
	if c.OllamaURL == "" && values["OLLAMA_PORT"] != "" {
		n, e := strconv.Atoi(values["OLLAMA_PORT"])
		if e != nil || n < 1 || n > 65535 {
			return c, fmt.Errorf("OLLAMA_PORT 无效")
		}
		c.OllamaURL = "http://127.0.0.1:" + strconv.Itoa(n)
	}
	c.ProxyURL = values["VLM_PROXY_URL"]
	for _, v := range []string{c.ChannelURL, c.OllamaURL, c.ProxyURL} {
		if v != "" {
			u, e := url.Parse(v)
			if e != nil || u.Host == "" || (u.Scheme != "http" && u.Scheme != "https") || u.User != nil || u.RawQuery != "" || u.Fragment != "" {
				return c, fmt.Errorf("服务或代理地址无效，禁止内嵌凭据及查询参数")
			}
		}
	}
	if v := values["VLM_TIMEOUT"]; v != "" {
		c.Timeout, err = time.ParseDuration(v)
		if err != nil || c.Timeout <= 0 {
			return c, fmt.Errorf("VLM_TIMEOUT 必须是正时长，例如120s")
		}
	}
	return c, nil
}
