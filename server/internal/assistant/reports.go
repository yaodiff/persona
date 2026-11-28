package assistant

import (
	"cmp"
	"context"
	"fmt"
	"maps"
	"slices"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/nwverse/persona/server/internal/assistant/repository"
	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/botcard"
	"github.com/nwverse/persona/server/internal/closeout"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/larkhost"
	"github.com/nwverse/persona/server/internal/lazyre"
	"github.com/nwverse/persona/server/internal/proto"
)

// Task reports in Feishu (#2768, docs/extensions/bots.md "Task progress"): the user reads the bot's chat, not the app, so how the
// main conversation's tasks go — done, waiting for the user, cut off, did not start, closed by the judge — is told
// there by the bot itself (no lark-cli, nothing to approve per message). Changes within reportDelay are one card:
// what needs the user's decision on top (each with its own answerable card below it), then each task with its stage
// (plan → develop → review → merge → deploy → verified), a line of what happened and links to its page, its PR and its agent.
// That is the card with report_steps on; off (the default) only goal-level changes go out (#3218, below).
// The bot's reports switch turns it off (on by default). With only_conclusions on (#1226) the card carries only how tasks
// ended (closed, did not start): a finished turn, waiting and interrupted stay in the task list and the briefing, and a
// decision waiting still gets its own answerable card. A supervised task's review sends its conclusion card instead (conclusion.go).

// Goal-level reports (#3218): by default the card is not a log of the work. A finished turn, an interruption, a closing
// by the judge and the stage moving on are execution steps and are not pushed; what is pushed is a change of the task's
// goal: reached (the conclusion card, conclusion.go, or the "Please verify" card of deployed work), waiting on the owner
// (the decision's own answerable card) and stuck (it did not start, or its turns were cut off reportStuckAfter times in a
// row). A stuck task's card says the goal, where it is now and one line of evidence with a link: no agent's words, no
// stage bar. The bot's report_steps switch brings back the card as it was.

// reportStuckAfter is how many interrupted turns in a row, with no finished one between, make a task stuck.
const reportStuckAfter = 3

// goalCardMaxItems bounds the stuck card; the rest are counted in a line pointing to the to-do list.
const goalCardMaxItems = 10

// reportDelay is how long a change waits for others before the card goes out (reportBook.delay overrides it).
const reportDelay = time.Minute

// reportRetryDelay is how long a batch Feishu could not take waits before it is sent again (reportBook.retry
// overrides it). reportMaxRetries caps the retries after the first attempt: then the batch is given up and logged,
// so a dead transport cannot pin the queue (and terminal reports) forever.
const (
	reportRetryDelay = 30 * time.Second
	reportMaxRetries = 3
	// Keep the existing card-budget names for the assistant's other card builders.
	reportCardMaxBytes    = botcard.MaxBytes
	reportCardMaxElements = botcard.MaxElements
	reportFallbackMax     = 1500 // runes of a card's rich-text fallback (PostMarkdown clips neither)
)

// Report states, in the order a card lists them after the decisions.
const (
	reportNeeds       = "needs"
	reportFailed      = "failed"
	reportInterrupted = "interrupted"
	reportDone        = "done"
	reportClosed      = "closed"
)

var reportOrder = map[string]int{reportNeeds: 0, reportFailed: 1, reportInterrupted: 2, reportDone: 3, reportClosed: 4}

// Stages of a task, in order (Todo.Stage; the apps draw the same bar).
var taskStages = []string{"plan", "develop", "review", "merge", "deploy", "verify"}

func stageIndex(s string) int {
	for i, x := range taskStages {
		if x == s {
			return i
		}
	}
	return -1
}

var (
	deployWords = lazyre.New(`(?i)\bdeployed\b|\bis live\b|\bwent live\b|已部署|已经部署|完成部署|部署好|部署了|部署完|部署到|部署上线|上线了|已上线|发布了|已发布|发版了`)
	verifyWords = lazyre.New(`(?i)\bverified\b|已验证|验证通过|实测通过|实测.{0,6}(通过|成功|收到)|截图.{0,4}(确认|为证)`)
	// A deploy or verify phrase does not count when, in its own clause, the few words right before it negate or defer
	// it ("not deployed to … yet", "don't deploy yet", "deploy after you confirm", not yet deployed, it will be deployed) or what follows asks or says
	// "yet" ("deployed?", deployed yet): a false "deployed" would ask the user to check something that is not there.
	notYetZh = lazyre.New(`没|未|不|别|无需|暂不|无法|待|等|还要|需要|准备`)
	notYetEn = lazyre.New(`(?i)\b(not|never|nothing|before|will|once|awaiting|pending)\b|n't\b|\byet to\b|\bto be\b`)
	askAfter = lazyre.New(`(?i)^\s*(吗|么|没有?|与否|\?|？|yet\b)`)
)

// clauseMarks end a clause: a negation before one of them is about something else.
const clauseMarks = "，。；：,.;:!?！？\n"

// said: re matches text somewhere it is told as done, not negated, deferred or asked.
func said(re *lazyre.Regexp, text string) bool {
	for _, m := range re.FindAllStringIndex(text, -1) {
		if askAfter.MatchString(text[m[1]:]) || negatedBefore(text[:m[0]]) {
			continue
		}
		return true
	}
	return false
}

// negatedBefore: the end of pre, within its last clause, negates or defers what comes next (8 characters for
// Chinese, 3 words for English).
func negatedBefore(pre string) bool {
	if i := strings.LastIndexAny(pre, clauseMarks); i >= 0 {
		_, w := utf8.DecodeRuneInString(pre[i:])
		pre = pre[i+w:]
	}
	tail := pre
	for i, n := len(pre), 0; i > 0; n++ {
		if n == 8 {
			tail = pre[i:]
			break
		}
		_, w := utf8.DecodeLastRuneInString(pre[:i])
		i -= w
	}
	words := strings.Fields(pre)
	if len(words) > 3 {
		words = words[len(words)-3:]
	}
	return notYetZh.MatchString(tail) || notYetEn.MatchString(strings.Join(words, " "))
}

// stageOf reads a reply on its own. A structured closeout may name a PR, and unbound legacy tasks can use a single
// unambiguous PR from prose.
func stageOf(text string) (stage, pr string) {
	if deliveryCloseout(text) != "" {
		pr = prLink.FindString(closeoutPRRef(text))
		return stageFromReply(text, pr != ""), pr
	}
	pr, seen, _ := legacyReportPR(text)
	return stageFromReply(text, seen), pr
}

func stageFromReply(text string, sawPR bool) string {
	stage := "develop"
	if sawPR {
		stage = "review"
		if said(mergedWords, text) {
			stage = "merge"
		}
	}
	if said(deployWords, text) {
		stage = "deploy"
	}
	if stage == "deploy" && said(verifyWords, text) {
		stage = "verify" // checked on what was deployed
	}
	if d, ok := deliveryMark(text); ok {
		stage = "develop"
		if st := deliveryStage(d.State); st != "" {
			stage = st
		}
	}
	return stage
}

func (s *Service) replyRepositoryBindings(ctx context.Context, id string) ([]repository.TaskRepository, bool) {
	st, ok := s.store().(todoRepositoryStore)
	if !ok {
		return nil, true
	}
	set, err := st.TodoRepositories(ctx, id)
	if err != nil {
		s.logf("task %s: repository bindings could not be read: %v", id, err)
		return nil, false
	}
	return set.Repositories, true
}

// advanceTaskReply handles progress and final answers identically, including the running-hub deploy check.
// Without a recorded turn, ownership must come from the independent checkout read.
func (s *Service) advanceTaskReply(ctx context.Context, t Todo, machine, session, text string) bool {
	return s.advanceTaskReplyAt(ctx, t, machine, session, text, time.Time{})
}

func (s *Service) advanceTaskReplyAt(ctx context.Context, t Todo, machine, session, text string, at time.Time) bool {
	if cur, ok := s.todoNow(t.ID); ok {
		t.PRURL, t.PRs = cur.PRURL, cur.PRs
		t.Phase, t.WaitState = cur.Phase, cur.WaitState
	}
	structured := t.hasTaskRuntime()
	urls := taskRepositoryReportLinks(text)
	references := referenceReportLinks(text, urls)
	block := deliveryCloseout(text)
	var rows []repository.TaskRepository
	bindingsRead := true
	if len(urls) != 0 || block != "" || t.PRURL == "" && strings.Contains(text, "#") {
		rows, bindingsRead = s.replyRepositoryBindings(ctx, t.ID)
	}
	var pr string
	var deliveries []string
	var sawPR, legacy bool
	switch {
	case block != "":
		pr = prLink.FindString(closeoutPRRef(text))
		if pr == "" {
			pr = s.prFromNumber(ctx, t, machine, session, text)
		}
		_, artifacts, _ := closeout.Parse(block)
		for _, a := range artifacts {
			if u := strings.TrimSpace(a.Ref); a.Kind == "pr" && u != "" && prLink.FindString(u) == u {
				deliveries = append(deliveries, u)
			}
		}
		if pr != "" && !slices.Contains(deliveries, pr) {
			deliveries = append(deliveries, pr)
		}
		sawPR = pr != ""
	case !structured && bindingsRead && t.PRURL == "" && len(t.PRs) == 0:
		var reference bool
		pr, sawPR, reference = legacyReportPR(text)
		if !sawPR {
			pr = s.prFromNumber(ctx, t, machine, session, text)
			sawPR = pr != ""
		}
		if pr != "" && (reference || reportBindingsBlockAcceptedLegacy(rows, pr)) {
			pr = ""
		}
		if pr != "" {
			legacy = true
			deliveries = []string{pr}
		} else if !sawPR && !reportBindingsBlockLegacy(rows, "") {
			legacy = true
		}
	}
	var ownedCandidates []string
	for _, raw := range urls {
		if !structured && !references[raw] {
			ownedCandidates = append(ownedCandidates, raw)
		}
	}
	for _, raw := range deliveries {
		if (structured || !references[raw]) && !slices.Contains(ownedCandidates, raw) {
			ownedCandidates = append(ownedCandidates, raw)
		}
	}
	// Keep incidental built-in links as references even on replies that carry no stage update.
	// Delivery candidates go through importOwnedTaskRepositoryLinks, never this fallback.
	defer func() {
		if !bindingsRead || len(urls) == 0 {
			return
		}
		links, roles := reportLinkRoles(urls, deliveries, rows)
		var refLinks []string
		refRoles := map[string]string{}
		for _, raw := range links {
			if roles[raw] != repository.LinkRoleReference {
				continue
			}
			if _, _, err := repository.ParseTaskLink(raw); err != nil {
				continue
			}
			refLinks = append(refLinks, raw)
			refRoles[raw] = repository.LinkRoleReference
		}
		if len(refLinks) == 0 {
			return
		}
		if _, err := s.importReportedLinksWithRoles(ctx, t.ID, refLinks, "pull_request", nil, refRoles, nil); err != nil {
			s.logf("task %s: repository links not saved: %v", t.ID, err)
		}
	}()
	var stage string
	var d TodoDelivery
	var marked bool
	if !structured {
		stage = stageFromReply(text, sawPR)
		d, marked = deliveryMark(text)
	}
	var accepted []acceptedReportLink
	if len(ownedCandidates) != 0 {
		var err error
		accepted, err = s.importOwnedTaskRepositoryLinks(ctx, t, machine, session, ownedCandidates, text, at,
			func(ctx context.Context, accepted []acceptedReportLink) {
				link := taskReplyAcceptedPR(pr, accepted)
				if link.canonical == "" || len(s.taskRepositoryReportTurn(ctx, t, machine, session, text, at)) == 0 {
					return
				}
				current, active := s.openTodoOfAgent(machine, session)
				if !active || current.ID != t.ID ||
					!taskRepositoryAgentLinkedAt(current, machine, session).Equal(taskRepositoryAgentLinkedAt(t, machine, session)) ||
					current.PRURL != t.PRURL && !sameReplyPR(current.PRURL, link.canonical) {
					return
				}
				if deployed, _ := s.applyTaskReplyStage(ctx, current, machine, session, text, stage, link, len(accepted) > 0, d, marked, legacy); deployed {
					go s.askAcceptance(current, machine, session, text)
				}
			})
		if err != nil {
			s.logf("task %s: owned repository links not saved: %v", t.ID, err)
		}
	}
	link := taskReplyAcceptedPR(pr, accepted)
	deployed, written := s.applyTaskReplyStage(ctx, t, machine, session, text, stage, link, len(accepted) > 0, d, marked, legacy && link.canonical != "")
	if !written && legacy {
		deliveries = nil
		rows, bindingsRead = s.replyRepositoryBindings(ctx, t.ID)
		return false
	}
	if link.canonical != "" && legacy && !slices.Contains(urls, link.canonical) {
		urls = append(urls, link.canonical) // the lone legacy reference may have been a comment URL
	}
	return deployed
}

// Apply the stored spelling, including when the provider supplied it after the report was read.
func taskReplyAcceptedPR(pr string, accepted []acceptedReportLink) acceptedReportLink {
	if pr == "" && len(accepted) == 1 {
		return accepted[0] // a provider-specific URL can be absent from stageOf's syntax
	}
	for _, link := range accepted {
		if sameReplyPR(link.raw, pr) || sameReplyPR(link.canonical, pr) {
			return link
		}
	}
	return acceptedReportLink{}
}

func (s *Service) applyTaskReplyStage(ctx context.Context, t Todo, machine, session, text, stage string, link acceptedReportLink,
	hasOwnedPR bool, d TodoDelivery, marked, legacy bool) (deployed, written bool) {
	pr := link.canonical
	if t.hasTaskRuntime() {
		if pr == "" || stage != "" {
			return false, true // a legacy reply overtaken by a structured report has no stage evidence
		}
		return false, s.writeTaskStage(ctx, t.ID, "", pr, "", false, "", false, false)
	}
	if pr == "" && !hasOwnedPR && (stage == "review" || stage == "merge") {
		stage = "develop"
	}
	if changeSeen(text, d, marked) {
		s.restage(ctx, t.ID, func(st *todoStage) {
			if !st.hasTaskRuntime() {
				st.Change = true
			}
		})
	}
	if replyNamesOtherPR(text, cmp.Or(pr, t.PRURL), link.raw) && (!marked || d.State != deliveryNone) {
		if pr != "" || hasOwnedPR {
			if !s.writeTaskStage(ctx, t.ID, "review", pr, "", false, "", legacy, true) {
				return false, false
			}
			if pr != "" {
				s.learnRepoRoot(ctx, machine, session, pr)
			}
		}
		return false, true // the reported merge or deploy is not attributable to this task
	}
	if pr != "" && stage == "develop" && !marked {
		stage = "review" // as stageOf has it for a linked PR (#7921)
		if said(mergedWords, text) {
			stage = "merge"
		}
	}
	if !marked && stageIndex(stage) < stageIndex("merge") && pr == "" && t.PRURL != "" && said(mergedWords, text) {
		stage = "merge" // later replies need not repeat the link already kept on the task
	}
	if stage == "develop" && pr == "" && !marked {
		return false, true // most progress messages carry no new stage evidence
	}
	stage, deployed = s.gateDeployStage(ctx, t, stage, pr, machine, session, text)
	if !marked && !deployed {
		if cur, ok := s.todoNow(t.ID); ok {
			t = cur
		}
		if t.Delivery != nil {
			if st := deliveryStage(t.Delivery.State); st != "" {
				stage = st
			} else if t.Delivery.State == deliveryNone {
				stage = "develop"
			}
		}
	}
	if marked && (stage == deliveryStage(d.State) || d.State == deliveryNone) {
		d.At = time.Now().UTC()
		s.writeTaskDelivery(ctx, t.ID, d, true) // a held deploy check takes precedence over the agent's mark
	}
	if !s.writeTaskStage(ctx, t.ID, stage, pr, "", false, "", legacy, true) {
		return false, false
	}
	if pr != "" {
		s.learnRepoRoot(ctx, machine, session, pr)
	}
	return deployed, true
}

type reportItem struct {
	todo    string
	state   string
	machine string // a name
	session [2]string
	text    string
	ix      *proto.Interaction
	at      time.Time
	tries   int  // sends already attempted for this change (0: the first send is pending)
	cuts    int  // interrupted turns in a row so far, this one included (interrupted only)
	cutOff  bool // these cuts make the task stuck: the one reaching reportStuckAfter, or a later one replacing it unsent
}

type reportBook struct {
	stageWriting sync.Mutex // serialize stage/delivery storage through todo publication
	mu           sync.Mutex
	sending      sync.Mutex             // serializes sendReports: a later timer must not take a batch while a post is still in flight
	pending      map[string]*reportItem // by todo id: the latest change of each
	timer        *time.Timer
	stages       map[string]todoStage // by todo id (reportStagesKey)
	loaded       bool
	delay        time.Duration  // 0: reportDelay (the tests' is shorter)
	retry        time.Duration  // 0: reportRetryDelay (the tests' is shorter)
	cuts         map[string]int // by todo id: interrupted turns in a row (a finished or closed one resets it)
}

type todoStage struct {
	Stage string `json:"stage"`
	Kind  string `json:"stage_kind,omitempty"`
	// Readonly: its latest start handed it out read-only; Change: a reply showed a change made without its PR's link (a
	// closeout's commit or branch, a delivery held at a PR or a merge). What taskStageKind keeps reading (restage).
	Readonly bool          `json:"readonly,omitempty"`
	Change   bool          `json:"change,omitempty"`
	PR       string        `json:"pr,omitempty"`
	PRs      []string      `json:"prs,omitempty"`       // the PRs named before PR, newest last, at most stagePRsKept (#6507)
	Delivery *TodoDelivery `json:"delivery,omitempty"`  // delivery.go
	Deploy   *deployWatch  `json:"deploy,omitempty"`    // deploy_check.go: held until the running hub has the work
	Verified bool          `json:"verified,omitempty"`  // explicit acceptance outranks delivery and reply keywords
	Check    *TodoCheck    `json:"check,omitempty"`     // task_check.go
	PRStatus *TodoPRStatus `json:"pr_status,omitempty"` // Observed forge facts, not a task state.
	// EarlierPRStatus: the same facts for the PRs in PRs, by URL (#7935): work that spans several PRs waits for merge
	// while any of them is open, not only the last one named.
	EarlierPRStatus map[string]TodoPRStatus      `json:"prs_status,omitempty"`
	Phase           assistantwire.TaskPhase      `json:"phase,omitempty"`
	WaitState       *assistantwire.TaskWaitState `json:"wait_state,omitempty"`
	RuntimeAt       time.Time                    `json:"runtime_at,omitzero"`     // most recent report or observed start
	ObservedWait    bool                         `json:"observed_wait,omitempty"` // wait supplied by a provider
}

// otherPRFacts are st's facts for its earlier PRs, newest first: what Todo.OtherPRs carries.
func (st todoStage) otherPRFacts() []TodoPRStatus {
	var out []TodoPRStatus
	for i := len(st.PRs) - 1; i >= 0; i-- {
		if f, ok := st.EarlierPRStatus[st.PRs[i]]; ok && st.PRs[i] != st.PR {
			out = append(out, f)
		}
	}
	return out
}

const reportStagesKey = "assistant.todo_stages"

// empty: st keeps nothing (its row can go).
func (st todoStage) empty() bool {
	return st.Stage == "" && st.Kind == "" && !st.Readonly && !st.Change && st.PR == "" && len(st.PRs) == 0 && st.Delivery == nil &&
		st.Deploy == nil && !st.Verified && st.Check == nil && st.PRStatus == nil && len(st.EarlierPRStatus) == 0 && !st.hasTaskRuntime()
}

// stagePRsKept bounds a task's earlier PRs (todoStage.PRs).
const stagePRsKept = 20

// keptPRs is prs with pr moved to its end (a PR named again is the newest), at most stagePRsKept.
func keptPRs(prs []string, pr string) []string {
	out := append(slices.DeleteFunc(slices.Clone(prs), func(p string) bool { return p == pr }), pr)
	return out[max(0, len(out)-stagePRsKept):]
}

// stageMirrorsLink matches an unbound link to the current or an earlier stage PR. Rows saved before provenance was
// recorded use the same stage comparison (#17796).
func stageMirrorsLink(pr string, earlier []string, link repository.TaskLink) bool {
	if link.Provenance == repository.LinkProvenanceExplicit {
		return false
	}
	key := normalizePRKey(link.CanonicalURL)
	same := func(p string) bool { return normalizePRKey(p) == key }
	return key != "" && (same(pr) || slices.ContainsFunc(earlier, same))
}

// forgetTaskPR removes a non-delivery record and installs only an explicitly recovered delivery, never an
// unproven historical reference. A confirmed no-PR result also discards that reference history and its hold.
func (s *Service) forgetTaskPR(ctx context.Context, id, pr, replacement string) error {
	b := &s.reports
	b.stageWriting.Lock()
	defer b.stageWriting.Unlock()
	b.mu.Lock()
	if !s.loadStagesLocked(ctx) {
		b.mu.Unlock()
		return fmt.Errorf("task %s: stages could not be read", id)
	}
	cur := b.stages[id]
	if replacement == "" {
		if err := s.forgetReportedPRLinks(ctx, id, cur); err != nil {
			b.mu.Unlock()
			return err
		}
	}
	removed := cur.PR == pr
	history := slices.DeleteFunc(slices.Clone(cur.PRs), func(p string) bool { return p == pr })
	changed := removed || len(history) != len(cur.PRs)
	cur.PRs = history
	if removed {
		cur.PR = replacement
		if replacement != "" {
			cur.PRs = slices.DeleteFunc(cur.PRs, func(p string) bool { return p == replacement })
		}
		if cur.PR == "" {
			cur.PRs = nil
		}
		if cur.PRStatus != nil && (cur.PR == "" || cur.PRStatus.URL == pr) {
			cur.PRStatus = nil
		}
		if cur.PR == "" && !cur.Verified && (cur.Stage == "review" || cur.Stage == "merge") {
			cur.Stage = "develop"
		}
	}
	removedWatch := cur.Deploy != nil && (cur.Deploy.PR == pr || removed && cur.PR == "")
	if removedWatch {
		cur.Deploy, changed = nil, true
	}
	if (removed || removedWatch) && cur.Delivery != nil &&
		(cur.Delivery.State == deliveryPR || cur.Delivery.State == deliveryMerged) {
		cur.Delivery = nil
	}
	if !changed {
		b.mu.Unlock()
		return nil
	}
	stages := maps.Clone(b.stages)
	stages[id] = cur
	if st := s.store(); st != nil {
		if err := st.PutSetting(ctx, reportStagesKey, stages); err != nil {
			b.mu.Unlock()
			return fmt.Errorf("task %s: remove non-PR: %w", id, err)
		}
	}
	b.stages = stages
	b.mu.Unlock()
	s.todos.mu.Lock()
	if t := s.todos.todos[id]; t != nil {
		t.Stage, t.PRURL, t.PRs, t.Delivery, t.PRStatus = cur.Stage, cur.PR, cur.PRs, cur.Delivery, cur.PRStatus
	}
	s.todos.mu.Unlock()
	s.deploy.mu.Lock()
	delete(s.deploy.seen, id)
	s.deploy.mu.Unlock()
	s.todosChanged(ctx)
	return nil
}

// forgetReportedPRLinks removes reported PRs and retains other links. A saved delivery binding can appear while
// acceptance checks the forge, so recheck the links and require the revision read here when saving.
func (s *Service) forgetReportedPRLinks(ctx context.Context, id string, stage todoStage) error {
	st, ok := s.store().(todoRepositoryStore)
	if !ok {
		return nil
	}
	set, err := st.TodoRepositories(ctx, id)
	if err != nil {
		return fmt.Errorf("task %s: read repository links to remove a non-PR: %w", id, err)
	}
	for _, row := range set.Repositories {
		if slices.ContainsFunc(row.Links, func(link repository.TaskLink) bool {
			role, _ := repository.NormalizeLinkRole(link.Role)
			return link.Kind == "pull_request" && role == repository.LinkRoleDelivery &&
				!stageMirrorsLink(stage.PR, stage.PRs, link)
		}) {
			return fmt.Errorf("task %s: its delivery links changed during verification; retry the review", id)
		}
	}
	for _, row := range set.Repositories {
		kept := make([]repository.LinkInput, 0, len(row.Links))
		changed := false
		for _, link := range row.Links {
			if link.Kind == "pull_request" {
				changed = true
				continue
			}
			in := repository.LinkInput{URL: link.CanonicalURL, Title: link.Title, Role: link.Role}
			if repository.IsExtensionProvider(row.SourceSnapshot.Identity.Provider) {
				in.Resolved = &repository.ResolvedLink{Source: row.SourceSnapshot, Link: link}
			}
			kept = append(kept, in)
		}
		if !changed {
			continue
		}
		revision := set.Revision
		if _, err := st.SetTodoRepositoryLinks(ctx, id, row.RepoID, repository.LinksInput{ExpectedRevision: &revision, Links: kept}); err != nil {
			return fmt.Errorf("task %s: remove non-PR repository links: %w", id, err)
		}
		set.Revision++
	}
	return nil
}

// taskReport notes a change of task t for the next card. ix: what it asks (needs).
func (s *Service) taskReport(t Todo, state, machine string, session [2]string, text string, ix *proto.Interaction) {
	if t.Origin == "" || s.isLeaderRef(t.Origin) { // a task leader's worker is no task of the owner's chat (task_leader.go)
		return
	}
	b := &s.reports
	b.mu.Lock()
	defer b.mu.Unlock()
	if b.pending == nil {
		b.pending = map[string]*reportItem{}
	}
	it := &reportItem{todo: t.ID, state: state, machine: machine, session: session, text: text, ix: ix, at: time.Now()}
	switch state {
	case reportInterrupted:
		if b.cuts == nil {
			b.cuts = map[string]int{}
		}
		b.cuts[t.ID]++
		it.cuts = b.cuts[t.ID]
		prev := b.pending[t.ID]
		it.cutOff = it.cuts == reportStuckAfter || prev != nil && prev.cutOff // a stuck card still unsent is not lost
	case reportDone, reportClosed:
		delete(b.cuts, t.ID)
	}
	b.pending[t.ID] = it
	if b.timer == nil {
		d := b.delay
		if d <= 0 {
			d = reportDelay
		}
		b.timer = time.AfterFunc(d, s.sendReports)
	}
}

// setTaskStage moves task id to stage (never back) with its PR, kept in the store and on the todo.
func (s *Service) setTaskStage(ctx context.Context, id, stage, pr string) {
	s.setTaskStageKind(ctx, id, stage, pr, "", false)
}

func (s *Service) setTaskStageKind(ctx context.Context, id, stage, pr, kind string, verified bool) {
	if pr != "" {
		if _, err := s.importReportedLinks(ctx, id, []string{pr}, "", nil, nil); err != nil {
			s.logf("task %s: repository link not saved: %v", id, err)
		}
	}
	s.setTaskStageOn(ctx, id, stage, pr, kind, verified, "")
}

// setTaskStageOn is setTaskStageKind, and when onPR is set it does nothing unless, under the stage lock, the open to-do
// and its stage still name that PR: an observation of a PR that was replaced meanwhile moves no stage (#7614 review).
func (s *Service) setTaskStageOn(ctx context.Context, id, stage, pr, kind string, verified bool, onPR string) {
	s.writeTaskStage(ctx, id, stage, pr, kind, verified, onPR, false, false)
}

// writeTaskStage checks the prior PR under the same lock that publishes stage updates. A legacy fallback may fill an
// empty PR slot only while there is no target or candidate binding, including when another reply overtook it.
func (s *Service) writeTaskStage(ctx context.Context, id, stage, pr, kind string, verified bool, onPR string, legacy, prose bool) bool {
	if !verified && stage == "verify" {
		if t, ok := s.todoNow(id); ok && t.Supervision != nil {
			v := t.Supervision
			if v.state(t) == reviewRework || v.state(t) == reviewNeedsUser || userCheckAsked(t) {
				stage = "deploy" // a keyword cannot pass a check the review left outstanding
			}
		}
	}
	var completionMode assistantwire.CompletionMode
	if pr != "" {
		brief, _ := s.committedBriefOfTodo(id)
		completionMode = brief.CompletionModeNow
	}
	b := &s.reports
	b.stageWriting.Lock()
	defer b.stageWriting.Unlock()
	if onPR != "" || legacy {
		// The to-do's PR changes only under stageWriting (below), so it is read before b.mu, in the usual lock order.
		if t, ok := s.todoNow(id); !ok || t.DoneAt != nil || t.PRURL != onPR {
			return false
		}
	}
	if legacy {
		if rows, ok := s.replyRepositoryBindings(ctx, id); !ok || reportBindingsBlockAcceptedLegacy(rows, pr) {
			return false
		}
	}
	b.mu.Lock()
	if !s.loadStagesLocked(ctx) {
		b.mu.Unlock()
		s.logf("task %s: stage %s not kept: the stages could not be read", id, stage)
		return false
	}
	cur := b.stages[id]
	if prose && cur.hasTaskRuntime() {
		b.mu.Unlock()
		return false
	}
	if (onPR != "" || legacy) && cur.PR != onPR {
		b.mu.Unlock()
		return false
	}
	changed := false
	if verified && !cur.Verified {
		cur.Verified, changed = true, true
	}
	if stage == "unverified" {
		// A later review requests another check: an older acceptance or keyword match is not a pass.
		if cur.Verified {
			cur.Verified, changed = false, true
		}
		stage = ""
		if cur.Stage == "verify" {
			cur.Stage, changed = "deploy", true
			if cur.Delivery != nil && deliveryStage(cur.Delivery.State) != "" {
				cur.Stage = deliveryStage(cur.Delivery.State)
			}
		}
	}
	if cur.Verified {
		stage = "verify"
	}
	if pr != "" || cur.PR != "" {
		kind = "code"
	}
	if kind != "" && kind != cur.Kind {
		cur.Kind, changed = kind, true
	}
	if stageIndex(stage) > stageIndex(cur.Stage) {
		cur.Stage, changed = stage, true
	}
	if cur.bindPR(pr) {
		changed = true
		if cur.hasTaskRuntime() {
			cur.refreshRuntimeDelivery(time.Now(), completionMode)
		}
	}
	if changed {
		if err := s.keepTodoStageLocked(ctx, id, cur); err != nil {
			b.mu.Unlock()
			s.logf("task %s: stage: %v", id, err)
			return false
		}
	}
	b.mu.Unlock()
	if !changed {
		return true
	}
	s.todos.mu.Lock()
	if t := s.todos.todos[id]; t != nil {
		t.Stage, t.PRURL, t.PRs, t.StageKind, t.stageVerified = cur.Stage, cur.PR, cur.PRs, cur.Kind, cur.Verified
		t.PRStatus, t.OtherPRs = cur.PRStatus, cur.otherPRFacts()
		t.Delivery = cur.Delivery
		cur.applyRuntime(t)
	}
	s.todos.mu.Unlock()
	s.todosChanged(ctx)
	return true
}

// bindPR changes the current delivery reference and keeps its earlier facts
// with their own references.
func (st *todoStage) bindPR(pr string) bool {
	if pr == "" || pr == st.PR {
		return false
	}
	if st.PR != "" {
		st.PRs = keptPRs(st.PRs, st.PR)
	}
	st.PRs = slices.DeleteFunc(slices.Clone(st.PRs), func(u string) bool { return u == pr })
	facts := maps.Clone(st.EarlierPRStatus)
	if facts == nil {
		facts = map[string]TodoPRStatus{}
	}
	if st.PRStatus != nil && st.PRStatus.URL == st.PR && st.PR != "" {
		facts[st.PR] = *st.PRStatus
	}
	st.PRStatus = nil
	if f, ok := facts[pr]; ok {
		st.PRStatus = &f
	}
	st.EarlierPRStatus = nil
	for _, u := range st.PRs {
		if f, ok := facts[u]; ok {
			if st.EarlierPRStatus == nil {
				st.EarlierPRStatus = map[string]TodoPRStatus{}
			}
			st.EarlierPRStatus[u] = f
		}
	}
	st.PR, st.Kind = pr, "code"
	if st.hasTaskRuntime() && st.Deploy != nil && st.Deploy.PR != pr {
		st.Deploy = nil
	}
	return true
}

// loadStagesLocked reads the stages once and reports whether they are in memory; s.reports.mu is held. A failed read
// leaves them unloaded (the next look tries again): writing the book after it would put one task's stage over every
// other task's stage, PR and delivery.
func (s *Service) loadStagesLocked(ctx context.Context) bool {
	b := &s.reports
	if b.loaded {
		return true
	}
	stages := map[string]todoStage{}
	if st := s.store(); st != nil {
		if _, err := st.Setting(ctx, reportStagesKey, &stages); err != nil {
			s.logf("todo stages: %v", err)
			return false
		}
		if stages == nil {
			stages = map[string]todoStage{}
		}
	}
	b.stages, b.loaded = stages, true
	return true
}

// todoStages are the kept stages, for the todos as they are read.
func (s *Service) todoStages(ctx context.Context) map[string]todoStage {
	b := &s.reports
	b.mu.Lock()
	defer b.mu.Unlock()
	if !s.loadStagesLocked(ctx) {
		return map[string]todoStage{}
	}
	out := make(map[string]todoStage, len(b.stages))
	for k, v := range b.stages {
		out[k] = v
	}
	return out
}

// sendReports sends the changes noted since the last card: one card, the decisions first. Nothing goes out while the
// bot is off, unpaired or its reports switch is off (the changes are dropped: a later card says how things are then).
// A transport failure is not a drop: the batch goes back to pending and is retried (requeueReports).
func (s *Service) sendReports() {
	b := &s.reports
	// Hold sending for the whole post: a normal-delay timer that fires while a long post is still in flight waits
	// here, so its run cannot snapshot the newer items a failed run needs to merge with.
	b.sending.Lock()
	defer b.sending.Unlock()
	b.mu.Lock()
	items := make([]*reportItem, 0, len(b.pending))
	for _, it := range b.pending {
		items = append(items, it)
	}
	b.pending, b.timer = nil, nil
	b.mu.Unlock()
	if len(items) == 0 {
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 60*time.Second)
	defer cancel()
	f, ready := s.chat().Features(ctx)
	if !ready || !f.Reports {
		return
	}
	var keep, asks []*reportItem // asks: decisions whose own card goes out without a line in the report (only_conclusions)
	quiet := s.quietModeOn(ctx)
	for _, it := range items {
		t, ok := s.todoNow(it.todo)
		if !ok {
			continue // deleted meanwhile
		}
		if it.state == reportNeeds && !s.taskWaits(ctx, t) {
			continue // answered meanwhile
		}
		if it.state == reportNeeds && it.ix != nil && s.botCardOut(it.ix.ID) {
			continue // its question card is in the chat already (cards / all_cards): that says it waits
		}
		if t.Quiet && f.Sync && it.state != reportClosed {
			continue // told in the main conversation, whose turns the chat mirrors (sync); closing is not told there
		}
		if quiet {
			// Low-distraction mode (push_quiet.go): no progress, stuck or conclusion lines in the chat; the briefing and the
			// board have them. A decision still gets its own answerable card.
			if it.state == reportNeeds && it.ix != nil {
				asks = append(asks, it)
			}
			continue
		}
		if !f.ReportSteps {
			if it.state == reportNeeds && it.ix != nil {
				asks = append(asks, it) // the decision is the push: its own answerable card
			}
			if !it.stuck() {
				continue // an execution step: a turn ended, was cut off once, or the judge closed it (#3218)
			}
		} else if f.OnlyConclusions && !reportConclusion(it.state) {
			if it.state == reportNeeds && it.ix != nil {
				asks = append(asks, it) // the decision itself still reaches the owner
			}
			continue
		}
		keep = append(keep, it)
	}
	if len(keep) == 0 {
		s.postReportAsks(asks)
		return
	}
	sort.SliceStable(keep, func(i, j int) bool {
		if a, b := reportOrder[keep[i].state], reportOrder[keep[j].state]; a != b {
			return a < b
		}
		if !keep[i].at.Equal(keep[j].at) {
			return keep[i].at.Before(keep[j].at)
		}
		return keep[i].todo < keep[j].todo // identical timestamps stay in a stable, repost-safe order
	})
	var cards []*botcard.Card
	var fallbacks []string
	if f.ReportSteps {
		cards, fallbacks = s.reportCards(ctx, keep)
	} else {
		c, fb := s.goalCard(ctx, keep)
		cards, fallbacks = []*botcard.Card{c}, []string{fb}
	}
	// A notifications companion takes the report (feishu_companions.go); the decisions' own cards, which answer for
	// the owner, still go to the chat below.
	if s.divertNotices(ctx, assistantwire.NoticeReport(), cards, fallbacks) {
		s.postReportAsks(append(asks, keep...))
		return
	}
	// Every card must land: a failure after some went out requeues the whole batch (reposts early ones, retry-capped).
	for i, c := range cards {
		if _, err := s.chat().Post(ctx, larkhost.Target{}, "", c, fallbacks[i], ""); err != nil {
			s.logf("bots: report: %v", err)
			s.requeueReports(append(keep, asks...)) // the decisions held back from the card are not lost with it
			return
		}
	}
	s.postReportAsks(append(asks, keep...))
}

// postReportAsks: a decision to make gets its own card with the choices, answerable there, unless one is already in the chat.
func (s *Service) postReportAsks(items []*reportItem) {
	for _, it := range items {
		if it.state == reportNeeds && it.ix != nil && !s.botCardOut(it.ix.ID) {
			// Its card goes out whatever the cards switches say; answering it goes as any card's (the hub checks the
			// owner pressed it).
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			s.larkModule().AskPost(ctx, *it.ix)
			cancel()
		}
	}
}

// stuck: the change says the task's goal is stuck — it did not start, or its turns were cut off reportStuckAfter times in
// a row (told once, on the turn that makes it so).
func (it *reportItem) stuck() bool {
	return it.state == reportFailed || it.state == reportInterrupted && it.cutOff
}

// goalCard is the goal-level card for stuck tasks: for each, the goal, where it is now and one line of evidence with its
// link (#3218). items are sorted; past goalCardMaxItems a line counts the rest.
func (s *Service) goalCard(ctx context.Context, items []*reportItem) (*botcard.Card, string) {
	l, web := s.botLang(ctx), s.webBase(ctx)
	names := s.feishuDirections(ctx)
	var titles, lines []string
	for _, it := range items[:min(len(items), goalCardMaxItems)] {
		t, _ := s.todoNow(it.todo)
		direction := s.feishuDirection(ctx, t, names)
		titles = append(titles, direction+clip(oneLine(t.Title), 40))
		if len(items) == 1 {
			direction = "" // the header already identifies this row
		}
		lines = append(lines, goalLines(l, web, t, it, direction))
	}
	status := i18n.T(l, "assistant-reports.goal_stuck_title", i18n.Args{"n": len(items)})
	title := reportTitle(l, titles, status)
	c := botcard.NewCard(title, botcard.Red, title).Tag(i18n.T(l, "assistant-reports.tag_stuck"), botcard.Red)
	for i, line := range lines {
		if i > 0 {
			c.HR()
		}
		c.Markdown(line)
	}
	if len(items) > len(lines) {
		c.HR()
		c.Markdown(i18n.T(l, "assistant-reports.goal_more", i18n.Args{"n": len(items) - len(lines)}))
	}
	return c, i18n.T(l, "assistant-reports.goal_summary", i18n.Args{"title": status,
		"tasks": clip(strings.Join(titles, i18n.T(l, "assistant-tasks.list_sep")), reportFallbackMax)})
}

// goalLines are a stuck task's three lines: its goal, where it is now, and the evidence — the error in a line and a link
// (the agent's conversation, else the task's page).
func goalLines(l i18n.Lang, web string, t Todo, it *reportItem, direction string) string {
	now := i18n.T(l, "assistant-reports.goal_now_failed")
	if it.state == reportInterrupted {
		now = i18n.T(l, "assistant-reports.goal_now_cut", i18n.Args{"n": it.cuts})
	}
	link := todoLink(web, t.ID)
	if it.session[1] != "" && web != "" {
		link = sessionLink(web, it.session[0], it.session[1], false)
	}
	evidence := botcard.EscapeMarkdown(clip(oneLine(it.text), 80))
	if link != "" {
		a := "[" + i18n.T(l, "assistant-reports.goal_open") + "](" + link + ")"
		evidence = strings.TrimSpace(evidence + " " + a)
	}
	if evidence == "" {
		evidence = i18n.T(l, "assistant-reports.goal_no_evidence")
	}
	return i18n.T(l, "assistant-reports.goal_lines", i18n.Args{"goal": botcard.EscapeMarkdown(direction + clip(oneLine(t.Title), 80)),
		"now": now, "evidence": evidence})
}

// reportConclusion says a report state tells how a task ended (closed by the judge, or did not start), the lines
// only_conclusions keeps.
func reportConclusion(state string) bool { return state == reportClosed || state == reportFailed }

// dropTaskReport forgets task id's change still waiting for the next card: its conclusion card tells how it ended.
func (s *Service) dropTaskReport(id string) {
	b := &s.reports
	b.mu.Lock()
	delete(b.pending, id)
	b.mu.Unlock()
}

// requeueReports puts a batch the transport could not deliver back for a short-delayed retry. It keeps the semantic
// drops sendReports already made (deleted, answered, quiet-synced items are not in the batch). A change of the same
// todo that arrived while the send was in flight replaces the failed one when it is newer; items that have used
// reportMaxRetries retries are given up (and logged). Whatever is pending afterwards — requeued or newly arrived —
// gets one timer.
func (s *Service) requeueReports(items []*reportItem) {
	b := &s.reports
	b.mu.Lock()
	if b.pending == nil {
		b.pending = map[string]*reportItem{}
	}
	var gaveUp []string
	for _, it := range items {
		if cur, ok := b.pending[it.todo]; ok && cur.at.After(it.at) {
			if it.cutOff && cur.state == reportInterrupted {
				cur.cutOff = true // the newer cut still tells the stuck task the failed batch carried
			}
			continue // a newer change of this todo arrived meanwhile
		}
		if it.tries >= reportMaxRetries {
			gaveUp = append(gaveUp, it.todo)
			continue
		}
		it.tries++
		b.pending[it.todo] = it
	}
	if len(b.pending) > 0 {
		if b.timer != nil {
			b.timer.Stop() // a new change's normal-delay timer: the failed batch gets the short retry delay
		}
		d := b.retry
		if d <= 0 {
			d = reportRetryDelay
		}
		b.timer = time.AfterFunc(d, s.sendReports)
	}
	b.mu.Unlock()
	for _, id := range gaveUp {
		s.logf("bots: report: giving up task %s after %d failed sends", id, reportMaxRetries+1)
	}
}

// botCardOut: the bot has posted (or is posting) a card for interaction id.
func (s *Service) botCardOut(id string) bool {
	ok := false
	s.larkModule().Book(context.Background(), func(b assistantwire.ChatBook) { _, ok = b.Card(id) })
	return ok
}

type reportEntry struct {
	title   string
	md      string
	buttons []botcard.Button
	bytes   int // marshaled JSON length of its Markdown block plus button row
}

func (s *Service) reportCards(ctx context.Context, items []*reportItem) ([]*botcard.Card, []string) {
	l, web, stages := s.botLang(ctx), s.webBase(ctx), s.todoStages(ctx)
	names := s.feishuDirections(ctx)
	es, needs := make([]reportEntry, len(items)), 0
	for i, it := range items {
		if it.state == reportNeeds {
			needs++
		}
		t, _ := s.todoNow(it.todo)
		direction := s.feishuDirection(ctx, t, names)
		title := direction + clip(oneLine(t.Title), 40)
		if len(items) == 1 {
			direction = ""
		}
		md, buttons := s.reportItemView(l, web, stages, t, it, direction)
		es[i] = reportEntry{title: title, md: md, buttons: buttons}
		es[i].bytes = feishuElBytes(func(c *botcard.Card) {
			c.Markdown(md)
			if len(buttons) > 0 {
				c.Buttons(buttons...)
			}
		})
	}
	head1, headN := reportCardHeads(l, needs, len(items))
	hr := feishuElBytes(func(c *botcard.Card) { c.HR() })
	segs := reportCardSegments(es, head1, headN, hr)
	cards, fallbacks := make([]*botcard.Card, 0, len(segs)), make([]string, 0, len(segs))
	offset := 0
	for k, seg := range segs {
		c, fb := reportCardOne(l, seg, k, len(segs), offset, needs, len(items))
		cards, fallbacks, offset = append(cards, c), append(fallbacks, fb), offset+len(seg)
	}
	return cards, fallbacks
}

func feishuElBytes(add func(*botcard.Card)) int {
	return botcard.ElementBytes(add)
}

// Several tasks may share a report. Name the tasks on this card in delivery
// order, within a bounded header; the complete entries follow in the body.
const reportTitleRunes = 140

func reportTitle(l i18n.Lang, titles []string, status string) string {
	if len(titles) == 0 {
		return status
	}
	return clip(strings.Join(titles, i18n.T(l, "assistant-tasks.list_sep")), reportTitleRunes) + " · " + status
}

// reportCardHeads measures the first card (longest title, both section labels charged) and the plain later cards.
func reportCardHeads(l i18n.Lang, needs, total int) (head1, headN int) {
	part := i18n.T(l, "assistant-reports.part", i18n.Args{"k": total, "n": total})
	first := i18n.T(l, "assistant-reports.title", i18n.Args{"n": total}) + part
	if needs > 0 {
		first = i18n.T(l, "assistant-reports.title_needs", i18n.Args{"n": needs}) + part
	}
	c1 := reportCardStart(l, first, botcard.Orange)
	c1.Markdown("**" + i18n.T(l, "assistant-reports.decide") + "**")
	c1.Markdown("**" + i18n.T(l, "assistant-reports.progress") + "**")
	plain := i18n.T(l, "assistant-reports.title", i18n.Args{"n": total}) + part
	cN := reportCardStart(l, plain, botcard.Blue)                      // suffix measured with the longest part too
	cN.Markdown("**" + i18n.T(l, "assistant-reports.progress") + "**") // a progress label may land past card 1
	// Each escaped rune in the new header and its 60-rune chat preview costs
	// at most six JSON bytes (e.g. a control character).
	const titleBudget = (reportTitleRunes + 3 + 60) * 6
	return len(c1.JSON()) + titleBudget, len(cN.JSON()) + titleBudget
}

// reportCardSegments keeps the report entry API on botcard's generic packer.
func reportCardSegments(es []reportEntry, head1, headN, hr int) [][]reportEntry {
	return botcard.Segments(es, head1, headN, hr, func(e reportEntry) (bytes, elements int) {
		elements = 1
		if len(e.buttons) > 0 {
			elements++ // its button row
		}
		return e.bytes, elements
	})
}

// reportCardOne builds one segment; k is its 0-based index of n, off its first item's global position.
func reportCardOne(l i18n.Lang, seg []reportEntry, k, n, off, needs, total int) (*botcard.Card, string) {
	title, template := i18n.T(l, "assistant-reports.title", i18n.Args{"n": total}), botcard.Blue
	if needs > 0 && k == 0 { // only the first card carries the decisions
		title, template = i18n.T(l, "assistant-reports.title_needs", i18n.Args{"n": needs}), botcard.Orange
	}
	if n > 1 {
		title += i18n.T(l, "assistant-reports.part", i18n.Args{"k": k + 1, "n": n})
	}
	titles := make([]string, len(seg))
	for i, e := range seg {
		titles[i] = e.title
	}
	header := reportTitle(l, titles, title)
	c := reportCardStart(l, header, template)
	for i, e := range seg {
		if i > 0 {
			c.HR()
		}
		if g := off + i; g == 0 && needs > 0 {
			c.Markdown("**" + i18n.T(l, "assistant-reports.decide") + "**")
		} else if g == needs && needs > 0 {
			c.Markdown("**" + i18n.T(l, "assistant-reports.progress") + "**")
		}
		c.Markdown(e.md)
		if len(e.buttons) > 0 {
			c.Buttons(e.buttons...)
		}
	}
	return c, title + "：" + clip(strings.Join(titles, i18n.T(l, "assistant-tasks.list_sep")), reportFallbackMax)
}

// reportCardStart is a report card's header: its title, its colour (orange while it carries decisions) and its kind.
func reportCardStart(l i18n.Lang, title, template string) *botcard.Card {
	return botcard.NewCard(title, template, title).Tag(i18n.T(l, "assistant-reports.tag_report"), botcard.Neutral)
}

// reportStateColors are the colours of an item's state tag: waiting for the owner orange, failed or cut off red, a
// turn finished blue (the task goes on), done green.
var reportStateColors = map[string]string{reportNeeds: botcard.Orange, reportFailed: botcard.Red,
	reportInterrupted: botcard.Red, reportDone: botcard.Blue, reportClosed: botcard.Green}

// reportItemView renders one item's Markdown body and its link buttons (task page, PR, agent chat).
func (s *Service) reportItemView(l i18n.Lang, web string, stages map[string]todoStage, t Todo, it *reportItem, direction string) (string, []botcard.Button) {
	var md strings.Builder
	color := reportStateColors[it.state]
	if color == "" {
		color = botcard.Neutral
	}
	md.WriteString(botcard.InlineTag(i18n.T(l, "assistant-reports.state_"+it.state), color) + " **" + botcard.EscapeMarkdown(direction+clip(oneLine(t.Title), 40)) + "**")
	if it.machine != "" {
		md.WriteString(" · " + botcard.EscapeMarkdown(it.machine))
	}
	if t.StageKind != "general" {
		if t.Stage == "" {
			t.Stage = stages[t.ID].Stage
		}
		md.WriteString("\n" + stageBar(l, t.Stage, stageDone(t)))
	}
	if text := strings.TrimSpace(it.text); text != "" {
		// Charts become numbers first, as the bot's answers and the briefing card do: clipMarkdown drops a persona-card
		// block it cannot draw down to its title, so a chart in the answer would reach the owner without its numbers.
		md.WriteString("\n" + botcard.CardMarkdown(clipMarkdown(botcard.PersonaCards(text), 300)))
	}
	if it.state == reportNeeds {
		md.WriteString("\n" + i18n.T(l, "assistant-reports.answer_below"))
	}
	var bs []botcard.Button
	if link := todoLink(web, t.ID); link != "" {
		bs = append(bs, botcard.PageBtn(i18n.T(l, "assistant-reports.open_task"), link))
	}
	if pr := stages[t.ID].PR; pr != "" {
		bs = append(bs, botcard.LinkBtn(i18n.T(l, "assistant-reports.open_pr"), pr))
	}
	if it.session[1] != "" && web != "" {
		bs = append(bs, botcard.PageBtn(i18n.T(l, "assistant-reports.open_agent"), sessionLink(web, it.session[0], it.session[1], false)))
	}
	return md.String(), bs
}

// stageBar draws the stages with the one reached marked: ✅ before it, 🔵 on it, ⚪ after (none known: all ⚪). done: the
// work ended there with nothing left to deploy (stageDone): the steps it went through, then Done, all ✅.
func stageBar(l i18n.Lang, stage string, done bool) string {
	at := stageIndex(stage)
	if done && at >= 0 {
		parts := make([]string, 0, at+2)
		for _, st := range taskStages[:at+1] {
			parts = append(parts, "✅"+i18n.T(l, "assistant-reports.stage_"+st))
		}
		return strings.Join(append(parts, "✅**"+i18n.T(l, "assistant-reports.stage_done")+"**"), " → ")
	}
	parts := make([]string, len(taskStages))
	for i, st := range taskStages {
		mark := "⚪"
		switch {
		case i < at:
			mark = "✅"
		case i == at:
			mark = "🔵"
		}
		name := i18n.T(l, "assistant-reports.stage_"+st)
		if i == at {
			name = "**" + name + "**"
		}
		parts[i] = mark + name
	}
	return strings.Join(parts, " → ")
}

// taskAsks notes a card of a task's agent (not a quiet one: InteractionRaised notes those) for the next report.
func (s *Service) taskAsks(ix proto.Interaction) {
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()
	t, ok := s.taskOf(ctx, ix.MachineID, ix.SessionID)
	if !ok || t.Quiet {
		return
	}
	name := ix.MachineID
	if m, ok := s.device(ctx, ix.MachineID); ok {
		name = m.Name
	}
	s.taskReport(t, reportNeeds, name, [2]string{ix.MachineID, ix.SessionID}, noticeAsk(ix), &ix)
}

// taskClosedReport: the judge closed task t (reason: why). Its done report still waiting becomes this one, keeping
// what the agent said; else the reason is the line.
func (s *Service) taskClosedReport(t Todo, reason string) {
	b := &s.reports
	b.mu.Lock()
	if cur, ok := b.pending[t.ID]; ok && cur.state == reportDone {
		cur.state = reportClosed
		b.mu.Unlock()
		return
	}
	b.mu.Unlock()
	s.taskReport(t, reportClosed, "", [2]string{}, reason, nil)
}
