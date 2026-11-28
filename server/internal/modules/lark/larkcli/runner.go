// Package larkcli is the small, subprocess-only adapter for the official lark-cli.
// Credentials remain owned by lark-cli; this package only starts commands and decodes
// their JSON output (docs/plans/plan-lark-cli.md).
package larkcli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/nwverse/persona/server/internal/lazyre"
	"github.com/nwverse/persona/server/internal/osproc"
)

const defaultTimeout = 15 * time.Second

const loginTimeout = 10 * time.Minute

var (
	// maxCommandStdout and maxCommandStderr bound buffered output from a single lark-cli run
	// so a runaway subprocess cannot exhaust hub memory.
	maxCommandStdout  = 16 << 20
	maxCommandStderr  = 1 << 20
	loginReadyTimeout = 10 * time.Second
)

// Runner executes lark-cli with a bounded context and decodes only JSON output.
// It never logs stdout or stderr. The latter is captured solely to classify a
// structured CLI error without retaining its message body in the returned error.
type Runner struct {
	cfg Config

	mu   sync.Mutex
	path string

	clock func() time.Time
	cache statusCache
}

// New creates a runner. An empty Bin means lark-cli on PATH (or in the login
// shell's PATH, matching the L1 assistant wrapper).
func New(cfg Config) *Runner {
	if cfg.Timeout <= 0 {
		cfg.Timeout = defaultTimeout
	}
	return &Runner{cfg: cfg}
}

// NewRunner is a compact constructor for callers that already have the three
// configuration values from lark.cli.
func NewRunner(bin, profile string, timeout time.Duration) *Runner {
	return New(Config{Bin: bin, Profile: profile, Timeout: timeout})
}

// Profile returns the configured lark-cli profile name. It contains no
// credentials and is used only to make user-facing commands point at the same
// profile as the runner.
func (r *Runner) Profile() string {
	if r == nil {
		return ""
	}
	return r.cfg.Profile
}

// Run executes a read-only user command and returns its JSON stdout. It is the
// generic escape hatch for adapter additions; typed methods below own the L2
// command shapes.
func (r *Runner) Run(ctx context.Context, args ...string) ([]byte, error) {
	return r.execute(ctx, args, outputJSON)
}

// RunJSON is an explicit alias for Run for callers that want to document the
// output contract at the call site.
func (r *Runner) RunJSON(ctx context.Context, args ...string) ([]byte, error) {
	return r.Run(ctx, args...)
}

// RunAs executes a read or write command with an explicit lark-cli identity.
// Reads in this adapter use user by default; Inbox replies deliberately use bot
// because the default Lark app cannot send as the user.
func (r *Runner) RunAs(ctx context.Context, identity string, args ...string) ([]byte, error) {
	if identity == "" {
		identity = "user"
	}
	return r.executeAs(ctx, args, outputJSON, identity)
}

func (r *Runner) now() time.Time {
	if r != nil && r.cfg.Now != nil {
		return r.cfg.Now()
	}
	return time.Now()
}

// CheckVersion rejects versions older than the pinned minimum.
func (r *Runner) CheckVersion(ctx context.Context) error {
	got, err := r.Version(ctx)
	if err != nil {
		return err
	}
	if compareVersion(got, MinimumVersion) < 0 {
		return &Error{Kind: KindUnsupportedVersion, Version: got, Minimum: MinimumVersion}
	}
	return nil
}

// ConfigShow reads the selected profile's app binding. It never returns the
// app secret or any other credential material.
func (r *Runner) ConfigShow(ctx context.Context) (ConfigInfo, error) {
	out, err := r.execute(ctx, []string{"config", "show"}, outputRaw)
	if err != nil {
		return ConfigInfo{}, err
	}
	return parseConfigInfo(out), nil
}

// InitProfile creates or updates a named profile. The secret is written only
// to stdin; it is never put in argv, an error, or a log.
func (r *Runner) InitProfile(ctx context.Context, name, appID, secret, brand string) error {
	if strings.TrimSpace(name) == "" || strings.TrimSpace(appID) == "" || secret == "" {
		return errors.New("lark-cli profile initialization needs a name, app id and secret")
	}
	args := []string{"config", "init", "--name", name, "--app-id", appID, "--app-secret-stdin", "--brand", brand}
	_, err := r.executeInput(ctx, args, outputRaw, "", []byte(secret))
	r.invalidateAuth()
	return err
}

// AuthLogout signs the selected profile out. lark-cli owns the credential
// store; this method only asks it to remove the login.
func (r *Runner) AuthLogout(ctx context.Context) error {
	_, err := r.execute(ctx, []string{"auth", "logout"}, outputRaw)
	r.invalidateAuth()
	return err
}

// LoginInfo is the device-flow link printed by lark-cli auth login.
type LoginInfo struct {
	URL      string
	UserCode string
}

// LoginProcess is a blocking lark-cli auth login process. lark-cli itself
// performs the device-flow polling; Persona only exposes its link and watches
// the process until it exits.
type LoginProcess struct {
	mu     sync.Mutex
	info   LoginInfo
	err    error
	ready  chan struct{}
	done   chan struct{}
	cancel context.CancelFunc
	once   sync.Once
}

// StartLogin starts the connector's least-privilege `auth login --scope`
// request without --no-wait. The command
// has its own ten-minute lifetime so the HTTP request that began the login may
// finish while the process continues in the background.
func (r *Runner) StartLogin(ctx context.Context) (*LoginProcess, error) {
	if err := ctx.Err(); err != nil {
		return nil, cliError(KindTimeout, err)
	}
	profile, err := r.resolveProfile(ctx)
	if err != nil {
		return nil, err
	}
	bin, err := r.binary(ctx)
	if err != nil {
		return nil, err
	}
	r.invalidateAuth()
	loginCtx, cancel := context.WithTimeout(context.Background(), loginTimeout)
	p := &LoginProcess{ready: make(chan struct{}), done: make(chan struct{}), cancel: cancel}
	argv := profileArgs(profile, []string{"auth", "login", "--scope", strings.Join(ReadAsMeScopes, " ")})
	cmd := exec.CommandContext(loginCtx, bin, argv...)
	cmd.Env = cliEnv(bin)
	// lark-cli may launch a child process of its own. Killing only the shell
	// wrapper leaves that child holding stdout open, so start it in its own
	// session/process group (avoiding SIGTTOU/SIGTTIN) and terminate the group
	// on cancellation or timeout via cmd.Cancel.
	cmd.SysProcAttr = osproc.SessionGroupAttr()
	var procID atomic.Pointer[osproc.ProcIdentity]
	cmd.Cancel = func() error {
		return stopProcessGroup(cmd.Process, &procID)
	}
	cmd.WaitDelay = time.Second
	stdoutW := &loginLineWriter{p: p}
	stderrW := &loginLineWriter{p: p}
	cmd.Stdout = stdoutW
	cmd.Stderr = stderrW
	if err := cmd.Start(); err != nil {
		cancel()
		return nil, cliError(KindOther, err)
	}
	id := osproc.Identify(cmd.Process.Pid)
	procID.Store(&id)
	go func() {
		err := cmd.Wait()
		stdoutW.Flush()
		stderrW.Flush()
		if errors.Is(err, exec.ErrWaitDelay) && loginCtx.Err() == nil {
			cleanupWaitDelayGroup(cmd.Process, &procID)
			err = nil
		}
		p.mu.Lock()
		if loginCtx.Err() == context.DeadlineExceeded {
			p.err = &Error{Kind: KindTimeout}
		} else if err != nil && loginCtx.Err() == nil {
			p.err = cliError(KindOther, err)
		}
		p.mu.Unlock()
		cancel()
		r.invalidateAuth()
		close(p.done)
	}()
	// The URL is normally printed immediately. A short independent wait keeps
	// a malformed or very old CLI from making the request hang forever.
	timer := time.NewTimer(loginReadyTimeout)
	defer timer.Stop()
	select {
	case <-p.ready:
		return p, nil
	case <-p.done:
		select {
		case <-p.ready:
			return p, nil
		default:
			return p, p.startError()
		}
	case <-ctx.Done():
		p.Cancel()
		<-p.done
		return nil, cliError(KindTimeout, ctx.Err())
	case <-timer.C:
		p.Cancel()
		<-p.done
		return nil, &Error{Kind: KindTimeout}
	}
}

// Profiles reads the named lark-cli profiles without returning secrets. The
// profile list command emits JSON by default, so no output-format flag is
// needed (and older CLI releases reject one).
func (r *Runner) Profiles(ctx context.Context) ([]Profile, error) {
	out, err := r.executeNoProfile(ctx, []string{"profile", "list"}, outputRaw)
	if err != nil {
		return nil, err
	}
	var raw []profileWire
	if err := json.Unmarshal(out, &raw); err != nil {
		var envelope struct {
			Profiles []profileWire `json:"profiles"`
		}
		if err := json.Unmarshal(out, &envelope); err != nil {
			return nil, cliError(KindOther, errors.New("invalid profile list JSON"))
		}
		raw = envelope.Profiles
	}
	profiles := make([]Profile, 0, len(raw))
	for _, p := range raw {
		name := strings.TrimSpace(firstNonEmpty(p.Name, p.NameSnake))
		if name == "" {
			continue
		}
		profiles = append(profiles, Profile{
			Name:        name,
			AppID:       strings.TrimSpace(firstNonEmpty(p.AppID, p.AppIDSnake)),
			Brand:       strings.TrimSpace(p.Brand),
			User:        strings.TrimSpace(firstNonEmpty(p.User, p.UserSnake)),
			TokenStatus: strings.TrimSpace(firstNonEmpty(p.TokenStatus, p.TokenStatusSnake)),
		})
	}
	return profiles, nil
}

type profileWire struct {
	Name             string `json:"name"`
	NameSnake        string `json:"profile_name"`
	AppID            string `json:"appId"`
	AppIDSnake       string `json:"app_id"`
	Brand            string `json:"brand"`
	User             string `json:"user"`
	UserSnake        string `json:"user_name"`
	TokenStatus      string `json:"tokenStatus"`
	TokenStatusSnake string `json:"token_status"`
}

func (r *Runner) resolveProfile(ctx context.Context) (string, error) {
	if r.cfg.ResolveProfile != nil {
		return r.cfg.ResolveProfile(ctx)
	}
	return r.cfg.Profile, nil
}

func profileArgs(profile string, args []string) []string {
	argv := make([]string, 0, len(args)+2)
	if profile != "" {
		argv = append(argv, "--profile", profile)
	}
	return append(argv, args...)
}

func (p *LoginProcess) line(line string) {
	if info, ok := parseLoginInfo(line); ok {
		p.mu.Lock()
		if p.info.URL == "" {
			p.info = info
			p.once.Do(func() { close(p.ready) })
		}
		p.mu.Unlock()
	}
}

func (p *LoginProcess) startError() error {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.err != nil {
		return p.err
	}
	return errors.New("lark-cli did not provide a device verification link")
}

// Info returns the URL and user code after StartLogin has observed them.
func (p *LoginProcess) Info() LoginInfo {
	if p == nil {
		return LoginInfo{}
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.info
}

// Poll reports whether the process exited. It never returns command output.
func (p *LoginProcess) Poll() (bool, error) {
	if p == nil {
		return true, errors.New("no lark-cli login process")
	}
	select {
	case <-p.done:
		p.mu.Lock()
		err := p.err
		p.mu.Unlock()
		return true, err
	default:
		return false, nil
	}
}

// Cancel stops the process and is safe to call more than once.
func (p *LoginProcess) Cancel() {
	if p != nil && p.cancel != nil {
		p.cancel()
	}
}

// ListChats calls the recent chat shortcut. It includes both group and direct
// chats and asks the CLI for newest activity first.
func (r *Runner) ListChats(ctx context.Context, limit int) (ChatList, error) {
	if limit <= 0 {
		limit = 30
	}
	if limit > 100 {
		limit = 100
	}
	out, err := r.execute(ctx, []string{"im", "+chat-list", "--types", "p2p,group", "--sort", "active_time", "--page-size", fmt.Sprint(limit)}, outputJSON)
	if err != nil {
		return ChatList{}, err
	}
	var page ChatList
	if err := decodeData(out, &page); err != nil {
		return ChatList{}, cliError(KindOther, errors.New("invalid chat list JSON"))
	}
	return page, nil
}

// ListChatsAtMe finds chats with a message mentioning the user and normalizes
// the message results to the same chat list shape as ListChats.
func (r *Runner) ListChatsAtMe(ctx context.Context, limit int) (ChatList, error) {
	if limit <= 0 {
		limit = 30
	}
	if limit > 100 {
		limit = 100
	}
	page, err := r.SearchMessages(ctx, "", SearchOptions{AtMe: true, Limit: limit})
	if err != nil {
		return ChatList{}, err
	}
	return ChatListFromMessages(page.Messages, limit), nil
}

// ChatListFromMessages keeps one row per conversation while preserving the
// first result's activity order.
func ChatListFromMessages(messages []Message, limit int) ChatList {
	seen := map[string]bool{}
	out := ChatList{}
	for _, message := range messages {
		if message.ChatID == "" || seen[message.ChatID] {
			continue
		}
		seen[message.ChatID] = true
		out.Chats = append(out.Chats, Chat{ChatID: message.ChatID, Name: message.ChatName, ChatType: message.ChatType})
		if len(out.Chats) == limit {
			break
		}
	}
	out.Total = len(out.Chats)
	return out
}

// ListMessages reads one chat or thread. The CLI's newest-first response is
// reversed so the connector's historical tool contract remains oldest first.
func (r *Runner) ListMessages(ctx context.Context, chatID, thread, before string, limit int) (MessagePage, error) {
	if limit <= 0 {
		limit = 40
	}
	if limit > 50 {
		limit = 50
	}
	args := []string{"im"}
	if thread != "" {
		args = append(args, "+threads-messages-list", "--thread", thread)
	} else {
		if chatID == "" {
			return MessagePage{}, errors.New("chat_id is required")
		}
		args = append(args, "+chat-messages-list", "--chat-id", chatID)
	}
	args = append(args, "--order", "desc", "--page-size", fmt.Sprint(limit), "--no-reactions")
	if before != "" {
		args = append(args, "--page-token", before)
	}
	out, err := r.execute(ctx, args, outputJSON)
	if err != nil {
		return MessagePage{}, err
	}
	var page MessagePage
	if err := decodeData(out, &page); err != nil {
		return MessagePage{}, cliError(KindOther, errors.New("invalid message list JSON"))
	}
	for i, j := 0, len(page.Messages)-1; i < j; i, j = i+1, j-1 {
		page.Messages[i], page.Messages[j] = page.Messages[j], page.Messages[i]
	}
	return page, nil
}

// GetChat reads one chat's own record (`im chats get`): its mode (group,
// topic, p2p) and name. The message lists carry neither (lark-cli 1.0.98 to
// 1.0.100); only a search result does, for the chats it found.
func (r *Runner) GetChat(ctx context.Context, chatID string) (Chat, error) {
	if chatID == "" {
		return Chat{}, errors.New("chat_id is required")
	}
	out, err := r.execute(ctx, []string{"im", "chats", "get", "--chat-id", chatID}, outputJSON)
	if err != nil {
		return Chat{}, err
	}
	var chat Chat
	if err := decodeData(out, &chat); err != nil {
		return Chat{}, cliError(KindOther, errors.New("invalid chat JSON"))
	}
	chat.ChatID = chatID
	return chat, nil
}

// ListDirectChats lists the user's direct (p2p) chats, most recently active
// first. It needs the user identity, like every Inbox read.
func (r *Runner) ListDirectChats(ctx context.Context, limit int) (ChatList, error) {
	if limit <= 0 {
		limit = 20
	}
	if limit > 100 {
		limit = 100
	}
	out, err := r.execute(ctx, []string{"im", "+chat-list", "--types", "p2p", "--sort", "active_time", "--page-size", fmt.Sprint(limit)}, outputJSON)
	if err != nil {
		return ChatList{}, err
	}
	var page ChatList
	if err := decodeData(out, &page); err != nil {
		return ChatList{}, cliError(KindOther, errors.New("invalid chat list JSON"))
	}
	return page, nil
}

// ListMessagesSince reads one chat's messages created at or after start
// (RFC 3339), newest page only, returned oldest first. Unlike message search it
// reads the chat history itself, which has no indexing delay.
func (r *Runner) ListMessagesSince(ctx context.Context, chatID, start string, limit int) (MessagePage, error) {
	if chatID == "" {
		return MessagePage{}, errors.New("chat_id is required")
	}
	if limit <= 0 {
		limit = 50
	}
	if limit > 50 {
		limit = 50
	}
	args := []string{"im", "+chat-messages-list", "--chat-id", chatID}
	if start != "" {
		args = append(args, "--start", start)
	}
	args = append(args, "--order", "desc", "--page-size", fmt.Sprint(limit), "--no-reactions")
	out, err := r.execute(ctx, args, outputJSON)
	if err != nil {
		return MessagePage{}, err
	}
	var page MessagePage
	if err := decodeData(out, &page); err != nil {
		return MessagePage{}, cliError(KindOther, errors.New("invalid message list JSON"))
	}
	for i, j := 0, len(page.Messages)-1; i < j; i, j = i+1, j-1 {
		page.Messages[i], page.Messages[j] = page.Messages[j], page.Messages[i]
	}
	return page, nil
}

// SearchMessages searches the user's messages. It deliberately disables
// reaction enrichment: reactions are an optional second permission and are not
// needed by the read-tool contract.
func (r *Runner) SearchMessages(ctx context.Context, query string, opts SearchOptions) (MessagePage, error) {
	args := []string{"im", "+messages-search"}
	if query != "" {
		args = append(args, "--query", query)
	}
	for _, pair := range [][2]string{{"--chat-id", opts.ChatID}, {"--sender", opts.Sender}, {"--chat-type", opts.ChatType}, {"--start", opts.Start}, {"--end", opts.End}} {
		if pair[1] != "" {
			args = append(args, pair[0], pair[1])
		}
	}
	if opts.AtMe {
		args = append(args, "--is-at-me")
	}
	args = append(args, "--page-size", fmt.Sprint(opts.NormalizedLimit()), "--no-reactions")
	if opts.PageToken != "" {
		args = append(args, "--page-token", opts.PageToken)
	}
	out, err := r.execute(ctx, args, outputJSON)
	if err != nil {
		return MessagePage{}, err
	}
	var page MessagePage
	if err := decodeData(out, &page); err != nil {
		return MessagePage{}, cliError(KindOther, errors.New("invalid message search JSON"))
	}
	return page, nil
}

// InspectDocument resolves a document or wiki link to the title and the
// underlying Drive metadata. It is deliberately read-only and always runs as
// the lark-cli user identity.
// OwnedDocument is one document the signed-in user owns, from `drive +search --mine`.
type OwnedDocument struct {
	URL     string
	Token   string
	Type    string
	Title   string
	Owner   string
	Updated time.Time
	Opened  time.Time
}

// OwnedDocuments lists documents the user owns, most recently edited first, one page of at most 20 (`drive +search
// --mine --sort edit_time`). next is the token for the following page, empty at the end.
func (r *Runner) OwnedDocuments(ctx context.Context, pageToken string) (docs []OwnedDocument, next string, err error) {
	// Only types whose comments lark-cli can list: folders and mind notes would only fail every read.
	args := []string{"drive", "+search", "--mine", "--sort", "edit_time", "--page-size", "20", "--doc-types", commentableDocTypes}
	if pageToken != "" {
		args = append(args, "--page-token", pageToken)
	}
	out, err := r.execute(ctx, args, outputJSON)
	if err != nil {
		return nil, "", err
	}
	var raw struct {
		HasMore   bool   `json:"has_more"`
		PageToken string `json:"page_token"`
		Results   []struct {
			Title string `json:"title_highlighted"`
			Meta  struct {
				URL      string `json:"url"`
				Token    string `json:"token"`
				DocTypes string `json:"doc_types"`
				Owner    string `json:"owner_id"`
				Updated  int64  `json:"update_time"`
				Opened   int64  `json:"last_open_time"`
			} `json:"result_meta"`
		} `json:"results"`
	}
	if err := decodeData(out, &raw); err != nil {
		return nil, "", cliError(KindOther, errors.New("invalid document search JSON"))
	}
	for _, res := range raw.Results {
		if res.Meta.URL == "" {
			continue
		}
		d := OwnedDocument{URL: res.Meta.URL, Token: res.Meta.Token, Type: strings.ToLower(res.Meta.DocTypes), Title: stripHighlight(res.Title), Owner: res.Meta.Owner}
		if res.Meta.Updated > 0 {
			d.Updated = time.Unix(res.Meta.Updated, 0).UTC()
		}
		if res.Meta.Opened > 0 {
			d.Opened = time.Unix(res.Meta.Opened, 0).UTC()
		}
		docs = append(docs, d)
	}
	if raw.HasMore {
		next = raw.PageToken
	}
	return docs, next, nil
}

// commentableDocTypes are the drive search types `drive +list-comments` accepts (lark-cli 1.0.99).
const commentableDocTypes = "doc,docx,sheet,bitable,file,slides,wiki"

// stripHighlight drops the search's <em> marks from a title.
func stripHighlight(s string) string {
	return strings.TrimSpace(strings.NewReplacer("<em>", "", "</em>", "", "<b>", "", "</b>", "").Replace(s))
}

// DocumentOwner returns the open_id of a document's owner (`drive metas batch_query`). token and docType are the
// underlying document's, as InspectDocument returns them (a wiki link is unwrapped there).
func (r *Runner) DocumentOwner(ctx context.Context, token, docType string) (string, error) {
	if strings.TrimSpace(token) == "" || strings.TrimSpace(docType) == "" {
		return "", errors.New("document token and type are required")
	}
	body, err := json.Marshal(map[string]any{"request_docs": []map[string]string{{"doc_token": token, "doc_type": docType}}})
	if err != nil {
		return "", err
	}
	out, err := r.execute(ctx, []string{"drive", "metas", "batch_query", "--user-id-type", "open_id", "--data", string(body)}, outputJSON)
	if err != nil {
		return "", err
	}
	var raw struct {
		Metas []struct {
			Token string `json:"doc_token"`
			Owner string `json:"owner_id"`
		} `json:"metas"`
		Failed []struct {
			Code int `json:"code"`
		} `json:"failed_list"`
	}
	if err := decodeData(out, &raw); err != nil {
		return "", cliError(KindOther, errors.New("invalid document metadata JSON"))
	}
	for _, meta := range raw.Metas {
		if meta.Owner != "" {
			return meta.Owner, nil
		}
	}
	if len(raw.Failed) > 0 {
		return "", &Error{Kind: KindMissingPermission}
	}
	return "", nil
}

func (r *Runner) InspectDocument(ctx context.Context, link string) (Document, error) {
	if strings.TrimSpace(link) == "" {
		return Document{}, errors.New("document URL is required")
	}
	out, err := r.execute(ctx, []string{"drive", "+inspect", "--url", link}, outputJSON)
	if err != nil {
		return Document{}, err
	}
	var raw struct {
		Document
		Canonical string `json:"canonical_url"`
		FileToken string `json:"file_token"`
		FileType  string `json:"file_type"`
		ObjToken  string `json:"obj_token"`
		ObjType   string `json:"obj_type"`
		Metas     []struct {
			URL   string `json:"url"`
			Token string `json:"doc_token"`
			Type  string `json:"doc_type"`
			Title string `json:"title"`
		} `json:"metas"`
		Failed []struct {
			Code int `json:"code"`
		} `json:"failed_list"`
	}
	if err := decodeData(out, &raw); err != nil {
		return Document{}, cliError(KindOther, errors.New("invalid document metadata JSON"))
	}
	if len(raw.Failed) > 0 && len(raw.Metas) == 0 {
		return Document{}, &Error{Kind: KindMissingPermission}
	}
	d := raw.Document
	if d.URL == "" {
		d.URL = link
	}
	if d.CanonicalURL == "" {
		d.CanonicalURL = raw.Canonical
	}
	if d.Token == "" {
		d.Token = raw.FileToken
	}
	if d.Token == "" {
		d.Token = raw.ObjToken
	}
	if d.Type == "" {
		d.Type = raw.FileType
	}
	if d.Type == "" {
		d.Type = raw.ObjType
	}
	if len(raw.Metas) > 0 {
		meta := raw.Metas[0]
		if d.URL == "" {
			d.URL = meta.URL
		}
		if d.CanonicalURL == "" {
			d.CanonicalURL = meta.URL
		}
		if d.Token == "" {
			d.Token = meta.Token
		}
		if d.Type == "" {
			d.Type = meta.Type
		}
		if d.Title == "" {
			d.Title = meta.Title
		}
	}
	return d, nil
}

// ListComments lists unresolved comments on a document. The returned page
// token is an opaque value for the next page in the same poll.
func (r *Runner) ListComments(ctx context.Context, link, pageToken string, limit int) (CommentPage, error) {
	if strings.TrimSpace(link) == "" {
		return CommentPage{}, errors.New("document URL is required")
	}
	args := []string{"drive", "+list-comments", "--url", link, "--solved-status", "false", "--page-size", fmt.Sprint(pageLimit(limit))}
	if pageToken != "" {
		args = append(args, "--page-token", pageToken)
	}
	out, err := r.execute(ctx, args, outputJSON)
	if err != nil {
		return CommentPage{}, err
	}
	var raw struct {
		Items     []Comment `json:"items"`
		Comments  []Comment `json:"comments"`
		HasMore   bool      `json:"has_more,omitempty"`
		PageToken string    `json:"page_token,omitempty"`
	}
	if err := decodeData(out, &raw); err != nil {
		return CommentPage{}, cliError(KindOther, errors.New("invalid document comments JSON"))
	}
	if len(raw.Items) == 0 {
		raw.Items = raw.Comments
	}
	return CommentPage{Comments: raw.Items, HasMore: raw.HasMore, PageToken: raw.PageToken}, nil
}

// ListReplies lists one comment's root body and replies. The first page's
// first item is the root body according to lark-cli's documented contract.
func (r *Runner) ListReplies(ctx context.Context, link, commentID, pageToken string, limit int) (ReplyPage, error) {
	if strings.TrimSpace(link) == "" || strings.TrimSpace(commentID) == "" {
		return ReplyPage{}, errors.New("document URL and comment id are required")
	}
	args := []string{"drive", "+list-replies", "--url", link, "--comment-id", commentID, "--page-size", fmt.Sprint(pageLimit(limit))}
	if pageToken != "" {
		args = append(args, "--page-token", pageToken)
	}
	out, err := r.execute(ctx, args, outputJSON)
	if err != nil {
		return ReplyPage{}, err
	}
	var raw struct {
		Items     []Reply `json:"items"`
		Replies   []Reply `json:"replies"`
		HasMore   bool    `json:"has_more,omitempty"`
		PageToken string  `json:"page_token,omitempty"`
	}
	if err := decodeData(out, &raw); err != nil {
		return ReplyPage{}, cliError(KindOther, errors.New("invalid document replies JSON"))
	}
	if len(raw.Items) == 0 {
		raw.Items = raw.Replies
	}
	return ReplyPage{Replies: raw.Items, HasMore: raw.HasMore, PageToken: raw.PageToken}, nil
}

// ListDocumentComments is a descriptive alias used by the connector.
func (r *Runner) ListDocumentComments(ctx context.Context, link, pageToken string, limit int) (CommentPage, error) {
	return r.ListComments(ctx, link, pageToken, limit)
}

func (r *Runner) ListWikiSpaces(ctx context.Context, pageToken string, limit int) (WikiSpaceList, error) {
	args := []string{"wiki", "spaces", "list", "--page-size", fmt.Sprint(wikiPageLimit(limit))}
	if pageToken != "" {
		args = append(args, "--page-token", pageToken)
	}
	out, err := r.execute(ctx, args, outputJSON)
	if err != nil {
		return WikiSpaceList{}, err
	}
	var raw struct {
		Items     []WikiSpace `json:"items"`
		Spaces    []WikiSpace `json:"spaces"`
		HasMore   bool        `json:"has_more,omitempty"`
		PageToken string      `json:"page_token,omitempty"`
	}
	if err := decodeData(out, &raw); err != nil {
		return WikiSpaceList{}, cliError(KindOther, errors.New("invalid wiki spaces JSON"))
	}
	if len(raw.Items) == 0 {
		raw.Items = raw.Spaces
	}
	return WikiSpaceList{Spaces: raw.Items, HasMore: raw.HasMore, PageToken: raw.PageToken}, nil
}

func (r *Runner) ListWikiNodes(ctx context.Context, spaceID, parentToken, pageToken string, limit int) (WikiNodeList, error) {
	if strings.TrimSpace(spaceID) == "" {
		return WikiNodeList{}, errors.New("wiki space id is required")
	}
	args := []string{"wiki", "nodes", "list", "--space-id", spaceID, "--page-size", fmt.Sprint(wikiPageLimit(limit))}
	if parentToken != "" {
		args = append(args, "--parent-node-token", parentToken)
	}
	if pageToken != "" {
		args = append(args, "--page-token", pageToken)
	}
	out, err := r.execute(ctx, args, outputJSON)
	if err != nil {
		return WikiNodeList{}, err
	}
	var raw struct {
		Items     []WikiNode `json:"items"`
		Nodes     []WikiNode `json:"nodes"`
		HasMore   bool       `json:"has_more,omitempty"`
		PageToken string     `json:"page_token,omitempty"`
	}
	if err := decodeData(out, &raw); err != nil {
		return WikiNodeList{}, cliError(KindOther, errors.New("invalid wiki nodes JSON"))
	}
	if len(raw.Items) == 0 {
		raw.Items = raw.Nodes
	}
	return WikiNodeList{Nodes: raw.Items, HasMore: raw.HasMore, PageToken: raw.PageToken}, nil
}

func pageLimit(limit int) int {
	if limit <= 0 {
		return 50
	}
	if limit > 100 {
		return 100
	}
	return limit
}

func wikiPageLimit(limit int) int {
	if limit <= 0 {
		return 50
	}
	if limit > 50 {
		return 50
	}
	return limit
}

// Search runs the lark_search kind mappings that are safe and read-only in L2.
// Message search is typed; the other supported kinds are returned as the CLI's
// normalized JSON data because their schemas belong to later connector steps.
func (r *Runner) Search(ctx context.Context, kind, query string) (json.RawMessage, error) {
	return r.SearchLimit(ctx, kind, query, 0)
}

// SearchLimit bounds the requested search page. A nonpositive limit preserves
// the CLI's default page size; message search retains its existing default.
func (r *Runner) SearchLimit(ctx context.Context, kind, query string, limit int) (json.RawMessage, error) {
	var args []string
	maximum := 0
	switch kind {
	case "", "messages":
		page, err := r.SearchMessages(ctx, query, SearchOptions{Limit: limit})
		if err != nil {
			return nil, err
		}
		return json.Marshal(page)
	case "chats":
		args = []string{"im", "+chat-search", "--query", query}
		maximum = 50
	case "docs":
		args = []string{"drive", "+search", "--query", query}
		maximum = 20
	case "people":
		args = []string{"contact", "+search-user", "--query", query}
		maximum = 30
	default:
		return nil, fmt.Errorf("unknown search kind %q", kind)
	}
	if limit > 0 {
		if limit > maximum {
			limit = maximum
		}
		args = append(args, "--page-size", fmt.Sprint(limit))
	}
	return r.rawSearch(ctx, args)
}

// SendText sends a plain text message as the bot identity. The idempotency key
// is optional and is passed through to lark-cli when supplied.
func (r *Runner) SendText(ctx context.Context, chatID, text, idempotencyKey string) (json.RawMessage, error) {
	return r.SendTextAs(ctx, "bot", chatID, text, idempotencyKey)
}

func (r *Runner) SendTextAs(ctx context.Context, identity, chatID, text, idempotencyKey string) (json.RawMessage, error) {
	if identity != "bot" && identity != "user" {
		return nil, errors.New("unsupported message sender identity")
	}
	if strings.TrimSpace(chatID) == "" {
		return nil, errors.New("chat_id is required")
	}
	if text == "" {
		return nil, errors.New("text is required")
	}
	args := []string{"im", "+messages-send", "--chat-id", chatID, "--text", text}
	if idempotencyKey != "" {
		args = append(args, "--idempotency-key", idempotencyKey)
	}
	return r.RunAs(ctx, identity, args...)
}

// ReplyText replies to a known message as the bot identity. The caller may ask
// for a topic reply; normal Inbox replies keep the source message's topic.
func (r *Runner) ReplyText(ctx context.Context, messageID, text, idempotencyKey string, inThread bool) (json.RawMessage, error) {
	return r.ReplyTextAs(ctx, "bot", messageID, text, idempotencyKey, inThread)
}

func (r *Runner) ReplyTextAs(ctx context.Context, identity, messageID, text, idempotencyKey string, inThread bool) (json.RawMessage, error) {
	if identity != "bot" && identity != "user" {
		return nil, errors.New("unsupported message sender identity")
	}
	if strings.TrimSpace(messageID) == "" {
		return nil, errors.New("message_id is required")
	}
	if text == "" {
		return nil, errors.New("text is required")
	}
	args := []string{"im", "+messages-reply", "--message-id", messageID, "--text", text}
	if idempotencyKey != "" {
		args = append(args, "--idempotency-key", idempotencyKey)
	}
	if inThread {
		args = append(args, "--reply-in-thread")
	}
	return r.RunAs(ctx, identity, args...)
}

func (r *Runner) rawSearch(ctx context.Context, args []string) (json.RawMessage, error) {
	out, err := r.execute(ctx, args, outputJSON)
	if err != nil {
		return nil, err
	}
	data, err := unwrapData(out)
	if err != nil {
		return nil, cliError(KindOther, errors.New("invalid search JSON"))
	}
	return data, nil
}

type outputMode uint8

const (
	outputVersion outputMode = iota
	outputAuth
	outputJSON
	outputRaw
)

type commandResult struct {
	stdout []byte
	stderr []byte
}

func (r *Runner) execute(ctx context.Context, args []string, mode outputMode) ([]byte, error) {
	identity := ""
	if mode == outputJSON {
		identity = "user"
	}
	return r.executeAs(ctx, args, mode, identity)
}

func (r *Runner) executeAs(ctx context.Context, args []string, mode outputMode, identity string) ([]byte, error) {
	return r.executeInputProfile(ctx, args, mode, identity, nil, true)
}

func (r *Runner) executeNoProfile(ctx context.Context, args []string, mode outputMode) ([]byte, error) {
	return r.executeInputProfile(ctx, args, mode, "", nil, false)
}

func (r *Runner) executeInput(ctx context.Context, args []string, mode outputMode, identity string, input []byte) ([]byte, error) {
	return r.executeInputProfile(ctx, args, mode, identity, input, true)
}

const (
	loginLineLimit = 64 << 10
	loginScanLimit = 1 << 20
)

// loginLineWriter scans stdout/stderr lines for the device-flow login URL with bounded memory
// while always draining excess bytes so the child process never blocks on a full pipe.
type loginLineWriter struct {
	mu      sync.Mutex
	p       *LoginProcess
	buf     []byte
	scanned int
	skip    bool
}

func (w *loginLineWriter) Write(b []byte) (int, error) {
	w.mu.Lock()
	defer w.mu.Unlock()
	n := len(b)
	if w.scanned >= loginScanLimit {
		return n, nil
	}
	rem := loginScanLimit - w.scanned
	truncated := len(b) > rem
	if truncated {
		b = b[:rem]
	}
	w.scanned += len(b)
	for len(b) > 0 {
		nl := bytes.IndexByte(b, '\n')
		var chunk []byte
		if nl >= 0 {
			chunk = b[:nl]
			b = b[nl+1:]
		} else {
			chunk = b
			b = nil
		}
		if !w.skip {
			if len(w.buf)+len(chunk) > loginLineLimit {
				w.buf = nil
				w.skip = true
			} else {
				w.buf = append(w.buf, chunk...)
			}
		}
		if nl >= 0 {
			if !w.skip && len(w.buf) > 0 {
				w.p.line(strings.TrimRight(string(w.buf), "\r"))
			}
			w.buf = w.buf[:0]
			w.skip = false
		}
	}
	if truncated || w.scanned >= loginScanLimit {
		// Discard any unterminated partial line at the scan cap so Flush cannot parse
		// a half-written URL or user_code, and release the line buffer.
		w.buf = nil
		w.skip = true
	}
	return n, nil
}

func (w *loginLineWriter) Flush() {
	w.mu.Lock()
	defer w.mu.Unlock()
	if !w.skip && len(w.buf) > 0 {
		w.p.line(strings.TrimRight(string(w.buf), "\r"))
	}
	w.buf = nil
	w.skip = false
}

type cappedBuffer struct {
	mu        sync.Mutex
	buf       bytes.Buffer
	limit     int
	truncated bool
}

func (b *cappedBuffer) Write(p []byte) (int, error) {
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.limit <= 0 {
		if len(p) > 0 {
			b.truncated = true
		}
		return len(p), nil
	}
	room := b.limit - b.buf.Len()
	if room > 0 {
		if len(p) <= room {
			_, _ = b.buf.Write(p)
			return len(p), nil
		}
		_, _ = b.buf.Write(p[:room])
	}
	if len(p) > room {
		b.truncated = true
	}
	return len(p), nil
}

func (b *cappedBuffer) bytes() ([]byte, bool) {
	b.mu.Lock()
	defer b.mu.Unlock()
	return append([]byte(nil), b.buf.Bytes()...), b.truncated
}

func (r *Runner) executeInputProfile(ctx context.Context, args []string, mode outputMode, identity string, input []byte, includeProfile bool) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, cliError(KindTimeout, err)
	}
	profile, err := r.resolveProfile(ctx)
	if err != nil {
		return nil, err
	}
	bin, err := r.binary(ctx)
	if err != nil {
		return nil, err
	}
	argv := args
	if includeProfile {
		argv = profileArgs(profile, args)
	}
	switch mode {
	case outputAuth:
		argv = append(argv, "--json")
	case outputJSON:
		if identity == "" {
			identity = "user"
		}
		argv = append(argv, "--as", identity, "--format", "json")
	}

	timeout := r.cfg.Timeout
	if timeout <= 0 {
		timeout = defaultTimeout
	}
	cctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	cmd := exec.CommandContext(cctx, bin, argv...)
	cmd.Env = cliEnv(bin)
	cmd.SysProcAttr = osproc.SessionGroupAttr()
	var procID atomic.Pointer[osproc.ProcIdentity]
	cmd.Cancel = func() error {
		return stopProcessGroup(cmd.Process, &procID)
	}
	cmd.WaitDelay = time.Second
	stdout := &cappedBuffer{limit: maxCommandStdout}
	stderr := &cappedBuffer{limit: maxCommandStderr}
	cmd.Stdout = stdout
	cmd.Stderr = stderr
	if input != nil {
		cmd.Stdin = bytes.NewReader(input)
	}
	if err = cmd.Start(); err != nil {
		if cctx.Err() != nil {
			return nil, cliError(KindTimeout, cctx.Err())
		}
		return nil, cliError(KindOther, err)
	}
	id := osproc.Identify(cmd.Process.Pid)
	procID.Store(&id)
	err = cmd.Wait()
	if errors.Is(err, exec.ErrWaitDelay) && cctx.Err() == nil {
		cleanupWaitDelayGroup(cmd.Process, &procID)
		err = nil
	}
	stdoutBytes, stdoutTruncated := stdout.bytes()
	stderrBytes, _ := stderr.bytes()
	result := commandResult{stdout: stdoutBytes, stderr: stderrBytes}
	if cctx.Err() != nil {
		return nil, cliError(KindTimeout, cctx.Err())
	}
	if err != nil {
		if classified := classifyFailure(result); classified != nil {
			if mode != outputAuth && signedOut(classified.Kind) {
				r.invalidateAuth()
			}
			return nil, classified
		}
		return nil, cliError(KindOther, err)
	}
	if stdoutTruncated {
		return nil, cliError(KindOther, errors.New("lark-cli output exceeded limit"))
	}
	return result.stdout, nil
}

func parseConfigInfo(raw []byte) ConfigInfo {
	var value any
	if json.Unmarshal(raw, &value) == nil {
		var out ConfigInfo
		findConfigFields(value, &out)
		if out.AppID != "" {
			return out
		}
	}
	return ConfigInfo{AppID: firstRegex(raw, configAppIDPattern), Brand: firstRegex(raw, configBrandPattern)}
}

func findConfigFields(value any, out *ConfigInfo) {
	switch v := value.(type) {
	case map[string]any:
		for key, value := range v {
			switch strings.ToLower(strings.ReplaceAll(key, "_", "")) {
			case "appid":
				if s, ok := value.(string); ok && out.AppID == "" {
					out.AppID = strings.TrimSpace(s)
				}
			case "brand":
				if s, ok := value.(string); ok && out.Brand == "" {
					out.Brand = strings.TrimSpace(s)
				}
			}
			findConfigFields(value, out)
		}
	case []any:
		for _, item := range v {
			findConfigFields(item, out)
		}
	}
}

var (
	configAppIDPattern = lazyre.New(`(?i)["']?app[_-]?id["']?\s*[:=]\s*["']?([A-Za-z0-9_.-]+)`)
	configBrandPattern = lazyre.New(`(?i)["']?brand["']?\s*[:=]\s*["']?([A-Za-z0-9_-]+)`)
	loginURLPattern    = lazyre.New(`https://[^\s"'<>]+`)
)

func firstRegex(raw []byte, re *lazyre.Regexp) string {
	match := re.FindSubmatch(raw)
	if len(match) == 2 {
		return strings.TrimRight(string(match[1]), ".,;)")
	}
	return ""
}

func hasControlChar(s string) bool {
	for _, r := range s {
		if r < 0x20 || r == 0x7f {
			return true
		}
	}
	return false
}

func parseLoginInfo(line string) (LoginInfo, bool) {
	for _, candidate := range loginURLPattern.FindAllString(line, -1) {
		candidate = strings.TrimRight(candidate, ".,;)")
		if hasControlChar(candidate) {
			continue
		}
		u, err := url.Parse(candidate)
		if err != nil || u.Scheme != "https" || u.Hostname() == "" || strings.Contains(u.Host, "\\") || u.User != nil {
			continue
		}
		code := u.Query().Get("user_code")
		if code == "" {
			continue
		}
		return LoginInfo{URL: candidate, UserCode: code}, true
	}
	return LoginInfo{}, false
}

func (r *Runner) binary(ctx context.Context) (string, error) {
	r.mu.Lock()
	if r.path != "" {
		path := r.path
		r.mu.Unlock()
		return path, nil
	}
	r.mu.Unlock()
	bin := r.cfg.Bin
	if bin == "" {
		bin = "lark-cli"
	}
	if hasControlChar(bin) {
		return "", cliError(KindNotInstalled, errors.New("invalid lark-cli binary path"))
	}
	if filepath.IsAbs(bin) {
		if fi, err := os.Stat(bin); err != nil || fi.IsDir() {
			return "", cliError(KindNotInstalled, err)
		}
		r.mu.Lock()
		r.path = bin
		r.mu.Unlock()
		return bin, nil
	}
	path, err := exec.LookPath(bin)
	if err != nil {
		// npm global installs are often visible only in an interactive login shell.
		lookupCtx, cancel := context.WithTimeout(ctx, minDuration(r.cfg.Timeout, 5*time.Second))
		defer cancel()
		cmd := exec.CommandContext(lookupCtx, loginShell(), "-lc", "command -v -- "+shellQuote(bin))
		cmd.SysProcAttr = osproc.SessionGroupAttr()
		var procID atomic.Pointer[osproc.ProcIdentity]
		cmd.Cancel = func() error {
			return stopProcessGroup(cmd.Process, &procID)
		}
		cmd.WaitDelay = time.Second
		outBuf := &cappedBuffer{limit: 64 << 10}
		cmd.Stdout = outBuf
		if startErr := cmd.Start(); startErr == nil {
			id := osproc.Identify(cmd.Process.Pid)
			procID.Store(&id)
			waitErr := cmd.Wait()
			if errors.Is(waitErr, exec.ErrWaitDelay) && lookupCtx.Err() == nil {
				cleanupWaitDelayGroup(cmd.Process, &procID)
				waitErr = nil
			}
			if waitErr == nil {
				outBytes, truncated := outBuf.bytes()
				if !truncated {
					lines := strings.Split(strings.TrimSpace(string(outBytes)), "\n")
					if len(lines) > 0 {
						cand := strings.TrimSpace(lines[len(lines)-1])
						if cand != "" && !hasControlChar(cand) && filepath.IsAbs(cand) {
							if fi, statErr := os.Stat(cand); statErr == nil && !fi.IsDir() {
								path = cand
							}
						}
					}
				}
			}
		}
	}
	if path == "" {
		return "", cliError(KindNotInstalled, err)
	}
	r.mu.Lock()
	r.path = path
	r.mu.Unlock()
	return path, nil
}

func cliEnv(bin string) []string {
	env := make([]string, 0, len(os.Environ())+1)
	for _, kv := range os.Environ() {
		if len(kv) >= 5 && strings.EqualFold(kv[:5], "PATH=") {
			continue
		}
		env = append(env, kv)
	}
	pathVal := filepath.Dir(bin)
	if cur := os.Getenv("PATH"); cur != "" {
		pathVal += string(os.PathListSeparator) + cur
	}
	return append(env, "PATH="+pathVal)
}

func loginShell() string {
	if shell := os.Getenv("SHELL"); shell != "" {
		return shell
	}
	return "/bin/sh"
}

// shellQuote quotes s safely across POSIX shells (sh/bash/zsh), fish, and csh/tcsh.
func shellQuote(s string) string {
	return "'" + strings.NewReplacer(`'`, `'\''`, `\`, `'\\'`, `!`, `'\!'`).Replace(s) + "'"
}

func minDuration(a, b time.Duration) time.Duration {
	if a <= 0 || a > b {
		return b
	}
	return a
}

var versionPattern = lazyre.New(`(?i)(?:lark-cli\s+version\s+|version\s+)([0-9]+\.[0-9]+\.[0-9]+(?:-[0-9A-Za-z.-]+)?)`)

func parseVersion(s string) string {
	match := versionPattern.FindStringSubmatch(s)
	if len(match) == 2 {
		return match[1]
	}
	return ""
}

func compareVersion(a, b string) int {
	parse := func(s string) [3]int {
		var v [3]int
		base := strings.SplitN(s, "-", 2)[0]
		parts := strings.Split(base, ".")
		for i := 0; i < len(parts) && i < len(v); i++ {
			_, _ = fmt.Sscan(parts[i], &v[i])
		}
		return v
	}
	va, vb := parse(a), parse(b)
	for i := range va {
		if va[i] < vb[i] {
			return -1
		}
		if va[i] > vb[i] {
			return 1
		}
	}
	return 0
}

func decodeData(raw []byte, out any) error {
	data, err := unwrapData(raw)
	if err != nil {
		return err
	}
	return json.Unmarshal(data, out)
}

func unwrapData(raw []byte) (json.RawMessage, error) {
	var env map[string]json.RawMessage
	if err := json.Unmarshal(raw, &env); err != nil {
		return nil, err
	}
	if data, ok := env["data"]; ok && len(data) > 0 && string(data) != "null" {
		return data, nil
	}
	return raw, nil
}

func classifyFailure(result commandResult) *Error {
	for _, raw := range [][]byte{result.stdout, result.stderr} {
		if len(raw) == 0 {
			continue
		}
		var env struct {
			Code  json.RawMessage `json:"code"`
			Msg   string          `json:"msg"`
			Error struct {
				Code           json.RawMessage `json:"code"`
				Type           string          `json:"type"`
				Subtype        string          `json:"subtype"`
				Message        string          `json:"message"`
				MissingScopes  []string        `json:"missing_scopes"`
				MissingScopes2 []string        `json:"missingScopes"`
			} `json:"error"`
		}
		if json.Unmarshal(raw, &env) != nil {
			continue
		}
		words := strings.ToLower(env.Msg + " " + env.Error.Message + " " + env.Error.Subtype)
		if strings.Trim(string(env.Code), `"`) == "230002" || strings.Trim(string(env.Error.Code), `"`) == "230002" ||
			strings.Contains(words, "bot not in the chat") || strings.Contains(words, "bot is not in the group") ||
			strings.Contains(words, "bot_not_in_chat") {
			return &Error{Kind: KindBotNotInChat}
		}
		if env.Error.Type == "" && env.Error.Subtype == "" {
			continue
		}
		sub := strings.ToLower(env.Error.Subtype)
		typ := strings.ToLower(env.Error.Type)
		scopes := append(env.Error.MissingScopes, env.Error.MissingScopes2...)
		if len(scopes) == 0 {
			scopes = scopesFromMessage(env.Error.Message)
		}
		switch {
		case sub == "missing_scope" || sub == "permission_denied" || sub == "forbidden" || sub == "not_found" || sub == "resource_not_found" || sub == "document_not_found" || typ == "permission":
			return &Error{Kind: KindMissingPermission, MissingScopes: scopes}
		case sub == "rate_limited" || sub == "too_many_requests" || typ == "rate_limit":
			return &Error{Kind: KindRateLimited}
		case sub == "token_expired" || sub == "expired" || sub == "not_logged_in" || sub == "unauthorized" || typ == "authentication":
			if sub == "token_expired" || sub == "expired" {
				return cliError(KindExpired, nil)
			}
			return cliError(KindNotLoggedIn, nil)
		}
	}
	text := strings.ToLower(string(result.stdout) + "\n" + string(result.stderr))
	switch {
	case strings.Contains(text, "missing scope") || strings.Contains(text, "missing_scope"):
		return &Error{Kind: KindMissingPermission, MissingScopes: scopesFromMessage(text)}
	case strings.Contains(text, "rate limit") || strings.Contains(text, "too many requests"):
		return &Error{Kind: KindRateLimited}
	case strings.Contains(text, "token expired") || strings.Contains(text, "expired"):
		return &Error{Kind: KindExpired}
	case strings.Contains(text, "not found") || strings.Contains(text, "does not exist"):
		return &Error{Kind: KindMissingPermission}
	case strings.Contains(text, "not logged in") || strings.Contains(text, "not signed in"):
		return &Error{Kind: KindNotLoggedIn}
	}
	return nil
}

func scopesFromMessage(message string) []string {
	lower := strings.ToLower(message)
	const marker = "missing required scope(s):"
	index := strings.Index(lower, marker)
	if index < 0 {
		return nil
	}
	value := message[index+len(marker):]
	if end := strings.IndexAny(value, "\r\n"); end >= 0 {
		value = value[:end]
	}
	if end := strings.IndexAny(value, "\"}"); end >= 0 {
		value = value[:end]
	}
	value = strings.TrimSpace(value)
	if value == "" {
		return nil
	}
	parts := strings.FieldsFunc(value, func(r rune) bool { return r == ',' || r == ' ' || r == '\t' })
	return uniqueStrings(parts)
}
