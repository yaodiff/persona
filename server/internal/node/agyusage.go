package node

import (
	"bytes"
	"errors"
	"math"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/transcript"
)

// Antigravity (agy) transcript token accounting and model context window resolution (#12540).
// Native agy transcripts emit model, input_tokens (exclusive of cache_read_tokens), output_tokens,
// and cache_read_tokens on PLANNER_RESPONSE steps, and compact context on CHECKPOINT steps.

const agyMinCatalogWindow = 1024

var agyModelWindowCache = struct {
	sync.Mutex
	byID map[string]int
}{byID: map[string]int{}}

type agyMetaEntry struct {
	file os.FileInfo
	path string
	meta agySessionMeta
}

type agySessionMeta struct {
	agyTranscriptUsage
	background agyBackgroundTasks
}

func firstPositive64(vals ...int64) int64 {
	for _, v := range vals {
		if v > 0 {
			return v
		}
	}
	return 0
}

func firstPositiveInt(vals ...int) int {
	for _, v := range vals {
		if v > 0 {
			return v
		}
	}
	return 0
}

func clampInt64ToInt(v int64) int {
	if v <= 0 {
		return 0
	}
	if v > math.MaxInt {
		return math.MaxInt
	}
	return int(v)
}

func addSaturatingInt(a int, b int64) int {
	if b <= 0 {
		return a
	}
	if b > math.MaxInt-int64(a) {
		return math.MaxInt
	}
	return a + int(b)
}

func (s agyStep) normalizedModel() string {
	m := strings.TrimSpace(s.Model)
	if m == "" || isBracketedModel(m) {
		return ""
	}
	return m
}

// tokenUsage extracts per-request token counts and an optional recorded context window from a single agy step.
// agy reports input_tokens exclusive of cache_read_tokens (as in modelrun.go), matching proto.TokenUsage.Input.
func (s agyStep) tokenUsage() (usage proto.TokenUsage, contextTokens int, contextWindow int, ok bool) {
	var u agyStepUsage
	if s.Usage != nil {
		u = *s.Usage
	}
	in := firstPositive64(s.InputTokens, u.InputTokens)
	out := firstPositive64(s.OutputTokens, u.OutputTokens)
	cacheRead := firstPositive64(s.CacheReadTokens, s.CacheReadInputTokens, u.CacheReadTokens, u.CacheReadInputTokens)
	cacheWrite := firstPositive64(s.CacheWriteTokens, s.CacheCreationInputTokens, u.CacheWriteTokens, u.CacheCreationInputTokens)
	cacheWrite1h := firstPositive64(s.CacheWrite1hTokens, u.CacheWrite1hTokens)
	if cacheWrite1h > cacheWrite {
		cacheWrite = cacheWrite1h
	}
	contextWindow = firstPositiveInt(s.ContextWindow, s.ModelContextWindow, u.ContextWindow, u.ModelContextWindow)
	if in == 0 && out == 0 && cacheRead == 0 && cacheWrite == 0 {
		return proto.TokenUsage{}, 0, contextWindow, false
	}
	usage = proto.TokenUsage{
		Model:        s.normalizedModel(),
		Input:        in,
		Output:       out,
		CacheRead:    cacheRead,
		CacheWrite:   cacheWrite,
		CacheWrite1h: cacheWrite1h,
	}
	contextTokens = clampInt64ToInt(in + cacheRead + cacheWrite)
	return usage, contextTokens, contextWindow, true
}

func (u *agyTranscriptUsage) observeCheckpoint() {
	u.compactions++
	u.contextTokens = 0
	u.compacted = true
	u.contextStats.ResetCompacted()
}

func (u *agyTranscriptUsage) observeContextStep(step agyStep) {
	switch step.Type {
	case "SYSTEM_MESSAGE", "CORTEX_STEP_TYPE_SYSTEM_MESSAGE":
		u.contextStats.SystemBytes += transcript.SampleContextBytes(step.Content)
	case "USER_INPUT", "PLANNER_RESPONSE":
		u.contextStats.ConversationBytes += transcript.SampleContextBytes(step.Content) + transcript.SampleContextBytes(step.Thinking)
		for _, call := range step.ToolCalls {
			if call != nil {
				u.contextStats.ToolsBytes += transcript.SampleContextBytes(call.Name) + transcript.SampleContextBytes(call.Args)
			}
		}
	case "GENERIC":
		u.contextStats.ToolOutputBytes += transcript.SampleContextBytes(firstNonEmpty(step.Error, step.Content))
	}
}

func (u *agyTranscriptUsage) observePlanner(step agyStep) (proto.TokenUsage, bool) {
	model := step.normalizedModel()
	if model != "" {
		if u.model != "" && !strings.EqualFold(u.model, model) {
			u.contextWindow = 0
		}
		u.model = model
	}
	usage, ctxTokens, win, ok := step.tokenUsage()
	if win > 0 {
		u.contextWindow = win
	}
	if !ok {
		return proto.TokenUsage{}, false
	}
	u.hasUsage = true
	if ctxTokens > 0 {
		u.contextTokens = ctxTokens
		u.compacted = false
	}
	if usage.Output > 0 {
		u.outputTokens = addSaturatingInt(u.outputTokens, usage.Output)
	}
	return usage, true
}

// extractAgyUsage summarizes session-level and per-step token usage from parsed agy steps without building items, and
// keeps the first message the user typed.
func extractAgyUsage(steps []agyStep) agyTranscriptUsage {
	var u agyTranscriptUsage
	for _, step := range steps {
		u.observeContextStep(step)
		switch step.Type {
		case "USER_INPUT":
			if step.Source == "" || step.Source == "USER_EXPLICIT" {
				text := agyUserText(step.Content)
				if isAgyCompactCommand(text) {
					break
				}
				u.turns++
				if u.first == "" {
					u.first = text
				}
			}
		case "CHECKPOINT":
			u.observeCheckpoint()
		case "PLANNER_RESPONSE":
			u.observePlanner(step)
		}
	}
	return u
}

// trimAgyEffortSuffix strips one trailing Antigravity effort suffix (-low, -medium, -high, -xhigh, -max)
// so catalog/provider lookups match base model IDs (e.g. gemini-4-carbon-high -> gemini-4-carbon).
func trimAgyEffortSuffix(model string) string {
	trimmed := strings.TrimSpace(model)
	if trimmed == "" {
		return ""
	}
	base := proto.TrimModelWindow(trimmed)
	lower := strings.ToLower(base)
	for _, level := range proto.EffortLevels {
		if strings.HasSuffix(lower, "-"+level) && len(base) > len(level)+1 {
			return base[:len(base)-len(level)-1]
		}
	}
	return ""
}

// parseAgyModelWindow parses an optional context window token count from an extra column of `agy models`
// (such as "1000000", "200k", "1m", or "context_window=1000000"). Arbitrary text returns 0.
func parseAgyModelWindow(raw string) int {
	s := strings.TrimSpace(raw)
	if s == "" {
		return 0
	}
	lower := strings.ToLower(s)
	for _, prefix := range []string{"context_window=", "context_window:", "context=", "context:", "window=", "window:", "ctx=", "ctx:"} {
		if after, ok := strings.CutPrefix(lower, prefix); ok {
			lower = strings.TrimSpace(after)
			break
		}
	}
	lower = strings.TrimSuffix(lower, "tokens")
	lower = strings.TrimSpace(lower)
	lower = strings.ReplaceAll(lower, ",", "")
	lower = strings.ReplaceAll(lower, "_", "")
	if lower == "" {
		return 0
	}
	mult := 1
	switch lower[len(lower)-1] {
	case 'k':
		mult = 1_000
		lower = lower[:len(lower)-1]
	case 'm':
		mult = 1_000_000
		lower = lower[:len(lower)-1]
	}
	if lower == "" {
		return 0
	}
	for _, r := range lower {
		if r < '0' || r > '9' {
			return 0
		}
	}
	n, err := strconv.Atoi(lower)
	if err != nil || n <= 0 || n > math.MaxInt/mult {
		return 0
	}
	w := n * mult
	if w < agyMinCatalogWindow {
		return 0
	}
	return w
}

func recordAgyModelWindows(windows map[string]int) {
	if len(windows) == 0 {
		return
	}
	agyModelWindowCache.Lock()
	defer agyModelWindowCache.Unlock()
	for id, w := range windows {
		key := strings.ToLower(strings.TrimSpace(id))
		if key != "" && w > 0 {
			agyModelWindowCache.byID[key] = w
		}
	}
}

func lookupAgyModelWindow(model string) int {
	agyModelWindowCache.Lock()
	defer agyModelWindowCache.Unlock()
	return lookupAgyWindowMap(agyModelWindowCache.byID, model)
}

func lookupAgyWindowMap(windows map[string]int, model string) int {
	if len(windows) == 0 {
		return 0
	}
	candidates := agyModelCandidates(model)
	for _, c := range candidates {
		key := strings.ToLower(proto.TrimModelWindow(strings.TrimSpace(c)))
		if key == "" || isBracketedModel(key) {
			continue
		}
		if w := windows[key]; w > 0 {
			return w
		}
		for i := 0; i < len(key); i++ {
			if key[i] == '/' && i+1 < len(key) {
				if w := windows[key[i+1:]]; w > 0 {
					return w
				}
			}
		}
	}
	return 0
}

func agyModelCandidates(models ...string) []string {
	var out []string
	seen := map[string]bool{}
	add := func(m string) {
		m = strings.TrimSpace(m)
		if m == "" || seen[m] {
			return
		}
		seen[m] = true
		out = append(out, m)
	}
	for _, m := range models {
		add(m)
		if stripped := trimAgyEffortSuffix(m); stripped != "" {
			add(stripped)
		}
	}
	return out
}

// agyContextWindow resolves the context window for an Antigravity session without fabricating numbers for unknown
// models: a transcript-recorded window comes first, then a managed launcher's MaxContext / Models catalog (matching
// both exact and effort-stripped IDs), then discovered agyModelList window metadata, then built-in known model rules
// for the model that answered, else 0.
func agyContextWindow(l *proto.ManagedLauncher, model string, recorded, used int, catalogLookup func(string) int, fallbackModels ...string) int {
	if recorded > 0 {
		return recorded
	}
	allModels := make([]string, 0, 1+len(fallbackModels))
	if strings.TrimSpace(model) != "" {
		allModels = append(allModels, model)
	}
	allModels = append(allModels, fallbackModels...)
	candidates := agyModelCandidates(allModels...)
	if l != nil {
		if w := launcherContextWindow(*l, used, candidates...); w > 0 {
			return w
		}
	}
	if catalogLookup != nil {
		for _, c := range candidates {
			if w := catalogLookup(c); w > 0 {
				return w
			}
		}
	}
	for _, m := range allModels {
		if w := contextWindowOf(m, 0, used); w > 0 {
			return w
		}
	}
	return 0
}

func (n *Node) agyContextWindowFor(s *proto.Session, transcriptModel string, recorded, used int) int {
	if recorded > 0 {
		return recorded
	}
	var lPtr *proto.ManagedLauncher
	var fallbacks []string
	if s != nil {
		if l, ok := n.sessionManagedLauncher(s.SessionID, s.Launcher); ok {
			lPtr = &l
		}
		if s.Model != "" && s.Model != transcriptModel {
			fallbacks = append(fallbacks, s.Model)
		}
		var requested string
		if s.RequestedModel != nil && *s.RequestedModel != "" {
			requested = *s.RequestedModel
		} else if n != nil && n.state != nil && s.SessionID != "" {
			if st, ok := n.state.get(s.SessionID); ok && st.Model != "" {
				requested = st.Model
			}
		}
		if requested != "" && requested != transcriptModel && requested != s.Model {
			fallbacks = append(fallbacks, requested)
		}
	}
	return agyContextWindow(lPtr, transcriptModel, recorded, used, lookupAgyModelWindow, fallbacks...)
}

// resolveAgyContextWindow fills missing context metadata for a companion-supplied row.
func (n *Node) resolveAgyContextWindow(s *proto.Session) {
	if s == nil {
		return
	}
	conversation, _ := strings.CutPrefix(s.SessionID, agyAgent+"-")
	n.resolveAgyContext(s, conversation)
}

func (n *Node) resolveAgyContext(s *proto.Session, conversation string) {
	if s.ContextTokens > 0 && s.ContextWindow <= 0 {
		s.ContextWindow = proto.Tokens32(n.agyContextWindowFor(s, s.Model, 0, int(s.ContextTokens)))
	}
	if s.UpdatedAt.IsZero() && conversation != "" {
		if u, ok := n.agyTranscriptMeta(conversation); ok && !u.modTime.IsZero() {
			s.UpdatedAt = u.modTime
		}
	}
	if bd := s.ContextBreakdown(); bd != nil && (bd.Source == proto.ContextBreakdownExact || s.ContextTokens > 0) {
		fillBreakdownDerived(s, bd, 0)
		return
	}
	s.SetContextBreakdown(nil)
	if s.ContextTokens <= 0 {
		return
	}
	// The companion may have filtered out a rewound tail that is still on disk. Pair the
	// local category weights with companion counts only when their observed usage agrees.
	if u, ok := n.agyTranscriptMeta(conversation); ok && proto.Tokens32(u.contextTokens) == s.ContextTokens &&
		(s.OutputTokensValue() == 0 || int32(u.outputTokens) == s.OutputTokensValue()) {
		setAgyContextBreakdown(s, u.contextStats)
	}
}

func setAgyContextBreakdown(s *proto.Session, stats transcript.ContextStats) {
	if bd := s.ContextBreakdown(); bd != nil && bd.Source == proto.ContextBreakdownExact {
		fillBreakdownDerived(s, bd, 0)
		return
	}
	if s.ContextTokens <= 0 || stats.Empty() {
		s.SetContextBreakdown(nil)
		return
	}
	// Native exports can omit model instructions and clip message/tool bodies even from step zero.
	// Scale the observed categories without assigning the unrecorded context to system and rules.
	const fullWindow = false
	s.SetContextBreakdown(transcript.BuildContextBreakdown(stats, int(s.ContextTokens), int(s.ContextWindow), 0, fullWindow))
}

// agyTranscriptMeta reads the cached token usage, background work and latest turn of an agy conversation on disk.
func (n *Node) agyTranscriptMeta(conversation string) (agySessionMeta, bool) {
	return n.agyTranscriptMetaAt(conversation, time.Now().UTC())
}

func (n *Node) agyTranscriptMetaAt(conversation string, now time.Time) (agySessionMeta, bool) {
	if n == nil || conversation == "" || !uuidRe.MatchString(conversation) {
		return agySessionMeta{}, false
	}
	dir := n.agyRootKnownFor(conversation)
	root, err := agyBrain(dir, conversation)
	if err != nil {
		return agySessionMeta{}, false
	}
	defer root.Close()
	rel := agyTranscriptPath
	info, err := root.Stat(rel)
	if errors.Is(err, os.ErrNotExist) {
		rel = agyFullTranscriptPath
		info, err = root.Stat(rel)
		if err != nil || !info.Mode().IsRegular() {
			return agySessionMeta{}, false
		}
	}
	key := filepath.Join(dir, conversation)
	n.mu.Lock()
	if n.agyMetas != nil {
		if cached, ok := n.agyMetas.get(key); ok && cached.path == rel && sameAgyFile(info, cached.file) && now.Sub(info.ModTime()) > time.Second {
			if expiry := cached.meta.background.nextExpiry; expiry.IsZero() || now.Before(expiry) {
				n.mu.Unlock()
				return cached.meta, true
			}
		}
	}
	n.mu.Unlock()

	raw, err := root.ReadFile(rel)
	if err != nil {
		return agySessionMeta{}, false
	}
	raw = raw[:bytes.LastIndexByte(raw, '\n')+1]
	steps, _ := parseAgySteps(raw, conversation)
	usage := extractAgyUsage(steps)
	usage.turn = summarizeAgyTurn(steps)
	usage.modTime = info.ModTime().UTC()
	usage.size = info.Size()
	meta := agySessionMeta{agyTranscriptUsage: usage, background: agyBackgroundAt(steps, conversation, dir, root, now)}

	n.mu.Lock()
	if n.agyMetas == nil {
		n.agyMetas = newLRU[string, agyMetaEntry](256)
	}
	n.agyMetas.put(key, agyMetaEntry{file: info, path: rel, meta: meta})
	n.mu.Unlock()
	return meta, true
}

// mergeAgyTranscript populates a standalone agy pane row from its local transcript.
func (n *Node) mergeAgyTranscript(s *proto.Session, conversation string) {
	if s == nil {
		return
	}
	if u, ok := n.agyTranscriptMeta(conversation); ok {
		u.background.apply(s)
		if s.Model == "" && u.model != "" {
			s.Model = u.model
		}
		if u.outputTokens > 0 {
			s.SetOutputTokens(int(u.outputTokens))
		}
		if s.UpdatedAt.IsZero() && !u.modTime.IsZero() {
			s.UpdatedAt = u.modTime
		}
		if s.Rev == 0 && u.size > 0 {
			s.Rev = u.size
		}
		n.setAgyTranscriptContext(s, u.agyTranscriptUsage)
		return
	}
	n.resolveAgyContext(s, "")
}

func (n *Node) setAgyTranscriptContext(s *proto.Session, u agyTranscriptUsage) {
	switch {
	case u.contextTokens > 0:
		s.ContextTokens = proto.Tokens32(u.contextTokens)
		recorded := firstPositiveInt(u.contextWindow, int(s.ContextWindow))
		model := firstNonEmpty(u.model, s.Model)
		s.ContextWindow = proto.Tokens32(n.agyContextWindowFor(s, model, recorded, u.contextTokens))
	case u.compacted:
		s.ContextTokens, s.ContextWindow = 0, 0
	default:
		n.resolveAgyContext(s, "")
		return
	}
	setAgyContextBreakdown(s, u.contextStats)
}
