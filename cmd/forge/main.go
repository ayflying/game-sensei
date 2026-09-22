// Command forge 是「地下城里开商店」经营循环的正式入口。
//
// 为什么需要它：这款游戏没有固定胜负判据（顾客是移动的 NPC、界面态随摄像机漂移），
// 所以走不了 plan 线；但它的核心收益动作其实是**确定性**的——
// 「进制作页 → 点图纸开工 → 等制作完成 → 点就绪收取」，
// 每一步都能用 OCR 当帧定位文字再点，不依赖任何漂移坐标。
//
// 此前这条循环全靠手工点击：一轮要 4~5 次工具调用、两分半钟，
// 想靠它把玩家等级从 14 推到 15（扩建店铺的门槛）要跑几十轮，
// 既不经济也不满足「不中断、持续跑」的要求。按 AGENTS.md §2
// 「常用能力必须正式化」，本命令即该循环的正式落地。
//
// 它刻意**不使用任何写死的按钮坐标**：所有点击目标都来自当帧 OCR 的文字中心，
// 因为这款游戏有三处已实测的坐标漂移（制作页卡片顺序每帧都变、
// 就绪按钮随完成顺序左右移动、悬浮入口随摄像机漂移）。
//
// 用法：
//
//	forge -rounds 10                                   # 连跑 10 轮「制作→收取」
//	forge -item 合身外套 -rounds 5                      # 换一种图纸
//	forge -settle 4m -v -shots .workbuddy/tmp/forge     # 留取证帧 + 打印每帧 OCR
//	forge -probe                                        # 只抓一帧，报告能识别的关键文字（不点击）
//	forge -market -rounds 20 -wait 90s                  # 跑市场「收单→挂单」卖货循环（升级线）
//
// 安全约束（安酱定下的规则，代码里显式遵守）：
//
//   - **绝不使用钻石**：本命令只点「制作」「就绪」和图纸卡片，不触碰任何钻石按钮；
//     钻石支出在游戏里是不可逆的，任何自动化都不该替玩家决定。
//   - 出现「退出确认」模态框时点「返回」把它关掉，绝不点「确定」（会退出游戏）。
package main

import (
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"time"

	"github.com/ayflying/game-sensei/internal/android"
	"github.com/ayflying/game-sensei/internal/ocr"
)

// 主界面上「制作」按钮的 y 下界。制作页里也有「制作」标题（y≈648），
// 靠这个下界把两者分开——这是唯一一处依赖位置的地方，且只用于筛选同一文字的两个候选。
const homeCraftMinY = 1200

// 收取成品时最多点几次「就绪」。两个制作栏位，外加为「弹窗清障」留的余量。
const maxCollectTaps = 6

func main() {
	var (
		adbPath = flag.String("adb", "", "adb 可执行文件路径；空则自动探测。⚠️ 不要指向 .workbuddy/bin/gadb.exe——它是项目自封装的精简 adb，只认 -serial，不接受标准的 -P/-s，会在 start-server 处直接失败")
		serial  = flag.String("serial", "", "设备序列号；host:port 形式会自动 connect，空则自动发现")
		port    = flag.Int("port", 5038, "adb server 端口（本机多个 adb 共存时必须独立）")
		item    = flag.String("item", "合身外套", "要制作的物品名（制作页卡片上的文字）。默认取合身外套是因为它只需基础布料：加强木盾要「零件」，零件耗尽后点它只会弹「缺少必需品」框，白跑一轮")
		rounds  = flag.Int("rounds", 1, "制作轮数")
		settle  = flag.Duration("settle", 3*time.Minute, "单轮等待制作完成的预算")
		delay   = flag.Duration("delay", 3*time.Second, "每次点击后的等待")
		shots   = flag.String("shots", "", "留存取证截图的目录（空则不落盘）")
		probe   = flag.Bool("probe", false, "只抓一帧报告识别到的文字，不做任何点击")
		verbose = flag.Bool("v", false, "打印每帧 OCR 明细")

		market   = flag.Bool("market", false, "改跑「市场收单 → 挂单」循环（卖货线），而不是制作线")
		sellItem = flag.String("sell", "加强木盾", "市场挂单要卖的物品名（-market 时生效）")
		wait     = flag.Duration("wait", 30*time.Second, "市场每轮之间的额外间歇（-market 时生效）。成交等待已由 -sold 轮询负责，这里只是轮次间的喘息")
		sold     = flag.Duration("sold", 12*time.Minute, "在途订单等成交的预算（-market 时生效）。用轮询而不是固定 sleep：实测成交时间在 3~10 分钟之间浮动，固定等待要么白等要么等不够")
	)
	flag.Parse()

	cfg, err := ocr.LoadConfig("")
	check(err)

	dev, err := android.OpenServer(*adbPath, *serial, *port)
	if err != nil {
		// 不要写成 check(fmt.Errorf(..., err))：err 为 nil 时 %w 会产出
		// 「%!w(<nil>)」这样一个非 nil 的错误值，把「连接成功」误报成失败。
		check(fmt.Errorf("连接设备 %s: %w", *serial, err))
	}

	eng := ocr.NewEngine(cfg)
	defer eng.Close()

	if *shots != "" {
		check(os.MkdirAll(*shots, 0o755))
	}

	b := &bot{dev: dev, eng: eng, shots: *shots, verbose: *verbose, delay: *delay}

	if *probe {
		texts, err := b.frame()
		check(err)
		for _, t := range texts {
			fmt.Printf("(%4d,%4d)  %s   [%.2f]\n", t.CX(), t.CY(), t.Text, t.Score)
		}
		return
	}

	if *market {
		fmt.Printf("[forge] 市场卖货循环：物品=%s 轮数=%d 设备=%s\n", *sellItem, *rounds, *serial)
		for r := 1; r <= *rounds; r++ {
			fmt.Printf("\n=== 市场第 %d/%d 轮 ===\n", r, *rounds)
			if err := b.marketRound(*sellItem, *sold); err != nil {
				fmt.Printf("[forge] 第 %d 轮失败: %v（继续下一轮）\n", r, err)
			}
			if r < *rounds {
				time.Sleep(*wait)
			}
		}
		fmt.Printf("\n[forge] 结束：共 %d 轮\n", *rounds)
		return
	}

	fmt.Printf("[forge] 目标物品=%s 轮数=%d 设备=%s\n", *item, *rounds, *serial)
	for r := 1; r <= *rounds; r++ {
		fmt.Printf("\n=== 第 %d/%d 轮 ===\n", r, *rounds)
		if err := b.oneRound(*item, *settle); err != nil {
			// 单轮失败不终止整批：下一轮会重新回到主界面重试，
			// 让一次偶发的界面态错位不至于把长跑作废。
			fmt.Printf("[forge] 第 %d 轮失败: %v（继续下一轮）\n", r, err)
		}
	}
	fmt.Printf("\n[forge] 结束：共 %d 轮\n", *rounds)
}

// bot 把设备、OCR 引擎与取证开关绑在一起，避免每个函数都传一长串参数。
type bot struct {
	dev     *android.Device
	eng     *ocr.Engine
	shots   string
	verbose bool
	delay   time.Duration
	seq     int
}

// frame 抓一帧并识别，必要时落盘取证。
//
// 先存后认：OCR 的结论要能事后复核，而复核依托的是同一张图——
// 若先认后存，中途报错就丢掉了「识别失败的那一帧」。
func (b *bot) frame() ([]ocr.Text, error) {
	img, err := b.dev.Screenshot()
	if err != nil {
		return nil, err
	}
	if b.shots != "" {
		b.seq++
		if err := android.SavePNG(img, filepath.Join(b.shots, fmt.Sprintf("f%03d.png", b.seq))); err != nil {
			fmt.Fprintf(os.Stderr, "[forge] 取证截图写入失败: %v\n", err)
		}
	}
	texts, err := b.eng.Recognize(img)
	if err != nil {
		return nil, err
	}
	if b.verbose {
		fmt.Printf("  [帧 %d] 识别 %d 条\n", b.seq, len(texts))
		for _, t := range texts {
			fmt.Printf("    (%4d,%4d)  %s   [%.2f]\n", t.CX(), t.CY(), t.Text, t.Score)
		}
	}
	return texts, nil
}

// oneRound 跑一轮完整的「制作 → 收取」。
func (b *bot) oneRound(item string, settle time.Duration) error {
	if err := b.ensureHome(); err != nil {
		return fmt.Errorf("回主界面: %w", err)
	}
	started, err := b.startCraft(item)
	if err != nil {
		return fmt.Errorf("开工: %w", err)
	}
	fmt.Printf("  已开工 %d 个栏位\n", started)
	if started == 0 {
		return fmt.Errorf("没有任何栏位开工（图纸卡片未定位到）")
	}
	// 只数「点了几次卡片」不作数：材料不足时点击一样"成功"，只是弹出缺料框，
	// 一张图纸都没做。所以回主界面按「有倒计时 or 就绪」复核真实在跑的栏位。
	running, err := b.verifyRunning()
	if err != nil {
		return err
	}
	if running == 0 {
		return fmt.Errorf("点击后主界面没有任何栏位在跑（多半是材料不足）")
	}
	fmt.Printf("  确认 %d 个栏位在制作\n", running)
	if err := b.waitReady(settle); err != nil {
		return err
	}
	taps, gained := b.collectAll()
	fmt.Printf("  收取 %d 次%s\n", taps, gained)
	if taps == 0 {
		return fmt.Errorf("等待超时后仍未见「就绪」")
	}
	return nil
}

// ensureHome 保证当前在避难所主界面。
//
// 判据用「制作」按钮是否出现在 y>1200 处——这比记按钮坐标稳，
// 也比判断场景截图尺寸稳（同一界面的截图体积会随场景内容浮动）。
// 退出确认模态框会把后续所有点击吞掉（已实测多次），所以见到它就点「返回」。
func (b *bot) ensureHome() error {
	for attempt := 0; attempt < 8; attempt++ {
		texts, err := b.frame()
		if err != nil {
			return err
		}
		if b.dismissExitDialog(texts) {
			time.Sleep(b.delay)
			continue
		}
		// 奖励到账 / 升级演出 / 断线重连这类模态弹窗会把「制作」整个盖住，
		// 而且 **back 关不掉它们**（back 只对子页有效）——实测就是这样卡住的：
		// 「收到物品！设计草图x1」弹窗一直挂着，连续 back 5 次都回不到主界面。
		// 必须先点掉它的底部按钮。
		if b.clearModal(texts) {
			time.Sleep(b.delay)
			continue
		}
		if _, ok := pick(texts, "制作", homeCraftMinY, 0); ok {
			return nil
		}
		// 不在主界面：按返回。注意先排除模态框，否则这一下会点到对话框上。
		if err := b.dev.Key("4"); err != nil {
			return err
		}
		time.Sleep(b.delay)
	}
	return fmt.Errorf("连续 8 次尝试后仍未看到主界面的「制作」按钮")
}

// clearModal 点掉挡在路上的模态弹窗，返回是否点了一下。
//
// 只认底部（y>1250）的「重新连接 / 确认 / 继续」这三个词：它们在游戏里
// 一律是"关掉当前弹窗"的肯定按钮，点了不会造成任何不可逆后果。
// 反向的安全边界是把退出确认框的「确定」排除在外——那一对按钮在 y≈875，
// 被下面的 y 下界挡住，所以这里不可能误点到它。
func (b *bot) clearModal(texts []ocr.Text) bool {
	// 「缺少必需品 / 缺少零件 0/N」是**材料不足**弹窗：它只有「市场」「前往废弃都市」
	// 两个会把玩家带走的按钮，没有关闭键 —— 只能按 back 退掉。
	//
	// 这是实测踩到的最大的坑：该弹窗盖住主界面之后，OCR 既看不到「制作」也看不到
	// 「就绪」，现象是「连续 8 轮全部超时」，而每一轮都还报着"已开工 N 个栏位"
	// （其实一张图纸都没做出来，点击只是弹出了这个框）。材料耗尽后它必然出现，
	// 所以必须当成常态来处理，不能只当异常。
	if _, ok := pick(texts, "缺少必需品", 0, 0); ok {
		fmt.Println("  清障：材料不足弹窗，按返回键退出")
		if err := b.dev.Key("4"); err != nil {
			fmt.Fprintf(os.Stderr, "[forge] 返回键失败: %v\n", err)
		}
		return true
	}
	// 「断开连接（错误4）」的「重新连接」按钮在 y≈978，落在下面的 y 下界之外，
	// 长跑时必须单独捞——否则挂机久了必然卡在这一屏，整批轮次全部空转。
	if t, ok := pick(texts, "重新连接", 0, 0); ok {
		if err := b.dev.Tap(t.CX(), t.CY()); err != nil {
			fmt.Fprintf(os.Stderr, "[forge] 点「重新连接」失败: %v\n", err)
			return false
		}
		fmt.Printf("  清障：断线重连 (%d,%d)\n", t.CX(), t.CY())
		return true
	}
	for _, w := range []string{"重新连接", "确认", "继续"} {
		t, ok := pick(texts, w, 1250, 0)
		if !ok {
			continue
		}
		if err := b.dev.Tap(t.CX(), t.CY()); err != nil {
			fmt.Fprintf(os.Stderr, "[forge] 点「%s」失败: %v\n", w, err)
			return false
		}
		fmt.Printf("  清障：点掉「%s」(%d,%d)\n", w, t.CX(), t.CY())
		return true
	}
	return false
}

// dismissExitDialog 处理「是否确定退出？」模态框：点「返回」，绝不点「确定」。
//
// 两个按钮同一行（y≈875），「返回」在右、「确定」在左，靠 x 大小区分。
func (b *bot) dismissExitDialog(texts []ocr.Text) bool {
	var confirm, cancel ocr.Text
	hasConfirm, hasCancel := false, false
	for _, t := range texts {
		txt := strings.TrimSpace(t.Text)
		switch {
		case strings.Contains(txt, "确定") && t.CY() > 700 && t.CY() < 1100:
			confirm, hasConfirm = t, true
		case strings.Contains(txt, "返回") && t.CY() > 700 && t.CY() < 1100:
			cancel, hasCancel = t, true
		}
	}
	if !hasConfirm || !hasCancel {
		return false
	}
	if cancel.CX() < confirm.CX() {
		cancel, confirm = confirm, cancel
	}
	fmt.Printf("  发现退出确认框，点「返回」(%d,%d) 关闭\n", cancel.CX(), cancel.CY())
	if err := b.dev.Tap(cancel.CX(), cancel.CY()); err != nil {
		fmt.Fprintf(os.Stderr, "[forge] 点返回失败: %v\n", err)
	}
	return true
}

// startCraft 进制作页、连点图纸卡片开工，返回成功开工的栏位数。
//
// 点两次是因为两个制作栏位可以同时在跑；但游戏点一次就可能自动退回主界面，
// 所以每次点击后都重新确认自己在哪一屏，而不是盲连点两次。
func (b *bot) startCraft(item string) (int, error) {
	texts, err := b.frame()
	if err != nil {
		return 0, err
	}
	craft, ok := pick(texts, "制作", homeCraftMinY, 0)
	if !ok {
		return 0, fmt.Errorf("主界面没找到「制作」按钮")
	}
	if err := b.dev.Tap(craft.CX(), craft.CY()); err != nil {
		return 0, err
	}
	time.Sleep(b.delay)

	started := 0
	for attempt := 0; attempt < 2; attempt++ {
		texts, err := b.frame()
		if err != nil {
			return started, err
		}
		if attempt == 0 {
			// 制作页顶部那一条才有玩家等级（主界面顶部只有好感度/金币/钻石）。
			// 扩建店铺卡的是等级 15，所以每轮都把它打出来，长跑才有迹可循。
			if lv := topLevel(texts); lv != "" {
				fmt.Printf("  玩家等级=%s\n", lv)
			}
		}
		card, ok := pick(texts, item, 1000, 1600)
		if !ok {
			// 卡片不在屏上（可能已退回主界面，或列表滚动位置变了）就停手，
			// 已开工的栏位照常进入等待，不算失败。
			break
		}
		if err := b.dev.Tap(card.CX(), card.CY()); err != nil {
			return started, err
		}
		started++
		time.Sleep(b.delay)
	}
	return started, nil
}

// verifyRunning 回主界面数一数真正在跑的栏位（倒计时和已就绪都算一个）。
//
// 存在的理由：开工的唯一可信判据是主界面上的倒计时/就绪文字，
// 而不是「我点了几次卡片」——材料不足时后者会给出完全错误的乐观结论。
func (b *bot) verifyRunning() (int, error) {
	time.Sleep(2 * time.Second)
	texts, err := b.frame()
	if err != nil {
		return 0, err
	}
	n := 0
	for _, t := range texts {
		txt := strings.TrimSpace(t.Text)
		if reCountdown.MatchString(txt) || strings.Contains(txt, "就绪") {
			n++
		}
	}
	return n, nil
}

// waitReady 轮询等待主界面出现「就绪」，每隔几秒抓一帧。
//
// 用轮询而不是固定 sleep：制作时长受工匠等级（制作速度加成）影响，
// 写死等待要么白等要么等不够，而「就绪」文字本身就是最准的完成信号。
func (b *bot) waitReady(budget time.Duration) error {
	deadline := time.Now().Add(budget)
	interval := 5 * time.Second
	for {
		texts, err := b.frame()
		if err != nil {
			return err
		}
		if _, ok := pick(texts, "就绪", 0, 0); ok {
			return nil
		}
		// 制作期间冒出来的奖励/升级弹窗会把「就绪」盖住，顺手清掉再接着等。
		if b.clearModal(texts) {
			time.Sleep(b.delay)
			continue
		}
		if time.Now().After(deadline) {
			return fmt.Errorf("等待 %v 后仍未出现「就绪」", budget)
		}
		time.Sleep(interval)
	}
}

// collectAll 反复点「就绪」直到不再出现（每点一次收 1 件，同时结算经验）。
func (b *bot) collectAll() (int, string) {
	taps := 0
	for i := 0; i < maxCollectTaps; i++ {
		texts, err := b.frame()
		if err != nil {
			fmt.Fprintf(os.Stderr, "[forge] 抓帧失败: %v\n", err)
			return taps, ""
		}
		ready, ok := pick(texts, "就绪", 0, 0)
		if !ok {
			// 收完一件常弹「收到物品！」或升级演出，会盖住另一个栏位的「就绪」；
			// 先清一次障再判一次，避免把「还有一个没收」误判成「收完了」。
			if b.clearModal(texts) {
				continue
			}
			break
		}
		if err := b.dev.Tap(ready.CX(), ready.CY()); err != nil {
			fmt.Fprintf(os.Stderr, "[forge] 点就绪失败: %v\n", err)
			return taps, ""
		}
		taps++
		time.Sleep(2 * time.Second)
	}
	return taps, b.readStatus()
}

// readStatus 读一帧顶部状态（金币/等级/仓库计数）与经验提示，用于长跑留痕。
func (b *bot) readStatus() string {
	texts, err := b.frame()
	if err != nil {
		return ""
	}
	var parts []string
	for _, t := range texts {
		txt := strings.TrimSpace(t.Text)
		if t.CY() > 400 {
			continue
		}
		switch {
		case reCoins.MatchString(txt):
			parts = append(parts, "金币="+txt)
		case reExp.MatchString(txt):
			parts = append(parts, txt)
		}
	}
	if len(parts) == 0 {
		return ""
	}
	return "（" + strings.Join(parts, " ") + "）"
}

var (
	reCoins     = regexp.MustCompile(`^\d{1,3}(,\d{3})+$`)
	reExp       = regexp.MustCompile(`经验|等级`)
	reLevel     = regexp.MustCompile(`^\d{1,2}$`)
	reCountdown = regexp.MustCompile(`^\d+分\d+秒$|^\d+秒$|^\d+时\d+分$`)
)

// topLevel 从制作页顶部读出玩家等级。
//
// 用「y<100 且整条只有 1~2 位数字」定位：同一行里的好感度是 "107/107"、
// 金币是 "31,207"、钻石是 "260"，都不满足这个形状，所以不会串台。
func topLevel(texts []ocr.Text) string {
	for _, t := range texts {
		if t.CY() >= 100 {
			continue
		}
		if txt := strings.TrimSpace(t.Text); reLevel.MatchString(txt) {
			return txt
		}
	}
	return ""
}

// pick 在识别结果里挑一条包含 sub 的文字。
//
// minY/maxY 用于把「同一文字出现在多处」的情形分开（如主界面与制作页都有「制作」）；
// 传 0 表示不限制。命中多条时取 y 最大的一条——对底部按钮而言，
// 越靠下的那条越可能是当前屏的交互目标。
func pick(texts []ocr.Text, sub string, minY, maxY int) (ocr.Text, bool) {
	var best ocr.Text
	found := false
	for _, t := range texts {
		if !strings.Contains(t.Text, sub) {
			continue
		}
		cy := t.CY()
		if minY > 0 && cy < minY {
			continue
		}
		if maxY > 0 && cy > maxY {
			continue
		}
		if !found || cy > best.CY() {
			best, found = t, true
		}
	}
	return best, found
}

func check(err error) {
	if err == nil {
		return
	}
	fmt.Fprintln(os.Stderr, "forge:", err)
	os.Exit(1)
}

// ensureMarket 保证当前停在「市场 → 您的列表」页。
//
// 为什么不用固定坐标切模式：店铺模式与避难所模式的底部导航是两套
// （店铺/角色/市场/战利品/社区 ↔ 避难所/角色/任务/仓库/编辑），
// 而两种模式的第一个键互为切换开关。靠「导航里有没有『市场』」当判据，
// 就能在任意起点自愈到市场页，不需要记住自己是从哪一屏进来的。
func (b *bot) ensureMarket() error {
	for attempt := 0; attempt < 10; attempt++ {
		texts, err := b.frame()
		if err != nil {
			return err
		}
		if b.dismissExitDialog(texts) {
			time.Sleep(b.delay)
			continue
		}
		if b.clearModal(texts) {
			time.Sleep(b.delay)
			continue
		}
		// 已在市场页：分页标签「您的列表」只在市场页出现。
		if _, ok := pick(texts, "您的列表", 0, 0); ok {
			return nil
		}
		// 底部导航里的「市场」。市场页标题也叫「市场」（y≈490），
		// 用 y 下界把它们分开。
		if t, ok := pick(texts, "市场", 1450, 0); ok {
			if err := b.dev.Tap(t.CX(), t.CY()); err != nil {
				return err
			}
			time.Sleep(b.delay)
			continue
		}
		// 避难所模式的导航第一个键是「避难所」，点它切到店铺模式。
		if t, ok := pick(texts, "避难所", 1450, 0); ok {
			if err := b.dev.Tap(t.CX(), t.CY()); err != nil {
				return err
			}
			time.Sleep(b.delay)
			continue
		}
		if err := b.dev.Key("4"); err != nil {
			return err
		}
		time.Sleep(b.delay)
	}
	return fmt.Errorf("连续 10 次尝试后仍未进入市场页")
}

// marketRound 跑一轮「先收已成交的单，再挂一张新单」。
//
// 存在的理由：官方 FAQ 明确「售卖物品才可以获得经验提升等级，物品价值越高，
// 经验越多」——制作只加工匠经验，对扩建店铺卡的那个等级毫无帮助。
// 而市场挂单实测几分钟内就成交（不是页面上写的 12 小时），
// 所以「收单 → 挂单」是当前唯一能持续推进等级的确定性动作。
//
// 槽位只有 1 个，所以节奏是「挂着等 → 成交收钱 → 立刻续挂」：
// 若进轮时槽位还被在途订单占着，就先轮询等它成交，而不是固定 sleep
// （实测成交在 3~10 分钟之间浮动，固定等待必然浪费轮次）。
func (b *bot) marketRound(item string, soldBudget time.Duration) error {
	if err := b.ensureMarket(); err != nil {
		return fmt.Errorf("回市场页: %w", err)
	}

	// 先把已经成交、摆在下面等人的那一笔收掉。
	if n := b.collectSales(); n > 0 {
		fmt.Printf("  收单 %d 笔%s\n", n, b.readStatus())
	}

	// 槽位还被在途订单占着 → 等它成交后再收一次。
	texts, err := b.frame()
	if err != nil {
		return err
	}
	if _, ok := pick(texts, "可用", 600, 1200); !ok {
		if !b.waitSold(soldBudget, item) {
			return fmt.Errorf("等待 %v 后订单仍未成交", soldBudget)
		}
		if n := b.collectSales(); n > 0 {
			fmt.Printf("  收单 %d 笔%s\n", n, b.readStatus())
		}
	}
	return b.listItem(item)
}

// collectSales 把当前页面上所有「获取」按钮点掉，返回点了几笔。
func (b *bot) collectSales() int {
	n := 0
	for i := 0; i < 3; i++ {
		texts, err := b.frame()
		if err != nil {
			fmt.Fprintf(os.Stderr, "[forge] 抓帧失败: %v\n", err)
			return n
		}
		got, ok := pick(texts, "获取", 600, 1200)
		if !ok {
			return n
		}
		if err := b.dev.Tap(got.CX(), got.CY()); err != nil {
			fmt.Fprintf(os.Stderr, "[forge] 点「获取」失败: %v\n", err)
			return n
		}
		n++
		fmt.Printf("  收单：点「获取」(%d,%d)\n", got.CX(), got.CY())
		time.Sleep(b.delay)
	}
	return n
}

// waitSold 轮询等在途订单成交（页面出现「获取」），成交返回 true。
//
// 边等边清障：挂机久了会撞上「断开连接（错误4）」和各类奖励弹窗，
// 不清掉的话「获取」永远不会出现，整批轮次就白跑了。
func (b *bot) waitSold(budget time.Duration, item string) bool {
	deadline := time.Now().Add(budget)
	interval := 30 * time.Second
	for {
		texts, err := b.frame()
		if err != nil {
			fmt.Fprintf(os.Stderr, "[forge] 抓帧失败: %v\n", err)
			return false
		}
		if b.dismissExitDialog(texts) {
			time.Sleep(b.delay)
			continue
		}
		if b.clearModal(texts) {
			time.Sleep(b.delay)
			continue
		}
		if _, ok := pick(texts, "获取", 600, 1200); ok {
			fmt.Printf("  等到了：%s 已成交\n", item)
			return true
		}
		if time.Now().After(deadline) {
			return false
		}
		time.Sleep(interval)
	}
}

// listItem 挂一张卖单：可用槽 → 创建订单 → 选品 → 下一步 ×N → 确定。
func (b *bot) listItem(item string) error {
	texts, err := b.frame()
	if err != nil {
		return err
	}
	slot, ok := pick(texts, "可用", 600, 1200)
	if !ok {
		return fmt.Errorf("没有空闲挂单槽位（已有在途订单）")
	}
	if err := b.dev.Tap(slot.CX(), slot.CY()); err != nil {
		return err
	}
	time.Sleep(b.delay)

	texts, err = b.frame()
	if err != nil {
		return err
	}
	create, ok := pick(texts, "创建订单", 0, 0)
	if !ok {
		return fmt.Errorf("创建列表页没找到「创建订单」")
	}
	if err := b.dev.Tap(create.CX(), create.CY()); err != nil {
		return err
	}
	time.Sleep(b.delay)

	// 选品页按价值升序排列，价值最高的物品压在最底部，所以边滚边找。
	var target ocr.Text
	found := false
	for i := 0; i < 8; i++ {
		texts, err = b.frame()
		if err != nil {
			return err
		}
		if t, ok := pick(texts, item, 900, 1500); ok {
			target, found = t, true
			break
		}
		if err := b.dev.Swipe(450, 1400, 450, 900, 220*time.Millisecond); err != nil {
			return err
		}
		time.Sleep(time.Second)
	}
	if !found {
		return fmt.Errorf("选品列表里没找到 %q", item)
	}
	if err := b.dev.Tap(target.CX(), target.CY()); err != nil {
		return err
	}
	time.Sleep(b.delay)

	// 价格页 → 时长页 → 确认页。前两页是「下一步」，末页是「确定」；
	// 一律靠 OCR 认字，不写死坐标（这三页的按钮位置已实测稳定，
	// 但写死坐标会在版式微调时静默点空）。
	confirmed := false
	for step := 0; step < 4 && !confirmed; step++ {
		texts, err = b.frame()
		if err != nil {
			return err
		}
		// y 下界 1000：把退出确认框的「确定」（y≈875）排除在外，
		// 那一对按钮绝不能碰。
		if t, ok := pick(texts, "下一步", 1000, 0); ok {
			if err := b.dev.Tap(t.CX(), t.CY()); err != nil {
				return err
			}
			time.Sleep(b.delay)
			continue
		}
		if t, ok := pick(texts, "确定", 1000, 0); ok {
			if err := b.dev.Tap(t.CX(), t.CY()); err != nil {
				return err
			}
			confirmed = true
			time.Sleep(b.delay)
			continue
		}
		return fmt.Errorf("挂单第 %d 步既没找到「下一步」也没找到「确定」", step+1)
	}
	if !confirmed {
		return fmt.Errorf("走完 4 步仍未出现确认页的「确定」")
	}

	// 复核：回到市场页应看到在途订单条目（含剩余时长）。
	texts, err = b.frame()
	if err != nil {
		return err
	}
	if t, ok := pick(texts, "订单", 0, 0); ok {
		fmt.Printf("  已挂单：%s（%s）\n", item, t.Text)
		return nil
	}
	return fmt.Errorf("挂单后市场页未见订单条目")
}
