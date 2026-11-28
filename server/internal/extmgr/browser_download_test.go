package extmgr

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/nwverse/persona/server/internal/proto"
)

const (
	browserDownloadTool = "browser_fetch_download"
	testDownloadName    = "report.bin"
	testDownloadPath    = "/home/example/.persona/browser-downloads/report.bin"
	testUploadedPath    = "/home/example/uploads/report.bin"
)

func enableBrowserDownloads(t *testing.T, h *Manager, confirm bool) {
	t.Helper()
	ctx := t.Context()
	if err := h.extensions.reg.SetEnabled(ctx, egoExtensionID, true, "test"); err != nil {
		t.Fatal(err)
	}
	raw := json.RawMessage(fmt.Sprintf(`{"machines":["source"],"confirm":%t}`, confirm))
	if err := h.extensions.reg.SetConfig(ctx, egoExtensionID, raw, "test"); err != nil {
		t.Fatal(err)
	}
}

func fetchTestDownload(t *testing.T, ctx context.Context, h *Manager, caller, filename string) proto.ToolResult {
	t.Helper()
	args, _ := json.Marshal(map[string]string{"machine": "source", "filename": filename})
	res, handled := h.CallTool(ctx, caller, "conv", browserDownloadTool, args, true)
	if !handled || len(res.Content) != 1 {
		t.Fatalf("browser_fetch_download not handled: %+v", res)
	}
	return res
}

func callsWithPrefix(calls []browserCallSeen, prefix string) []browserCallSeen {
	return slices.DeleteFunc(calls, func(c browserCallSeen) bool { return !strings.HasPrefix(c.Method, prefix) })
}

func TestBrowserFetchDownload(t *testing.T) {
	for _, caller := range []string{"", "source", "SOURCE", "destination", "empty-destination"} {
		t.Run("caller="+caller, func(t *testing.T) {
			h, _ := extensionTestHub(t)
			data := bytes.Repeat([]byte{0, 0xff, 0x13, 0x07}, proto.FileDownloadChunkBytes/4+2)
			if caller == "empty-destination" {
				data = nil
			}
			digest := fmt.Sprintf("%x", sha256.Sum256(data))
			_, fromCalls := egoMacWithCalls(t, h, "source", func(method string, params json.RawMessage) (any, error) {
				var p struct {
					Filename string
					Offset   int
					Length   int
				}
				if err := json.Unmarshal(params, &p); err != nil || p.Filename != testDownloadName {
					return nil, fmt.Errorf("unexpected source request %s: %s", method, params)
				}
				switch method {
				case "browser.download_info":
					return proto.UploadedFile{Name: testDownloadName, Path: testDownloadPath, Size: int64(len(data)), SHA256: digest}, nil
				case "browser.download_chunk":
					if p.Offset < 0 || p.Length <= 0 || p.Length > proto.FileDownloadChunkBytes || p.Offset+p.Length > len(data) {
						return nil, fmt.Errorf("unexpected chunk request: %+v", p)
					}
					return map[string]any{"name": testDownloadName, "offset": p.Offset, "size": len(data),
						"data": data[p.Offset : p.Offset+p.Length], "eof": p.Offset+p.Length == len(data)}, nil
				}
				return nil, fmt.Errorf("unexpected source method %s", method)
			})
			copied := caller == "destination" || caller == "empty-destination"
			var toCalls func() []browserCallSeen
			if copied {
				_, toCalls = egoMacWithCalls(t, h, caller, func(method string, _ json.RawMessage) (any, error) {
					switch method {
					case "file.upload.begin":
						return map[string]any{"upload_id": "upload", "chunk_bytes": proto.UploadChunkBytes / 2, "sha256": true}, nil
					case "file.upload.chunk":
						return map[string]any{}, nil
					case "file.upload.end":
						return proto.UploadedFile{Name: testDownloadName, Path: testUploadedPath, Size: int64(len(data)), SHA256: digest}, nil
					}
					return nil, fmt.Errorf("unexpected destination method %s", method)
				})
			}
			enableBrowserDownloads(t, h, false)
			res := fetchTestDownload(t, t.Context(), h, caller, testDownloadName)
			var receipt struct {
				SourceMachine string `json:"source_machine"`
				DestMachine   string `json:"dest_machine"`
				Filename      string `json:"filename"`
				Path          string `json:"path"`
				Size          int64  `json:"size"`
				SHA256        string `json:"sha256"`
				Copied        bool   `json:"copied"`
			}
			if res.IsError || json.Unmarshal([]byte(res.Content[0].Text), &receipt) != nil {
				t.Fatalf("download result: %+v", res)
			}
			dest, path := "source", testDownloadPath
			if copied {
				dest, path = caller, testUploadedPath
			}
			if receipt.SourceMachine != "source" || receipt.DestMachine != dest || receipt.Filename != testDownloadName ||
				receipt.Path != path || receipt.Size != int64(len(data)) || receipt.SHA256 != digest || receipt.Copied != copied {
				t.Fatalf("download receipt: %+v", receipt)
			}
			if !copied {
				if calls := callsWithPrefix(fromCalls(), "browser.download_"); len(calls) != 1 || calls[0].Method != "browser.download_info" {
					t.Fatalf("same-machine transfer used additional RPCs: %+v", calls)
				}
				if calls := callsWithPrefix(fromCalls(), "file.upload."); len(calls) != 0 {
					t.Fatalf("same-machine transfer uploaded a copy: %+v", calls)
				}
				return
			}
			calls := callsWithPrefix(toCalls(), "file.upload.")
			if len(calls) < 2 || calls[0].Method != "file.upload.begin" || calls[len(calls)-1].Method != "file.upload.end" {
				t.Fatalf("upload begin/end: %+v", calls)
			}
			if begin := calls[0].Params; begin["name"] != testDownloadName || begin["size"] != float64(len(data)) ||
				begin["dest"] != proto.UploadDestTerminal || begin["sha256"] != digest {
				t.Fatalf("upload begin: %+v", begin)
			}
			if end := calls[len(calls)-1].Params; end["upload_id"] != "upload" || end["sha256"] != digest {
				t.Fatalf("upload end: %+v", end)
			}
			var uploaded []byte
			for _, c := range calls[1 : len(calls)-1] {
				if c.Method != "file.upload.chunk" || c.Params["offset"] != float64(len(uploaded)) || c.Params["upload_id"] != "upload" {
					t.Fatalf("upload chunk method=%s offset=%v upload_id=%v", c.Method, c.Params["offset"], c.Params["upload_id"])
				}
				encoded, _ := c.Params["data"].(string)
				part, err := base64.StdEncoding.DecodeString(encoded)
				if err != nil || len(part) > proto.UploadChunkBytes/2 {
					t.Fatalf("chunk size=%d, error=%v", len(part), err)
				}
				uploaded = append(uploaded, part...)
			}
			if !bytes.Equal(uploaded, data) {
				t.Fatalf("uploaded %d bytes differing from %d requested bytes", len(uploaded), len(data))
			}
		})
	}
}

func TestBrowserFetchDownloadAbortsOnFailure(t *testing.T) {
	for _, failure := range []string{"old destination", "read", "offset", "short", "digest", "upload", "finalize", "receipt", "cancel"} {
		t.Run(failure, func(t *testing.T) {
			h, _ := extensionTestHub(t)
			ctx, cancel := context.WithCancel(t.Context())
			defer cancel()
			digest := fmt.Sprintf("%x", sha256.Sum256([]byte("good")))
			_, _ = egoMacWithCalls(t, h, "source", func(method string, _ json.RawMessage) (any, error) {
				switch method {
				case "browser.download_info":
					return proto.UploadedFile{Name: testDownloadName, Path: testDownloadPath, Size: 4, SHA256: digest}, nil
				case "browser.download_chunk":
					data, offset := []byte("good"), 0
					switch failure {
					case "read":
						return nil, errors.New("source read failed")
					case "offset":
						offset = 1
					case "short":
						data = data[:2]
					case "digest":
						data = []byte("bad!")
					case "cancel":
						cancel()
					}
					return map[string]any{"name": testDownloadName, "offset": offset, "size": 4, "data": data, "eof": true}, nil
				}
				return nil, fmt.Errorf("unexpected source method %s", method)
			})
			_, calls := egoMacWithCalls(t, h, "destination", func(method string, _ json.RawMessage) (any, error) {
				switch method {
				case "file.upload.begin":
					return map[string]any{"upload_id": "upload", "sha256": failure != "old destination"}, nil
				case "file.upload.chunk":
					if failure == "upload" {
						return nil, errors.New("write failed")
					}
					return map[string]any{}, nil
				case "file.upload.end":
					if failure == "receipt" {
						return proto.UploadedFile{Name: testDownloadName, Path: testUploadedPath, Size: 4}, nil
					}
					return nil, errors.New("finalize failed")
				case "file.upload.abort":
					return map[string]any{}, nil
				}
				return nil, fmt.Errorf("unexpected destination method %s", method)
			})
			enableBrowserDownloads(t, h, false)
			if res := fetchTestDownload(t, ctx, h, "destination", testDownloadName); !res.IsError {
				t.Fatalf("failure was reported as success: %+v", res)
			}
			got := callsWithPrefix(calls(), "file.upload.")
			if len(got) < 2 || got[len(got)-1].Method != "file.upload.abort" || got[len(got)-1].Params["upload_id"] != "upload" {
				t.Fatalf("upload not aborted: %+v", got)
			}
			for _, c := range got {
				if c.Method == "file.upload.end" && failure != "finalize" && failure != "receipt" {
					t.Fatalf("upload finalized after a %s failure", failure)
				}
			}
		})
	}
}

func TestBrowserFetchDownloadChecksAvailabilityAndApproval(t *testing.T) {
	h, _ := extensionTestHub(t)
	src, calls := egoMacWithCalls(t, h, "source", func(method string, _ json.RawMessage) (any, error) {
		if method != "browser.download_info" {
			return nil, fmt.Errorf("unexpected source method %s", method)
		}
		return proto.UploadedFile{Name: testDownloadName, Path: testDownloadPath, SHA256: fmt.Sprintf("%x", sha256.Sum256(nil))}, nil
	})
	offline, _ := egoMac(t, h, "offline")
	offline.mu.Lock()
	offline.online = false
	offline.mu.Unlock()
	work, workCalls := egoMac(t, h, "work")
	workConfig := work.config()
	workConfig.trust = "work"
	work.listed.Store(&workConfig)
	enableBrowserDownloads(t, h, true)
	asked := 0
	allow := false
	h.opts.Ask = func(_ context.Context, ix proto.Interaction) (proto.Resolution, error) {
		asked++
		if ix.ToolName != browserDownloadTool || !strings.Contains(ix.Summary, testDownloadName) {
			t.Errorf("approval card: %+v", ix)
		}
		return proto.Resolution{Allow: &allow}, nil
	}
	missingMachine := json.RawMessage(`{"filename":"report.bin"}`)
	if res, handled := h.CallTool(t.Context(), "source", "conv", browserDownloadTool, missingMachine, true); !handled || !res.IsError {
		t.Fatalf("omitted browser machine: %+v", res)
	}
	for _, invalid := range []struct{ caller, filename string }{
		{"source", ""}, {"source", "../report.bin"}, {"source", "dir\\report.bin"},
		{"missing", testDownloadName}, {"offline", testDownloadName}, {"work", testDownloadName},
	} {
		if res := fetchTestDownload(t, t.Context(), h, invalid.caller, invalid.filename); !res.IsError {
			t.Fatalf("invalid request succeeded: %+v", invalid)
		}
	}
	if asked != 0 {
		t.Fatalf("invalid requests asked for approval %d times", asked)
	}
	if res := fetchTestDownload(t, t.Context(), h, src.cfg.ID, testDownloadName); !res.IsError || !strings.Contains(res.Content[0].Text, "declined") {
		t.Fatalf("declined file request: %+v", res)
	}
	if known, readOnly := h.ToolAccess(browserDownloadTool); !known || readOnly {
		t.Fatalf("tool access: known=%t readOnly=%t", known, readOnly)
	}
	if got := callsWithPrefix(calls(), "browser.download_"); asked != 1 || len(got) != 0 {
		t.Fatalf("unapproved file request: %d approvals, %v download calls", asked, got)
	}
	allow = true
	if res := fetchTestDownload(t, t.Context(), h, src.cfg.ID, testDownloadName); res.IsError {
		t.Fatalf("approved file request: %+v", res)
	}
	if got := callsWithPrefix(calls(), "browser.download_"); asked != 2 || len(got) != 1 || got[0].Method != "browser.download_info" {
		t.Fatalf("approved file request: %d approvals, %v download calls", asked, got)
	}
	sourceConfig := src.config()
	sourceConfig.trust = "work"
	src.listed.Store(&sourceConfig)
	if res := fetchTestDownload(t, t.Context(), h, src.cfg.ID, testDownloadName); !res.IsError || !strings.Contains(res.Content[0].Text, "work computer") {
		t.Fatalf("work source download: %+v", res)
	}
	if got := callsWithPrefix(calls(), "browser.download_"); asked != 2 || len(got) != 1 || len(callsWithPrefix(workCalls(), "file.upload.")) != 0 {
		t.Fatalf("restricted file request: %d approvals, %v download calls", asked, got)
	}
}
