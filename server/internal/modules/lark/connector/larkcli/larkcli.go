// Package larkcli exposes the read-only Lark tools backed by the user's
// lark-cli login (docs/plans/plan-lark-cli.md, L2).
package larkcli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"github.com/nwverse/persona/server/internal/connectorwire"

	"github.com/nwverse/persona/server/internal/cliwire"
	"github.com/nwverse/persona/server/internal/i18n"
	adapter "github.com/nwverse/persona/server/internal/modules/lark/larkcli"
	"github.com/nwverse/persona/server/internal/prompts"
	"github.com/nwverse/persona/server/internal/proto"
)

const (
	ID   = cliwire.ConnectorID
	Name = "Lark via lark-cli"
)

// Connector is enabled only when manager.json configures lark.cli. It owns no
// credentials; the adapter delegates all authentication to lark-cli.
type Connector struct {
	runner  *adapter.Runner
	docs    *documentInbox
	now     func() time.Time
	profile string

	inboxMu            sync.Mutex
	inboxThings        map[string]connectorwire.InboxThing
	inboxSeen          map[string]struct{}
	inboxTopics        map[string]struct{}
	inboxAllChats      map[string]struct{}
	ownerPromiseGroups map[string]struct{}
	ownerPromiseModel  bool
	// Private-chat visibility controls are opt-in and apply to collection, model tools and retained source things.
	inboxExcludeChats       map[string]struct{}
	inboxExcludeDirectChats bool
	// The manager's central boundary is kept separate from the connector's own options so an options.set cannot clear it.
	managedInboxExcludeChats       map[string]struct{}
	managedInboxExcludeDirectChats bool
	managedInboxVisibilityInvalid  bool
	// triageRules are the user's triage rules for lark-cli's Inbox items, in their words (options.triage_rules): the
	// Inbox's triage reads them instead of the global inbox.rules.
	triageRules       string
	inboxState        persistedInboxState
	inboxStateLoaded  bool
	inboxCollecting   bool
	inboxStore        StateStore
	inboxPrefer       string
	gatewayAvailable  func(context.Context) bool
	inboxPoll         time.Duration
	inboxPageLimit    int
	inboxDirectOffset int
	inboxBotAppID     string
	inboxBotOpenID    string

	// chatRecords are the chats' own records a conversation page read (threads.go conversationChat), by chat id.
	recordsMu   sync.Mutex
	chatRecords map[string]chatRecord

	mu      sync.Mutex
	pending *adapter.LoginProcess
}

// New creates the built-in connector from the configured CLI runner.
func New(cfg adapter.Config) *Connector {
	c := &Connector{runner: adapter.New(cfg), docs: newDocumentInbox(), profile: cfg.Profile, inboxPoll: cfg.InboxPoll, inboxPageLimit: cfg.InboxPageLimit}
	if c.inboxPoll <= 0 {
		c.inboxPoll = inboxPollDefault
	}
	if c.inboxPoll < inboxPollMinimum {
		c.inboxPoll = inboxPollMinimum
	}
	if c.inboxPageLimit <= 0 {
		c.inboxPageLimit = inboxPageLimit
	}
	if c.inboxPageLimit > inboxPageLimitMax {
		c.inboxPageLimit = inboxPageLimitMax
	}
	return c
}

// NewWithRunner is useful for unit tests and embedders that already constructed a
// runner. The runner still executes the real configured subprocess.
func NewWithRunner(r *adapter.Runner) *Connector {
	return &Connector{runner: r, docs: newDocumentInbox(), profile: r.Profile(), inboxPoll: inboxPollDefault, inboxPageLimit: inboxPageLimit}
}

func (c *Connector) ID() string   { return ID }
func (c *Connector) Name() string { return Name }

// DisplayName is what people read: "Lark (as you)" in their language, matching the Lark page's "read as you" section.
func (c *Connector) DisplayName(ctx context.Context) string {
	return i18n.T(c.lang(ctx), "hub-connectors.larkcli_name")
}

// Profile is the effective profile used by every lark-cli invocation.
func (c *Connector) Profile() string {
	if c == nil {
		return ""
	}
	if c.profile != "" {
		return c.profile
	}
	if c.runner != nil {
		return c.runner.Profile()
	}
	return ""
}

func (c *Connector) Manifest() connectorwire.Manifest {
	return connectorwire.Manifest{Capabilities: []connectorwire.Capability{connectorwire.CapStatus, connectorwire.CapTools, connectorwire.CapEvents, connectorwire.CapInbox, connectorwire.CapOptions, connectorwire.CapLogin, connectorwire.CapThreads, connectorwire.CapInboxVisibility}, Kinds: []string{"dm", "bot", "mention", "group", "doc", "bot_handoff", connectorwire.OwnerPromiseKind}}
}

func (c *Connector) Tools() []proto.Tool {
	return []proto.Tool{tool("larkcli_chats"), tool("larkcli_messages"), tool("larkcli_search")}
}

func tool(name string) proto.Tool {
	t := prompts.Tool(name)
	return proto.Tool{Name: t.Name, Description: t.Description, InputSchema: t.InputSchema}
}

// Status reads the CLI version and auth status. It never includes raw command
// output, message bodies, or account tokens; the configured profile appears
// only in the localized login command shown when the user must sign in.
func (c *Connector) Status(ctx context.Context) connectorwire.Status {
	st := connectorwire.Status{State: connectorwire.StateError, Via: "lark-cli"}
	if c == nil || c.runner == nil {
		return c.statusError(ctx, st, nil, adapter.KindNotInstalled)
	}
	if err := c.runner.CheckVersion(ctx); err != nil {
		return c.statusForError(ctx, st, err)
	}
	auth, err := c.runner.AuthStatus(ctx)
	if err != nil {
		return c.statusForError(ctx, st, err)
	}
	st.Account = auth.UserName
	if st.Account == "" {
		st.Account = auth.OpenID
	}
	if auth.Expired {
		return c.statusExpired(ctx, st)
	}
	if !auth.LoggedIn {
		return c.statusNotSignedIn(ctx, st)
	}
	missing := append([]string(nil), auth.MissingScopes...)
	missing = append(missing, missingRequiredScopes(auth.GrantedScopes)...)
	if c.docs != nil {
		missing = append(missing, c.docs.scopesMissing()...)
	}
	if missing = uniqueScopes(missing); len(missing) > 0 {
		return c.statusMissingScopes(ctx, st, missing, auth.AppID, auth.Brand)
	}
	st.State = connectorwire.StateOK
	now := c.currentTime()
	if auth.RefreshWindowNeedsReminder(now) {
		st.Error = i18n.T(c.lang(ctx), "hub-connectors.expiry_reminder", i18n.Args{
			"count":   auth.DaysUntilExpiry(now),
			"command": c.loginCommand(),
		})
	}
	if c.docs != nil {
		if warning := c.docs.warning(c.lang(ctx)); warning != "" {
			if st.Error != "" {
				st.Error += " · " + warning
			} else {
				st.Error = warning
			}
		}
	}
	return st
}

func (c *Connector) Call(ctx context.Context, name string, args json.RawMessage) (string, error) {
	if c == nil || c.runner == nil {
		return "", &adapter.Error{Kind: adapter.KindNotInstalled}
	}
	// The old gateway's field shapes are retained. The larkcli prefix avoids a
	// collision when a Lark gateway extension is installed at the same time.
	switch name {
	case "larkcli_chats", "lark_chats":
		return c.chats(ctx, args)
	case "larkcli_messages", "lark_messages":
		return c.messages(ctx, args)
	case "larkcli_search", "lark_search":
		return c.search(ctx, args)
	default:
		return "", fmt.Errorf("unknown tool %s", name)
	}
}

func (c *Connector) chats(ctx context.Context, args json.RawMessage) (string, error) {
	var a struct {
		Filter string `json:"filter"`
		Limit  int    `json:"limit"`
	}
	if err := json.Unmarshal(args, &a); err != nil {
		return "", fmt.Errorf("invalid arguments: %v", err)
	}
	if a.Limit <= 0 || a.Limit > 100 {
		a.Limit = 30
	}
	switch a.Filter {
	case "", "recent", "unread":
		// Open Platform has no unread state. The CLI's active-time list is the
		// documented L2 fallback for both the old default and recent filter.
		page, err := c.runner.ListChats(ctx, a.Limit)
		if err == nil {
			page = c.filterReplyChats(page)
		}
		return marshal(page, err)
	case "at_me":
		if c.replyVisibilityEnabled() {
			// ListChatsAtMe is implemented by a body-returning message search, not a metadata chat-list endpoint.
			return "", errReplyVisibilityDenied
		}
		page, err := c.runner.ListChatsAtMe(ctx, a.Limit)
		if err == nil {
			page = c.filterReplyChats(page)
		}
		return marshal(page, err)
	default:
		return "", fmt.Errorf("unknown chat filter %q", a.Filter)
	}
}

func (c *Connector) messages(ctx context.Context, args json.RawMessage) (string, error) {
	var a struct {
		ChatID  string `json:"chat_id"`
		Thread  string `json:"thread"`
		Limit   int    `json:"limit"`
		Before  string `json:"before"`
		Account int    `json:"account"` // retained for gateway-compatible input; profile selects the CLI account
	}
	if err := json.Unmarshal(args, &a); err != nil {
		return "", fmt.Errorf("invalid arguments: %v", err)
	}
	if a.ChatID == "" && a.Thread == "" {
		return "", fmt.Errorf("chat_id is required")
	}
	if a.Thread != "" && c.replyVisibilityEnabled() {
		// The CLI reads a thread instead of chat_id whenever both are present. A chat_id therefore cannot authorize
		// a mixed request, and the thread's own chat cannot be verified before its bodies are returned.
		return "", errReplyVisibilityDenied
	}
	if err := c.checkReplyChat(ctx, a.ChatID); err != nil {
		return "", err
	}
	page, err := c.runner.ListMessages(ctx, a.ChatID, a.Thread, a.Before, a.Limit)
	return marshal(page, err)
}

func (c *Connector) search(ctx context.Context, args json.RawMessage) (string, error) {
	var a struct {
		Query string `json:"query"`
		Kind  string `json:"kind"`
	}
	if err := json.Unmarshal(args, &a); err != nil {
		return "", fmt.Errorf("invalid arguments: %v", err)
	}
	if strings.TrimSpace(a.Query) == "" {
		return "", fmt.Errorf("query is required")
	}
	if c.replyVisibilityEnabled() && (a.Kind == "" || a.Kind == "messages" || a.Kind == "chats") {
		return "", errors.New("lark-cli: global chat and message search is unavailable while private-chat visibility controls are enabled")
	}
	raw, err := c.runner.Search(ctx, a.Kind, a.Query)
	if err != nil {
		return "", err
	}
	return string(raw), nil
}

func marshal(value any, err error) (string, error) {
	if err != nil {
		return "", err
	}
	b, err := json.Marshal(value)
	return string(b), err
}

func (c *Connector) currentTime() time.Time {
	if c.now != nil {
		return c.now()
	}
	return time.Now()
}

func (c *Connector) lang(ctx context.Context) i18n.Lang { return i18n.From(ctx).Or(i18n.Default) }

func (c *Connector) statusForError(ctx context.Context, st connectorwire.Status, err error) connectorwire.Status {
	return c.statusError(ctx, st, err, adapter.KindOf(err))
}

func (c *Connector) statusError(ctx context.Context, st connectorwire.Status, err error, kind adapter.ErrorKind) connectorwire.Status {
	if e, ok := err.(*adapter.Error); ok {
		switch kind {
		case adapter.KindMissingPermission:
			return c.statusMissingScopes(ctx, st, e.MissingScopes, "", "")
		case adapter.KindNotLoggedIn:
			return c.statusNotSignedIn(ctx, st)
		case adapter.KindExpired:
			return c.statusExpired(ctx, st)
		}
	}
	st.State = connectorwire.StateError
	switch kind {
	case adapter.KindNotInstalled:
		st.Error = i18n.T(c.lang(ctx), "hub-connectors.not_installed")
		st.Guide = []string{i18n.T(c.lang(ctx), "hub-connectors.install_guide")}
	case adapter.KindUnsupportedVersion:
		if e, ok := err.(*adapter.Error); ok {
			st.Error = i18n.T(c.lang(ctx), "hub-connectors.old_version", i18n.Args{"got": e.Version, "want": e.Minimum})
		} else {
			st.Error = i18n.T(c.lang(ctx), "hub-connectors.old_version", i18n.Args{"got": "", "want": adapter.MinimumVersion})
		}
	case adapter.KindRateLimited:
		st.State = connectorwire.StateRateLimited
		st.Error = i18n.T(c.lang(ctx), "hub-connectors.rate_limited")
	default:
		st.Error = i18n.T(c.lang(ctx), "hub-connectors.error")
	}
	return st
}

func (c *Connector) statusNotSignedIn(ctx context.Context, st connectorwire.Status) connectorwire.Status {
	st.State = connectorwire.StateNotSignedIn
	st.Error = i18n.T(c.lang(ctx), "hub-connectors.not_signed_in")
	st.Guide = []string{i18n.T(c.lang(ctx), "hub-connectors.login_guide", i18n.Args{"command": c.loginCommand()})}
	return st
}

func (c *Connector) statusExpired(ctx context.Context, st connectorwire.Status) connectorwire.Status {
	st.State = connectorwire.StateNotSignedIn
	st.Error = i18n.T(c.lang(ctx), "hub-connectors.expired")
	st.Guide = []string{i18n.T(c.lang(ctx), "hub-connectors.login_guide", i18n.Args{"command": c.loginCommand()})}
	return st
}

func (c *Connector) statusMissingScopes(ctx context.Context, st connectorwire.Status, scopes []string, appID, brand string) connectorwire.Status {
	st.State = connectorwire.StateNoPermission
	st.MissingScopes = uniqueScopes(scopes)
	args := i18n.Args{"scope": strings.Join(st.MissingScopes, ", ")}
	st.Error = i18n.T(c.lang(ctx), "hub-connectors.missing_permission", args)
	st.Guide = []string{i18n.T(c.lang(ctx), "hub-connectors.login_guide", i18n.Args{"command": c.loginCommand()})}
	st.GuideURL = permissionURL(appID, brand)
	return st
}

func permissionURL(appID, brand string) string {
	if strings.TrimSpace(appID) == "" {
		return ""
	}
	domain := "feishu.cn"
	if strings.EqualFold(strings.TrimSpace(brand), "lark") {
		domain = "larksuite.com"
	}
	return "https://open." + domain + "/app/" + appID + "/auth"
}

// LoginStep starts and polls lark-cli's blocking device login. The process is
// kept independently of the HTTP request so the app can poll after the start
// response has returned.
func (c *Connector) LoginStep(ctx context.Context, step string, _ map[string]string) (connectorwire.Prompt, error) {
	switch step {
	case "":
		return c.startLogin(ctx)
	case "device":
		return c.pollLogin(ctx)
	default:
		return connectorwire.Prompt{}, fmt.Errorf("unknown step %q", step)
	}
}

func (c *Connector) startLogin(ctx context.Context) (connectorwire.Prompt, error) {
	if c == nil || c.runner == nil {
		return connectorwire.Prompt{}, &adapter.Error{Kind: adapter.KindNotInstalled}
	}
	c.cancelPending()
	p, err := c.runner.StartLogin(ctx)
	if err != nil {
		return connectorwire.Prompt{}, err
	}
	c.mu.Lock()
	c.pending = p
	c.mu.Unlock()
	return c.loginPrompt(p.Info(), ""), nil
}

func (c *Connector) pollLogin(ctx context.Context) (connectorwire.Prompt, error) {
	c.mu.Lock()
	p := c.pending
	c.mu.Unlock()
	if p == nil {
		return connectorwire.Prompt{Step: "device", Error: i18n.T(c.lang(ctx), "hub-connectors.login_restart")}, nil
	}
	done, err := p.Poll()
	if !done {
		return c.loginPrompt(p.Info(), ""), nil
	}
	c.mu.Lock()
	if c.pending == p {
		c.pending = nil
	}
	c.mu.Unlock()
	if err != nil {
		return c.loginPrompt(p.Info(), c.loginError(ctx, err)), nil
	}
	// The blocking CLI is the source of truth for completion. Read status once
	// here so connector status and the app's completed prompt change together.
	auth, statusErr := c.runner.AuthStatus(ctx)
	if statusErr != nil {
		return c.loginPrompt(p.Info(), c.loginError(ctx, statusErr)), nil
	}
	if !auth.LoggedIn {
		return c.loginPrompt(p.Info(), i18n.T(c.lang(ctx), "hub-connectors.login_not_complete")), nil
	}
	account := auth.UserName
	if account == "" {
		account = auth.OpenID
	}
	return connectorwire.Prompt{Done: true, Account: account}, nil
}

func (c *Connector) loginPrompt(info adapter.LoginInfo, err string) connectorwire.Prompt {
	return connectorwire.Prompt{Step: "device", Kind: connectorwire.PromptDeviceCode, URL: info.URL, UserCode: info.UserCode, Interval: 2, Error: err}
}

func (c *Connector) loginError(ctx context.Context, err error) string {
	if adapter.KindOf(err) == adapter.KindTimeout {
		return i18n.T(c.lang(ctx), "hub-connectors.login_timeout")
	}
	return i18n.T(c.lang(ctx), "hub-connectors.login_failed")
}

func (c *Connector) cancelPending() {
	c.mu.Lock()
	p := c.pending
	c.pending = nil
	c.mu.Unlock()
	if p != nil {
		p.Cancel()
	}
}

// Run gives Registry a lifecycle hook so a manager shutdown cancels a pending
// device login and never leaves lark-cli running after the connector stops.
func (c *Connector) Run(ctx context.Context) {
	<-ctx.Done()
	c.cancelPending()
}

// Logout cancels a pending login and asks lark-cli to remove the selected
// profile's user login.
func (c *Connector) Logout(ctx context.Context) error {
	if c == nil || c.runner == nil {
		return &adapter.Error{Kind: adapter.KindNotInstalled}
	}
	c.cancelPending()
	return c.runner.AuthLogout(ctx)
}

func (c *Connector) loginCommand() string {
	command := "lark-cli"
	profile := c.profile
	if profile == "" && c.runner != nil {
		profile = c.runner.Profile()
	}
	if profile != "" {
		command += " --profile " + commandArg(profile)
	}
	return command + " auth login --scope " + commandArg(strings.Join(adapter.ReadAsMeScopes, " "))
}

func commandArg(value string) string {
	if value != "" {
		safe := true
		for _, r := range value {
			if !(r >= 'a' && r <= 'z' || r >= 'A' && r <= 'Z' || r >= '0' && r <= '9' || strings.ContainsRune("_-.", r)) {
				safe = false
				break
			}
		}
		if safe {
			return value
		}
	}
	return "'" + strings.ReplaceAll(value, "'", "'\\''") + "'"
}

func missingRequiredScopes(granted []string) []string {
	have := make(map[string]struct{}, len(granted))
	for _, scope := range granted {
		have[strings.TrimSpace(scope)] = struct{}{}
	}
	var missing []string
	for _, scope := range adapter.ReadAsMeScopes {
		if _, ok := have[scope]; !ok {
			missing = append(missing, scope)
		}
	}
	return missing
}

func uniqueScopes(scopes []string) []string {
	seen := make(map[string]struct{}, len(scopes))
	result := make([]string, 0, len(scopes))
	for _, scope := range scopes {
		scope = strings.TrimSpace(scope)
		if scope == "" {
			continue
		}
		if _, ok := seen[scope]; ok {
			continue
		}
		seen[scope] = struct{}{}
		result = append(result, scope)
	}
	return result
}
