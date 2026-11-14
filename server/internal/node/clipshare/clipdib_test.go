package clipshare

import (
	"bytes"
	"encoding/binary"
	"hash/crc32"
	"image"
	"image/color"
	"image/jpeg"
	"image/png"
	"testing"
)

func decodePNG(t *testing.T, b []byte) image.Image {
	t.Helper()
	img, err := png.Decode(bytes.NewReader(b))
	if err != nil {
		t.Fatal(err)
	}
	return img
}

// An image set on Windows goes on as a 32-bit CF_DIB, and a CF_DIB read back is the same picture as PNG.
func TestDIBRoundTrip(t *testing.T) {
	src := image.NewNRGBA(image.Rect(0, 0, 3, 2))
	src.SetNRGBA(0, 0, color.NRGBA{R: 255, A: 255})
	src.SetNRGBA(2, 1, color.NRGBA{B: 255, A: 128})
	var in bytes.Buffer
	_ = png.Encode(&in, src)
	dib, pngData, err := imageToDIB(in.Bytes())
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(pngData, in.Bytes()) {
		t.Fatal("a PNG is put on as it came")
	}
	out, err := dibToPNG(dib)
	if err != nil {
		t.Fatal(err)
	}
	got := decodePNG(t, out)
	if got.Bounds() != src.Bounds() {
		t.Fatalf("bounds %v", got.Bounds())
	}
	for _, p := range []image.Point{{0, 0}, {2, 1}, {1, 0}} {
		want := src.NRGBAAt(p.X, p.Y)
		if c := color.NRGBAModel.Convert(got.At(p.X, p.Y)).(color.NRGBA); c != want {
			t.Errorf("%v: %v, want %v", p, c, want)
		}
	}
}

// A JPEG is put on as PNG too.
func TestDIBFromJPEG(t *testing.T) {
	var in bytes.Buffer
	_ = jpeg.Encode(&in, image.NewRGBA(image.Rect(0, 0, 4, 4)), nil)
	dib, pngData, err := imageToDIB(in.Bytes())
	if err != nil || sniffImage(pngData) != "image/png" || len(dib) != dibHeader+4*4*4 {
		t.Fatalf("%v %q %d", err, sniffImage(pngData), len(dib))
	}
}

// What Windows apps put on: 24-bit top-down, and 32-bit with no alpha (opaque, not invisible).
func TestDIBVariants(t *testing.T) {
	le := binary.LittleEndian
	header := func(w, h int32, bpp uint16, comp uint32) []byte {
		b := make([]byte, dibHeader)
		le.PutUint32(b[0:], dibHeader)
		le.PutUint32(b[4:], uint32(w))
		le.PutUint32(b[8:], uint32(h))
		le.PutUint16(b[12:], 1)
		le.PutUint16(b[14:], bpp)
		le.PutUint32(b[16:], comp)
		return b
	}
	// 2×2, 24-bit, top-down: rows padded to 8 bytes; first row red, blue.
	d := header(2, -2, 24, biRGB)
	d = append(d, 0, 0, 255, 255, 0, 0, 0, 0)
	d = append(d, 0, 255, 0, 0, 0, 0, 0, 0)
	img := decodePNG(t, must(dibToPNG(d)))
	if c := color.NRGBAModel.Convert(img.At(0, 0)).(color.NRGBA); c != (color.NRGBA{R: 255, A: 255}) {
		t.Errorf("24-bit top-left %v", c)
	}
	if c := color.NRGBAModel.Convert(img.At(1, 0)).(color.NRGBA); c != (color.NRGBA{B: 255, A: 255}) {
		t.Errorf("24-bit top-right %v", c)
	}
	// 1×2, 32-bit bitfields, bottom-up, alpha all zero: the bottom row is first.
	d = header(1, 2, 32, biBitfields)
	d = le.AppendUint32(d, 0x00FF0000)
	d = le.AppendUint32(d, 0x0000FF00)
	d = le.AppendUint32(d, 0x000000FF)
	d = le.AppendUint32(d, 0x0000FF00) // bottom: green
	d = le.AppendUint32(d, 0x00FF0000) // top: red
	img = decodePNG(t, must(dibToPNG(d)))
	if c := color.NRGBAModel.Convert(img.At(0, 0)).(color.NRGBA); c != (color.NRGBA{R: 255, A: 255}) {
		t.Errorf("32-bit top %v", c)
	}
	if c := color.NRGBAModel.Convert(img.At(0, 1)).(color.NRGBA); c != (color.NRGBA{G: 255, A: 255}) {
		t.Errorf("32-bit bottom %v", c)
	}
	// A color table (biClrUsed) before the pixels is skipped.
	d = header(1, 1, 24, biRGB)
	le.PutUint32(d[32:], 2)
	d = append(d, 9, 9, 9, 9, 9, 9, 9, 9) // two palette entries
	d = append(d, 0, 0, 255, 0)           // one red pixel, padded to 4
	img = decodePNG(t, must(dibToPNG(d)))
	if c := color.NRGBAModel.Convert(img.At(0, 0)).(color.NRGBA); c != (color.NRGBA{R: 255, A: 255}) {
		t.Errorf("after a color table %v", c)
	}
	// Corrupt or unsupported ones are refused, not read past their end.
	for name, bad := range map[string][]byte{
		"short":      d[:20],
		"truncated":  header(100, 100, 32, biRGB),
		"8-bit":      append(header(1, 1, 8, biRGB), 0, 0, 0, 0),
		"compressed": append(header(1, 1, 32, 1), 0, 0, 0, 0),
		"huge":       header(1<<20, 1<<20, 32, biRGB),
	} {
		if _, err := dibToPNG(bad); err == nil {
			t.Errorf("%s: accepted", name)
		}
	}
}

// A small file that claims a huge canvas is refused before it is decoded.
func TestDIBRefusesHugeCanvas(t *testing.T) {
	var b bytes.Buffer
	_ = png.Encode(&b, image.NewGray(image.Rect(0, 0, 1, 1)))
	huge := append([]byte{}, b.Bytes()...)
	binary.BigEndian.PutUint32(huge[16:], 60000) // IHDR width
	binary.BigEndian.PutUint32(huge[20:], 60000) // and height
	binary.BigEndian.PutUint32(huge[29:], crc32.ChecksumIEEE(huge[12:29]))
	if cfg, err := png.DecodeConfig(bytes.NewReader(huge)); err != nil || cfg.Width != 60000 {
		t.Fatalf("the header: %v %v", cfg, err)
	}
	if _, _, err := imageToDIB(huge); err == nil {
		t.Fatal("accepted")
	}
}

func TestTrimPNG(t *testing.T) {
	padded := append(append([]byte{}, tinyPNG...), 0, 0, 0, 0, 0)
	if !bytes.Equal(trimPNG(padded), tinyPNG) {
		t.Fatal("padding after IEND stays")
	}
}

func must(b []byte, err error) []byte {
	if err != nil {
		panic(err)
	}
	return b
}
