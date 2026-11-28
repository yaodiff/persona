package extmgr

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"slices"
	"sync/atomic"
	"time"
	"unicode/utf8"

	"github.com/nwverse/persona/sdk/go/wire"
	"github.com/nwverse/persona/server/internal/buswire"
	"github.com/nwverse/persona/server/internal/connector"
	"github.com/nwverse/persona/server/internal/extension"
	"github.com/nwverse/persona/server/internal/extwire"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

// Memory sources (docs/extensions/extensions.md "Memory sources", #13532, #13586): a memory extension's
// memory_sources contribution answers memory.entries, read here for the assistant's memory assembler, which maps the
// pages (memoryExtensionBatchOf) and decides which entries reach which scene. Every call is bounded, every page's own
// fields are checked before any of it is used, and an extension whose read fails gives no entries this time: never its
// text instead. Once an extension declares the contribution (extension.DeclaresMemorySources), its text outlets are
// no longer delivered directly: its assistant instructions (extensionInstructions), its project and Inbox context
// (projectContext, inboxContext), its connectors' read tools in the assistant's conversations (memoryWithheld), its
// own MCP servers in the assistant (assistantMCPServers) and the skills in its data directory (extensionSkills).

// MemoryReadBounds are the bounds of reading one memory_sources contribution for an assembly; the assistant gives
// them (memory_extension_wire.go), so they are written once.
type MemoryReadBounds struct {
	// PageLimit is the limit each call asks for. MaxPages (when > 0), ReadTimeout and TotalMost bound the complete read, never a prefix.
	PageLimit   int
	MaxPages    int
	ReadTimeout time.Duration
	TotalMost   int
	// CallTimeout bounds one call; AnswerMost is the most bytes one answer may hold.
	CallTimeout time.Duration
	AnswerMost  int
}

// MemorySourceRead is what one memory_sources contribution answered: its pages in the order read, or why none (Err;
// the pages are then empty).
type MemorySourceRead struct {
	Extension, Contribution string
	// Name is the extension's name, what the Inbox's background calls it.
	Name  string
	Pages []wire.MemoryEntriesResult
	Err   error
}

// The bounds of one page as memory.schema.json states them: a page's own fields beyond them fail the answer.
const (
	memoryPageEntriesMost = 500
	memoryAfterMost       = 512
)

// memorySourceGeneration changes whenever an extension changes (installed, upgraded, removed, switched, set up), so
// the assistant's cache of what the memory sources answered is read again (memory_extension_source.go there).
var memorySourceGeneration atomic.Uint64

// memorySourcesChanged is told by changed: the memory sources are read again at the next assembly.
func memorySourcesChanged() { memorySourceGeneration.Add(1) }

// MemorySourcesGeneration is the extensions' generation: a different one means a cached read may be out of date.
func (h *Manager) MemorySourcesGeneration() uint64 { return memorySourceGeneration.Load() }

// errMemoryNotStarted is the one read before the extensions have started: nothing can be told yet, so the caller asks
// again soon rather than keeping "no memory sources".
var errMemoryNotStarted = errors.New("the extensions have not started")

// MemorySourceReads reads every memory_sources contribution that is on, one after another, by extension id. A
// contribution whose program does not run, or whose read fails, says why and is logged; before startup there is one
// read, failed with errMemoryNotStarted.
func (h *Manager) MemorySourceReads(ctx context.Context, b MemoryReadBounds) []MemorySourceRead {
	if h.extensions == nil {
		return nil
	}
	if !h.startedNow() {
		return []MemorySourceRead{{Err: errMemoryNotStarted}}
	}
	ps := h.extensions
	var out []MemorySourceRead
	for _, src := range ps.reg.MemorySources() {
		if !src.On {
			continue
		}
		r := MemorySourceRead{Extension: src.Extension, Contribution: src.Contribution, Name: src.ExtensionName}
		if hp := ps.get(src.Extension); hp == nil || hp.proc == nil {
			r.Err = errors.New("its program does not run")
		} else {
			r.Pages, r.Err = readMemoryPages(ctx, hp.proc, src.Contribution, b)
		}
		if r.Err != nil {
			r.Pages = nil
			ps.logf("plugin %s: memory.entries: none of its entries used this time: %v", src.Extension, r.Err)
		}
		out = append(out, r)
	}
	return out
}

// memoryCaller is the program a contribution's calls go to (a *host.Proc; a test's).
type memoryCaller interface {
	Call(ctx context.Context, method string, params, out any) error
}

// readMemoryPages follows the contribution's existing opaque cursors until the source ends. A failed page, repeated
// cursor or exhausted resource budget fails the whole read: an incomplete prefix must never become the recall cache.
func readMemoryPages(ctx context.Context, proc memoryCaller, contribution string, b MemoryReadBounds) ([]wire.MemoryEntriesResult, error) {
	ctx, cancel := context.WithTimeout(ctx, b.ReadTimeout)
	defer cancel()
	var pages []wire.MemoryEntriesResult
	sent := map[string]bool{}
	after, total := "", 0
	for {
		if err := ctx.Err(); err != nil {
			return nil, fmt.Errorf("incomplete memory read: %w", err)
		}
		page, size, err := memoryPage(ctx, proc, wire.MemoryEntriesParams{Contribution: contribution, Limit: b.PageLimit, After: after}, b)
		if err != nil {
			return nil, err
		}
		total += size
		if total > b.TotalMost {
			return nil, fmt.Errorf("incomplete memory read: answers over %d bytes", b.TotalMost)
		}
		pages = append(pages, page)
		if !page.HasMore || page.After == "" {
			return pages, nil
		}
		if b.MaxPages > 0 && len(pages) >= b.MaxPages {
			return nil, fmt.Errorf("incomplete memory read: over %d pages", b.MaxPages)
		}
		if sent[page.After] {
			return nil, errors.New("incomplete memory read: repeated cursor")
		}
		sent[page.After] = true
		after = page.After
	}
}

// memoryPage is one memory.entries call within b.CallTimeout, its answer checked (memoryPageOf).
func memoryPage(ctx context.Context, proc memoryCaller, params wire.MemoryEntriesParams, b MemoryReadBounds) (wire.MemoryEntriesResult, int, error) {
	cctx, cancel := context.WithTimeout(ctx, b.CallTimeout)
	defer cancel()
	raw, err := buswire.Call(cctx, proc, extwire.MemoryEntriesMethod, params)
	if err != nil {
		var re *rpc.Error
		switch {
		case ctx.Err() != nil:
			return wire.MemoryEntriesResult{}, 0, fmt.Errorf("canceled: %w", ctx.Err())
		case cctx.Err() != nil:
			return wire.MemoryEntriesResult{}, 0, fmt.Errorf("no answer in %s", b.CallTimeout)
		case errors.As(err, &re):
			return wire.MemoryEntriesResult{}, 0, fmt.Errorf("%s: %s", re.Code, repoWords(re.Message))
		}
		return wire.MemoryEntriesResult{}, 0, errors.New(repoWords(err.Error()))
	}
	page, err := memoryPageOf(raw, b.AnswerMost)
	return page, len(raw), err
}

// memoryPageShape is a page's own fields, each told missing from wrong.
type memoryPageShape struct {
	Entries *[]json.RawMessage `json:"entries"`
	Tagged  *bool              `json:"tagged"`
	HasMore *bool              `json:"has_more"`
	After   *string            `json:"after"`
	// StandingBytes is the budget the program wants for its standing entries; the assistant caps it.
	StandingBytes *int64 `json:"standing_bytes"`
}

// memoryPageOf is raw as a page: refused whole when it is over most bytes or its own fields (entries as an array of at
// most 500, tagged, has_more, after of at most 512 characters, standing_bytes of at least 0) do not hold to the schema. An entry that is not an
// entry object is dropped alone; memoryExtensionBatchOf (the assistant's) checks the rest of each entry's rules.
func memoryPageOf(raw json.RawMessage, most int) (wire.MemoryEntriesResult, error) {
	if len(raw) > most {
		return wire.MemoryEntriesResult{}, fmt.Errorf("an answer of %d bytes, over %d", len(raw), most)
	}
	var shape memoryPageShape
	if err := json.Unmarshal(raw, &shape); err != nil {
		return wire.MemoryEntriesResult{}, errors.New("an answer that is not a page")
	}
	switch {
	case shape.Entries == nil:
		return wire.MemoryEntriesResult{}, errors.New("a page without its entries")
	case shape.Tagged == nil || shape.HasMore == nil:
		return wire.MemoryEntriesResult{}, errors.New("a page without tagged or has_more")
	case len(*shape.Entries) > memoryPageEntriesMost:
		return wire.MemoryEntriesResult{}, fmt.Errorf("a page of %d entries, over %d", len(*shape.Entries), memoryPageEntriesMost)
	case shape.After != nil && utf8.RuneCountInString(*shape.After) > memoryAfterMost:
		return wire.MemoryEntriesResult{}, fmt.Errorf("a cursor over %d characters", memoryAfterMost)
	case shape.StandingBytes != nil && *shape.StandingBytes < 0:
		return wire.MemoryEntriesResult{}, errors.New("a negative standing_bytes")
	}
	page := wire.MemoryEntriesResult{Tagged: *shape.Tagged, HasMore: *shape.HasMore, Entries: make([]wire.MemoryEntry, 0, len(*shape.Entries))}
	if shape.After != nil {
		page.After = *shape.After
	}
	if shape.StandingBytes != nil {
		// Past any budget the assistant allows: kept at its cap there (memoryExtensionStandingMost), never wrapped.
		page.StandingBytes = int(min(*shape.StandingBytes, 1<<30))
	}
	for _, e := range *shape.Entries {
		var w wire.MemoryEntry
		if json.Unmarshal(e, &w) == nil {
			page.Entries = append(page.Entries, w)
		}
	}
	return page, nil
}

// memoryConnector says whether connector c is an extension's that declares memory_sources: the extension that runs it
// when one does (ownerOf), so a connector id another extension also names decides nothing; else (none runs it now)
// any extension that declares it and contributes c's id, the stricter reading.
func (ps *extensions) memoryConnector(c connector.Connector) bool {
	if ps == nil || ps.reg == nil || c == nil {
		return false
	}
	if hp := ps.ownerOf(c); hp != nil {
		return extension.DeclaresMemorySources(ps.reg.Get(hp.id))
	}
	return memoryConnectorIn(ps.reg.List(), c.ID())
}

// memoryConnectorIn says whether one of list that declares memory_sources contributes connector id (as installed, on
// or off).
func memoryConnectorIn(list []*extension.Extension, id string) bool {
	for _, p := range list {
		if !extension.DeclaresMemorySources(p) {
			continue
		}
		m := p.Full
		if m == nil {
			m = p.Manifest
		}
		if slices.ContainsFunc(m.Connectors(), func(c extension.ConnectorItem) bool { return c.ID == id }) {
			return true
		}
	}
	return false
}

// memoryWithheld says whether name is a read tool of a connector that an extension declaring memory_sources
// contributes: the assistant's conversations are not offered it, nor may they call it, as what it reads would reach
// them around the hub's filter (decisions_query recalls the entries instead). Its writes, and what coding Agents get
// of it (agent_tools), stay as they were.
func (h *Manager) memoryWithheld(name string) bool {
	if h.connectors == nil || h.extensions == nil || !h.connectors.ReadOnly(name) {
		return false
	}
	return h.extensions.memoryConnector(h.connectors.Owner(name))
}

// withoutMemoryReads is tools without the ones memoryWithheld withholds.
func (h *Manager) withoutMemoryReads(tools []proto.Tool) []proto.Tool {
	return slices.DeleteFunc(tools, func(t proto.Tool) bool { return h.memoryWithheld(t.Name) })
}

// memoryWithheldResult is the answer to a call of a withheld tool.
func memoryWithheldResult(name string) proto.ToolResult {
	return proto.TextResult(name+" is not offered in the assistant's conversations: Persona reads this extension's memory itself "+
		"and puts what each conversation may use in its context; decisions_query recalls more.", true)
}
