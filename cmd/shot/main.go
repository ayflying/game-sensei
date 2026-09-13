// Command shot 是真机（安卓后端）标定/复盘用的截图小工具。
//
// 为什么需要它：AGENTS.md §0.4 立了一条铁律——**读图只读压缩版 `.s.jpg`**。
// 真机截图（`screencap`）出来的就是全尺寸 PNG（2136x3200、约 1.2MB），
// 直接交给多模态模型会把对话请求体撑到 8MB+，服务端直接 400 打断整个回合。
// 所以每次要看画面，都必须先压成 1024 宽的小图（约 90KB，UI 文字仍清晰）。
//
// 本机没有 PIL（python 侧压不动），所以这里用 Go 复用 `internal/vision` 的
// `Downscale` + `EncodeJPEG`，同时复用 `internal/android` 的截图与点击，
// 一个命令就能「点一下 → 截图 → 压出小图」——标定新坐标时最省事。
//
// 用法：
//
//	shot -o _cal.png                                  # 截图并压出 _cal.s.jpg
//	shot -tap 0.786,0.906 -o _aim.png -wait 2s        # 先点「捕捉」再截图（看投球瞄准态）
//	shot -swipe 0.30,0.28,0.62,0.52 -drag 220ms -o _fling.png   # 拖拽（试投球抛物线）
//	shot -front                                       # 只打印前台包名（判断游戏在不在前台）
//	shot -pack .workbuddy/demos                       # 把目录里已有的 PNG 批量压成 .s.jpg
//
// 坐标一律用归一化 0~1（与游戏档案、动作协议同一套口径），不用像素。
package main

import (
	"flag"
	"fmt"
	"image"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/ayflying/game-sensei/internal/android"
	"github.com/ayflying/game-sensei/internal/vision"
)

// smallWidth 压缩图宽度。与 AGENTS.md §0.4 的口径一致（720~1024 宽、
// q80~88 下约 90~130KB），再大就逼近 400 的临界值了。
const smallWidth = 1024

func main() {
	var (
		adbPath  = flag.String("adb", "", "adb 可执行文件路径（空则自动查找）")
		serial   = flag.String("serial", "", "设备序列号（空则取唯一在线设备）")
		out      = flag.String("o", "", "原始 PNG 输出路径（同时生成同名 .s.jpg）")
		quality  = flag.Int("q", 88, "JPEG 质量")
		width    = flag.Int("w", smallWidth, "压缩图最大宽度（像素）")
		tapArg   = flag.String("tap", "", "截图前先点一下，归一化坐标 x,y（如 0.786,0.906）")
		swipeArg = flag.String("swipe", "", "截图前先拖拽，归一化 x1,y1,x2,y2")
		dragMs   = flag.Int("drag", 220, "拖拽时长（毫秒），仅 -swipe 有效")
		wait     = flag.Duration("wait", 1500*time.Millisecond, "动作后等待多久再截图")
		front    = flag.Bool("front", false, "只打印前台包名后退出")
		pack     = flag.String("pack", "", "批量模式：把该目录（含子目录）里的 PNG 压成同名 .s.jpg")
		noJPG    = flag.Bool("nojpg", false, "只存原始 PNG，不生成压缩图")
	)
	flag.Parse()

	if *pack != "" {
		if err := packDir(*pack, *width, *quality); err != nil {
			fail(err)
		}
		return
	}

	dev, err := openDevice(*adbPath, *serial)
	if err != nil {
		fail(err)
	}

	if *front {
		pkg, err := dev.Foreground()
		if err != nil {
			fail(err)
		}
		fmt.Println(pkg)
		return
	}

	// 可选：先做动作——标定时最常用的就是「点一下再截图」。
	if *tapArg != "" {
		x, y, err := parsePair(*tapArg)
		if err != nil {
			fail(fmt.Errorf("-tap %q 不是 x,y 形式: %w", *tapArg, err))
		}
		px, py, err := dev.TapNorm(x, y)
		if err != nil {
			fail(err)
		}
		fmt.Printf("tap (%.3f,%.3f) → 像素 (%d,%d)\n", x, y, px, py)
		time.Sleep(*wait)
	}
	if *swipeArg != "" {
		v, err := parseQuad(*swipeArg)
		if err != nil {
			fail(fmt.Errorf("-swipe %q 不是 x1,y1,x2,y2 形式: %w", *swipeArg, err))
		}
		d := time.Duration(*dragMs) * time.Millisecond
		if err := dev.SwipeNorm(v[0], v[1], v[2], v[3], d); err != nil {
			fail(err)
		}
		fmt.Printf("swipe (%.3f,%.3f)→(%.3f,%.3f) %v\n", v[0], v[1], v[2], v[3], d)
		time.Sleep(*wait)
	}

	img, err := dev.Screenshot()
	if err != nil {
		fail(err)
	}
	b := img.Bounds()
	fmt.Printf("screenshot %dx%d\n", b.Dx(), b.Dy())

	if *out == "" {
		*out = filepath.Join(".workbuddy", "demos", "_shot.png")
	}
	if err := os.MkdirAll(filepath.Dir(*out), 0o755); err != nil && filepath.Dir(*out) != "." {
		fail(err)
	}
	if err := android.SavePNG(img, *out); err != nil {
		fail(err)
	}
	abs, _ := filepath.Abs(*out)
	st, _ := os.Stat(*out)
	fmt.Printf("PNG  %s (%d KB)\n", abs, sizeKB(st))

	if *noJPG {
		return
	}
	small := vision.Downscale(img, *width)
	raw, err := vision.EncodeJPEG(small, *quality)
	if err != nil {
		fail(err)
	}
	jpgPath := strings.TrimSuffix(*out, filepath.Ext(*out)) + ".s.jpg"
	if err := os.WriteFile(jpgPath, raw, 0o644); err != nil {
		fail(err)
	}
	absJ, _ := filepath.Abs(jpgPath)
	sb := small.Bounds()
	fmt.Printf("JPG  %s (%dx%d, %d KB) ← 读这张\n", absJ, sb.Dx(), sb.Dy(), len(raw)/1024)
}

// openDevice 打开安卓设备：adbPath/serial 留空时自动发现。
func openDevice(adbPath, serial string) (*android.Device, error) {
	if adbPath == "" {
		p, err := android.FindADB()
		if err != nil {
			return nil, fmt.Errorf("找不到 adb（用 -adb 指定）: %w", err)
		}
		adbPath = p
	}
	if serial == "" {
		infos, err := android.Devices(adbPath)
		if err != nil {
			return nil, err
		}
		if len(infos) == 0 {
			return nil, fmt.Errorf("没有在线设备；无线调试请先 adb connect <ip:port>")
		}
		if len(infos) > 1 {
			var names []string
			for _, i := range infos {
				names = append(names, i.Serial)
			}
			return nil, fmt.Errorf("有多个设备，请用 -serial 指定：%s", strings.Join(names, "、"))
		}
		serial = infos[0].Serial
	}
	return android.Open(adbPath, serial)
}

// packDir 批量压缩目录里已有的 PNG。用于给历史采集/标定产物补压缩图。
func packDir(dir string, width, quality int) error {
	n, saved := 0, 0
	err := filepath.Walk(dir, func(path string, info os.FileInfo, err error) error {
		if err != nil || info.IsDir() {
			return err
		}
		if !strings.EqualFold(filepath.Ext(path), ".png") {
			return nil
		}
		f, err := os.Open(path)
		if err != nil {
			return err
		}
		img, _, derr := image.Decode(f)
		f.Close()
		if derr != nil {
			fmt.Printf("  跳过（解码失败）%s: %v\n", path, derr)
			return nil
		}
		raw, err := vision.EncodeJPEG(vision.Downscale(img, width), quality)
		if err != nil {
			return err
		}
		dst := strings.TrimSuffix(path, filepath.Ext(path)) + ".s.jpg"
		if err := os.WriteFile(dst, raw, 0o644); err != nil {
			return err
		}
		n++
		saved += len(raw)
		return nil
	})
	if err != nil {
		return err
	}
	fmt.Printf("pack 完成：%d 张 PNG → .s.jpg，共 %d KB\n", n, saved/1024)
	return nil
}

func parsePair(s string) (float64, float64, error) {
	parts := strings.Split(s, ",")
	if len(parts) != 2 {
		return 0, 0, fmt.Errorf("需要 2 个数")
	}
	a, err := strconv.ParseFloat(strings.TrimSpace(parts[0]), 64)
	if err != nil {
		return 0, 0, err
	}
	b, err := strconv.ParseFloat(strings.TrimSpace(parts[1]), 64)
	if err != nil {
		return 0, 0, err
	}
	return a, b, nil
}

func parseQuad(s string) ([4]float64, error) {
	var out [4]float64
	parts := strings.Split(s, ",")
	if len(parts) != 4 {
		return out, fmt.Errorf("需要 4 个数")
	}
	for i, p := range parts {
		v, err := strconv.ParseFloat(strings.TrimSpace(p), 64)
		if err != nil {
			return out, err
		}
		out[i] = v
	}
	return out, nil
}

func sizeKB(st os.FileInfo) int64 {
	if st == nil {
		return 0
	}
	return st.Size() / 1024
}

func fail(err error) {
	fmt.Fprintln(os.Stderr, "shot:", err)
	os.Exit(1)
}
