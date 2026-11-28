package assistant

import (
	"cmp"
	"context"
	"net/url"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/assistant/repository"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/lazyre"
)

var (
	inboxTaskURL = lazyre.New(`https?://[^\s<>"\x60]+`)
	// inboxRepoRef: owner/repo#N written out, as GitHub shows a cross-repository reference.
	inboxRepoRef = lazyre.New(`(?:^|[^\w./-])([A-Za-z0-9_.-]+/[A-Za-z0-9_.-]+)#([1-9][0-9]*)\b`)
	// inboxBareRef: a bare #N (not an anchor in a URL, not part of a word or of owner/repo#N).
	inboxBareRef = lazyre.New(`(?:^|[^\w./#&-])#([1-9][0-9]*)\b`)
	// inboxCloses: a pull request's own words that it closes an issue of its repository (GitHub's closing keywords).
	inboxCloses = lazyre.New(`(?i)\b(?:close[sd]?|fix(?:e[sd])?|resolve[sd]?)\s*:?\s+(?:https://github\.com/([\w.-]+/[\w.-]+)/issues/|([\w.-]+/[\w.-]+)?#)([1-9][0-9]*)\b`)
)

// githubRef is the ref of number n in GitHub repository repo (owner/repo): GitHub numbers issues and pull requests in
// one sequence, so the kind is left out and an issue's link and a bare #N meet a PR's.
func githubRef(repo string, n uint64) string {
	return "github.com/" + strings.ToLower(repo) + "#" + strconv.FormatUint(n, 10)
}

// inboxTaskRefs reads the forge links and qualified GitHub numbers that legacy task text can name.
func inboxTaskRefs(text string) []string {
	return inboxRefs(text, false)
}

// inboxLinkRef normalizes a URL for matching. The second result says the URL has a known issue or PR shape.
// Provider-specific URLs can still match a saved canonical binding. Queries and comment fragments are ignored.
func inboxLinkRef(raw string) (string, bool) {
	u, err := url.Parse(strings.TrimRight(raw, ".,;:!?)]}，。；：！）》」』")) // i18n-ignore: URL punctuation, not displayed text
	if err != nil || u.Host == "" || u.User != nil || u.Scheme != "http" && u.Scheme != "https" {
		return "", false
	}
	path := strings.Trim(u.EscapedPath(), "/")
	parts := strings.Split(path, "/")
	if len(parts) == 4 && parts[0] != "" && parts[1] != "" && (parts[2] == "pull" || parts[2] == "issues") {
		if n, err := strconv.ParseUint(parts[3], 10, 64); err == nil && n != 0 {
			if strings.EqualFold(u.Hostname(), "github.com") {
				return githubRef(parts[0]+"/"+parts[1], n), true
			}
			return strings.ToLower(u.Host) + "/" + strings.Join(parts[:3], "/") + "/" + strconv.FormatUint(n, 10), true
		}
	}
	key := u.Scheme + "://" + strings.ToLower(u.Host) + "/" + path
	if strings.Contains(path, "/merge_requests/") || strings.Contains(path, "/-/issues/") {
		if _, link, err := repository.ParseTaskLink(key); err == nil {
			return link.CanonicalURL, true
		}
	}
	return key, false
}

func inboxRefs(text string, includeProviderURLs bool) []string {
	var refs []string
	for _, raw := range inboxTaskURL.FindAllString(text, -1) {
		if ref, known := inboxLinkRef(raw); ref != "" && (known || includeProviderURLs) {
			refs = append(refs, ref)
		}
	}
	for _, m := range inboxRepoRef.FindAllStringSubmatch(text, -1) {
		if n, err := strconv.ParseUint(m[2], 10, 64); err == nil {
			refs = append(refs, githubRef(m[1], n))
		}
	}
	return refs
}

// inboxBareRefs are the bare #N of text.
func inboxBareRefs(text string) []uint64 {
	var out []uint64
	for _, m := range inboxBareRef.FindAllStringSubmatch(text, -1) {
		if n, err := strconv.ParseUint(m[1], 10, 64); err == nil && !slices.Contains(out, n) {
			out = append(out, n)
		}
	}
	return out
}

// inboxItemNumber is the number of a GitHub item's own issue or pull request (its thread owner/repo#N); 0 for none.
func inboxItemNumber(it *InboxItem) uint64 {
	if it.Source != "github" || it.Connector == nil {
		return 0
	}
	m := inboxRepoThread.FindStringSubmatch(strings.TrimSpace(it.Connector.Thread))
	if m == nil {
		return 0
	}
	_, num, _ := strings.Cut(strings.TrimSpace(it.Connector.Thread), "#")
	n, _ := strconv.ParseUint(num, 10, 64)
	return n
}

// taskText is what a task's refs are read from: its PR links (the current one and the earlier ones its Agents named),
// title and notes.
func taskText(t Todo) string {
	return t.PRURL + "\n" + strings.Join(t.PRs, "\n") + "\n" + t.Title + "\n" + t.Notes
}

// inboxTaskIndex finds the open tasks an item's refs name (#5578, #6507).
type inboxTaskIndex struct {
	bound      map[string][]string        // canonical ref → open tasks with an authoritative link
	byRef      map[string][]string        // ref → open tasks naming it in legacy fields
	bare       map[uint64][]Todo          // #N → open tasks naming it bare
	references map[string]map[string]bool // task ID → refs excluded from routing to it
	open       map[string]Todo
}

func (x *inboxTaskIndex) addReferences(id string, rows []repository.TaskRepository) {
	for _, row := range rows {
		for _, link := range row.Links {
			if link.Role == repository.LinkRoleReference {
				if ref, _ := inboxLinkRef(link.CanonicalURL); ref != "" {
					if x.references[id] == nil {
						x.references[id] = map[string]bool{}
					}
					x.references[id][ref] = true
				}
			}
		}
	}
}

func (x *inboxTaskIndex) add(t Todo, rows []repository.TaskRepository) {
	x.open[t.ID] = t
	x.addReferences(t.ID, rows)
	hasBinding := false
	for _, row := range rows {
		for _, link := range row.Links {
			if link.Role == repository.LinkRoleReference || link.Kind != "pull_request" && link.Kind != "issue" {
				continue
			}
			if ref, _ := inboxLinkRef(link.CanonicalURL); ref != "" {
				hasBinding = true
				if !x.references[t.ID][ref] && !slices.Contains(x.bound[ref], t.ID) {
					x.bound[ref] = append(x.bound[ref], t.ID)
				}
			}
		}
	}
	if hasBinding {
		return
	}
	for _, ref := range inboxTaskRefs(taskText(t)) {
		if !x.references[t.ID][ref] && !slices.Contains(x.byRef[ref], t.ID) {
			x.byRef[ref] = append(x.byRef[ref], t.ID)
		}
	}
	for _, n := range inboxBareRefs(t.Title + "\n" + t.Notes) {
		x.bare[n] = append(x.bare[n], t)
	}
}

// legacyTasksOf includes a bare #N only within its task's repository and only if the ref is eligible for routing.
func (x *inboxTaskIndex) legacyTasksOf(ref, repo string, n uint64) []string {
	ids := slices.Clone(x.byRef[ref])
	if repo != "" && n != 0 {
		for _, t := range x.bare[n] {
			if !x.references[t.ID][ref] && !slices.Contains(ids, t.ID) && taskOfRepo(t, repo) {
				ids = append(ids, t.ID)
			}
		}
	}
	return ids
}

func inboxItemRefs(it *InboxItem, repo string, n uint64) (link, thread string) {
	if it.Connector != nil {
		link, _ = inboxLinkRef(it.Connector.URL)
	}
	if repo != "" && n != 0 {
		thread = githubRef(repo, n)
	}
	return link, thread
}

func (x *inboxTaskIndex) itemIsReference(id, link, thread string) bool {
	refs := x.references[id]
	return refs[link] || refs[thread]
}

func inboxBoundToRepo(rows []repository.TaskRepository, repo string) bool {
	return slices.ContainsFunc(rows, func(row repository.TaskRepository) bool {
		return slices.ContainsFunc(row.Links, func(link repository.TaskLink) bool {
			ref, _ := inboxLinkRef(link.CanonicalURL)
			return link.Role != repository.LinkRoleReference && strings.HasPrefix(ref, "github.com/"+repo+"#")
		})
	})
}

// match finds the one open task the item names. Saved links take precedence over legacy fields. When an issue
// has no direct match, its repository's closing PRs can link it too.
func (x *inboxTaskIndex) match(it *InboxItem, closes map[string][]uint64) string {
	text := it.Title + " " + it.Preview + " " + it.Context
	if it.Connector != nil {
		text += " " + it.Connector.URL
	}
	repo, own := "", inboxItemNumber(it)
	if own != 0 {
		repo = inboxScopeOf(it).repo
	}
	link, thread := inboxItemRefs(it, repo, own)
	bound, legacy := map[string]bool{}, map[string]bool{}
	note := func(ref, repo string, n uint64) {
		for _, id := range x.bound[ref] {
			if !x.itemIsReference(id, link, thread) {
				bound[id] = true
			}
		}
		for _, id := range x.legacyTasksOf(ref, repo, n) {
			if !x.itemIsReference(id, link, thread) {
				legacy[id] = true
			}
		}
	}
	for _, ref := range inboxRefs(text, true) {
		note(ref, "", 0)
	}
	if thread != "" {
		note(thread, repo, own)
		if len(bound) == 0 && len(legacy) == 0 {
			for _, pr := range closes[thread] {
				note(githubRef(repo, pr), repo, pr)
			}
		}
	}
	found := bound
	if len(found) == 0 {
		found = legacy
	}
	if len(found) != 1 {
		return ""
	}
	for id := range found {
		return id
	}
	return ""
}

// inboxClosesIndex reads, off the GitHub pull request items the Inbox holds (open or done), which issues of their
// repository each says it closes: issue ref → pull request numbers. Under b.mu.
func inboxClosesIndex(items map[string]*InboxItem, excluded func(*InboxItem) bool) map[string][]uint64 {
	out := map[string][]uint64{}
	for _, it := range items {
		if excluded != nil && excluded(it) {
			continue
		}
		pr := inboxItemNumber(it)
		if pr == 0 || it.Connector == nil || !strings.Contains(it.Connector.URL, "/pull/") {
			continue
		}
		repo := inboxScopeOf(it).repo
		for _, m := range inboxCloses.FindAllStringSubmatch(it.Title+"\n"+it.Preview+"\n"+it.Context, -1) {
			if r := cmp.Or(m[1], m[2], repo); !strings.EqualFold(r, repo) {
				continue // another repository's issue
			}
			n, err := strconv.ParseUint(m[3], 10, 64)
			if err != nil || n == pr {
				continue
			}
			if ref := githubRef(repo, n); !slices.Contains(out[ref], pr) {
				out[ref] = append(out[ref], pr)
			}
		}
	}
	return out
}

// linkInboxTaskURLs runs before the AI twin's switch: exact task links need neither triage nor a model. Keep
// existing links, leave ambiguous matches to the judge, and persist new links before notifying their supervisors. An
// existing automatic link to an open task is cleared when the item is a reference or no longer fits the task's
// repository scope (inbox_task_scope.go, #6424).
func (s *Service) linkInboxTaskURLs(ctx context.Context) {
	fullCfg, err := s.inboxConfigFresh(ctx)
	if err != nil {
		return
	}
	if s.ensureTodosLoaded(ctx) != nil {
		return
	}
	visCfg := fullCfg.Visibility
	var providerAllowed map[string]bool
	if visCfg != nil && (visCfg.DirectChats || len(visCfg.Chats) > 0) {
		b := &s.inbox
		b.mu.Lock()
		sources := map[string]struct{}{}
		for _, it := range b.items {
			sources[it.Source] = struct{}{}
		}
		b.mu.Unlock()
		providerAllowed = make(map[string]bool, len(sources))
		for src := range sources {
			providerAllowed[src] = s.inboxVisibilityProviderAllowed(visCfg, src)
		}
	}
	idx := &inboxTaskIndex{
		bound: map[string][]string{}, byRef: map[string][]string{}, bare: map[uint64][]Todo{},
		references: map[string]map[string]bool{}, open: map[string]Todo{},
	}
	acceptedPRs := map[string]Todo{}
	followAcceptedPRs := s.inboxSignalsOn(ctx)
	var tasks []Todo
	var ids []string
	s.todos.mu.RLock()
	for _, t := range s.todos.todos {
		if t.Cancelled() || isAskTodo(t.ID) || t.DoneAt != nil && (!followAcceptedPRs || !taskAcceptedForFeedback(*t)) {
			continue
		}
		tasks = append(tasks, copyTodoLocked(t))
		ids = append(ids, t.ID)
	}
	s.todos.mu.RUnlock()
	if _, ok := s.store().(todoRepositoryStore); ok {
		for _, todo := range tasks {
			if todo.DoneAt != nil {
				continue
			}
			for _, agent := range todo.Agents {
				if agent.ReleasedAt != nil || agent.DismissedAt != nil {
					continue
				}
				if e, found := s.workEntry([2]string{agent.MachineID, agent.SessionID}); found {
					// A failed report import remains retryable on the next Inbox pass.
					if err := s.importTaskRepositoryArtifacts(ctx, e); err != nil {
						s.logf("task %s: artifact repository links not saved: %v", todo.ID, err)
					}
				}
			}
		}
	}
	var repositories map[string][]repository.TaskRepository
	if st, ok := s.store().(todoRepositoryBatchStore); ok {
		repositories, err = st.ListTodoRepositories(ctx, ids)
		if err != nil {
			s.logf("inbox: task repository bindings could not be read: %v", err)
			return
		}
	} else {
		repositories = s.loadOpenTodoRepositories(ctx, ids)
	}
	for _, t := range tasks {
		rows := repositories[t.ID]
		if t.DoneAt != nil {
			idx.addReferences(t.ID, rows)
			for _, raw := range append(slices.Clone(t.PRs), t.PRURL) {
				ref, _ := inboxLinkRef(raw)
				if u := triageNormalizePRURL(raw); u != "" && !idx.references[t.ID][ref] {
					if other, ok := acceptedPRs[u]; !ok || t.DoneAt.After(*other.DoneAt) {
						acceptedPRs[u] = t
					}
				}
			}
			continue
		}
		idx.add(t, rows)
	}
	open := idx.open
	b := &s.inbox
	linked := map[*InboxItem]bool{}
	var writes []*inboxWrite
	var items []*InboxItem
	var unlinked []string
	var closes map[string][]uint64
	var done []*InboxItem
	itemVisibilityExcludedLocked := func(it *InboxItem) bool {
		if providerAllowed != nil && !providerAllowed[it.Source] {
			return true
		}
		return inboxVisibilityWithMembersExcludedLocked(b, it, visCfg)
	}
	b.mu.Lock()
	for _, it := range b.items {
		if itemVisibilityExcludedLocked(it) || it.externallyRouted() || !inboxTaskLinkEligible(it) {
			continue
		}
		if !it.open() {
			if it.Agent != nil && it.Agent.TodoID != "" {
				done = append(done, it) // its detail still names a task: the one doing the work now (#6546)
			}
			continue
		}
		if a := it.Agent; a != nil && a.TodoID != "" {
			if t, ok := open[a.TodoID]; ok && t.InboxItem != it.ID && (it.TodoForward == nil || it.TodoForward.TodoID != a.TodoID) {
				repo := inboxScopeOf(it).repo
				link, thread := inboxItemRefs(it, repo, inboxItemNumber(it))
				inRepo := repo == "" || slices.Contains(idx.bound[link], t.ID) ||
					taskOfRepo(t, repo) || inboxBoundToRepo(repositories[t.ID], repo)
				if idx.itemIsReference(t.ID, link, thread) || !inRepo {
					w := beginWrite(it)
					unlinked = append(unlinked, it.ID+" from "+a.TodoID)
					a.TodoID = ""
					b.settle(w)
					writes = append(writes, w)
					continue // one write per item a pass: a URL of it can link it on the next one
				}
			}
		}
		if it.Agent == nil || it.Agent.TodoID == "" {
			if closes == nil {
				closes = inboxClosesIndex(b.items, itemVisibilityExcludedLocked)
			}
			id := idx.match(it, closes)
			if id == "" {
				if u := inboxOpenPR(it); u != "" {
					repo, n := inboxScopeOf(it).repo, inboxItemNumber(it)
					ref := githubRef(repo, n)
					if n != 0 && len(idx.bound[ref]) == 0 && len(idx.legacyTasksOf(ref, repo, n)) == 0 {
						id = acceptedPRs[u].ID
					}
				}
			}
			if id != "" {
				w := beginWrite(it)
				if it.Agent == nil {
					it.Agent = &InboxAgent{State: AgentSkipped}
				}
				it.Agent.TodoID = id
				b.settle(w)
				writes = append(writes, w)
				linked[it] = true
			}
		}
		if it.Agent != nil && it.Agent.TodoID != "" {
			items = append(items, it)
		}
	}
	b.mu.Unlock()
	for _, u := range unlinked {
		s.logf("inbox: unlinked %s: the item's repository link is outside the task's routing scope", u)
	}
	failed := s.saveWrites(ctx, writes, "its task URL link")
	kept := items[:0]
	for _, it := range items {
		if !failed[it.ID] {
			kept = append(kept, it)
		}
	}
	s.superviseInbox(ctx, kept, linked)
	s.fileTaskDone(ctx, kept)
	s.followContinuations(ctx, done) // a handled item gets no signal, but its link follows the work as an open one's does
}

// fileTaskDone files the open items still linked, after superviseInbox followed any continuation, to a to-do that is
// done (not cancelled): the work is finished, so the item needs nothing. An item its conversation handles or that has a
// suggestion stays. Saved like any item write; a failed save undoes it.
func (s *Service) fileTaskDone(ctx context.Context, items []*InboxItem) {
	b := &s.inbox
	followAcceptedPRs := s.inboxSignalsOn(ctx)
	pending := map[string]bool{}
	s.todos.mu.RLock()
	for _, task := range s.todos.todos {
		if task.InboxItem != "" && (task.DoneAt == nil || task.Cancelled()) {
			pending[task.InboxItem] = true
		}
	}
	s.todos.mu.RUnlock()
	var writes []*inboxWrite
	for _, it := range items {
		b.mu.Lock()
		var id string
		if taskDoneFileable(it) {
			id = it.Agent.TodoID
		}
		b.mu.Unlock()
		if id == "" {
			continue
		}
		t, ok := s.todoNow(id) // not under b.mu: todos.mu is taken on its own
		if !ok || t.DoneAt == nil || t.Cancelled() || pending[it.ID] {
			continue
		}
		b.mu.Lock()
		if b.items[it.ID] == it && taskDoneFileable(it) && it.Agent.TodoID == id &&
			(!followAcceptedPRs || !acceptedPRFeedback(t, inboxOpenPR(it))) {
			now := time.Now()
			w := beginWrite(it)
			it.done(now, doneByManager)
			b.chainAdd(it, InboxChainEntry{Kind: ChainDone, At: now.UTC(), By: doneByManager, Text: i18n.T(b.userLang(), "assistant-inbox_chain.done_task"),
				Detail: clipRunesTo(t.Title, 60), Link: &InboxChainLink{Type: LinkTodo, ID: id}})
			it.SettledOf = taskDoneEvidence(id)
			b.settle(w)
			writes = append(writes, w)
		}
		b.mu.Unlock()
	}
	s.saveWrites(ctx, writes, "its filing for a finished task")
}

// taskDoneFileable: an open item linked to a to-do that its finishing may file: no processing conversation of the AI
// twin (that one decides, and its judge links the item to the task carrying the work on), nothing handling it, no
// suggestion waiting, and not filed for this to-do before (reopened by hand, or by new messages, it stays). Under b.mu.
func taskDoneFileable(it *InboxItem) bool {
	return !it.VisibilityExcluded && it.open() && it.Agent != nil && it.Agent.TodoID != "" && it.Agent.SessionID == "" && !it.handled() &&
		currentSuggestion(it) == nil && it.SettledOf != taskDoneEvidence(it.Agent.TodoID)
}

// taskDoneEvidence is SettledOf for an item filed because its to-do finished.
func taskDoneEvidence(todoID string) string { return "todo:" + todoID }
