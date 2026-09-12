package main

// Python/Go 前向传播一致性校验：同一帧（随机种子 42 的第 1 个样本）
// Go 算出的 logits/coords 应与 torch 基准一致（误差 < 1e-3）。
import (
	"encoding/json"
	"fmt"
	"math"
	"math/rand"
	"os"

	"github.com/ayflying/game-sensei/internal/student"
)

func main() {
	// 复现 torch 侧合成样本：np.random.rand(48,64)，seed 42 的第一个样本
	// Python 的 np.random.rand 与 Go 的 rand 不同源——直接从磁盘重算：
	// 简化：Python 端另存 sample0 的帧到 json，这里读入。
	raw, err := os.ReadFile(".workbuddy/smoke_sample0.json")
	if err != nil {
		fmt.Println("缺基准帧:", err)
		os.Exit(1)
	}
	var doc struct {
		Frame  []float64 `json:"frame"`
		Logits []float64 `json:"logits"`
		Coords []float64 `json:"coords"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		panic(err)
	}

	net, err := student.Load("models/smoke.weights.json")
	if err != nil {
		panic(err)
	}
	x := make([]float32, len(doc.Frame))
	for i, v := range doc.Frame {
		x[i] = float32(v)
	}
	logits, coords := net.ForwardRaw(x)

	fmt.Println("Go logits:", f5(logits))
	fmt.Println("基准     :", f5f(doc.Logits))
	fmt.Println("Go coords:", f5(coords))
	fmt.Println("基准      :", f5f(doc.Coords))

	maxDiff := 0.0
	for i := range doc.Logits {
		if d := math.Abs(float64(logits[i]) - doc.Logits[i]); d > maxDiff {
			maxDiff = d
		}
	}
	for i := range doc.Coords {
		if d := math.Abs(float64(coords[i]) - doc.Coords[i]); d > maxDiff {
			maxDiff = d
		}
	}
	fmt.Printf("最大误差: %.6f %s\n", maxDiff, verdict(maxDiff))
}

func verdict(d float64) string {
	if d < 1e-3 {
		return "✅ 一致"
	}
	return "❌ 不一致"
}

func f5(v []float32) string {
	s := "["
	for i, x := range v {
		if i > 0 {
			s += ", "
		}
		s += fmt.Sprintf("%.5f", x)
	}
	return s + "]"
}

func f5f(v []float64) string {
	s := "["
	for i, x := range v {
		if i > 0 {
			s += ", "
		}
		s += fmt.Sprintf("%.5f", x)
	}
	return s + "]"
}

var _ = rand.Int
