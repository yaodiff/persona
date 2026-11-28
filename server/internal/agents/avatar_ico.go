package agents

import (
	"bytes"
	"encoding/binary"
	"errors"
	"image"
	"image/color"
	"image/png"

	"golang.org/x/image/bmp"
)

// Favicons (.ico) are a common avatar for a web-backed agent. Browsers draw them, but not every client decoder does and
// they often hold several sizes, so the hub serves the largest frame as a PNG: a PNG frame is passed through, a DIB
// frame (24 or 32 bits with its AND mask, or a paletted one) is decoded and encoded again.

var errICO = errors.New("agents: unsupported .ico avatar")

func isICOType(ctype string) bool {
	switch ctype {
	case "image/x-icon", "image/vnd.microsoft.icon", "image/ico", "image/icon":
		return true
	}
	return false
}

// looksLikeICO is the ICONDIR header: reserved 0, type 1 (icon), at least one image.
func looksLikeICO(data []byte) bool {
	return len(data) >= 6 && data[0] == 0 && data[1] == 0 && data[2] == 1 && data[3] == 0 && (data[4] != 0 || data[5] != 0)
}

// icoToPNG answers the largest frame of an .ico file as PNG bytes.
func icoToPNG(data []byte) ([]byte, error) {
	if !looksLikeICO(data) {
		return nil, errICO
	}
	count := int(binary.LittleEndian.Uint16(data[4:6]))
	if len(data) < 6+16*count {
		return nil, errICO
	}
	best, bestArea := -1, -1
	for i := range count {
		e := data[6+16*i:]
		w, h := int(e[0]), int(e[1])
		if w == 0 {
			w = 256
		}
		if h == 0 {
			h = 256
		}
		if w*h > bestArea {
			best, bestArea = i, w*h
		}
	}
	e := data[6+16*best:]
	size := int(binary.LittleEndian.Uint32(e[8:12]))
	off := int(binary.LittleEndian.Uint32(e[12:16]))
	if size <= 0 || off < 0 || off > len(data) || size > len(data)-off {
		return nil, errICO
	}
	frame := data[off : off+size]
	if bytes.HasPrefix(frame, []byte("\x89PNG\r\n\x1a\n")) {
		return frame, nil
	}
	img, err := decodeICODIB(frame)
	if err != nil {
		return nil, err
	}
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// decodeICODIB decodes one DIB frame: a BITMAPINFOHEADER whose height counts the XOR image and the AND mask together.
func decodeICODIB(dib []byte) (image.Image, error) {
	if len(dib) < 40 {
		return nil, errICO
	}
	hdr := int(binary.LittleEndian.Uint32(dib[0:4]))
	w := int(int32(binary.LittleEndian.Uint32(dib[4:8])))
	h := int(int32(binary.LittleEndian.Uint32(dib[8:12]))) / 2
	bpp := int(binary.LittleEndian.Uint16(dib[14:16]))
	compression := binary.LittleEndian.Uint32(dib[16:20])
	if hdr < 40 || hdr > len(dib) || w <= 0 || h <= 0 || w > 1024 || h > 1024 || compression != 0 {
		return nil, errICO
	}
	if bpp != 24 && bpp != 32 {
		return decodePalettedDIB(dib, hdr, h)
	}
	stride := (w*bpp/8 + 3) &^ 3
	maskStride := ((w + 31) / 32) * 4
	pix := dib[hdr:]
	if len(pix) < stride*h {
		return nil, errICO
	}
	mask := pix[stride*h:]
	hasMask := len(mask) >= maskStride*h
	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	anyAlpha := false
	if bpp == 32 {
		for i := 3; i < stride*h; i += 4 {
			if pix[i] != 0 {
				anyAlpha = true
				break
			}
		}
	}
	for y := range h {
		row := pix[(h-1-y)*stride:] // bottom-up
		for x := range w {
			var c color.NRGBA
			if bpp == 32 {
				p := row[x*4:]
				c = color.NRGBA{R: p[2], G: p[1], B: p[0], A: p[3]}
				if !anyAlpha {
					c.A = 0xFF
				}
			} else {
				p := row[x*3:]
				c = color.NRGBA{R: p[2], G: p[1], B: p[0], A: 0xFF}
			}
			if hasMask && (bpp == 24 || !anyAlpha) {
				m := mask[(h-1-y)*maskStride:]
				if m[x/8]&(0x80>>(x%8)) != 0 {
					c.A = 0
				}
			}
			img.SetNRGBA(x, y, c)
		}
	}
	return img, nil
}

// decodePalettedDIB lends a paletted frame a BMP file header (with the height halved, so the mask is left out) and
// lets the bmp package read it; such old icons are opaque.
func decodePalettedDIB(dib []byte, hdr, h int) (image.Image, error) {
	frame := append([]byte(nil), dib...)
	binary.LittleEndian.PutUint32(frame[8:12], uint32(h))
	bpp := int(binary.LittleEndian.Uint16(frame[14:16]))
	colors := int(binary.LittleEndian.Uint32(frame[32:36]))
	if colors == 0 && bpp <= 8 {
		colors = 1 << bpp
	}
	file := make([]byte, 14, 14+len(frame))
	file[0], file[1] = 'B', 'M'
	binary.LittleEndian.PutUint32(file[2:6], uint32(14+len(frame)))
	binary.LittleEndian.PutUint32(file[10:14], uint32(14+hdr+colors*4))
	img, err := bmp.Decode(bytes.NewReader(append(file, frame...)))
	if err != nil {
		return nil, errICO
	}
	return img, nil
}
