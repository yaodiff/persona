// Package ai calls Messages, Responses or Chat Completions APIs for small structured
// tasks in the app (e.g. turning a pasted blob into a mail account). The key is read from its key file at call time
// and never stored by Persona.
package ai

import (
	"bufio"
	"bytes"
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/aiwire"
	"github.com/nwverse/persona/server/internal/modelhttp"
	"github.com/nwverse/persona/server/internal/prompts"
)

// Defined in the contract package aiwire (docs/server/architecture.md "Contract packages"); the old names stay.
type Config = aiwire.Config

// errNotConfigured is shown in the app as is (e.g. under "AI Draft Reply"), so it says where to fix it there.
var errNotConfigured = errors.New("还没有能直接调用的模型：在 设置 › 模型供应商 添加带 Key 的接入，再到 设置 › 模型配置 为这项选择它" +
	`（或在助手的 manager.json 写 "ai"）`)

// IsNotConfigured tells a request that had no model to go to (nothing set up for it) from one that failed.
func IsNotConfigured(err error) bool { return errors.Is(err, errNotConfigured) }

type Client struct {
	cfg      Config
	http     *http.Client
	resolve  func(context.Context) (*Client, error)
	run      func(context.Context, Request) (Reply, error)
	onResult func(Reply, error)
}

// NewResolved resolves the execution configuration at the start of every request.
func NewResolved(resolve func(context.Context) (*Client, error)) *Client {
	return &Client{resolve: resolve}
}

// Config returns the immutable connection configuration, never an API response.
func (c *Client) Config() Config { return c.cfg }

// WithResult returns a client that reports each completed Chat call.
func (c *Client) WithResult(report func(Reply, error)) *Client {
	copied := *c
	copied.onResult = report
	return &copied
}

func New(cfg Config) *Client {
	return NewWith(cfg, nil)
}

// NewWith is New sending its requests through transport (nil: this machine's network).
func NewWith(cfg Config, transport http.RoundTripper) *Client {
	c := &Client{cfg: cfg}
	c.http = &http.Client{Timeout: 60 * time.Second, Transport: transport, CheckRedirect: c.checkRedirect}
	return c
}

// net/http copies headers from the first request on every redirect, so check the entire chain before retaining
// provider-specific values. Once a chain leaves its original origin, all later hops use Persona's identity and omit
// API credentials, custom provider headers and gateway-scoped session headers.
func checkRedirect(req *http.Request, via []*http.Request) error {
	return checkRedirectHeaders(req, via, nil)
}

func (c *Client) checkRedirect(req *http.Request, via []*http.Request) error {
	return checkRedirectHeaders(req, via, c.cfg.Headers)
}

func checkRedirectHeaders(req *http.Request, via []*http.Request, headers map[string]string) error {
	if len(via) >= 10 {
		return errors.New("stopped after 10 redirects")
	}
	if len(via) == 0 || via[0] == nil || req == nil {
		return nil
	}
	origin := via[0].URL
	clearPrivate := func() {
		for name := range headers {
			req.Header.Del(name)
		}
		req.Header.Del("Authorization")
		req.Header.Del("Proxy-Authorization")
		req.Header.Del("X-Api-Key")
		req.Header.Del("Cookie")
		req.Header.Del("Cookie2")
		req.Header.Set("User-Agent", modelhttp.UserAgent())
		modelhttp.ClearOpenCodeSession(req.Header)
		modelhttp.ClearSource(req.Header)
	}
	if !sameOrigin(origin, req.URL) {
		clearPrivate()
		return nil
	}
	for _, previous := range via[1:] {
		if previous == nil || !sameOrigin(origin, previous.URL) {
			clearPrivate()
			break
		}
	}
	return nil
}

func sameOrigin(a, b *url.URL) bool {
	if a == nil || b == nil {
		return false
	}
	port := func(u *url.URL) string {
		if p := u.Port(); p != "" {
			return p
		}
		switch strings.ToLower(u.Scheme) {
		case "http":
			return "80"
		case "https":
			return "443"
		}
		return ""
	}
	return strings.EqualFold(a.Scheme, b.Scheme) && strings.EqualFold(a.Hostname(), b.Hostname()) && port(a) == port(b)
}

// NewRun sends requests through a launcher callback. Each configured launcher shares a gate
// with other clients running the same model on the same computer, so consecutive full-budget
// timeouts back off and cap concurrency instead of piling onto a wedged launcher.
func NewRun(cfg Config, run func(context.Context, Request) (Reply, error)) *Client {
	if cfg.RunLauncher != "" && run != nil {
		g := gateForRun(cfg)
		return &Client{cfg: cfg, run: func(ctx context.Context, r Request) (Reply, error) {
			return g.run(ctx, r, run)
		}}
	}
	return &Client{cfg: cfg, run: run}
}

func (c *Client) key() (string, error) {
	if c.cfg.APIKey != "" {
		return c.cfg.APIKey, nil
	}
	path := c.cfg.APIKeyFile
	if strings.HasPrefix(path, "~/") {
		home, _ := os.UserHomeDir()
		path = filepath.Join(home, path[2:])
	}
	b, err := os.ReadFile(path)
	if err != nil {
		return "", fmt.Errorf("ai: read api key: %w", err)
	}
	return strings.TrimSpace(string(b)), nil
}

// Complete sends one user message with a system prompt and returns the text reply. A reply the upstream cut off at
// maxTokens comes back as far as it got.
func (c *Client) Complete(ctx context.Context, system, user string, maxTokens int) (string, error) {
	text, _, err := c.complete(ctx, system, user, maxTokens)
	return text, err
}

// complete is Complete that also says whether the reply stopped at maxTokens (the Messages API's stop_reason).
func (c *Client) complete(ctx context.Context, system, user string, maxTokens int) (text string, truncated bool, err error) {
	r, err := c.Chat(ctx, Request{System: system, Messages: []Message{{Role: RoleUser, Content: user}}, MaxTokens: maxTokens})
	return r.Text, r.Truncated, err
}

// The roles of a conversation's turns.
const (
	RoleUser      = "user"
	RoleAssistant = "assistant"
)

// Message is one turn of a conversation: the user's, or the model's own earlier answer.
type Message struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// Request is a conversation to continue: a system prompt and turns that start and end with the user's.
type Request struct {
	System    string
	Messages  []Message
	MaxTokens int // output limit for Messages and Chat Completions
	// SessionID optionally scopes session-routed gateways (such as OpenCode Go) across turns; empty falls back to ctx
	// (WithSessionID) or the client's stable session identifier.
	SessionID string
	// BoundOutput also enforces MaxTokens on Responses. Cost-sensitive background calls
	// fail closed when a relay refuses the bound; legacy callers keep their existing behavior.
	BoundOutput bool
	// DisableThinking asks the API to turn extended thinking/reasoning off for this call, through each protocol's
	// standard "off" encoding (Messages thinking.type=disabled with the model-specific fallback, Chat Completions
	// reasoning_effort="none", Responses reasoning.effort="none"). Short deterministic tasks such as title naming
	// set it: a thinking model spends the small output room on reasoning and a relay that surfaces that reasoning
	// as text can leak cut-off monologue fragments into the answer. It wins over Config.ReasoningEffort for this
	// request only; protocols and models that cannot express "off" keep ApplyReasoningEffort's compatibility rules.
	DisableThinking bool
}

// effortForRequest is the reasoning effort to encode for one call. Request.DisableThinking forces the generic
// "off" level, which ApplyReasoningEffort maps to each protocol's own encoding; otherwise the connection's
// configured effort is used ("" leaves the model/provider default).
func effortForRequest(configured string, r Request) string {
	if r.DisableThinking {
		return "off"
	}
	return configured
}

type Usage = aiwire.Usage

// Reply is the model's answer: its text, whether it stopped at MaxTokens, the model the upstream says answered
// (the one asked for when it does not say) and what it spent.
type Reply struct {
	Text      string
	Truncated bool
	Model     string
	Usage     Usage
}

// Chat sends a conversation and returns the reply. A context with a deadline bounds the request by it instead of the
// client's 60 seconds (a long answer).
func (c *Client) Chat(ctx context.Context, r Request) (Reply, error) {
	reply, err := c.chat(ctx, r)
	if c.onResult != nil {
		c.onResult(reply, err)
	}
	return reply, err
}

func (c *Client) chat(ctx context.Context, r Request) (Reply, error) {
	if c.resolve != nil {
		resolved, err := c.resolve(ctx)
		if err != nil {
			return Reply{}, err
		}
		return resolved.Chat(ctx, r)
	}
	if c.run != nil {
		reply, err := c.run(ctx, r)
		reply.Model = cmp.Or(reply.Model, c.cfg.Model)
		return reply, err
	}
	if !c.cfg.Configured() {
		return Reply{}, errNotConfigured
	}
	key, err := c.key()
	if err != nil {
		return Reply{}, err
	}
	// Every client of one address and key goes through the same gate, which holds requests back while the upstream
	// is out of quota or busy (limit.go).
	g := gateForAPI(c.cfg.API, c.cfg.BaseURL, key)
	t, err := g.admit(ctx)
	if err != nil {
		return Reply{}, err
	}
	hc := c.http
	if _, ok := ctx.Deadline(); ok && hc != nil && hc.Timeout != 0 {
		copied := *hc
		copied.Timeout = 0
		hc = &copied
	}
	var found *probeAnswer // this request finds out whether a pause is over: the gate goes at its 200 (limit.go)
	if t.probe && hc != nil {
		found = &probeAnswer{next: hc.Transport, g: g}
		copied := *hc
		copied.Transport = found
		hc = &copied
	}
	finding := func() bool { return t.probe && !(found != nil && found.let.Load()) } // still the one finding out
	// The turn goes back however the exchange ends, a panic included. One that unwound through here used to skip
	// done, and where its caller recovers and carries on (the knowledge base loop's kbRun, its job pool) the gate
	// kept counting the request as out for good: under a cap its slot was never free again, and as the request
	// finding out nobody found out after it. Every later request on the connection was then put off as busy with
	// nothing sent, until the process restarted (#6698). It is told as a failure that says nothing of the upstream.
	returned := false
	defer func() {
		if !returned {
			_ = g.done(t, finding(), false, errUnwound)
		}
	}()
	var reply Reply
	var answered bool
	switch c.cfg.API {
	case "responses":
		reply, answered, err = c.respond(ctx, hc, key, r)
	case "chat":
		reply, answered, err = c.chatCompletions(ctx, hc, key, r)
	default:
		reply, answered, err = c.messages(ctx, hc, key, r)
	}
	err = c.redactError(err, key)
	returned = true
	if reply.Model == "" {
		reply.Model = c.cfg.Model
	}
	return reply, g.done(t, finding(), answered, err)
}

// errUnwound is what the gate is told of a request a panic unwound through: it ended, with no answer to read.
var errUnwound = errors.New("ai: the request did not return")

// trimSingleEndpointPath drops one pasted endpoint suffix (e.g. /chat/completions) from the URL path of base,
// leaving scheme, host, query and fragment intact.
func trimSingleEndpointPath(base, suffix string) string {
	base = strings.TrimRight(strings.TrimSpace(base), "/")
	u, err := url.Parse(base)
	if err != nil || u.Host == "" || u.Opaque != "" || u.RawQuery != "" || u.Fragment != "" {
		return base
	}
	authority := strings.Index(base, "//") + len("//")
	slash := strings.IndexByte(base[authority:], '/')
	if slash < 0 {
		return base
	}
	root, path := base[:authority+slash], base[authority+slash:]
	if rest, ok := strings.CutSuffix(path, suffix); ok {
		return root + strings.TrimRight(rest, "/")
	}
	return root + path
}

// trimVersionedEndpointPath drops a single pasted /v1<suffix> endpoint (e.g. /v1/messages or /v1/responses) while
// keeping the preceding /v1 and leaving bare deployment prefixes ("https://gw/messages", "https://gw/responses") intact.
func trimVersionedEndpointPath(base, suffix string) string {
	base = strings.TrimRight(strings.TrimSpace(base), "/")
	u, err := url.Parse(base)
	if err != nil || u.Host == "" || u.Opaque != "" || u.RawQuery != "" || u.Fragment != "" {
		return base
	}
	authority := strings.Index(base, "//") + len("//")
	slash := strings.IndexByte(base[authority:], '/')
	if slash < 0 {
		return base
	}
	root, path := base[:authority+slash], base[authority+slash:]
	rest, ok := strings.CutSuffix(path, suffix)
	if !ok {
		return root + path
	}
	rest = strings.TrimRight(rest, "/")
	if !strings.HasSuffix(rest, "/v1") {
		return root + path
	}
	return root + rest
}

// apiEndpoint preserves deployment prefixes, strips a pasted endpoint suffix, and accepts bases with or without /v1.
func apiEndpoint(base, path string) string {
	switch path {
	case "messages", "responses":
		base = trimVersionedEndpointPath(base, "/"+path)
	default:
		base = trimSingleEndpointPath(base, "/"+path)
	}
	if !strings.HasSuffix(base, "/v1") {
		base += "/v1"
	}
	return base + "/" + path
}

// setCustomHeaders applies provider fields before the protocol's own credentials and metadata, which always win.
func (c *Client) setCustomHeaders(req *http.Request) {
	for name, value := range c.cfg.Headers {
		req.Header.Set(name, value)
	}
	modelhttp.Source{Role: "background"}.ApplyForURL(req.Header, req.URL.String())
}

// setUserAgent gives an explicit connection value precedence over Persona's default.
func (c *Client) setUserAgent(req *http.Request) {
	if c.cfg.UserAgent != nil {
		req.Header.Set("User-Agent", *c.cfg.UserAgent)
	}
	modelhttp.SetDefaultUserAgent(req.Header)
}

// messages calls the Messages API; answered is whether the upstream said 200, Truncated whether the reply stopped at
// MaxTokens. A model that thinks spends its thinking from the same MaxTokens, so a long answer can stop half written.
// It streams (#4245): a gateway's cap on a non-streamed request's total time (often 120 s) otherwise fails every
// answer a slow model needs longer for. A server that ignores "stream" and answers one JSON message is read as before.
func (c *Client) messages(ctx context.Context, hc *http.Client, key string, r Request) (reply Reply, answered bool, err error) {
	turns := make([]any, 0, len(r.Messages))
	for _, m := range r.Messages {
		turns = append(turns, map[string]any{"role": m.Role, "content": m.Content})
	}
	bodyMap := map[string]any{"model": c.cfg.Model, "max_tokens": r.MaxTokens, "system": r.System, "messages": turns, "stream": true}
	ApplyReasoningEffort(bodyMap, "messages", c.cfg.Model, effortForRequest(c.cfg.ReasoningEffort, r))
	body, _ := json.Marshal(bodyMap)
	req, err := http.NewRequestWithContext(ctx, "POST", apiEndpoint(c.cfg.BaseURL, "messages"), bytes.NewReader(body))
	if err != nil {
		return reply, false, err
	}
	c.setCustomHeaders(req)
	req.Header.Set("content-type", "application/json")
	req.Header.Set("accept", "text/event-stream")
	if c.cfg.Bearer {
		req.Header.Set("authorization", "Bearer "+key)
	} else {
		req.Header.Set("x-api-key", key)
	}
	req.Header.Set("anthropic-version", "2023-06-01")
	c.setGatewayHeaders(ctx, req, r)
	resp, err := hc.Do(req)
	if err != nil {
		return reply, false, fmt.Errorf("ai: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
		return reply, false, c.newHTTPError(resp, raw, key)
	}
	in := bufio.NewReader(resp.Body)
	if wholeMessage(in) {
		return readMessage(in)
	}
	return readMessageStream(in)
}

// wholeMessage says the answer is one JSON message rather than an event stream: its first byte past any white space
// opens an object (an event stream starts with a field or a comment). A body that can't be read is left to the stream
// reader, which reports it.
func wholeMessage(in *bufio.Reader) bool {
	for {
		b, err := in.Peek(1)
		if err != nil {
			return false
		}
		switch b[0] {
		case ' ', '\t', '\r', '\n':
			_, _ = in.ReadByte()
		default:
			return b[0] == '{'
		}
	}
}

// messageUsage is a Messages API usage object, whose fields come in parts across a stream.
type messageUsage struct {
	InputTokens              *int `json:"input_tokens"`
	OutputTokens             *int `json:"output_tokens"`
	CacheReadInputTokens     *int `json:"cache_read_input_tokens"`
	CacheCreationInputTokens *int `json:"cache_creation_input_tokens"`
}

// into sets the fields u has on to: message_delta's usage carries the totals so far, of only some fields. A zero
// is not taken: some relays send input_tokens 0 in message_delta, which would wipe message_start's real count.
func (u *messageUsage) into(to *Usage) {
	if u == nil {
		return
	}
	for _, f := range []struct {
		from *int
		to   *int
	}{{u.InputTokens, &to.InputTokens}, {u.OutputTokens, &to.OutputTokens},
		{u.CacheReadInputTokens, &to.CacheReadInputTokens}, {u.CacheCreationInputTokens, &to.CacheCreationInputTokens}} {
		if f.from != nil && *f.from != 0 {
			*f.to = *f.from
		}
	}
}

// responsesUsage is a Responses API response.usage object. input_tokens counts the cached tokens too, so they are
// subtracted to match Usage.InputTokens.
type responsesUsage struct {
	InputTokens        int `json:"input_tokens"`
	OutputTokens       int `json:"output_tokens"`
	InputTokensDetails struct {
		CachedTokens int `json:"cached_tokens"`
	} `json:"input_tokens_details"`
}

func (u *responsesUsage) usage() Usage {
	if u == nil {
		return Usage{}
	}
	return Usage{InputTokens: u.InputTokens - u.InputTokensDetails.CachedTokens, OutputTokens: u.OutputTokens,
		CacheReadInputTokens: u.InputTokensDetails.CachedTokens}
}

// errorOf reads a Messages error: an object with its type, code and message, or only a message string. kind retains
// the established display/type behavior; code stays separate so new code semantics never mistake type for it.
func errorOf(raw json.RawMessage) (kind, code, msg string, ok bool) {
	if len(raw) == 0 || string(raw) == "null" {
		return "", "", "", false
	}
	if json.Unmarshal(raw, &msg) == nil {
		return "error", "", msg, true
	}
	code, msg, kind, ok = gatewayErrorDetailsWithType(raw)
	if !ok {
		return "", "", "", false
	}
	return cmp.Or(kind, code, "error"), code, msg, true
}

// messageFailure is the error a Messages error reported after the 200 stands for, and whether it counts as an
// answer: a limit (overloaded_error, rate_limit_error, or an explicit limit code) holds the gate and is no answer;
// any other is a streamFailure, Transient as its type's HTTP status would be (api_error 500, timeout_error 504).
func messageFailure(kind, code, msg string) (bool, error) {
	kind = cmp.Or(kind, code, "error")
	if limit := streamLimitFor("error", code, msg); limit != nil {
		return false, limit
	}
	status := messageErrorStatus[strings.ToLower(kind)]
	failureStatus := status
	if failureStatus == 0 { // a generic stream error has no HTTP type; wording fallback uses bad-request semantics
		failureStatus = http.StatusBadRequest
	}
	// An unknown code retains the established type-based limit behavior. A recognized non-limit code (a missing model,
	// excess context or an unsupported endpoint) is not replaced by a contradictory type.
	_, recognizedNonLimit := failureKindForCode(code)
	if !recognizedNonLimit {
		if limit := streamLimitFor("error", kind, msg); limit != nil {
			return false, limit
		}
	}
	failure := &streamFailure{status: status, kind: kind, code: code, msg: msg}
	return true, withFailureKind(failure, failureKindFor(failureStatus, code, msg, kind))
}

// streamCut is the error of a stream that ended before its end event: the connection was cut, and whatever text
// came is only part of the answer (io.ErrUnexpectedEOF, so Transient), not a short reply. what names the end missed.
func streamCut(gotText bool, what string) error {
	if !gotText {
		return fmt.Errorf("ai: stream ended without a reply: %w", io.ErrUnexpectedEOF)
	}
	return fmt.Errorf("ai: stream ended before the %s completed: %w", what, io.ErrUnexpectedEOF)
}

// readMessage reads a whole Messages API answer (a server that didn't stream).
func readMessage(body io.Reader) (Reply, bool, error) {
	raw, err := io.ReadAll(io.LimitReader(body, 4<<20))
	if err != nil {
		return Reply{}, true, fmt.Errorf("ai: read response: %w", err)
	}
	var out struct {
		Type    json.RawMessage `json:"type"`
		Code    json.RawMessage `json:"code"`
		Message json.RawMessage `json:"message"`
		Model   string          `json:"model"`
		Content []struct {
			Type string `json:"type"`
			Text string `json:"text"`
		} `json:"content"`
		StopReason string          `json:"stop_reason"`
		Usage      Usage           `json:"usage"`
		Error      json.RawMessage `json:"error"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return Reply{}, true, fmt.Errorf("ai: decode: %w", err)
	}
	// An error body sent with 200 (a gateway that ignored "stream" and failed) is the failure it reports.
	typ := decodeErrorString(out.Type)
	if kind, code, msg, ok := errorOf(out.Error); typ == "error" || (typ == "" && ok && len(out.Content) == 0) {
		answered, err := messageFailure(kind, cmp.Or(code, decodeErrorCode(out.Code)), cmp.Or(msg, decodeErrorString(out.Message)))
		return Reply{}, answered, err
	}
	var b strings.Builder
	for _, part := range out.Content {
		if part.Type == "text" {
			b.WriteString(part.Text)
		}
	}
	return messageReply(b.String(), out.StopReason, out.Model, out.Usage)
}

// messageReply is the reply of a finished message, or the refusal it was.
func messageReply(text, stopReason, model string, usage Usage) (Reply, bool, error) {
	if stopReason == "refusal" { // declined, usually with no text at all: not a reply that lacks its JSON
		return Reply{Model: model, Usage: usage}, true, &RefusalError{StopReason: stopReason}
	}
	return Reply{Text: text, Truncated: stopReason == "max_tokens", Model: model, Usage: usage}, true, nil
}

// readMessageStream reads a Messages API event stream, framed by SSEEvents: the model and usage from message_start,
// the text of the text blocks (content_block_start, text_delta; thinking and tool input are skipped), the stop reason
// and the usage totals from message_delta. message_stop ends it. A stream that ends with neither message_stop nor a
// stop reason was cut (streamCut). An error event, read leniently (errorOf: an object or a string, with or without
// the event's own type), is the failure it reports (messageFailure). Data that isn't JSON is skipped.
func readMessageStream(body io.Reader) (Reply, bool, error) {
	var (
		b              strings.Builder
		model          string
		stop           string
		usage          Usage
		failed         error
		failedAnswered bool
		stopped        bool
	)
	err := SSEEvents(body, func(data string) bool {
		var ev struct {
			Type json.RawMessage `json:"type"`
			Code json.RawMessage `json:"code"`
			// Message is message_start's message, or an error's text where a relay puts it at the top.
			Message      json.RawMessage `json:"message"`
			ContentBlock struct {
				Type string `json:"type"`
				Text string `json:"text"`
			} `json:"content_block"`
			Delta struct {
				Type       string `json:"type"`
				Text       string `json:"text"`
				StopReason string `json:"stop_reason"`
			} `json:"delta"`
			Usage *messageUsage   `json:"usage"`
			Error json.RawMessage `json:"error"`
		}
		if json.Unmarshal([]byte(data), &ev) != nil {
			return false
		}
		kind, code, msg, isError := errorOf(ev.Error)
		code = cmp.Or(code, decodeErrorCode(ev.Code))
		eventType := decodeErrorString(ev.Type)
		if eventType == "" && isError { // an error event without its type: the error is what it carries
			eventType = "error"
		}
		switch eventType {
		case "message_start":
			var m struct {
				Model string        `json:"model"`
				Usage *messageUsage `json:"usage"`
			}
			if json.Unmarshal(ev.Message, &m) == nil {
				model = m.Model
				m.Usage.into(&usage)
			}
		case "content_block_start":
			if ev.ContentBlock.Type == "text" {
				b.WriteString(ev.ContentBlock.Text)
			}
		case "content_block_delta":
			if ev.Delta.Type == "text_delta" {
				b.WriteString(ev.Delta.Text)
			}
		case "message_delta":
			stop = cmp.Or(ev.Delta.StopReason, stop)
			ev.Usage.into(&usage)
		case "message_stop":
			stopped = true
			return true
		case "error":
			if msg == "" {
				_ = json.Unmarshal(ev.Message, &msg) // a relay's {"type":"error","message":"..."}
			}
			failedAnswered, failed = messageFailure(kind, code, msg)
			return true
		}
		return false
	})
	if err != nil {
		// The stream was cut while reading (deadline, reset, EOF): keep the usage message_start/message_delta already
		// reported, so a long reply cut off here is not metered as zero tokens. No answer text is returned with the error.
		return Reply{Model: model, Usage: usage}, true, fmt.Errorf("ai: read stream: %w", err)
	}
	if failed != nil {
		return Reply{}, failedAnswered, failed
	}
	if !stopped && stop == "" {
		return Reply{}, true, streamCut(b.Len() > 0, "message")
	}
	return messageReply(b.String(), stop, model, usage)
}

// messagesThinkingDisabledUnsupported reports models whose Messages API rejects thinking.disabled. Match the ID
// anywhere so provider-prefixed and dated variants follow the same rule; Fable/Mythos 5.x always require thinking.
func messagesThinkingDisabledUnsupported(model string) bool {
	model = strings.ToLower(model)
	for _, id := range []string{"opus-5-5", "sonnet-5-5", "fable-5", "mythos-5", "mythos-preview"} {
		if strings.Contains(model, id) {
			return true
		}
	}
	return false
}

// DisabledThinking is the Messages API's thinking object that turns thinking off. It is built here, with the rest of
// the providers' request formats, so callers that force it (the model probe) send the same bytes.
func DisabledThinking() map[string]any { return map[string]any{"type": "disabled"} }

// ApplyReasoningEffort keeps connection probes and runtime calls on the same wire format.
// False means this protocol cannot express the requested effort; Chat and Responses validate model-specific levels upstream.
func ApplyReasoningEffort(body map[string]any, api, model, effort string) bool {
	if effort == "" {
		return true
	}
	normalized := strings.ToLower(strings.TrimSpace(effort))
	switch api {
	case "chat":
		if normalized == "off" || normalized == "none" || normalized == "disabled" {
			effort = "none"
		}
		body["reasoning_effort"] = effort
	case "responses":
		if normalized == "off" || normalized == "none" || normalized == "disabled" {
			effort = "none"
		}
		body["reasoning"] = map[string]any{"effort": effort}
	default:
		switch normalized {
		case "off", "none", "disabled":
			switch {
			case strings.Contains(strings.ToLower(model), "sonnet-5-5"):
				body["thinking"] = map[string]any{"type": "between_tools"}
				body["output_config"] = map[string]any{"effort": "low"}
			case messagesThinkingDisabledUnsupported(model):
				body["output_config"] = map[string]any{"effort": "low"}
			default:
				body["thinking"] = DisabledThinking()
			}
		case "low", "medium", "high", "xhigh", "max":
			body["output_config"] = map[string]any{"effort": effort}
		default:
			return false
		}
	}
	return true
}

// respond calls the Responses API. It always streams: some compatible endpoints (Codex-backed relays) reject
// stream=false, and the text is just the concatenated output_text deltas. The system prompt goes in as a developer
// message rather than `instructions`, which those relays replace with their own. Events are framed as SSE
// (SSEEvents), and a stream that ends before its terminal event (completed, incomplete or failed) was cut: an error
// wrapping io.ErrUnexpectedEOF, not a short reply.
func (c *Client) respond(ctx context.Context, hc *http.Client, key string, r Request) (reply Reply, answered bool, err error) {
	input := []any{map[string]any{"role": "developer", "content": r.System}}
	for _, m := range r.Messages {
		input = append(input, map[string]any{"role": m.Role, "content": m.Content})
	}
	req := map[string]any{"model": c.cfg.Model, "store": false, "stream": true, "input": input}
	if r.BoundOutput && r.MaxTokens > 0 {
		req["max_output_tokens"] = r.MaxTokens
	}
	ApplyReasoningEffort(req, "responses", c.cfg.Model, effortForRequest(c.cfg.ReasoningEffort, r))
	body, _ := json.Marshal(req)
	hreq, err := http.NewRequestWithContext(ctx, "POST", apiEndpoint(c.cfg.BaseURL, "responses"), bytes.NewReader(body))
	if err != nil {
		return reply, false, err
	}
	c.setCustomHeaders(hreq)
	hreq.Header.Set("content-type", "application/json")
	hreq.Header.Set("accept", "text/event-stream")
	hreq.Header.Set("authorization", "Bearer "+key)
	c.setGatewayHeaders(ctx, hreq, r)
	resp, err := hc.Do(hreq)
	if err != nil {
		return reply, false, fmt.Errorf("ai: %w", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		raw, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<10))
		return reply, false, c.newHTTPError(resp, raw, key)
	}
	var b strings.Builder
	refused := false // a refusal content part came: the model declined, whatever else the stream says
	// partial keeps the model/usage any event has already reported, for the read-error return below.
	var partial Reply
	// event acts on one event's data, and done says it ended the stream, with the reply and error to return. ended
	// returns them: a limit the stream reported is no answer, so it holds the gate like the HTTP 429 it stands for.
	ended := func(got Reply, err error) (Reply, bool, error) {
		var limit *streamLimit
		return got, !errors.As(err, &limit), err
	}
	event := func(data string) (reply Reply, done bool, err error) {
		var ev struct {
			Type    json.RawMessage `json:"type"`
			Delta   string          `json:"delta"`
			Message json.RawMessage `json:"message"`
			Code    json.RawMessage `json:"code"` // an error event's
			Error   json.RawMessage `json:"error"`
			// Part is the content part of response.content_part.added/done: type output_text or refusal.
			Part struct {
				Type string `json:"type"`
			} `json:"part"`
			Response struct {
				Model             string          `json:"model"`
				Error             json.RawMessage `json:"error"`
				IncompleteDetails *struct {
					Reason string `json:"reason"`
				} `json:"incomplete_details"`
				Usage *responsesUsage `json:"usage"`
			} `json:"response"`
		}
		if json.Unmarshal([]byte(data), &ev) != nil {
			return reply, false, nil
		}
		// Any event's response object can name the model and report usage (possibly partial, before the terminal
		// response.completed). Keep the latest so a stream cut by a read error still returns what was metered.
		if ev.Response.Model != "" {
			partial.Model = ev.Response.Model
		}
		if ev.Response.Usage != nil {
			partial.Usage = ev.Response.Usage.usage()
		}
		finished := func() Reply {
			return Reply{Text: b.String(), Model: ev.Response.Model, Usage: ev.Response.Usage.usage()}
		}
		failureDetails := func() (code, msg string) {
			code, msg = decodeErrorCode(ev.Code), decodeErrorString(ev.Message)
			if _, nestedCode, nestedMsg, ok := errorOf(ev.Error); ok {
				code, msg = cmp.Or(nestedCode, code), cmp.Or(nestedMsg, msg)
			}
			if _, responseCode, responseMsg, ok := errorOf(ev.Response.Error); ok { // response.error is most specific
				code, msg = cmp.Or(responseCode, code), cmp.Or(responseMsg, msg)
			}
			return code, msg
		}
		eventType := decodeErrorString(ev.Type)
		switch eventType {
		case "response.output_text.delta":
			b.WriteString(ev.Delta)
		case "response.refusal.delta", "response.refusal.done":
			refused = true
		case "response.content_part.added", "response.content_part.done":
			if ev.Part.Type == "refusal" {
				refused = true
			}
		case "response.completed":
			reply = finished()
			if refused { // a refusal part, whatever text came with it: the same as the Messages API's stop_reason
				return Reply{Model: reply.Model, Usage: reply.Usage}, true, &RefusalError{StopReason: "refusal"}
			}
			return reply, true, nil
		case "response.incomplete":
			reason := ""
			if d := ev.Response.IncompleteDetails; d != nil {
				reason = d.Reason
			}
			if reason == "content_filter" || refused { // stopped by a filter: declined, not a failure to retry
				if reason != "content_filter" {
					reason = "refusal"
				}
				reply = finished()
				return Reply{Model: reply.Model, Usage: reply.Usage}, true, &RefusalError{StopReason: reason}
			}
			code, msg := failureDetails()
			return reply, true, streamFailureError(eventType, code, msg)
		case "response.failed", "error":
			code, msg := failureDetails()
			return reply, true, streamFailureError(eventType, code, msg)
		}
		return reply, false, nil
	}
	var (
		got     Reply
		gotErr  error
		stopped bool
	)
	if err := SSEEvents(resp.Body, func(data string) bool {
		got, stopped, gotErr = event(data)
		return stopped
	}); err != nil {
		return partial, true, fmt.Errorf("ai: read stream: %w", err)
	}
	if stopped {
		return ended(got, gotErr)
	}
	return reply, true, streamCut(b.Len() > 0, "response") // no terminal event
}

// JSON asks for a JSON object and decodes the first one in the reply into out.
func (c *Client) JSON(ctx context.Context, system, user string, out any) error {
	return c.JSONMax(ctx, system, user, 1024, out)
}

// JSONMax is JSON with room for a longer answer (summaries). A reply cut off at maxTokens before its object closed is
// asked for once more with twice the room; cut off again, the error says so (ErrTruncated) instead of blaming the JSON.
func (c *Client) JSONMax(ctx context.Context, system, user string, maxTokens int, out any) error {
	system = prompts.RenderJSONOnly(system)
	for attempt := 0; ; attempt++ {
		text, truncated, err := c.complete(ctx, system, user, maxTokens)
		if err != nil {
			return err
		}
		obj, err := FirstObject(text)
		if err == nil {
			return unmarshalReply([]byte(obj), out)
		}
		if !truncated {
			return err
		}
		if attempt > 0 {
			return fmt.Errorf("%w (max_tokens %d)", ErrTruncated, maxTokens)
		}
		maxTokens *= 2
	}
}

// ErrNoObject: the reply holds no JSON object at all.
var ErrNoObject = aiwire.ErrNoObject

// ErrUnterminated: the reply's JSON object never closes (and the reply was not cut off at max_tokens).
var ErrUnterminated = aiwire.ErrUnterminated

// ErrTruncated: the reply stopped at max_tokens before the answer was complete, even with the room doubled.
var ErrTruncated = aiwire.ErrTruncated

// ErrRefused: the model declined the request (the Messages API's stop_reason "refusal", e.g. a safety classifier).
// The same input is declined again, so sending it as is once more only spends a call.
var ErrRefused = aiwire.ErrRefused

// RefusalError is ErrRefused with what the upstream said: StopReason is "refusal" (the Messages API's stop_reason, a
// Responses API refusal part or a Chat Completions refusal delta) or "content_filter" (an incomplete response's reason
// or a Chat Completions finish_reason). errors.Is(err, ErrRefused) holds for it; the Reply returned with it still has
// the call's Model and Usage.
type RefusalError struct {
	StopReason string
}

func (e *RefusalError) Error() string { return ErrRefused.Error() }
func (e *RefusalError) Unwrap() error { return ErrRefused }

// FirstObject extracts the first balanced {...} from model output (tolerating fences and chatter).
func FirstObject(s string) (string, error) {
	start := strings.IndexByte(s, '{')
	if start < 0 {
		return "", ErrNoObject
	}
	depth, inStr, esc := 0, false, false
	for i := start; i < len(s); i++ {
		ch := s[i]
		switch {
		case esc:
			esc = false
		case ch == '\\' && inStr:
			esc = true
		case ch == '"':
			inStr = !inStr
		case inStr:
		case ch == '{':
			depth++
		case ch == '}':
			depth--
			if depth == 0 {
				return s[start : i+1], nil
			}
		}
	}
	return "", ErrUnterminated
}

// unmarshalReply is json.Unmarshal for a model's object, whose strings may hold what JSON does not allow there: an
// escape JSON lacks (#2151: \' as JavaScript and Python write a quote, a lone backslash as in a Windows path), or a
// control character written as it is (#4872: the line breaks of a summary in paragraphs, a tab). Those are repaired
// only when the object does not parse as it is, so valid JSON is never touched; when the repair does not help, the
// first error stands, as it is the one that says what the model wrote.
func unmarshalReply(data []byte, out any) error {
	err := json.Unmarshal(data, out)
	var syntax *json.SyntaxError
	if err == nil || !errors.As(err, &syntax) {
		return err
	}
	if fixed, changed := fixEscapes(data); changed && json.Unmarshal(fixed, out) == nil {
		return nil
	}
	return err
}

// fixEscapes rewrites, inside strings, what JSON does not allow there: \' becomes a quote, any other unknown \x a
// literal backslash followed by x, and a control character (U+0000–U+001F) written as it is its escape. The escapes
// JSON has are kept as they are, and so is everything outside strings (line breaks and tabs are whitespace there).
func fixEscapes(data []byte) ([]byte, bool) {
	var b bytes.Buffer
	b.Grow(len(data) + 8)
	changed, inStr := false, false
	for i := 0; i < len(data); i++ {
		ch := data[i]
		switch {
		case !inStr:
			inStr = ch == '"'
		case ch == '"':
			inStr = false
		case ch == '\\' && i+1 < len(data):
			switch next := data[i+1]; next {
			case '"', '\\', '/', 'b', 'f', 'n', 'r', 't', 'u':
				b.WriteByte(ch)
				b.WriteByte(next)
				i++
			case '\'':
				b.WriteByte(next)
				i++
				changed = true
			default:
				b.WriteString(`\\`)
				changed = true
			}
			continue
		case ch < 0x20:
			b.WriteString(controlEscape(ch))
			changed = true
			continue
		}
		b.WriteByte(ch)
	}
	return b.Bytes(), changed
}

// controlEscape is JSON's escape for a control character: the short one where JSON has it, \u00XX for the rest.
func controlEscape(ch byte) string {
	switch ch {
	case '\n':
		return `\n`
	case '\r':
		return `\r`
	case '\t':
		return `\t`
	case '\b':
		return `\b`
	case '\f':
		return `\f`
	}
	return fmt.Sprintf(`\u%04x`, ch)
}
