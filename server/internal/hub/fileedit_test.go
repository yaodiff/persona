package hub

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

// fileEditHub serves the four routes over a real rpc pair whose node answers with answer.
func fileEditHub(t *testing.T, answer func(method string, params map[string]any) (any, error)) http.Handler {
	t.Helper()
	h := newTestHub(t)
	toNode, nodeIn := io.Pipe()
	toHub, hubIn := io.Pipe()
	hubPeer := rpc.New("h", nodeIn)
	nodePeer := rpc.New("n", hubIn)
	nodePeer.OnRequest = func(_ context.Context, method string, params json.RawMessage) (any, error) {
		var p map[string]any
		_ = json.Unmarshal(params, &p)
		return answer(method, p)
	}
	ctx, cancel := context.WithCancel(context.Background())
	t.Cleanup(func() { cancel(); toNode.Close(); toHub.Close() })
	go hubPeer.Serve(ctx, toHub)
	go nodePeer.Serve(ctx, toNode)
	m := newMachine(h, MachineConfig{ID: "m1", Name: "box"})
	m.peer = hubPeer
	h.byID["m1"] = m
	mux := http.NewServeMux()
	for _, op := range []string{"rename", "move", "trash", "restore", "mkdir", "write", "zip", "copy", "chmod"} {
		mux.HandleFunc("POST /api/v1/machines/{mid}/files/"+op, h.fileEdit(op))
	}
	return mux
}

func postEdit(mux http.Handler, op, body string) (int, map[string]any) {
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, httptest.NewRequest("POST", "/api/v1/machines/m1/files/"+op, strings.NewReader(body)))
	var out map[string]any
	_ = json.Unmarshal(rec.Body.Bytes(), &out)
	return rec.Code, out
}

// Each route reaches the node's file.<op> with the body's fields and returns its answer as it is.
func TestFileEditPassesThrough(t *testing.T) {
	var got []string
	mux := fileEditHub(t, func(method string, p map[string]any) (any, error) {
		b, _ := json.Marshal(p)
		got = append(got, method+" "+string(b))
		return map[string]any{"ok": method}, nil
	})
	for _, c := range []struct{ op, body string }{
		{"rename", `{"path":"~/a.txt","cwd":"/w","name":"b.txt"}`},
		{"move", `{"paths":["~/a","~/b"],"cwd":"/w","dir":"~/d","conflict":"keep"}`},
		{"trash", `{"paths":["~/a"],"cwd":"/w"}`},
		{"restore", `{"trash_ids":["t1"]}`},
	} {
		if code, out := postEdit(mux, c.op, c.body); code != 200 || out["ok"] != "file."+c.op {
			t.Fatalf("%s: %d %v", c.op, code, out)
		}
	}
	want := []string{
		`file.rename {"cwd":"/w","name":"b.txt","path":"~/a.txt"}`,
		`file.move {"conflict":"keep","cwd":"/w","dir":"~/d","paths":["~/a","~/b"]}`,
		`file.trash {"cwd":"/w","paths":["~/a"]}`,
		`file.restore {"trash_ids":["t1"]}`,
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("node got:\n%s", strings.Join(got, "\n"))
	}
}

// What can't be a request is 400 before the node hears of it.
func TestFileEditRefusesBadRequests(t *testing.T) {
	mux := fileEditHub(t, func(method string, _ map[string]any) (any, error) {
		t.Errorf("%s reached the node", method)
		return nil, nil
	})
	for _, c := range []struct{ op, body string }{
		{"rename", `{"name":"b"}`},
		{"rename", `{"path":"~/a","name":"x/y"}`},
		{"rename", `{"path":"~/a","name":".."}`},
		{"move", `{"paths":["~/a"]}`},
		{"move", `{"paths":[],"dir":"~"}`},
		{"move", `{"paths":["~/a"],"dir":"~","conflict":"overwrite"}`},
		{"trash", `{"paths":[""]}`},
		{"restore", `{}`},
		{"trash", `not json`},
	} {
		if code, _ := postEdit(mux, c.op, c.body); code != 400 {
			t.Errorf("%s %s: %d", c.op, c.body, code)
		}
	}
}

// The node's refusals come out as the app acts on them: read_only and exists (with the name) 409, not_found 404,
// a node from before these 409 node_outdated, anything else 502.
func TestFileEditErrors(t *testing.T) {
	var fail error
	mux := fileEditHub(t, func(string, map[string]any) (any, error) { return nil, fail })
	for _, c := range []struct {
		err    error
		status int
		code   string
	}{
		{&rpc.Error{Code: proto.CodeReadOnly, Message: "read-only"}, 409, "read_only"},
		{&rpc.Error{Code: proto.CodeExists, Message: "taken", Data: json.RawMessage(`{"name":"a.txt"}`)}, 409, "exists"},
		{&rpc.Error{Code: proto.CodeNotFound, Message: "~/a is not there"}, 404, "not_found"},
		{errors.New("unknown method file.trash"), 409, "node_outdated"},
		{errors.New("~/a is on another disk"), 502, "internal"},
	} {
		fail = c.err
		code, out := postEdit(mux, "trash", `{"paths":["~/a"]}`)
		if code != c.status || out["code"] != c.code {
			t.Errorf("%v: %d %v", c.err, code, out)
		}
		if c.code == "exists" && out["name"] != "a.txt" {
			t.Errorf("exists without the name: %v", out)
		}
	}
}

// #5828 / #5829: a new folder, a text file and a zip reach the node with their fields; what can't be one is 400 before
// the node is asked; a write's conflict comes back as the node's file_changed.
func TestFileMakeRoutes(t *testing.T) {
	var got []string
	mux := fileEditHub(t, func(method string, p map[string]any) (any, error) {
		b, _ := json.Marshal(p)
		got = append(got, method+" "+string(b))
		if method == "file.write" && p["revision"] == "old" {
			return nil, &rpc.Error{Code: proto.CodeFileChanged, Message: "changed", Data: json.RawMessage(`{"revision":"new"}`)}
		}
		return map[string]any{"ok": method}, nil
	})
	for _, c := range []struct{ op, body string }{
		{"mkdir", `{"dir":"~/w","cwd":"/w","name":"new"}`},
		{"write", `{"path":"~/w/a.txt","text":"","create":true}`},
		{"write", `{"path":"~/w/a.txt","text":"x","revision":"r1","sha256":"ab","force":true}`},
		{"zip", `{"paths":["~/w/a","~/w/b"],"cwd":"/w","name":"Archive.zip"}`},
	} {
		if code, out := postEdit(mux, c.op, c.body); code != 200 || out["ok"] != "file."+c.op {
			t.Fatalf("%s: %d %v", c.op, code, out)
		}
	}
	want := []string{
		`file.mkdir {"cwd":"/w","dir":"~/w","name":"new"}`,
		`file.write {"create":true,"path":"~/w/a.txt","text":""}`,
		`file.write {"force":true,"path":"~/w/a.txt","revision":"r1","sha256":"ab","text":"x"}`,
		`file.zip {"cwd":"/w","name":"Archive.zip","paths":["~/w/a","~/w/b"]}`,
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("node got:\n%s", strings.Join(got, "\n"))
	}
	for _, c := range []struct{ op, body string }{
		{"mkdir", `{"dir":"~/w","name":"a/b"}`},
		{"mkdir", `{"name":"x"}`},
		{"write", `{"path":"~/w/a.txt"}`},
		{"write", `{"text":"x"}`},
		{"zip", `{"paths":[]}`},
	} {
		if code, _ := postEdit(mux, c.op, c.body); code != http.StatusBadRequest {
			t.Fatalf("%s %s: %d, want 400", c.op, c.body, code)
		}
	}
	if len(got) != 4 {
		t.Fatalf("a refused request reached the node: %v", got[4:])
	}
	code, out := postEdit(mux, "write", `{"path":"~/w/a.txt","text":"x","revision":"old"}`)
	if code != http.StatusConflict || out["code"] != proto.CodeFileChanged {
		t.Fatalf("conflict: %d %v", code, out)
	}
}

// #5831: chmod reaches the node with its bits; bits past 0777 (or none) are 400.
func TestFileChmodRoute(t *testing.T) {
	var got []string
	mux := fileEditHub(t, func(method string, p map[string]any) (any, error) {
		b, _ := json.Marshal(p)
		got = append(got, method+" "+string(b))
		return map[string]any{"mode": p["mode"]}, nil
	})
	if code, out := postEdit(mux, "chmod", `{"path":"~/w/run.sh","mode":493}`); code != 200 || out["mode"] != float64(493) {
		t.Fatalf("chmod: %d %v", code, out)
	}
	for _, body := range []string{`{"path":"~/w/run.sh","mode":2541}`, `{"path":"~/w/run.sh"}`, `{"mode":420}`, `{"path":"~/w/run.sh","mode":-1}`} {
		if code, _ := postEdit(mux, "chmod", body); code != http.StatusBadRequest {
			t.Fatalf("%s: %d, want 400", body, code)
		}
	}
	if len(got) != 1 || got[0] != `file.chmod {"mode":493,"path":"~/w/run.sh"}` {
		t.Fatalf("node got %v", got)
	}
}

func TestFileCopyPassesOperationID(t *testing.T) {
	var methods []string
	mux := fileEditHub(t, func(method string, p map[string]any) (any, error) {
		methods = append(methods, method)
		return map[string]any{"id": p["operation_id"], "state": "running"}, nil
	})
	code, out := postEdit(mux, "copy", `{"paths":["~/a"],"dir":"~/dest","operation_id":"op-1"}`)
	if code != 200 || out["id"] != "op-1" {
		t.Fatalf("copy operation_id: %d %v", code, out)
	}
	if len(methods) != 1 || methods[0] != "file.copy" {
		t.Fatalf("methods = %v", methods)
	}
}
