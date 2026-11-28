// Package mergetrain implements a pure, deterministic state machine for per-repository
// serial PR merge queues, conflict-driven serialization, tiered merge-policy guards,
// and a consecutive rebase-failure circuit breaker.
//
// It is pure: no IO, no clock, no network, and no dependencies outside the Go standard
// library and server/internal/assistant/todo/conflict.
//
// Concurrency and isolation guarantees:
//   - Different RepoKeys maintain completely independent queues and never block each other.
//   - Within a single RepoKey, at most one entry is StateActive (the queue head) at any time.
//   - Higher-priority queued entries move ahead of lower-priority queued entries, but never
//     preempt an already-active head.
//   - Runtime or static conflicts with conflict.ActionSerialize are deterministically serialized
//     in the target repository's queue with explicit conflict reasons recorded on each entry.
//   - Entries with PolicyHumanReviewRequired never receive ActionRebaseAndMerge until explicitly
//     approved via Approve, pausing in StateAwaitingApproval even when checks are green.
//   - Consecutive rebase failures are counted per entry; reaching MaxRebaseFailures (default 2)
//     trips the circuit breaker to StateEscalated (ActionEscalateNeedsUser) and stops automatic
//     retries. A successful rebase resets the failure streak.
//
// Escalation head policy:
//   - By default (fail-closed), an escalated head remains parked in the head slot in StateEscalated
//     and blocks subsequent entries in the same repository until explicitly cancelled or merged
//     out-of-band. Calling EvaluateHead on an escalated head always returns ActionEscalateNeedsUser.
//   - When constructed with WithAdvanceOnEscalation(true), an escalated head is moved to the repo's
//     escalated list and the next queued entry that has no recorded conflict with any escalated entry
//     is promoted to StateActive.
package mergetrain

import (
	"cmp"
	"errors"
	"fmt"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"sync"

	"github.com/nwverse/persona/server/internal/assistant/todo/conflict"
)

const (
	// DefaultMaxRebaseFailures is the default consecutive rebase failure threshold
	// before an entry trips the circuit breaker and escalates to needs_user.
	DefaultMaxRebaseFailures = 2
)

// MergePolicy defines the merge permission tier for a queued PR.
type MergePolicy string

const (
	// PolicyAutoMerge permits automatic rebase and merge once checks are green at queue head.
	PolicyAutoMerge MergePolicy = "auto_merge"
	// PolicyHumanReviewRequired requires explicit human approval before merge; it is never
	// auto-merged even when checks are green at queue head.
	PolicyHumanReviewRequired MergePolicy = "human_review_required"
)

// EntryState represents the lifecycle state of an entry in a repository merge train.
type EntryState string

const (
	// StateQueued indicates the entry is waiting in the repository queue behind earlier work.
	StateQueued EntryState = "queued"
	// StateActive indicates the entry is the current repository head being verified/rebased/merged.
	StateActive EntryState = "active"
	// StateAwaitingApproval indicates the entry is at queue head with green checks, paused
	// because PolicyHumanReviewRequired has not yet been approved.
	StateAwaitingApproval EntryState = "awaiting_approval"
	// StateEscalated indicates consecutive rebase failures reached MaxRebaseFailures and the
	// entry has been circuit-broken to needs_user.
	StateEscalated EntryState = "escalated"
	// StateMerged indicates the entry was successfully merged (terminal).
	StateMerged EntryState = "merged"
	// StateCancelled indicates the entry was cancelled or removed from the train (terminal).
	StateCancelled EntryState = "cancelled"
)

// Action is the pure orchestration directive returned by the merge train state machine.
type Action string

const (
	// ActionRebaseAndMerge instructs the orchestrator to rebase and merge the repository head.
	ActionRebaseAndMerge Action = "rebase_and_merge"
	// ActionWaitQueue instructs the orchestrator to wait (checks not green, retry pending, or queued).
	ActionWaitQueue Action = "wait_queue"
	// ActionAwaitHumanReview instructs the orchestrator to pause and wait for human approval.
	ActionAwaitHumanReview Action = "await_human_review"
	// ActionEscalateNeedsUser instructs the orchestrator to stop automatic retries and escalate to the user.
	ActionEscalateNeedsUser Action = "escalate_needs_user"
	// ActionNoop indicates no action is required (e.g. empty queue or terminal entry).
	ActionNoop Action = "noop"
)

var (
	// ErrInvalidEntry is returned when a to-do or a valid PR URL is missing.
	ErrInvalidEntry = errors.New("mergetrain: todo_id and a pull or merge request URL are required")
	// ErrInvalidPolicy is returned when an Entry specifies an unrecognized MergePolicy.
	ErrInvalidPolicy = errors.New("mergetrain: invalid merge policy")
	// ErrEntryNotFound is returned when an operation targets a non-existent entry.
	ErrEntryNotFound = errors.New("mergetrain: entry not found")
	// ErrAlreadyTerminal is returned when a mutating operation targets a merged or cancelled entry.
	ErrAlreadyTerminal = errors.New("mergetrain: entry is already in a terminal state")
	// ErrEntryEscalated is returned when an operation cannot proceed because the entry is escalated.
	ErrEntryEscalated = errors.New("mergetrain: entry is escalated and requires user intervention")
)

// Entry describes a single PR/todo tracked by a repository's merge train.
type Entry struct {
	TodoID            string      `json:"todo_id"`
	RepoKey           string      `json:"repo_key"`
	PRNumber          int         `json:"pr_number,omitempty"`
	PRURL             string      `json:"pr_url,omitempty"`
	Priority          int         `json:"priority,omitempty"`
	Policy            MergePolicy `json:"policy"`
	Approved          bool        `json:"approved,omitempty"`
	State             EntryState  `json:"state"`
	RebaseFailures    int         `json:"rebase_failures,omitempty"`
	MaxRebaseFailures int         `json:"max_rebase_failures"`
	LastError         string      `json:"last_error,omitempty"`
	ConflictReasons   []string    `json:"conflict_reasons,omitempty"`
}

// Decision is the deterministic directive produced by evaluating or advancing the train.
type Decision struct {
	RepoKey string     `json:"repo_key"`
	TodoID  string     `json:"todo_id,omitempty"`
	Action  Action     `json:"action"`
	State   EntryState `json:"state,omitempty"`
	Reason  string     `json:"reason,omitempty"`
}

// RepoSnapshot is an immutable point-in-time view of a single repository's merge train.
type RepoSnapshot struct {
	RepoKey   string  `json:"repo_key"`
	Head      *Entry  `json:"head,omitempty"`
	Queue     []Entry `json:"queue,omitempty"`
	Escalated []Entry `json:"escalated,omitempty"`
	Terminal  []Entry `json:"terminal,omitempty"`
}

// Snapshot is an immutable, deterministically ordered view of all repositories in the train.
type Snapshot struct {
	Repos []RepoSnapshot `json:"repos,omitempty"`
}

// Option configures a Train instance.
type Option func(*Train)

// WithDefaultMaxRebaseFailures overrides the default consecutive rebase failure threshold
// for entries that do not specify a positive MaxRebaseFailures. Non-positive values are ignored.
func WithDefaultMaxRebaseFailures(n int) Option {
	return func(t *Train) {
		if n > 0 {
			t.defaultMaxRebaseFailures = n
		}
	}
}

// WithAdvanceOnEscalation configures whether an escalated head is moved aside to allow
// subsequent non-conflicting queued entries in the same repository to advance.
// When false (default), an escalated head holds the head slot until explicitly cancelled or merged.
func WithAdvanceOnEscalation(advance bool) Option {
	return func(t *Train) {
		t.advanceOnEscalation = advance
	}
}

type queuedItem struct {
	entry Entry
	seq   uint64
}

type repoQueue struct {
	head          *queuedItem
	queue         []*queuedItem
	escalated     []*queuedItem
	terminal      []*queuedItem
	byPR          map[string]*queuedItem
	conflictPeers map[string]map[string]struct{}
}

func newRepoQueue() *repoQueue {
	return &repoQueue{
		byPR:          make(map[string]*queuedItem),
		conflictPeers: make(map[string]map[string]struct{}),
	}
}

// Train manages independent serial merge queues keyed by RepoKey.
// All exported methods are safe for concurrent use.
type Train struct {
	mu                       sync.RWMutex
	defaultMaxRebaseFailures int
	advanceOnEscalation      bool
	nextSeq                  uint64
	repos                    map[string]*repoQueue
}

// New creates a new empty Train with the supplied options.
func New(opts ...Option) *Train {
	t := &Train{
		defaultMaxRebaseFailures: DefaultMaxRebaseFailures,
		repos:                    make(map[string]*repoQueue),
	}
	for _, opt := range opts {
		if opt != nil {
			opt(t)
		}
	}
	return t
}

// NormalizeRepoKey canonicalizes a repository identifier using the same casing as its PRs.
func NormalizeRepoKey(raw string) string {
	s := strings.TrimSpace(raw)
	s = strings.TrimRight(s, "/")
	return strings.ToLower(s)
}

// ParsePRURL returns the repository, number and canonical URL for a pull or merge request.
func ParsePRURL(raw string) (repoKey string, number int, canonical string, ok bool) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Hostname() == "" || u.User != nil || (u.Scheme != "https" && u.Scheme != "http") {
		return "", 0, "", false
	}
	host := strings.ToLower(u.Host)
	if u.Port() == "443" && u.Scheme == "https" || u.Port() == "80" && u.Scheme == "http" {
		host = strings.TrimSuffix(host, ":"+u.Port())
	}
	if host == "github.com" {
		u.Scheme = "https"
	}
	path := strings.ToLower(strings.Trim(u.Path, "/"))
	for _, delimiter := range []string{"/-/merge_requests/", "/merge_requests/", "/pull/"} {
		repo, tail, found := strings.Cut(path, delimiter)
		if !found {
			continue
		}
		rawNumber, _, _ := strings.Cut(tail, "/")
		n, err := strconv.Atoi(rawNumber)
		if err != nil || n <= 0 || repo == "" {
			return "", 0, "", false
		}
		if delimiter == "/merge_requests/" {
			delimiter = "/-/merge_requests/"
		}
		link := url.URL{Scheme: u.Scheme, Host: host, Path: "/" + repo + delimiter + strconv.Itoa(n)}
		return host + "/" + repo, n, link.String(), true
	}
	return "", 0, "", false
}

func validatePolicy(p MergePolicy) (MergePolicy, error) {
	switch p {
	case "", PolicyAutoMerge:
		return PolicyAutoMerge, nil
	case PolicyHumanReviewRequired:
		return PolicyHumanReviewRequired, nil
	default:
		return "", fmt.Errorf("%w: %q", ErrInvalidPolicy, p)
	}
}

func isTerminal(s EntryState) bool {
	return s == StateMerged || s == StateCancelled
}

func cloneEntry(e Entry) Entry {
	cp := e
	if len(e.ConflictReasons) > 0 {
		cp.ConflictReasons = slices.Clone(e.ConflictReasons)
	} else {
		cp.ConflictReasons = nil
	}
	return cp
}

func (t *Train) getOrCreateRepoLocked(repoKey string) *repoQueue {
	rq, ok := t.repos[repoKey]
	if !ok {
		rq = newRepoQueue()
		t.repos[repoKey] = rq
	}
	return rq
}

func (t *Train) enqueueLocked(entry Entry) (Entry, error) {
	todoID := strings.TrimSpace(entry.TodoID)
	repoKey, number, prURL, ok := ParsePRURL(entry.PRURL)
	if todoID == "" || !ok {
		return Entry{}, ErrInvalidEntry
	}
	policy, err := validatePolicy(entry.Policy)
	if err != nil {
		return Entry{}, err
	}
	maxFailures := entry.MaxRebaseFailures
	if maxFailures <= 0 {
		maxFailures = t.defaultMaxRebaseFailures
	}

	rq := t.getOrCreateRepoLocked(repoKey)
	if existing := rq.byPR[prURL]; existing != nil {
		if existing.entry.State != StateCancelled {
			// A second to-do reporting the same PR cannot duplicate it or change its owning to-do.
			for _, reason := range entry.ConflictReasons {
				addConflictReasonLocked(existing, reason)
			}
			return cloneEntry(existing.entry), nil
		}
		// A closed PR can reopen. Keep the existing head and give this PR a new queue position.
		removeFromSliceLocked(&rq.terminal, existing)
	}

	t.nextSeq++
	item := &queuedItem{
		seq: t.nextSeq,
		entry: Entry{
			TodoID:            todoID,
			RepoKey:           repoKey,
			PRNumber:          number,
			PRURL:             prURL,
			Priority:          entry.Priority,
			Policy:            policy,
			Approved:          entry.Approved,
			State:             StateQueued,
			RebaseFailures:    0,
			MaxRebaseFailures: maxFailures,
		},
	}
	for _, r := range entry.ConflictReasons {
		addConflictReasonLocked(item, r)
	}
	rq.byPR[prURL] = item

	if rq.head == nil && !t.conflictsWithEscalatedLocked(rq, todoID) {
		item.entry.State = StateActive
		rq.head = item
	} else {
		insertQueuedLocked(rq, item)
	}
	return cloneEntry(item.entry), nil
}

func insertQueuedLocked(rq *repoQueue, item *queuedItem) {
	item.entry.State = StateQueued
	rq.queue = append(rq.queue, item)
	slices.SortFunc(rq.queue, func(a, b *queuedItem) int {
		// Higher Priority first; ties broken by FIFO insertion sequence.
		return cmp.Or(
			cmp.Compare(b.entry.Priority, a.entry.Priority),
			cmp.Compare(a.seq, b.seq),
		)
	})
}

func addConflictReasonLocked(item *queuedItem, rawReason string) {
	reason := strings.TrimSpace(rawReason)
	if reason == "" || slices.Contains(item.entry.ConflictReasons, reason) {
		return
	}
	item.entry.ConflictReasons = append(item.entry.ConflictReasons, reason)
	slices.Sort(item.entry.ConflictReasons)
}

func recordConflictPeerLocked(rq *repoQueue, todoA, todoB string) {
	if _, ok := rq.conflictPeers[todoA]; !ok {
		rq.conflictPeers[todoA] = make(map[string]struct{})
	}
	if _, ok := rq.conflictPeers[todoB]; !ok {
		rq.conflictPeers[todoB] = make(map[string]struct{})
	}
	rq.conflictPeers[todoA][todoB] = struct{}{}
	rq.conflictPeers[todoB][todoA] = struct{}{}
}

func (t *Train) conflictsWithEscalatedLocked(rq *repoQueue, todoID string) bool {
	if !t.advanceOnEscalation || len(rq.escalated) == 0 {
		return false
	}
	peers := rq.conflictPeers[todoID]
	if len(peers) == 0 {
		return false
	}
	for _, esc := range rq.escalated {
		if _, ok := peers[esc.entry.TodoID]; ok {
			return true
		}
	}
	return false
}

func (t *Train) promoteNextHeadLocked(rq *repoQueue) *queuedItem {
	if rq.head != nil || len(rq.queue) == 0 {
		return rq.head
	}
	for i, item := range rq.queue {
		if t.conflictsWithEscalatedLocked(rq, item.entry.TodoID) {
			continue
		}
		rq.queue = slices.Delete(rq.queue, i, i+1)
		if item.entry.State != StateEscalated {
			item.entry.State = StateActive
		}
		rq.head = item
		return item
	}
	return nil
}

// Enqueue validates and adds an entry to its repository's merge train.
// If the repository currently has no head, the entry is immediately activated as StateActive.
// Otherwise it is placed in StateQueued ordered by Priority descending (FIFO on ties) without
// preempting the active head.
// Re-enqueuing a PR by its canonical URL preserves its current position and state.
func (t *Train) Enqueue(entry Entry) (Entry, error) {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.enqueueLocked(entry)
}

// ApplyConflicts consumes write-scope conflicts produced by conflict.Detect.
// Only pairs with Action == conflict.ActionSerialize are applied; ActionWarnOverridden pairs
// are ignored. Conflict pairs annotate the observed PRs of the two to-dos.
//
// Returns deterministic Decisions for affected repositories' heads (sorted by RepoKey ascending).
func (t *Train) ApplyConflicts(conflicts []conflict.PairConflict) []Decision {
	if len(conflicts) == 0 {
		return nil
	}
	normalized := make([]conflict.PairConflict, 0, len(conflicts))
	for _, c := range conflicts {
		if c.Action != conflict.ActionSerialize {
			continue
		}
		repoKey := NormalizeRepoKey(c.RepoKey)
		todoA := strings.TrimSpace(c.TodoA)
		todoB := strings.TrimSpace(c.TodoB)
		if repoKey == "" || todoA == "" || todoB == "" || todoA == todoB {
			continue
		}
		if todoA > todoB {
			todoA, todoB = todoB, todoA
		}
		nc := c
		nc.RepoKey = repoKey
		nc.TodoA = todoA
		nc.TodoB = todoB
		normalized = append(normalized, nc)
	}
	if len(normalized) == 0 {
		return nil
	}

	slices.SortFunc(normalized, func(a, b conflict.PairConflict) int {
		return cmp.Or(
			strings.Compare(a.RepoKey, b.RepoKey),
			strings.Compare(a.TodoA, b.TodoA),
			strings.Compare(a.TodoB, b.TodoB),
			strings.Compare(a.Layer, b.Layer),
		)
	})

	t.mu.Lock()
	defer t.mu.Unlock()

	affectedRepos := make(map[string]struct{})
	for _, c := range normalized {
		rq := t.repos[c.RepoKey]
		if rq == nil {
			continue
		}
		recordConflictPeerLocked(rq, c.TodoA, c.TodoB)
		for _, item := range rq.byPR {
			if isTerminal(item.entry.State) {
				continue
			}
			switch item.entry.TodoID {
			case c.TodoA:
				addConflictReasonLocked(item, formatConflictReason(c, c.TodoB))
			case c.TodoB:
				addConflictReasonLocked(item, formatConflictReason(c, c.TodoA))
			default:
				continue
			}
			affectedRepos[c.RepoKey] = struct{}{}
		}
	}

	repoKeys := make([]string, 0, len(affectedRepos))
	for rk := range affectedRepos {
		repoKeys = append(repoKeys, rk)
	}
	slices.Sort(repoKeys)

	decisions := make([]Decision, 0, len(repoKeys))
	for _, rk := range repoKeys {
		rq := t.repos[rk]
		if rq == nil || rq.head == nil {
			decisions = append(decisions, Decision{
				RepoKey: rk,
				Action:  ActionNoop,
				Reason:  "no active head in repository queue",
			})
			continue
		}
		decisions = append(decisions, Decision{
			RepoKey: rk,
			TodoID:  rq.head.entry.TodoID,
			Action:  ActionWaitQueue,
			State:   rq.head.entry.State,
			Reason:  "serialized conflicting tasks in repository queue",
		})
	}
	return decisions
}

func formatConflictReason(c conflict.PairConflict, peerTodoID string) string {
	layer := strings.TrimSpace(c.Layer)
	if layer == "" {
		layer = "unknown"
	}
	var parts []string
	if len(c.StaticOverlaps) > 0 {
		overlaps := slices.Clone(c.StaticOverlaps)
		slices.Sort(overlaps)
		parts = append(parts, "prefixes=["+strings.Join(overlaps, ",")+"]")
	}
	if len(c.RuntimeFiles) > 0 {
		files := slices.Clone(c.RuntimeFiles)
		slices.Sort(files)
		parts = append(parts, "files=["+strings.Join(files, ",")+"]")
	}
	if len(parts) == 0 {
		return fmt.Sprintf("conflict(%s): serialized with %s", layer, peerTodoID)
	}
	return fmt.Sprintf("conflict(%s): serialized with %s on %s", layer, peerTodoID, strings.Join(parts, " "))
}

// EvaluateHead inspects the current head of repoKey given whether its required CI checks are green
// and returns the next deterministic orchestration Decision.
//
// Policy guarantees:
//   - An escalated head (StateEscalated) always returns ActionEscalateNeedsUser and never retries.
//   - PolicyHumanReviewRequired without explicit approval transitions to StateAwaitingApproval
//     and returns ActionAwaitHumanReview even when checksGreen is true.
//   - ActionRebaseAndMerge is only returned when checksGreen is true AND either PolicyAutoMerge
//     applies or PolicyHumanReviewRequired has been explicitly approved.
func (t *Train) EvaluateHead(repoKey string, checksGreen bool) Decision {
	rk := NormalizeRepoKey(repoKey)
	if rk == "" {
		return Decision{Action: ActionNoop, Reason: "empty repo_key"}
	}

	t.mu.Lock()
	defer t.mu.Unlock()

	rq := t.repos[rk]
	if rq == nil || rq.head == nil {
		return Decision{
			RepoKey: rk,
			Action:  ActionNoop,
			Reason:  "queue is empty",
		}
	}

	head := rq.head
	if head.entry.State == StateEscalated {
		reason := "rebase failure circuit breaker tripped"
		if head.entry.LastError != "" {
			reason = fmt.Sprintf("rebase failure circuit breaker tripped (%d/%d): %s",
				head.entry.RebaseFailures, head.entry.MaxRebaseFailures, head.entry.LastError)
		}
		return Decision{
			RepoKey: rk,
			TodoID:  head.entry.TodoID,
			Action:  ActionEscalateNeedsUser,
			State:   StateEscalated,
			Reason:  reason,
		}
	}

	if !checksGreen {
		if head.entry.State == StateAwaitingApproval {
			head.entry.State = StateActive
		}
		return Decision{
			RepoKey: rk,
			TodoID:  head.entry.TodoID,
			Action:  ActionWaitQueue,
			State:   head.entry.State,
			Reason:  "waiting for checks to turn green",
		}
	}

	if head.entry.Policy == PolicyHumanReviewRequired && !head.entry.Approved {
		head.entry.State = StateAwaitingApproval
		return Decision{
			RepoKey: rk,
			TodoID:  head.entry.TodoID,
			Action:  ActionAwaitHumanReview,
			State:   StateAwaitingApproval,
			Reason:  "human review approval required before merge",
		}
	}

	head.entry.State = StateActive
	return Decision{
		RepoKey: rk,
		TodoID:  head.entry.TodoID,
		Action:  ActionRebaseAndMerge,
		State:   StateActive,
		Reason:  "head checks green and merge policy satisfied",
	}
}

// Approve records explicit human approval for a PR.
// If the entry is currently paused at StateAwaitingApproval, it transitions back to StateActive
// and returns ActionRebaseAndMerge. Escalated or terminal entries cannot be approved.
func (t *Train) Approve(rawPRURL string) (Decision, error) {
	rk, _, prURL, ok := ParsePRURL(rawPRURL)
	if !ok {
		return Decision{Action: ActionNoop}, ErrInvalidEntry
	}

	t.mu.Lock()
	defer t.mu.Unlock()

	rq := t.repos[rk]
	if rq == nil {
		return Decision{RepoKey: rk, Action: ActionNoop}, ErrEntryNotFound
	}
	item := rq.byPR[prURL]
	if item == nil {
		return Decision{RepoKey: rk, Action: ActionNoop}, ErrEntryNotFound
	}
	tid := item.entry.TodoID
	if isTerminal(item.entry.State) {
		return Decision{
			RepoKey: rk,
			TodoID:  tid,
			Action:  ActionNoop,
			State:   item.entry.State,
			Reason:  "entry already terminal",
		}, ErrAlreadyTerminal
	}
	if item.entry.State == StateEscalated {
		return Decision{
			RepoKey: rk,
			TodoID:  tid,
			Action:  ActionEscalateNeedsUser,
			State:   StateEscalated,
			Reason:  "escalated entry requires user resolution",
		}, ErrEntryEscalated
	}

	item.entry.Approved = true
	if rq.head == item && item.entry.State == StateAwaitingApproval {
		item.entry.State = StateActive
		return Decision{
			RepoKey: rk,
			TodoID:  tid,
			Action:  ActionRebaseAndMerge,
			State:   StateActive,
			Reason:  "human review approved; ready to rebase and merge",
		}, nil
	}

	return Decision{
		RepoKey: rk,
		TodoID:  tid,
		Action:  ActionWaitQueue,
		State:   item.entry.State,
		Reason:  "human review approval recorded",
	}, nil
}

// RecordRebaseFailure increments the consecutive rebase failure count for the PR.
// When RebaseFailures reaches MaxRebaseFailures (default 2), the entry transitions to
// StateEscalated and returns ActionEscalateNeedsUser.
func (t *Train) RecordRebaseFailure(rawPRURL, errMsg string) (Decision, error) {
	rk, _, prURL, ok := ParsePRURL(rawPRURL)
	if !ok {
		return Decision{Action: ActionNoop}, ErrInvalidEntry
	}

	t.mu.Lock()
	defer t.mu.Unlock()

	rq := t.repos[rk]
	if rq == nil {
		return Decision{RepoKey: rk, Action: ActionNoop}, ErrEntryNotFound
	}
	item := rq.byPR[prURL]
	if item == nil {
		return Decision{RepoKey: rk, Action: ActionNoop}, ErrEntryNotFound
	}
	tid := item.entry.TodoID
	if isTerminal(item.entry.State) {
		return Decision{
			RepoKey: rk,
			TodoID:  tid,
			Action:  ActionNoop,
			State:   item.entry.State,
			Reason:  "entry already terminal",
		}, ErrAlreadyTerminal
	}

	msg := strings.TrimSpace(errMsg)
	if item.entry.State == StateEscalated {
		if msg != "" {
			item.entry.LastError = msg
		}
		return Decision{
			RepoKey: rk,
			TodoID:  tid,
			Action:  ActionEscalateNeedsUser,
			State:   StateEscalated,
			Reason:  fmt.Sprintf("already escalated after %d/%d rebase failures", item.entry.RebaseFailures, item.entry.MaxRebaseFailures),
		}, nil
	}

	item.entry.RebaseFailures++
	item.entry.LastError = msg

	if item.entry.RebaseFailures >= item.entry.MaxRebaseFailures {
		item.entry.State = StateEscalated
		if t.advanceOnEscalation {
			if rq.head == item {
				rq.head = nil
			} else {
				removeFromSliceLocked(&rq.queue, item)
			}
			rq.escalated = append(rq.escalated, item)
			slices.SortFunc(rq.escalated, func(a, b *queuedItem) int {
				return strings.Compare(a.entry.PRURL, b.entry.PRURL)
			})
			t.promoteNextHeadLocked(rq)
		}
		reason := fmt.Sprintf("consecutive rebase failures reached limit (%d/%d)", item.entry.RebaseFailures, item.entry.MaxRebaseFailures)
		if msg != "" {
			reason = reason + ": " + msg
		}
		return Decision{
			RepoKey: rk,
			TodoID:  tid,
			Action:  ActionEscalateNeedsUser,
			State:   StateEscalated,
			Reason:  reason,
		}, nil
	}

	return Decision{
		RepoKey: rk,
		TodoID:  tid,
		Action:  ActionWaitQueue,
		State:   item.entry.State,
		Reason:  fmt.Sprintf("rebase failed (%d/%d); retry permitted", item.entry.RebaseFailures, item.entry.MaxRebaseFailures),
	}, nil
}

// RecordRebaseSuccess clears the consecutive rebase failure count and last error for the PR.
func (t *Train) RecordRebaseSuccess(rawPRURL string) error {
	rk, _, prURL, ok := ParsePRURL(rawPRURL)
	if !ok {
		return ErrInvalidEntry
	}

	t.mu.Lock()
	defer t.mu.Unlock()

	rq := t.repos[rk]
	if rq == nil {
		return ErrEntryNotFound
	}
	item := rq.byPR[prURL]
	if item == nil {
		return ErrEntryNotFound
	}
	if isTerminal(item.entry.State) {
		return ErrAlreadyTerminal
	}
	if item.entry.State == StateEscalated {
		return ErrEntryEscalated
	}
	item.entry.RebaseFailures = 0
	item.entry.LastError = ""
	return nil
}

// RecordMerged marks the PR as StateMerged, removes it from active/queued/escalated slots,
// and deterministically promotes the next eligible queued entry to StateActive if the head was freed.
func (t *Train) RecordMerged(prURL string) (Decision, error) {
	return t.finalize(prURL, StateMerged, "merged")
}

// Cancel marks the PR as StateCancelled, removes it from active/queued/escalated slots,
// and deterministically promotes the next eligible queued entry to StateActive if the head was freed.
func (t *Train) Cancel(prURL string) (Decision, error) {
	return t.finalize(prURL, StateCancelled, "cancelled")
}

func (t *Train) finalize(rawPRURL string, targetState EntryState, verb string) (Decision, error) {
	rk, _, prURL, ok := ParsePRURL(rawPRURL)
	if !ok {
		return Decision{Action: ActionNoop}, ErrInvalidEntry
	}

	t.mu.Lock()
	defer t.mu.Unlock()

	rq := t.repos[rk]
	if rq == nil {
		return Decision{RepoKey: rk, Action: ActionNoop}, ErrEntryNotFound
	}
	item := rq.byPR[prURL]
	if item == nil {
		return Decision{RepoKey: rk, Action: ActionNoop}, ErrEntryNotFound
	}
	tid := item.entry.TodoID
	if isTerminal(item.entry.State) {
		if item.entry.State == targetState {
			return Decision{
				RepoKey: rk,
				TodoID:  tid,
				Action:  ActionNoop,
				State:   targetState,
				Reason:  "entry already " + verb,
			}, nil
		}
		return Decision{
			RepoKey: rk,
			TodoID:  tid,
			Action:  ActionNoop,
			State:   item.entry.State,
			Reason:  "entry already terminal",
		}, ErrAlreadyTerminal
	}

	wasHead := rq.head == item
	if wasHead {
		rq.head = nil
	} else {
		removeFromSliceLocked(&rq.queue, item)
		removeFromSliceLocked(&rq.escalated, item)
	}

	item.entry.State = targetState
	rq.terminal = append(rq.terminal, item)

	// Always try to promote the next eligible head (covers both head removal and
	// freeing a conflict block when an escalated entry is cancelled/merged).
	next := t.promoteNextHeadLocked(rq)
	if next != nil {
		if next.entry.State == StateEscalated {
			reason := fmt.Sprintf("head %s is escalated after %s %s", next.entry.TodoID, tid, verb)
			if next.entry.LastError != "" {
				reason = fmt.Sprintf("%s (%d/%d): %s", reason, next.entry.RebaseFailures, next.entry.MaxRebaseFailures, next.entry.LastError)
			}
			return Decision{
				RepoKey: rk,
				TodoID:  next.entry.TodoID,
				Action:  ActionEscalateNeedsUser,
				State:   StateEscalated,
				Reason:  reason,
			}, nil
		}
		return Decision{
			RepoKey: rk,
			TodoID:  next.entry.TodoID,
			Action:  ActionWaitQueue,
			State:   next.entry.State,
			Reason:  fmt.Sprintf("next head ready after %s %s", tid, verb),
		}, nil
	}

	return Decision{
		RepoKey: rk,
		Action:  ActionNoop,
		Reason:  fmt.Sprintf("queue empty after %s %s", tid, verb),
	}, nil
}

func removeFromSliceLocked(slice *[]*queuedItem, target *queuedItem) {
	s := *slice
	for i, item := range s {
		if item == target {
			*slice = slices.Delete(s, i, i+1)
			return
		}
	}
}

// Entry returns a copy of the current Entry for a PR, if tracked.
func (t *Train) Entry(rawPRURL string) (Entry, bool) {
	rk, _, prURL, ok := ParsePRURL(rawPRURL)
	if !ok {
		return Entry{}, false
	}

	t.mu.RLock()
	defer t.mu.RUnlock()

	rq := t.repos[rk]
	if rq == nil {
		return Entry{}, false
	}
	item := rq.byPR[prURL]
	if item == nil {
		return Entry{}, false
	}
	return cloneEntry(item.entry), true
}

// Head returns the current entry at the front of a repository queue.
func (t *Train) Head(repoKey string) (Entry, bool) {
	t.mu.RLock()
	defer t.mu.RUnlock()
	rq := t.repos[NormalizeRepoKey(repoKey)]
	if rq == nil || rq.head == nil {
		return Entry{}, false
	}
	return cloneEntry(rq.head.entry), true
}

// Forget drops one PR and promotes the next repository head if its turn was active.
func (t *Train) Forget(rawPRURL string) {
	rk, _, prURL, ok := ParsePRURL(rawPRURL)
	if !ok {
		return
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	rq := t.repos[rk]
	if rq == nil {
		return
	}
	item := rq.byPR[prURL]
	if item == nil {
		return
	}
	if rq.head == item {
		rq.head = nil
	}
	removeFromSliceLocked(&rq.queue, item)
	removeFromSliceLocked(&rq.escalated, item)
	removeFromSliceLocked(&rq.terminal, item)
	delete(rq.byPR, prURL)
	t.promoteNextHeadLocked(rq)
	if len(rq.byPR) == 0 {
		delete(t.repos, rk)
	}
}

// Snapshot returns a deterministic deep copy of all repository queues sorted by RepoKey ascending,
// with each repository's queue in exact priority + FIFO execution order.
func (t *Train) Snapshot() Snapshot {
	t.mu.RLock()
	defer t.mu.RUnlock()

	if len(t.repos) == 0 {
		return Snapshot{}
	}

	keys := make([]string, 0, len(t.repos))
	for rk := range t.repos {
		keys = append(keys, rk)
	}
	slices.Sort(keys)

	repos := make([]RepoSnapshot, 0, len(keys))
	for _, rk := range keys {
		rq := t.repos[rk]
		rs := RepoSnapshot{RepoKey: rk}
		if rq.head != nil {
			h := cloneEntry(rq.head.entry)
			rs.Head = &h
		}
		if len(rq.queue) > 0 {
			rs.Queue = make([]Entry, len(rq.queue))
			for i, item := range rq.queue {
				rs.Queue[i] = cloneEntry(item.entry)
			}
		}
		if len(rq.escalated) > 0 {
			rs.Escalated = make([]Entry, len(rq.escalated))
			for i, item := range rq.escalated {
				rs.Escalated[i] = cloneEntry(item.entry)
			}
		}
		if len(rq.terminal) > 0 {
			rs.Terminal = make([]Entry, len(rq.terminal))
			for i, item := range rq.terminal {
				rs.Terminal[i] = cloneEntry(item.entry)
			}
		}
		repos = append(repos, rs)
	}
	return Snapshot{Repos: repos}
}
