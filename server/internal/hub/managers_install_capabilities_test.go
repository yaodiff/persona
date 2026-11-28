package hub

import (
	"bytes"
	"compress/gzip"
	"crypto/sha256"
	"encoding/hex"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
)

const compatibleManagerBinary = `#!/bin/sh
if [ "$1 $2 $3" = "manager capabilities --json" ]; then
	printf '%s\n' '{"capabilities":["config.own","machines.own"]}'
	exit 0
fi
`

func TestManagerInstallRequiresOwnConfigBeforeSideEffects(t *testing.T) {
	const joined = "echo joined >> \"$HOME/calls\"\n"
	const old = "#!/bin/sh\nif [ \"$2\" = capabilities ]; then exit 2; fi\n" + joined
	for _, tc := range []struct {
		name, existing, download string
		wantOK                   bool
	}{
		{"reuse capable", compatibleManagerBinary + joined, "", true},
		{"reject old without download", old, "", false},
		{"reject renamed capability", "#!/bin/sh\nif [ \"$2\" = capabilities ]; then echo '{\"capabilities\":[\"config.own.future\"]}'; exit 0; fi\n" + joined, "", false},
		{"reject old release keeping old binary", old, old, false},
		{"update old before join", old, compatibleManagerBinary + joined, true},
		{"reject old release before installation", "", old, false},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home := t.TempDir()
			bin := filepath.Join(home, ".persona", "bin", "personad")
			if tc.existing != "" {
				if err := os.MkdirAll(filepath.Dir(bin), 0o755); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(bin, []byte(tc.existing), 0o755); err != nil {
					t.Fatal(err)
				}
			}
			m := managerScript{ID: "m-0a1b2c", Hub: "https://hub.example.com", Code: "mi_x"}
			if tc.download != "" {
				archive := "personad-" + runtime.GOOS + "-" + runtime.GOARCH + ".gz"
				var gz bytes.Buffer
				zw := gzip.NewWriter(&gz)
				if _, err := zw.Write([]byte(tc.download)); err != nil {
					t.Fatal(err)
				}
				if err := zw.Close(); err != nil {
					t.Fatal(err)
				}
				sum := sha256.Sum256(gz.Bytes())
				dl := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
					if r.URL.Path != "/"+archive {
						http.NotFound(w, r)
						return
					}
					if _, err := w.Write(gz.Bytes()); err != nil {
						t.Errorf("download: %v", err)
					}
				}))
				defer dl.Close()
				m.DL = dl.URL
				m.Sums = map[string]string{archive: hex.EncodeToString(sum[:])}
			}
			cmd := exec.Command("sh")
			cmd.Stdin = strings.NewReader(scriptOnlyHere(t, managerInstallScript(m), t.TempDir(), home))
			cmd.Env = scriptEnv(home, "/usr/bin:/bin", "NO_PROXY=*", "no_proxy=*", "CURL_HOME="+home)
			out, err := cmd.CombinedOutput()
			if (err == nil) != tc.wantOK {
				t.Fatalf("success=%t want=%t: %v\n%s", err == nil, tc.wantOK, err, out)
			}
			calls, readErr := os.ReadFile(filepath.Join(home, "calls"))
			if tc.wantOK {
				if readErr != nil || strings.Count(string(calls), "joined") != 1 {
					t.Fatalf("join not called exactly once: %q %v", calls, readErr)
				}
			} else {
				if !os.IsNotExist(readErr) {
					t.Fatalf("incompatible program consumed code or created service: %q %v", calls, readErr)
				}
				if !strings.Contains(string(out), "config.own") {
					t.Fatalf("missing update guidance: %s", out)
				}
				kept, readErr := os.ReadFile(bin)
				if tc.existing == "" {
					if !os.IsNotExist(readErr) {
						t.Fatalf("installed incompatible download: %q %v", kept, readErr)
					}
				} else if readErr != nil || string(kept) != tc.existing {
					t.Fatalf("replaced existing binary: %q %v", kept, readErr)
				}
			}
		})
	}
}
