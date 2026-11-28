package extmgr

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/sdk/go/wire"
	"github.com/nwverse/persona/server/internal/connector"
	"github.com/nwverse/persona/server/internal/extension"
	pserve "github.com/nwverse/persona/server/internal/extension/serve"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

// serveMemorySourceExtensionForTest is a fake memory extension's program: memory.entries answers two pages of fake
// entries, the second after the cursor "page-2".
func serveMemorySourceExtensionForTest() {
	_ = pserve.Serve(context.Background(), os.Stdin, os.Stdout, pserve.Program{ID: "qa-memory", Version: "1.0.0",
		Namespaces: map[string]pserve.Handler{
			"memory": func(_ context.Context, method string, params json.RawMessage) (any, error) {
				if method != "entries" {
					return nil, pserve.UnknownMethod(method)
				}
				var p wire.MemoryEntriesParams
				_ = json.Unmarshal(params, &p)
				if p.After == "page-2" {
					return wire.MemoryEntriesResult{Tagged: true, Entries: []wire.MemoryEntry{
						{ID: "qa-finance", Text: "qa: quarterly budget envelope totals", Status: "active", OriginSpace: "qa-finance", Categories: []string{"finance"}},
					}}, nil
				}
				return wire.MemoryEntriesResult{Tagged: true, HasMore: true, After: "page-2", Entries: []wire.MemoryEntry{
					{ID: "qa-plain", Text: "qa: notes are written in short lines", Status: "active", Standing: true, OriginSpace: "qa-plain"},
				}}, nil
			}}})
}

// memorySourcePackage is a fake memory extension: memory_sources, and the text outlets it had before it declared it
// (package instructions, a project's context and the Inbox's), each saying "qa-memory-outlet".
func memorySourcePackage(t *testing.T) string {
	dir := t.TempDir()
	self, _ := os.Executable()
	_ = os.MkdirAll(filepath.Join(dir, "bin"), 0o755)
	_ = os.WriteFile(filepath.Join(dir, "bin", "memory"), []byte("#!/bin/sh\nPERSONA_TEST_PLUGIN=memory exec "+self+" \"$@\"\n"), 0o755)
	_ = os.MkdirAll(filepath.Join(dir, "assistant"), 0o755)
	_ = os.WriteFile(filepath.Join(dir, "assistant", "about.md"), []byte("qa-memory-outlet: the user likes short answers.\n"), 0o644)
	manifest := fmt.Sprintf(`{"manifest_version": 1, "id": "qa-memory", "name": "QA Memory", "version": "1.0.0",
		"engines": {"plugin_protocol": 1}, "runtime": {"command": {%q: "bin/memory"}},
		"contributes": {"memory_sources": [{"id": "memory", "name": "QA memory"}],
			"prompts": [{"id": "about", "kind": "instructions", "file": "assistant/about.md"},
				{"id": "projects", "kind": "project_context", "source": "data", "dir": "agent/projects"},
				{"id": "inbox", "kind": "inbox_context", "file": "inbox/context.md", "never_quote": "inbox/never_quote.txt"}]}}`,
		runtime.GOOS+"-"+runtime.GOARCH)
	_ = os.WriteFile(filepath.Join(dir, extension.ManifestFile), []byte(manifest), 0o644)
	return dir
}

// An extension that declares memory_sources: the install page says so, the manager reads its entries page by page,
// and its text outlets (instructions, project context, Inbox context) no longer reach the prompts, while the same
// outlets of an extension that does not declare it still do. The sentences a draft must not repeat still count.
func TestMemorySourceOutlets(t *testing.T) {
	h, _ := extensionTestHub(t)
	mux := h.routes()
	master := h.cfg.Token
	ctx := context.Background()

	// Prompt ids of its own: the registry refuses an id another installed extension contributes (#13649).
	plain := declarativePackage(t, `{"manifest_version": 1, "id": "qa-notes", "name": "QA Notes", "version": "1.0.0", "contributes": {
		"prompts": [{"id": "notes-about", "kind": "instructions", "file": "assistant/about.md"},
			{"id": "notes-projects", "kind": "project_context", "source": "data", "dir": "agent/projects"},
			{"id": "notes-inbox", "kind": "inbox_context", "file": "inbox/context.md"}]}}`)
	_ = os.MkdirAll(filepath.Join(plain, "assistant"), 0o755)
	_ = os.WriteFile(filepath.Join(plain, "assistant", "about.md"), []byte("qa-notes-outlet: notes go in the weekly file.\n"), 0o644)
	installForTest(t, h, plain)

	mem := memorySourcePackage(t)
	code, pv := call(t, mux, "POST", "/api/v1/extensions/-/preview", master, `{"source": "dir:`+mem+`"}`)
	if code != 200 {
		t.Fatalf("preview: %d %v", code, pv)
	}
	if !strings.Contains(fmt.Sprint(pv["permissions"]), "memory_source") {
		t.Fatalf("the install page says the hub reads its memory: %v", pv["permissions"])
	}
	if c, body := call(t, mux, "POST", "/api/v1/extensions/-/install", master, fmt.Sprintf(`{"preview_id": %q, "permissions_digest": %q}`, pv["preview_id"], pv["permissions_digest"])); c != 200 {
		t.Fatalf("install: %d %v", c, body)
	}
	for _, id := range []string{"qa-notes", "qa-memory"} {
		data := h.extensions.reg.DataDir(id)
		mustWrite(t, filepath.Join(data, "agent", "projects", "qa-plain.md"), id+"-outlet: builds run at night.\n")
		mustWrite(t, filepath.Join(data, "inbox", "context.md"), id+"-outlet: the user answers briefly.\n")
	}
	mustWrite(t, filepath.Join(h.extensions.reg.DataDir("qa-memory"), "inbox", "never_quote.txt"), "Thinks the vendor is unreliable\n")

	bounds := MemoryReadBounds{PageLimit: 200, ReadTimeout: 2 * time.Minute, TotalMost: 128 << 20, CallTimeout: 5 * time.Second, AnswerMost: 1 << 20}
	var reads []MemorySourceRead
	waitLong(t, "the memory source read", func() bool {
		reads = h.MemorySourceReads(ctx, bounds)
		return len(reads) == 1 && reads[0].Err == nil
	})
	if r := reads[0]; r.Extension != "qa-memory" || r.Contribution != "memory" || r.Name != "QA Memory" || len(r.Pages) != 2 ||
		r.Pages[0].Entries[0].ID != "qa-plain" || !r.Pages[0].Entries[0].Standing || r.Pages[1].Entries[0].ID != "qa-finance" {
		t.Fatalf("read %+v", r)
	}
	gen := h.MemorySourcesGeneration()

	got := h.extensions.extensionInstructions(ctx, "")
	if !strings.Contains(got, "qa-notes-outlet") || strings.Contains(got, "qa-memory-outlet") {
		t.Errorf("instructions: %q", got)
	}
	blocks := h.extensions.projectContext([]string{"qa-plain"})
	if len(blocks) != 1 || blocks[0].Extension != "qa-notes" {
		t.Errorf("project context: %+v", blocks)
	}
	inbox := h.extensions.inboxContext()
	if len(inbox.Blocks) != 1 || inbox.Blocks[0].Extension != "qa-notes" || strings.Join(inbox.NeverQuote, "|") != "Thinks the vendor is unreliable" {
		t.Errorf("Inbox context: %+v", inbox)
	}

	// Switched off, its contribution is not read; its outlets stay withheld, as it still declares the contribution.
	if code, out := call(t, mux, "PUT", "/api/v1/extensions/qa-memory/-/contributions/memory_sources/memory", master, `{"enabled": false}`); code != 200 {
		t.Fatalf("%d %v", code, out)
	}
	waitLong(t, "the generation moved and the contribution is not read", func() bool {
		return h.MemorySourcesGeneration() != gen && len(h.MemorySourceReads(ctx, bounds)) == 0
	})
	if got := h.extensions.extensionInstructions(ctx, ""); strings.Contains(got, "qa-memory-outlet") {
		t.Errorf("switched off: its instructions came back: %q", got)
	}
}

// fakeMemoryCaller answers memory.entries from pages by the cursor it is asked with ("" the first), or err.
type fakeMemoryCaller struct {
	pages map[string]json.RawMessage
	err   error
	asked []string
}

func (f *fakeMemoryCaller) Call(ctx context.Context, method string, params, out any) error {
	p := params.(wire.MemoryEntriesParams)
	f.asked = append(f.asked, p.After)
	if method != "memory.entries" || p.Contribution != "memory" || p.Limit != 200 {
		return fmt.Errorf("asked %s %+v", method, p)
	}
	if f.err != nil {
		if errors.Is(f.err, context.DeadlineExceeded) {
			<-ctx.Done()
			return ctx.Err()
		}
		return f.err
	}
	*(out.(*json.RawMessage)) = f.pages[p.After]
	return nil
}

func memoryTestBounds() MemoryReadBounds {
	return MemoryReadBounds{PageLimit: 200, ReadTimeout: 2 * time.Minute, TotalMost: 128 << 20, CallTimeout: 50 * time.Millisecond, AnswerMost: 1 << 20}
}

// Existing plugins still receive only contribution, limit and after. Short pages are followed too, through pools
// larger than the old five-page limit, without treating a full prefix as the complete source.
func TestMemoryPagesComplete(t *testing.T) {
	f := &fakeMemoryCaller{pages: map[string]json.RawMessage{}}
	const count = 24001
	for start := 0; start < count; start += 137 {
		page := wire.MemoryEntriesResult{Tagged: true}
		for i := start; i < min(start+137, count); i++ {
			page.Entries = append(page.Entries, wire.MemoryEntry{ID: fmt.Sprintf("qa-%d", i), Text: "qa: entry", Status: "active"})
		}
		if start+137 < count {
			page.HasMore, page.After = true, fmt.Sprintf("cursor-%d", start+137)
		}
		after := ""
		if start > 0 {
			after = fmt.Sprintf("cursor-%d", start)
		}
		f.pages[after], _ = json.Marshal(page)
	}
	pages, err := readMemoryPages(t.Context(), f, "memory", memoryTestBounds())
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, p := range pages {
		n += len(p.Entries)
	}
	if n != count || len(f.asked) != len(f.pages) || pages[len(pages)-1].Entries[0].ID != "qa-23975" {
		t.Fatalf("read %d entries in %d pages, asked %d", n, len(pages), len(f.asked))
	}
}

func TestMemoryPagesIncomplete(t *testing.T) {
	first := json.RawMessage(`{"entries": [], "tagged": true, "has_more": true, "after": "next"}`)
	last := json.RawMessage(`{"entries": [], "tagged": true, "has_more": false}`)
	for _, name := range []string{"cursor cycle", "total bytes", "total timeout", "late page failure"} {
		t.Run(name, func(t *testing.T) {
			b := memoryTestBounds()
			f := &fakeMemoryCaller{pages: map[string]json.RawMessage{"": first, "next": last}}
			switch name {
			case "cursor cycle":
				f.pages["next"] = first
			case "total bytes":
				b.TotalMost = len(first) + len(last) - 1
			case "total timeout":
				b.ReadTimeout = time.Nanosecond
			case "late page failure":
				f.pages["next"] = json.RawMessage(`{"entries": []}`)
			}
			if pages, err := readMemoryPages(t.Context(), f, "memory", b); err == nil || pages != nil {
				t.Fatalf("incomplete source cached: %d pages, error %v", len(pages), err)
			}
		})
	}
	// Retain the protocol's compatibility rule: has_more with no cursor means end of source.
	f := &fakeMemoryCaller{pages: map[string]json.RawMessage{"": json.RawMessage(`{"entries": [], "tagged": true, "has_more": true}`)}}
	if pages, err := readMemoryPages(t.Context(), f, "memory", memoryTestBounds()); err != nil || len(pages) != 1 {
		t.Fatalf("legacy end: %d pages, %v", len(pages), err)
	}
}

// A page whose own fields break the schema, an answer over the bound, an error and a timeout each fail the whole read;
// an entry that is not an entry object is dropped alone.
func TestMemoryPagesRefused(t *testing.T) {
	ctx := context.Background()
	for name, raw := range map[string]string{
		"null":                    `null`,
		"no entries":              `{"tagged": true, "has_more": false}`,
		"entries null":            `{"entries": null, "tagged": true, "has_more": false}`,
		"entries a string":        `{"entries": "qa", "tagged": true, "has_more": false}`,
		"no tagged":               `{"entries": [], "has_more": false}`,
		"tagged a string":         `{"entries": [], "tagged": "yes", "has_more": false}`,
		"no has_more":             `{"entries": [], "tagged": true}`,
		"after a number":          `{"entries": [], "tagged": true, "has_more": false, "after": 2}`,
		"after too long":          `{"entries": [], "tagged": true, "has_more": false, "after": "` + strings.Repeat("a", memoryAfterMost+1) + `"}`,
		"too many":                `{"entries": [` + strings.TrimSuffix(strings.Repeat(`{},`, memoryPageEntriesMost+1), ",") + `], "tagged": true, "has_more": false}`,
		"standing_bytes negative": `{"entries": [], "tagged": true, "has_more": false, "standing_bytes": -1}`,
		"standing_bytes a string": `{"entries": [], "tagged": true, "has_more": false, "standing_bytes": "4096"}`,
	} {
		f := &fakeMemoryCaller{pages: map[string]json.RawMessage{"": json.RawMessage(raw)}}
		if pages, err := readMemoryPages(ctx, f, "memory", memoryTestBounds()); err == nil {
			t.Errorf("%s: read %+v", name, pages)
		}
	}
	big := &fakeMemoryCaller{pages: map[string]json.RawMessage{"": json.RawMessage(`{"entries": [], "tagged": true, "has_more": false}`)}}
	b := memoryTestBounds()
	b.AnswerMost = 10
	if _, err := readMemoryPages(ctx, big, "memory", b); err == nil || !strings.Contains(err.Error(), "over 10") {
		t.Errorf("an answer over the bound: %v", err)
	}
	for name, err := range map[string]error{
		"not ready": &rpc.Error{Code: wire.MemoryErrorCodeNotReady, Message: "loading\nstill"},
		"timeout":   context.DeadlineExceeded,
	} {
		f := &fakeMemoryCaller{err: err}
		if _, got := readMemoryPages(ctx, f, "memory", memoryTestBounds()); got == nil || strings.Contains(got.Error(), "\n") {
			t.Errorf("%s: %v", name, got)
		}
	}
	mixed := &fakeMemoryCaller{pages: map[string]json.RawMessage{"": json.RawMessage(`{"entries": [3, {"id": "qa-ok", "text": "qa: ok", "status": "active"},
		{"id": "qa-rank", "text": "qa: rank", "status": "active", "rank": "high"}], "tagged": true, "has_more": false}`)}}
	pages, err := readMemoryPages(ctx, mixed, "memory", memoryTestBounds())
	if err != nil || len(pages) != 1 || len(pages[0].Entries) != 1 || pages[0].Entries[0].ID != "qa-ok" {
		t.Errorf("single entries dropped alone: %+v %v", pages, err)
	}
}

// The connectors of an extension that declares memory_sources are the ones whose read tools are withheld from the
// assistant's conversations; another extension's are not.
func TestMemoryConnector(t *testing.T) {
	mk := func(id, contributes string) *extension.Extension {
		m := &extension.Manifest{ID: id, Name: id}
		_ = json.Unmarshal([]byte(contributes), &m.Contributes)
		return &extension.Extension{Record: extension.Record{ID: id, Enabled: true}, Manifest: m, Full: m}
	}
	memory := mk("qa-memory", `{"memory_sources": [{"id": "memory", "name": "QA memory"}], "connectors": [{"id": "qamem", "name": "QA", "capabilities": ["tools"]}]}`)
	other := mk("qa-other", `{"connectors": [{"id": "qaother", "name": "QA", "capabilities": ["tools"]}]}`)
	if !extension.DeclaresMemorySources(memory) || extension.DeclaresMemorySources(other) {
		t.Fatal("declares")
	}
	if !memoryConnectorIn([]*extension.Extension{other, memory}, "qamem") || memoryConnectorIn([]*extension.Extension{other, memory}, "qaother") {
		t.Error("memoryConnectorIn")
	}
}

// memReadConnector is a fake connector with two read tools: <id>_read only the assistant gets, <id>_agentread the
// coding agents get too.
type memReadConnector struct{ id string }

func (c *memReadConnector) ID() string   { return c.id }
func (c *memReadConnector) Name() string { return "QA" }
func (c *memReadConnector) Manifest() connector.Manifest {
	return connector.Manifest{Capabilities: []connector.Capability{connector.CapTools}, AgentTools: []string{c.id + "_agentread"}}
}
func (c *memReadConnector) Tools() []proto.Tool {
	return []proto.Tool{{Name: c.id + "_read", Description: "qa: read"}, {Name: c.id + "_agentread", Description: "qa: read for agents"}}
}
func (c *memReadConnector) Call(context.Context, string, json.RawMessage) (string, error) {
	return "qa: entries", nil
}

// memorySourceRegistry is an extension registry holding manifests, each a fake extension's contributes.
func memorySourceRegistry(t *testing.T, contributes map[string]string) *extension.Registry {
	t.Helper()
	reg := extension.NewRegistry(t.TempDir(), nil, nil, 0)
	for id, c := range contributes {
		m := &extension.Manifest{ManifestVersion: extension.ManifestVersion, ID: id, Name: id, Version: "1.0.0",
			Engines: extension.Engines{ExtensionProtocol: extension.Protocol}, Runtime: &extension.Runtime{Command: extension.Command{Path: id}}}
		if err := json.Unmarshal([]byte(c), &m.Contributes); err != nil {
			t.Fatal(err)
		}
		if err := reg.AddBuiltin(m, "/opt/"+id, true); err != nil {
			t.Fatal(err)
		}
	}
	if err := reg.Load(context.Background()); err != nil {
		t.Fatal(err)
	}
	return reg
}

// The read tools of a declaring extension's connector are neither listed for the assistant's conversations nor callable
// there, while the coding agents' list keeps its agent tool; another extension's connector keeps both. Its own MCP
// servers do not reach the assistant either; another extension's do.
func TestMemoryWithheldTools(t *testing.T) {
	ctx := context.Background()
	h := newTestManager(t)
	h.connectors = connector.NewRegistry(nil)
	h.extensions = newExtensions(t.TempDir(), nil, h.logf)
	h.extensions.reg = memorySourceRegistry(t, map[string]string{
		"qa-memory": `{"memory_sources": [{"id": "memory", "name": "QA memory"}], "connectors": [{"id": "qamem", "name": "QA", "capabilities": ["tools"]}],
			"mcp_servers": [{"id": "qamemrecall", "command": "qa-memory", "args": ["mcp"]}]}`,
		"qa-other": `{"connectors": [{"id": "qaother", "name": "QA", "capabilities": ["tools"]}],
			"mcp_servers": [{"id": "qaotherrecall", "command": "qa-other", "args": ["mcp"]}]}`,
	})
	for _, id := range []string{"qamem", "qaother"} {
		if err := h.addConnector(&memReadConnector{id: id}); err != nil {
			t.Fatal(err)
		}
	}
	names := func(tools []proto.Tool) string {
		var out []string
		for _, tl := range tools {
			out = append(out, tl.Name)
		}
		return strings.Join(out, ",")
	}
	agents, assistantOnly := h.AgentTools(ctx, true)
	offered := append(slices.Clone(agents), assistantOnly...)
	for _, name := range []string{"qamem_read", "qamem_agentread"} {
		if slices.ContainsFunc(offered, func(tl proto.Tool) bool { return tl.Name == name }) {
			t.Errorf("%s offered in the assistant's conversations: %s", name, names(offered))
		}
	}
	for _, name := range []string{"qaother_read", "qaother_agentread"} {
		if !slices.ContainsFunc(offered, func(tl proto.Tool) bool { return tl.Name == name }) {
			t.Errorf("%s not offered in the assistant's conversations: %s", name, names(offered))
		}
	}
	if agents, _ := h.AgentTools(ctx, false); names(agents) != "qamem_agentread,qaother_agentread" {
		t.Errorf("coding agents' tools = %s", names(agents))
	}
	for _, name := range []string{"qamem_read", "qamem_agentread"} {
		if res, handled := h.CallTool(ctx, "qa-machine", "qa-main", name, json.RawMessage(`{}`), true); !handled || !res.IsError {
			t.Errorf("%s called in an assistant conversation: %+v %v", name, res, handled)
		}
	}
	var servers []string
	for _, s := range h.extensions.assistantMCPServers() {
		servers = append(servers, s.ID)
	}
	if got := strings.Join(servers, ","); got != "qaotherrecall" {
		t.Errorf("assistant MCP servers = %s", got)
	}
}

// A page's standing_bytes, the budget the program wants for its standing entries (#15312), goes through as stated:
// the assistant caps it. Left out, it is 0.
func TestMemoryPageStandingBytes(t *testing.T) {
	ctx := context.Background()
	for raw, want := range map[string]int{
		`{"entries": [], "tagged": true, "has_more": false, "standing_bytes": 6144}`:        6144,
		`{"entries": [], "tagged": true, "has_more": false, "standing_bytes": 99999999999}`: 1 << 30,
		`{"entries": [], "tagged": true, "has_more": false}`:                                0,
	} {
		f := &fakeMemoryCaller{pages: map[string]json.RawMessage{"": json.RawMessage(raw)}}
		pages, err := readMemoryPages(ctx, f, "memory", memoryTestBounds())
		if err != nil || len(pages) != 1 || pages[0].StandingBytes != want {
			t.Errorf("%s: %+v, %v; want standing_bytes %d", raw, pages, err, want)
		}
	}
}

func TestReadMemoryPagesMaxPagesBound(t *testing.T) {
	ctx := context.Background()
	f := &fakeMemoryCaller{pages: map[string]json.RawMessage{
		"":   json.RawMessage(`{"entries": [], "tagged": true, "has_more": true, "after": "p2"}`),
		"p2": json.RawMessage(`{"entries": [], "tagged": true, "has_more": false}`),
	}}
	b := memoryTestBounds()
	b.MaxPages = 1
	if _, err := readMemoryPages(ctx, f, "memory", b); err == nil || !strings.Contains(err.Error(), "over 1 pages") {
		t.Fatalf("MaxPages=1: err=%v", err)
	}
	b.MaxPages = 2
	pages, err := readMemoryPages(ctx, f, "memory", b)
	if err != nil || len(pages) != 2 {
		t.Fatalf("MaxPages=2: pages=%d err=%v", len(pages), err)
	}
}
