package chatexport

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/coder/websocket"
	"github.com/coder/websocket/wsjson"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/proto"
)

const (
	fixtureToken   = "synthetic-cli-credential"
	fixtureMachine = "box/one"
	fixtureCurrent = "active #1"
	fixtureEarlier = "earlier#2"
)

var fixtureTime = time.Date(2020, time.January, 1, 0, 0, 0, 0, time.UTC)

func fixtureItem(id string, seq, minute int, role, kind, text string) proto.Item {
	return proto.Item{ID: id, Seq: seq, TS: fixtureTime.Add(time.Duration(minute) * time.Minute),
		Role: role, Kind: kind, Text: text}
}

type fixtureHub struct {
	t      *testing.T
	server *httptest.Server
	token  string
	output string
}

func newFixtureHub(t *testing.T, features []string, handler http.HandlerFunc) fixtureHub {
	t.Helper()
	t.Setenv("LC_ALL", "en_US.UTF-8")
	dir := t.TempDir()
	f := fixtureHub{t: t, token: filepath.Join(dir, "token"), output: filepath.Join(dir, "conversation.md")}
	if err := os.WriteFile(f.token, []byte(fixtureToken+"\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	f.server = httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != "GET" || r.Header.Get("Authorization") != "Bearer "+fixtureToken ||
			r.URL.Query().Has("token") || r.Header.Get("Accept-Language") != "en" {
			t.Errorf("unexpected authentication or method for %s", r.URL.EscapedPath())
			w.WriteHeader(http.StatusUnauthorized)
			return
		}
		if r.URL.Path == "/mount/api/v1/ws" {
			conn, err := websocket.Accept(w, r, nil)
			if err != nil {
				t.Error(err)
				return
			}
			defer conn.CloseNow()
			if err := wsjson.Write(r.Context(), conn, map[string]any{"type": "hello", "features": features}); err != nil {
				t.Error(err)
			}
			return
		}
		handler.ServeHTTP(w, r)
	}))
	t.Cleanup(f.server.Close)
	return f
}

func (f fixtureHub) run(more ...string) (string, error) {
	f.t.Helper()
	args := []string{"export", "--hub", f.server.URL + "/mount", "--token-file", f.token, "--output", f.output}
	args = append(args, more...)
	args = append(args, fixtureMachine, fixtureCurrent)
	var output bytes.Buffer
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()
	var err error
	if bin := os.Getenv("PERSONA_TEST_CHAT_EXPORT_BIN"); bin != "" {
		cmd := exec.CommandContext(ctx, bin, append([]string{"chat"}, args...)...)
		cmd.Stdout, cmd.Stderr = &output, &output
		err = cmd.Run()
	} else {
		err = Run(ctx, args, &output, &output)
		if err != nil {
			output.WriteString(err.Error())
		}
	}
	return output.String(), err
}

func (f fixtureHub) noFile() {
	f.t.Helper()
	files, err := os.ReadDir(filepath.Dir(f.output))
	if err != nil || len(files) != 1 || files[0].Name() != "token" {
		f.t.Fatalf("failed export left files: %v %v", files, err)
	}
}

func respondJSON(w http.ResponseWriter, value any) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(value)
}

func respondRow(t *testing.T, w http.ResponseWriter, r *http.Request, row sessionRow) {
	t.Helper()
	q := r.URL.Query()
	if q.Get("machine_id") != fixtureMachine || q.Get("session_id") != fixtureCurrent ||
		q.Get("summary") != "0" || q.Get("limit") != "1" {
		t.Errorf("not an exact session lookup: %v", q)
		w.WriteHeader(http.StatusBadRequest)
		return
	}
	respondJSON(w, map[string]any{"sessions": []sessionRow{row}, "total": 1, "revision": "opaque-list", "has_more": false})
}

func respondPage(w http.ResponseWriter, history string, to int, more bool, items ...proto.Item) {
	if items == nil {
		items = []proto.Item{}
	}
	from := 0
	if more && len(items) > 0 {
		from = items[0].Seq - 1
	}
	respondJSON(w, map[string]any{"items": items, "has_more": more, "history": history, "to_seq": to, "from_seq": from, "strict_read": true})
}

func TestJoinedExportPreservesTheCutoffAndCompleteText(t *testing.T) {
	for _, steps := range []bool{false, true} {
		t.Run(map[bool]string{false: "conversation", true: "with steps"}[steps], func(t *testing.T) {
			const sessionPath = "/mount/api/v1/sessions/"
			const currentPath = sessionPath + "box%2Fone/active%20%231/items"
			const earlierPath = sessionPath + "box%2Fone/earlier%232/items"
			row := sessionRow{Machine: fixtureMachine, ID: fixtureCurrent, Title: "Synthetic decision",
				Agent: "claude", Folder: "/fixture workspace", Manager: "fixture-manager",
				History: []proto.EngineLeg{
					{SessionID: fixtureCurrent, Agent: "claude", Until: fixtureTime.Add(10 * time.Minute)},
					{SessionID: fixtureEarlier, Agent: "codex", Until: fixtureTime.Add(20 * time.Minute)},
				}}
			whole := "Current reply: 完整\n\n    keep indentation\n\n    after whitespace\n" +
				strings.Repeat("large content ", 1600) + " final-proof"
			cut := fixtureItem("answer#0", 7, 24, "assistant", "text", "Current reply: preview")
			cut.FullSize = len(whole)
			result := fixtureItem("output#1", 6, 23, "assistant", "tool_result", "Step result")
			tool := fixtureItem("call#1", 6, 23, "assistant", "tool_use", "")
			tool.ToolName, tool.ToolInput = "fixture-command", json.RawMessage(`{"pattern":"value"}`)
			thought := fixtureItem("thought", 6, 23, "assistant", "thinking", "reasoning")
			narration := fixtureItem("narration", 6, 23, "assistant", "text", "intermediate update")
			narration.Narration = true
			question := fixtureItem("question#1", 5, 22, "user", "text", "Resume the decision")
			picture := fixtureItem("question#2", 5, 22, "user", "image", "")
			question.ClientMsgID, picture.ClientMsgID = "same-message", "same-message"
			var currentHeads, earlierHeads atomic.Int32
			f := newFixtureHub(t, []string{"sessions_lookup"}, func(w http.ResponseWriter, r *http.Request) {
				q := r.URL.Query()
				switch r.URL.EscapedPath() {
				case "/mount/api/v1/sessions":
					respondRow(t, w, r, row)
				case currentPath:
					if q.Get("lazy_images") != "1" {
						t.Error("did not opt out of image bytes")
					}
					switch {
					case q.Get("limit") == "1":
						respondPage(w, "current-history", 10, true, fixtureItem("later", 10, 25, "assistant", "text", "Later append"))
					case q.Get("before_seq") == "5":
						respondPage(w, "current-history", 4, false, fixtureItem("old", 2, 5, "user", "text", "First stretch"))
					case q.Get("before_seq") == "":
						currentHeads.Add(1)
						respondPage(w, "current-history", 9, true, question, picture, tool, result, thought, narration, cut)
					default:
						t.Error("bad current paging cursor", q)
						w.WriteHeader(http.StatusBadRequest)
					}
				case earlierPath:
					if q.Get("limit") != "1" {
						earlierHeads.Add(1)
					}
					respondPage(w, "earlier-history", 3, false,
						fixtureItem("old", 1, 10, "assistant", "text", "Copied before switch"),
						fixtureItem("middle", 2, 20, "assistant", "text", "Middle stretch"),
						fixtureItem("detached", 3, 21, "assistant", "text", "Detached later run"))
				case currentPath + "/answer%230/full":
					item := cut
					item.Text, item.FullSize = whole, 0
					respondJSON(w, struct {
						proto.Item
						StrictRead      bool `json:"strict_read"`
						SourceTextBytes int  `json:"source_text_bytes"`
					}{item, true, len(item.Text)})
				default:
					t.Error("unexpected request", r.URL.EscapedPath())
					w.WriteHeader(http.StatusNotFound)
				}
			})
			var options []string
			if steps {
				options = append(options, "--include-steps")
			}
			if report, err := f.run(options...); err != nil || !strings.Contains(report, "Saved the conversation") {
				t.Fatalf("export failed: %v %s", err, report)
			}
			data, err := os.ReadFile(f.output)
			if err != nil {
				t.Fatal(err)
			}
			text := string(data)
			last := -1
			for _, want := range []string{"First stretch", "Middle stretch", "Resume the decision", "Image attachment", whole} {
				at := strings.Index(text, want)
				if at <= last || strings.Count(text, want) != 1 {
					t.Fatalf("missing, duplicated or out of order near %q", want[:min(len(want), 30)])
				}
				last = at
			}
			for _, excluded := range []string{"Later append", "Copied before switch", "Detached later run", "preview", fixtureToken} {
				if strings.Contains(text, excluded) {
					t.Fatalf("unwanted text %q", excluded)
				}
			}
			for _, content := range []string{"Step result", "reasoning", "intermediate update", "fixture-command", "<details>"} {
				if strings.Contains(text, content) != steps {
					t.Errorf("%q did not respect the work steps option", content)
				}
			}
			if currentHeads.Load() != 1 || earlierHeads.Load() != 1 {
				t.Fatal("a repeated session was collected twice")
			}
			info, err := os.Stat(f.output)
			if err != nil || runtime.GOOS != "windows" && info.Mode().Perm() != 0o600 {
				t.Fatalf("output permissions: %v %v", info, err)
			}
			if report, err := f.run(options...); err == nil || !strings.Contains(report, "already exists") {
				t.Fatalf("existing file was accepted: %s %v", report, err)
			}
			again, _ := os.ReadFile(f.output)
			if !bytes.Equal(data, again) {
				t.Fatal("existing file changed")
			}
		})
	}
}

func TestGenerationChangeRestartsTheWholeReadOnce(t *testing.T) {
	for _, steady := range []bool{true, false} {
		t.Run(map[bool]string{true: "recovers", false: "rejects repeated rewrite"}[steady], func(t *testing.T) {
			var heads atomic.Int32
			f := newFixtureHub(t, []string{"sessions_lookup"}, func(w http.ResponseWriter, r *http.Request) {
				switch {
				case r.URL.Path == "/mount/api/v1/sessions":
					respondRow(t, w, r, sessionRow{Machine: fixtureMachine, ID: fixtureCurrent, Assistant: true})
				case r.URL.Query().Get("before_seq") == "2":
					token := "two"
					if !steady && heads.Load() == 2 {
						token = "three"
					}
					respondPage(w, token, 1, false, fixtureItem("first", 1, 2, "user", "text", "First question"))
				case r.URL.Query().Get("limit") == "1":
					respondPage(w, "two", 2, true, fixtureItem("answer", 2, 3, "assistant", "text", "New generation"))
				default:
					token, answer := "one", "Old generation"
					if heads.Add(1) == 2 {
						token, answer = "two", "New generation"
					}
					respondPage(w, token, 2, true, fixtureItem("answer", 2, 3, "assistant", "text", answer))
				}
			})
			report, err := f.run()
			if heads.Load() != 2 || !strings.Contains(report, "Reading it again") {
				t.Fatalf("wrong number of attempts: %d %q", heads.Load(), report)
			}
			if !steady {
				if err == nil || !strings.Contains(report, "changed during export") {
					t.Fatalf("allowed an unstable conversation: %v %s", err, report)
				}
				f.noFile()
				return
			}
			doc, readErr := os.ReadFile(f.output)
			if err != nil || readErr != nil || bytes.Contains(doc, []byte("Old generation")) ||
				!bytes.Contains(doc, []byte("New generation")) || !bytes.Contains(doc, []byte("### Persona")) {
				t.Fatalf("wrong recovered document: %s %v %v", report, err, readErr)
			}
		})
	}
}

func TestIncompleteOrUnauthorizedExportPublishesNothing(t *testing.T) {
	for _, scenario := range []string{"unsupported", "unauthorized", "stalled", "overlap", "wrong boundary", "missing span", "missing strict", "invalid full", "redirect"} {
		t.Run(scenario, func(t *testing.T) {
			var leaked atomic.Bool
			redirect := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				leaked.Store(true)
				w.WriteHeader(http.StatusUnauthorized)
			}))
			defer redirect.Close()
			var features []string
			if scenario != "unsupported" {
				features = []string{"sessions_lookup"}
			}
			f := newFixtureHub(t, features, func(w http.ResponseWriter, r *http.Request) {
				if r.URL.Path == "/mount/api/v1/sessions" {
					switch scenario {
					case "unsupported":
						t.Error("used a capability before checking it")
					case "unauthorized":
						w.WriteHeader(http.StatusForbidden)
						respondJSON(w, map[string]string{"error": fixtureToken})
						return
					case "redirect":
						http.Redirect(w, r, redirect.URL, http.StatusFound)
						return
					}
					respondRow(t, w, r, sessionRow{Machine: fixtureMachine, ID: fixtureCurrent})
					return
				}
				item := fixtureItem("item", 2, 1, "assistant", "text", "preview")
				switch scenario {
				case "missing span":
					respondJSON(w, map[string]any{"items": []proto.Item{item}, "has_more": false, "strict_read": true})
				case "missing strict":
					respondJSON(w, map[string]any{"items": []proto.Item{item}, "has_more": false, "history": "old", "to_seq": 2})
				case "wrong boundary":
					if r.URL.Query().Get("before_seq") == "" {
						item.Seq = 4
						respondPage(w, "same", 4, true, item)
					} else {
						respondPage(w, "same", 2, false, item)
					}
				case "stalled", "overlap":
					if r.URL.Query().Get("before_seq") == "" {
						respondPage(w, "same", 2, true, item)
					} else if scenario == "stalled" {
						respondPage(w, "same", 1, false)
					} else {
						respondPage(w, "same", 2, false, item)
					}
				case "invalid full":
					item.FullSize = 200
					if strings.HasSuffix(r.URL.Path, "/full") {
						item.ID, item.FullSize, item.Text = "other item", 0, "other content"
						respondJSON(w, struct {
							proto.Item
							StrictRead      bool `json:"strict_read"`
							SourceTextBytes int  `json:"source_text_bytes"`
						}{item, true, len(item.Text)})
					} else {
						respondPage(w, "same", 2, false, item)
					}
				default:
					t.Error("unexpected items read")
					w.WriteHeader(http.StatusNotFound)
				}
			})
			report, err := f.run()
			if err == nil || strings.Contains(report, fixtureToken) || leaked.Load() {
				t.Fatalf("bad result: %v %q, redirect followed: %t", err, report, leaked.Load())
			}
			f.noFile()
		})
	}
}

func TestFullReadRetriesOnlyAfterAProvenRewrite(t *testing.T) {
	for _, rewrite := range []bool{false, true} {
		t.Run(map[bool]string{false: "keeps an ordinary read failure", true: "reads the replacement"}[rewrite], func(t *testing.T) {
			var heads atomic.Int32
			clipped := fixtureItem("old item", 1, 1, "assistant", "text", "preview")
			clipped.FullSize = 100
			f := newFixtureHub(t, []string{"sessions_lookup"}, func(w http.ResponseWriter, r *http.Request) {
				switch {
				case r.URL.Path == "/mount/api/v1/sessions":
					respondRow(t, w, r, sessionRow{Machine: fixtureMachine, ID: fixtureCurrent})
				case strings.HasSuffix(r.URL.Path, "/full"):
					if r.URL.Query().Get("strict") != "1" {
						t.Error("full item was not required to be fresh")
					}
					w.WriteHeader(http.StatusNotFound)
				case r.URL.Query().Get("limit") == "1":
					if rewrite {
						respondPage(w, "changed", 1, false, fixtureItem("new item", 1, 2, "assistant", "text", "new answer"))
					} else {
						respondPage(w, "old", 1, false, clipped)
					}
				default:
					if heads.Add(1) == 2 {
						respondPage(w, "changed", 1, false, fixtureItem("new item", 1, 2, "assistant", "text", "new answer"))
					} else {
						respondPage(w, "old", 1, false, clipped)
					}
				}
			})
			report, err := f.run()
			if !rewrite {
				if err == nil || heads.Load() != 1 || !strings.Contains(report, "HTTP 404") {
					t.Fatalf("ordinary failure was not preserved: %q %v", report, err)
				}
				f.noFile()
				return
			}
			data, fileErr := os.ReadFile(f.output)
			if err != nil || fileErr != nil || heads.Load() != 2 ||
				!bytes.Contains(data, []byte("new answer")) || bytes.Contains(data, []byte("preview")) {
				t.Fatalf("rewritten full item read: %q %v %v", report, err, fileErr)
			}
		})
	}
}

func TestWorkDetailsClipAtUnicodeBoundariesAndUseSafeFences(t *testing.T) {
	const embeddedFence = "````"
	body := strings.Repeat("好", 4_001) + "hidden tail"
	output := fixtureItem("result", 1, 1, "assistant", "tool_result", embeddedFence+"\n"+body)
	input := fixtureItem("call", 1, 1, "assistant", "tool_use", "")
	input.ToolName = "fixture tool"
	input.ToolInput = json.RawMessage(`{"value":"` + body + `"}`)
	var doc bytes.Buffer
	err := writeMarkdown(&doc, i18n.En, conversation{stretches: []stretch{{items: []proto.Item{input, output}}}}, true, fixtureTime)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Count(doc.String(), "More of this work step") != 2 || strings.Contains(doc.String(), "hidden tail") ||
		!strings.Contains(doc.String(), strings.Repeat("`", 5)+"\n"+embeddedFence+"\n") || !utf8.Valid(doc.Bytes()) {
		t.Fatal("tool report lost its fence, omission marker or UTF-8 boundary")
	}
}
