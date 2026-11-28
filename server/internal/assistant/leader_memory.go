package assistant

import (
	"cmp"
	"context"
	"fmt"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/nwverse/persona/server/internal/prompts"
	"github.com/nwverse/persona/server/internal/proto"
)

const (
	// leaderProjectMemoryMax caps the extension project memory injected into a leader conversation.
	leaderProjectMemoryMax = 1536
	// leaderDecisionsMax caps the scoped user decisions injected into a leader conversation's snapshot.
	leaderDecisionsMax = 1536
)

// withLeaderMemory appends a leader conversation's bound-project extension memory and active scoped user decisions
// to its snapshot. Because the node sends full snapshot sections on start, resume, and after compaction (and only
// changed sections between turns), leaders deterministically receive these blocks without restarting their process.
func (s *Service) withLeaderMemory(ctx context.Context, machine, session, snapshot string) string {
	if !s.isLeader(ctx, session) || !s.ownersPersonal(ctx, machine, session) {
		return snapshot
	}
	if section := s.leaderProjectMemorySection(ctx, machine, session); section != "" {
		snapshot = appendSnapshotSection(snapshot, section)
	}
	if section := s.leaderDecisionsSection(ctx, machine, session); section != "" {
		snapshot = appendSnapshotSection(snapshot, section)
	}
	return snapshot
}

// leaderBoundProject returns the active direction's bound KB project slug and display name (falling back to slug).
func (s *Service) leaderBoundProject(ctx context.Context, session string) (slug, name string) {
	d := s.directionOf(session)
	if d == nil || d.EndedAt != nil || d.Project == "" {
		return "", ""
	}
	slug, name = d.Project, d.Project
	if e, err := s.kbGet(ctx); err == nil && e != nil {
		for _, p := range e.Store.Projects() {
			if p.Slug == slug && strings.TrimSpace(p.Name) != "" {
				name = oneLine(p.Name)
				break
			}
		}
	}
	return slug, name
}

// leaderProjectMemoryEntries gathers extension project memory for leader session's bound project from both
// prompts/projects/<slug>.md (via projectExtensionContext, for non-memory_sources extensions) and memory_sources
// entries matching the bound project's slug or name (non-sensitive, deduplicated against standing), capped at
// leaderProjectMemoryMax bytes.
func (s *Service) leaderProjectMemoryEntries(ctx context.Context, machine, session string) (projectName string, entries []prompts.RecoveryEntry) {
	if !s.isLeader(ctx, session) || !s.ownersPersonal(ctx, machine, session) {
		return "", nil
	}
	slug, name := s.leaderBoundProject(ctx, session)
	if slug == "" {
		return "", nil
	}
	left := leaderProjectMemoryMax
	seenNorm := map[string]bool{}
	addLine := func(source, raw string) {
		line := oneLine(prompts.ContextBody(raw))
		if line == "" || left <= 0 {
			return
		}
		norm := normalizeMemoryLine(line)
		if norm == "" || seenNorm[norm] {
			return
		}
		if len(line) > left {
			if left < 32 {
				return
			}
			line = clipUTF8Bytes(line, left)
			if line == "" {
				return
			}
		}
		seenNorm[norm] = true
		left -= len(line)
		entries = append(entries, prompts.RecoveryEntry{Source: source, Text: line})
	}

	// 1. Extension prompts/projects blocks.
	if extBlocks := s.projectExtensionContext(ctx, []string{slug})[slug]; len(extBlocks) > 0 {
		for _, blk := range extBlocks {
			src := "project/" + slug + "/extension"
			if blk.Name != "" {
				src += "/" + blk.Name
			}
			for _, ln := range strings.Split(strings.ReplaceAll(blk.Text, "\r\n", "\n"), "\n") {
				ln = strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(ln), "- "))
				if ln == "" {
					continue
				}
				addLine(src, ln)
				if left == 0 {
					return name, entries
				}
			}
		}
	}

	// 2. memory_sources entries bound to this project (slug or KB project name), non-sensitive and not already standing.
	b := s.cachedExtensionMemoryBatch(ctx)
	if len(b.Entries) == 0 || left <= 0 {
		return name, entries
	}
	standingIDs, standingTexts := s.standingMemoryIDsAndTexts(ctx, session, b)
	visible := s.nonSensitiveExtensionEntries(ctx, session, b)
	slugLower := strings.ToLower(strings.TrimSpace(slug))
	nameLower := strings.ToLower(strings.TrimSpace(name))
	queryLower := strings.TrimSpace(slugLower + " " + nameLower)
	type matched struct {
		entry memoryExtensionEntry
		exact bool
		order int
	}
	var hits []matched
	for idx, e := range visible {
		if standingIDs[memoryExtensionIDPrefix+e.ID] {
			continue
		}
		if standingTexts[normalizeMemoryLine(e.Text)] {
			continue
		}
		matchedProject, exact := false, false
		for _, subj := range e.Tags {
			subjLower := strings.ToLower(strings.TrimSpace(subj))
			if subjLower == "" {
				continue
			}
			if subjLower == slugLower || (nameLower != "" && subjLower == nameLower) {
				matchedProject, exact = true, true
				break
			}
			if _, ok := matchEntityInQuery(queryLower, subj); ok {
				matchedProject = true
			}
		}
		if matchedProject {
			hits = append(hits, matched{entry: e, exact: exact, order: idx})
		}
	}
	slices.SortStableFunc(hits, func(x, y matched) int {
		if x.exact != y.exact {
			if x.exact {
				return -1
			}
			return 1
		}
		if c := cmp.Compare(y.entry.Rank, x.entry.Rank); c != 0 {
			return c
		}
		return cmp.Compare(y.order, x.order)
	})
	for _, h := range hits {
		addLine("project/"+slug+"/memory/"+h.entry.ID, h.entry.Text)
		if left == 0 {
			break
		}
	}
	return name, entries
}

func (s *Service) leaderProjectMemorySection(ctx context.Context, machine, session string) string {
	projectName, entries := s.leaderProjectMemoryEntries(ctx, machine, session)
	if len(entries) == 0 {
		return ""
	}
	d := prompts.LeaderProjectMemory{Language: answerLanguage(s.hello(ctx).Locale), Project: projectName}
	for _, e := range entries {
		d.Entries = append(d.Entries, prompts.MemoryEntry{Text: e.Text})
	}
	return prompts.RenderLeaderProjectMemory(d)
}

func clipUTF8Bytes(s string, maxBytes int) string {
	if len(s) <= maxBytes {
		return s
	}
	if maxBytes <= 3 {
		return ""
	}
	n := maxBytes - 3
	for n > 0 && !utf8.ValidString(s[:n]) {
		n--
	}
	return strings.TrimSpace(s[:n]) + "…"
}

func formatLedgerUserDecision(e proto.AssistantEntry) string {
	text := oneLine(prompts.ContextBody(e.Text))
	quote := oneLine(prompts.ContextBody(e.Quote))
	switch {
	case text == "" && quote == "":
		return ""
	case quote == "":
		return text
	case text == "" || text == quote || strings.Contains(text, quote):
		if text != "" {
			return text
		}
		return quote
	default:
		return fmt.Sprintf("%s (quote: %q)", text, quote)
	}
}

// scopedLedgerUserDecisions reads active, non-expired user decisions from the unified ledger (entriesDB) visible to
// conversation session. For a leader, it keeps global decisions, decisions scoped to the direction's bound project,
// and decisions sourced from or relayed via this leader. For main chat, it keeps global and recent (<=14d) user
// decisions. Results are returned newest first within maxBytes.
func (s *Service) scopedLedgerUserDecisions(ctx context.Context, machine, session string, main bool, projectSlug string, maxBytes int) []prompts.RecoveryEntry {
	if maxBytes <= 0 || s.isWorkMachine(machine) || machine == "" || machine != s.settings(ctx).MachineID {
		return nil
	}
	if s.isGuest(ctx, machine, session) {
		return nil
	}
	raw, err := s.memoryEntriesFor(ctx, proto.AssistantEntriesParams{Type: "decision", By: "user"}, true)
	if err != nil || len(raw) == 0 {
		return nil
	}
	now := time.Now().UTC()
	cutoff := now.AddDate(0, 0, -14)
	var scoped []proto.AssistantEntry
	for _, e := range raw {
		if e.By != "user" || e.Status != "active" {
			continue
		}
		if e.ExpiresAt != nil && !e.ExpiresAt.After(now) {
			continue
		}
		if main {
			if len(e.Scope.Project) > 0 && !e.At.IsZero() && e.At.Before(cutoff) {
				continue
			}
			scoped = append(scoped, e)
			continue
		}
		inScope := len(e.Scope.Project) == 0 ||
			(projectSlug != "" && slices.Contains(e.Scope.Project, projectSlug)) ||
			e.Source.Session == session ||
			slices.Contains(e.RelayedVia, session)
		if inScope {
			scoped = append(scoped, e)
		}
	}
	if len(scoped) == 0 {
		return nil
	}
	visible := s.recallMemory(ctx, session, scoped).Entries
	if len(visible) == 0 {
		return nil
	}
	left := maxBytes
	seen := map[string]bool{}
	var out []prompts.RecoveryEntry
	for i := len(visible) - 1; i >= 0; i-- {
		e := visible[i]
		line := formatLedgerUserDecision(e)
		if line == "" {
			continue
		}
		norm := normalizeMemoryLine(line)
		if seen[norm] {
			continue
		}
		if len(line) > left {
			continue
		}
		seen[norm] = true
		left -= len(line)
		src := fmt.Sprintf("entry/%s trust=%s", e.ID, e.Trust)
		if e.Source.Session != "" {
			src += " " + e.Source.Machine + "/" + e.Source.Session
		}
		if !e.At.IsZero() {
			src += " at=" + e.At.UTC().Format(time.RFC3339)
		}
		out = append(out, prompts.RecoveryEntry{Source: src, Text: line})
		if left == 0 {
			break
		}
	}
	return out
}

func (s *Service) leaderDecisionsSection(ctx context.Context, machine, session string) string {
	if !s.isLeader(ctx, session) || !s.ownersPersonal(ctx, machine, session) {
		return ""
	}
	slug, _ := s.leaderBoundProject(ctx, session)
	entries := s.scopedLedgerUserDecisions(ctx, machine, session, false, slug, leaderDecisionsMax)
	if len(entries) == 0 {
		return ""
	}
	d := prompts.StandingMemory{Language: answerLanguage(s.hello(ctx).Locale)}
	for _, e := range entries {
		d.Entries = append(d.Entries, prompts.MemoryEntry{Text: e.Text})
	}
	return prompts.RenderLeaderDecisions(d)
}
