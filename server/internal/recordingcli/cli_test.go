package recordingcli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/nwverse/persona/server/internal/clitext"
)

const cliID = "11111111-1234-4234-8234-abcdef012345"
const cliEntry = `{"kind":"recordings","id":"` + cliID + `","name":"Meeting\u001b[2J"}`
const cliDetail = `{"detail":{"kind":"recordings","id":"` + cliID + `","meta":{"kind":"meeting","id":"` + cliID + `","name":"Review","started_at":"2026-01-02T03:00:00Z"},"segments":[{"start":0.5,"end":1,"text":"First line"}]}}`

func TestListAndSearchUseReadRoutes(t *testing.T) {
	t.Setenv("PERSONA_LANG", "en")
	t.Setenv(tokenEnv, "unused")
	file := filepath.Join(t.TempDir(), "device-token")
	if err := os.WriteFile(file, []byte("file-test-token\n"), 0600); err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	hub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Method != http.MethodGet || r.Header.Get("Authorization") != "Bearer file-test-token" || r.Header.Get("Accept-Language") != "en" {
			t.Error("unexpected method or headers")
		}
		switch r.URL.EscapedPath() {
		case "/mounted%20hub" + listPath:
			if r.URL.RawQuery != "" {
				t.Errorf("unexpected query %v", r.URL.Query())
			}
			io.WriteString(w, `{"items":[`+cliEntry+`]}`)
		case "/mounted%20hub/api/v1/kb-search":
			if q := r.URL.Query(); q.Get("kind") != "recordings" || q.Get("results") != "1" || q.Get("q") != "coffee & tea" {
				t.Errorf("search query %v", q)
			}
			// A hub that ignored kind still returns an entry's kind.
			io.WriteString(w, `{"results":[{"entry":{"kind":"projects","id":"a-project"}},{"entry":`+cliEntry+`,"hits":[{"line":"private matching line"}]}]}`)
		default:
			t.Errorf("unexpected path %s", r.URL.EscapedPath())
			http.NotFound(w, r)
		}
	}))
	defer hub.Close()
	args := []string{"list", "--hub", hub.URL + "/mounted%20hub", "--token-file", file}
	var out bytes.Buffer
	if err := Run(t.Context(), args, &out); err != nil {
		t.Fatal(err)
	}
	if out.String() != cliID+"\tMeeting<ESC>[2J\n" {
		t.Fatalf("list: %q", out.String())
	}
	out.Reset()
	if err := Run(t.Context(), append(args, "--json", "--query", "coffee & tea"), &out); err != nil {
		t.Fatal(err)
	}
	var page recordingList
	if err := json.Unmarshal(out.Bytes(), &page); err != nil || len(page.Items) != 1 || page.Items[0].ID != cliID || page.Items[0].Name != "Meeting\x1b[2J" {
		t.Fatalf("json list: %v %s", err, out.String())
	}
	if strings.Contains(out.String(), "private matching line") || calls.Load() != 2 {
		t.Fatal("unexpected search output or request count")
	}
}

func TestExportStdoutAndPrivateFile(t *testing.T) {
	t.Setenv("PERSONA_LANG", "en")
	t.Setenv(tokenEnv, "mock-paired-device")
	var calls atomic.Int32
	hub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		calls.Add(1)
		if r.Method != http.MethodGet || r.URL.Path != listPath+"/"+cliID || r.Header.Get("Authorization") != "Bearer mock-paired-device" {
			t.Error("expected one authenticated GET for the requested recording")
		}
		io.WriteString(w, cliDetail)
	}))
	defer hub.Close()
	var out bytes.Buffer
	args := []string{"export", "--hub", hub.URL}
	if err := Run(t.Context(), append(args, strings.ToUpper(cliID)), &out); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(out.String(), "First line") || !strings.Contains(out.String(), "Review") {
		t.Fatalf("Markdown: %s", out.String())
	}
	path := filepath.Join(t.TempDir(), "captions.vtt")
	toFile := append(args, "--format", "vtt", "--output", path, cliID)
	out.Reset()
	if err := Run(t.Context(), toFile, &out); err != nil {
		t.Fatal(err)
	}
	saved, err := os.ReadFile(path)
	if err != nil || !strings.HasPrefix(string(saved), "WEBVTT\n") || !strings.Contains(string(saved), "First line") {
		t.Fatalf("saved VTT: %v, %q", err, saved)
	}
	if mode, err := os.Stat(path); err != nil || runtime.GOOS != "windows" && mode.Mode().Perm() != 0600 {
		t.Fatalf("output permissions: %v, %v", mode, err)
	}
	if !strings.Contains(out.String(), path) || strings.Contains(out.String(), "First line") {
		t.Fatalf("save status: %q", out.String())
	}
	out.Reset()
	if err := Run(t.Context(), toFile, &out); err == nil || !strings.Contains(err.Error(), "already exists") || out.Len() != 0 || calls.Load() != 2 {
		t.Fatalf("collision: err=%v, calls=%d, stdout=%q", err, calls.Load(), out.String())
	}
}

func TestInvalidRepliesAndFailedExportLeaveNoOutput(t *testing.T) {
	t.Setenv("PERSONA_LANG", "en")
	t.Setenv(tokenEnv, "mock-paired-device")
	for _, test := range []struct {
		name   string
		args   []string
		raw    string
		status int
	}{
		{"list without items", []string{"list"}, `{"tags":[]}`, 200},
		{"invalid UUID", []string{"list"}, `{"items":[` + cliEntry + `,{"id":"bad","kind":"recordings"}]}`, 200},
		{"search without results", []string{"list", "--query", "a"}, `{"hits":[]}`, 200},
		{"search missing entry", []string{"list", "--query", "a"}, `{"results":[{}]}`, 200},
		{"truncated", []string{"export"}, `{"detail":`, 200},
		{"wrong entity", []string{"export"}, `{"error":"private First line"}`, 404},
		{"wrong kind", []string{"export"}, strings.Replace(cliDetail, `"recordings"`, `"people"`, 1), 200},
		{"wrong recording", []string{"export"}, strings.Replace(cliDetail, cliID, "00000000-0000-4000-8000-000000000000", 1), 200},
	} {
		t.Run(test.name, func(t *testing.T) {
			hub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(test.status)
				io.WriteString(w, test.raw)
			}))
			defer hub.Close()
			dir := t.TempDir()
			args := append(test.args, "--hub", hub.URL)
			if test.args[0] == "export" {
				args = append(args, "--output", filepath.Join(dir, "notes.md"), cliID)
			}
			var out bytes.Buffer
			err := Run(t.Context(), args, &out)
			if err == nil || out.Len() != 0 || strings.Contains(err.Error(), "First line") {
				t.Fatalf("err=%v, stdout=%q", err, out.String())
			}
			files, err := os.ReadDir(dir)
			if err != nil || len(files) != 0 {
				t.Fatalf("destination after failure: %v, %v", files, err)
			}
		})
	}
}

func TestRecordingCommandHelpAndSourceValidation(t *testing.T) {
	t.Setenv("PERSONA_LANG", "en")
	t.Setenv(tokenEnv, "")
	var out bytes.Buffer
	if err := Run(t.Context(), []string{"export", "--help"}, &out); err != nil || !strings.Contains(out.String(), "PERSONA_RECORDINGS_TOKEN") {
		t.Fatalf("help: %v, %s", err, out.String())
	}
	for _, args := range [][]string{nil, {"unknown"}, {"list", "extra"}, {"export"}, {"export", "--format", "html", cliID}, {"export", "../escape"}, {"export", "--output=", cliID}, {"list", "--hub", "http://example.com"}} {
		var usageErr clitext.UsageError
		if err := Run(t.Context(), args, io.Discard); !errors.As(err, &usageErr) {
			t.Fatalf("%v: expected usage error, got %v", args, err)
		}
	}
	if err := Run(t.Context(), []string{"list", "--hub", "https://example.com"}, io.Discard); err == nil || !strings.Contains(err.Error(), tokenEnv) {
		t.Fatalf("missing authentication: %v", err)
	}
}

func TestEmptyListAndCanceledRead(t *testing.T) {
	t.Setenv("PERSONA_LANG", "en")
	t.Setenv(tokenEnv, "mock-paired-device")
	hub := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { io.WriteString(w, `{"items":[]}`) }))
	defer hub.Close()
	args := []string{"list", "--hub", hub.URL, "--json"}
	var out bytes.Buffer
	if err := Run(t.Context(), args, &out); err != nil || out.String() != "{\"items\":[]}\n" {
		t.Fatalf("empty list: %v %s", err, out.String())
	}
	out.Reset()
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := Run(ctx, args, &out); !errors.Is(err, context.Canceled) || out.Len() != 0 {
		t.Fatalf("cancellation: %v %s", err, out.String())
	}
}
