// Package ext is an extension program's side of the process protocol (docs/extensions/extensions.md "Process extension protocol"): it answers
// initialize, ping and manifest, hands each namespaced call (connector.status, ui.render, …) to its handler, and
// lets the program call the hub and send it events. It has no dependencies beyond the standard library and the
// generated wire types, so an extension's repository can carry a copy (scripts/gen/sync-sdk.sh).
//
// A program:
//
//	func main() {
//		ext.Main(ext.Program{
//			ID: "example", Version: "1.0.0", Manifest: manifest,
//			UI: map[string]ext.Page{"home": {Render: home}},
//		})
//	}
package ext

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"sync"
	"sync/atomic"
	"syscall"

	"github.com/nwverse/persona/sdk/go/wire"
)

// Protocol is the version of the process protocol this SDK speaks.
const Protocol = 1

// Handler answers the calls of one namespace: method is without the namespace ("status" for connector.status),
// params as sent (with "contribution").
type Handler func(ctx context.Context, method string, params json.RawMessage) (any, error)

// Program is an extension program.
type Program struct {
	ID, Version string
	// Manifest is its persona-extension.json, answered to manifest (an extension running elsewhere must have it; one
	// the hub starts from its package may leave it out).
	Manifest json.RawMessage
	// Init is called once, with what the hub sent; it says what the program contributes this run (a subset of its
	// manifest's; nil is all of them), and may start the program's own work with ctx (which ends with the program).
	Init func(ctx context.Context, init *Init, hub *Hub) (contributes map[string]any, err error)
	// Namespaces are the handlers by namespace ("connector", "quota", …). "ui" is UI's unless given here.
	Namespaces map[string]Handler
	// UI are the pages by ui contribution id: ui.render and ui.action go to them (widgets too: placement widget).
	UI map[string]Page
	// Shortcuts are the shortcuts by contribution id: shortcut.run goes to them. "shortcut" is Shortcuts' unless
	// given in Namespaces.
	Shortcuts map[string]Shortcut
	// RoutineFacts collects the day's facts and report sections by manifest contribution ID.
	RoutineFacts map[string]RoutineFactsCollector
	// HTTP, if set, is served on the socket the hub gives (runtime.http in the manifest) by Main.
	HTTP http.Handler
}

// Init is what the hub sent with initialize.
type Init struct {
	wire.InitializeParams
	// Raw is initialize's params as sent, for fields this SDK does not know yet.
	Raw json.RawMessage
}

// Info is the extension's own (id, version, directories), under whichever key the hub sent it.
func (i *Init) Info() wire.ExtensionInfo {
	if i.Extension != nil {
		return *i.Extension
	}
	if i.Plugin != nil {
		return *i.Plugin
	}
	return wire.ExtensionInfo{}
}

// Config decodes the configuration (the schema's defaults filled in) into v.
func (i *Init) Config(v any) error {
	if len(i.InitializeParams.Config) == 0 {
		return nil
	}
	return json.Unmarshal(i.InitializeParams.Config, v)
}

// Hub is the hub, for a program's calls and events.
type Hub struct{ p *peer }

// Call calls a method of the hub's (connector.secrets.load, quota.http, …) and decodes its result into out.
func (h *Hub) Call(ctx context.Context, method string, params, out any) error {
	return h.p.call(ctx, method, params, out)
}

// Notify sends the hub an event (connector.changed, …).
func (h *Hub) Notify(event string, params any) error { return h.p.notify(event, params) }

// Badge sets the badge on a nav_page's entry; empty text clears it.
func (h *Hub) Badge(contribution, text, tone string) error {
	return h.Notify("ui.badge", wire.BadgeEvent{Contribution: contribution, Text: text, Tone: tone})
}

// Invalidate tells the apps that a ui contribution's data changed ("" is all of this extension's), so the pages on
// screen draw again. Hubs before UI version 3 ignore it; pages there redraw on their refresh_after.
func (h *Hub) Invalidate(contribution string) error {
	params := map[string]string{}
	if contribution != "" {
		params["contribution"] = contribution
	}
	return h.Notify("ui.invalidate", params)
}

// Serve serves prog on in / out until the hub closes them (the program should then exit) or ctx ends.
func Serve(ctx context.Context, in io.Reader, out io.Writer, prog Program) error {
	ctx, cancel := context.WithCancel(ctx)
	defer cancel()
	p := newPeer(out)
	s := &server{prog: prog, hub: &Hub{p: p}, ctx: ctx}
	p.onRead = s.read
	p.onRequest = s.handle
	p.onEvent = s.event
	return p.serve(ctx, in)
}

// Main runs prog as the hub starts it: the protocol on stdin / stdout (unless the manifest says rpc: none, when
// the hub sends nothing there), HTTP on the hub's socket when prog has HTTP. It returns only to exit: when the hub
// closes stdin or stops the program, after the HTTP requests in flight are answered (see ServeHTTP).
func Main(prog Program) {
	sig, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	if len(os.Args) > 1 && os.Args[1] == "manifest" && len(prog.Manifest) > 0 {
		_, _ = os.Stdout.Write(append(answeredManifest(prog.Manifest), '\n'))
		return
	}
	// ctx also ends when stdin closes. Not through stop: the hub's SIGTERM would then kill the program while it
	// drains its HTTP requests.
	ctx, cancel := context.WithCancel(sig)
	defer cancel()
	var httpDone chan error
	if prog.HTTP != nil {
		sock := Env().Socket
		if sock == "" {
			fmt.Fprintln(os.Stderr, "no PERSONA_EXTENSION_SOCKET: runtime.http is not set in the manifest")
			os.Exit(2)
		}
		httpDone = make(chan error, 1)
		go func() { httpDone <- ServeHTTP(ctx, sock, prog.HTTP) }()
	}
	in := &counting{r: os.Stdin}
	rpcDone := make(chan error, 1)
	go func() { rpcDone <- Serve(ctx, in, os.Stdout, prog) }()
	var err error
	for running := true; running; {
		select {
		case <-ctx.Done():
			running = false
		case err = <-rpcDone:
			// rpc: none (only HTTP): the hub sends nothing on stdin; serve until it stops the program.
			running = err == io.EOF && httpDone != nil && in.n == 0
		case err = <-httpDone:
			httpDone, running = nil, false
		}
	}
	failed := err != nil && err != io.EOF && ctx.Err() == nil
	if failed {
		fmt.Fprintln(os.Stderr, err)
	}
	cancel()
	if httpDone != nil {
		if err := <-httpDone; err != nil {
			fmt.Fprintln(os.Stderr, err)
		}
	}
	if failed {
		os.Exit(1)
	}
}

// counting counts what was read, to tell a hub that speaks the protocol from one that sends nothing.
type counting struct {
	r io.Reader
	n int64
}

func (c *counting) Read(p []byte) (int, error) {
	n, err := c.r.Read(p)
	c.n += int64(n)
	return n, err
}

type server struct {
	prog Program
	hub  *Hub
	ctx  context.Context

	pushes pushes

	mu          sync.Mutex
	initialized atomic.Bool
	init        *Init
	contributes map[string]json.RawMessage

	// initializing is open while inits initializes that were read are not answered yet: a call read behind one
	// waits for its answer, as in the TS SDK.
	imu          sync.Mutex
	inits        int
	initializing chan struct{}
}

// initWait is the context key of the initializes a call was read behind.
type initWait struct{}

// read notes an initialize as it is read, and gives a call read behind an unanswered one the initializes to wait for.
func (s *server) read(ctx context.Context, method string) context.Context {
	s.imu.Lock()
	defer s.imu.Unlock()
	if method == wire.MethodInitialize {
		if s.inits == 0 {
			s.initializing = make(chan struct{})
		}
		s.inits++
		return ctx
	}
	if s.initializing == nil {
		return ctx
	}
	return context.WithValue(ctx, initWait{}, s.initializing)
}

// initDone ends an initialize noted by read.
func (s *server) initDone() {
	s.imu.Lock()
	defer s.imu.Unlock()
	if s.inits == 0 {
		return
	}
	if s.inits--; s.inits == 0 {
		close(s.initializing)
		s.initializing = nil
	}
}

// waitInit waits for the initializes a call was read behind to be answered (or ctx to end).
func waitInit(ctx context.Context) {
	ch, _ := ctx.Value(initWait{}).(chan struct{})
	if ch == nil {
		return
	}
	select {
	case <-ch:
	case <-ctx.Done():
	}
}

func (s *server) handle(ctx context.Context, method string, params json.RawMessage) (any, error) {
	switch method {
	case wire.MethodInitialize:
		defer s.initDone()
		return s.initialize(params)
	case wire.MethodManifest:
		if len(s.prog.Manifest) == 0 {
			return nil, &Error{Code: wire.ErrorCodeUnknownMethod, Message: "no manifest here: it is in the package"}
		}
		return answeredManifest(s.prog.Manifest), nil
	}
	// A call sent right behind initialize (without waiting for its answer) waits for it; one before it is not
	// initialized.
	waitInit(ctx)
	if !s.initialized.Load() {
		return nil, &Error{Code: wire.ErrorCodeNotInitialized, Message: "not initialized"}
	}
	if method == wire.MethodPing {
		return struct{}{}, nil
	}
	ns, rest, found := strings.Cut(method, ".")
	if !found {
		return nil, UnknownMethod(method)
	}
	if h := s.prog.Namespaces[ns]; h != nil {
		return h(ctx, rest, params)
	}
	if ns == "ui" && s.prog.UI != nil {
		return s.ui(ctx, rest, params)
	}
	if ns == "shortcut" && s.prog.Shortcuts != nil {
		return s.shortcut(ctx, rest, params)
	}
	if ns == "routine_facts" && s.prog.RoutineFacts != nil {
		return s.collectRoutineFacts(ctx, rest, params)
	}
	return nil, UnknownMethod(method)
}

// event hands an event the hub tells (event.<topic>) to its namespace's handler; nothing is answered.
func (s *server) event(method string, params json.RawMessage) {
	if !s.initialized.Load() {
		return
	}
	ns, rest, found := strings.Cut(method, ".")
	if h := s.prog.Namespaces[ns]; found && h != nil {
		go func() { _, _ = h(s.ctx, rest, params) }()
	}
}

func (s *server) initialize(raw json.RawMessage) (any, error) {
	in := &Init{Raw: raw}
	if err := json.Unmarshal(raw, &in.InitializeParams); err != nil {
		return nil, err
	}
	if in.Protocol != Protocol {
		return nil, &Error{Code: wire.ErrorCodeWrongProtocol, Message: fmt.Sprintf("protocol %d, not %d", in.Protocol, Protocol)}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if !s.initialized.Load() {
		var contributes map[string]any
		if s.prog.Init != nil {
			var err error
			if contributes, err = s.prog.Init(s.ctx, in, s.hub); err != nil {
				return nil, err
			}
		}
		var rawContributes map[string]json.RawMessage
		if len(contributes) > 0 {
			rawContributes = make(map[string]json.RawMessage, len(contributes))
			for k, v := range contributes {
				b, err := json.Marshal(v)
				if err != nil {
					return nil, err
				}
				rawContributes[k] = b
			}
		}
		s.init, s.contributes = in, rawContributes
		s.initialized.Store(true)
	}
	res := wire.InitializeResult{Protocol: Protocol, ID: s.prog.ID, Version: s.prog.Version}
	if len(s.contributes) > 0 {
		res.Contributes = make(map[string]json.RawMessage, len(s.contributes))
		for k, v := range s.contributes {
			res.Contributes[k] = v
		}
	}
	return res, nil
}

// NoContribution is the error for a call to a contribution the program does not have.
func NoContribution(id string) error {
	return &Error{Code: wire.ErrorCodeNoContribution, Message: "no contribution " + id}
}

// UnknownMethod is the error for a method the namespace does not have.
func UnknownMethod(method string) error {
	return &Error{Code: wire.ErrorCodeUnknownMethod, Message: "unknown method " + method}
}

// Unsupported is the error for what the program does not do here (not its gateway, …).
func Unsupported(why string) error { return &Error{Code: wire.ErrorCodeUnsupported, Message: why} }

// Contribution reads the contribution a call is for ("" when the call did not say).
func Contribution(params json.RawMessage) string {
	var t struct {
		Contribution string `json:"contribution"`
	}
	_ = json.Unmarshal(params, &t)
	return t.Contribution
}

// answeredManifest removes source-only tags and icon metadata for strict older hubs.
// A manifest that is not an object goes out as it is.
func answeredManifest(m json.RawMessage) json.RawMessage {
	var fields map[string]json.RawMessage
	if json.Unmarshal(m, &fields) != nil || fields["tags"] == nil && fields["icon"] == nil {
		return m
	}
	delete(fields, "tags")
	delete(fields, "icon")
	out, err := json.Marshal(fields)
	if err != nil {
		return m
	}
	return out
}
