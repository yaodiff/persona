package knowledgeui

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"sync"
	"sync/atomic"
	"time"

	"github.com/nwverse/persona/sdk/go/wire"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/connectorwire"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/kb"
	"github.com/nwverse/persona/server/internal/modules/knowledge-ui/kbhistory"
	"github.com/nwverse/persona/server/internal/modules/knowledge-ui/kbsent"
)

// Host is what the module needs of the core: the assistant's Service, through internal/knowledgeuihost.Host, which
// has the same methods (internal/modules/all hands one to New). Each takes and gives only what both sides can name:
// the standard library's, the knowledge base's (kb), and the contract packages'.
type Host interface {
	Logf(format string, args ...any)
	Lang(ctx context.Context) i18n.Lang
	Location(ctx context.Context) *time.Location
	Engine(ctx context.Context) (*kb.Engine, error)
	WritableEngine(ctx context.Context) (*kb.Engine, error)
	Settings(ctx context.Context) *assistantwire.KBSettings
	History(ctx context.Context) *assistantwire.KBHistory
	Connected() bool
	Current() *kb.Engine
	Wake()
	Notify(ctx context.Context, title, body string)
	UpdateSelection(ctx context.Context, f func(*kb.Selection) (bool, error)) (*kb.Selection, error)
	RefreshNames(ctx context.Context, connector string, chats []connectorwire.HistoryChat)
	Archives(ctx context.Context) ([]assistantwire.ArchiveSource, error)
	HistoryChats(ctx context.Context, id string) ([]connectorwire.HistoryChat, error)
	HistoryPage(ctx context.Context, id string, q connectorwire.HistoryQuery) (connectorwire.HistoryPage, error)
	HistoryAnchors(ctx context.Context, id string, q connectorwire.HistoryAnchorsQuery) (connectorwire.HistoryAnchors, error)
	ArchiveMedia(ctx context.Context, id, ref string) (connectorwire.Media, error)
	HasSentLedger() bool
	SentAsUsersForArchive(ctx context.Context, connector string, since, until time.Time) ([]connectorwire.SentAsUser, error)
	DayStart(now time.Time) time.Time
}

// Layer is the module for one manager: the history import's shared state (one request in flight to any source, the
// source this process imports now, this process's instance) and what the core asks of it (knowledgeuihost.Layer).
type Layer struct {
	h        Host
	calls    sync.Mutex             // one request in flight to any source, the driver's and the tools' alike
	running  atomic.Pointer[string] // the source this process is importing now
	instance string
}

// New is the module's layer for h.
func New(h Host) *Layer {
	b := make([]byte, 8)
	_, _ = rand.Read(b)
	return &Layer{h: h, instance: hex.EncodeToString(b)}
}

// Tool runs one of the history import's tools (kbhistory/tools.go).
func (l *Layer) Tool(ctx context.Context, name string, args json.RawMessage) (string, error) {
	return l.tools().Call(ctx, name, args)
}

// RunOnce runs what the import of the knowledge base in data can run now.
func (l *Layer) RunOnce(ctx context.Context, data string) error {
	return kbhistory.NewDriver(l.deps(data)).RunOnce(ctx)
}

// Chats are the source's chats for the selection page. The import's calls are paced: the list is not asked for
// between them, nor while it is stopped; then they are the chats its plan listed.
func (l *Layer) Chats(ctx context.Context, data, source string) ([]connectorwire.HistoryChat, time.Time, bool, error) {
	var chats []kbhistory.PlanChat
	var listedAt time.Time
	cached := false
	t := l.tools()
	if _, stopped := kbhistory.ReadStop(data); stopped || t.BusyElsewhere(data) {
		if p, ok, err := kbhistory.LoadPlan(data, source); err == nil && ok {
			chats, listedAt = p.Chats, p.GeneratedAt
		}
		cached = true
	} else {
		list, err := t.ListChats(ctx, data, source)
		if err != nil {
			return nil, time.Time{}, false, err
		}
		l.h.RefreshNames(ctx, source, list)
		p := &kbhistory.Plan{Source: source}
		p.Regenerate(list, time.Now())
		chats, listedAt = p.Chats, p.GeneratedAt
	}
	out := make([]connectorwire.HistoryChat, 0, len(chats))
	for _, c := range chats {
		out = append(out, connectorwire.HistoryChat{ID: c.ID, Name: c.Name, Kind: c.Kind, Muted: c.Muted, Collapsed: c.Collapsed,
			Done: c.Done, Bot: c.Bot, Estimate: c.Estimate, Source: c.FiledAs})
	}
	return out, listedAt, cached, nil
}

// ChatsProblem says why Chats failed.
func (l *Layer) ChatsProblem(err error, lang i18n.Lang) (string, string) {
	var h *kbhistory.Halt
	switch {
	case errors.Is(err, kbhistory.ErrUnavailable):
		return "no_history", ""
	case errors.As(err, &h):
		return "stopped", h.ReasonText(lang)
	}
	return "failed", kbhistory.ErrCode(err)
}

// Need is what the import reads of chat c now (kbhistory.Need).
func (l *Layer) Need(sel *kb.Selection, ret kb.Retention, connector string, c connectorwire.HistoryChat) kb.ReadPlan {
	return kbhistory.Need(sel, ret, connector, kbhistory.PlanChat{ID: c.ID, Name: c.Name, Kind: c.Kind, Muted: c.Muted,
		Collapsed: c.Collapsed, Done: c.Done, Bot: c.Bot, Estimate: c.Estimate, FiledAs: c.Source})
}

// ProviderCheckpoint resolves the owner's exact connector identity before exposing any progress.
func (l *Layer) ProviderCheckpoint(e *kb.Engine, cfg *assistantwire.KBHistory, binding assistantwire.KBImportProvider, day string, now time.Time) (wire.KBImportCheckpointResult, bool) {
	return kbhistory.ProviderCheckpoint(e, cfg, binding, day, now)
}

// Day is the day p names with the lines the owner sent from Persona marked (kbsent).
func (l *Layer) Day(ctx context.Context, e *kb.Engine, p assistantwire.KBDayParams) (assistantwire.KBDayResult, error) {
	var st any
	if l.h.HasSentLedger() {
		st = l.h
	}
	return kbsent.Day(ctx, st, l.h.DayStart, e, p)
}

// tools are the assistant's history import tools.
func (l *Layer) tools() *kbhistory.Tools {
	h := l.h
	return kbhistory.NewTools(kbhistory.Host{
		Lang: h.Lang, Engine: h.Engine, Settings: h.Settings, Location: h.Location,
		Deps: l.deps, Running: &l.running, Instance: l.instance,
		Wake: h.Wake, Logf: h.Logf, UpdateSelection: h.UpdateSelection, RefreshNames: h.RefreshNames,
	})
}

func (l *Layer) deps(data string) kbhistory.Deps {
	h := l.h
	return kbhistory.Deps{
		Data:      data,
		Src:       source{h},
		Eng:       h.WritableEngine,
		Cfg:       h.History,
		Connected: h.Connected,
		Logf:      h.Logf,
		Notify: func(ctx context.Context, src string, why kbhistory.Reason) {
			lang := h.Lang(ctx)
			title, body := "assistant-kb_history.stopped_title", "assistant-kb_history.stopped_body"
			if why.Code == "plan_changed" {
				title, body = "assistant-kb_history.plan_changed_title", "assistant-kb_history.plan_changed_body"
			}
			h.Notify(ctx, i18n.T(lang, title),
				i18n.T(lang, body, i18n.Args{"source": src, "reason": kbhistory.ReasonText(lang, why), "count": why.Args["count"]}))
		},
		Calls:    &l.calls,
		Instance: l.instance,
		Indexed:  func() bool { return kbhistory.Indexed(h.Current()) },
		Running:  &l.running,
	}
}

// source reaches the connectors through the core's host (the hub's connectors.*).
type source struct{ h Host }

func (s source) Archives(ctx context.Context) ([]assistantwire.ArchiveSource, error) {
	return s.h.Archives(ctx)
}

func (s source) Chats(ctx context.Context, id string) ([]connectorwire.HistoryChat, error) {
	return s.h.HistoryChats(ctx, id)
}

func (s source) Page(ctx context.Context, id string, q connectorwire.HistoryQuery) (connectorwire.HistoryPage, error) {
	return s.h.HistoryPage(ctx, id, q)
}

func (s source) Anchors(ctx context.Context, id string, q connectorwire.HistoryAnchorsQuery) (connectorwire.HistoryAnchors, error) {
	return s.h.HistoryAnchors(ctx, id, q)
}

func (s source) Media(ctx context.Context, id, ref string) (connectorwire.Media, error) {
	return s.h.ArchiveMedia(ctx, id, ref)
}
