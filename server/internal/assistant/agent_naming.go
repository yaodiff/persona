package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"path"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"golang.org/x/sync/singleflight"

	"github.com/nwverse/persona/server/internal/ai"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/prompts"
	"github.com/nwverse/persona/server/internal/proto"
)

const agentNamingKey = "agent.naming.v1"
const agentNamingFirstRunes = 2000
const agentNamingRecentRunes = 4000
const agentNamingTitleRunes = 40
const agentNamingFailureLimit = 3

var agentNamingTimeout = 45 * time.Second

// A distinct manager-owned ledger preserves counters and manual intent across restarts.
// Keys encode both opaque identifiers, rather than joining them with a delimiter.
type agentNameRecord struct {
	Machine     string    `json:"machine"`
	Session     string    `json:"session"`
	Title       string    `json:"title,omitempty"`
	RawTitle    string    `json:"raw_title,omitempty"`
	Manual      bool      `json:"manual,omitempty"`
	Changes     int       `json:"changes,omitempty"`
	NamedAt     time.Time `json:"named_at,omitempty"`
	NamedOutput int       `json:"named_output,omitempty"`
	Failures    int       `json:"failures,omitempty"`
	LastFailure string    `json:"last_failure,omitempty"`
	NextAttempt time.Time `json:"next_attempt,omitempty"`
	// Requested: the user asked for this automatic name (agent_naming_request.go). It also covers a custom title
	// and a title of unknown provenance, until the native title changes or the user names the session by hand.
	Requested bool `json:"requested,omitempty"`
}

type agentNamingBook struct {
	mu        sync.Mutex
	loaded    bool
	records   map[string]agentNameRecord
	display   map[[2]string]agentNameRecord
	pending   map[string][]proto.Session
	running   bool
	scanAfter time.Time
	interval  time.Duration // zero is the production pass interval; tests use a shorter queue delay
	timer     *time.Timer
	timerSeq  uint64
	requests  singleflight.Group // one model call per session for requested names (agent_naming_request.go)
	// beforeRequest lets a concurrency test observe every caller reaching the singleflight gate; nil in production.
	beforeRequest func()
}

func agentNameKey(machine, session string) string {
	b, _ := json.Marshal([2]string{machine, session})
	return string(b)
}

// Terminal animation is not a native rename. Share this presentation-only
// normalization with candidate recognition and both native-title rechecks.
func agentTitleText(title string) string {
	return strings.TrimLeftFunc(strings.TrimSpace(title), func(r rune) bool {
		return unicode.IsSpace(r) || strings.ContainsRune("◐◑◒◓⠋⠙⠹⠸⠼⠴⠦⠧⠇⠏", r)
	})
}

func sameAgentRawTitle(a, b string) bool {
	return a == b || agentTitleText(a) == agentTitleText(b)
}

// Claude's prompt provenance proves that the title is its first-message fallback,
// even when that message does not start with a known instruction template.
// Spinner/tool and prompt presentations are not native renames.
func sameAgentNamingTitle(before string, current proto.Session) bool {
	if sameAgentRawTitle(before, current.Title) {
		return true
	}
	if current.Agent != "claude" || current.TitleSource != proto.TitleSourcePrompt {
		return false
	}
	return claudePromptFallback(before) && claudePromptFallback(current.Title)
}

func claudePromptFallback(title string) bool {
	switch strings.ToLower(agentTitleText(title)) {
	case "codex", "opencode", "pi", "persona":
		return false // a different runtime/tool is not Claude's prompt fallback
	}
	return true
}

// Prompt provenance identifies copied or truncated first user messages without
// reading every transcript on the list path. Auto/runtime titles need text evidence.
func badAgentSessionName(c proto.Session) bool {
	return badAgentName(c.Title, c.Cwd) || c.TitleSource == proto.TitleSourcePrompt && claudePromptFallback(c.Title)
}

// Bad names are recognizable placeholders or instruction scaffolding, not arbitrary
// short names. Meaningful-but-stale names require opting into periodic refresh.
func badAgentName(title, cwd string) bool {
	title = strings.TrimSpace(title)
	plain := agentTitleText(title)
	if utf8.RuneCountInString(plain) > agentNamingTitleRunes {
		return true
	}
	low := strings.ToLower(plain)
	// i18n-ignore: Observed native placeholder labels, matched as data rather than UI copy.
	for _, name := range []string{"", "claude", "claude code", "codex", "opencode", "pi", "persona", "guest", "new chat", "untitled", "new session", "新对话", "新会话", "無題"} {
		if low == name {
			return true
		}
	}
	if cwd != "" && plain == path.Base(strings.TrimRight(cwd, "/")) {
		return true
	}
	// i18n-ignore: Instruction-template openings must match the original text.
	for _, prefix := range []string{
		"用中文工作", "用中文汇报", "请全程", "请用", "请检查", "请帮", "请在", "请给", "请把", "请将", "请对", "请按", "请修", "请提供", "请执行", "请完成", "请实现", "请查", "你是", "你需要", "帮我", "帮忙", "参考已有", "这个如果", "执行一次", "读 ", "任务：", "任务:", "## 需求", "## 任务", "这是一次只读", "这是一次技术验收", // i18n-ignore: match instruction-template data
		"/loop", "/goal", "<system-reminder", "<persona-context", "ultrathink", "ultracode",
		"work in chinese", "please ", "you are ", "you need to ", "could you ", "can you ", "task:", "## task", "this is a read-only", "日本語で作業", "中国語で作業", // i18n-ignore: match instruction-template data
	} {
		if strings.HasPrefix(low, strings.ToLower(prefix)) {
			return true
		}
	}
	return strings.ContainsAny(title, "\r\n") || strings.HasPrefix(title, "## ")
}

func validAgentName(title string) bool {
	if title == "" || utf8.RuneCountInString(title) > agentNamingTitleRunes || badAgentName(title, "") {
		return false
	}
	if strings.ContainsAny(title, "\r\n<>`#") {
		return false
	}
	hasWords := false
	for _, r := range title {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			hasWords = true
		}
		if unicode.IsControl(r) || strings.ContainsRune("◐◑◒◓⠋⠙⠹⠸⠼⠴⠦⠧⠇⠏", r) {
			return false
		}
	}
	return hasWords
}

func knownAgentTitle(c proto.Session) bool {
	return c.TitleSource.String() == "auto" || c.TitleSource.String() == "prompt" || c.TitleSource.String() == "runtime"
}

func agentNameDue(v Settings, r agentNameRecord, c proto.Session, now time.Time) bool {
	if !quietOn(v.AgentAutoNaming) || c.Assistant || c.Archived || c.SessionID == "" || c.TitleManual || c.TitleSource.String() == "manual" ||
		r.Manual || !knownAgentTitle(c) || r.Changes >= agentNamingLimit(v) || r.Failures >= agentNamingFailureLimit || now.Before(r.NextAttempt) {
		return false
	}
	if r.Changes == 0 && badAgentSessionName(c) && (r.Title == "" || !sameAgentNamingTitle(r.RawTitle, c)) {
		return true
	}
	hours := agentNamingInterval(v)
	return hours > 0 && !r.NamedAt.IsZero() && now.Sub(r.NamedAt) >= time.Duration(hours)*time.Hour && c.OutputTokens > r.NamedOutput &&
		c.Status == proto.StatusWorking && !c.UpdatedAt.IsZero() && now.Sub(c.UpdatedAt) < 5*time.Minute
}

func (s *Service) agentNamesLocked(ctx context.Context) bool {
	b := &s.agentNames
	if b.loaded {
		return true
	}
	m := map[string]agentNameRecord{}
	if st := s.store(); st != nil {
		if _, err := st.Setting(ctx, agentNamingKey, &m); err != nil {
			s.logf("agent naming: read ledger: %v", err)
			return false
		}
	}
	if m == nil {
		m = map[string]agentNameRecord{}
	}
	b.records, b.loaded = m, true
	b.display = make(map[[2]string]agentNameRecord, len(m))
	for _, rec := range m {
		b.display[[2]string{rec.Machine, rec.Session}] = rec
	}
	return true
}

// Publish memory only after durable storage accepts the new ledger.
func (s *Service) putAgentNameLocked(ctx context.Context, key string, rec agentNameRecord) error {
	b := &s.agentNames
	next := make(map[string]agentNameRecord, len(b.records)+1)
	for k, v := range b.records {
		next[k] = v
	}
	next[key] = rec
	if st := s.store(); st != nil {
		if err := st.PutSetting(ctx, agentNamingKey, next); err != nil {
			return err
		}
	}
	b.records = next
	b.display[[2]string{rec.Machine, rec.Session}] = rec
	return nil
}

// List delivery only wakes one coalescing worker; it never waits for disk or a model.
// Queue-local one-shot timers drain existing bad names even without another node event.
// They stop once no candidate is due; periodic refresh still requires new node output.
func (s *Service) agentNamesChanged(d DeviceSessions) {
	if s.runCtx().Err() != nil {
		return
	}
	b := &s.agentNames
	b.mu.Lock()
	if b.pending == nil {
		b.pending = map[string][]proto.Session{}
	}
	if d.MachineID != "" {
		b.pending[d.MachineID] = d.Sessions
	}
	if b.running {
		b.mu.Unlock()
		return
	}
	if time.Now().Before(b.scanAfter) {
		s.armAgentNamesLocked()
		b.mu.Unlock()
		return
	}
	if b.timer != nil {
		b.timer.Stop()
		b.timer = nil
		b.timerSeq++
	}
	b.running = true
	pending := b.pending
	b.pending = map[string][]proto.Session{}
	interval := b.interval
	if interval <= 0 {
		interval = 30 * time.Second
	}
	b.scanAfter = time.Now().Add(interval)
	b.mu.Unlock()
	go func() {
		attempted := false
		defer func() {
			b.mu.Lock()
			if attempted {
				for mid, list := range pending {
					if _, newer := b.pending[mid]; !newer {
						b.pending[mid] = list
					}
				}
			}
			b.running = false
			s.armAgentNamesLocked()
			b.mu.Unlock()
		}()
		ctx, cancel := context.WithTimeout(s.runCtx(), 2*time.Minute)
		defer cancel()
		prefs, ok := s.savedSettings(ctx)
		if !ok || !quietOn(prefs.AgentAutoNaming) {
			return
		}
		machines := make([]string, 0, len(pending))
		for mid := range pending {
			machines = append(machines, mid)
		}
		sort.Strings(machines)
		for _, mid := range machines {
			for _, c := range pending[mid] {
				if c.Assistant || c.Archived || !knownAgentTitle(c) {
					continue
				}
				if s.tryAgentName(ctx, mid, c, time.Now()) {
					attempted = true
					return
				}
			}
		}
	}()
}

// b.mu is held. A generation retires an already-firing timer when a newer list starts the pass.
func (s *Service) armAgentNamesLocked() {
	b := &s.agentNames
	if b.timer != nil || len(b.pending) == 0 || s.runCtx().Err() != nil {
		return
	}
	b.timerSeq++
	generation := b.timerSeq
	b.timer = time.AfterFunc(max(time.Until(b.scanAfter), 0), func() {
		b.mu.Lock()
		if generation != b.timerSeq {
			b.mu.Unlock()
			return
		}
		b.timer = nil
		b.mu.Unlock()
		s.agentNamesChanged(DeviceSessions{})
	})
}

func (s *Service) tryAgentName(ctx context.Context, machine string, c proto.Session, now time.Time) bool {
	if m, found := s.fleetMachine(machine); found {
		c.Assistant = m.isAssistant(c)
	}
	if s.isMain(ctx, c.SessionID) {
		return false
	}
	prefs, ok := s.savedSettings(ctx)
	if !ok {
		return false
	}
	key := agentNameKey(machine, c.SessionID)
	b := &s.agentNames
	b.mu.Lock()
	if !s.agentNamesLocked(ctx) {
		b.mu.Unlock()
		return false
	}
	r := b.records[key]
	if c.TitleManual || c.TitleSource.String() == "manual" {
		// A name the user asked for over this custom title stays until the title changes again.
		if !r.Manual && r.Title != "" && !(r.Requested && sameAgentRawTitle(r.RawTitle, c.Title)) {
			r.Manual = true
			r.Requested = false
			r.Title = ""
			_ = s.putAgentNameLocked(ctx, key, r)
		}
		b.mu.Unlock()
		return false
	}
	if quietOn(prefs.AgentAutoNaming) && !c.Assistant && !c.Archived && knownAgentTitle(c) && !r.Manual && r.NamedAt.IsZero() && agentNamingInterval(prefs) > 0 && !badAgentSessionName(c) {
		r.Machine, r.Session = machine, c.SessionID
		r.NamedAt = now
		r.NamedOutput = c.OutputTokens
		_ = s.putAgentNameLocked(ctx, key, r)
		b.mu.Unlock()
		return false
	}
	if !agentNameDue(prefs, r, c, now) {
		b.mu.Unlock()
		return false
	}
	r.Machine, r.Session = machine, c.SessionID
	// Lease before dispatch: a redeploy during the request cannot trigger a call storm.
	r.NextAttempt = now.Add(10 * time.Minute)
	if err := s.putAgentNameLocked(ctx, key, r); err != nil {
		b.mu.Unlock()
		s.logf("agent naming: session=%q/%q reason=lease_write_failed", machine, c.SessionID)
		return false
	}
	b.mu.Unlock()
	callCtx, cancel := context.WithTimeout(ctx, agentNamingTimeout)
	defer cancel()
	title, failureReason := s.agentNameFromModel(callCtx, machine, c.SessionID)
	if failureReason != "" {
		s.logf("agent naming: session=%q/%q reason=%s", machine, c.SessionID, failureReason)
	}
	// Observe the raw node row, not our decorated title; a native /rename wins immediately.
	current, found := s.rawAgentForNaming(ctx, machine, c.SessionID)
	prefs, settingsOK := s.savedSettings(ctx)
	b.mu.Lock()
	defer b.mu.Unlock()
	r = b.records[key]
	if r.Manual || !settingsOK || !quietOn(prefs.AgentAutoNaming) || r.Changes >= agentNamingLimit(prefs) || !found || current.Assistant || current.Archived || current.TitleManual || current.TitleSource.String() == "manual" || !knownAgentTitle(current) || !sameAgentNamingTitle(c.Title, current) {
		return true
	}
	check := r
	check.NextAttempt = time.Time{}
	if !agentNameDue(prefs, check, current, time.Now()) {
		return true
	}
	if failureReason != "" {
		r.Failures++
		r.LastFailure = failureReason
		r.NextAttempt = now.Add(time.Duration(10*(1<<min(r.Failures-1, 5))) * time.Minute)
		if saveErr := s.putAgentNameLocked(ctx, key, r); saveErr != nil {
			s.logf("agent naming: session=%q/%q reason=failure_record_write_failed", machine, c.SessionID)
		}
		return true
	}
	if r.Changes > 0 && agentNamingInterval(prefs) == 0 {
		return true
	}
	if title == r.Title || r.Title == "" && title == current.Title {
		// A successful check that keeps the same name still advances the periodic
		// baseline. Otherwise a daily setting would call again every hour.
		// Keep proof that this prompt fallback was checked, without counting a
		// title change. Otherwise an already concise first message retries forever.
		r.Title, r.RawTitle, r.Requested = title, current.Title, false
		r.NamedAt, r.NamedOutput, r.Failures, r.NextAttempt = now, c.OutputTokens, 0, time.Time{}
		r.LastFailure = ""
		if err := s.putAgentNameLocked(ctx, key, r); err != nil {
			s.logf("agent naming: session=%q/%q reason=result_write_failed", machine, c.SessionID)
		}
		return true
	}
	r.Title, r.RawTitle, r.Requested = title, current.Title, false
	r.Changes++
	r.Failures = 0
	r.LastFailure = ""
	r.NamedAt = now
	r.NamedOutput = c.OutputTokens
	r.NextAttempt = time.Time{}
	if err := s.putAgentNameLocked(ctx, key, r); err != nil {
		s.logf("agent naming: session=%q/%q reason=result_write_failed", machine, c.SessionID)
		return true
	}
	// Publishing takes the ledger lock to decorate, so release it before the asynchronous publication.
	go s.publishSessions(machine)
	return true
}

// agentNameFromModel asks the naming model for session id's name: the dialogue, the prompt and the validity check
// the background pass and a requested name share. reason is "" with a usable title, else a static code. Provider
// errors may contain credentials or echoed private text, so only the code leaves here, for the log, the ledger and
// the app; the caller logs it, even if a later native-title recheck discards the result.
func (s *Service) agentNameFromModel(ctx context.Context, machine, id string) (title, reason string) {
	first, recent, err := s.agentNameDialogue(ctx, machine, id)
	reason = "dialogue_unavailable"
	var reply ai.Reply
	if err == nil && first == "" && recent == "" {
		err = errors.New("no dialogue")
		reason = "no_dialogue"
	}
	if err == nil {
		reason = "model_call_failed"
		reply, err = s.purposeModel("inbox.triage").Chat(ctx, ai.Request{System: prompts.RenderAgentSessionName(), Messages: []ai.Message{{Role: ai.RoleUser, Content: prompts.RenderAgentSessionNameInput(prompts.AgentSessionNameInput{First: first, Recent: recent})}}, MaxTokens: 256, BoundOutput: true})
		if ai.IsNotConfigured(err) {
			reason = "model_not_configured"
		}
	}
	title = strings.TrimSpace(strings.Trim(strings.TrimSpace(reply.Text), "\"“”『』")) // i18n-ignore: punctuation normalization
	if err == nil && (reply.Truncated || !validAgentName(title)) {
		err = errors.New("invalid name")
		reason = "invalid_name"
		if reply.Truncated {
			reason = "model_output_truncated"
		}
	}
	if err == nil {
		return title, ""
	}
	if errors.Is(ctx.Err(), context.DeadlineExceeded) {
		reason = "naming_timeout"
	} else if errors.Is(ctx.Err(), context.Canceled) {
		reason = "naming_canceled"
	}
	return "", reason
}

func (s *Service) rawAgentForNaming(ctx context.Context, machine, id string) (proto.Session, bool) {
	if s.fleetOn() {
		c, ok := s.nodeSession(machine, id)
		if m, found := s.fleetMachine(machine); found {
			c.Assistant = m.isAssistant(c)
		}
		return c, ok
	}
	return s.session(ctx, machine, id)
}

func (s *Service) agentNameDialogue(ctx context.Context, machine, id string) (string, string, error) {
	var head, tail struct {
		Items []proto.Item `json:"items"`
	}
	if err := s.call(ctx, machine, DeviceSessionItems, map[string]any{"session_id": id, "after_seq": 0, "limit": 80, "lazy_images": true, "fold_work": true}, &head); err != nil {
		return "", "", err
	}
	first := ""
	for _, it := range head.Items {
		if it.Role == "user" && it.Kind == proto.KindText && !it.Queued && strings.TrimSpace(it.Text) != "" {
			first = cutRunes(it.Text, agentNamingFirstRunes)
			break
		}
	}
	if err := s.call(ctx, machine, DeviceSessionItems, map[string]any{"session_id": id, "turns": 2, "lazy_images": true, "fold_work": true}, &tail); err != nil {
		return "", "", err
	}
	lines := []string{}
	budget := agentNamingRecentRunes
	for i := len(tail.Items) - 1; i >= 0 && budget > 0; i-- {
		it := tail.Items[i]
		if it.Kind != proto.KindText || it.Queued || (it.Role != "user" && it.Role != "assistant") || strings.TrimSpace(it.Text) == "" {
			continue
		}
		prefix := it.Role + ": "
		room := budget - utf8.RuneCountInString(prefix) - 1 // include the joining newline
		if room <= 0 {
			break
		}
		text := cutRunes(it.Text, min(room, 1500))
		lines = append(lines, prefix+text)
		budget -= utf8.RuneCountInString(prefix) + utf8.RuneCountInString(text) + 1
	}
	for i, j := 0, len(lines)-1; i < j; i, j = i+1, j-1 {
		lines[i], lines[j] = lines[j], lines[i]
	}
	return first, strings.Join(lines, "\n"), nil
}

func (s *Service) agentDisplayName(machine string, c proto.Session) string {
	title, _ := s.agentDisplay(machine, c)
	return title
}

// agentDisplay is the manager's name for the row ("" for none) and whether the user asked for it as an automatic
// name: that one stands over a custom title too, so the row's provenance is then the manager's to state.
func (s *Service) agentDisplay(machine string, c proto.Session) (title string, requested bool) {
	if c.Assistant {
		return "", false
	}
	b := &s.agentNames
	b.mu.Lock()
	defer b.mu.Unlock()
	if !s.agentNamesLocked(context.Background()) {
		return "", false
	}
	r := b.display[[2]string{machine, c.SessionID}]
	if r.Requested && !r.Manual {
		if !sameAgentNamingTitle(r.RawTitle, c) {
			return "", false // the native title changed since: it wins
		}
		return r.Title, r.Title != ""
	}
	if c.TitleManual || c.TitleSource.String() == "manual" {
		return "", false
	}
	if !r.Manual && (!knownAgentTitle(c) || !sameAgentNamingTitle(r.RawTitle, c)) {
		return "", false
	}
	return r.Title, false
}

// nameAgentRow puts the manager's name on a row the apps get. A name the user asked for is an automatic one whatever
// the native title's provenance was.
func (s *Service) nameAgentRow(machine string, c *proto.Session) {
	title, requested := s.agentDisplay(machine, *c)
	if title == "" {
		return
	}
	c.Title = title
	if requested {
		c.TitleSource, c.TitleManual = proto.TitleSourceAuto, false
	}
}

// Manual intent is recorded before contacting the node. A refused/uncertain rename
// still stops background renaming; the prior displayed name is preserved on failure.
func (s *Service) renameAgent(ctx context.Context, p RenameParams, c proto.Session) (json.RawMessage, error) {
	title, _ := p.Params["title"].(string)
	key := agentNameKey(p.MachineID, p.SessionID)
	b := &s.agentNames
	if strings.TrimSpace(title) != "" {
		b.mu.Lock()
		if !s.agentNamesLocked(ctx) {
			b.mu.Unlock()
			return nil, i18n.Err("assistant-profile.agent_names_unavailable")
		}
		r := b.records[key]
		r.Machine, r.Session = p.MachineID, p.SessionID
		r.Manual, r.Requested = true, false
		if err := s.putAgentNameLocked(ctx, key, r); err != nil {
			b.mu.Unlock()
			return nil, err
		}
		b.mu.Unlock()
	}
	params := p.Params
	params["session_id"] = p.SessionID
	out := json.RawMessage(`{"ok":true}`)
	if params["title"] != nil || params["tab_name"] != nil {
		if err := s.call(ctx, p.MachineID, DeviceSessionRename, params, &out); err != nil {
			return nil, err
		}
	}
	if strings.TrimSpace(title) != "" {
		b.mu.Lock()
		r := b.records[key]
		r.Title = title
		r.RawTitle = c.Title
		r.Manual, r.Requested = true, false // also when a requested name landed while the node was renaming
		err := s.putAgentNameLocked(ctx, key, r)
		b.mu.Unlock()
		if err != nil {
			return nil, err
		}
		s.publishSessions(p.MachineID)
	}
	return out, nil
}
