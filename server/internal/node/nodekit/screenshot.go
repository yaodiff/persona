package nodekit

import (
	"bytes"
	"encoding/base64"
	"image"
	"image/color"
	"image/draw"
	"image/jpeg"
	_ "image/png" // cua-driver's screenshots
	"net/http"
	"os"

	"github.com/nwverse/persona/server/internal/proto"
)

// ScreenshotImage is the screenshot cua-driver wrote, as a JPEG when that is smaller (a window's transparent corners
// on white): a desktop's PNG runs to megabytes.
func ScreenshotImage(path string) *proto.Image {
	data, err := os.ReadFile(path)
	if err != nil || len(data) == 0 {
		return nil
	}
	img, _, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return &proto.Image{Mime: http.DetectContentType(data), Data: base64.StdEncoding.EncodeToString(data)}
	}
	b := img.Bounds()
	// The bytes' own type: a screenshot may come as a JPEG (the iPhone bridge's), not only a PNG.
	out := &proto.Image{Mime: http.DetectContentType(data), Data: base64.StdEncoding.EncodeToString(data), Width: b.Dx(), Height: b.Dy()}
	flat := image.NewRGBA(b)
	draw.Draw(flat, b, &image.Uniform{C: color.White}, image.Point{}, draw.Src)
	draw.Draw(flat, b, img, b.Min, draw.Over)
	var j bytes.Buffer
	if jpeg.Encode(&j, flat, &jpeg.Options{Quality: 85}) == nil && j.Len() < len(data) {
		out.Mime, out.Data = "image/jpeg", base64.StdEncoding.EncodeToString(j.Bytes())
	}
	return out
}
