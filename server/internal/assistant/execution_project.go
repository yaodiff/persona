package assistant

import (
	"context"
	"slices"

	"github.com/nwverse/persona/server/internal/proto"
)

// The four dimensions on the records that name a launcher (docs/assistant/execution-spec.md "Launchers are aliases"):
// a tier's candidates and a conversation's pick keep the launcher that runs, and may also say the runtime and
// channel they were asked for in. Given without a launcher, a save fills the alias that runs them; given with one,
// the launcher wins. A record that says neither is read and written exactly as before.

// projectTiers fills, in every tier of tiers, the launcher of each candidate that names a runtime or a channel and
// no launcher with the alias that runs them; a combination that cannot run is refused with why.
func (s *Service) projectTiers(ctx context.Context, tiers map[string]ModelTier) error {
	var d *execDecl
	for name, t := range tiers {
		list := slices.Clone(t.list())
		changed := false
		for i, c := range list {
			if c.Launcher != "" || c.Runtime == "" && c.Channel == "" {
				continue
			}
			if d == nil {
				decl := s.execDecl(ctx)
				d = &decl
			}
			v := d.resolve(proto.ExecutionSpec{Runtime: c.Runtime, Channel: c.Channel, Model: c.Model, Effort: c.Effort}, "")
			if v.blocked() {
				return v.err()
			}
			list[i].Launcher, list[i].Runtime, list[i].Channel = v.launcher, v.spec.Runtime, v.spec.Channel
			changed = true
		}
		if !changed {
			continue
		}
		if len(t.Candidates) > 0 {
			t.Candidates = list
		}
		t.setFirst(list[0])
		tiers[name] = t
	}
	return nil
}

// reprojectCandidates is list with each candidate that keeps a runtime and channel run on the alias that runs them
// now, when its launcher no longer does (the alias was renamed, or the channel rebound). Candidates without them are
// left as they are, and nothing is read when none has them.
func (s *Service) reprojectCandidates(ctx context.Context, list []TierCandidate) []TierCandidate {
	if !slices.ContainsFunc(list, func(c TierCandidate) bool { return c.Runtime != "" && c.Channel != "" }) {
		return list
	}
	d := s.execDecl(ctx)
	out := slices.Clone(list)
	for i, c := range out {
		if c.Runtime == "" || c.Channel == "" {
			continue
		}
		if a, ok := d.alias(c.Launcher); ok && a.spec.Runtime == c.Runtime && a.spec.Channel == c.Channel {
			continue
		}
		if l := d.launcherFor(c.Runtime, c.Channel); l != "" {
			out[i].Launcher = l
		}
	}
	return out
}

// projectPick is a pick asked for with a runtime or a channel: with a launcher, the launcher's own runtime and
// channel; without, the alias that runs them on the conversation's computer, and the model as that channel takes it.
// A combination that cannot run there is refused with why.
func (s *Service) projectPick(ctx context.Context, sid, machine string, in proto.ModelPick) (proto.ModelPick, error) {
	d := s.execDecl(ctx)
	if in.Launcher != "" {
		spec, _ := d.specOfLauncher(in.Launcher, in.Model, in.Effort)
		in.Runtime, in.Channel = spec.Runtime, spec.Channel // unknown: empty, and the launcher's own checks answer
		return in, nil
	}
	on := s.logicalExecutionMachine(ctx, s.conversationMachine(ctx, sid, machine), sid)
	v := d.resolve(proto.ExecutionSpec{Runtime: in.Runtime, Channel: in.Channel, Model: in.Model, Effort: in.Effort}, on)
	if v.blocked() {
		return in, v.err()
	}
	in.Launcher, in.Runtime, in.Channel = v.launcher, v.spec.Runtime, v.spec.Channel
	if in.Model != "" {
		in.Model = v.wireModel
	}
	return in, nil
}
