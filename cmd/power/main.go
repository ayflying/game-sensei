// Command power（编译产物建议命名 gpower）是安卓设备省电与休眠的正式入口。
//
// 为什么需要它（2026-09-22 安酱要求）：跑批设备通常被设成「插电永不休眠 +
// 常亮」，真机连跑一晚很容易掉到没电。省电方向应反过来——压低亮度、允许
// 系统按时熄屏，由程序在抓帧前自己唤醒（internal/android 的 Screenshot
// 已内置带节流的休眠自检，所以熄屏不再等于「抓不到画面」）。
//
// 用法（-adb / -serial / -P 与其他安卓命令同义；坐标无关，均为设置项）：
//
//	gpower -serial ecbff3a5 -status                  # 亮度/熄屏超时/休眠/电量
//	gpower -serial ecbff3a5 -status -json            # 同上，JSON（供脚本消费）
//	gpower -serial ecbff3a5 -save                    # 省电档：亮度 5% + 2 分钟熄屏 + 允许休眠
//	gpower -serial ecbff3a5 -save -brightness 3% -timeout 5m
//	gpower -serial ecbff3a5 -wake                    # 熄屏则唤醒（亮屏时是空操作）
//	gpower -serial ecbff3a5 -sleep                   # 立即熄屏（验证自动唤醒链路用）
//	gpower -serial ecbff3a5 -stayon 7                # 恢复插电永不休眠
//	gpower -serial ecbff3a5 -exempt com.tencent.nrc  # 加入 Doze 白名单（熄屏不掉线）
//	gpower -serial ecbff3a5 -exempt-list             # 列出 Doze 白名单
//
// -brightness 接受绝对值（如 100）或百分比（如 5%）。**推荐百分比**：
// 亮度值域因 ROM 而异（Android 标准 0~255，MIX 3 的 MIUI 实测 10~2047），
// 同一个绝对值在两套值域里含义完全不同。
//
// -exempt 与 -save 搭配用才完整：-save 允许设备熄屏省电，但熄屏会进 Doze
// 并限制后台网络，实测 180 秒即让在线游戏掉线重载；把游戏加入白名单后
// 熄屏期间网络不再被限制，「省电」与「不断线」才能兼得。
//
// 不带任何动作参数时等价于 -status。
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"time"

	"github.com/ayflying/game-sensei/internal/android"
)

// statusJSON 是 -status -json 的输出结构，供 Python 等外部脚本消费。
type statusJSON struct {
	Serial             string `json:"serial"`
	Wakefulness        string `json:"wakefulness"`
	Interactive        bool   `json:"interactive"`
	Brightness         int    `json:"brightness"`
	BrightnessPercent  int    `json:"brightness_percent"`
	BrightnessMode     int    `json:"brightness_mode"`
	BrightnessMax      int    `json:"brightness_max"`
	ScreenOffTimeoutMS int    `json:"screen_off_timeout_ms"`
	StayOnPluggedIn    int    `json:"stay_on_while_plugged_in"`
	BatteryLevel       int    `json:"battery_level"`
	PlugType           int    `json:"plug_type"`
}

func main() {
	var (
		adbPath = flag.String("adb", "", "adb 可执行文件路径（空则自动查找）")
		serial  = flag.String("serial", "", "设备序列号；空则取唯一在线设备")
		port    = flag.Int("P", 0, "adb server 端口；0=默认 server")

		status     = flag.Bool("status", false, "打印电源与省电状态（默认动作）")
		asJSON     = flag.Bool("json", false, "状态以 JSON 输出")
		save       = flag.Bool("save", false, "应用省电档：低亮度 + 允许休眠 + 短超时")
		wake       = flag.Bool("wake", false, "熄屏则唤醒（亮屏时为空操作）")
		sleep      = flag.Bool("sleep", false, "立即熄屏（验证自动唤醒用）")
		brightSpec = flag.String("brightness", "", "亮度：绝对值（100）或百分比（5%）；空=不改")
		timeout    = flag.Duration("timeout", 0, "设置熄屏超时（0=不改）")
		stayon     = flag.Int("stayon", -1, "设置插电保持唤醒位掩码 0~7（-1=不改）")

		exemptPkg   = flag.String("exempt", "", "把该包加入 Doze 白名单（熄屏不掉线）")
		unexemptPkg = flag.String("unexempt", "", "把该包移出 Doze 白名单")
		exemptList  = flag.Bool("exempt-list", false, "列出 Doze 白名单（电池优化豁免）")
	)
	flag.Parse()

	// 先解析亮度参数，格式错误就不必连设备了。
	var (
		brightVal     int
		brightIsPct   bool
		brightGiven   bool
		brightParseEr error
	)
	if *brightSpec != "" {
		brightGiven = true
		brightVal, brightIsPct, brightParseEr = android.ParseBrightnessSpec(*brightSpec)
		if brightParseEr != nil {
			fmt.Fprintln(os.Stderr, "错误:", brightParseEr)
			os.Exit(2)
		}
	}

	// 互斥的动作：一次只做一个，避免「既唤醒又熄屏」这类自相矛盾。
	exclusive := 0
	for _, on := range []bool{*save, *wake, *sleep, *exemptList, *exemptPkg != "", *unexemptPkg != ""} {
		if on {
			exclusive++
		}
	}
	if exclusive > 1 {
		fmt.Fprintln(os.Stderr, "错误：-save / -wake / -sleep / -exempt / -unexempt / -exempt-list 一次只能用其中一个")
		os.Exit(2)
	}

	d, err := android.OpenServer(*adbPath, *serial, *port)
	if err != nil {
		fmt.Fprintln(os.Stderr, "错误:", err)
		os.Exit(1)
	}

	switch {
	case *exemptList:
		pkgs, err := d.BatteryExemptList()
		if err != nil {
			fmt.Fprintln(os.Stderr, "错误: 读取 Doze 白名单失败:", err)
			os.Exit(1)
		}
		if len(pkgs) == 0 {
			fmt.Println("Doze 白名单为空")
			return
		}
		fmt.Printf("Doze 白名单 %d 个:\n", len(pkgs))
		for _, p := range pkgs {
			fmt.Println("  " + p)
		}
	case *exemptPkg != "":
		if err := d.SetBatteryExempt(*exemptPkg, true); err != nil {
			fmt.Fprintln(os.Stderr, "错误:", err)
			os.Exit(1)
		}
		fmt.Printf("已加入 Doze 白名单：%s（熄屏后网络不再被限制，不会再因 Doze 掉线）\n", *exemptPkg)
	case *unexemptPkg != "":
		if err := d.SetBatteryExempt(*unexemptPkg, false); err != nil {
			fmt.Fprintln(os.Stderr, "错误:", err)
			os.Exit(1)
		}
		fmt.Printf("已移出 Doze 白名单：%s\n", *unexemptPkg)
	case *save:
		opt := android.DefaultPowerSaving()
		if brightGiven {
			if !brightIsPct {
				// 省电档以百分比表达目标；给绝对值时按当前设备值域换算。
				min, max, rerr := d.BrightnessRange()
				if rerr != nil {
					fmt.Fprintln(os.Stderr, "错误: 读取亮度值域失败:", rerr)
					os.Exit(1)
				}
				if max <= min {
					fmt.Fprintln(os.Stderr, "错误: 亮度值域异常:", min, max)
					os.Exit(1)
				}
				opt.BrightnessPercent = android.ClampInt((brightVal-min)*100/(max-min), 0, 100)
			} else {
				opt.BrightnessPercent = brightVal
			}
		}
		if *timeout > 0 {
			opt.OffTimeout = *timeout
		}
		before, err := d.PowerState()
		if err != nil {
			fmt.Fprintln(os.Stderr, "错误: 读取设置前状态失败:", err)
			os.Exit(1)
		}
		fmt.Println("改动前:", before.String())

		after, err := d.ApplyPowerSaving(opt)
		if err != nil {
			fmt.Fprintln(os.Stderr, "错误: 应用省电档失败:", err)
			os.Exit(1)
		}
		fmt.Println("改动后:", after.String())
		reportPowerSavingGaps(opt, after)

	case *wake:
		woken, err := d.EnsureAwake()
		if err != nil {
			fmt.Fprintln(os.Stderr, "错误: 唤醒失败:", err)
			os.Exit(1)
		}
		if woken {
			fmt.Println("已发唤醒键（原为熄屏）")
		} else {
			fmt.Println("屏幕本来就是亮的，无需唤醒")
		}

	case *sleep:
		if err := d.Sleep(); err != nil {
			fmt.Fprintln(os.Stderr, "错误: 熄屏失败:", err)
			os.Exit(1)
		}
		fmt.Println("已发熄屏键")

	default:
		// 单项设置：按参数逐个应用（可组合），全部应用后读回。
		applied := false
		if *stayon >= 0 {
			if err := d.SetStayOnPluggedIn(*stayon); err != nil {
				fmt.Fprintln(os.Stderr, "错误: 设置插电唤醒失败:", err)
				os.Exit(1)
			}
			fmt.Printf("已设置 stay_on_while_plugged_in=%d\n", *stayon)
			applied = true
		}
		if brightGiven {
			var wrote int
			var berr error
			if brightIsPct {
				wrote, berr = d.SetBrightnessPercent(brightVal)
			} else {
				berr = d.SetBrightness(brightVal)
				wrote = brightVal
			}
			if berr != nil {
				fmt.Fprintln(os.Stderr, "错误: 设置亮度失败:", berr)
				os.Exit(1)
			}
			fmt.Printf("已设置亮度=%d（并关闭自动亮度）\n", wrote)
			applied = true
		}
		if *timeout > 0 {
			if err := d.SetScreenOffTimeout(*timeout); err != nil {
				fmt.Fprintln(os.Stderr, "错误: 设置熄屏超时失败:", err)
				os.Exit(1)
			}
			fmt.Printf("已设置熄屏超时=%s\n", *timeout)
			applied = true
		}
		if !applied && !*status {
			// 无动作参数 ⇒ 默认看状态
			*status = true
		}
		if *status || applied {
			st, err := d.PowerState()
			if err != nil {
				fmt.Fprintln(os.Stderr, "错误: 读取状态失败:", err)
				os.Exit(1)
			}
			printStatus(d.Serial(), st, *asJSON)
		}
	}
}

// reportPowerSavingGaps 把「设了但没生效」的项显式说出来。
//
// 设备侧有下限（熄屏超时 6 秒）、ROM 定制（MIUI 亮度值域 10~2047）与
// 自动亮度回写，静默不一致会让「已省电」变成一句没有证据的话。
func reportPowerSavingGaps(opt android.PowerSavingOptions, st android.PowerState) {
	if st.StayOnPluggedIn != 0 {
		fmt.Printf("⚠ 插电保持唤醒仍为 %d（期望 0）：该 ROM 可能不允许改此项\n", st.StayOnPluggedIn)
	}
	if st.BrightnessMode == 1 {
		fmt.Println("⚠ 自动亮度仍为开启：环境光会覆盖低亮度设置，实际亮度可能被拉高")
	}
	if pct := st.BrightnessPercent(); pct >= 0 && pct > opt.BrightnessPercent+5 {
		fmt.Printf("⚠ 亮度读回约 %d%%（期望 %d%%）：设备侧可能有下限\n", pct, opt.BrightnessPercent)
	}
	if st.ScreenOffTimeout > 0 && st.ScreenOffTimeout != opt.OffTimeout {
		fmt.Printf("⚠ 熄屏超时读回 %s（期望 %s）：设备侧下限约 6 秒\n", st.ScreenOffTimeout, opt.OffTimeout)
	}
	if st.StayOnPluggedIn == 0 && st.IsAwake() {
		fmt.Printf("省电档已生效：%s 后自动熄屏，抓帧时程序会自行唤醒\n", st.ScreenOffTimeout)
	}
}

func printStatus(serial string, st android.PowerState, asJSON bool) {
	if asJSON {
		out := statusJSON{
			Serial:             serial,
			Wakefulness:        st.Wakefulness,
			Interactive:        st.IsAwake(),
			Brightness:         st.Brightness,
			BrightnessPercent:  st.BrightnessPercent(),
			BrightnessMode:     st.BrightnessMode,
			BrightnessMax:      st.BrightnessMax,
			ScreenOffTimeoutMS: int(st.ScreenOffTimeout / time.Millisecond),
			StayOnPluggedIn:    st.StayOnPluggedIn,
			BatteryLevel:       st.BatteryLevel,
			PlugType:           st.PlugType,
		}
		b, err := json.MarshalIndent(out, "", "  ")
		if err != nil {
			fmt.Fprintln(os.Stderr, "错误: JSON 编码失败:", err)
			os.Exit(1)
		}
		fmt.Println(string(b))
		return
	}
	fmt.Println(st.String())
	if st.StayOnPluggedIn == android.StayOnAll {
		fmt.Println("提示：当前插电永不休眠，要省电可执行 -save")
	}
	if st.BrightnessMode == 1 {
		fmt.Println("提示：当前为自动亮度，环境光会决定实际亮度")
	}
}
