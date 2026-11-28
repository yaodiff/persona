package hub

import (
	"slices"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
)

// Neutral outcomes used by the versioned push grading evaluator. The current
// live boundary keeps its persisted intercept reasons; the adapter below is the
// only place that translates between the two vocabularies.
const (
	pushGradingOutcomeHeld     = "held"
	pushGradingOutcomeSilenced = "silenced"
	pushGradingOutcomeDeduped  = "deduped"
)

type pushGradingOutcome struct {
	Allow  bool
	Level  string
	Reason string
}

// evaluatePushGradingEffective applies the versioned effective configuration
// without mutating notification, dedup, intercept, or delivery state. Attention
// composition and main-conversation level derivation remain at their existing
// live and preview boundaries in this slice.
func evaluatePushGradingEffective(
	now time.Time,
	cfg pushGradingEffective,
	policy pushPolicy,
	lastAlert map[string]time.Time,
) pushGradingOutcome {
	level := normalizePushLevel(levelFor(policy.kind, policy.level))
	if policy.system {
		level = assistantwire.LevelP0
	} else if policy.done && policy.level == "" {
		level = assistantwire.LevelP2
	}
	outcome := pushGradingOutcome{Allow: true, Level: level}
	if policy.system || !cfg.Enabled {
		return outcome
	}

	action := cfg.Actions.P1
	switch level {
	case assistantwire.LevelP0:
		action = cfg.Actions.P0
	case assistantwire.LevelP2:
		action = cfg.Actions.P2
	}
	if action == "push" {
		return outcome
	}

	// Versioned level actions are policy configuration rather than the legacy
	// ordered-rule engine. A direct push still bypasses repeat suppression;
	// configured hold and silence preserve the versioned model's dedup-first
	// outcome, which the live adapter maps to the legacy dedup reason.
	key := strings.TrimSpace(policy.relatedKey)
	if key != "" && !slices.Contains(cfg.DedupExemptLevels, level) {
		if last, ok := lastAlert[key]; ok && now.Sub(last) < cfg.DedupWindow {
			outcome.Allow = false
			outcome.Reason = pushGradingOutcomeDeduped
			return outcome
		}
	}

	outcome.Allow = false
	switch action {
	case "silence":
		outcome.Reason = pushGradingOutcomeSilenced
	default:
		// Configuration validation restricts actions to push, hold, and silence.
		// Keep an unexpected internal value fail-closed rather than delivering it.
		outcome.Reason = pushGradingOutcomeHeld
	}
	return outcome
}

// legacyPushGradingReason translates neutral evaluator outcomes at the current
// live boundary. Unknown internal values fail closed as a held P1 push; the
// versioned evaluator itself only emits the three neutral held outcomes above.
func legacyPushGradingReason(reason string) string {
	switch reason {
	case "":
		return ""
	case pushGradingOutcomeSilenced:
		return interceptReasonP2
	case pushGradingOutcomeDeduped:
		return interceptReasonDedup
	default:
		return interceptReasonP1
	}
}

// pushGradingEvaluation is the complete pure grading verdict shared between live
// delivery (applyPushGrading) and read-only preview (POST /api/v1/push/grading/preview).
type pushGradingEvaluation struct {
	Allow          bool
	Hush           bool
	InputLevel     string
	EffectiveLevel string
	Reason         string // neutral ("held", "silenced", "deduped") or attention reason ("attn_*")
	LiveReason     string // persisted intercept reason (legacy adapter for neutral outcomes, or "attn_*")
	Action         string // "push", "hush", "hold", "silence"
	Source         string // "default", "base", "attention", "extension:<id>", "draft"
	DedupHit       bool
	AttentionHeld  bool
}

func pushLevelRank(level string) int {
	switch level {
	case assistantwire.LevelP0:
		return 0
	case assistantwire.LevelP2:
		return 2
	default:
		return 1
	}
}

// applyPushGradingNotifyUserLevel derives the effective level for a NotifyPush
// (notify_user) alert under cfg when grading or attention mode is active.
// Level-less main-conversation alerts use cfg.NotifyUser.Main (default P0);
// level-less non-main alerts use cfg.NotifyUser.Others (default P1); and
// non-main alerts are capped to no more urgent than cfg.NotifyUser.OthersMax
// when configured.
func applyPushGradingNotifyUserLevel(cfg pushGradingEffective, attnActive, isMain bool, explicitLevel, derivedLevel string) string {
	if !cfg.Enabled {
		// When push grading is disabled (including explicit owner enabled:false),
		// never apply base or extension notify_user overrides during attention modes:
		// keep only the built-in attention-mode main-conversation rule.
		if !attnActive {
			return derivedLevel
		}
		if explicitLevel == "" {
			if isMain {
				return assistantwire.LevelP0
			}
			return assistantwire.LevelP1
		}
		return derivedLevel
	}
	level := derivedLevel
	if explicitLevel == "" {
		if isMain {
			level = normalizePushLevel(cfg.NotifyUser.Main)
		} else {
			level = normalizePushLevel(cfg.NotifyUser.Others)
		}
	}
	if !isMain && cfg.NotifyUser.OthersMax != "" {
		maxLevel := normalizePushLevel(cfg.NotifyUser.OthersMax)
		if pushLevelRank(level) < pushLevelRank(maxLevel) {
			level = maxLevel
		}
	}
	return level
}

func pushGradingAllowAction(hush bool) string {
	if hush {
		return "hush"
	}
	return "push"
}

func pushGradingHoldAction(reason string) string {
	if reason == pushGradingOutcomeSilenced || reason == interceptReasonP2 || reason == assistantwire.AttentionReasonSilent {
		return "silence"
	}
	return "hold"
}

func pushGradingDecisionSource(cfg pushGradingEffective, outcome pushGradingOutcome) string {
	enabledBy := cfg.By["enabled"]
	if enabledBy == "" {
		enabledBy = "default"
	}
	if !cfg.Enabled {
		return enabledBy
	}
	if outcome.Reason == pushGradingOutcomeDeduped {
		if by := cfg.By["dedup_window"]; by != "" && by != "default" {
			return by
		}
		return enabledBy
	}
	if by := cfg.By["actions."+outcome.Level]; by != "" && by != "default" {
		return by
	}
	return enabledBy
}

// evaluatePushGradingDecision composes hub system alerts, notify_user level
// derivation, active owner attention modes, and the pure versioned evaluator
// without mutating any state. baseCfg is the owner-only effective configuration
// (without an extension layer), ensuring an extension layer can never weaken an
// active owner attention mode via notify_user promotion or actions.
func evaluatePushGradingDecision(
	now time.Time,
	cfg pushGradingEffective,
	baseCfg pushGradingEffective,
	attn assistantwire.Attention,
	lastAlert map[string]time.Time,
	policy pushPolicy,
) pushGradingEvaluation {
	inputLevel := normalizePushLevel(levelFor(policy.kind, policy.level))
	if policy.system {
		inputLevel = assistantwire.LevelP0
	} else if policy.done && policy.level == "" {
		inputLevel = assistantwire.LevelP2
	}

	// Hub system alerts (pairing, computer joining) are always P0 and are never
	// modified or held by base push grading config or extension layers.
	if policy.system {
		if attn.Active() {
			v := assistantwire.DecideAttention(attn, attentionPush(policy, assistantwire.LevelP0))
			if v.Hold {
				return pushGradingEvaluation{
					Allow:          false,
					InputLevel:     assistantwire.LevelP0,
					EffectiveLevel: assistantwire.LevelP0,
					Reason:         v.Reason,
					LiveReason:     v.Reason,
					Action:         pushGradingHoldAction(v.Reason),
					Source:         pushGradingSourceAttention,
					AttentionHeld:  true,
				}
			}
			return pushGradingEvaluation{
				Allow:          true,
				Hush:           v.Hush,
				InputLevel:     assistantwire.LevelP0,
				EffectiveLevel: assistantwire.LevelP0,
				Action:         pushGradingAllowAction(v.Hush),
				Source:         pushGradingSourceAttention,
			}
		}
		src := cfg.By["enabled"]
		if src == "" {
			src = "default"
		}
		return pushGradingEvaluation{
			Allow:          true,
			InputLevel:     assistantwire.LevelP0,
			EffectiveLevel: assistantwire.LevelP0,
			Action:         "push",
			Source:         src,
		}
	}

	baseLevel := inputLevel
	effectiveLevel := inputLevel
	if policy.kind == assistantwire.NotifyPush && policy.asked {
		explicit := policy.level
		if policy.derivedLevel {
			explicit = ""
		}
		baseLevel = applyPushGradingNotifyUserLevel(baseCfg, attn.Active(), policy.mainConv, explicit, inputLevel)
		effectiveLevel = applyPushGradingNotifyUserLevel(cfg, attn.Active(), policy.mainConv, explicit, inputLevel)
	}

	// Active owner attention mode runs first and is never weakened by an
	// extension layer (including notify_user promotion).
	if attn.Active() {
		baseAttn := assistantwire.DecideAttention(attn, attentionPush(policy, baseLevel))
		if baseAttn.Hold {
			return pushGradingEvaluation{
				Allow:          false,
				InputLevel:     inputLevel,
				EffectiveLevel: baseLevel,
				Reason:         baseAttn.Reason,
				LiveReason:     baseAttn.Reason,
				Action:         pushGradingHoldAction(baseAttn.Reason),
				Source:         pushGradingSourceAttention,
				AttentionHeld:  true,
			}
		}
		effAttn := assistantwire.DecideAttention(attn, attentionPush(policy, effectiveLevel))
		if effAttn.Hold {
			src := cfg.By["notify_user.others"]
			if policy.mainConv {
				src = cfg.By["notify_user.main"]
			}
			if src == "" || src == "default" {
				src = pushGradingSourceAttention
			}
			return pushGradingEvaluation{
				Allow:          false,
				InputLevel:     inputLevel,
				EffectiveLevel: effectiveLevel,
				Reason:         effAttn.Reason,
				LiveReason:     effAttn.Reason,
				Action:         pushGradingHoldAction(effAttn.Reason),
				Source:         src,
				AttentionHeld:  true,
			}
		}
		if cfg.Enabled {
			evalPolicy := policy
			evalPolicy.level = effectiveLevel
			outcome := evaluatePushGradingEffective(now, cfg, evalPolicy, lastAlert)
			if !outcome.Allow {
				return pushGradingEvaluation{
					Allow:          false,
					InputLevel:     inputLevel,
					EffectiveLevel: outcome.Level,
					Reason:         outcome.Reason,
					LiveReason:     legacyPushGradingReason(outcome.Reason),
					Action:         pushGradingHoldAction(outcome.Reason),
					Source:         pushGradingDecisionSource(cfg, outcome),
					DedupHit:       outcome.Reason == pushGradingOutcomeDeduped,
				}
			}
		}
		hush := baseAttn.Hush || effAttn.Hush
		return pushGradingEvaluation{
			Allow:          true,
			Hush:           hush,
			InputLevel:     inputLevel,
			EffectiveLevel: effectiveLevel,
			Action:         pushGradingAllowAction(hush),
			Source:         pushGradingSourceAttention,
		}
	}

	evalPolicy := policy
	evalPolicy.level = effectiveLevel
	outcome := evaluatePushGradingEffective(now, cfg, evalPolicy, lastAlert)
	if outcome.Allow {
		return pushGradingEvaluation{
			Allow:          true,
			InputLevel:     inputLevel,
			EffectiveLevel: outcome.Level,
			Action:         "push",
			Source:         pushGradingDecisionSource(cfg, outcome),
		}
	}
	return pushGradingEvaluation{
		Allow:          false,
		InputLevel:     inputLevel,
		EffectiveLevel: outcome.Level,
		Reason:         outcome.Reason,
		LiveReason:     legacyPushGradingReason(outcome.Reason),
		Action:         pushGradingHoldAction(outcome.Reason),
		Source:         pushGradingDecisionSource(cfg, outcome),
		DedupHit:       outcome.Reason == pushGradingOutcomeDeduped,
	}
}
