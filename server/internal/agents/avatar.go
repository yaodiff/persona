package agents

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"
	"image"
	"image/png"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/srwiley/oksvg"
	"github.com/srwiley/rasterx"
)

// Avatars come from the Lark gateway's feed as data: URLs (Grok Bot's generated one-path SVGs, Muse's JPEGs) or https URLs.
// The list carries only hasAvatar + avatarVersion; GET /api/v1/agents/sessions/{id}/avatar serves the image. SVGs are
// rasterized here to PNG because iOS cannot draw SVG data with public APIs; .ico favicons become PNG too (avatar_ico.go).

const (
	avatarMaxBytes = 2 << 20
	avatarPixels   = 128
)

// Avatar is a decoded session avatar, ready to serve.
type Avatar struct {
	Data        []byte
	ContentType string
	Version     string
}

func avatarVersion(u string) string {
	sum := sha256.Sum256([]byte(u))
	return hex.EncodeToString(sum[:6])
}

func withAvatar(s Session, avatarURL string) Session {
	if avatarURL == "" {
		return s
	}
	s.AvatarURL, s.HasAvatar, s.AvatarVersion = avatarURL, true, avatarVersion(avatarURL)
	return s
}

var avatarHTTP = &http.Client{Timeout: 10 * time.Second}

// decodeAvatar turns an avatar URL into image bytes: data: URLs are decoded, https URLs fetched (size-limited), and
// SVG and .ico favicons become PNG.
func decodeAvatar(ctx context.Context, raw string) (*Avatar, error) {
	var data []byte
	var ctype string
	switch {
	case strings.HasPrefix(raw, "data:"):
		meta, payload, ok := strings.Cut(raw[len("data:"):], ",")
		if !ok {
			return nil, fmt.Errorf("agents: malformed data URL")
		}
		parts := strings.Split(meta, ";")
		ctype = parts[0]
		if parts[len(parts)-1] == "base64" {
			b, err := base64.StdEncoding.DecodeString(payload)
			if err != nil {
				if b, err = base64.RawStdEncoding.DecodeString(strings.TrimRight(payload, "=")); err != nil {
					return nil, fmt.Errorf("agents: avatar base64: %w", err)
				}
			}
			data = b
		} else {
			s, err := url.PathUnescape(payload)
			if err != nil {
				return nil, err
			}
			data = []byte(s)
		}
	case strings.HasPrefix(raw, "https://"):
		req, err := http.NewRequestWithContext(ctx, http.MethodGet, raw, nil)
		if err != nil {
			return nil, err
		}
		resp, err := avatarHTTP.Do(req)
		if err != nil {
			return nil, err
		}
		defer resp.Body.Close()
		if resp.StatusCode != http.StatusOK {
			return nil, fmt.Errorf("agents: avatar fetch HTTP %d", resp.StatusCode)
		}
		data, err = io.ReadAll(io.LimitReader(resp.Body, avatarMaxBytes+1))
		if err != nil {
			return nil, err
		}
		ctype = strings.TrimSpace(strings.Split(resp.Header.Get("Content-Type"), ";")[0])
		if ctype == "" || ctype == "application/octet-stream" {
			ctype = http.DetectContentType(data)
		}
	default:
		return nil, fmt.Errorf("agents: unsupported avatar URL")
	}
	if len(data) > avatarMaxBytes {
		return nil, fmt.Errorf("agents: avatar too large")
	}
	if ctype == "image/svg+xml" || bytes.HasPrefix(bytes.TrimSpace(data), []byte("<svg")) {
		png, err := rasterizeSVG(data)
		if err != nil {
			return nil, err
		}
		data, ctype = png, "image/png"
	}
	if isICOType(ctype) || looksLikeICO(data) {
		png, err := icoToPNG(data)
		if err != nil {
			return nil, err
		}
		data, ctype = png, "image/png"
	}
	if !strings.HasPrefix(ctype, "image/") {
		return nil, fmt.Errorf("agents: avatar is %q, not an image", ctype)
	}
	return &Avatar{Data: data, ContentType: ctype, Version: avatarVersion(raw)}, nil
}

func rasterizeSVG(data []byte) (out []byte, err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("agents: svg: %v", r)
		}
	}()
	icon, err := oksvg.ReadIconStream(bytes.NewReader(data), oksvg.IgnoreErrorMode)
	if err != nil {
		return nil, err
	}
	icon.SetTarget(0, 0, avatarPixels, avatarPixels)
	img := image.NewRGBA(image.Rect(0, 0, avatarPixels, avatarPixels))
	sc := rasterx.NewScannerGV(avatarPixels, avatarPixels, img, img.Bounds())
	icon.Draw(rasterx.NewDasher(avatarPixels, avatarPixels, sc), 1)
	var buf bytes.Buffer
	if err := png.Encode(&buf, img); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}
