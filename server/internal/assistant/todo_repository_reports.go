package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/url"
	"slices"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/assistant/repository"
	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/proto"
)

type todoRepositoryImportStore interface {
	ImportTodoRepositoryLinks(context.Context, string, []string, string) (repository.TaskSet, bool, error)
}

type todoRepositoryLinkDetailsStore interface {
	ImportTodoRepositoryLinkDetails(context.Context, string, []repository.LinkInput, string) (repository.TaskSet, bool, error)
}

// knownTodoLinks are the links a to-do kept before repository associations, read offline, with the addresses they
// were found as.
func knownTodoLinks(todo Todo) (raws, urls []string) {
	for _, raw := range append(append([]string{}, todo.PRs...), todo.PRURL) {
		if _, link, err := repository.ParseTaskLink(raw); err == nil {
			raws, urls = append(raws, raw), append(urls, link.CanonicalURL)
		}
	}
	return raws, urls
}

func (s *Service) importTodoRepositoryLinks(ctx context.Context, id string, urls []string, source string) error {
	st, ok := s.store().(todoRepositoryImportStore)
	if !ok {
		return nil
	} // Older/in-memory adapters retain their existing task behavior.
	_, changed, err := st.ImportTodoRepositoryLinks(ctx, id, urls, source)
	if err == nil && changed {
		s.todosChanged(ctx)
	}
	return err
}

func (s *Service) importLegacyTodoRepositoryLinks(ctx context.Context, id string) error {
	if _, ok := s.store().(todoRepositoryImportStore); !ok {
		return nil
	}
	if err := s.ensureTodosLoaded(ctx); err != nil {
		return err
	}
	todo, ok := s.todoNow(id)
	if !ok {
		return db.ErrTodoNotFound
	}
	// This runs on every read: it never asks a provider and leaves the links one answers for alone. Until the providers
	// are listed (right after a restart) it waits for a later read, so their hosts never get the built-in guess.
	raws, known := knownTodoLinks(todo)
	src, urls := s.linkSources(ctx, id), []string{}
	for i, raw := range raws {
		in := repository.LinkInput{URL: raw}
		_, route, err := s.routeLinkURL(src, &in)
		if route == routeUnknown {
			return nil
		}
		if err == nil && route == routeBuiltin {
			urls = append(urls, known[i])
		}
	}
	return s.importTodoRepositoryLinks(ctx, id, urls, "legacy")
}

func (s *Service) toolTodoRepositoryLinksAdd(ctx context.Context, machine, self, name string, args json.RawMessage) (string, error) {
	var in struct {
		ID    string   `json:"id"`
		URLs  []string `json:"urls"`
		Links []struct {
			URL   string `json:"url"`
			Title string `json:"title,omitempty"`
		} `json:"links"`
	}
	if err := decodeTaskRepositoryInput(args, &in); err != nil {
		return "", err
	}
	if count := len(in.URLs) + len(in.Links); count == 0 || count > 50 {
		return "", repository.Problem("invalid")
	}
	if err := s.ensureTodosLoaded(ctx); err != nil {
		return "", err
	}
	todo, ok := s.todoNow(in.ID)
	if !ok {
		return "", db.ErrTodoNotFound
	}
	if ok, outcome, err := s.confirmOutbound(ctx, machine, self, name, "", todo.Title, args); err != nil || !ok {
		return todoNotAllowed(outcome, err)
	}
	if _, ok := s.store().(todoRepositoryImportStore); !ok {
		return "", repository.Problem("store_unavailable")
	}
	if err := s.importLegacyTodoRepositoryLinks(ctx, in.ID); err != nil {
		return "", err
	}
	// Links on an extension provider's repositories are resolved first, outside the store's transaction.
	links := make([]repository.LinkInput, 0, len(in.URLs)+len(in.Links))
	for _, raw := range in.URLs {
		links = append(links, repository.LinkInput{URL: raw})
	}
	for _, link := range in.Links {
		links = append(links, repository.LinkInput{URL: link.URL, Title: link.Title})
	}
	src, budget, resolved := s.linkSources(ctx, in.ID), withProviderBudget(ctx), false
	for i := range links {
		handled, err := s.resolveLinkURL(budget, src, &links[i], "")
		if err != nil {
			return "", s.taskRepositoryToolError(ctx, err)
		}
		resolved = resolved || handled
	}
	if len(in.Links) == 0 && !resolved {
		if err := s.importTodoRepositoryLinks(ctx, in.ID, in.URLs, "explicit"); err != nil {
			return "", err
		}
	} else if err := s.importTodoRepositoryLinkDetails(ctx, in.ID, links, "explicit"); err != nil {
		return "", err
	}
	set, err := s.readTodoRepositories(ctx, in.ID)
	if err != nil {
		return "", err
	}
	raw, err := json.Marshal(set)
	return string(raw), err
}

// Keep an exact canonical association list in agent context instead of a second PR list.
func formatTodoRepositoryContext(set repository.TaskSet) string {
	text := fmt.Sprintf("repositories (revision %d):\n", set.Revision)
	for _, row := range set.Repositories {
		text += fmt.Sprintf("- %s %s\n", row.RepoID, row.SourceSnapshot.Identity.CanonicalURL)
		for _, link := range row.Links {
			text += fmt.Sprintf("  %s %s\n", link.Kind, link.CanonicalURL)
			if link.Title != "" {
				text += fmt.Sprintf("    title: %s\n", link.Title)
			}
		}
	}
	return text
}

// Task agents use the same store and tools, bounded by their active session link.
func (s *Service) agentRepositoryTool(ctx context.Context, machine, session, name string, args json.RawMessage) (string, error) {
	if session == "" {
		return "", errors.New("Persona could not tell which agent session is calling")
	}
	if err := s.ensureTodosLoaded(ctx); err != nil {
		return "", err
	}
	todo, ok := s.openTodoOfAgent(machine, session)
	if !ok {
		return "", errors.New("you are not working on an open todo")
	}
	var in struct {
		ID string `json:"id"`
	}
	if err := json.Unmarshal(args, &in); err != nil {
		return "", err
	}
	if in.ID != todo.ID {
		return "", errors.New("repository tools can access only the todo this agent works on; read todo_decisions for its ID")
	}
	if name == "todo_repository_links_add" {
		return s.toolTodoRepositoryLinksAdd(ctx, machine, session, name, args)
	}
	return s.toolTodoRepositories(ctx, machine, session, name, args)
}

// taskRepositoryReportLinks are the addresses in an agent's report, without the punctuation after them.
func taskRepositoryReportLinks(text string) []string {
	if block := deliveryCloseout(text); block != "" {
		var artifacts []proto.WorkArtifact
		for _, line := range strings.Split(block, "\n") {
			if prefix := closeoutPRLine.FindString(line); prefix != "" {
				artifacts = append(artifacts, proto.WorkArtifact{Kind: "pr", Ref: strings.TrimPrefix(line, prefix)})
			}
		}
		return taskRepositoryArtifactLinks(artifacts, true)
	}
	urls := []string{}
	for _, raw := range inboxTaskURL.FindAllString(text, -1) {
		urls = append(urls, strings.TrimRight(raw, ".,;:!?)]}，。；：！）》」』")) // i18n-ignore: report punctuation, not displayed text
	}
	return urls
}

// Provider-specific addresses are candidates, not native CLI text proof.
func taskRepositoryArtifactLinks(artifacts []proto.WorkArtifact, includeProviderURLs bool) []string {
	urls := []string{}
	for _, artifact := range artifacts {
		if artifact.Kind != "pr" {
			continue
		}
		for _, raw := range inboxTaskURL.FindAllString(artifact.Ref, -1) {
			raw = strings.TrimRight(raw, ".,;:!?)]}，。；：！）》」』") // i18n-ignore: artifact punctuation, not displayed text
			u, err := url.Parse(raw)
			if err != nil {
				continue
			}
			u.User = nil
			u.RawQuery, u.Fragment, u.RawFragment, u.ForceQuery = "", "", "", false
			u.Path = strings.TrimRight(u.Path, "/")
			identity, link, err := repository.ParseTaskLink(u.String())
			if err != nil {
				// Unknown layouts remain candidates; their provider and ownership must both agree.
				if includeProviderURLs && linkShape(repository.LinkInput{URL: raw}) == nil {
					urls = append(urls, raw)
				}
				continue
			}
			if link.Kind != "pull_request" {
				continue
			}
			// Validate before discarding a query or fragment, including credentials.
			if _, err := repository.Parse(raw, identity.Provider); err == nil {
				urls = append(urls, link.CanonicalURL)
			}
		}
	}
	return urls
}

// A report or a model-produced artifact is only a candidate. A native successful
// PR-create outcome, or the node's independently read author and pushed checkout,
// establishes ownership. Missing evidence leaves automatic linking closed.
func (s *Service) importOwnedTaskRepositoryLinks(ctx context.Context, todo Todo, machine, session string, urls []string, reportText string, reportAt time.Time,
	onDeferred func(context.Context, []acceptedReportLink)) ([]acceptedReportLink, error) {
	inputs := make([]repository.LinkInput, 0, len(urls))
	for _, url := range urls {
		inputs = append(inputs, repository.LinkInput{URL: url})
	}
	owned, err := s.ownedTaskRepositoryLinkInputs(ctx, todo, machine, session, inputs, reportText, reportAt)
	if err != nil || len(owned) == 0 {
		return nil, err
	}
	urls = make([]string, 0, len(owned))
	for _, in := range owned {
		urls = append(urls, in.URL)
	}
	return s.importReportedLinks(ctx, todo.ID, urls, "pull_request",
		func(ctx context.Context, links []repository.LinkInput) ([]acceptedReportLink, error) {
			return s.importOwnedTaskRepositoryLinkDetails(ctx, todo, machine, session, owned, links, reportText, reportAt)
		}, onDeferred)
}

func (s *Service) ownedTaskRepositoryLinkInputs(ctx context.Context, todo Todo, machine, session string,
	links []repository.LinkInput, reportText string, reportAt time.Time) ([]repository.LinkInput, error) {
	linked := taskRepositoryAgentLinkedAt(todo, machine, session)
	if linked.IsZero() || len(links) == 0 {
		return nil, nil
	}
	urls := make([]string, 0, len(links))
	for _, in := range links {
		urls = append(urls, in.URL)
	}
	proven := map[string]bool{}
	markNativePR := func(raw string) {
		if raw != "" && linkShape(repository.LinkInput{URL: raw}) == nil {
			proven[canonicalReplyPR(raw)] = true
		}
		for _, address := range taskRepositoryArtifactLinks([]proto.WorkArtifact{{Kind: "pr", Ref: raw}}, false) {
			proven[canonicalReplyPR(address)] = true
		}
	}
	pushed := map[string]string{}
	pushCwd := ""
	if items := s.taskRepositoryReportTurn(ctx, todo, machine, session, reportText, reportAt); len(items) != 0 {
		reportAt = reportAt.Truncate(time.Microsecond)
		linkedAt := linked.Truncate(time.Microsecond)
		calls := map[string]proto.Item{}
		for _, it := range items {
			if it.Kind == proto.KindToolUse && it.ToolUseID != "" {
				calls[it.ToolUseID] = it
				continue
			}
			if it.Kind != proto.KindToolResult || it.IsError || it.TS.IsZero() || it.TS.Before(linkedAt) || it.TS.After(reportAt) ||
				it.Outcome == nil || it.Outcome.Status != proto.OutcomeOK ||
				it.Outcome.ExitCode != nil && *it.Outcome.ExitCode != 0 {
				continue
			}
			if git := it.Outcome.Git; git != nil {
				if pr := git.PR; pr != nil && pr.Action == "created" {
					markNativePR(pr.URL)
				}
			}
			call, found := calls[it.ToolUseID]
			if !found || call.TS.IsZero() || call.TS.Before(linkedAt) || call.TS.After(reportAt) {
				continue
			}
			op, cwd := taskRepositoryNativeCommand(call)
			switch output := it.Text + "\n" + it.Outcome.Tail; op {
			case "create":
				urls := taskRepositoryArtifactLinks([]proto.WorkArtifact{{Kind: "pr", Ref: output}}, false)
				slices.Sort(urls)
				urls = slices.Compact(urls)
				if len(urls) == 1 {
					proven[urls[0]] = true
				}
			case "push":
				for _, match := range repositoryPushResult.FindAllStringSubmatch(output, -1) {
					pushed[strings.TrimPrefix(match[1], "refs/heads/")] = cwd
					if cwd != "" {
						pushCwd = cwd
					}
				}
			}
		}
	}
	if slices.ContainsFunc(urls, func(raw string) bool {
		return !proven[canonicalReplyPR(raw)] && githubPROwnershipCandidate(raw)
	}) {
		if sess, ok := s.session(ctx, machine, session); ok && sess.Cwd != "" {
			cwd := sess.Cwd
			if pushCwd != "" {
				cwd = pushCwd // a successful native push may run in another worktree
			}
			var result struct {
				Branch  string `json:"branch"`
				HeadOID string `json:"head_oid"`
				Viewer  string `json:"viewer"`
				PR      *struct {
					URL     string `json:"url"`
					Author  string `json:"author"`
					Head    string `json:"head"`
					HeadOID string `json:"head_oid"`
				} `json:"pr"`
			}
			err := s.call(ctx, machine, "git.pr", map[string]any{"cwd": cwd, "ownership": true}, &result)
			if err != nil && len(proven) == 0 {
				return nil, err
			} else if pr := result.PR; err == nil && pr != nil {
				pushPath, pushSeen := pushed[pr.Head]
				ownPush := pushSeen && (pushPath == cwd || pushPath == "" && cwd == sess.Cwd)
				if result.Viewer != "" && (strings.EqualFold(pr.Author, result.Viewer) || ownPush) &&
					result.Branch != "" && pr.Head == result.Branch && result.HeadOID != "" && pr.HeadOID == result.HeadOID {
					markNativePR(pr.URL)
				}
			}
		}
	}
	var owned []string
	var inputs []repository.LinkInput
	for _, in := range links {
		if proven[canonicalReplyPR(in.URL)] {
			owned = append(owned, in.URL)
			inputs = append(inputs, in)
		}
	}
	if len(owned) == 0 {
		return nil, nil
	}
	current, active := s.openTodoOfAgent(machine, session)
	if !active || current.ID != todo.ID || !taskRepositoryAgentLinkedAt(current, machine, session).Equal(linked) {
		return nil, nil
	}
	return inputs, nil
}

func taskRepositoryAgentLinkedAt(todo Todo, machine, session string) time.Time {
	if todo.DoneAt == nil && !todo.Cancelled() {
		for _, a := range todo.Agents {
			if a.MachineID == machine && a.SessionID == session && a.ReleasedAt == nil && a.DismissedAt == nil {
				return a.LinkedAt
			}
		}
	}
	return time.Time{}
}

// taskRepositoryReportTurn matches the report, even when its session has other exchanges after the task link.
func (s *Service) taskRepositoryReportTurn(ctx context.Context, todo Todo, machine, session, text string, at time.Time) []proto.Item {
	at = at.Truncate(time.Microsecond)
	linked := taskRepositoryAgentLinkedAt(todo, machine, session).Truncate(time.Microsecond)
	if at.IsZero() || linked.IsZero() || at.Before(linked) {
		return nil
	}
	items, ok := s.lastTurnItems(ctx, machine, session)
	if !ok {
		return nil
	}
	if _, source := taskTurnSource(todo, items); source != "" {
		return nil
	}
	// Stored turn timestamps have Postgres microsecond precision.
	var endedAt time.Time
	for i := range items {
		items[i].TS = items[i].TS.Truncate(time.Microsecond)
		if items[i].TS.After(endedAt) {
			endedAt = items[i].TS
		}
	}
	matched := endedAt.Equal(at)
	if text != "" {
		matched = slices.ContainsFunc(items, func(it proto.Item) bool {
			return it.Role == "assistant" && it.Kind == proto.KindText && it.FullSize == 0 &&
				strings.TrimSpace(it.Text) == strings.TrimSpace(text) && (it.TS.Equal(at) || endedAt.Equal(at))
		})
	}
	if !matched {
		return nil
	}
	return items
}

func (s *Service) importOwnedTaskRepositoryLinkDetails(ctx context.Context, todo Todo, machine, session string,
	proof, inputs []repository.LinkInput, reportText string, reportAt time.Time) ([]acceptedReportLink, error) {
	if len(proof) == 0 || len(inputs) == 0 {
		return nil, nil
	}
	// A provider may canonicalize an alias. Recheck its original authorized address,
	// not the resulting metadata; the transaction stores only this resolver's inputs.
	checked, err := s.ownedTaskRepositoryLinkInputs(ctx, todo, machine, session, proof, reportText, reportAt)
	if err != nil || len(checked) != len(proof) {
		return nil, err
	}
	var set repository.TaskSet
	var changed bool
	if slices.ContainsFunc(inputs, func(in repository.LinkInput) bool { return in.Resolved != nil }) {
		st, ok := s.store().(todoRepositoryLinkDetailsStore)
		if !ok {
			return nil, repository.Problem("store_unavailable")
		}
		set, changed, err = st.ImportTodoRepositoryLinkDetails(ctx, todo.ID, inputs, "report")
	} else {
		st, ok := s.store().(todoRepositoryImportStore)
		if !ok {
			return nil, repository.Problem("store_unavailable")
		}
		urls := make([]string, 0, len(inputs))
		for _, in := range inputs {
			urls = append(urls, in.URL)
		}
		set, changed, err = st.ImportTodoRepositoryLinks(ctx, todo.ID, urls, "report")
	}
	if err != nil {
		return nil, err
	}
	if changed {
		s.todosChanged(ctx)
	}
	// The transaction may exclude a PR already assigned to another open to-do.
	// Do not let the legacy stage/PR fields bypass that exclusion.
	savedPRs := map[string]bool{}
	for _, row := range set.Repositories {
		for _, link := range row.Links {
			if link.Kind == "pull_request" {
				savedPRs[link.CanonicalURL] = true
			}
		}
	}
	var accepted []acceptedReportLink
	for _, in := range inputs {
		canonical := canonicalReplyPR(in.URL)
		if in.Resolved != nil {
			canonical = in.Resolved.Link.CanonicalURL
		}
		if savedPRs[canonical] {
			accepted = append(accepted, acceptedReportLink{raw: in.URL, canonical: canonical})
		}
	}
	return accepted, nil
}

// Artifacts also arrive through stopped-turn triage, without a full transcript
// subscription. Only the session's current task may inherit that turn's PRs.
func (s *Service) importTaskRepositoryArtifacts(ctx context.Context, e db.SessionWork) error {
	urls := taskRepositoryArtifactLinks(e.Work.Artifacts, true)
	if len(urls) == 0 || e.TurnAt.IsZero() || e.Work.RetiredAt != nil {
		return nil
	}
	if err := s.ensureTodosLoaded(ctx); err != nil {
		return err
	}
	todo, ok := s.openTodoOfAgent(e.MachineID, e.SessionID)
	if !ok {
		return nil
	}
	for _, agent := range todo.Agents {
		if agent.MachineID == e.MachineID && agent.SessionID == e.SessionID &&
			agent.ReleasedAt == nil && agent.DismissedAt == nil && !e.TurnAt.Before(agent.LinkedAt) {
			_, err := s.importOwnedTaskRepositoryLinks(ctx, todo, e.MachineID, e.SessionID, urls, "", e.TurnAt, nil)
			return err
		}
	}
	return nil
}
