package node

import (
	"context"
	"errors"
	"testing"

	"github.com/nwverse/persona/server/internal/host"
	"github.com/nwverse/persona/server/internal/i18n"
)

// The refusals on the Orca paths carry a code and the data the hub words them by in the reader's language (apierr,
// #17057), and keep the words and the error matching they had: an older hub, the logs and the node's own checks see
// what they saw before.

func TestUnsupportedIsCodedForTheHub(t *testing.T) {
	err := unsupported(&bareHost{kind: host.Orca})
	if !errors.Is(err, host.ErrUnsupported) || err.Error() != "unsupported: orca cannot do this" {
		t.Fatalf("unsupported = %v", err)
	}
	code, data := refusalOf(t, err)
	if code != "unsupported" || data["reason"] != "engine_unsupported" || data["engine"] != host.Orca || data["delivered"] != nil {
		t.Fatalf("unsupported = %s %v", code, data)
	}
}

func TestHostFailureKeepsWhatTheEngineSaid(t *testing.T) {
	ctx := context.Background()
	cause := host.Mark(errors.New("orca: the selected tab is no longer available"), host.ErrNotDelivered)
	err := hostFailure(ctx, "tab_close_failed", cause)
	code, data := refusalOf(t, err)
	want := i18n.T(i18n.Default, "rpc-errors.tab_close_failed", i18n.Args{"detail": cause.Error()})
	if code != "tab_close_failed" || data["detail"] != cause.Error() || data["delivered"] != nil || err.Error() != want {
		t.Fatalf("hostFailure = %s %v %q", code, data, err)
	}
	if !errors.Is(err, host.ErrNotDelivered) {
		t.Fatalf("hostFailure lost what the engine's error matches: %v", err)
	}
	// A refusal the engine's call already coded says more: it stays as it is.
	refused := refusal("orca_not_registered", i18n.T(i18n.Default, "rpc-errors.orca_not_registered"), nil)
	if got := hostFailure(ctx, "terminal_open_failed", host.Mark(refused, host.ErrNotDelivered)); rpcCode(got) != "orca_not_registered" {
		t.Fatalf("hostFailure over a coded refusal = %v (%s)", got, rpcCode(got))
	}
	// The words are the request's language when it has one.
	en := hostFailure(i18n.With(ctx, i18n.En), "pane_split_failed", errors.New("orca: no split direction"))
	if en.Error() != "Couldn’t split the pane: orca: no split direction" {
		t.Fatalf("hostFailure in English = %q", en)
	}
}

func TestOrcaPathRefusalsCarryTheirReason(t *testing.T) {
	for _, tc := range []struct {
		err          error
		code, reason string
		words        string
	}{
		{noEngineRefusal(), engineMissing, engineNone, i18n.T(i18n.Default, "rpc-errors.engine_none")},
		{terminalChangedRefusal(), "agent_busy", "terminal_changed", i18n.T(i18n.En, "rpc-errors.agent_busy_terminal_changed")},
		{shellOnlyRefusal(), notRunning, notRunningAgentExited, i18n.T(i18n.Default, "rpc-errors.not_running_agent_exited")},
		{unreadBoxRefusal(), notDelivered, "screen_unreadable", i18n.T(i18n.Default, "rpc-errors.not_delivered_screen_unreadable")},
		{launchPreferencesRefusal("quoting"), "codex_launch_preferences", "quoting", i18n.T(i18n.En, "rpc-errors.codex_launch_preferences_quoting")},
	} {
		code, data := refusalOf(t, tc.err)
		if code != tc.code || data["reason"] != tc.reason || data["delivered"] != deliveredNone || tc.err.Error() != tc.words {
			t.Errorf("%s/%s: %s %v %q", tc.code, tc.reason, code, data, tc.err)
		}
	}
}

func TestRenameTabRefusesALabelInTheRequestsLanguage(t *testing.T) {
	n := &Node{}
	err := n.renameTab(i18n.With(context.Background(), i18n.En), "", "t1", "   ")
	code, data := refusalOf(t, err)
	if code != "tab_name_invalid" || data["max"] != float64(maxLabel) || err.Error() != "A tab name needs 1 to 80 characters." {
		t.Fatalf("renameTab = %s %v %q", code, data, err)
	}
}
