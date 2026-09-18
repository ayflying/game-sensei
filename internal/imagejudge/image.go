package imagejudge

import (
	"bytes"
	"fmt"
	"image"
	"image/draw"
	_ "image/jpeg"
	"image/png"
	"os"
	"strconv"
	"strings"
)

type Picture struct {
	Data []byte
	MIME string
}

// LoadPicture 保留原始编码，裁剪仅在内存中无损编码，不删除调用者输入。
func LoadPicture(path, crop string) (Picture, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return Picture{}, fmt.Errorf("读取图片失败")
	}
	cfg, format, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil || (format != "png" && format != "jpeg") {
		return Picture{}, fmt.Errorf("仅支持完整PNG/JPEG图片")
	}
	if cfg.Width <= 0 || cfg.Height <= 0 || int64(cfg.Width)*int64(cfg.Height) > 50000000 {
		return Picture{}, fmt.Errorf("图片像素数量超出限制")
	}
	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return Picture{}, fmt.Errorf("图片损坏或截断")
	}
	mime := "image/" + format
	if crop == "" {
		return Picture{data, mime}, nil
	}
	parts := strings.Split(crop, ",")
	if len(parts) != 4 {
		return Picture{}, fmt.Errorf("crop必须是四个像素坐标")
	}
	var n [4]int
	for i, v := range parts {
		n[i], err = strconv.Atoi(strings.TrimSpace(v))
		if err != nil {
			return Picture{}, fmt.Errorf("crop坐标必须是整数")
		}
	}
	if n[0] < 0 || n[1] < 0 || n[2] <= n[0] || n[3] <= n[1] || n[2] > cfg.Width || n[3] > cfg.Height {
		return Picture{}, fmt.Errorf("crop范围为空或超出原图")
	}
	dst := image.NewNRGBA(image.Rect(0, 0, n[2]-n[0], n[3]-n[1]))
	draw.Draw(dst, dst.Bounds(), img, image.Pt(n[0], n[1]), draw.Src)
	var b bytes.Buffer
	if err = png.Encode(&b, dst); err != nil {
		return Picture{}, fmt.Errorf("裁剪编码失败")
	}
	return Picture{b.Bytes(), "image/png"}, nil
}
