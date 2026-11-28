package extension

import (
	"archive/tar"
	"archive/zip"
	"bytes"
	"compress/gzip"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"image"
	"image/png"
	"io"
	"math"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"
)

func TestBudgetRejectsNegativeAndOverflowingSizes(t *testing.T) {
	b := &budget{maxBytes: 1024, maxFiles: 10}
	if err := b.take(-1); err == nil {
		t.Fatal("budget accepted negative size")
	}
	if err := b.take(math.MaxInt64); err == nil {
		t.Fatal("budget accepted MaxInt64 overflow")
	}
	if _, err := b.takeUint64(math.MaxUint64); err == nil {
		t.Fatal("budget accepted MaxUint64 sign-overflow size")
	}
	if _, err := b.takeUint64(1 << 63); err == nil {
		t.Fatal("budget accepted 1<<63 sign-overflow size")
	}
	if b.bytes != 0 || b.files != 0 {
		t.Fatalf("failed take calls mutated budget: bytes=%d files=%d", b.bytes, b.files)
	}
	if err := b.take(600); err != nil {
		t.Fatalf("take(600): %v", err)
	}
	if err := b.take(500); err == nil {
		t.Fatal("budget accepted cumulative bytes past maxBytes")
	}
}

// A zip64 entry claiming UncompressedSize64 >= 1<<63 used to cast to a negative int64,
// shrinking budget.bytes and disabling copyOut's io.LimitReader completely.
func TestUnzipRejectsZip64SignOverflowBomb(t *testing.T) {
	var raw bytes.Buffer
	zw := zip.NewWriter(&raw)
	fw, err := zw.Create("bomb.txt")
	if err != nil {
		t.Fatal(err)
	}
	payload := bytes.Repeat([]byte("A"), 4096)
	if _, err := fw.Write(payload); err != nil {
		t.Fatal(err)
	}
	if err := zw.Close(); err != nil {
		t.Fatal(err)
	}

	zipBytes := patchZip64UncompressedSize(t, raw.Bytes(), math.MaxUint64)
	archivePath := filepath.Join(t.TempDir(), "bomb.zip")
	if err := os.WriteFile(archivePath, zipBytes, 0o600); err != nil {
		t.Fatal(err)
	}
	zr, err := zip.OpenReader(archivePath)
	if err != nil {
		t.Fatalf("patched zip failed to open: %v", err)
	}
	if len(zr.File) != 1 || zr.File[0].UncompressedSize64 != math.MaxUint64 {
		zr.Close()
		t.Fatalf("patched UncompressedSize64 = %#x, want MaxUint64", zr.File[0].UncompressedSize64)
	}
	zr.Close()

	to := filepath.Join(t.TempDir(), "out")
	if err := unzipIn(archivePath, to, &budget{maxBytes: 1024, maxFiles: 10}); err == nil {
		t.Fatal("unzipIn accepted zip64 entry with UncompressedSize64 = MaxUint64")
	}
	if _, err := os.Stat(filepath.Join(to, "bomb.txt")); !os.IsNotExist(err) {
		t.Fatalf("bomb.txt should not have been extracted, stat err = %v", err)
	}
}

func patchZip64UncompressedSize(t *testing.T, src []byte, forged uint64) []byte {
	t.Helper()
	cdSig := []byte{0x50, 0x4b, 0x01, 0x02}
	eocdSig := []byte{0x50, 0x4b, 0x05, 0x06}
	cdOff := bytes.Index(src, cdSig)
	eocdOff := bytes.LastIndex(src, eocdSig)
	if cdOff < 0 || eocdOff < 0 || eocdOff < cdOff {
		t.Fatal("unexpected zip layout")
	}
	nameLen := int(binary.LittleEndian.Uint16(src[cdOff+28 : cdOff+30]))
	extraLen := int(binary.LittleEndian.Uint16(src[cdOff+30 : cdOff+32]))
	commentLen := int(binary.LittleEndian.Uint16(src[cdOff+32 : cdOff+34]))
	cdEntryEnd := cdOff + 46 + nameLen + extraLen + commentLen
	if cdEntryEnd != eocdOff {
		t.Fatalf("unexpected single-entry CD size: entryEnd=%d eocdOff=%d", cdEntryEnd, eocdOff)
	}
	z64 := make([]byte, 12)
	binary.LittleEndian.PutUint16(z64[0:2], 0x0001)
	binary.LittleEndian.PutUint16(z64[2:4], 8)
	binary.LittleEndian.PutUint64(z64[4:12], forged)

	var out bytes.Buffer
	out.Write(src[:cdOff])
	cdHdr := append([]byte(nil), src[cdOff:cdOff+46]...)
	binary.LittleEndian.PutUint32(cdHdr[24:28], 0xffffffff)
	binary.LittleEndian.PutUint16(cdHdr[30:32], uint16(extraLen+len(z64)))
	out.Write(cdHdr)
	out.Write(src[cdOff+46 : cdOff+46+nameLen+extraLen])
	out.Write(z64)
	out.Write(src[cdOff+46+nameLen+extraLen : cdEntryEnd])

	eocd := append([]byte(nil), src[eocdOff:]...)
	oldCDSize := binary.LittleEndian.Uint32(eocd[12:16])
	binary.LittleEndian.PutUint32(eocd[12:16], oldCDSize+uint32(len(z64)))
	out.Write(eocd)
	return out.Bytes()
}

func TestUnpackCountsDirectoriesSymlinksAndSkippedEntries(t *testing.T) {
	t.Run("tar dirs exceed maxFiles", func(t *testing.T) {
		var buf bytes.Buffer
		gz := gzip.NewWriter(&buf)
		tw := tar.NewWriter(gz)
		for i := range 6 {
			if err := tw.WriteHeader(&tar.Header{Name: fmt.Sprintf("d%d/", i), Typeflag: tar.TypeDir, Mode: 0o755}); err != nil {
				t.Fatal(err)
			}
		}
		_ = tw.Close()
		_ = gz.Close()
		p := filepath.Join(t.TempDir(), "dirs.tar.gz")
		if err := os.WriteFile(p, buf.Bytes(), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := untarIn(p, filepath.Join(t.TempDir(), "out"), &budget{maxBytes: 1 << 20, maxFiles: 3}); err == nil {
			t.Fatal("untarIn allowed directory entries past maxFiles budget")
		}
	})

	t.Run("tar symlinks exceed maxFiles", func(t *testing.T) {
		var buf bytes.Buffer
		gz := gzip.NewWriter(&buf)
		tw := tar.NewWriter(gz)
		for i := range 6 {
			if err := tw.WriteHeader(&tar.Header{Name: fmt.Sprintf("l%d", i), Typeflag: tar.TypeSymlink, Linkname: "target"}); err != nil {
				t.Fatal(err)
			}
		}
		_ = tw.Close()
		_ = gz.Close()
		p := filepath.Join(t.TempDir(), "links.tar.gz")
		if err := os.WriteFile(p, buf.Bytes(), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := untarIn(p, filepath.Join(t.TempDir(), "out"), &budget{maxBytes: 1 << 20, maxFiles: 3}); err == nil {
			t.Fatal("untarIn allowed symlink entries past maxFiles budget")
		}
	})

	t.Run("tar __MACOSX payload bomb blocked by budget", func(t *testing.T) {
		var buf bytes.Buffer
		gz := gzip.NewWriter(&buf)
		tw := tar.NewWriter(gz)
		payload := bytes.Repeat([]byte("M"), 4096)
		if err := tw.WriteHeader(&tar.Header{Name: "__MACOSX/bomb", Typeflag: tar.TypeReg, Mode: 0o644, Size: int64(len(payload))}); err != nil {
			t.Fatal(err)
		}
		_, _ = tw.Write(payload)
		_ = tw.Close()
		_ = gz.Close()
		p := filepath.Join(t.TempDir(), "macbomb.tar.gz")
		if err := os.WriteFile(p, buf.Bytes(), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := untarIn(p, filepath.Join(t.TempDir(), "out"), &budget{maxBytes: 1024, maxFiles: 10}); err == nil {
			t.Fatal("untarIn allowed oversized __MACOSX entry past byte budget")
		}
	})

	t.Run("tar dot masquerading as regular file rejected", func(t *testing.T) {
		for _, badName := range []string{".", "./"} {
			var buf bytes.Buffer
			gz := gzip.NewWriter(&buf)
			payload := bytes.Repeat([]byte("D"), 2048)
			writeRawTarEntry(t, gz, badName, tar.TypeReg, payload)
			_, _ = gz.Write(make([]byte, 1024)) // EOF blocks
			_ = gz.Close()
			p := filepath.Join(t.TempDir(), "dotfile.tar.gz")
			if err := os.WriteFile(p, buf.Bytes(), 0o600); err != nil {
				t.Fatal(err)
			}
			if err := untarIn(p, filepath.Join(t.TempDir(), "out"), &budget{maxBytes: 1 << 20, maxFiles: 10}); err == nil {
				t.Fatalf("untarIn allowed %q masquerading as a regular file", badName)
			}
		}
	})

	t.Run("tar many skipped entries exceed maxFiles", func(t *testing.T) {
		var buf bytes.Buffer
		gz := gzip.NewWriter(&buf)
		tw := tar.NewWriter(gz)
		for i := range 8 {
			if err := tw.WriteHeader(&tar.Header{Name: fmt.Sprintf("__MACOSX/f%d", i), Typeflag: tar.TypeReg, Mode: 0o644, Size: 0}); err != nil {
				t.Fatal(err)
			}
		}
		_ = tw.Close()
		_ = gz.Close()
		p := filepath.Join(t.TempDir(), "many-skipped.tar.gz")
		if err := os.WriteFile(p, buf.Bytes(), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := untarIn(p, filepath.Join(t.TempDir(), "out"), &budget{maxBytes: 1 << 20, maxFiles: 3}); err == nil {
			t.Fatal("untarIn allowed skipped __MACOSX entries past maxFiles budget")
		}
	})

	t.Run("tar oversized stream blocked before EOF", func(t *testing.T) {
		var buf bytes.Buffer
		gz := gzip.NewWriter(&buf)
		// Multiple consecutive raw PAX extended headers (>1.5 MiB total) before any regular entry
		// are consumed internally by tar.Reader.Next() and must trip boundedReader's errStreamLimit.
		paxLine := []byte("20 comment=12345678\n") // 20 bytes including length prefix and newline
		paxBlock := bytes.Repeat(paxLine, 20000)   // 400 KiB per PAX header (< archive/tar's 1 MiB single-block cap)
		for range 5 {
			writeRawTarEntry(t, gz, "PaxHeaders/x", tar.TypeXHeader, paxBlock)
		}
		writeRawTarEntry(t, gz, "tiny.txt", tar.TypeReg, []byte("x"))
		_, _ = gz.Write(make([]byte, 1024))
		_ = gz.Close()
		p := filepath.Join(t.TempDir(), "paxbomb.tar.gz")
		if err := os.WriteFile(p, buf.Bytes(), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := untarIn(p, filepath.Join(t.TempDir(), "out"), &budget{maxBytes: 1024, maxFiles: 5}); !errors.Is(err, errStreamLimit) {
			t.Fatalf("untarIn err = %v, want errStreamLimit", err)
		}
	})

	t.Run("zip dirs, skipped entries, and root slash rejected", func(t *testing.T) {
		var buf bytes.Buffer
		zw := zip.NewWriter(&buf)
		for i := range 6 {
			if _, err := zw.Create(fmt.Sprintf("d%d/", i)); err != nil {
				t.Fatal(err)
			}
		}
		_ = zw.Close()
		p := filepath.Join(t.TempDir(), "dirs.zip")
		if err := os.WriteFile(p, buf.Bytes(), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := unzipIn(p, filepath.Join(t.TempDir(), "out"), &budget{maxBytes: 1 << 20, maxFiles: 3}); err == nil {
			t.Fatal("unzipIn allowed directory entries past maxFiles budget")
		}

		var macBuf bytes.Buffer
		mzw := zip.NewWriter(&macBuf)
		for i := range 6 {
			if _, err := mzw.Create(fmt.Sprintf("__MACOSX/f%d", i)); err != nil {
				t.Fatal(err)
			}
		}
		_ = mzw.Close()
		mp := filepath.Join(t.TempDir(), "mac.zip")
		if err := os.WriteFile(mp, macBuf.Bytes(), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := unzipIn(mp, filepath.Join(t.TempDir(), "out2"), &budget{maxBytes: 1 << 20, maxFiles: 3}); err == nil {
			t.Fatal("unzipIn allowed skipped __MACOSX entries past maxFiles budget")
		}

		var slashBuf bytes.Buffer
		szw := zip.NewWriter(&slashBuf)
		if _, err := szw.Create("/"); err != nil {
			t.Fatal(err)
		}
		_ = szw.Close()
		sp := filepath.Join(t.TempDir(), "slash.zip")
		if err := os.WriteFile(sp, slashBuf.Bytes(), 0o600); err != nil {
			t.Fatal(err)
		}
		if err := unzipIn(sp, filepath.Join(t.TempDir(), "out3"), &budget{maxBytes: 1 << 20, maxFiles: 10}); err == nil {
			t.Fatal("unzipIn silently accepted absolute '/' entry")
		}
	})
}

func TestCopyTreeAndPackageRootRefuseEscapingSymlinks(t *testing.T) {
	t.Run("copyTree symlink chain", func(t *testing.T) {
		src := t.TempDir()
		if err := os.Symlink(".", filepath.Join(src, "a")); err != nil {
			t.Fatal(err)
		}
		if err := os.Symlink("a/..", filepath.Join(src, "d")); err != nil {
			t.Fatal(err)
		}
		to := filepath.Join(t.TempDir(), "stage", "pkg")
		if err := copyTree(src, to); err == nil {
			t.Fatal("copyTree allowed symlink chain escaping package root")
		}
	})

	t.Run("packageRoot stripped top-level dir rejects escaping symlink", func(t *testing.T) {
		var buf bytes.Buffer
		gz := gzip.NewWriter(&buf)
		tw := tar.NewWriter(gz)
		manifest := []byte(`{"id":"demo","version":"1.0.0"}`)
		_ = tw.WriteHeader(&tar.Header{Name: "myext/", Typeflag: tar.TypeDir, Mode: 0o755})
		_ = tw.WriteHeader(&tar.Header{Name: "myext/" + ManifestFile, Typeflag: tar.TypeReg, Mode: 0o644, Size: int64(len(manifest))})
		_, _ = tw.Write(manifest)
		// Valid inside the outer unpack dir, but escapes once packageRoot strips "myext/".
		_ = tw.WriteHeader(&tar.Header{Name: "myext/escape", Typeflag: tar.TypeSymlink, Linkname: ".."})
		_ = tw.Close()
		_ = gz.Close()

		archive := filepath.Join(t.TempDir(), "escape.tar.gz")
		if err := os.WriteFile(archive, buf.Bytes(), 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := Stage(context.Background(), Source{Kind: "file", Path: archive}, filepath.Join(t.TempDir(), "stage")); err == nil {
			t.Fatal("Stage accepted symlink escaping stripped packageRoot directory")
		}
	})
}

func TestPackageIconAndFingerprintAcceptRootDotEntriesAndSymlinks(t *testing.T) {
	// PackageIcon must still accept tar archives starting with a standard "./" root directory entry.
	var iconPNG bytes.Buffer
	img := image.NewRGBA(image.Rect(0, 0, IconMin, IconMin))
	if err := png.Encode(&iconPNG, img); err != nil {
		t.Fatal(err)
	}
	var tarBuf bytes.Buffer
	gz := gzip.NewWriter(&tarBuf)
	tw := tar.NewWriter(gz)
	_ = tw.WriteHeader(&tar.Header{Name: "./", Typeflag: tar.TypeDir, Mode: 0o755})
	_ = tw.WriteHeader(&tar.Header{Name: "./assets/", Typeflag: tar.TypeDir, Mode: 0o755})
	_ = tw.WriteHeader(&tar.Header{Name: "./" + IconPath, Typeflag: tar.TypeReg, Mode: 0o644, Size: int64(iconPNG.Len())})
	_, _ = tw.Write(iconPNG.Bytes())
	_ = tw.Close()
	_ = gz.Close()

	tarPath := filepath.Join(t.TempDir(), "pkg.tar.gz")
	if err := os.WriteFile(tarPath, tarBuf.Bytes(), 0o600); err != nil {
		t.Fatal(err)
	}
	gotIcon, err := PackageIcon(tarPath, "pkg.tar.gz", "demo")
	if err != nil || !bytes.Equal(gotIcon, iconPNG.Bytes()) {
		t.Fatalf("PackageIcon with leading './' failed: len=%d err=%v", len(gotIcon), err)
	}

	// packageFingerprint must allow symlinks resolving to the package root "." (matching linkInside).
	pkgDir := t.TempDir()
	if err := os.MkdirAll(filepath.Join(pkgDir, "sub"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(".", filepath.Join(pkgDir, "self")); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("..", filepath.Join(pkgDir, "sub", "root")); err != nil {
		t.Fatal(err)
	}
	if fp, err := packageFingerprint(pkgDir); err != nil || fp == "" {
		t.Fatalf("packageFingerprint rejected in-package root symlinks: fp=%q err=%v", fp, err)
	}
}

func TestInsideAndLinkInsideRejectTraversalEdgeCases(t *testing.T) {
	for _, bad := range []string{"", ".", "..", "../a", "/abs", "a/../b", `a\..\b`, "a\x00b"} {
		if inside(bad) {
			t.Errorf("inside(%q) = true, want false", bad)
		}
	}
	for _, bad := range []struct{ rel, target string }{
		{"a", ""},
		{"a", "/etc/passwd"},
		{"a", `..\outside`},
		{"a", "b\x00c"},
		{"sub/a", "../../outside"},
	} {
		if linkInside(bad.rel, bad.target) {
			t.Errorf("linkInside(%q, %q) = true, want false", bad.rel, bad.target)
		}
	}
	if !linkInside("a", ".") || !linkInside("lib/up", "../cur/real.txt") {
		t.Error("linkInside rejected valid in-package relative links")
	}
}

func TestUntarDetectsCorruptedGzipFooter(t *testing.T) {
	var buf bytes.Buffer
	gz := gzip.NewWriter(&buf)
	tw := tar.NewWriter(gz)
	body := []byte("hello")
	_ = tw.WriteHeader(&tar.Header{Name: "hello.txt", Typeflag: tar.TypeReg, Mode: 0o644, Size: int64(len(body))})
	_, _ = tw.Write(body)
	_ = tw.Close()
	_ = gz.Close()

	corrupted := append([]byte(nil), buf.Bytes()...)
	corrupted[len(corrupted)-2] ^= 0xff
	p := filepath.Join(t.TempDir(), "bad-crc.tar.gz")
	if err := os.WriteFile(p, corrupted, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := untar(p, filepath.Join(t.TempDir(), "out")); err == nil {
		t.Fatal("untar ignored corrupted gzip CRC/ISIZE trailer")
	}
}

func TestFetchRedirectsSchemesHeadersAndBodyCap(t *testing.T) {
	var tlsURL string
	plain := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/to-https":
			http.Redirect(w, r, tlsURL+"/downgrade", http.StatusFound)
		default:
			_, _ = io.WriteString(w, "downgraded")
		}
	}))
	defer plain.Close()

	tlsSrv := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/downgrade":
			http.Redirect(w, r, plain.URL+"/payload", http.StatusFound)
		case "/userinfo":
			http.Redirect(w, r, "https://user:pass@example.invalid/payload", http.StatusFound)
		case "/file":
			http.Redirect(w, r, "file:///etc/passwd", http.StatusFound)
		case "/loop":
			http.Redirect(w, r, "/loop", http.StatusFound)
		case "/big":
			_, _ = w.Write(bytes.Repeat([]byte("B"), 512))
		default:
			_, _ = io.WriteString(w, "ok")
		}
	}))
	defer tlsSrv.Close()
	tlsURL = tlsSrv.URL

	origTransport := fetchTransport
	fetchTransport = tlsSrv.Client().Transport
	defer func() { fetchTransport = origTransport }()

	for _, u := range []string{
		tlsSrv.URL + "/downgrade",
		tlsSrv.URL + "/userinfo",
		tlsSrv.URL + "/file",
		tlsSrv.URL + "/loop",
		plain.URL + "/to-https", // two-hop http -> https -> http downgrade
		"https://user:pass@example.invalid/pkg.tar.gz",
		"file:///etc/passwd",
	} {
		var out bytes.Buffer
		if err := Fetch(context.Background(), u, &out); err == nil {
			t.Errorf("Fetch(%s) succeeded, want refusal", u)
		}
	}

	// Response body limit enforces fail-closed cap.
	origLimit := fetchBodyLimit
	fetchBodyLimit = 256
	defer func() { fetchBodyLimit = origLimit }()
	if err := Fetch(context.Background(), tlsSrv.URL+"/big", io.Discard); err == nil {
		t.Fatal("Fetch accepted response body exceeding limit")
	}

	// Cross-origin redirect strips Authorization, Cookie, and Proxy-Authorization; sameOrigin normalizes default ports.
	if !sameOrigin("http://example.invalid/a", "http://example.invalid:80/b") {
		t.Fatal("sameOrigin did not normalize default HTTP port 80")
	}
	if !sameOrigin("https://example.invalid/a", "https://example.invalid:443/b") {
		t.Fatal("sameOrigin did not normalize default HTTPS port 443")
	}
	if sameOrigin("https://example.invalid:443/a", "https://example.invalid:8443/a") {
		t.Fatal("sameOrigin matched different HTTPS ports")
	}
	origURL, _ := url.Parse("https://origin.invalid/a")
	check := fetchCheckRedirect(origURL, origURL.String())
	nextReq, _ := http.NewRequest(http.MethodGet, "https://other.invalid/b", nil)
	nextReq.Header.Set("Authorization", "Bearer secret")
	nextReq.Header.Set("Cookie", "sid=1")
	nextReq.Header.Set("Proxy-Authorization", "Basic secret")
	if err := check(nextReq, []*http.Request{nextReq}); err != nil {
		t.Fatalf("valid cross-origin https redirect refused: %v", err)
	}
	for _, hdr := range []string{"Authorization", "Cookie", "Proxy-Authorization"} {
		if got := nextReq.Header.Get(hdr); got != "" {
			t.Errorf("cross-origin redirect kept %s header: %q", hdr, got)
		}
	}

	// Stage https error cleans up its temporary download file.
	stageDir := filepath.Join(t.TempDir(), "stage")
	_, err := Stage(context.Background(), Source{Kind: "https", Path: tlsSrv.URL + "/ok", SHA256: strings.Repeat("0", 64)}, stageDir)
	if err == nil {
		t.Fatal("Stage accepted wrong sha256")
	}
	if _, statErr := os.Stat(filepath.Join(stageDir, "download")); !os.IsNotExist(statErr) {
		t.Fatalf("Stage left temporary download file behind after error: %v", statErr)
	}
}

func writeRawTarEntry(t *testing.T, w io.Writer, name string, typeflag byte, body []byte) {
	t.Helper()
	var hdr [512]byte
	copy(hdr[0:100], name)
	copy(hdr[100:108], "0000644\x00")
	copy(hdr[108:116], "0000000\x00")
	copy(hdr[116:124], "0000000\x00")
	copy(hdr[124:136], fmt.Sprintf("%011o\x00", len(body)))
	copy(hdr[136:148], "00000000000\x00")
	for i := 148; i < 156; i++ {
		hdr[i] = ' '
	}
	hdr[156] = typeflag
	copy(hdr[257:263], "ustar\x00")
	copy(hdr[263:265], "00")
	var sum int64
	for _, b := range hdr {
		sum += int64(b)
	}
	copy(hdr[148:156], fmt.Sprintf("%06o\x00 ", sum))
	if _, err := w.Write(hdr[:]); err != nil {
		t.Fatal(err)
	}
	if len(body) > 0 {
		if _, err := w.Write(body); err != nil {
			t.Fatal(err)
		}
		if rem := len(body) % 512; rem != 0 {
			if _, err := w.Write(make([]byte, 512-rem)); err != nil {
				t.Fatal(err)
			}
		}
	}
}

func TestUploadNamePreservesValidUTF8OnTruncation(t *testing.T) {
	longName := strings.Repeat("中", 60) + ".tar.gz"
	got, err := UploadName(longName)
	if err != nil {
		t.Fatalf("UploadName: %v", err)
	}
	if len(got) > 128 {
		t.Fatalf("len(got) = %d > 128", len(got))
	}
	if !utf8.ValidString(got) {
		t.Fatalf("UploadName split a UTF-8 rune: %q", got)
	}
}
