package hub

import (
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
)

func gradingEffectiveConfig(t *testing.T, raw string) pushGradingEffective {
	t.Helper()
	cfg := parseGradingTestConfig(t, raw, false)
	return resolvePushGrading(&cfg, nil, "")
}

func gradingEffectivePolicy(level, key string) pushPolicy {
	return pushPolicy{kind: assistantwire.NotifyPush, level: level, relatedKey: key, manager: assistantwire.AssistantID}
}

func TestPushGradingEffectiveEvaluator(t *testing.T) {
	now := time.Date(2026, 1, 2, 10, 0, 0, 0, time.UTC)
	lastAlert := map[string]time.Time{"recent": now.Add(-time.Hour)}

	t.Run("disabled grading always pushes", func(t *testing.T) {
		cfg := gradingEffectiveConfig(t, `{"version":1,"enabled":false}`)
		got := evaluatePushGradingEffective(now, cfg, gradingEffectivePolicy(assistantwire.LevelP2, "recent"), lastAlert)
		if !got.Allow || got.Reason != "" || got.Level != assistantwire.LevelP2 {
			t.Fatalf("disabled outcome = %+v", got)
		}
	})

	t.Run("default actions and neutral outcomes", func(t *testing.T) {
		cfg := gradingEffectiveConfig(t, `{"version":1,"enabled":true}`)
		cases := []struct {
			name   string
			level  string
			key    string
			reason string
			allow  bool
		}{
			{name: "P0 pushes", level: assistantwire.LevelP0, key: "recent", allow: true},
			{name: "P1 repeats are deduped", level: assistantwire.LevelP1, key: "recent", reason: pushGradingOutcomeDeduped},
			{name: "P2 repeats are deduped", level: assistantwire.LevelP2, key: "recent", reason: pushGradingOutcomeDeduped},
			{name: "P1 is held", level: assistantwire.LevelP1, key: "fresh", reason: pushGradingOutcomeHeld},
			{name: "P2 is silenced", level: assistantwire.LevelP2, key: "fresh", reason: pushGradingOutcomeSilenced},
			{name: "empty key cannot dedup", level: assistantwire.LevelP1, key: " ", reason: pushGradingOutcomeHeld},
		}
		for _, tc := range cases {
			t.Run(tc.name, func(t *testing.T) {
				got := evaluatePushGradingEffective(now, cfg, gradingEffectivePolicy(tc.level, tc.key), lastAlert)
				if got.Allow != tc.allow || got.Reason != tc.reason || got.Level != tc.level {
					t.Fatalf("outcome = %+v, want allow=%v reason=%q level=%q", got, tc.allow, tc.reason, tc.level)
				}
			})
		}
	})

	t.Run("dedup window expires", func(t *testing.T) {
		cfg := gradingEffectiveConfig(t, `{"version":1,"enabled":true,"dedup_window":"1h"}`)
		got := evaluatePushGradingEffective(now, cfg, gradingEffectivePolicy(assistantwire.LevelP1, "recent"), map[string]time.Time{
			"recent": now.Add(-2 * time.Hour),
		})
		if got.Allow || got.Reason != pushGradingOutcomeHeld {
			t.Fatalf("expired window outcome = %+v", got)
		}
	})

	t.Run("configured exemptions bypass dedup", func(t *testing.T) {
		cfg := gradingEffectiveConfig(t, `{"version":1,"enabled":true,"dedup_exempt_levels":["P0","P1"]}`)
		got := evaluatePushGradingEffective(now, cfg, gradingEffectivePolicy(assistantwire.LevelP1, "recent"), lastAlert)
		if got.Allow || got.Reason != pushGradingOutcomeHeld {
			t.Fatalf("exempt outcome = %+v", got)
		}
	})

	t.Run("configured actions choose their neutral outcomes", func(t *testing.T) {
		cfg := gradingEffectiveConfig(t, `{"version":1,"enabled":true,"actions":{"P1":"push","P2":"hold"}}`)
		pushed := evaluatePushGradingEffective(now, cfg, gradingEffectivePolicy(assistantwire.LevelP1, "recent"), lastAlert)
		if !pushed.Allow || pushed.Reason != "" {
			t.Fatalf("P1 push outcome = %+v", pushed)
		}
		held := evaluatePushGradingEffective(now, cfg, gradingEffectivePolicy(assistantwire.LevelP2, "fresh"), nil)
		if held.Allow || held.Reason != pushGradingOutcomeHeld || held.Level != assistantwire.LevelP2 {
			t.Fatalf("P2 hold outcome = %+v", held)
		}
		repeatHeld := evaluatePushGradingEffective(now, cfg, gradingEffectivePolicy(assistantwire.LevelP2, "recent"), lastAlert)
		if repeatHeld.Allow || repeatHeld.Reason != pushGradingOutcomeDeduped || repeatHeld.Level != assistantwire.LevelP2 {
			t.Fatalf("configured hold repeat outcome = %+v", repeatHeld)
		}

		silenced := gradingEffectiveConfig(t, `{"version":1,"enabled":true,"actions":{"P1":"silence"}}`)
		repeatSilenced := evaluatePushGradingEffective(now, silenced, gradingEffectivePolicy(assistantwire.LevelP1, "recent"), lastAlert)
		if repeatSilenced.Allow || repeatSilenced.Reason != pushGradingOutcomeDeduped || repeatSilenced.Level != assistantwire.LevelP1 {
			t.Fatalf("configured silence repeat outcome = %+v", repeatSilenced)
		}
	})

	t.Run("system alerts keep pushing", func(t *testing.T) {
		cfg := gradingEffectiveConfig(t, `{"version":1,"enabled":true,"actions":{"P0":"hold"}}`)
		policy := gradingEffectivePolicy(assistantwire.LevelP2, "")
		policy.system = true
		got := evaluatePushGradingEffective(now, cfg, policy, nil)
		if !got.Allow || got.Level != assistantwire.LevelP0 || got.Reason != "" {
			t.Fatalf("system outcome = %+v", got)
		}
	})

	t.Run("finished turn without a level is P2", func(t *testing.T) {
		cfg := gradingEffectiveConfig(t, `{"version":1,"enabled":true}`)
		policy := pushPolicy{kind: assistantwire.NotifyDone, done: true, manager: assistantwire.AssistantID}
		got := evaluatePushGradingEffective(now, cfg, policy, nil)
		if got.Allow || got.Level != assistantwire.LevelP2 || got.Reason != pushGradingOutcomeSilenced {
			t.Fatalf("done outcome = %+v", got)
		}
	})
}

func TestPushGradingEffectiveMatchesLegacyDefaults(t *testing.T) {
	now := time.Date(2026, 1, 2, 10, 0, 0, 0, time.UTC)
	lastAlert := map[string]time.Time{
		"repeat":  now.Add(-time.Hour),
		"expired": now.Add(-13 * time.Hour),
	}
	attention := assistantwire.Attention{Mode: assistantwire.AttentionNormal}
	cfg := gradingEffectiveConfig(t, `{"version":1,"enabled":true}`)

	cases := []struct {
		name       string
		policy     pushPolicy
		wantAllow  bool
		wantLevel  string
		wantReason string
		wantLegacy string
	}{
		{name: "P0 fresh", policy: gradingEffectivePolicy(assistantwire.LevelP0, ""), wantAllow: true, wantLevel: assistantwire.LevelP0},
		{name: "P1 held", policy: gradingEffectivePolicy(assistantwire.LevelP1, ""), wantAllow: false, wantLevel: assistantwire.LevelP1, wantReason: pushGradingOutcomeHeld, wantLegacy: interceptReasonP1},
		{name: "P2 silenced", policy: gradingEffectivePolicy(assistantwire.LevelP2, ""), wantAllow: false, wantLevel: assistantwire.LevelP2, wantReason: pushGradingOutcomeSilenced, wantLegacy: interceptReasonP2},
		{name: "P0 repeats", policy: gradingEffectivePolicy(assistantwire.LevelP0, "repeat"), wantAllow: true, wantLevel: assistantwire.LevelP0},
		{name: "P1 dedup", policy: gradingEffectivePolicy(assistantwire.LevelP1, "repeat"), wantAllow: false, wantLevel: assistantwire.LevelP1, wantReason: pushGradingOutcomeDeduped, wantLegacy: interceptReasonDedup},
		{name: "P2 dedup", policy: gradingEffectivePolicy(assistantwire.LevelP2, "repeat"), wantAllow: false, wantLevel: assistantwire.LevelP2, wantReason: pushGradingOutcomeDeduped, wantLegacy: interceptReasonDedup},
		{name: "P1 dedup expires", policy: gradingEffectivePolicy(assistantwire.LevelP1, "expired"), wantAllow: false, wantLevel: assistantwire.LevelP1, wantReason: pushGradingOutcomeHeld, wantLegacy: interceptReasonP1},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			got := evaluatePushGradingEffective(now, cfg, tc.policy, lastAlert)
			eval := evaluatePushGradingDecision(now, cfg, cfg, attention, lastAlert, tc.policy)
			if got.Allow != tc.wantAllow || got.Level != tc.wantLevel || got.Reason != tc.wantReason {
				t.Fatalf("evaluator = %+v, want allow=%v level=%q reason=%q", got, tc.wantAllow, tc.wantLevel, tc.wantReason)
			}
			if eval.Allow != tc.wantAllow || eval.EffectiveLevel != tc.wantLevel || eval.Reason != tc.wantReason || eval.LiveReason != tc.wantLegacy {
				t.Fatalf("decision = %+v, want allow=%v level=%q reason=%q live=%q", eval, tc.wantAllow, tc.wantLevel, tc.wantReason, tc.wantLegacy)
			}
		})
	}

	t.Run("disabled configuration matches grading off", func(t *testing.T) {
		disabled := gradingEffectiveConfig(t, `{"version":1,"enabled":false}`)
		for _, level := range []string{assistantwire.LevelP0, assistantwire.LevelP1, assistantwire.LevelP2} {
			policy := gradingEffectivePolicy(level, "repeat")
			got := evaluatePushGradingEffective(now, disabled, policy, lastAlert)
			eval := evaluatePushGradingDecision(now, disabled, disabled, attention, lastAlert, policy)
			if !got.Allow || !eval.Allow || got.Level != level || eval.EffectiveLevel != level || eval.Reason != "" || eval.LiveReason != "" {
				t.Fatalf("disabled mismatch for %s: eval=%+v decision=%+v", level, got, eval)
			}
		}
	})

	t.Run("extension notify_user promotion cannot weaken active owner attention mode", func(t *testing.T) {
		baseCfg := resolvePushGrading(nil, nil, "")
		layer := parseGradingTestConfig(t, `{"version":1,"notify_user":{"others":"P0"},"actions":{"P1":"push","P2":"push"}}`, true)
		effCfg := resolvePushGrading(nil, &layer, "extension:ext-a")
		immersive := assistantwire.Attention{
			Mode:  assistantwire.AttentionImmersive,
			Until: now.Add(time.Hour),
			Focus: &assistantwire.AttentionFocus{Track: "focus-track"},
		}
		policy := pushPolicy{
			kind:         assistantwire.NotifyPush,
			level:        assistantwire.LevelP1,
			derivedLevel: true,
			asked:        true,
			manager:      assistantwire.AssistantID,
			track:        "focus-track",
		}
		eval := evaluatePushGradingDecision(now, effCfg, baseCfg, immersive, nil, policy)
		if eval.Allow || eval.Reason != assistantwire.AttentionReasonImmersiveHeld || eval.LiveReason != assistantwire.AttentionReasonImmersiveHeld {
			t.Fatalf("extension weakened immersive mode: %+v", eval)
		}
	})

	t.Run("notify_user main promotion and others_max cap", func(t *testing.T) {
		layer := parseGradingTestConfig(t, `{"version":1,"notify_user":{"main":"P0","others":"P2","others_max":"P1"}}`, true)
		effCfg := resolvePushGrading(nil, &layer, "extension:ext-a")
		mainPolicy := pushPolicy{kind: assistantwire.NotifyPush, level: assistantwire.LevelP1, derivedLevel: true, asked: true, manager: assistantwire.AssistantID, mainConv: true}
		if eval := evaluatePushGradingDecision(now, effCfg, effCfg, attention, nil, mainPolicy); !eval.Allow || eval.EffectiveLevel != assistantwire.LevelP0 {
			t.Fatalf("main promotion = %+v, want allow P0", eval)
		}
		nonMainExplicitP0 := pushPolicy{kind: assistantwire.NotifyPush, level: assistantwire.LevelP0, asked: true, manager: assistantwire.AssistantID}
		if eval := evaluatePushGradingDecision(now, effCfg, effCfg, attention, nil, nonMainExplicitP0); eval.Allow || eval.EffectiveLevel != assistantwire.LevelP1 || eval.Reason != pushGradingOutcomeHeld {
			t.Fatalf("non-main others_max cap = %+v, want held P1", eval)
		}
		nonMainDefault := pushPolicy{kind: assistantwire.NotifyPush, level: assistantwire.LevelP1, derivedLevel: true, asked: true, manager: assistantwire.AssistantID}
		if eval := evaluatePushGradingDecision(now, effCfg, effCfg, attention, nil, nonMainDefault); eval.Allow || eval.EffectiveLevel != assistantwire.LevelP2 || eval.Reason != pushGradingOutcomeSilenced {
			t.Fatalf("non-main others default = %+v, want silenced P2", eval)
		}
	})

	t.Run("P1-3 disabled base or layer notify_user overrides do not run during attention mode", func(t *testing.T) {
		baseOff := parseGradingTestConfig(t, `{"version":1,"enabled":false,"notify_user":{"main":"P2","others_max":"P2"}}`, false)
		layer := parseGradingTestConfig(t, `{"version":1,"notify_user":{"main":"P2","others_max":"P2"}}`, true)
		effCfg := resolvePushGrading(&baseOff, &layer, "extension:ext-a")
		baseCfg := resolvePushGrading(&baseOff, nil, "")
		if effCfg.Enabled || baseCfg.Enabled {
			t.Fatalf("expected disabled effective config: eff=%+v base=%+v", effCfg, baseCfg)
		}
		fragment := assistantwire.Attention{Mode: assistantwire.AttentionFragment, Until: now.Add(time.Hour)}
		mainPolicy := pushPolicy{kind: assistantwire.NotifyPush, level: assistantwire.LevelP1, derivedLevel: true, asked: true, mainConv: true, manager: assistantwire.AssistantID}
		eval := evaluatePushGradingDecision(now, effCfg, baseCfg, fragment, nil, mainPolicy)
		if !eval.Allow || eval.EffectiveLevel != assistantwire.LevelP0 {
			t.Fatalf("disabled layer muted main conversation during fragment mode: %+v", eval)
		}
		explicitP0 := pushPolicy{kind: assistantwire.NotifyPush, level: assistantwire.LevelP0, asked: true, manager: assistantwire.AssistantID}
		eval2 := evaluatePushGradingDecision(now, effCfg, baseCfg, fragment, nil, explicitP0)
		if !eval2.Allow || eval2.EffectiveLevel != assistantwire.LevelP0 {
			t.Fatalf("disabled layer capped explicit P0 during fragment mode: %+v", eval2)
		}
	})
}

func TestPushGradingLegacyReasonAdapter(t *testing.T) {
	cases := map[string]string{
		"":                           "",
		pushGradingOutcomeHeld:       interceptReasonP1,
		pushGradingOutcomeSilenced:   interceptReasonP2,
		pushGradingOutcomeDeduped:    interceptReasonDedup,
		"unexpected_internal_reason": interceptReasonP1,
	}
	for neutral, legacy := range cases {
		if got := legacyPushGradingReason(neutral); got != legacy {
			t.Errorf("legacyPushGradingReason(%q) = %q, want %q", neutral, got, legacy)
		}
	}
}
