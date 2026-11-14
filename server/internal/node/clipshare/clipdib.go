package clipshare

// Windows' clipboard bitmaps (CF_DIB: a BITMAPINFOHEADER and its pixels) to and from PNG, for clipshare_windows.go.
// Kept free of Windows APIs so it is tested everywhere.

import (
	"bytes"
	"encoding/binary"
	"errors"
	"image"
	"image/color"
	_ "image/jpeg" // clipboard.set takes JPEG too
	"image/png"
)

const (
	biRGB       = 0
	biBitfields = 3
	dibHeader   = 40 // sizeof(BITMAPINFOHEADER)
	// dibPixelsMost bounds the pixels a bitmap may claim (a corrupt header must not allocate gigabytes).
	dibPixelsMost = 64 << 20
)

var errDIB = errors.New("unsupported bitmap")

// dibToPNG turns a packed DIB (24- or 32-bit, uncompressed or bitfields; BITMAPINFOHEADER, V4 or V5) into a PNG.
func dibToPNG(dib []byte) ([]byte, error) {
	if len(dib) < dibHeader {
		return nil, errDIB
	}
	le := binary.LittleEndian
	size := int(le.Uint32(dib[0:]))
	w := int(int32(le.Uint32(dib[4:])))
	h := int(int32(le.Uint32(dib[8:])))
	bpp := int(le.Uint16(dib[14:]))
	comp := le.Uint32(dib[16:])
	if size < dibHeader || size > len(dib) || w <= 0 || h == 0 || (bpp != 24 && bpp != 32) {
		return nil, errDIB
	}
	topDown := h < 0
	if topDown {
		h = -h
	}
	if w*h > dibPixelsMost {
		return nil, errDIB
	}
	offset := size + 4*int(le.Uint32(dib[32:]))            // a color table (biClrUsed) may sit before the pixels
	masks := [3]uint32{0x00FF0000, 0x0000FF00, 0x000000FF} // R, G, B
	switch comp {
	case biRGB:
	case biBitfields:
		if bpp != 32 {
			return nil, errDIB
		}
		if size == dibHeader { // the masks follow the header
			if len(dib) < dibHeader+12 {
				return nil, errDIB
			}
			offset += 12
		}
		masks = [3]uint32{le.Uint32(dib[40:]), le.Uint32(dib[44:]), le.Uint32(dib[48:])}
	default:
		return nil, errDIB
	}
	stride := (w*bpp/8 + 3) &^ 3
	if len(dib) < offset+stride*h {
		return nil, errDIB
	}
	px := dib[offset:]
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	anyAlpha := false
	for y := range h {
		row := px[stride*y:]
		dy := h - 1 - y
		if topDown {
			dy = y
		}
		for x := range w {
			var r, g, b, a uint8
			if bpp == 24 {
				b, g, r, a = row[x*3], row[x*3+1], row[x*3+2], 255
			} else {
				v := le.Uint32(row[x*4:])
				r, g, b = maskByte(v, masks[0]), maskByte(v, masks[1]), maskByte(v, masks[2])
				a = uint8(v >> 24)
				if comp == biBitfields {
					a = 0 // the alpha mask (V4/V5) is not trusted: most apps leave it zero
				}
				anyAlpha = anyAlpha || a != 0
			}
			img.SetNRGBA(x, dy, color.NRGBA{R: r, G: g, B: b, A: a})
		}
	}
	if !anyAlpha && bpp == 32 { // 32-bit with no alpha at all is opaque, not invisible
		for i := 3; i < len(img.Pix); i += 4 {
			img.Pix[i] = 255
		}
	}
	var out bytes.Buffer
	if err := png.Encode(&out, img); err != nil {
		return nil, err
	}
	return out.Bytes(), nil
}

// maskByte is the channel of v under mask, scaled to 8 bits.
func maskByte(v, mask uint32) uint8 {
	if mask == 0 {
		return 0
	}
	shift := 0
	for mask&1 == 0 {
		mask >>= 1
		shift++
	}
	c := (v >> shift) & mask
	if mask == 0xFF {
		return uint8(c)
	}
	return uint8(uint64(c) * 255 / uint64(mask))
}

// imageToDIB decodes a PNG or JPEG and makes a 32-bit bottom-up CF_DIB of it, with the PNG to put beside it (the
// registered "PNG" format keeps transparency for the apps that read it).
func imageToDIB(data []byte) (dib, pngData []byte, err error) {
	// The size its header claims first: a small file may declare a canvas that would take gigabytes to decode.
	cfg, _, err := image.DecodeConfig(bytes.NewReader(data))
	if err != nil {
		return nil, nil, err
	}
	if cfg.Width <= 0 || cfg.Height <= 0 || cfg.Width*cfg.Height > dibPixelsMost {
		return nil, nil, errDIB
	}
	img, format, err := image.Decode(bytes.NewReader(data))
	if err != nil {
		return nil, nil, err
	}
	b := img.Bounds()
	w, h := b.Dx(), b.Dy()
	if w <= 0 || h <= 0 || w*h > dibPixelsMost {
		return nil, nil, errDIB
	}
	pngData = data
	if format != "png" {
		var buf bytes.Buffer
		if err := png.Encode(&buf, img); err != nil {
			return nil, nil, err
		}
		pngData = buf.Bytes()
	}
	dib = make([]byte, dibHeader+w*h*4)
	le := binary.LittleEndian
	le.PutUint32(dib[0:], dibHeader)
	le.PutUint32(dib[4:], uint32(w))
	le.PutUint32(dib[8:], uint32(h)) // positive: bottom-up
	le.PutUint16(dib[12:], 1)
	le.PutUint16(dib[14:], 32)
	le.PutUint32(dib[16:], biRGB)
	le.PutUint32(dib[20:], uint32(w*h*4))
	px := dib[dibHeader:]
	for y := range h {
		row := px[(h-1-y)*w*4:]
		for x := range w {
			c := color.NRGBAModel.Convert(img.At(b.Min.X+x, b.Min.Y+y)).(color.NRGBA)
			row[x*4], row[x*4+1], row[x*4+2], row[x*4+3] = c.B, c.G, c.R, c.A
		}
	}
	return dib, pngData, nil
}

// trimPNG cuts what follows a PNG's IEND chunk (a clipboard's memory block is often rounded up).
func trimPNG(b []byte) []byte {
	if i := bytes.LastIndex(b, []byte("IEND")); i >= 4 && len(b) >= i+8 {
		return b[:i+8]
	}
	return b
}
