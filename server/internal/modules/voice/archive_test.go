package voice

import (
	"context"
	"io"
	"log"
	"net/http"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
)

// archiveUpstream is a service that answers each typed line with a caption of the user's and a reply of its own.
func archiveUpstream(t *testing.T, caption, reply string) *fakeUpstream {
	return newFakeUpstream(t, func(u *fakeUpstream, c *websocket.Conn, f map[string]any) {
		switch f["type"] {
		case "session.start":
			u.send(c, map[string]any{"type": "session.started", "session": map[string]any{}})
		case "session.commentary.append":
			if f["delegation_id"] != nil || f["event_id"] != nil {
				return
			}
			u.send(c, map[string]any{"type": "session.input_transcript.delta", "delta": caption})
			u.send(c, map[string]any{"type": "session.output_transcript.delta", "delta": reply})
		}
	})
}

// archivedCall runs a call with cfg's archive settings: the user types a line, the service answers it with a caption
// of the user's and a reply of its own; hangUp ends it (the app hangs up, or the extension stops).
func archivedCall(t *testing.T, cfg Config, hub *fakeHub, typed, caption, reply string, hangUp func(*Plugin, *app)) (*Plugin, *logCapture) {
	t.Helper()
	up := archiveUpstream(t, caption, reply)
	cfg.Providers = []Provider{{ID: "rt", URL: up.url(), Models: []string{"m1"}, Tools: new(true)}}
	pl := testPlugin(t, cfg, hub.server(t))
	logs := captureLog(t, pl)
	a := dialApp(t, pl, boundStart("a1", nil))
	a.until(ofType("ready"))
	a.send(map[string]any{"type": "text", "text": typed})
	a.until(func(f map[string]any) bool {
		return f["type"] == "transcript" && f["role"] == "assistant" && f["text"] == reply
	})
	hangUp(pl, a)
	logs.wait("call 1: conversation closed")
	return pl, logs
}

func appHangsUp(_ *Plugin, a *app) { _ = a.c.Close(websocket.StatusNormalClosure, "bye") }

func hubFiles(hub *fakeHub) (map[string]string, []string) {
	hub.mu.Lock()
	defer hub.mu.Unlock()
	files := map[string]string{}
	for k, v := range hub.files {
		files[k] = v
	}
	return files, slices.Clone(hub.dirs)
}

// With archive_machine set, a call's lines go to that computer as one Markdown file when it ends, in the order they
// were said, each with its time and whose it is; the folder is made first; the log says how many lines went where
// and nothing of what was said (#13249).
func TestArchiveWritesTheCallToTheComputer(t *testing.T) {
	const (
		typed   = "zebra-typed-41 what is on today"
		caption = "zebra-caption-52 and tomorrow"
		reply   = "ocelot-reply-13 a meeting at three"
	)
	hub := agentHub(t)
	pl, logs := archivedCall(t, Config{ArchiveMachine: "mac", ArchiveDir: "~/PersonaCalls"}, hub, typed, caption, reply, appHangsUp)
	line := logs.wait("call 1: archived ", " lines to mac:~/PersonaCalls/")
	files, dirs := hubFiles(hub)
	if len(files) != 1 {
		t.Fatalf("files %v", files)
	}
	if !slices.Equal(dirs, []string{"mac:~/PersonaCalls"}) {
		t.Errorf("folders made %v", dirs)
	}
	var key, text string
	for k, v := range files {
		key, text = k, v
	}
	if !strings.HasPrefix(key, "mac:~/PersonaCalls/") || !strings.HasSuffix(key, "-call1-"+pl.nonce+".md") || len(pl.nonce) != 4 {
		t.Errorf("file %s (run %q)", key, pl.nonce)
	}
	for _, want := range []string{"---\ncall: 1\nstarted: ", "\nresumed: false\n", "\nconversation: m/a1\n", "\nmodel: rt/m1\n", "\nlines: 3\n"} {
		if !strings.Contains(text, want) {
			t.Errorf("file lacks %q:\n%s", want, text)
		}
	}
	iTyped, iCaption, iReply := strings.Index(text, "] "+typed), strings.Index(text, "] "+caption), strings.Index(text, "] "+reply)
	if iTyped < 0 || iCaption < 0 || iReply < 0 || !(iTyped < iCaption && iCaption < iReply) {
		t.Fatalf("lines out of order or missing:\n%s", text)
	}
	for _, want := range []string{" [user] " + typed, " [user] " + caption, " [assistant] " + reply} {
		if !strings.Contains(text, want) {
			t.Errorf("file lacks %q:\n%s", want, text)
		}
	}
	if !strings.Contains(line, "archived 3 lines") {
		t.Errorf("log line %s", line)
	}
	all := logs.String()
	for _, words := range []string{"zebra-typed", "zebra-caption", "ocelot-reply"} {
		if strings.Contains(all, words) {
			t.Errorf("the log has %q:\n%s", words, all)
		}
	}
}

// A call still under way when the extension stops is ended then and its lines written (the server does not wait
// for its WebSockets).
func TestArchiveWhenTheExtensionStops(t *testing.T) {
	hub := agentHub(t)
	_, logs := archivedCall(t, Config{ArchiveMachine: "mac"}, hub, "typed line", "a caption", "the reply", func(pl *Plugin, _ *app) {
		pl.endOpenCalls()
		pl.waitArchives(5 * time.Second)
	})
	logs.wait("call 1: conversation closed", "the extension stopped")
	logs.wait("call 1: archived 3 lines to mac:~/PersonaCalls/")
	if files, _ := hubFiles(hub); len(files) != 1 {
		t.Fatalf("files %v", files)
	}
}

// Without archive_machine nothing is written anywhere.
func TestArchiveOffWritesNothing(t *testing.T) {
	hub := agentHub(t)
	_, logs := archivedCall(t, Config{}, hub, "hello", "and you", "fine", appHangsUp)
	// Off, shutdown starts no write at all (archive() is nil before any goroutine), so nothing can come later.
	if files, dirs := hubFiles(hub); len(files) != 0 || len(dirs) != 0 {
		t.Fatalf("wrote %v %v", files, dirs)
	}
	if strings.Contains(logs.String(), "archive") {
		t.Errorf("log mentions an archive:\n%s", logs.String())
	}
}

func archivePlugin(t *testing.T, hub *fakeHub) (*Plugin, *logCapture) {
	t.Helper()
	srv := hub.server(t)
	pl := New(Config{ArchiveMachine: "mac"}, srv.URL, "tok", log.New(io.Discard, t.Name()+": ", 0))
	return pl, captureLog(t, pl)
}

func testArchive() *callArchive {
	at := time.Date(2026, 11, 23, 3, 15, 2, 0, time.Local)
	return &callArchive{id: 7, opened: at, ended: at.Add(time.Minute), machine: "mac", dir: DefaultArchiveDir, nonce: "ab12",
		lines: []keptLine{{role: "user", text: "hi", at: at.Add(time.Second)}}}
}

const testArchiveName = "~/PersonaCalls/2026-11-23-031502-call7-ab12.md"

// A computer that cannot be reached (asleep, offline) is tried again; the file lands once it can.
func TestArchiveRetriesUntilTheComputerAnswers(t *testing.T) {
	setVar(t, &archiveRetryWaits, []time.Duration{10 * time.Millisecond})
	hub := agentHub(t)
	tries := 0
	hub.onWrite = func(string, string) (int, string) {
		if tries++; tries < 3 {
			return http.StatusBadGateway, "machine_offline"
		}
		return 0, ""
	}
	pl, logs := archivePlugin(t, hub)
	pl.writeArchive(context.Background(), testArchive())
	logs.wait("call 7: archived 1 lines to mac:" + testArchiveName)
	if tries != 3 {
		t.Errorf("tries %d", tries)
	}
}

// A try whose answer was lost (the node wrote the file, the hub's answer did not come back) is not written a second
// time: the name taken on the next try is this file's own.
func TestArchiveLostAnswerIsNotWrittenTwice(t *testing.T) {
	setVar(t, &archiveRetryWaits, []time.Duration{10 * time.Millisecond})
	hub := agentHub(t)
	tries := 0
	hub.onWrite = func(mid, p string) (int, string) {
		if tries++; tries == 1 {
			hub.mu.Lock()
			hub.files = map[string]string{mid + ":" + p: "written"}
			hub.mu.Unlock()
			return http.StatusBadGateway, "timeout"
		}
		return 0, ""
	}
	pl, logs := archivePlugin(t, hub)
	pl.writeArchive(context.Background(), testArchive())
	logs.wait("call 7: archived 1 lines to mac:"+testArchiveName, "(an earlier try wrote it)")
	if files, _ := hubFiles(hub); len(files) != 1 {
		t.Errorf("files %v", files)
	}
}

// A refusal that another try cannot change (the computer's write guard, a bad path, a name taken on the first try)
// is not tried again; past archiveRetryFor the file is dropped; the log says the hub's status and code only.
func TestArchiveGivesUp(t *testing.T) {
	hub := agentHub(t)
	tries := 0
	hub.onWrite = func(string, string) (int, string) { tries++; return http.StatusForbidden, "read_only" }
	pl, logs := archivePlugin(t, hub)
	pl.writeArchive(context.Background(), testArchive())
	logs.wait("call 7: archive not written to mac: HTTP 403 read_only")
	if tries != 1 {
		t.Errorf("tries %d", tries)
	}

	hub.mu.Lock()
	hub.onWrite = nil
	hub.files = map[string]string{"mac:" + testArchiveName: "another"}
	hub.mu.Unlock()
	pl.writeArchive(context.Background(), testArchive())
	logs.wait("call 7: archive not written to mac: HTTP 409 exists")
	if files, _ := hubFiles(hub); files["mac:"+testArchiveName] != "another" {
		t.Error("replaced the file that was there")
	}

	setVar(t, &archiveRetryFor, 30*time.Millisecond)
	setVar(t, &archiveRetryWaits, []time.Duration{10 * time.Millisecond})
	hub.mu.Lock()
	hub.onWrite = func(string, string) (int, string) { return http.StatusServiceUnavailable, "assistant_offline" }
	hub.mu.Unlock()
	pl.writeArchive(context.Background(), testArchive())
	logs.wait("call 7: archive dropped after ", "HTTP 503 assistant_offline")
}

// Too many archives waiting for their computer: the next is dropped, not held.
func TestArchivePendingLimit(t *testing.T) {
	hub := agentHub(t)
	pl, logs := archivePlugin(t, hub)
	pl.archN = archivePendingMost
	pl.startArchive(testArchive())
	logs.wait("call 7: archive dropped: 50 archives wait for their computer already")
	if pl.archN != archivePendingMost {
		t.Errorf("waiting %d", pl.archN)
	}
}

// Every folder of archive_dir below the home or the root is made when it is not there; those above the home, which
// the write guard refuses to make, are left to the write.
func TestArchiveMakesNestedFolders(t *testing.T) {
	hub := agentHub(t)
	pl, logs := archivePlugin(t, hub)
	a := testArchive()
	a.dir = "~/notes/calls"
	pl.writeArchive(context.Background(), a)
	logs.wait("call 7: archived 1 lines to mac:~/notes/calls/")
	a.dir = "/Users/me/calls"
	pl.writeArchive(context.Background(), a)
	logs.wait("call 7: archived 1 lines to mac:/Users/me/calls/")
	if _, dirs := hubFiles(hub); !slices.Equal(dirs, []string{"mac:~/notes", "mac:~/notes/calls", "mac:/Users/me/calls"}) {
		t.Errorf("folders %v", dirs)
	}
}

// keep puts a user's line before the model's lines that began after they stopped (their caption comes late), as
// what was said is kept for the model; past archiveLinesMost lines or archiveBytesMost bytes later lines are
// counted, not kept; nothing is kept with the archive off.
func TestKeepOrder(t *testing.T) {
	t0 := time.Now()
	c := &conversation{pl: &Plugin{cfg: Config{ArchiveMachine: "mac"}}}
	c.keep("assistant", "a1", t0)
	c.keep("assistant", "a2", t0.Add(2*time.Second))
	c.keep("user", "u1", t0.Add(time.Second))
	c.keep("user", "  ", t0)
	var got []string
	for _, l := range c.kept {
		got = append(got, l.text)
	}
	if strings.Join(got, ",") != "a1,u1,a2" {
		t.Fatalf("order %v", got)
	}
	for range archiveLinesMost {
		c.keep("user", "x", time.Now())
	}
	if len(c.kept) != archiveLinesMost || c.keptDropped != 3 {
		t.Errorf("kept %d dropped %d", len(c.kept), c.keptDropped)
	}
	big := &conversation{pl: c.pl}
	big.keep("user", strings.Repeat("y", archiveBytesMost-1), t0)
	big.keep("assistant", "zz", t0)
	big.keep("assistant", "z", t0)
	if len(big.kept) != 2 || big.keptDropped != 1 {
		t.Errorf("kept %d dropped %d", len(big.kept), big.keptDropped)
	}
	off := &conversation{pl: &Plugin{}}
	off.keep("user", "x", t0)
	if len(off.kept) != 0 {
		t.Error("kept with the archive off")
	}
}

// The file's lines are one line each (a line's own line breaks become spaces); a call cut short says how many lines
// it did not keep.
func TestArchiveMarkdown(t *testing.T) {
	a := testArchive()
	a.resumed, a.target, a.model, a.dropped = true, "mac/s1", "rt/m1", 4
	a.lines = append(a.lines, keptLine{role: "assistant", text: "two\nlines\r\nhere", at: a.opened.Add(2 * time.Second)})
	got := a.markdown()
	for _, want := range []string{"resumed: true\n", "conversation: mac/s1\n", "lines: 2\nlines_not_kept: 4\n---\n\n",
		"- 03:15:03 [user] hi\n", "- 03:15:04 [assistant] two lines here\n"} {
		if !strings.Contains(got, want) {
			t.Errorf("lacks %q:\n%s", want, got)
		}
	}
}

// The settings form's archive fields are taken over hub.json's; a folder that is neither in the home nor absolute
// turns the archive off with a warning and leaves the rest of the configuration working.
func TestArchiveConfig(t *testing.T) {
	hubJSON := []byte(`{"providers":[{"id":"rt","url":"wss://x/v1/live","key":"k"}],"archive_machine":"m1","archive_dir":"/data/calls"}`)
	c, err := Merge(Sources{HubJSON: hubJSON, Form: []byte(`{"archive_machine":" mac "}`)})
	if err != nil || c.ArchiveMachine != "mac" || c.ArchiveDir != "/data/calls" {
		t.Fatalf("%+v %v", c, err)
	}
	c, err = Merge(Sources{HubJSON: hubJSON, Form: []byte(`{"archive_dir":"calls"}`)})
	if err != nil || c.ArchiveMachine != "" || !slices.Contains(c.Warnings, WarnArchiveDir) || len(c.Providers) != 1 {
		t.Errorf("%+v %v", c, err)
	}
}
