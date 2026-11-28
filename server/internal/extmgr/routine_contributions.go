package extmgr

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"sync"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/nwverse/persona/sdk/go/wire"
	"github.com/nwverse/persona/server/internal/extension"
	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/modapi"
	"github.com/nwverse/persona/server/internal/wireids"
)

const (
	routineCollectMethod = "routine_facts.collect"
	routineCollectWithin = 2 * time.Second
	routineMostSources   = 8
	routineAnswerBytes   = 32 << 10
)

var _ modapi.RoutineContributionSource = (*Manager)(nil)

// Bind each item to its immutable effective manifest and supervisor. A switch or
// upgrade during collection invalidates the old answer.
type routineTarget struct {
	owner    string
	manifest *extension.Manifest
	process  *hubExtension
	item     wire.RoutineFactsItem
}

func routineAllowed(p *extension.Extension) bool {
	return p != nil && p.Record.Enabled && p.Problem == "" && p.Manifest != nil &&
		p.Manifest.Permissions.RoutineFacts && p.Record.Granted.RoutineFacts
}

// routineTargets returns a deterministic, bounded prefix of enabled items that have a supervisor.
func (ps *extensions) routineTargets() []routineTarget {
	var targets []routineTarget
	for _, p := range ps.reg.Snapshots() { // already ordered by extension id
		if !routineAllowed(p) {
			continue
		}
		hp := ps.get(p.Record.ID)
		if hp == nil || hp.proc == nil || (!hp.proc.Spec().OnCall && !hp.proc.Ready()) {
			continue
		}
		items := p.Manifest.RoutineFacts()
		slices.SortFunc(items, func(a, b wire.RoutineFactsItem) int { return cmp.Compare(a.ID, b.ID) })
		for _, item := range items {
			targets = append(targets, routineTarget{p.Record.ID, p.Manifest, hp, item})
			if len(targets) == routineMostSources {
				return targets
			}
		}
	}
	return targets
}

func (target routineTarget) current(ps *extensions) bool {
	p := ps.reg.Snapshot(target.owner)
	return routineAllowed(p) && p.Record.ID == target.owner && p.Manifest == target.manifest &&
		ps.get(target.owner) == target.process
}

type routineRead struct {
	entry      modapi.RoutineContribution
	generation uint64
}

func (target routineTarget) read(ctx context.Context, ps *extensions, day, language string) routineRead {
	if !target.current(ps) {
		return routineRead{}
	}
	proc := target.process.proc
	// Only on-call activation may wake an idle provider. For an extension whose
	// connector owns its lifecycle, respect the connector's switch.
	if proc.Spec().OnCall {
		release := proc.Hold()
		defer release()
		if err := proc.Wait(ctx); err != nil {
			return routineRead{}
		}
	}
	generation := proc.Generation()
	if generation == 0 || ctx.Err() != nil || !target.current(ps) {
		return routineRead{}
	}
	params := wire.RoutineFactsParams{Contribution: target.item.ID, Date: day, Language: language}
	var raw json.RawMessage
	if err := proc.Call(ctx, routineCollectMethod, params, &raw); err != nil {
		ps.logf("extension %s: routine contribution %s unavailable", target.owner, target.item.ID)
		return routineRead{}
	}
	entry, err := routineAnswer(target.owner, target.item, raw)
	if err != nil {
		// A rejected answer can contain private text. Log only its source.
		ps.logf("extension %s: routine contribution %s returned an invalid answer", target.owner, target.item.ID)
		return routineRead{}
	}
	if ctx.Err() != nil {
		return routineRead{}
	}
	return routineRead{entry, generation}
}

// RoutineContributions collects facts for one routine occurrence. Each provider sees only its
// item id, local calendar date and language. All calls share a deadline while healthy providers
// can answer independently. Nothing is cached across runs.
func (h *Manager) RoutineContributions(ctx context.Context, day, language string) []modapi.RoutineContribution {
	if h == nil || !h.startedNow() || h.extensions == nil || h.extensions.reg == nil || ctx.Err() != nil {
		return nil
	}
	if _, err := time.Parse(time.DateOnly, day); err != nil {
		return nil
	}
	language = string(i18n.Resolve(language).Or(i18n.Default))
	ps := h.extensions
	targets := ps.routineTargets()
	if len(targets) == 0 {
		return nil
	}
	readCtx, cancel := context.WithTimeout(ctx, routineCollectWithin)
	defer cancel()
	answers := make([]routineRead, len(targets))
	var reads sync.WaitGroup
	for i, target := range targets {
		reads.Go(func() {
			answers[i] = target.read(readCtx, ps, day, language)
		})
	}
	reads.Wait()
	if ctx.Err() != nil {
		return nil
	}
	var out []modapi.RoutineContribution
	for i, answer := range answers {
		target := targets[i]
		if answer.generation != 0 && target.current(ps) && target.process.proc.Generation() == answer.generation &&
			(len(answer.entry.Facts) != 0 || len(answer.entry.Sections) != 0) {
			out = append(out, answer.entry)
		}
	}
	return out
}

func routineAnswer(owner string, item wire.RoutineFactsItem, raw json.RawMessage) (modapi.RoutineContribution, error) {
	var empty modapi.RoutineContribution
	if len(raw) > routineAnswerBytes || !utf8.Valid(raw) {
		return empty, errors.New("invalid routine answer size or encoding")
	}
	if err := wire.ValidateResult(routineCollectMethod, raw); err != nil {
		return empty, err
	}
	var result wire.RoutineFactsResult
	if err := json.Unmarshal(raw, &result); err != nil {
		return empty, err
	}
	entry := modapi.RoutineContribution{ExtensionID: wireids.ExtensionID(owner), ContributionID: item.ID, Title: item.Title}
	for _, fact := range result.Facts {
		if !routineTextOK(fact, false) {
			return empty, errors.New("invalid routine fact")
		}
		entry.Facts = append(entry.Facts, strings.TrimSpace(fact))
	}
	for _, section := range result.Sections {
		if !routineTextOK(section.Title, false) || !routineTextOK(section.Body, true) {
			return empty, errors.New("invalid routine section")
		}
		entry.Sections = append(entry.Sections, modapi.RoutineSection{
			Title: strings.TrimSpace(section.Title), Body: strings.TrimSpace(section.Body),
		})
	}
	return entry, nil
}

func routineTextOK(s string, multiline bool) bool {
	return strings.TrimSpace(s) != "" && !strings.ContainsFunc(s, func(r rune) bool {
		return unicode.IsControl(r) && !(multiline && (r == '\n' || r == '\t'))
	})
}
