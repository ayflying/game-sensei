package game

import (
	"image"
	"image/color"
	"os"
	"strings"
	"testing"

	"github.com/ayflying/game-sensei/internal/agent"
)

// ---- 宏（macro）----
//
// 背景：技能盘是「展开 → 选卡 → 等结算」的隐藏状态序列，关思考的小模型
// 维持不住，只会复读第一步（实测 2026-09-12：连点 battle_skill 从不点卡）。
// 于是把整条序列打包成档案里的一个命名宏，老师只需一次 PRESS。
// 这组测试钉住四件事：宏能被解析出来、宏不能被塌成单点、hidden 项不进提示词、
// 动作空间能按界面态切开。

func TestMacro_按规范名与别名查找(t *testing.T) {
	p, err := Load("nrc")
	if err != nil {
		t.Fatal(err)
	}
	if len(p.Macros) == 0 {
		t.Fatal("nrc 档案应定义技能宏")
	}
	// 规范名
	if m, ok := p.Macro("cast_hetu"); !ok {
		t.Error("按规范名 cast_hetu 应能找到宏")
	} else if len(m.Steps) < 2 {
		t.Errorf("cast_hetu 至少应含「展开技能盘 + 点技能卡」两步，实际 %d 步", len(m.Steps))
	}
	// 大小写不敏感 + 中文别名（老师可能直接输出中文技能名）
	if _, ok := p.Macro("CAST_HETU"); !ok {
		t.Error("宏查找应大小写不敏感")
	}
	if _, ok := p.Macro("赫突"); !ok {
		t.Error("按中文别名应能找到宏")
	}
	if _, ok := p.Macro("不存在的宏"); ok {
		t.Error("不存在的宏不该被找到")
	}
}

func TestMacro_每个技能宏都以展开钮开头(t *testing.T) {
	p, err := Load("nrc")
	if err != nil {
		t.Fatal(err)
	}
	skill, ok := p.Button("battle_skill")
	if !ok {
		t.Fatal("档案应有 battle_skill 展开钮")
	}
	energy, ok := p.Button("battle_energy")
	if !ok {
		t.Fatal("档案应有 battle_energy 聚能钮")
	}
	for _, m := range p.Macros {
		if len(m.Steps) == 0 {
			t.Errorf("宏 %s 没有步骤", m.Name)
			continue
		}
		first := m.Steps[0]
		switch m.Name {
		case "cancel_aim":
			// 取消宏只点一次展开钮，不选卡——但它仍要展开技能盘，走下面的通用检查。
			if !approx(first.Pos[0], skill.Pos[0]) || !approx(first.Pos[1], skill.Pos[1]) {
				t.Errorf("cancel_aim 第一步应点展开钮 %v，实际 %v", skill.Pos, first.Pos)
			}
		case "gather_energy":
			// 聚能宏不碰技能盘：第一步必须点聚能钮（battle_energy）。
			if !approx(first.Pos[0], energy.Pos[0]) || !approx(first.Pos[1], energy.Pos[1]) {
				t.Errorf("gather_energy 第一步应点聚能钮 %v，实际 %v", energy.Pos, first.Pos)
			}
		case "flee_battle":
			// 逃跑宏第一步点逃跑钮，第二步必须点确认弹窗（实测：只点逃跑钮不退出）。
			if len(m.Steps) < 2 {
				t.Fatalf("flee_battle 必须有 2 步（逃跑钮 + 确认弹窗），实际 %d 步", len(m.Steps))
			}
		default:
			// 技能宏（cast_*）必须先从展开钮开始，否则后面点卡全是空的。
			if !approx(first.Pos[0], skill.Pos[0]) || !approx(first.Pos[1], skill.Pos[1]) {
				t.Errorf("宏 %s 第一步应点展开钮 %v，实际 %v", m.Name, skill.Pos, first.Pos)
			}
		}
	}
}

// TestFleeMacro_含二次确认 守卫一条真机实测出来的机制：
// 逃跑不是「点一下逃跑钮」，而是「点逃跑钮 → 弹确认框 → 点『是』」。
// 少了第二步，战斗里按逃跑会停在弹窗上，表现为「逃不掉」。
func TestFleeMacro_含二次确认(t *testing.T) {
	p, err := Load("nrc")
	if err != nil {
		t.Fatal(err)
	}
	var flee *Macro
	for i := range p.Macros {
		if p.Macros[i].Name == "flee_battle" {
			flee = &p.Macros[i]
		}
	}
	if flee == nil {
		t.Fatal("档案缺少 flee_battle 宏——战斗态需要它才能被确定性脱身")
	}
	if flee.State != "battle" {
		t.Errorf("flee_battle 的 state 应为 battle，实际 %q", flee.State)
	}

	// 第一步必须落在逃跑钮上
	fb, ok := p.Button("battle_flee")
	if !ok {
		t.Fatal("档案应有 battle_flee 逃跑钮")
	}
	if !approx(flee.Steps[0].Pos[0], fb.Pos[0]) || !approx(flee.Steps[0].Pos[1], fb.Pos[1]) {
		t.Errorf("flee_battle 第一步应点逃跑钮 %v，实际 %v", fb.Pos, flee.Steps[0].Pos)
	}

	// 第二步必须与第一步是**不同**位置（否则等于连点两次逃跑钮，不会确认）
	second := flee.Steps[1]
	if approx(second.Pos[0], fb.Pos[0]) && approx(second.Pos[1], fb.Pos[1]) {
		t.Error("flee_battle 第二步与逃跑钮同位置——确认弹窗没被点到，逃跑不会生效")
	}
	// 确认框在屏幕中下部；用一个宽松的范围挡住「把确认坐标填到屏幕角落」这类笔误
	if second.Pos[1] < 0.5 || second.Pos[1] > 0.95 {
		t.Errorf("flee_battle 确认按钮的 y=%v 不在中下部（0.5~0.95），疑似坐标填错", second.Pos[1])
	}
}

func TestResolve_宏不能被塌成单点(t *testing.T) {
	p, err := Load("nrc")
	if err != nil {
		t.Fatal(err)
	}
	_, err = p.Resolve(agent.Action{Kind: agent.ActionPress, Name: "cast_resha"})
	if err == nil {
		t.Fatal("宏不能直接 Resolve：如果静默点第一步，就等于把小模型踩过的坑搬进代码")
	}
}

func TestResolve_hidden按钮仍可直接执行(t *testing.T) {
	// hidden 只是「不出现在老师的动作协议里」，宏内部仍要能点到它。
	// 回路走的是宏的原始坐标（Tap），但按钮本身也该可解析，便于排查与复用。
	p, err := Load("nrc")
	if err != nil {
		t.Fatal(err)
	}
	out, err := p.Resolve(agent.Action{Kind: agent.ActionPress, Name: "sk_hetu"})
	if err != nil {
		t.Fatalf("hidden 按钮应仍可解析: %v", err)
	}
	if out.Kind != agent.ActionTap {
		t.Errorf("手机端 PRESS 展开后应为 Tap，得到 %v", out.Kind)
	}
}

func TestPressList_剔除hidden并带上宏(t *testing.T) {
	p, err := Load("nrc")
	if err != nil {
		t.Fatal(err)
	}
	list := p.PressList()
	for _, hidden := range []string{"battle_skill", "sk_chaodao", "sk_resha", "sk_hetu", "sk_huoyanjian"} {
		if hasItem(list, hidden) {
			t.Errorf("hidden 项 %s 不该出现在提示词清单里", hidden)
		}
	}
	// 宏必须出现：它才是模型真正该选的东西
	if !hasItem(list, "cast_hetu") {
		t.Error("宏 cast_hetu 应出现在可 PRESS 清单里（模型要按它）")
	}
	if !hasItem(list, "battle_catch") {
		t.Error("非 hidden 的 battle_catch 应仍可被老师直接按")
	}
}

func TestPressNames_不含hidden(t *testing.T) {
	p, err := Load("nrc")
	if err != nil {
		t.Fatal(err)
	}
	names := p.PressNames()
	if hasItem(names, "battle_skill") || hasItem(names, "sk_hetu") {
		t.Error("PressNames 供报错提示用，不该列 hidden 中转钮")
	}
	if !hasItem(names, "cast_hetu") {
		t.Error("PressNames 应含宏名")
	}
}

func TestPressNamesForState_战斗态只出战斗项(t *testing.T) {
	p, err := Load("nrc")
	if err != nil {
		t.Fatal(err)
	}
	battle := p.PressNamesForState(StateBattle)
	if len(battle) == 0 {
		t.Fatal("战斗态应有可轮换项")
	}
	// 严禁跨态：战斗界面没有摇杆和坐骑/跳跃钮，点了是空操作甚至误触
	for _, world := range []string{"star", "wolf", "run", "jump", "mount"} {
		if hasItem(battle, world) {
			t.Errorf("战斗态轮换里混进了大世界按钮 %s", world)
		}
	}
	for _, want := range []string{"cast_hetu", "battle_catch", "battle_flee"} {
		if !hasItem(battle, want) {
			t.Errorf("战斗态轮换应含 %s", want)
		}
	}
	// hidden 的中转钮同样不该出现在兜底轮换里
	if hasItem(battle, "battle_skill") || hasItem(battle, "sk_hetu") {
		t.Error("战斗态轮换不该含 hidden 中转钮")
	}

	world := p.PressNamesForState(StateWorld)
	if hasItem(world, "battle_flee") || hasItem(world, "battle_catch") {
		t.Error("大世界态轮换混进了战斗按钮（那些钮在大世界不存在）")
	}
	if !hasItem(world, "star") || !hasItem(world, "mount") {
		t.Error("大世界态轮换应含 star/mount")
	}
}

func TestProtocolOptionsForState_战斗态关闭MOVE且只列战斗项(t *testing.T) {
	p, err := Load("nrc")
	if err != nil {
		t.Fatal(err)
	}
	o := p.ProtocolOptionsForState(StateBattle)
	if o.HasMove {
		t.Error("战斗态必须关闭 MOVE：摇杆不存在，给 MOVE 只会空耗一步")
	}
	if len(o.Buttons) == 0 {
		t.Fatal("战斗态应列出战斗按钮/宏")
	}
	for _, hidden := range []string{"battle_skill", "sk_hetu"} {
		if hasItem(o.Buttons, hidden) {
			t.Errorf("战斗协议不该暴露 hidden 中转钮 %s", hidden)
		}
	}
	if !hasItem(o.Buttons, "cast_resha") {
		t.Error("战斗协议应把技能宏当作一个可选项列给老师")
	}
	// 战斗态同时收掉自由坐标（TAP/SWIPE/HOLD）：
	// pet_run10 实测老师在战斗里连点 7 步自由坐标全部空耗。
	if o.AllowFreePointer {
		t.Error("战斗态必须收掉自由坐标动作（战斗界面没有可自由点击的区域）")
	}

	// 世界态保留 MOVE 与摇杆相关项
	w := p.ProtocolOptionsForState(StateWorld)
	if !w.HasMove {
		t.Error("世界态应保留 MOVE")
	}
	if !w.AllowFreePointer {
		t.Error("世界态必须保留自由坐标（关弹窗/拖地图都要 TAP/SWIPE）")
	}
	// 未知态（传空串）等同世界态，不误收窄
	if u := p.ProtocolOptionsForState(""); !u.HasMove || len(u.Buttons) != len(w.Buttons) {
		t.Error("未判定出界面态时不该收窄动作空间（误判代价更大）")
	}
	if bad := p.ProtocolOptionsForState(stateInvalid); !bad.HasMove {
		t.Error("非法态名应退化为全量，而不是收窄成空")
	}
}

// ---- 战斗态像素判据 ----

// newFrame 造一张纯色底图，用于合成「战斗底条」与圆钮。
func newFrame(w, h int, bg color.RGBA) *image.RGBA {
	img := image.NewRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.SetRGBA(x, y, bg)
		}
	}
	return img
}

// drawDisc 在图上画一个实心圆（战斗圆钮的近似）。
func drawDisc(img *image.RGBA, cx, cy, r int, c color.RGBA) {
	b := img.Bounds()
	for y := cy - r; y <= cy+r; y++ {
		if y < 0 || y >= b.Dy() {
			continue
		}
		for x := cx - r; x <= cx+r; x++ {
			if x < 0 || x >= b.Dx() {
				continue
			}
			dx, dy := x-cx, y-cy
			if dx*dx+dy*dy <= r*r {
				img.SetRGBA(x, y, c)
			}
		}
	}
}

func TestIsBattle_合成帧(t *testing.T) {
	p, err := Load("nrc")
	if err != nil {
		t.Fatal(err)
	}
	if !p.CanDetectBattle() {
		t.Fatal("nrc 应配了 battle_detect")
	}

	// 用变量（而非常量）保存尺寸与位置：常量浮点乘法不能直接 int() 转换。
	w, h := 3200, 2136
	btnX := []float64{0.664, 0.728, 0.786, 0.847, 0.908} // 五个圆钮的实测归一化 x
	btnY := 0.906
	xAt := func(frac float64) int { return int(frac * float64(w)) }
	yAt := func(frac float64) int { return int(frac * float64(h)) }

	dark := color.RGBA{30, 45, 80, 255}     // 战斗底条：深蓝半透明
	cream := color.RGBA{245, 240, 225, 255} // 圆钮：亮且低饱和
	sat := color.RGBA{255, 20, 20, 255}     // 高饱和红：应被饱和度阈值排掉

	t.Run("五个奶油圆钮判为战斗", func(t *testing.T) {
		img := newFrame(w, h, dark)
		for _, fx := range btnX {
			drawDisc(img, xAt(fx), yAt(btnY), 22, cream)
		}
		if !p.IsBattle(img) {
			t.Error("底部五个奶油圆钮应判为战斗态")
		}
	})

	t.Run("纯色大世界不判战斗", func(t *testing.T) {
		if p.IsBattle(newFrame(w, h, dark)) {
			t.Error("没有圆钮的底图不该判为战斗态")
		}
	})

	t.Run("只有两个钮不达阈值", func(t *testing.T) {
		img := newFrame(w, h, dark)
		drawDisc(img, xAt(0.786), yAt(btnY), 22, cream)
		drawDisc(img, xAt(0.847), yAt(btnY), 22, cream)
		if p.IsBattle(img) {
			t.Error("2 个簇低于 min_clusters=3，不该判战斗")
		}
	})

	t.Run("高饱和亮块被排掉", func(t *testing.T) {
		img := newFrame(w, h, dark)
		for _, fx := range btnX {
			drawDisc(img, xAt(fx), yAt(btnY), 22, sat)
		}
		if p.IsBattle(img) {
			t.Error("高饱和技能特效不该被当成奶油色圆钮")
		}
	})

	t.Run("未配判据时恒为false且不panic", func(t *testing.T) {
		blank := &Profile{Name: "x"}
		if blank.CanDetectBattle() {
			t.Error("没配 battle_detect 不该说能检测")
		}
		if blank.IsBattle(newFrame(64, 64, dark)) {
			t.Error("未配判据应返回 false")
		}
		if p.IsBattle(nil) {
			t.Error("nil 图像应返回 false")
		}
		var nilP *Profile
		if nilP.IsBattle(newFrame(64, 64, dark)) {
			t.Error("nil Profile 应返回 false")
		}
	})
}

// 大地图上的圆形图标又亮又低饱和，只数「簇的个数」会把整张地图判成战斗。
// 实测（2026-09-12）：esc_map7 / probe_map3 / p_map_fresh / c0 等 16 张真实地图帧
// 全被旧判据误判成战斗，导致整轮采集在地图界面空跑（提示词被剥掉 MOVE、
// 兜底还在图上乱点、宏也在地图上瞎点）。位置判据就是为这件事加的。
func TestIsBattle_散乱图标不误判成战斗(t *testing.T) {
	p, err := Load("nrc")
	if err != nil {
		t.Fatal(err)
	}
	w, h := 3200, 2136
	dark := color.RGBA{30, 45, 80, 255}
	cream := color.RGBA{245, 240, 225, 255}
	xAt := func(f float64) int { return int(f * float64(w)) }
	yAt := func(f float64) int { return int(f * float64(h)) }

	// 这些 x 取自真实地图帧里检测到的候选簇位置（散乱、无规律）
	scattered := []float64{0.5826, 0.6252, 0.7984, 0.9828, 0.5609, 0.6034, 0.9352, 0.7294}
	for _, fy := range []float64{0.8777, 0.8914, 0.9051, 0.9171, 0.9298} {
		img := newFrame(w, h, dark)
		for _, fx := range scattered {
			drawDisc(img, xAt(fx), yAt(fy), 22, cream)
		}
		if p.IsBattle(img) {
			t.Errorf("地图式散乱图标（y=%.4f）不该被判成战斗态：8 个簇凑够了个数，但没有落在标定的按钮横排上", fy)
		}
	}
}

// 就算画了 5 个一模一样的圆钮，只要不在标定位置上（例如整体平移了 0.03），
// 也必须判成非战斗——位置判据的意义就在这里。
func TestIsBattle_位置不落位就不算战斗(t *testing.T) {
	p, err := Load("nrc")
	if err != nil {
		t.Fatal(err)
	}
	w, h := 3200, 2136
	dark := color.RGBA{30, 45, 80, 255}
	cream := color.RGBA{245, 240, 225, 255}
	xAt := func(f float64) int { return int(f * float64(w)) }
	yAt := func(f float64) int { return int(f * float64(h)) }

	// 战斗钮横排：x 方向整体右移 0.03（超过 0.015 容差），y 不变
	for _, shift := range []float64{0.03, -0.03} {
		img := newFrame(w, h, dark)
		for _, fx := range []float64{0.664, 0.728, 0.786, 0.847, 0.908} {
			drawDisc(img, xAt(fx+shift), yAt(0.905), 22, cream)
		}
		if p.IsBattle(img) {
			t.Errorf("圆钮整体平移 %.2f 后不该判成战斗（位置判据失效了）", shift)
		}
	}
	// 对照组：同一批坐标不平移 → 必须判成战斗（证明上面失败不是因为别的因素）
	img := newFrame(w, h, dark)
	for _, fx := range []float64{0.664, 0.728, 0.786, 0.847, 0.908} {
		drawDisc(img, xAt(fx), yAt(0.905), 22, cream)
	}
	if !p.IsBattle(img) {
		t.Error("落位准确的 5 个圆钮必须判成战斗（对照组失败说明测试本身有问题）")
	}
}

// 位置判据必须从档案读出来、并参与 normalize 校验。
func TestBattleDetect_位置与容差校验(t *testing.T) {
	p, err := Load("nrc")
	if err != nil {
		t.Fatal(err)
	}
	d := p.BattleDetect
	if d == nil {
		t.Fatal("nrc 应配 battle_detect")
	}
	if len(d.Positions) != 5 {
		t.Errorf("nrc 应标定 5 个战斗圆钮位置，实际 %d 个", len(d.Positions))
	}
	if d.MinHits < 3 {
		t.Errorf("min_hits=%d 太松，地图上的散乱图标会凑够", d.MinHits)
	}
	if d.MinHits > len(d.Positions) {
		t.Errorf("min_hits=%d 超过标定位置数 %d，永远判不出战斗", d.MinHits, len(d.Positions))
	}
	tolX, tolY := d.tolerances()
	if tolX <= 0 || tolY <= 0 {
		t.Errorf("容差必须为正，得到 %.3f/%.3f", tolX, tolY)
	}
	// 容差必须小于相邻按钮间距的一半，否则会互相串位（A 钮匹配到 B 钮上）
	if len(d.Positions) >= 2 {
		gap := d.Positions[1][0] - d.Positions[0][0]
		if tolX*2 >= gap {
			t.Errorf("tol_x=%.3f 相对按钮间距 %.3f 过大，会互相串位", tolX, gap)
		}
	}

	// 非法档案要被 normalize 拦下
	body := `{"name":"x","move":{"mode":"keys"},"battle_detect":{"band":[0.5,0.8,1.0,0.95],"positions":[[1.4,0.9]]}}`
	dir := t.TempDir()
	path := dir + "/bad_pos.json"
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil {
		t.Error("positions 里出现越界坐标应报错")
	}
	tol := `{"name":"x","move":{"mode":"keys"},"battle_detect":{"band":[0.5,0.8,1.0,0.95],"positions":[[0.6,0.9]],"tol_x":0.2}}`
	path2 := dir + "/bad_tol.json"
	if err := os.WriteFile(path2, []byte(tol), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path2); err == nil {
		t.Error("容差大到会串位时应报错")
	}
}

// hasItem 在带说明的清单里查找项：清单项形如 cast_hetu(释放赫突…)，
// 因此除了精确相等，还要认「名字 + 半角/全角左括号」这一种写法。
func hasItem(list []string, name string) bool {
	for _, s := range list {
		if s == name {
			return true
		}
		rest, ok := strings.CutPrefix(s, name)
		if ok && (strings.HasPrefix(rest, "(") || strings.HasPrefix(rest, "（")) {
			return true
		}
	}
	return false
}
