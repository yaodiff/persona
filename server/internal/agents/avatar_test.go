package agents

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"image"
	"image/png"
	"net/http"
	"net/http/httptest"
	"testing"
)

const svgCircle = `<svg xmlns="http://www.w3.org/2000/svg" viewBox="0 0 100 100"><path d="M50 8a42 42 0 1 1 0 84a42 42 0 0 1 0-84z" fill="#FF309B"/></svg>`

func TestAvatarMergedFromFeedAndRasterized(t *testing.T) {
	svgURL := "data:image/svg+xml;base64," + base64.StdEncoding.EncodeToString([]byte(svgCircle))
	f := &fakeBridge{
		sessions: []AgentSession{{SessionKey: "agent:grok_bot:a:1"}, {SessionKey: "agent:grok_bot:a:2"}},
		avatars:  map[string]string{"agent:grok_bot:a:1": svgURL},
	}
	s := NewService(nil, nil, nil, testBridge{API: f})
	ctx := context.Background()
	list, err := s.Sessions(ctx, "", "")
	if err != nil || len(list) != 2 {
		t.Fatalf("list %v %v", list, err)
	}
	if !list[0].HasAvatar || len(list[0].AvatarVersion) != 12 || list[1].HasAvatar {
		t.Fatalf("avatar flags %+v %+v", list[0], list[1])
	}
	b, _ := json.Marshal(list[0])
	if bytes.Contains(b, []byte("data:")) || !bytes.Contains(b, []byte(`"hasAvatar":true`)) {
		t.Fatalf("list JSON leaks the URL or lacks the flag: %s", b)
	}
	a, err := s.Avatar(ctx, list[0].ID)
	if err != nil || a.ContentType != "image/png" || a.Version != list[0].AvatarVersion {
		t.Fatalf("avatar %v %v", a, err)
	}
	img, err := png.Decode(bytes.NewReader(a.Data))
	if err != nil {
		t.Fatal(err)
	}
	if r, _, _, al := img.At(64, 64).RGBA(); al == 0 || r>>8 != 0xFF {
		t.Fatalf("center pixel not the fill: r=%x a=%x", r>>8, al>>8)
	}
	if _, _, _, al := img.At(1, 1).RGBA(); al != 0 {
		t.Fatalf("corner should be transparent")
	}
	if _, err := s.Avatar(ctx, list[1].ID); HTTPStatus(err) != http.StatusNotFound {
		t.Fatalf("no avatar: %v", err)
	}
}

func TestDecodeAvatarJPEGDataURL(t *testing.T) {
	a, err := decodeAvatar(context.Background(), "data:image/jpeg;base64,"+base64.StdEncoding.EncodeToString([]byte{0xff, 0xd8, 0xff, 0xe0}))
	if err != nil || a.ContentType != "image/jpeg" || len(a.Data) != 4 {
		t.Fatalf("%v %v", a, err)
	}
	if _, err := decodeAvatar(context.Background(), "http://insecure/x.png"); err == nil {
		t.Fatal("plain http accepted")
	}
}

// icoFile builds an .ico with one frame of the given bytes (w×h as the directory states it).
func icoFile(w, h byte, frame []byte) []byte {
	b := []byte{0, 0, 1, 0, 1, 0, w, h, 0, 0, 1, 0, 32, 0}
	b = binary.LittleEndian.AppendUint32(b, uint32(len(frame)))
	b = binary.LittleEndian.AppendUint32(b, 22)
	return append(b, frame...)
}

// dib32 is a 2×2 32-bit DIB frame: top-left red opaque, the rest transparent, with an AND mask.
func dib32() []byte {
	h := make([]byte, 40)
	binary.LittleEndian.PutUint32(h[0:4], 40)
	binary.LittleEndian.PutUint32(h[4:8], 2)
	binary.LittleEndian.PutUint32(h[8:12], 4) // XOR + AND
	binary.LittleEndian.PutUint16(h[12:14], 1)
	binary.LittleEndian.PutUint16(h[14:16], 32)
	// bottom-up rows of BGRA: bottom row transparent, top row red then transparent
	px := []byte{0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0xFF, 0xFF, 0, 0, 0, 0}
	mask := make([]byte, 8)
	return append(append(h, px...), mask...)
}

// .ico favicons (a web-backed agent's usual avatar) reach every client as PNG: a PNG frame as it is, a DIB frame
// decoded with its alpha; a broken one is refused rather than served as an image no client can draw.
func TestDecodeAvatarICO(t *testing.T) {
	ctx := context.Background()
	a, err := decodeAvatar(ctx, "data:image/x-icon;base64,"+base64.StdEncoding.EncodeToString(icoFile(2, 2, dib32())))
	if err != nil || a.ContentType != "image/png" {
		t.Fatalf("DIB ico: %v %v", a, err)
	}
	img, err := png.Decode(bytes.NewReader(a.Data))
	if err != nil || img.Bounds().Dx() != 2 || img.Bounds().Dy() != 2 {
		t.Fatalf("decoded %v %v", img, err)
	}
	if r, _, _, al := img.At(0, 0).RGBA(); r>>8 != 0xFF || al>>8 != 0xFF {
		t.Fatalf("top-left should be opaque red: r=%x a=%x", r>>8, al>>8)
	}
	if _, _, _, al := img.At(1, 1).RGBA(); al != 0 {
		t.Fatal("bottom-right should be transparent")
	}

	var pngBuf bytes.Buffer
	_ = png.Encode(&pngBuf, image.NewNRGBA(image.Rect(0, 0, 3, 3)))
	a, err = decodeAvatar(ctx, "data:image/vnd.microsoft.icon;base64,"+base64.StdEncoding.EncodeToString(icoFile(3, 3, pngBuf.Bytes())))
	if err != nil || a.ContentType != "image/png" || !bytes.Equal(a.Data, pngBuf.Bytes()) {
		t.Fatalf("PNG frame passes through: %v %v", a, err)
	}

	// sniffed by its header even when the type says nothing useful
	srv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/octet-stream")
		_, _ = w.Write(icoFile(2, 2, dib32()))
	}))
	defer srv.Close()
	old := avatarHTTP
	avatarHTTP = srv.Client()
	defer func() { avatarHTTP = old }()
	if a, err := decodeAvatar(ctx, srv.URL+"/favicon.ico"); err != nil || a.ContentType != "image/png" {
		t.Fatalf("fetched ico: %v %v", a, err)
	}

	if _, err := decodeAvatar(ctx, "data:image/x-icon;base64,"+base64.StdEncoding.EncodeToString([]byte{0, 0, 1, 0, 1, 0, 9})); err == nil {
		t.Fatal("a truncated ico was accepted")
	}
}
