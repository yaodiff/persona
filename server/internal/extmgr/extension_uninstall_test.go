package extmgr

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/extension"
	"github.com/nwverse/persona/server/internal/rpc"
)

func installUninstallFixture(t *testing.T, h *Manager, id string, pack bool) *extension.Extension {
	t.Helper()
	contributes := fmt.Sprintf(`"contributes":{"providers":[{"id":%q,"name":"Example","base_url":"https://example.com"}]}`, id+"-p")
	if pack {
		contributes = `"extension_pack":[{"id":"a"},{"id":"b"}]`
	}
	dir := declarativePackage(t, fmt.Sprintf(`{"manifest_version":1,"id":%q,"name":%q,"version":"1.0.0",%s}`, id, id, contributes))
	pv, err := h.extensions.reg.Preview(t.Context(), "dir:"+dir, "")
	if err != nil {
		t.Fatal(err)
	}
	p, err := h.extensions.reg.Install(t.Context(), pv.ID, pv.Digest, "test")
	if err != nil {
		t.Fatal(err)
	}
	// Fixtures use market origins so a full-rights remote device may remove them.
	p.Record.Origin = "market:demo/" + id
	return p
}

func TestExtensionTrustForgetRunsOnlyOnUninstall(t *testing.T) {
	h, phone, _ := marketDevices(t)
	installUninstallFixture(t, h, "example", false)
	var forgotten []string
	h.opts.Assistant = func(_ context.Context, method string, params json.RawMessage) (json.RawMessage, error) {
		if method == assistantwire.MethodExtensionTrustForget {
			forgotten = append(forgotten, string(params))
			return nil, &rpc.Error{Code: extension.CodeUnknownMethod, Message: "unknown method " + method}
		}
		return json.RawMessage(`{}`), nil
	}
	if err := h.extensions.reg.SetEnabled(t.Context(), "example", false, "test"); err != nil || len(forgotten) != 0 {
		t.Fatalf("switching off: forgot %v, error %v", forgotten, err)
	}
	code, out := call(t, h.routes(), "DELETE", "/api/v1/extensions/example/-/?keep_data=1", phone, "")
	if code != 200 || len(forgotten) != 1 || forgotten[0] != `{"extension":"example"}` {
		t.Fatalf("uninstall with an older assistant: HTTP %d %v, forgot %v", code, out, forgotten)
	}
}

func TestPackUninstallPartialFailure(t *testing.T) {
	for _, admin := range []bool{false, true} {
		for _, failID := range []string{"a", "both"} {
			t.Run(fmt.Sprintf("admin=%v/fail=%s", admin, failID), func(t *testing.T) {
				var h *Manager
				var store *extensionStore
				var request func() (int, map[string]any)
				if admin {
					h, store = extensionTestHub(t)
					mux := adminRoutes(h) // relayed with the admin header (the hub's admin session in front)
					request = func() (int, map[string]any) {
						return adminCall(t, mux, "DELETE", "/api/v1/admin/extensions/both", `{"members":true,"keep_data":true}`)
					}
				} else {
					var phone string
					h, phone, _ = marketDevices(t)
					store = h.store.(*extensionStore)
					mux := h.routes()
					request = func() (int, map[string]any) {
						return call(t, mux, "DELETE", "/api/v1/extensions/both/-/?members=1&keep_data=1", phone, "")
					}
				}
				for _, id := range []string{"a", "b", "both"} {
					installUninstallFixture(t, h, id, id == "both")
				}
				data := filepath.Join(h.extensions.reg.DataDir("a"), "kept.txt")
				if err := os.MkdirAll(filepath.Dir(data), 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(data, []byte("example"), 0o600); err != nil {
					t.Fatal(err)
				}
				store.failDelete = failID
				code, out := request()
				if code != 409 || out["ok"] != false || out["code"] != "partial_uninstall" {
					t.Fatalf("%d %v", code, out)
				}
				if h.extensions.reg.Get("both") == nil {
					t.Fatal("pack must remain for retry")
				}
				failed := out["failed_members"].([]any)
				if len(failed) != 1 || failed[0].(map[string]any)["id"] != failID {
					t.Fatalf("failed: %v", failed)
				}
				remaining := out["remaining_members"].([]any)
				want := 0
				if failID == "a" {
					want = 1
				}
				if len(remaining) != want || h.extensions.reg.Get("b") != nil {
					t.Fatalf("remaining=%v", remaining)
				}
				store.failDelete = ""
				if code, out = request(); code != 200 || out["ok"] != true {
					t.Fatalf("retry: %d %v", code, out)
				}
				for _, id := range []string{"a", "b", "both"} {
					if h.extensions.reg.Get(id) != nil {
						t.Fatalf("%s still installed", id)
					}
				}
				if _, err := os.Stat(data); err != nil {
					t.Fatalf("kept data: %v", err)
				}
			})
		}
	}
}

func TestPackCannotRemoveLocalMemberAsDevice(t *testing.T) {
	h, phone, _ := marketDevices(t)
	for _, id := range []string{"a", "b", "both"} {
		installUninstallFixture(t, h, id, id == "both")
	}
	h.extensions.reg.Get("a").Record.Origin = "dir:/example/local-package"
	code, out := call(t, h.routes(), "DELETE", "/api/v1/extensions/both/-/?members=1&cascade=1", phone, "")
	if code != 403 || out["code"] != "this_machine_only" {
		t.Fatalf("%d %v", code, out)
	}
	for _, id := range []string{"a", "b", "both"} {
		if h.extensions.reg.Get(id) == nil {
			t.Fatalf("%s removed before source precheck", id)
		}
	}
}

// TestUninstallOperationWarnings: an async uninstall whose files could not all be deleted succeeds, and its operation
// names the path to delete by hand, as the synchronous answer's warnings do.
func TestUninstallOperationWarnings(t *testing.T) {
	if os.Geteuid() == 0 {
		t.Skip("root removes from a read-only directory")
	}
	h, phone, _ := marketDevices(t)
	installUninstallFixture(t, h, "solo", false)
	// The data has a read-only directory in it: it moves aside, but cannot be removed there.
	locked := filepath.Join(h.extensions.reg.DataDir("solo"), "locked")
	if err := os.MkdirAll(locked, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(locked, "f"), []byte("x"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(locked, 0o500); err != nil {
		t.Fatal(err)
	}
	root := filepath.Dir(filepath.Dir(h.extensions.reg.DataDir("solo")))
	var left []string // where the locked directory went
	t.Cleanup(func() {
		for _, d := range left {
			_ = os.Chmod(d, 0o700)
		}
	})

	id := opCall(t, h.routes(), "DELETE", "/api/v1/extensions/solo/-/?async=1", phone, "")
	v := waitOp(t, h, id, opSucceeded, opFailed)
	_ = filepath.WalkDir(root, func(p string, d fs.DirEntry, err error) error {
		if err == nil && d.IsDir() && d.Name() == "locked" {
			left = append(left, p)
		}
		return nil
	})
	if v.State != opSucceeded || h.extensions.reg.Get("solo") != nil {
		t.Fatalf("uninstalled: %+v", v)
	}
	if len(left) != 1 || len(v.Warnings) != 1 {
		t.Fatalf("one leftover, named: %v %+v", left, v.Warnings)
	}
	named := false
	for d := filepath.Dir(left[0]); len(d) > len(root); d = filepath.Dir(d) {
		named = named || strings.Contains(v.Warnings[0], d)
	}
	if !named {
		t.Fatalf("the warning names where %s is: %q", left[0], v.Warnings[0])
	}
	code, out := call(t, h.routes(), "GET", "/api/v1/extensions/-/operations/"+id, phone, "")
	if ws, _ := out["warnings"].([]any); code != 200 || len(ws) != 1 || ws[0] != v.Warnings[0] {
		t.Fatalf("its answer has them: %d %v", code, out)
	}
}
