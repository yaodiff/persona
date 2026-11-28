package assistant

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/lazyre"
)

// Deployed needs evidence (#3205, docs/assistant/todo-status-sop.md "Deployed needs evidence"): a task whose work is a
// GitHub PR is "deployed" only when the commit the running hub was built from (its /api/v1/health commit, or the
// -g<hash> of its version) contains the PR's merge commit, which GitHub's compare answers. Until then the task is held
// at "merged, not deployed" (the stage bar stops at merge, the delivery says so, no "Please verify") and a watch looks
// again whenever the hub's commit changes: once the deploy lands it moves to deployed and only then asks to verify.
//
// What the check cannot speak for keeps the agent's word, logged as unverified: work that is not a GitHub PR, a PR in a
// repository the hub is not built from (the hub's commit is not in it), or a hub that does not say its commit (a local
// or Docker build). A check that fails (GitHub or the hub's health unreachable) never says deployed.

// Results of a deploy check.
const (
	deployIn         = "in"         // the running hub contains the change, by merge commit or local build receipt
	deployOut        = "out"        // merged; the running hub does not contain it yet
	deployUnmerged   = "unmerged"   // the PR is not merged
	deployUnknown    = "unknown"    // could not tell (network, rate limit): not deployed
	deployUnverified = "unverified" // nothing to check it against: the agent's word stands
	// deployNotIn: the deployed-commits file of the running hub does not name the PR and GitHub cannot say more: not
	// in the running hub as far as the hub knows (merged and not deployed, or not merged).
	deployNotIn = "not_in"
)

type deployResult struct {
	State     string
	Hub       string        // the running hub's commit
	Merge     string        // the upstream PR's confirmed merge commit
	Receipt   deployReceipt // the running build names this PR
	NotMerged bool          // the upstream PR was confirmed unmerged, even when the hub runs it
	Err       error
}

// held: the result keeps the work short of deployed.
func (r deployResult) held() bool {
	return r.State == deployOut || r.State == deployUnmerged || r.State == deployUnknown || r.State == deployNotIn
}

// prForge answers what a deploy check asks of where the PR lives.
type prForge interface {
	// IsPR distinguishes a pull request from an issue or a confirmed missing item.
	IsPR(ctx context.Context, repo string, number int) (bool, error)
	// PRMerge: whether PR number of repo (owner/name) is merged, and its merge commit.
	PRMerge(ctx context.Context, repo string, number int) (merged bool, sha string, err error)
	// Contains: whether head contains base in repo; found false when either is not a commit of repo.
	Contains(ctx context.Context, repo, base, head string) (contains, found bool, err error)
}

// issueStateForge is an optional capability on prForge that reports a GitHub issue's state ("open" or "closed").
type issueStateForge interface {
	IssueState(ctx context.Context, repo string, number int) (state string, err error)
}

// hubBuildReader is a hub that can say what it runs (BusHub: its /api/v1/health).
type hubBuildReader interface {
	HubBuild(ctx context.Context) (version, commit string, err error)
}

// deployProbe is how a Service checks deploys: the hub's build and the forge. Nil fields are the real ones.
type deployProbe struct {
	hubBuild func(ctx context.Context) (version, commit string, err error)
	forge    prForge

	mu    sync.Mutex
	cache map[string]deployCached // by PR URL + hub commit
	seen  map[string]prSeen       // by todo id: when sweepPRs last checked an open to-do's PR (memory only)
	// githubDown: GitHub failed to answer until then (a hub that cannot reach it waits on no timeouts meanwhile).
	githubDown    time.Time
	githubFailure error

	// listPaths are where the deployed-commits file is looked for (nil: deployedFiles; tests set their own), files
	// the ones read (deployed_commits.go).
	listPaths []string
	files     deployedFileCache
}

// prSeen is sweepPRs' last look at a to-do's PR: against which hub commit, what it said, when.
type prSeen struct {
	hub, state string
	at         time.Time
	deployPR   string
	statusPR   string
	statusAt   time.Time
}

type deployCached struct {
	r       deployResult
	at      time.Time
	hasList bool
}

// deployCacheFor is how long a result other than deployIn is reused (deployIn holds for good for that hub commit).
const deployCacheFor = 2 * time.Minute

// describeHash is the commit in a version as deploy.sh and publish.sh write it, "$(git describe --tags --match 'v[0-9]*'
// --always --dirty)-$BUILD":
// <tag>-<n>-g<hash>[-dirty]-<build>, or with no tag <hash>[-dirty]-<build>.
var describeHash = lazyre.New(`(?:-g|^)([0-9a-f]{7,40})(?:-dirty)?-[0-9]{12}$`)

// hubCommitOf is the commit a hub runs: the build's commit ("-dirty" taken off), else the hash of its git describe
// version; "" when neither says (a version that is only a tag names no commit).
func hubCommitOf(version, commit string) string {
	commit = strings.TrimSuffix(strings.TrimSpace(commit), "-dirty")
	if isHex(commit) && len(commit) >= 7 {
		return strings.ToLower(commit)
	}
	if m := describeHash.FindStringSubmatch(version); m != nil {
		return m[1]
	}
	return ""
}

func isHex(s string) bool {
	if s == "" {
		return false
	}
	for _, r := range s {
		if !(r >= '0' && r <= '9' || r >= 'a' && r <= 'f' || r >= 'A' && r <= 'F') {
			return false
		}
	}
	return true
}

// runningHubCommit is the commit of the hub the assistant talks to ("" with a nil error: it does not say).
func (s *Service) runningHubCommit(ctx context.Context) (string, error) {
	read := s.deploy.hubBuild
	if read == nil {
		h, ok := s.hub.(hubBuildReader)
		if !ok {
			return "", nil
		}
		read = h.HubBuild
	}
	version, commit, err := read(ctx)
	if err != nil {
		return "", err
	}
	return hubCommitOf(version, commit), nil
}

// checkDeployed says whether the running hub contains the PR's change.
func (s *Service) checkDeployed(ctx context.Context, pr string) deployResult {
	return s.checkPRDeployment(ctx, pr, false)
}

// Closeouts also check upstream merge status when a running-build receipt already proves deployment.
func (s *Service) checkPRDeployment(ctx context.Context, pr string, checkMerge bool) deployResult {
	m := ciPRURL.FindStringSubmatch(pr)
	if m == nil {
		return deployResult{State: deployUnverified}
	}
	repo := m[1]
	number, _ := strconv.Atoi(m[2])
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	hub, err := s.runningHubCommit(ctx)
	if err != nil {
		return deployResult{State: deployUnknown, Err: fmt.Errorf("the hub's health: %w", err)}
	}
	if hub == "" {
		return deployResult{State: deployUnverified}
	}
	// A repaired receipt changes what we know about the running build, including during a GitHub pause.
	l := s.deployedListFor(hub)
	hasList := l != nil && strings.EqualFold(l.repo, repo)
	var receipt deployReceipt
	if hasList {
		receipt = l.prs[number]
	}
	if receipt.sha != "" && !checkMerge {
		return deployResult{State: deployIn, Hub: hub, Receipt: receipt}
	}
	key := pr + "\x00" + hub
	p := &s.deploy
	p.mu.Lock()
	if c, ok := p.cache[key]; ok && c.r.Receipt == receipt && c.hasList == hasList &&
		(c.r.State == deployIn && c.r.Merge != "" || time.Since(c.at) < deployCacheFor) {
		p.mu.Unlock()
		return c.r
	}
	paused := time.Now().Before(p.githubDown)
	p.mu.Unlock()
	var r deployResult
	if receipt.sha != "" {
		r = deployResult{State: deployIn, Hub: hub, Receipt: receipt}
		if !paused {
			merged, sha, err := s.forge().PRMerge(ctx, repo, number)
			if err == nil {
				if merged {
					r.Merge = sha
				} else {
					r.NotMerged = true
				}
			}
		}
	} else {
		r = s.compareGitHub(ctx, repo, number, hub)
		if hasList && (r.State == deployUnknown || r.State == deployUnverified) {
			r.State = deployNotIn // as far as the file goes, not deployed
		}
	}
	p.mu.Lock()
	if p.cache == nil || len(p.cache) > 500 {
		p.cache = map[string]deployCached{}
	}
	p.cache[key] = deployCached{r: r, at: time.Now(), hasList: hasList}
	p.mu.Unlock()
	return r
}

// githubPause is how long GitHub is not asked after it failed to answer.
const githubPause = 10 * time.Minute

// compareGitHub asks GitHub whether hub contains PR number's merge commit.
func (s *Service) compareGitHub(ctx context.Context, repo string, number int, hub string) deployResult {
	p := &s.deploy
	p.mu.Lock()
	down := time.Now().Before(p.githubDown)
	reason, resume := p.githubFailure, p.githubDown
	p.mu.Unlock()
	if down {
		return deployResult{State: deployUnknown, Hub: hub, Err: fmt.Errorf("GitHub verification paused until %s; original failure: %v", resume.UTC().Format(time.RFC3339), reason)}
	}
	r := s.compareGitHubNow(ctx, repo, number, hub)
	if r.State == deployUnknown && !errors.Is(r.Err, errForgeNotFound) && !errors.Is(r.Err, errForgeUnprocessable) {
		var response *githubReadError
		if errors.As(r.Err, &response) && response.kind == "permission" {
			// Authorization failures are not evidence that GitHub is down for other PRs.
			s.logf("deploy verification unavailable: %v; no global pause for permission errors", r.Err)
			return r
		}
		p.mu.Lock()
		p.githubDown = time.Now().Add(githubPause)
		p.githubFailure = r.Err
		resume := p.githubDown
		p.mu.Unlock()
		s.logf("deploy verification unavailable: %v; paused until %s", r.Err, resume.UTC().Format(time.RFC3339))
	}
	return r
}

func (s *Service) compareGitHubNow(ctx context.Context, repo string, number int, hub string) deployResult {
	f := s.forge()
	r := deployResult{Hub: hub}
	merged, sha, err := f.PRMerge(ctx, repo, number)
	switch {
	case err != nil:
		r.State, r.Err = deployUnknown, fmt.Errorf("the PR: %w", err)
		return r
	case !merged:
		r.State = deployUnmerged
		return r
	case sha == "":
		r.State, r.Err = deployUnknown, errors.New("the merged PR has no merge commit")
		return r
	}
	r.Merge = sha
	contains, found, err := f.Contains(ctx, repo, sha, hub)
	switch {
	case err != nil:
		r.State, r.Err = deployUnknown, fmt.Errorf("compare: %w", err)
	case !found:
		r.State = deployUnverified // the hub's commit is not in this repository: not the hub's work
	case contains:
		r.State = deployIn
	default:
		r.State = deployOut
	}
	return r
}

// deployGateText says why the check holds the work short of deployed (the delivery's gate).
func (s *Service) deployGateText(ctx context.Context, r deployResult) string {
	l := s.lang(ctx)
	switch r.State {
	case deployOut:
		return i18n.T(l, "assistant-reports.deploy_gate_out", i18n.Args{"hub": shortSHA(r.Hub), "merge": shortSHA(r.Merge)})
	case deployUnmerged:
		return i18n.T(l, "assistant-reports.deploy_gate_unmerged")
	case deployNotIn:
		return i18n.T(l, "assistant-reports.deploy_gate_not_in", i18n.Args{"hub": shortSHA(r.Hub)})
	}
	return i18n.T(l, "assistant-reports.deploy_gate_unknown")
}

func shortSHA(sha string) string {
	if len(sha) > 8 {
		return sha[:8]
	}
	return sha
}

// heldDelivery is the delivery of work the check holds short of deployed.
func (s *Service) heldDelivery(ctx context.Context, r deployResult, previouslyMerged bool) TodoDelivery {
	state := deliveryPR
	if r.mergeKnown(previouslyMerged) {
		state = deliveryMerged
	}
	return TodoDelivery{State: state, Gate: clip(s.deployGateText(ctx, r), 60), Unblock: unblockOther, At: time.Now()}
}

func (r deployResult) mergeKnown(previouslyMerged bool) bool {
	return r.Merge != "" || r.State == deployOut || previouslyMerged && r.State != deployUnmerged && !r.NotMerged
}

func (s *Service) knownDeployMerge(ctx context.Context, t Todo, pr string) bool {
	if facts := prFacts(t, pr); facts != nil && facts.State == "merged" {
		return true
	}
	b := &s.reports
	b.mu.Lock()
	defer b.mu.Unlock()
	if !s.loadStagesLocked(ctx) {
		return false
	}
	w := b.stages[t.ID].Deploy
	return w != nil && w.PR == pr && (w.Merged || w.State == deployOut)
}

// gateDeployStage is a task turn's stage once checked: a stage at deploy or past it, for a task whose PR (pr, else
// the one it had) the check holds, comes back to merge (or review: not merged) with the delivery that says so, and a
// watch that moves it on once the deploy lands. live: the stage stands (checked, or nothing to check it against).
func (s *Service) gateDeployStage(ctx context.Context, t Todo, stage, pr, machine, session, reply string) (string, bool) {
	if stageIndex(stage) < stageIndex("deploy") {
		return stage, false
	}
	if pr == "" {
		pr = t.PRURL
	}
	r := s.checkDeployed(ctx, pr)
	switch {
	case r.State == deployUnverified:
		if pr != "" {
			s.logf("task %s: said deployed; %s cannot be checked against the running hub: unverified", t.ID, pr)
		}
		return stage, true
	case !r.held():
		return stage, true
	}
	if r.Err != nil {
		s.logf("task %s: said deployed, not checked: %v (held)", t.ID, r.Err)
	} else {
		s.logf("task %s: said deployed, but the running hub (%s) does not contain %s (%s): held", t.ID, shortSHA(r.Hub), pr, r.State)
	}
	d := s.heldDelivery(ctx, r, s.knownDeployMerge(ctx, t, pr))
	s.watchDeploy(ctx, t.ID, deployWatch{PR: pr, MachineID: machine, SessionID: session, Reply: clipMiddle(reply, 4000), Since: time.Now(), Hub: r.Hub, State: r.State, Merged: d.State == deliveryMerged, CheckedAt: time.Now()})
	s.writeTaskDelivery(ctx, t.ID, d, true)
	return deliveryStage(d.State), false
}

// gateDelivery checks a judged delivery against the running hub using an owned or saved PR.
// A reply naming another PR needs independent confirmation before it can count as merged or deployed.
func (s *Service) gateDelivery(ctx context.Context, t Todo, d TodoDelivery, pr string, otherPR bool) TodoDelivery {
	if pr == "" {
		pr = t.PRURL
	}
	if otherPR && pr == "" && d.State != deliveryNone {
		return TodoDelivery{} // no delivery status to save and no progress to apply
	}
	if d.State != deliveryLive && !(otherPR && (d.State == deliveryMerged || d.State == deliveryOff)) {
		return d
	}
	r := s.checkDeployed(ctx, pr)
	if !r.held() && (!otherPR || r.State == deployIn && (d.State != deliveryMerged || r.Merge != "")) {
		return d
	}
	h := s.heldDelivery(ctx, r, s.knownDeployMerge(ctx, t, pr))
	h.At = d.At
	return h
}

// deployWatch is a task held short of deployed by the check, looked at again until the deploy lands (kept with the
// task's stages, reportStagesKey). The reply and the agent are what its "Please verify" will be made of.
type deployWatch struct {
	PR        string    `json:"pr"`
	MachineID string    `json:"machine_id,omitempty"`
	SessionID string    `json:"session_id,omitempty"`
	Reply     string    `json:"reply,omitempty"`
	Since     time.Time `json:"since"`
	Hub       string    `json:"hub,omitempty"`    // the hub commit last checked against
	State     string    `json:"state,omitempty"`  // what that check said
	Merged    bool      `json:"merged,omitempty"` // last known upstream merge for this PR
	CheckedAt time.Time `json:"checked_at"`
	Gate      string    `json:"gate,omitempty"` // objective deployment hold for a structured task
}

// deployWatchMost is how long a held task is watched for its deploy.
const deployWatchMost = 14 * 24 * time.Hour

// deployRecheck is how often a watch whose check could not tell (or saw the PR open) asks again with the same hub.
const deployRecheck = 10 * time.Minute

func (s *Service) watchDeploy(ctx context.Context, id string, w deployWatch) {
	b := &s.reports
	b.mu.Lock()
	defer b.mu.Unlock()
	if !s.loadStagesLocked(ctx) {
		return
	}
	cur := b.stages[id]
	if cur.hasTaskRuntime() {
		return // the objective sweep owns deployment checks for this task
	}
	cur.Deploy = &w
	b.stages[id] = cur
	s.saveStagesLocked(ctx, id)
}

func (s *Service) saveStagesLocked(ctx context.Context, id string) {
	if st := s.store(); st != nil {
		if err := st.PutSetting(ctx, reportStagesKey, s.reports.stages); err != nil {
			s.logf("task %s: stages: %v", id, err)
		}
	}
}

// prStatusPassMost bounds one pass of PR observations (sweepPRStatusesBounded), apart from the deploy checks' time.
var prStatusPassMost = time.Minute

// sweepPRStatusesBounded is sweepPRStatuses, then sweepParkIssues, with its own deadline (prStatusPassMost).
func (s *Service) sweepPRStatusesBounded(now time.Time) {
	ctx, cancel := context.WithTimeout(context.Background(), prStatusPassMost)
	defer cancel()
	s.sweepPRStatuses(ctx, now)
	s.sweepParkIssues(ctx, now) // parks waiting on a GitHub issue (#14050)
}

// sweepDeploys looks again at the held tasks: once the running hub contains the work, the task is deployed (its
// delivery live, its stage deploy) and asked to be verified; a task gone or watched too long is let go.
func (s *Service) sweepDeploys(now time.Time) {
	defer s.sweepTopicFollowupsBounded(now)
	defer s.sweepParkRepliesBounded(now)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	b := &s.reports
	b.mu.Lock()
	if !s.loadStagesLocked(ctx) {
		b.mu.Unlock()
		return
	}
	watches := map[string]deployWatch{}
	for id, st := range b.stages {
		if st.Deploy != nil {
			watches[id] = *st.Deploy
		}
	}
	b.mu.Unlock()
	// The PR facts come after the deploy checks, with a time of their own: slow or hanging forge reads for many PRs
	// never use up this pass's time before the hub's commit, sweepPRs and the watches have run. They run even when the
	// hub's health cannot be read (they do not need it).
	defer s.sweepPRStatusesBounded(now)
	// Post-closure subscriptions ride the same minute pass, so a closed to-do's open PR keeps being watched without a
	// second timer (#12371).
	defer s.sweepPRFollowupsBounded(now)
	hub, err := s.runningHubCommit(ctx)
	if err != nil {
		s.logf("deploys: the hub's health: %v", err)
		return
	}
	s.sweepPRs(ctx, now, hub, watches)
	if len(watches) == 0 {
		return
	}
	for id, w := range watches {
		t, ok := s.todoNow(id)
		if ok && t.DoneAt == nil && t.hasTaskRuntime() {
			continue // sweepPRs records observations for structured tasks
		}
		if !ok || now.Sub(w.Since) > deployWatchMost {
			s.unwatchDeploy(ctx, id, w.Since)
			continue
		}
		if hub == w.Hub && (w.State == deployOut || w.State == deployNotIn) || hub == w.Hub && now.Sub(w.CheckedAt) < deployRecheck {
			continue // the same hub said no: only a deploy changes that
		}
		r := s.checkDeployed(ctx, w.PR)
		switch {
		case r.State == deployIn:
			s.unwatchDeploy(ctx, id, w.Since)
			s.logf("task %s: the running hub (%s) now contains %s: deployed", id, shortSHA(r.Hub), w.PR)
			kept := s.writeTaskDelivery(ctx, id, TodoDelivery{State: deliveryLive, At: now}, true)
			if kept && w.Reply != "" { // held from a turn that said deployed (sweepPRs' finds have no reply: nothing to verify by)
				s.askAcceptance(t, w.MachineID, w.SessionID, w.Reply)
			}
		case r.State == deployUnverified:
			s.unwatchDeploy(ctx, id, w.Since) // nothing to check it against any more: left as it is
		default:
			facts := prFacts(t, w.PR)
			w.Merged = r.mergeKnown(w.Merged || w.State == deployOut || facts != nil && facts.State == "merged")
			w.Hub, w.State, w.CheckedAt = cmp.Or(r.Hub, hub), r.State, now
			b.mu.Lock()
			if cur, ok := b.stages[id]; ok && cur.Deploy != nil && cur.Deploy.Since.Equal(w.Since) {
				cur.Deploy = &w
				b.stages[id] = cur
				s.saveStagesLocked(ctx, id)
			}
			b.mu.Unlock()
			if r.State != deployUnknown {
				if d := s.heldDelivery(ctx, r, w.Merged); t.Delivery == nil || t.Delivery.State != d.State || t.Delivery.Gate != d.Gate {
					s.writeTaskDelivery(ctx, id, d, true)
				}
			}
		}
	}
}

// sweepPRs looks at the open to-dos that name a PR and say nothing past it (no delivery, "none" or "pr"; not watched):
// a PR that is merged is not one to merge. Merged and in the running hub (hub) it is deployed (delivery live, stage
// deploy: Just finished); merged and not in it, "merged, not deployed" with a watch for the deploy. An open PR, or one
// the check cannot speak for, is left as it is. Each to-do is asked again only when the hub's commit changed, or every
// deployRecheck while its PR is open or the check could not tell.
func (s *Service) sweepPRs(ctx context.Context, now time.Time, hub string, watched map[string]deployWatch) {
	if hub == "" {
		return
	}
	p := &s.deploy
	for _, t := range s.todosOf(ctx, "*") {
		if t.DoneAt != nil || t.Cancelled() {
			continue
		}
		pr, structured := t.PRURL, t.hasTaskRuntime()
		if structured {
			if pr == "" && len(t.PRs) == 1 {
				pr = t.PRs[0]
			}
			w := watched[t.ID]
			if w.PR == pr && w.Hub == hub && (w.State == deployIn || w.State == deployOut || w.State == deployNotIn) {
				continue // a different running build changes this observation
			}
		} else {
			if stageIndex(t.Stage) >= stageIndex("deploy") {
				continue
			}
			if d := t.Delivery; d != nil && d.State != deliveryNone && d.State != deliveryPR {
				continue
			}
			if _, ok := watched[t.ID]; ok {
				continue
			}
		}
		if pr == "" {
			continue
		}
		p.mu.Lock()
		last, ok := p.seen[t.ID]
		p.mu.Unlock()
		if ok && last.deployPR == pr && last.hub == hub && (last.state == deployOut || last.state == deployUnverified || now.Sub(last.at) < deployRecheck) {
			continue
		}
		r := s.checkDeployed(ctx, pr)
		if structured {
			if err := s.applyRuntimeDeploy(ctx, t, pr, r, now); err != nil {
				s.logf("task %s: keep deployment observation: %v", t.ID, err)
				continue
			}
		}
		p.mu.Lock()
		if p.seen == nil || len(p.seen) > 2000 {
			p.seen = map[string]prSeen{}
		}
		seen := p.seen[t.ID]
		seen.hub, seen.state, seen.at, seen.deployPR = hub, r.State, now, pr
		p.seen[t.ID] = seen
		p.mu.Unlock()
		if structured {
			continue
		}
		switch r.State {
		case deployIn:
			s.logf("task %s: %s is in the running hub (%s): deployed", t.ID, t.PRURL, shortSHA(hub))
			s.writeTaskDelivery(ctx, t.ID, TodoDelivery{State: deliveryLive, At: now}, true)
		case deployOut:
			s.logf("task %s: %s is merged, not in the running hub (%s): merged, not deployed", t.ID, t.PRURL, shortSHA(hub))
			s.watchDeploy(ctx, t.ID, deployWatch{PR: t.PRURL, Since: now, Hub: hub, State: r.State, CheckedAt: now})
			s.writeTaskDelivery(ctx, t.ID, s.heldDelivery(ctx, r, false), true)
		}
	}
}

// applyRuntimeDeploy keeps a checked build and its projected task state in one
// write. A result for a replaced reference or from an older pass cannot win.
func (s *Service) applyRuntimeDeploy(ctx context.Context, t Todo, pr string, r deployResult, at time.Time) error {
	switch r.State {
	case deployIn, deployOut, deployNotIn, deployUnmerged:
	default:
		return nil
	}
	reason := ""
	if r.State == deployOut || r.State == deployNotIn {
		reason = s.deployGateText(ctx, r)
	}
	checksReason := i18n.T(s.lang(ctx), "assistant-reports.pr_checks_pending")
	brief, _ := s.committedBriefOfTodo(t.ID)
	_, err := s.updateTaskRuntime(ctx, t.ID, func(_ Todo, st *todoStage) (bool, error) {
		if !st.hasTaskRuntime() || st.PR != t.PRURL ||
			st.PR == "" && (len(st.PRs) != 1 || st.PRs[0] != pr) {
			return false, nil
		}
		old := st.Deploy
		if old != nil && old.PR == pr && !at.After(old.CheckedAt) {
			return false, nil
		}
		merged := r.mergeKnown(old != nil && old.PR == pr && old.Merged)
		if p := st.runtimePR(); p != nil && p.URL == pr && p.State == "merged" {
			merged = true
		}
		if r.State == deployNotIn && !merged || r.State == deployUnmerged && (old == nil || old.PR != pr || old.State != deployIn) {
			return false, nil // an unknown deployment or an ordinary open PR adds no deployment evidence
		}
		st.Deploy = &deployWatch{PR: pr, Since: at.UTC(), Hub: r.Hub, State: r.State, Merged: merged, CheckedAt: at.UTC(), Gate: reason}
		st.refreshRuntimeFromProviders(at, checksReason, brief.CompletionModeNow)
		return true, nil
	})
	return err
}

// unwatchDeploy drops task id's watch made at since (a newer one stays).
func (s *Service) unwatchDeploy(ctx context.Context, id string, since time.Time) {
	b := &s.reports
	b.mu.Lock()
	defer b.mu.Unlock()
	if cur, ok := b.stages[id]; ok && cur.Deploy != nil && cur.Deploy.Since.Equal(since) {
		cur.Deploy = nil
		b.stages[id] = cur
		s.saveStagesLocked(ctx, id)
	}
}

// githubForge uses the signed-in connector, else the hub user's authenticated GitHub CLI.
// Production never falls back to anonymous HTTP. Custom API bases are used by transport tests.
type githubForge struct {
	base   string
	client *http.Client
	via    func(ctx context.Context, path string, out any) (bool, error)
}

var githubHTTP = &http.Client{Timeout: 15 * time.Second}

// githubGetter is the extensions' GitHub connector (extmgr.Manager.GitHubGet).
type githubGetter interface {
	GitHubGet(ctx context.Context, path string, out any) (bool, error)
}

// githubConnectorStatus is the local connectivity check on extmgr.Manager (zero remote calls).
type githubConnectorStatus interface {
	GitHubConnected(ctx context.Context) bool
}

// forge is where deploy checks read PRs: the test's, else GitHub (through the connector when there is one).
func (s *Service) forge() prForge {
	if s.deploy.forge != nil {
		return s.deploy.forge
	}
	g := githubForge{base: "https://api.github.com", client: githubHTTP}
	if c, ok := s.ext.(githubGetter); ok {
		g.via = c.GitHubGet
	}
	return g
}

var (
	errForgeNotFound      = errors.New("not found")
	errForgeUnprocessable = errors.New("unprocessable entity")
	errGitHubUnconfigured = errors.New("GitHub authentication unavailable")
)

// connectorNotFound: the connector's error for a 404 or 422 ("GitHub GET <path>: 404 …").
var connectorNotFound = lazyre.New(`: (404|422)\b`)

func (g githubForge) get(ctx context.Context, path string, out any) error {
	if g.via != nil {
		if ok, err := g.via(ctx, path, out); ok {
			if err != nil {
				if m := connectorNotFound.FindStringSubmatch(err.Error()); m != nil {
					if m[1] == "422" {
						return errForgeUnprocessable
					}
					return errForgeNotFound
				}
			}
			return err
		}
	}
	token := ""
	if g.base == "https://api.github.com" {
		cli, err := hasGitHubCLI(ctx)
		if err != nil {
			return fmt.Errorf("GitHub CLI identification interrupted: %w", err)
		}
		if cli {
			return githubCLIGet(ctx, path, out)
		}
		token = strings.TrimSpace(os.Getenv("GH_TOKEN"))
		if token == "" {
			token = strings.TrimSpace(os.Getenv("GITHUB_TOKEN"))
		}
		if token == "" {
			return fmt.Errorf("%w: authenticated GitHub CLI unavailable and no environment authorization (no anonymous fallback)", errGitHubUnconfigured)
		}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, g.base+path, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "persona-hub")
	client := g.client
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
		// Environment authorization is only for this host, never a redirected request.
		authenticated := *client
		authenticated.CheckRedirect = func(*http.Request, []*http.Request) error { return http.ErrUseLastResponse }
		client = &authenticated
	}
	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("GitHub network error: %s", githubDiagnostic(err.Error()))
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusNotFound:
		// Preserve diagnostics while keeping the compare endpoint's missing-commit semantics.
		return fmt.Errorf("%w: %w", errForgeNotFound, githubResponseError(resp))
	case resp.StatusCode == http.StatusUnprocessableEntity:
		return fmt.Errorf("%w: %w", errForgeUnprocessable, githubResponseError(resp))
	case resp.StatusCode != http.StatusOK:
		return githubResponseError(resp)
	}
	return json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(out)
}

func (g githubForge) IsPR(ctx context.Context, repo string, number int) (bool, error) {
	var issue struct {
		PR *struct{} `json:"pull_request"`
	}
	err := g.get(ctx, fmt.Sprintf("/repos/%s/issues/%d", repo, number), &issue)
	if errors.Is(err, errForgeNotFound) {
		// Issue access and PR access can differ. A readable repository alone does not establish PR permissions.
		var pr struct {
			Number int `json:"number"`
		}
		err := g.get(ctx, fmt.Sprintf("/repos/%s/pulls/%d", repo, number), &pr)
		if err == nil {
			if pr.Number != number {
				return false, fmt.Errorf("PR identity could not be confirmed for %s#%d", repo, number)
			}
			return true, nil
		}
		if !errors.Is(err, errForgeNotFound) {
			return false, err
		}
		var prs []struct{}
		if err := g.get(ctx, "/repos/"+repo+"/pulls?state=all&per_page=1", &prs); err != nil {
			return false, err
		}
		if prs == nil {
			return false, fmt.Errorf("PR access could not be confirmed for %s", repo)
		}
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return issue.PR != nil, nil
}

func (g githubForge) PRMerge(ctx context.Context, repo string, number int) (bool, string, error) {
	var pr struct {
		Merged bool   `json:"merged"`
		SHA    string `json:"merge_commit_sha"`
	}
	if err := g.get(ctx, fmt.Sprintf("/repos/%s/pulls/%d", repo, number), &pr); err != nil {
		return false, "", err
	}
	return pr.Merged, pr.SHA, nil
}

func (g githubForge) IssueState(ctx context.Context, repo string, number int) (string, error) {
	var issue struct {
		State string `json:"state"`
	}
	if err := g.get(ctx, fmt.Sprintf("/repos/%s/issues/%d", repo, number), &issue); err != nil {
		return "", err
	}
	return strings.ToLower(strings.TrimSpace(issue.State)), nil
}

// compareEmptyPage is a page number well past page 1; GitHub caps a compare at 10,000 commits.
const compareEmptyPage = 1000

func (g githubForge) Contains(ctx context.Context, repo, base, head string) (bool, bool, error) {
	var c struct {
		Status string `json:"status"`
	}
	// Only the status is read. per_page=1 alone still returns the first commit and up to 300 files with their patches
	// (1.3 MB on a busy main). Files come only on page 1, and status, ahead_by and behind_by repeat on every page, so a
	// high page number carries the status with no files and at most one commit.
	err := g.get(ctx, fmt.Sprintf("/repos/%s/compare/%s...%s?per_page=1&page=%d", repo, base, head, compareEmptyPage), &c)
	if errors.Is(err, errForgeNotFound) || errors.Is(err, errForgeUnprocessable) {
		return false, false, nil
	}
	if err != nil {
		return false, false, err
	}
	switch c.Status {
	case "ahead", "identical":
		return true, true, nil
	case "behind", "diverged":
		return false, true, nil
	}
	return false, false, fmt.Errorf("compare status %q", c.Status)
}
