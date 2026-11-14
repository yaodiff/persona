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
	deployIn         = "in"         // the running hub contains the merge commit
	deployOut        = "out"        // merged; the running hub does not contain it yet
	deployUnmerged   = "unmerged"   // the PR is not merged
	deployUnknown    = "unknown"    // could not tell (network, rate limit): not deployed
	deployUnverified = "unverified" // nothing to check it against: the agent's word stands
	// deployNotIn: the deployed-commits file of the running hub does not name the PR and GitHub cannot say more: not
	// in the running hub as far as the hub knows (merged and not deployed, or not merged).
	deployNotIn = "not_in"
)

type deployResult struct {
	State string
	Hub   string // the running hub's commit
	Merge string // the PR's merge commit
	Err   error
}

// held: the result keeps the work short of deployed.
func (r deployResult) held() bool {
	return r.State == deployOut || r.State == deployUnmerged || r.State == deployUnknown || r.State == deployNotIn
}

// prForge answers what a deploy check asks of where the PR lives.
type prForge interface {
	// PRMerge: whether PR number of repo (owner/name) is merged, and its merge commit.
	PRMerge(ctx context.Context, repo string, number int) (merged bool, sha string, err error)
	// Contains: whether head contains base in repo; found false when either is not a commit of repo.
	Contains(ctx context.Context, repo, base, head string) (contains, found bool, err error)
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
	githubDown time.Time

	// listPaths are where the deployed-commits file is looked for (nil: deployedFiles; tests set their own), files
	// the ones read (deployed_commits.go).
	listPaths []string
	files     deployedFileCache
}

// prSeen is sweepPRs' last look at a to-do's PR: against which hub commit, what it said, when.
type prSeen struct {
	hub, state string
	at         time.Time
}

type deployCached struct {
	r  deployResult
	at time.Time
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

// checkDeployed says whether the running hub contains PR pr's merge commit.
func (s *Service) checkDeployed(ctx context.Context, pr string) deployResult {
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
	key := pr + "\x00" + hub
	p := &s.deploy
	p.mu.Lock()
	if c, ok := p.cache[key]; ok && (c.r.State == deployIn || time.Since(c.at) < deployCacheFor) {
		p.mu.Unlock()
		return c.r
	}
	p.mu.Unlock()
	r := s.compareDeploy(ctx, repo, number, hub)
	p.mu.Lock()
	if p.cache == nil || len(p.cache) > 500 {
		p.cache = map[string]deployCached{}
	}
	p.cache[key] = deployCached{r: r, at: time.Now()}
	p.mu.Unlock()
	return r
}

// githubPause is how long GitHub is not asked after it failed to answer.
const githubPause = 10 * time.Minute

// compareDeploy answers with the running hub's deployed-commits file first (no network: deployed_commits.go), and
// GitHub when the file cannot say (none, another repository, or the PR not named in it).
func (s *Service) compareDeploy(ctx context.Context, repo string, number int, hub string) deployResult {
	l := s.deployedListFor(hub)
	if l == nil || !strings.EqualFold(l.repo, repo) {
		return s.compareGitHub(ctx, repo, number, hub)
	}
	if sha, ok := l.prs[number]; ok {
		return deployResult{State: deployIn, Hub: hub, Merge: sha}
	}
	r := s.compareGitHub(ctx, repo, number, hub)
	if r.State == deployUnknown || r.State == deployUnverified {
		return deployResult{State: deployNotIn, Hub: hub, Err: r.Err} // as far as the file goes, not deployed
	}
	return r
}

// compareGitHub asks GitHub whether hub contains PR number's merge commit.
func (s *Service) compareGitHub(ctx context.Context, repo string, number int, hub string) deployResult {
	p := &s.deploy
	p.mu.Lock()
	down := time.Now().Before(p.githubDown)
	p.mu.Unlock()
	if down {
		return deployResult{State: deployUnknown, Hub: hub, Err: errors.New("GitHub did not answer a moment ago")}
	}
	r := s.compareGitHubNow(ctx, repo, number, hub)
	if r.State == deployUnknown {
		p.mu.Lock()
		p.githubDown = time.Now().Add(githubPause)
		p.mu.Unlock()
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
	case !merged || sha == "":
		r.State = deployUnmerged
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
func (s *Service) heldDelivery(ctx context.Context, r deployResult) TodoDelivery {
	state := deliveryMerged
	if r.State == deployUnmerged {
		state = deliveryPR
	}
	return TodoDelivery{State: state, Gate: clip(s.deployGateText(ctx, r), 60), Unblock: unblockOther, At: time.Now()}
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
	d := s.heldDelivery(ctx, r)
	s.watchDeploy(ctx, t.ID, deployWatch{PR: pr, MachineID: machine, SessionID: session, Reply: clipMiddle(reply, 4000), Since: time.Now(), Hub: r.Hub, State: r.State, CheckedAt: time.Now()})
	s.setTaskDelivery(ctx, t.ID, d)
	return deliveryStage(d.State), false
}

// gateDelivery checks a judged delivery that says live (deliveryLive) against the running hub: held, it is merged
// (or pr) with why. replies name the PR (else the todo's).
func (s *Service) gateDelivery(ctx context.Context, t Todo, d TodoDelivery, pr string) TodoDelivery {
	if d.State != deliveryLive {
		return d
	}
	if pr == "" {
		pr = t.PRURL
	}
	r := s.checkDeployed(ctx, pr)
	if !r.held() {
		return d
	}
	h := s.heldDelivery(ctx, r)
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
	Hub       string    `json:"hub,omitempty"`   // the hub commit last checked against
	State     string    `json:"state,omitempty"` // what that check said
	CheckedAt time.Time `json:"checked_at"`
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

// sweepDeploys looks again at the held tasks: once the running hub contains the work, the task is deployed (its
// delivery live, its stage deploy) and asked to be verified; a task gone or watched too long is let go.
func (s *Service) sweepDeploys(now time.Time) {
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
			s.setTaskDelivery(ctx, id, TodoDelivery{State: deliveryLive, At: now})
			if w.Reply != "" { // held from a turn that said deployed (sweepPRs' finds have no reply: nothing to verify by)
				s.askAcceptance(t, w.MachineID, w.SessionID, w.Reply)
			}
		case r.State == deployUnverified:
			s.unwatchDeploy(ctx, id, w.Since) // nothing to check it against any more: left as it is
		default:
			w.Hub, w.State, w.CheckedAt = cmp.Or(r.Hub, hub), r.State, now
			b.mu.Lock()
			if cur, ok := b.stages[id]; ok && cur.Deploy != nil && cur.Deploy.Since.Equal(w.Since) {
				cur.Deploy = &w
				b.stages[id] = cur
				s.saveStagesLocked(ctx, id)
			}
			b.mu.Unlock()
			if r.State != deployUnknown {
				if d := s.heldDelivery(ctx, r); t.Delivery == nil || t.Delivery.Gate != d.Gate {
					s.setTaskDelivery(ctx, id, d)
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
		if t.PRURL == "" || t.DoneAt != nil || t.Cancelled() || stageIndex(t.Stage) >= stageIndex("deploy") {
			continue
		}
		if d := t.Delivery; d != nil && d.State != deliveryNone && d.State != deliveryPR {
			continue
		}
		if _, ok := watched[t.ID]; ok {
			continue
		}
		p.mu.Lock()
		last, ok := p.seen[t.ID]
		p.mu.Unlock()
		if ok && last.hub == hub && (last.state == deployOut || last.state == deployUnverified || now.Sub(last.at) < deployRecheck) {
			continue
		}
		r := s.checkDeployed(ctx, t.PRURL)
		p.mu.Lock()
		if p.seen == nil || len(p.seen) > 2000 {
			p.seen = map[string]prSeen{}
		}
		p.seen[t.ID] = prSeen{hub: hub, state: r.State, at: now}
		p.mu.Unlock()
		switch r.State {
		case deployIn:
			s.logf("task %s: %s is merged and in the running hub (%s): deployed", t.ID, t.PRURL, shortSHA(hub))
			s.setTaskDelivery(ctx, t.ID, TodoDelivery{State: deliveryLive, At: now})
		case deployOut:
			s.logf("task %s: %s is merged, not in the running hub (%s): merged, not deployed", t.ID, t.PRURL, shortSHA(hub))
			s.watchDeploy(ctx, t.ID, deployWatch{PR: t.PRURL, Since: now, Hub: hub, State: r.State, CheckedAt: now})
			s.setTaskDelivery(ctx, t.ID, s.heldDelivery(ctx, r))
		}
	}
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

// githubForge is GitHub's REST API: through the hub's GitHub connector when it is signed in (via), else directly, with
// GH_TOKEN or GITHUB_TOKEN from the environment when set (the repository may be public: without a token it is read as
// anyone may).
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

var errForgeNotFound = errors.New("not found")

// connectorNotFound: the connector's error for a 404 or 422 ("GitHub GET <path>: 404 …").
var connectorNotFound = lazyre.New(`: (404|422)\b`)

func (g githubForge) get(ctx context.Context, path string, out any) error {
	if g.via != nil {
		if ok, err := g.via(ctx, path, out); ok {
			if err != nil && connectorNotFound.MatchString(err.Error()) {
				return errForgeNotFound
			}
			return err
		}
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, g.base+path, nil)
	if err != nil {
		return err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("User-Agent", "persona-hub")
	for _, k := range []string{"GH_TOKEN", "GITHUB_TOKEN"} {
		if tok := strings.TrimSpace(os.Getenv(k)); tok != "" {
			req.Header.Set("Authorization", "Bearer "+tok)
			break
		}
	}
	resp, err := g.client.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	switch {
	case resp.StatusCode == http.StatusNotFound || resp.StatusCode == http.StatusUnprocessableEntity:
		return errForgeNotFound // compare answers 404 (or 422) for a commit the repository does not have
	case resp.StatusCode != http.StatusOK:
		return fmt.Errorf("GitHub answered %d", resp.StatusCode)
	}
	return json.NewDecoder(io.LimitReader(resp.Body, 4<<20)).Decode(out)
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

func (g githubForge) Contains(ctx context.Context, repo, base, head string) (bool, bool, error) {
	var c struct {
		Status string `json:"status"`
	}
	// per_page=1: only the status is read, not the commits and files between them
	err := g.get(ctx, fmt.Sprintf("/repos/%s/compare/%s...%s?per_page=1", repo, base, head), &c)
	if errors.Is(err, errForgeNotFound) {
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
