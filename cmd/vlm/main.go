// vlm 从根目录.env读取配置，渠道优先、本地兜底，输出纯文字判读。
package main

import (
	"context"
	"flag"
	"fmt"
	"github.com/ayflying/game-sensei/internal/imagejudge"
	"os"
	"os/signal"
)

func main() {
	in := flag.String("in", "", "PNG/JPEG图片路径")
	question := flag.String("question", "", "明确的图片判读问题")
	env := flag.String("env", "", "配置文件路径，默认寻找项目根目录.env")
	crop := flag.String("crop", "", "原图像素区域x0,y0,x1,y1")
	backend := flag.String("backend", "auto", "auto渠道优先本地兜底；aiferry或ollama仅用指定后端")
	flag.Parse()
	if *in == "" || *question == "" {
		fmt.Fprintln(os.Stderr, "必须指定-in和-question")
		os.Exit(2)
	}
	cfg, err := imagejudge.LoadConfig(*env)
	if err != nil {
		fail(err)
	}
	pic, err := imagejudge.LoadPicture(*in, *crop)
	if err != nil {
		fail(err)
	}
	ctx, cancel := signal.NotifyContext(context.Background(), os.Interrupt)
	defer cancel()
	result, err := imagejudge.Judge(ctx, cfg, pic, *question, *backend)
	for _, a := range result.Attempts {
		fmt.Fprintf(os.Stderr, "[尝试] %s / %s：%s\n", a.Backend, a.Model, a.Status)
	}
	if err != nil {
		fail(err)
	}
	fmt.Printf("[后端] %s\n[模型] %s\n[判读] %s\n", result.Backend, result.Model, result.Text)
}
func fail(err error) { fmt.Fprintln(os.Stderr, err); os.Exit(2) }
