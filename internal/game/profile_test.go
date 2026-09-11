package game

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/ayflying/game-sensei/internal/agent"
)

func TestLoad_内置档案(t *testing.T) {
	names := Names()
	if len(names) == 0 {
		t.Fatal("内置档案列表为空——profiles/*.json 没被打进二进制")
	}
	want := map[string]bool{"nrc": false, "mobile_generic": false, "pc_generic": false}
	for _, n := range names {
		if _, ok := want[n]; ok {
			want[n] = true
		}
	}
	for n, found := range want {
		if !found {
			t.Errorf("内置档案缺少 %q（现有 %v）", n, names)
		}
	}
}

func TestLoad_nrc档案内容(t *testing.T) {
	p, err := Load("nrc")
	if err != nil {
		t.Fatalf("加载 nrc 档案失败: %v", err)
	}
	if p.Name != "洛克王国：世界" {
		t.Errorf("Name = %q", p.Name)
	}
	if p.Package != "com.tencent.nrc" {
		t.Errorf("Package = %q", p.Package)
	}
	if p.Move.Mode != MoveJoystick {
		t.Fatalf("Move.Mode = %q, 期望 joystick", p.Move.Mode)
	}
	// 实测值：ADB 点 (545,832) / 屏 2608x1200 → 0.209, 0.693
	if !approx(p.Move.Center[0], 0.21) || !approx(p.Move.Center[1], 0.69) {
		t.Errorf("摇杆中心 = %v, 期望接近 [0.21 0.69]", p.Move.Center)
	}
	// 半径没配时必须填成默认值，否则推杆幅度为 0 = 点了不动
	if p.Move.Radius[0] <= 0 || p.Move.Radius[1] <= 0 {
		t.Errorf("半径未填默认值: %v", p.Move.Radius)
	}
	if len(p.Hints) == 0 {
		t.Error("nrc 档案应带有界面先验")
	}
}

func TestLoad_外部文件覆盖内置(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "tmp.json")
	body := `{"name":"临时游戏","move":{"mode":"keys"}}`
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	p, err := Load(path)
	if err != nil {
		t.Fatalf("按路径加载失败: %v", err)
	}
	if p.Name != "临时游戏" {
		t.Errorf("Name = %q", p.Name)
	}
	if p.Move.Mode != MoveKeys {
		t.Errorf("Move.Mode = %q", p.Move.Mode)
	}
}

func TestLoad_字段名拼错要报错(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "bad.json")
	// centar 是 center 的错拼；静默忽略会让摇杆中心变成 0,0 而没人发现
	body := `{"name":"错拼","move":{"mode":"joystick","centar":[0.2,0.7]}}`
	if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
		t.Fatal(err)
	}
	if _, err := Load(path); err == nil {
		t.Error("字段名拼错时应报错，而不是静默忽略")
	}
}

func TestLoad_校验规则(t *testing.T) {
	cases := []struct {
		name string
		body string
	}{
		{"缺 name", `{"move":{"mode":"keys"}}`},
		{"joystick 缺 center", `{"name":"x","move":{"mode":"joystick"}}`},
		{"joystick center 全零", `{"name":"x","move":{"mode":"joystick","center":[0,0]}}`},
		{"未知 mode", `{"name":"x","move":{"mode":"teleport"}}`},
		{"按钮缺 name", `{"name":"x","move":{"mode":"keys"},"buttons":[{"pos":[0.5,0.5]}]}`},
		{"按钮名重复", `{"name":"x","move":{"mode":"keys"},"buttons":[{"name":"a","pos":[0.1,0.1]},{"name":"a","pos":[0.2,0.2]}]}`},
		{"按钮坐标越界", `{"name":"x","move":{"mode":"keys"},"buttons":[{"name":"a","pos":[1.5,0.1]}]}`},
	}
	for _, c := range cases {
		dir := t.TempDir()
		path := filepath.Join(dir, "p.json")
		if err := os.WriteFile(path, []byte(c.body), 0o644); err != nil {
			t.Fatal(err)
		}
		if _, err := Load(path); err == nil {
			t.Errorf("%s 应报错", c.name)
		}
	}
}

// move.mode=none 对纯卡牌/文字游戏是合法的：MOVE 会明确报错而不是静默乱推。
func TestLoad_允许无移动(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "cards.json")
	if err := os.WriteFile(path, []byte(`{"name":"卡牌游戏","move":{"mode":"none"}}`), 0o644); err != nil {
		t.Fatal(err)
	}
	p, err := Load(path)
	if err != nil {
		t.Fatalf("无移动的游戏应能加载: %v", err)
	}
	if p.Move.Mode != MoveNone {
		t.Errorf("Move.Mode = %q, 期望 none", p.Move.Mode)
	}
}

func TestLoad_找不到档案的报错要有用(t *testing.T) {
	_, err := Load("不存在的游戏档案")
	if err == nil {
		t.Fatal("应报错")
	}
	msg := err.Error()
	// 报错里必须列出可选的档案名，否则用户不知道下一步怎么改
	if !contains(msg, "nrc") {
		t.Errorf("报错应列出内置档案名，实际: %s", msg)
	}
}

func TestButton_按规范名与别名查找(t *testing.T) {
	p, err := Load("nrc")
	if err != nil {
		t.Fatal(err)
	}
	if _, ok := p.Button("star"); !ok {
		t.Error("按规范名 star 应能找到按钮")
	}
	if _, ok := p.Button("STAR"); !ok {
		t.Error("按钮查找应大小写不敏感")
	}
	if _, ok := p.Button("星形"); !ok {
		t.Error("按中文别名应能找到按钮")
	}
	if _, ok := p.Button("不存在的按钮"); ok {
		t.Error("不存在的按钮不该被找到")
	}
}

func TestButtonNames(t *testing.T) {
	p, err := Load("nrc")
	if err != nil {
		t.Fatal(err)
	}
	names := p.ButtonNames()
	if len(names) != len(p.Buttons) {
		t.Fatalf("ButtonNames 返回 %d 个，档案里有 %d 个", len(names), len(p.Buttons))
	}
	if names[0] != p.Buttons[0].Name {
		t.Error("ButtonNames 顺序应与档案定义一致（提示词要可复现）")
	}
}

func TestNilProfile_安全(t *testing.T) {
	var p *Profile
	if _, ok := p.Button("x"); ok {
		t.Error("nil Profile 的 Button 应返回 false")
	}
	if p.ButtonNames() != nil {
		t.Error("nil Profile 的 ButtonNames 应返回 nil")
	}
	if _, err := p.Resolve(agent.Action{Kind: agent.ActionMove, Dir: agent.DirUp}); err == nil {
		t.Error("nil Profile 解析 MOVE 应报错，而不是静默执行")
	}
}

func approx(a, b float64) bool {
	d := a - b
	if d < 0 {
		d = -d
	}
	return d < 0.005
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
