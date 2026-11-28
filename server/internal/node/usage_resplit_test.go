package node

import (
	"context"
	"encoding/json"
	"io"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

// resplitManager answers the re-split's calls as a manager that keeps directories does (status: the range), or as an
// older one (an unknown method), and hands over each usage.resplit request.
type resplitManager struct {
	answers       io.Writer
	since, before int64
	knows         bool
	requests      chan json.RawMessage
}

func (w resplitManager) Write(p []byte) (int, error) {
	var call struct {
		ID     string          `json:"id"`
		Method string          `json:"method"`
		Params json.RawMessage `json:"params"`
	}
	if json.Unmarshal(p, &call) != nil || call.Method == "" {
		return len(p), nil
	}
	reply := `{"id":"` + call.ID + `","error":{"message":"unknown method"}}`
	switch {
	case !w.knows:
	case call.Method == "usage.resplit.status":
		b, _ := json.Marshal(map[string]any{"done": false, "since": w.since, "before": w.before})
		reply = `{"id":"` + call.ID + `","result":` + string(b) + `}`
	case call.Method == "usage.resplit":
		w.requests <- call.Params
		reply = `{"id":"` + call.ID + `","result":{"moved":1}}`
	}
	go func() { _, _ = w.answers.Write([]byte(reply + "\n")) }()
	return len(p), nil
}

// Once, the node re-reads the range the manager names by the ten minutes, each bucket under the directory its
// session started in, and sends it with the last ten minutes and done; with an older manager it sends nothing (#16530).
func TestResplitUsage(t *testing.T) {
	old := resplitDelay
	resplitDelay = 0
	t.Cleanup(func() { resplitDelay = old })
	for _, knows := range []bool{true, false} {
		config := filepath.Join(t.TempDir(), "claude-fixture")
		t.Setenv("CLAUDE_CONFIG_DIR", config)
		project := filepath.Join(config, "projects", "fixture")
		if err := os.MkdirAll(project, 0o700); err != nil {
			t.Fatal(err)
		}
		now := time.Now().UTC()
		at := now.Add(-30 * time.Hour)
		body := `{"type":"user","cwd":"/home/tester/code/shop","timestamp":"` + at.Format(time.RFC3339Nano) + `"}` + "\n" +
			claudeReply(at, "r1", "fixture-model", 1, 0, 0, 2)
		if err := os.WriteFile(filepath.Join(project, "sess.jsonl"), []byte(body), 0o600); err != nil {
			t.Fatal(err)
		}
		in, answers := io.Pipe()
		since, before := now.Add(-48*time.Hour).Truncate(resplitStep), now.Add(-26*time.Hour).Truncate(resplitStep)
		w := resplitManager{answers: answers, since: since.Unix(), before: before.Unix(), knows: knows, requests: make(chan json.RawMessage, 4)}
		n := &Node{home: "/home/tester", dir: t.TempDir(), state: newState(), logger: log.New(io.Discard, "", 0)}
		n.peer = rpc.New("resplit-test", w)
		go func() { _ = n.peer.Serve(context.Background(), in) }()
		n.usageDirs.p.Store(newUsageDirs(n.home)) // as the meter does with a manager that keeps directories
		ready := make(chan struct{})
		close(ready)
		n.resplitUsage(context.Background(), ready)
		n.peer.Close(io.EOF)
		_ = answers.Close()
		if !knows {
			if len(w.requests) != 0 {
				t.Fatal("re-split sent to a manager that does not know it")
			}
			continue
		}
		select {
		case raw := <-w.requests:
			var req struct {
				Buckets []proto.UsageBucket `json:"buckets"`
				Through int64               `json:"through"`
				Done    bool                `json:"done"`
			}
			if err := json.Unmarshal(raw, &req); err != nil {
				t.Fatal(err)
			}
			slot := at.Unix() / 600 * 600
			if !req.Done || req.Through != before.Unix()-600 || len(req.Buckets) != 1 || req.Buckets[0].Minute != slot ||
				req.Buckets[0].Dir != "~/code/shop" || req.Buckets[0].Output != 2 {
				t.Fatalf("request %s", raw)
			}
		default:
			t.Fatal("no re-split request")
		}
	}
}

// resplitPassManager answers usage.resplit.status with its statuses in turn (then done), and hands over what each
// status call and usage.resplit request said.
type resplitPassManager struct {
	answers  io.Writer
	statuses chan map[string]any
	asks     chan json.RawMessage
	requests chan json.RawMessage
}

func (w resplitPassManager) Write(p []byte) (int, error) {
	var call struct {
		ID     string          `json:"id"`
		Method string          `json:"method"`
		Params json.RawMessage `json:"params"`
	}
	if json.Unmarshal(p, &call) != nil || call.Method == "" {
		return len(p), nil
	}
	reply := `{"id":"` + call.ID + `","error":{"message":"unknown method"}}`
	switch call.Method {
	case "usage.resplit.status":
		w.asks <- call.Params
		status := map[string]any{"done": true}
		select {
		case status = <-w.statuses:
		default:
		}
		b, _ := json.Marshal(status)
		reply = `{"id":"` + call.ID + `","result":` + string(b) + `}`
	case "usage.resplit":
		w.requests <- call.Params
		reply = `{"id":"` + call.ID + `","result":{"moved":1}}`
	}
	go func() { _, _ = w.answers.Write([]byte(reply + "\n")) }()
	return len(p), nil
}

// A node that names temporary folders by their paths says so, and after the first pass does the temporary folders'
// one the manager names: only the sessions that started in a temporary folder, with what their folders resolve to
// (a worktree there: its repository). One that does not name them never does that pass (#16827).
func TestResplitUsageTempPass(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("no git")
	}
	t.Setenv("GIT_CONFIG_GLOBAL", "/dev/null")
	t.Setenv("GIT_CONFIG_NOSYSTEM", "1")
	old := resplitDelay
	resplitDelay = 0
	t.Cleanup(func() { resplitDelay = old })
	base := t.TempDir()
	tmp := usageMkdir(t, base, "tmp")
	usageTempRootsAre(t, tmp)
	app := usageMkdir(t, base, "repos", "app")
	usageGit(t, app, "init", "-q")
	usageGit(t, app, "remote", "add", "origin", "https://github.com/acme/app.git")
	usageGit(t, app, "commit", "-q", "--allow-empty", "-m", "first")
	lane := filepath.Join(tmp, "app-lane-x")
	usageGit(t, app, "worktree", "add", "-q", lane)

	for _, temps := range []bool{true, false} {
		config := filepath.Join(t.TempDir(), "claude-fixture")
		t.Setenv("CLAUDE_CONFIG_DIR", config)
		project := usageMkdir(t, config, "projects", "fixture")
		now := time.Now().UTC()
		at := now.Add(-30 * time.Hour)
		for name, cwd := range map[string]string{"shop.jsonl": "/home/tester/code/shop", "lane.jsonl": lane} {
			body := `{"type":"user","cwd":"` + cwd + `","timestamp":"` + at.Format(time.RFC3339Nano) + `"}` + "\n" +
				claudeReply(at, "r-"+name, "fixture-model", 1, 0, 0, 2)
			if err := os.WriteFile(filepath.Join(project, name), []byte(body), 0o600); err != nil {
				t.Fatal(err)
			}
		}
		in, answers := io.Pipe()
		since, before := now.Add(-48*time.Hour).Truncate(resplitStep), now.Add(-26*time.Hour).Truncate(resplitStep)
		w := resplitPassManager{answers: answers, statuses: make(chan map[string]any, 2), asks: make(chan json.RawMessage, 4),
			requests: make(chan json.RawMessage, 4)}
		w.statuses <- map[string]any{"done": false, "since": since.Unix(), "before": before.Unix()}
		w.statuses <- map[string]any{"done": false, "since": since.Unix(), "before": before.Unix(), "pass": "temp"}
		n := &Node{home: "/home/tester", dir: t.TempDir(), state: newState(), logger: log.New(io.Discard, "", 0)}
		n.peer = rpc.New("resplit-test", w)
		go func() { _ = n.peer.Serve(context.Background(), in) }()
		dirs := newUsageDirs(n.home)
		dirs.temps = temps
		n.usageDirs.p.Store(dirs)
		ready := make(chan struct{})
		close(ready)
		n.resplitUsage(context.Background(), ready)
		n.peer.Close(io.EOF)
		_ = answers.Close()

		type request struct {
			Buckets []proto.UsageBucket `json:"buckets"`
			Dirs    []proto.UsageDir    `json:"dirs"`
			Pass    string              `json:"pass"`
			Done    bool                `json:"done"`
		}
		var got []request
		for len(w.requests) > 0 {
			var r request
			if err := json.Unmarshal(<-w.requests, &r); err != nil {
				t.Fatal(err)
			}
			got = append(got, r)
		}
		for len(w.asks) > 0 {
			if ask := string(<-w.asks); ask != map[bool]string{true: `{"temp_dirs":true}`, false: `{}`}[temps] {
				t.Errorf("temps=%v: asked %s", temps, ask)
			}
		}
		if !temps { // the first pass, under :temp as before; the temporary folders' never
			if len(got) != 1 || got[0].Pass != "" || len(got[0].Buckets) != 2 {
				t.Fatalf("requests %+v", got)
			}
			continue
		}
		if len(got) != 2 || got[0].Pass != "" || len(got[0].Buckets) != 2 || !got[0].Done {
			t.Fatalf("first pass %+v", got)
		}
		temp := got[1]
		if temp.Pass != "temp" || !temp.Done || len(temp.Buckets) != 1 || temp.Buckets[0].Dir != lane || temp.Buckets[0].Output != 2 ||
			len(temp.Dirs) != 1 || temp.Dirs[0].Project != "git:github.com/acme/app" || !temp.Dirs[0].Worktree {
			t.Fatalf("temporary folders' pass %+v", temp)
		}
	}
}
