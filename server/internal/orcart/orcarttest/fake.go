// Package orcarttest is a scriptable fake of Orca's runtime socket for tests (a unix socket, a named pipe on Windows,
// as Orca itself serves there): it writes its own orca-runtime.json,
// checks the auth token, answers status.get, terminal.list, worktree.ps and terminal.read from its state, records every
// call in order, and lets a test replace any method's answer. Nothing here talks to a real Orca.
package orcarttest

import (
	"bufio"
	"encoding/json"
	"fmt"
	"maps"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
	"testing"
)

// Fake is one fake Orca runtime.
type Fake struct {
	// MetaPath is its orca-runtime.json, Socket its socket (a named pipe on Windows) and Kind that transport's kind.
	MetaPath string
	Socket   string
	Kind     string

	mu          sync.Mutex
	token       string
	runtimeID   string
	log         []Call
	terms       []map[string]any
	layouts     []any
	trees       []map[string]any
	screens     map[string]map[string]any
	outputs     map[string]*output
	status      map[string]any
	on          map[string]func(params map[string]any) any
	keepalive   int
	ln          net.Listener
	agentStatus map[string]map[string]any
	// processes are terminal.inspectProcess's answers by handle; nil: the method is unknown (an older Orca).
	processes map[string]map[string]any
	revisions map[string]int
	writes    []Write
	invalid   []string
	// splits counts terminal.split's new terminals; lacking are the methods it answers method_not_found to (Lack);
	// customTitles are tabs' custom names by tab id (terminal.rename, RenameTab).
	splits       int
	lacking      map[string]bool
	customTitles map[string]string
	// unknown: params each method's schema does not have and refuses (Unknown), as an older, strict Orca's would.
	unknown map[string][]string
}

// Call is one request the fake got (status.get included).
type Call struct {
	Method string
	Params map[string]any
}

// String is "method {params}" with the params as sorted JSON (client and expectedIncarnationId left out: every
// handle call carries them; Writes and the fake's validation check them).
func (c Call) String() string {
	p := map[string]any{}
	for k, v := range c.Params {
		if k != "client" && k != "expectedIncarnationId" {
			p[k] = v
		}
	}
	b, _ := json.Marshal(p)
	return c.Method + " " + string(b)
}

// Fail makes a handler answer with an error of that code.
type Fail struct{ Code string }

// Refuse makes a handler answer with an error of that code in Orca's own words (message).
func Refuse(code, message string) any { return refusal{code: code, message: message} }

// refusal is Refuse's answer.
type refusal struct{ code, message string }

// New starts a fake runtime in a fresh directory (TempDir).
func New(t testing.TB) *Fake {
	t.Helper()
	dir := TempDir(t)
	kind, socket := Endpoint(dir)
	f := &Fake{
		MetaPath: filepath.Join(dir, "orca-runtime.json"), Socket: socket, Kind: kind,
		token: "test-token-1", runtimeID: "rt-1", screens: map[string]map[string]any{}, outputs: map[string]*output{},
		on: map[string]func(map[string]any) any{}, agentStatus: map[string]map[string]any{}, revisions: map[string]int{},
		status: map[string]any{"appVersion": "1.4.245", "runtimeProtocolVersion": 3, "minCompatibleRuntimeClientVersion": 2,
			"protocolVersion": 3, "capabilities": []string{"terminal.prompt-delivery.v1"}},
	}
	if err := f.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(f.Stop)
	return f
}

// Start listens (again, after Stop) and writes the metadata file.
func (f *Fake) Start() error {
	ln, err := Listen(f.Kind, f.Socket)
	if err != nil {
		return err
	}
	f.mu.Lock()
	f.ln = ln
	f.mu.Unlock()
	if err := f.writeMeta(); err != nil {
		return err
	}
	go func() {
		for {
			c, err := ln.Accept()
			if err != nil {
				return
			}
			go f.serve(c)
		}
	}()
	return nil
}

// Stop closes the socket (Orca quit); the metadata file stays behind, as Orca's may.
func (f *Fake) Stop() {
	f.mu.Lock()
	ln := f.ln
	f.ln = nil
	f.mu.Unlock()
	if ln != nil {
		ln.Close()
	}
}

// Restart is Orca starting again: a new token and runtime id in the metadata file.
func (f *Fake) Restart(token string) error {
	f.Stop()
	f.mu.Lock()
	f.token, f.runtimeID = token, "rt-"+token
	f.mu.Unlock()
	return f.Start()
}

func (f *Fake) writeMeta() error {
	f.mu.Lock()
	meta := map[string]any{"runtimeId": f.runtimeID, "pid": os.Getpid(), "authToken": f.token, "startedAt": 1,
		"transports": []any{map[string]any{"kind": f.Kind, "endpoint": f.Socket},
			map[string]any{"kind": "websocket", "endpoint": "ws://127.0.0.1:1"}}}
	f.mu.Unlock()
	b, _ := json.Marshal(meta)
	return os.WriteFile(f.MetaPath, b, 0o600)
}

func (f *Fake) serve(c net.Conn) {
	defer c.Close()
	r := bufio.NewReader(c)
	for {
		line, err := r.ReadBytes('\n')
		if err != nil {
			return
		}
		var req struct {
			ID        any            `json:"id"`
			AuthToken string         `json:"authToken"`
			Method    string         `json:"method"`
			Params    map[string]any `json:"params"`
		}
		if json.Unmarshal(line, &req) != nil {
			return
		}
		if req.Params == nil {
			req.Params = map[string]any{}
		}
		f.mu.Lock()
		authed := req.AuthToken == f.token
		var result any
		invalid := ""
		if authed {
			f.log = append(f.log, Call{Method: req.Method, Params: req.Params})
			if invalid = f.unknownLocked(req.Method, req.Params); invalid == "" {
				invalid = validate(req.Method, req.Params)
			}
			if f.lacking[req.Method] {
				result = Fail{"method_not_found"} // an older Orca: nothing done
			} else if invalid == "" {
				result = f.answerLocked(req.Method, req.Params)
			} else {
				f.invalid = append(f.invalid, req.Method+": "+invalid)
			}
		}
		h, keepalive, rt := f.on[req.Method], f.keepalive, f.runtimeID
		f.mu.Unlock()
		if !authed {
			f.write(c, map[string]any{"id": req.ID, "ok": false, "error": map[string]any{"code": "unauthorized",
				"message": "Invalid auth token"}})
			continue
		}
		if invalid != "" {
			// Orca's dispatcher refuses params its schema rejects before any handler runs.
			f.write(c, map[string]any{"id": req.ID, "ok": false, "error": map[string]any{"code": "invalid_argument",
				"message": invalid}})
			continue
		}
		if h != nil {
			result = h(req.Params)
		}
		for range keepalive {
			f.write(c, map[string]any{"_keepalive": true})
		}
		resp := map[string]any{"id": req.ID, "_meta": map[string]any{"runtimeId": rt}}
		if e, ok := result.(Fail); ok {
			resp["ok"] = false
			resp["error"] = map[string]any{"code": e.Code, "message": e.Code}
		} else if e, ok := result.(refusal); ok {
			resp["ok"] = false
			resp["error"] = map[string]any{"code": e.code, "message": e.message}
		} else {
			resp["ok"], resp["result"] = true, result
		}
		f.write(c, resp)
	}
}

func (f *Fake) answerLocked(method string, p map[string]any) any {
	h, _ := p["terminal"].(string)
	switch method {
	case "status.get":
		return f.status
	case "agentSession.agents":
		return Fail{"method_not_found"} // an Orca before 1.4.277 (Handle answers as a newer one)
	case "repo.add":
		path, _ := p["path"].(string)
		return map[string]any{"repo": map[string]any{"path": path, "ok": true}}
	case "worktree.set":
		id, _ := p["worktree"].(string)
		for _, tree := range f.trees {
			if tree["worktreeId"] == strings.TrimPrefix(id, "id:") {
				tree["displayName"] = p["displayName"]
				return map[string]any{"worktree": tree}
			}
		}
		return Fail{Code: "worktree_not_found"}
	case "terminal.create":
		wtID, _ := p["worktree"].(string)
		wtID = strings.TrimPrefix(wtID, "id:")
		newIdx := len(f.terms) + 1
		newHandle := fmt.Sprintf("term_fake_%d", newIdx)
		newTab := fmt.Sprintf("tab_%d", newIdx)
		newLeaf := fmt.Sprintf("leaf_%d", newIdx)
		newInc := fmt.Sprintf("inc_%d", newIdx)
		title, _ := p["title"].(string)
		newTerm := map[string]any{
			"handle": newHandle, "tabId": newTab, "leafId": newLeaf, "incarnationId": newInc,
			"worktreeId": wtID, "title": title, "connected": true, "writable": true,
			"orphaned": false, "executionHostId": "local", "agentIdentity": "",
		}
		f.terms = append(f.terms, newTerm)
		return map[string]any{
			"terminal": map[string]any{
				"handle": newHandle, "tabId": newTab, "leafId": newLeaf, "incarnationId": newInc,
				"worktreeId": wtID, "title": title, "connected": true, "writable": true,
				"orphaned": false, "executionHostId": "local", "agentIdentity": "",
				"paneKey": newTab + ":" + newLeaf, "processId": os.Getpid(),
			},
		}
	case "terminal.list":
		// Copies of each terminal: the answer is encoded after the lock is let go, while another request may rename one.
		terms := make([]map[string]any, len(f.terms))
		for i, t := range f.terms {
			terms[i] = maps.Clone(t)
		}
		out := map[string]any{"terminals": terms, "topologyRevisions": maps.Clone(f.revisions),
			"hostScope": map[string]any{"hostIds": []string{"local"}}, "totalCount": len(f.terms), "truncated": false}
		if p["includeVisualLayouts"] != false { // absent includes them, as for older clients
			out["visualLayouts"] = slices.Clone(f.layouts)
		}
		return out
	case "session.tabs.list":
		// The phone's tab model: a terminal entry per pane, its tab's custom name when it has one.
		sel, _ := p["worktree"].(string)
		wt := strings.TrimPrefix(sel, "id:")
		tabs := []any{}
		for _, x := range f.terms {
			if x["worktreeId"] != wt {
				continue
			}
			tab, _ := x["tabId"].(string)
			leaf, _ := x["leafId"].(string)
			entry := map[string]any{"type": "terminal", "id": tab + "::" + leaf, "parentTabId": tab, "leafId": leaf,
				"title": f.layoutTitleLocked(tab), "customTitle": nil}
			if c, ok := f.customTitles[tab]; ok {
				entry["title"], entry["customTitle"] = c, c
			}
			tabs = append(tabs, entry)
		}
		return map[string]any{"worktree": wt, "tabs": tabs}
	case "worktree.ps":
		b, _ := json.Marshal(f.trees)
		id := fmt.Sprintf("snap-%x", fnv32(b))
		if _, ok := p["afterSnapshotId"]; !ok {
			return map[string]any{"worktrees": slices.Clone(f.trees), "totalCount": len(f.trees), "truncated": false}
		}
		if p["afterSnapshotId"] == id {
			return map[string]any{"unchanged": true, "snapshotId": id}
		}
		return map[string]any{"worktrees": slices.Clone(f.trees), "totalCount": len(f.trees), "truncated": false,
			"snapshotId": id}
	}
	t := f.termLocked(h)
	if t == nil {
		return Fail{"terminal_handle_stale"}
	}
	if inc, ok := p["expectedIncarnationId"]; ok && inc != t["incarnationId"] {
		return Fail{"terminal_handle_stale"} // the handle names another process now
	}
	switch method {
	case "terminal.read":
		if p["screen"] != true {
			return map[string]any{"terminal": f.readOutputLocked(h, p)}
		}
		s := f.screens[h]
		if s == nil {
			s = map[string]any{"tail": []string{}, "tailRows": []any{}}
		}
		out := map[string]any{"handle": h, "status": "running", "truncated": false, "source": "screen"}
		maps.Copy(out, s)
		return map[string]any{"terminal": out}
	case "terminal.agentStatus":
		st := f.agentStatus[h]
		if st == nil {
			st = map[string]any{"isRunningAgent": true, "status": nil}
		}
		out := map[string]any{"handle": h}
		maps.Copy(out, st)
		return map[string]any{"agentStatus": out}
	case "terminal.inspectProcess":
		if f.processes == nil {
			return Fail{"method_not_found"}
		}
		pr := f.processes[h]
		if pr == nil {
			// What Orca says when it cannot read the terminal's processes.
			pr = map[string]any{"foregroundProcess": nil, "hasChildProcesses": true, "childProcessEvidence": "unverifiable"}
		}
		return map[string]any{"process": maps.Clone(pr)}
	case "terminal.close":
		f.writes = append(f.writes, Write{Handle: h, Close: true})
		return map[string]any{"close": map[string]any{"handle": h, "tabId": t["tabId"], "ptyKilled": true}}
	case "terminal.rename":
		title, _ := p["title"].(string)
		tab, _ := t["tabId"].(string)
		f.renameTabLocked(tab, title)
		return map[string]any{"rename": map[string]any{"handle": h, "tabId": tab, "title": title}}
	case "terminal.setPaneTitle":
		title, _ := p["title"].(string)
		return map[string]any{"setPaneTitle": map[string]any{"handle": h, "title": title}}
	case "terminal.split":
		f.splits++
		handle := fmt.Sprintf("term_split_%d", f.splits)
		leaf := fmt.Sprintf("leaf_split_%d", f.splits)
		nt := maps.Clone(t)
		nt["handle"], nt["leafId"], nt["incarnationId"], nt["ptyId"] = handle, leaf, "inc-"+handle, "pty-"+handle
		nt["agentIdentity"], nt["title"] = "", ""
		f.terms = append(slices.Clone(f.terms), nt)
		direction, _ := p["direction"].(string)
		if direction == "" {
			direction = "horizontal" // Orca's default
		}
		command, _ := p["command"].(string)
		f.writes = append(f.writes, Write{Handle: h, Split: direction, Text: command})
		return map[string]any{"split": map[string]any{"handle": handle, "tabId": t["tabId"], "leafId": leaf, "paneRuntimeId": -1}}
	case "terminal.closeTab":
		tab := t["tabId"]
		var keep []map[string]any
		for _, x := range f.terms {
			if x["tabId"] != tab {
				keep = append(keep, x)
			}
		}
		f.terms = keep
		f.retabLocked(func(tabs []any) []any {
			return slices.DeleteFunc(tabs, func(x any) bool { m, _ := x.(map[string]any); return m["tabId"] == tab })
		})
		f.writes = append(f.writes, Write{Handle: h, Close: true, Tab: true})
		return map[string]any{"close": map[string]any{"handle": h, "tabId": tab, "closeMode": "tab", "ptyKilled": false}}
	case "terminal.send":
		text, _ := p["text"].(string)
		enter := p["enter"] == true
		if p["requireAgentStatus"] == "sendable" && text != "" && enter {
			// Guarded sends are two-phase in Orca: a combined text and submit is refused outright.
			return map[string]any{"send": map[string]any{"handle": h, "accepted": false, "bytesWritten": 0}}
		}
		st := f.agentStatus[h]
		running := st == nil || st["isRunningAgent"] == true
		client, _ := p["client"].(map[string]any)
		paste := p["agentPrompt"] == true && text != "" && enter && client["type"] == "desktop" && running
		w := Write{Handle: h, Text: text, Enter: enter, Pasted: paste}
		f.writes = append(f.writes, w)
		send := map[string]any{"handle": h, "accepted": true, "bytesWritten": len(text)}
		if paste {
			send["prompt"] = map[string]any{"requestId": "r", "stages": []string{"input_accepted"}, "provider": "claude",
				"observation": "supported", "processIncarnation": t["incarnationId"], "generation": 1,
				"baselineWorkingSequence": 0}
		}
		return map[string]any{"send": send}
	}
	return Fail{"method_not_found"}
}

// retabLocked rewrites each worktree layout's tabs through fn, as fresh maps: terminal.list answers hold the old ones,
// encoded after the lock is let go.
func (f *Fake) retabLocked(fn func(tabs []any) []any) {
	layouts := make([]any, len(f.layouts))
	for i, l := range f.layouts {
		lm, _ := l.(map[string]any)
		root, _ := lm["root"].(map[string]any)
		tabs, _ := root["tabs"].([]any)
		nr := maps.Clone(root)
		nr["tabs"] = fn(slices.Clone(tabs))
		nl := maps.Clone(lm)
		nl["root"] = nr
		layouts[i] = nl
	}
	f.layouts = layouts
}

// renameTabLocked does what Orca 1.4.277 with no window does for terminal.rename (checked live): the tab gets the
// custom name (session.tabs.list), every terminal in it that title (terminal.list), and its layout keeps the title it
// was made with.
func (f *Fake) renameTabLocked(tab, title string) {
	if f.customTitles == nil {
		f.customTitles = map[string]string{}
	}
	f.customTitles[tab] = title
	for _, x := range f.terms {
		if x["tabId"] == tab {
			h, _ := x["handle"].(string)
			f.replaceLocked(h, map[string]any{"title": title})
		}
	}
}

// RenameTab renames a tab the way someone does in Orca's window or with Orca's own CLI (terminal.rename's effect).
func (f *Fake) RenameTab(tab, title string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.renameTabLocked(tab, title)
}

// layoutTitleLocked is the tab's title in the layouts, "" when no layout has it.
func (f *Fake) layoutTitleLocked(tab string) string {
	for _, l := range f.layouts {
		lm, _ := l.(map[string]any)
		root, _ := lm["root"].(map[string]any)
		tabs, _ := root["tabs"].([]any)
		for _, x := range tabs {
			if m, _ := x.(map[string]any); m["tabId"] == tab {
				title, _ := m["title"].(string)
				return title
			}
		}
	}
	return ""
}

func (f *Fake) termLocked(h string) map[string]any {
	for _, t := range f.terms {
		if t["handle"] == h {
			return t
		}
	}
	return nil
}

func fnv32(b []byte) uint32 {
	h := uint32(2166136261)
	for _, c := range b {
		h ^= uint32(c)
		h *= 16777619
	}
	return h
}

// validate checks a request's params the way Orca's zod schemas do (src/shared/rpc-contract), for the methods Persona
// calls: "" when valid, else the reason (answered as invalid_argument). Unknown keys pass, as zod strips them.
func validate(method string, p map[string]any) string {
	str := func(k string, required bool) string {
		v, ok := p[k]
		if !ok || v == nil && !required {
			if required {
				return "Missing " + k
			}
			return ""
		}
		if s, isStr := v.(string); !isStr || s == "" {
			return k + " must be a non-empty string"
		}
		return ""
	}
	boolean := func(k string) string {
		if v, ok := p[k]; ok {
			if _, isBool := v.(bool); !isBool {
				return k + " must be a boolean"
			}
		}
		return ""
	}
	literalTrue := func(k string) string {
		if v, ok := p[k]; ok && v != true {
			return k + " must be true"
		}
		return ""
	}
	first := func(reasons ...string) string {
		for _, r := range reasons {
			if r != "" {
				return r
			}
		}
		return ""
	}
	handle := func() string {
		if v, ok := p["expectedIncarnationId"]; ok {
			if s, isStr := v.(string); !isStr || s == "" {
				return "Missing PTY incarnation"
			}
		}
		return str("terminal", true)
	}
	switch method {
	case "status.get":
		return ""
	case "terminal.list":
		return first(boolean("includeVisualLayouts"), boolean("includePreview"), boolean("requireFreshPtyLiveness"))
	case "worktree.ps":
		if v, ok := p["afterSnapshotId"]; ok && v != nil {
			if s, isStr := v.(string); !isStr || s == "" || len(s) > 128 {
				return "afterSnapshotId must be a string of 1–128 characters or null"
			}
		}
		return literalTrue("acceptRowDelta")
	case "terminal.read":
		if p["screen"] == true && p["cursor"] != nil {
			return "Cursor cannot be combined with a screen read"
		}
		if v, ok := p["cursor"]; ok {
			if c, isNum := v.(float64); !isNum || c < 0 || c != float64(int64(c)) {
				return "Cursor must be a non-negative integer"
			}
		}
		return first(handle(), literalTrue("screen"))
	case "terminal.agentStatus", "terminal.close", "terminal.closeTab":
		return handle()
	case "terminal.split":
		if v, ok := p["direction"]; ok && v != "vertical" && v != "horizontal" {
			// Orca turns any other value into its default (horizontal): a wrong word would split the wrong way.
			return "direction must be vertical or horizontal"
		}
		return first(handle(), str("command", false), str("cwd", false))
	case "terminal.inspectProcess":
		return first(handle(), boolean("scanChildProcesses"))
	case "session.tabs.list":
		return str("worktree", true)
	case "terminal.rename", "terminal.setPaneTitle":
		return first(handle(), str("title", true))
	case "repo.add":
		return str("path", true)
	case "worktree.set":
		return first(str("worktree", true), str("displayName", true))
	case "worktree.create":
		return first(str("repo", true), str("name", false), str("branchNameOverride", false), str("baseBranch", false),
			str("parentWorktree", false), boolean("activate"), boolean("nameWasGenerated"))
	case "worktree.rm": // Orca 1.4.277's worktree.rm params (the ones Persona sends)
		return first(str("worktree", true), str("hostId", false), boolean("deleteBranch"), boolean("force"),
			boolean("runHooks"), boolean("waitForBackgroundRemoval"))
	case "agentSession.history":
		if r := str("sessionId", true); r != "" {
			return r
		}
		direction, _ := p["direction"].(string)
		if direction != "tail" && direction != "before" && direction != "after" {
			return "direction must be tail, before or after"
		}
		if v, ok := p["limit"]; ok {
			n, isNumber := v.(float64)
			if !isNumber || n < 1 || n > 200 || n != float64(int(n)) {
				return "limit must be an integer of 1–200"
			}
		}
		if v, ok := p["cursor"]; ok {
			cursor, isObject := v.(map[string]any)
			epoch, epochOK := cursor["epoch"].(string)
			sequence, sequenceOK := cursor["sequence"].(float64)
			if !isObject || !epochOK || epoch == "" || !sequenceOK || sequence < 0 {
				return "cursor must contain epoch and a non-negative sequence"
			}
		}
		return ""
	case "session.tabs.listAll":
		return ""
	case "agentSession.reveal":
		return str("sessionId", true)
	case "agentSession.respondToApproval":
		return first(envelopeOf(p), str("itemId", true), str("optionId", true), positiveInt(p, "expectedRevision"))
	case "agentSession.respondToQuestion":
		if r := first(envelopeOf(p), str("itemId", true), positiveInt(p, "expectedRevision")); r != "" {
			return r
		}
		answers, hasAnswers := p["answers"].([]any)
		_, hasOption := p["optionId"]
		if hasAnswers == hasOption {
			return "Send exactly one of an option id or answers"
		}
		if hasAnswers && len(answers) == 0 {
			return "answers must not be empty"
		}
		return ""
	case "agentSession.setOption":
		return first(envelopeOf(p), str("key", true))
	case "agentSession.rename":
		if name, _ := p["name"].(string); len(name) > 4096 {
			return "name must be at most 4096 characters"
		}
		return first(str("sessionId", true), str("name", true))
	case "agentSession.rewind":
		return first(envelopeOf(p), str("itemId", true), str("expectedEpoch", true))
	case "agentSession.acknowledgeAttention":
		cursor, isObject := p["observedCursor"].(map[string]any)
		epoch, _ := cursor["epoch"].(string)
		sequence, isNumber := cursor["sequence"].(float64)
		if !isObject || epoch == "" || !isNumber || sequence < 0 || sequence != float64(int64(sequence)) {
			return "observedCursor must contain epoch and a non-negative sequence"
		}
		return str("sessionId", true)
	case "agentSession.cancel":
		return envelopeOf(p)
	case "preflight.installedAgents":
		return ""
	case "agentSession.createSupport":
		return first(str("worktree", true), str("agent", true))
	case "agent.launch":
		return launchInvalid(p)
	case "agentSession.create":
		return createInvalid(p)
	case "agentSession.send":
		if r := envelopeOf(p); r != "" {
			return r
		}
		body, ok := p["body"].(map[string]any)
		if !ok || body["kind"] != "message" || body["role"] != "user" {
			return "body must be a user message"
		}
		blocks, ok := body["blocks"].([]any)
		if !ok || len(blocks) == 0 || len(blocks) > 64 {
			return "body.blocks must contain 1–64 blocks"
		}
		if delivery, ok := p["delivery"]; ok && delivery != "queue-if-active" {
			return "delivery must be queue-if-active"
		}
		return ""
	case "terminal.create":
		if v, ok := p["env"]; ok {
			env, isObj := v.(map[string]any)
			if !isObj {
				return "env must be an object of strings"
			}
			for _, val := range env {
				if _, isStr := val.(string); !isStr {
					return "env must be an object of strings"
				}
			}
		}
		if v, ok := p["startupCommandDelivery"]; ok && v != "fast" && v != "shell-ready" {
			return "startupCommandDelivery must be fast or shell-ready"
		}
		return first(str("worktree", true), str("title", true), str("shell", false), str("command", false))
	case "terminal.send":
		if r := first(handle(), literalTrue("agentPrompt"), literalTrue("requireWriteSettlement"),
			literalTrue("claimViewport")); r != "" {
			return r
		}
		if v, ok := p["text"]; ok && v != nil {
			if _, isStr := v.(string); !isStr {
				return "text must be a string"
			}
		}
		if v, ok := p["requireAgentStatus"]; ok && v != "sendable" {
			return "requireAgentStatus must be sendable"
		}
		if v, ok := p["client"]; ok {
			c, isObj := v.(map[string]any)
			if !isObj {
				return "client must be an object"
			}
			if id, _ := c["id"].(string); id == "" {
				return "Missing client ID"
			}
			if t, ok := c["type"]; ok && t != "mobile" && t != "desktop" {
				return "client.type must be mobile or desktop"
			}
		}
		_, hasText := p["text"]
		if !hasText && p["enter"] != true && p["interrupt"] != true {
			return "Missing terminal send payload"
		}
		return ""
	}
	return ""
}

// envelopeOf checks an agentSession write's envelope as Orca's schema does: session, operation id and a lowercase
// sha256 fingerprint, and a positive fence ("" when valid). null, which a create sends, is checked by its method.
func envelopeOf(p map[string]any) string {
	envelope, ok := p["envelope"].(map[string]any)
	if !ok {
		return "Missing envelope"
	}
	for _, field := range []string{"sessionId", "clientOperationId", "payloadFingerprint"} {
		if value, _ := envelope[field].(string); value == "" {
			return "Missing envelope." + field
		}
	}
	fingerprint, _ := envelope["payloadFingerprint"].(string)
	if len(fingerprint) != 64 {
		return "payloadFingerprint must be a sha256 hex digest"
	}
	for _, r := range fingerprint {
		if !(r >= '0' && r <= '9' || r >= 'a' && r <= 'f') {
			return "payloadFingerprint must be lowercase hex"
		}
	}
	if fence, ok := envelope["expectedRuntimeFence"].(float64); !ok || fence <= 0 {
		return "expectedRuntimeFence must be positive"
	}
	return ""
}

// positiveInt checks that p[k] is a positive integer ("" when it is).
func positiveInt(p map[string]any, k string) string {
	n, ok := p[k].(float64)
	if !ok || n < 1 || n != float64(int64(n)) {
		return k + " must be a positive integer"
	}
	return ""
}

// orcaSessionID is what Orca 1.4.277 takes as a session id.
func orcaSessionID(id string) bool {
	if len(id) < 8 || len(id) > 128 {
		return false
	}
	for _, r := range id {
		if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || r == '_' || r == '-') {
			return false
		}
	}
	return true
}

// stringMap checks that v, when present, is an object of at most 32 string values ("" when it is).
func stringMap(v any, name string) string {
	if v == nil {
		return ""
	}
	m, ok := v.(map[string]any)
	if !ok || len(m) > 32 {
		return name + " must be an object of at most 32 strings"
	}
	for _, x := range m {
		if s, ok := x.(string); !ok || s == "" {
			return name + " values must be non-empty strings"
		}
	}
	return ""
}

// userMessage checks a user message body as agentSession.send and agentSession.create take it.
func userMessage(v any) string {
	body, ok := v.(map[string]any)
	if !ok || body["kind"] != "message" || body["role"] != "user" {
		return "body must be a user message"
	}
	if blocks, ok := body["blocks"].([]any); !ok || len(blocks) == 0 || len(blocks) > 64 {
		return "body.blocks must contain 1–64 blocks"
	}
	return ""
}

// launchInvalid checks agent.launch's params as Orca 1.4.277 does ("" when they pass): the agent, a launch operation
// id ("<13-digit ms>-<32 hex>"), an existing worktree target, a prompt to submit or draft, string chat options, and
// the session id and surface it may name.
func launchInvalid(p map[string]any) string {
	if agent, _ := p["agent"].(string); agent == "" {
		return "Missing agent"
	}
	if op, ok := p["operationId"]; ok {
		s, _ := op.(string)
		head, hexPart, cut := strings.Cut(s, "-")
		if !cut || len(head) != 13 || len(hexPart) != 32 || strings.Trim(head, "0123456789") != "" ||
			strings.Trim(hexPart, "0123456789abcdef") != "" {
			return "Malformed launch operation id"
		}
	}
	target, _ := p["target"].(map[string]any)
	if worktree, _ := target["worktree"].(string); target["kind"] != "existing" || worktree == "" {
		return "target must be an existing worktree"
	}
	if prompt, ok := p["prompt"]; ok {
		pr, _ := prompt.(map[string]any)
		if _, isText := pr["text"].(string); !isText || pr["delivery"] != "submit" && pr["delivery"] != "draft" {
			return "prompt must have text and a delivery"
		}
	}
	if r := stringMap(p["chatOptions"], "chatOptions"); r != "" {
		return r
	}
	if id, ok := p["sessionId"]; ok {
		if s, _ := id.(string); !orcaSessionID(s) {
			return "Invalid session id"
		}
	}
	if surface, ok := p["surface"]; ok && surface != "chat" && surface != "terminal" {
		return "surface must be chat or terminal"
	}
	return ""
}

// createInvalid checks agentSession.create's params as Orca 1.4.277 does ("" when they pass): an envelope naming no
// fence (the session does not exist yet), the worktree and agent, a first message, string options, nothing else.
func createInvalid(p map[string]any) string {
	envelope, ok := p["envelope"].(map[string]any)
	if !ok {
		return "Missing envelope"
	}
	if fence, has := envelope["expectedRuntimeFence"]; !has || fence != nil {
		return "agent_session_operation_invalid: requestMalformed (a create names no fence)"
	}
	if id, _ := envelope["sessionId"].(string); !orcaSessionID(id) {
		return "Invalid session id"
	}
	for _, field := range []string{"clientOperationId", "payloadFingerprint"} {
		if value, _ := envelope[field].(string); value == "" {
			return "Missing envelope." + field
		}
	}
	for k := range p {
		if !slices.Contains([]string{"envelope", "worktree", "agent", "firstMessage", "options", "optionsMode", "tabId",
			"tabPlacement", "resumeFrom", "forkFrom"}, k) {
			return "Unrecognized key " + k
		}
	}
	if worktree, _ := p["worktree"].(string); worktree == "" {
		return "Missing worktree"
	}
	if agent, _ := p["agent"].(string); agent == "" {
		return "Missing agent"
	}
	if msg, ok := p["firstMessage"]; ok {
		f, _ := msg.(map[string]any)
		if id, _ := f["clientMessageId"].(string); id == "" {
			return "Missing firstMessage.clientMessageId"
		}
		if r := userMessage(f["body"]); r != "" {
			return r
		}
	}
	return stringMap(p["options"], "options")
}

// Write is one terminal.send or terminal.close the fake took: what reached the terminal.
type Write struct {
	Handle string
	Text   string
	Enter  bool
	// Pasted: delivered as an agent prompt (bracketed paste); else written raw.
	Pasted bool
	Close  bool
	// Tab: the close was terminal.closeTab (the whole tab).
	Tab bool
	// Split is terminal.split's direction for a split of the terminal (its command, if any, in Text).
	Split string
}

// Writes are the sends and closes taken so far.
func (f *Fake) Writes() []Write {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.writes)
}

// Lack makes the fake answer method_not_found to method, doing nothing, as an Orca older than the method would.
func (f *Fake) Lack(method string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.lacking == nil {
		f.lacking = map[string]bool{}
	}
	f.lacking[method] = true
}

// Unknown makes the fake refuse method's requests that carry any of keys (invalid_argument, before any handler), as
// an Orca whose schema for it does not have them and refuses unknown keys would.
func (f *Fake) Unknown(method string, keys ...string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.unknown == nil {
		f.unknown = map[string][]string{}
	}
	f.unknown[method] = keys
}

// unknownLocked is the refusal of a request carrying a key Unknown named for its method, "" for none.
func (f *Fake) unknownLocked(method string, p map[string]any) string {
	for _, k := range f.unknown[method] {
		if _, ok := p[k]; ok {
			return "Unrecognized key: \"" + k + "\""
		}
	}
	return ""
}

// Invalid are the requests refused for their params so far ("method: reason").
func (f *Fake) Invalid() []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return slices.Clone(f.invalid)
}

// SetAgentStatus sets terminal.agentStatus for a handle: whether Orca sees an agent running in front, and its state
// ("" for unknown). Without it a terminal runs an agent of unknown state.
func (f *Fake) SetAgentStatus(handle string, running bool, status string) {
	var st any
	if status != "" {
		st = status
	}
	f.mu.Lock()
	f.agentStatus[handle] = map[string]any{"isRunningAgent": running, "status": st}
	f.mu.Unlock()
}

// SetProcess sets terminal.inspectProcess's answer for a handle (ShellAtRest, ShellWithJobs, Running, Unverifiable);
// from the first call on the fake knows the method, and a handle with no answer set is one Orca cannot read. nil
// clears the handle's answer.
func (f *Fake) SetProcess(handle string, process map[string]any) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if f.processes == nil {
		f.processes = map[string]map[string]any{}
	}
	if process == nil {
		delete(f.processes, handle)
		return
	}
	f.processes[handle] = process
}

// ShellAtRest is Orca 1.4.277's terminal.inspectProcess answer for a shell at rest with nothing else in its terminal.
func ShellAtRest(shellPID int) map[string]any {
	return map[string]any{"foregroundProcess": nil, "hasChildProcesses": false, "idleShellProven": true,
		"foregroundProcessEvidence": map[string]any{"verdict": "live", "processName": nil,
			"fence": map[string]any{"platform": "posix", "shellPid": shellPID, "foregroundPgid": shellPID}}}
}

// ShellWithJobs is its answer for a shell in front with a background or stopped job: in front, not proven idle.
func ShellWithJobs(shellPID int) map[string]any {
	return map[string]any{"foregroundProcess": nil, "hasChildProcesses": false,
		"foregroundProcessEvidence": map[string]any{"verdict": "live", "processName": nil,
			"fence": map[string]any{"platform": "posix", "shellPid": shellPID, "foregroundPgid": shellPID}}}
}

// Running is its answer for a program in front of the shell ("sleep", "vi", "python3"…).
func Running(name string, shellPID int) map[string]any {
	return map[string]any{"foregroundProcess": name, "hasChildProcesses": true,
		"foregroundProcessEvidence": map[string]any{"verdict": "live", "processName": nil,
			"fence": map[string]any{"platform": "posix", "shellPid": shellPID, "foregroundPgid": shellPID + 1}}}
}

// Unverifiable is its answer when it cannot tell (reason "incarnation_mismatch", "process_table_unreadable"…): the
// foreground it names then is only its last guess.
func Unverifiable(reason string) map[string]any {
	return map[string]any{"foregroundProcess": "bash", "hasChildProcesses": false,
		"foregroundProcessEvidence": map[string]any{"verdict": "unverifiable", "reason": reason}}
}

// Orphan does to a terminal what Orca does about 2 s after terminal.create in a worktree its UI is not showing: the
// UI does not adopt it, so it becomes a background terminal, orphaned, its tabId and leafId rewritten to
// "pty:<worktreeId>@@<suffix>". Its handle and incarnation stay (#5547). It returns the new pane key.
func (f *Fake) Orphan(handle string) string {
	f.mu.Lock()
	defer f.mu.Unlock()
	t := f.termLocked(handle)
	if t == nil {
		return ""
	}
	key := fmt.Sprintf("pty:%v@@%x", t["worktreeId"], fnv32([]byte(handle)))
	f.replaceLocked(handle, map[string]any{"tabId": key, "leafId": key, "orphaned": true})
	return key + ":" + key
}

// Move puts a terminal in another tab and leaf (the user dragged it), still adopted by the UI: the same handle and
// incarnation, not orphaned.
func (f *Fake) Move(handle, tab, leaf string) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.replaceLocked(handle, map[string]any{"tabId": tab, "leafId": leaf})
}

// Update sets fields of a terminal's terminal.list entry.
func (f *Fake) Update(handle string, fields map[string]any) {
	f.mu.Lock()
	defer f.mu.Unlock()
	f.replaceLocked(handle, fields)
}

// replaceLocked replaces a terminal's entry with a copy carrying fields: terminal.list answers hold the old entries,
// encoded outside the lock.
func (f *Fake) replaceLocked(handle string, fields map[string]any) {
	terms := slices.Clone(f.terms)
	for i, t := range terms {
		if t["handle"] == handle {
			c := maps.Clone(t)
			maps.Copy(c, fields)
			terms[i] = c
		}
	}
	f.terms = terms
}

// SetRevision sets a worktree's topology revision (terminal.list's topologyRevisions).
func (f *Fake) SetRevision(worktree string, rev int) {
	f.mu.Lock()
	f.revisions[worktree] = rev
	f.mu.Unlock()
}

func (f *Fake) write(c net.Conn, msg any) {
	b, _ := json.Marshal(msg)
	_, _ = c.Write(append(b, '\n'))
}

// SetStatus replaces status.get's answer fields.
func (f *Fake) SetStatus(fields map[string]any) {
	f.mu.Lock()
	maps.Copy(f.status, fields)
	f.mu.Unlock()
}

// Keepalives makes every answer come after n keepalive lines.
func (f *Fake) Keepalives(n int) {
	f.mu.Lock()
	f.keepalive = n
	f.mu.Unlock()
}

// Terminal is a terminal.list entry on this machine.
func Terminal(handle, tab, leaf, worktree, path, branch, title, identity string) map[string]any {
	return map[string]any{"handle": handle, "ptyId": "pty-" + handle, "incarnationId": "inc-" + handle, "orphaned": false, "worktreeId": worktree,
		"worktreePath": path, "branch": branch, "tabId": tab, "leafId": leaf, "title": title, "connected": true,
		"writable": true, "lastOutputAt": 1, "preview": "", "executionHostId": "local", "agentIdentity": identity}
}

// SetTerminals replaces terminal.list's terminals; the layout gets one tab per tab id seen, titled "tab <id>".
func (f *Fake) SetTerminals(terms ...map[string]any) {
	byTree := map[string][]any{}
	seen := map[string]bool{}
	var order []string
	for _, t := range terms {
		w, tab := t["worktreeId"].(string), t["tabId"].(string)
		if _, ok := byTree[w]; !ok {
			order = append(order, w)
		}
		if !seen[tab] {
			seen[tab] = true
			byTree[w] = append(byTree[w], map[string]any{"tabId": tab, "title": "tab " + tab, "activeLeafId": t["leafId"],
				"panes": map[string]any{"type": "terminal", "handle": t["handle"], "tabId": tab, "leafId": t["leafId"]}})
		}
	}
	var layouts []any
	for _, w := range order {
		layouts = append(layouts, map[string]any{"worktreeId": w, "root": map[string]any{"type": "group",
			"groupId": "g-" + w, "activeTabId": byTree[w][0].(map[string]any)["tabId"], "tabs": byTree[w]}})
	}
	f.mu.Lock()
	f.terms, f.layouts = terms, layouts
	f.mu.Unlock()
}

// Agent is a worktree.ps agent row.
func Agent(paneKey, agentType, state string) map[string]any {
	return map[string]any{"paneKey": paneKey, "parentPaneKey": nil, "state": state, "agentType": agentType,
		"hasLivePty": true, "updatedAt": 1, "stateStartedAt": 1}
}

// Worktree is a worktree.ps entry.
func Worktree(id, path, branch string, agents ...map[string]any) map[string]any {
	a := make([]any, len(agents))
	for i, x := range agents {
		a[i] = x
	}
	return map[string]any{"worktreeId": id, "path": path, "branch": branch, "displayName": filepath.Base(path),
		"repo": filepath.Base(path), "agents": a}
}

// SetWorktrees replaces worktree.ps's worktrees.
func (f *Fake) SetWorktrees(trees ...map[string]any) {
	f.mu.Lock()
	f.trees = trees
	f.mu.Unlock()
}

// SetScreen sets terminal.read's answer for a handle: its lines (none wrapped).
func (f *Fake) SetScreen(handle string, text string) {
	lines := strings.Split(strings.TrimSuffix(text, "\n"), "\n")
	rows := make([]any, len(lines))
	for i := range rows {
		rows[i] = map[string]any{"wrapped": false, "trailingSpaces": 0}
	}
	f.mu.Lock()
	f.screens[handle] = map[string]any{"tail": lines, "tailRows": rows, "returnedLineCount": len(lines)}
	f.mu.Unlock()
}

// SetBoxScreen sets terminal.read's answer for a handle the way Orca 1.4.277 reads an agent's input box: text is the
// screen with the box's prompt row as its bare glyph (❯), and draft the text the box shows, lifted out of the rows
// ("" for none). Orca's draft has no colors: Claude Code's grey suggestion comes back as a draft too.
func (f *Fake) SetBoxScreen(handle, text, draft string) {
	f.SetScreen(handle, text)
	if draft == "" {
		return
	}
	f.mu.Lock()
	f.screens[handle]["draft"] = draft
	f.mu.Unlock()
}

// SetScreenRows sets terminal.read's answer with explicit rows and wrap flags.
func (f *Fake) SetScreenRows(handle string, lines []string, wrapped []bool) {
	rows := make([]any, len(lines))
	for i := range rows {
		rows[i] = map[string]any{"wrapped": i < len(wrapped) && wrapped[i]}
	}
	f.mu.Lock()
	f.screens[handle] = map[string]any{"tail": lines, "tailRows": rows}
	f.mu.Unlock()
}

// output is what a terminal's program wrote, as Orca keeps it (terminal.read without screen).
type output struct {
	// tail is the output tail: the newest lines, rows redrawn in place as they ended, the line being written last.
	tail          []string
	tailTruncated bool
	// log is every completed line, redrawn rows each time, oldest kept first; total counts all the completed lines
	// ever written (the cursor after the newest), so the oldest kept is total-len(log).
	log   []string
	total int
	// noCursor answers as an Orca without cursor reads: no cursors, a cursor ignored.
	noCursor bool
}

// SetOutput sets what terminal.read without screen answers for a handle: tail is the output tail (the newest lines,
// rows redrawn in place as they ended, the line being written last; truncated: Orca let older lines of it go), log
// every completed line, redrawn rows each time, oldest kept first, and total the count of every completed line ever
// written (the cursor after the newest; the oldest kept is total-len(log)). A handle without output answers a tail read
// with the screen, as Orca does for a program on the alternate screen.
func (f *Fake) SetOutput(handle string, tail []string, truncated bool, log []string, total int) {
	f.mu.Lock()
	f.outputs[handle] = &output{tail: tail, tailTruncated: truncated, log: log, total: total}
	f.mu.Unlock()
}

// SetOldOutput sets a handle's output tail as an Orca without cursor reads answers it: no cursors, a cursor ignored.
func (f *Fake) SetOldOutput(handle string, tail []string) {
	f.mu.Lock()
	f.outputs[handle] = &output{tail: tail, noCursor: true}
	f.mu.Unlock()
}

// readOutputLocked answers terminal.read without screen as Orca 1.4.277 does (lFt in its runtime): a cursor pages
// the log, at most limit (default and cap 2000) lines; without one, the tail's last limit (default 120, cap 2000)
// lines, cut further to 32 KiB of text from the end.
func (f *Fake) readOutputLocked(h string, p map[string]any) map[string]any {
	o := f.outputs[h]
	if o == nil {
		s := f.screens[h]
		if s == nil {
			s = map[string]any{"tail": []string{}, "tailRows": []any{}}
		}
		out := map[string]any{"handle": h, "status": "running", "truncated": false, "source": "screen"}
		maps.Copy(out, s)
		return out
	}
	limit := func(def int) int {
		if v, ok := p["limit"].(float64); ok && v > 0 {
			return min(max(1, int(v)), 2000)
		}
		return def
	}
	oldest, latest := max(0, o.total-len(o.log)), o.total
	out := map[string]any{"handle": h, "status": "running", "source": "stream"}
	if !o.noCursor {
		out["oldestCursor"], out["nextCursor"], out["latestCursor"] = fmt.Sprint(oldest), fmt.Sprint(latest), fmt.Sprint(latest)
	}
	if c, ok := p["cursor"].(float64); ok && !o.noCursor {
		cursor := int(c)
		if cursor > latest {
			out["tail"], out["truncated"], out["limited"] = []string{}, false, false
			return out
		}
		from := max(cursor, oldest)
		rest := o.log[from-oldest:]
		lines := rest[:min(len(rest), limit(2000))]
		out["tail"], out["truncated"], out["limited"] = slices.Clone(lines), cursor < oldest, len(lines) < len(rest)
		out["nextCursor"] = fmt.Sprint(from + len(lines))
		return out
	}
	lines := slices.Clone(o.tail[max(0, len(o.tail)-limit(120)):])
	limited := len(lines) < len(o.tail)
	chars := 0
	for _, l := range lines {
		chars += len(l)
	}
	for len(lines) > 0 && chars-len(lines[0]) >= 32768 {
		chars -= len(lines[0])
		lines, limited = lines[1:], true
	}
	if chars > 32768 {
		lines[0], limited = lines[0][chars-32768:], true
	}
	out["tail"], out["truncated"], out["limited"] = lines, o.tailTruncated, limited
	return out
}

// Handle makes a method answer through fn (a Fail for an error).
func (f *Fake) Handle(method string, fn func(params map[string]any) any) {
	f.mu.Lock()
	f.on[method] = fn
	f.mu.Unlock()
}

// Trace is the calls so far ("method {params}"), only of the given methods when any are named, else all but the
// listing ones a refresh makes (status.get, terminal.list, worktree.ps, and session.tabs.list with the layouts).
func (f *Fake) Trace(methods ...string) []string {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []string
	for _, c := range f.log {
		if len(methods) > 0 && !slices.Contains(methods, c.Method) ||
			len(methods) == 0 && slices.Contains([]string{"status.get", "terminal.list", "worktree.ps", "session.tabs.list"}, c.Method) {
			continue
		}
		out = append(out, c.String())
	}
	return out
}

// Params are the params of every call of method so far, in order.
func (f *Fake) Params(method string) []map[string]any {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []map[string]any
	for _, c := range f.log {
		if c.Method == method {
			out = append(out, c.Params)
		}
	}
	return out
}

// Count is how many calls of method came.
func (f *Fake) Count(method string) int {
	f.mu.Lock()
	defer f.mu.Unlock()
	n := 0
	for _, c := range f.log {
		if c.Method == method {
			n++
		}
	}
	return n
}

// Reset forgets the calls and writes so far.
func (f *Fake) Reset() {
	f.mu.Lock()
	f.log, f.writes = nil, nil
	f.mu.Unlock()
}

// Token is the auth token the fake takes now (tests check it never leaks into logs).
func (f *Fake) Token() string {
	f.mu.Lock()
	defer f.mu.Unlock()
	return f.token
}

// SendCall formats an expected terminal.send trace line.
func SendCall(handle string, fields map[string]any) string {
	p := map[string]any{"terminal": handle}
	maps.Copy(p, fields)
	b, _ := json.Marshal(p)
	return fmt.Sprintf("terminal.send %s", b)
}
