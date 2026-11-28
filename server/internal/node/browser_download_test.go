package node

import (
	"bytes"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/nwverse/persona/server/internal/proto"
)

func browserDownloadNode(t *testing.T) (*Node, string) {
	t.Helper()
	n, home := testNode(t)
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	dir := filepath.Join(home, ".persona", "browser-downloads")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		t.Fatal(err)
	}
	return n, dir
}

func TestBrowserDownloadDirectoryResolvesSymlink(t *testing.T) {
	fakeEgoHome(t, false, false, false, "")
	dir, target := browserDownloadsDir(), t.TempDir()
	if err := os.MkdirAll(filepath.Dir(dir), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(target, dir); err != nil {
		if runtime.GOOS == "windows" {
			t.Skipf("symlink unavailable: %v", err)
		}
		t.Fatal(err)
	}
	want, err := filepath.EvalSymlinks(target)
	if err != nil {
		t.Fatal(err)
	}
	if got, err := ensureBrowserDownloadsDir(); err != nil || got != want {
		t.Fatalf("managed download directory = %q, want %q: %v", got, want, err)
	}
	const name = "report.txt"
	if err := os.WriteFile(filepath.Join(target, name), []byte("report"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got, _, err := resolveBrowserDownload(name); err != nil || got != filepath.Join(want, name) {
		t.Fatalf("resolve browser download = %q: %v", got, err)
	}
	if err := os.Rename(target, target+"-moved"); err != nil {
		t.Fatal(err)
	}
	if got, err := ensureBrowserDownloadsDir(); err == nil {
		t.Fatalf("accepted dangling download directory symlink: %q", got)
	}
}

func TestBrowserDownloadDirectoryRequiresAbsoluteHome(t *testing.T) {
	for _, home := range []string{"", "relative"} {
		t.Setenv("HOME", home)
		t.Setenv("USERPROFILE", home)
		if got, err := ensureBrowserDownloadsDir(); err == nil {
			t.Errorf("accepted home %q as download directory %q", home, got)
		}
	}
}

func TestBrowserDownloadScriptConfiguresInitialAndNewPages(t *testing.T) {
	const dir = "/tmp/dl"
	const command = `p.cdp("Page.setDownloadBehavior", { behavior: "allow", downloadPath: __dlDir })`
	run := browserScript("persona-1", "return 1;", false, false, false, "", dir)
	for _, want := range []string{
		"const page = task.page(\"p1\");\nconst __dlDir = \"/tmp/dl\";",
		command, "await __bindDl(page);", "task.newPage.bind(task)", "task.newPage = async", "await __bindDl(p);",
	} {
		if !strings.Contains(run, want) {
			t.Errorf("missing %q from browser script:\n%s", want, run)
		}
	}
	look := browserScript("persona-1", "", true, false, false, "", dir)
	if strings.Contains(look, "Page.setDownloadBehavior") || strings.Contains(look, "task.newPage") {
		t.Fatalf("look script configures downloads:\n%s", look)
	}
}

func TestBrowserDownloadInfoAndChunks(t *testing.T) {
	n, dir := browserDownloadNode(t)
	const name = "browser report.bin"
	data := bytes.Repeat([]byte{0, 0xff, 0x7e, 0x0a}, proto.FileDownloadChunkBytes/4+2)
	path := filepath.Join(dir, name)
	if err := os.WriteFile(path, data, 0o600); err != nil {
		t.Fatal(err)
	}
	for _, builtin := range []bool{false, true} {
		t.Run(fmt.Sprintf("builtin=%t", builtin), func(t *testing.T) {
			call := func(method string, params map[string]any, out any) {
				t.Helper()
				var res any
				var err error
				if builtin {
					raw, _ := json.Marshal(params)
					res, err = pluginCall(t, n, "plugin.node.call", proto.PluginNodeCall{ID: "ego", Method: method, Params: raw})
				} else {
					res, err = pluginCall(t, n, "browser."+method, params)
				}
				if err != nil {
					t.Fatal(err)
				}
				raw, err := json.Marshal(res)
				if err != nil {
					t.Fatal(err)
				}
				if err := json.Unmarshal(raw, out); err != nil {
					t.Fatal(err)
				}
			}
			var info proto.UploadedFile
			call("download_info", map[string]any{"filename": name}, &info)
			realPath, err := filepath.EvalSymlinks(path)
			if err != nil {
				t.Fatal(err)
			}
			if info.Name != name || info.Path != realPath || info.Size != int64(len(data)) || info.SHA256 != fmt.Sprintf("%x", sha256.Sum256(data)) {
				t.Fatalf("download info: %+v", info)
			}
			for _, tc := range []struct {
				offset, length, count int
				eof                   bool
			}{
				{0, 0, proto.FileDownloadChunkBytes, false},
				{0, 2 * proto.FileDownloadChunkBytes, proto.FileDownloadChunkBytes, false},
				{1, 7, 7, false},
				{proto.FileDownloadChunkBytes, 100, 8, true},
				{len(data), 10, 0, true},
			} {
				var chunk struct {
					proto.FileChunk
					EOF bool `json:"eof"`
				}
				call("download_chunk", map[string]any{"filename": name, "offset": tc.offset, "length": tc.length}, &chunk)
				if chunk.Name != name || chunk.Offset != int64(tc.offset) || chunk.Size != info.Size || chunk.EOF != tc.eof ||
					!bytes.Equal(chunk.Data, data[tc.offset:tc.offset+tc.count]) {
					t.Errorf("range %+v: name=%q offset=%d size=%d eof=%t bytes=%d", tc, chunk.Name, chunk.Offset, chunk.Size, chunk.EOF, len(chunk.Data))
				}
			}
		})
	}
}

func TestBrowserDownloadRejectsInvalidFilesAndRanges(t *testing.T) {
	n, dir := browserDownloadNode(t)
	if err := os.Mkdir(filepath.Join(dir, "folder"), 0o700); err != nil {
		t.Fatal(err)
	}
	path := filepath.Join(dir, "large.bin")
	f, err := os.Create(path)
	if err != nil {
		t.Fatal(err)
	}
	err = f.Truncate(proto.MaxTerminalUploadBytes + 1)
	f.Close()
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "empty.bin"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"", ".", "..", "nul\x00.bin", "/download.bin", "sub/download.bin", `sub\download.bin`, "missing.bin", "folder", "large.bin"} {
		t.Run(fmt.Sprintf("%q", name), func(t *testing.T) {
			for _, method := range []string{"browser.download_info", "browser.download_chunk"} {
				if _, err := pluginCall(t, n, method, map[string]any{"filename": name}); err == nil || strings.Contains(err.Error(), "unknown method") {
					t.Errorf("%s should reject %q with a download error: %v", method, name, err)
				}
			}
		})
	}
	t.Run("symlink", func(t *testing.T) {
		if err := os.Symlink(filepath.Join(dir, "empty.bin"), filepath.Join(dir, "link.bin")); err != nil {
			if runtime.GOOS == "windows" {
				t.Skipf("symlink unavailable: %v", err)
			}
			t.Fatal(err)
		}
		for _, method := range []string{"browser.download_info", "browser.download_chunk"} {
			if _, err := pluginCall(t, n, method, map[string]any{"filename": "link.bin"}); err == nil || !strings.Contains(err.Error(), "regular file") {
				t.Errorf("%s allowed a symlink: %v", method, err)
			}
		}
	})
	for _, offset := range []int{-1, 1} {
		if _, err := pluginCall(t, n, "browser.download_chunk", map[string]any{"filename": "empty.bin", "offset": offset}); err == nil ||
			!strings.Contains(err.Error(), "offset") {
			t.Errorf("offset %d should be refused: %v", offset, err)
		}
	}
}

func TestBrowserDownloadPermissionError(t *testing.T) {
	if runtime.GOOS != "darwin" || os.Geteuid() == 0 {
		t.Skip("requires a non-root macOS filesystem permission check")
	}
	n, dir := browserDownloadNode(t)
	const name = "protected.bin"
	if err := os.WriteFile(filepath.Join(dir, name), nil, 0); err != nil {
		t.Fatal(err)
	}
	assertRefused := func() {
		t.Helper()
		for _, method := range []string{"browser.download_info", "browser.download_chunk"} {
			_, err := pluginCall(t, n, method, map[string]any{"filename": name})
			if !errors.Is(err, os.ErrPermission) || !strings.Contains(err.Error(), "Persona browser downloads") {
				t.Errorf("%s missing actionable permission error: %v", method, err)
			}
		}
	}
	assertRefused()
	if err := os.Chmod(dir, 0o600); err != nil {
		t.Fatal(err)
	}
	defer os.Chmod(dir, 0o700)
	assertRefused()
}
