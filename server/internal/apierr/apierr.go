// Package apierr writes the JSON bodies of the hub's HTTP API (docs/protocol/protocol.md "Error body"): a result, or an error with a
// stable code. It is shared by the hub and the services whose requests the hub relays (internal/assistant), so an
// error reads the same whichever of them answered.
package apierr

import (
	"cmp"
	"context"
	"encoding/json"
	"errors"
	"io"
	"net"
	"net/http"
	"strings"

	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

// ErrOffline: the machine a call was for has no connection to the hub.
var ErrOffline = errors.New("machine is offline")

// WriteJSON writes v as the response.
func WriteJSON(w http.ResponseWriter, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	_ = json.NewEncoder(w).Encode(v)
}

// Write writes err with the code its status stands for.
func Write(w http.ResponseWriter, status int, err error) {
	WriteCode(w, status, "", err)
}

// WriteCode preserves error for old clients and adds a stable machine-readable code.
// An i18n.Message reads in the request's language (docs/protocol/i18n.md "Protocol").
func WriteCode(w http.ResponseWriter, status int, code string, err error) {
	WriteBody(w, status, CodeBody(w, status, code, err))
}

// CodeBody is the body WriteCode writes, for a caller that adds to it (a to-do's write says delivered on every
// refusal).
func CodeBody(w http.ResponseWriter, status int, code string, err error) map[string]any {
	l := i18n.OfWriter(w)
	body := LocalizedBody(status, code, err, l)
	if l != "" {
		// Only exact standard status text is ours to translate; arbitrary server/node details remain untouched.
		if key, ok := standardStatusKeys[status]; ok && err.Error() == http.StatusText(status) {
			body["error"] = i18n.T(l, key)
		}
	}
	return body
}

var standardStatusKeys = map[int]string{
	http.StatusBadRequest:            "http-status.bad_request",
	http.StatusUnauthorized:          "http-status.unauthorized",
	http.StatusForbidden:             "http-status.forbidden",
	http.StatusNotFound:              "http-status.not_found",
	http.StatusMethodNotAllowed:      "http-status.method_not_allowed",
	http.StatusRequestTimeout:        "http-status.timeout",
	http.StatusConflict:              "http-status.conflict",
	http.StatusGone:                  "http-status.gone",
	http.StatusRequestEntityTooLarge: "http-status.too_large",
	http.StatusTooManyRequests:       "http-status.rate_limited",
	http.StatusInternalServerError:   "http-status.internal",
	http.StatusBadGateway:            "http-status.bad_gateway",
	http.StatusServiceUnavailable:    "http-status.unavailable",
	http.StatusGatewayTimeout:        "http-status.timeout",
}

// WriteBody writes body, an error, with status, and notes it on w (NoteError).
func WriteBody(w http.ResponseWriter, status int, body map[string]any) {
	NoteError(w, status, body)
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(body)
}

// ErrorNoter is a ResponseWriter that takes note of the error bodies written to it with WriteBody: the hub's request
// log says a server error's code and words from them (#14490). A body passed on as bytes, such as another service's
// answer through a reverse proxy, is never noted.
type ErrorNoter interface {
	NoteError(status int, body map[string]any)
}

// NoteError tells the first writer in w's Unwrap chain that takes note of errors (ErrorNoter) that body goes out with
// status. For a body the caller built itself and writes as bytes; WriteBody notes its own.
func NoteError(w http.ResponseWriter, status int, body map[string]any) {
	for range 16 {
		if n, ok := w.(ErrorNoter); ok {
			n.NoteError(status, body)
			return
		}
		u, ok := w.(interface{ Unwrap() http.ResponseWriter })
		if !ok {
			return
		}
		w = u.Unwrap()
	}
}

func Body(status int, code string, err error) map[string]any {
	if code == "" {
		code = StatusCode(status)
		if status >= 500 && code != "timeout" {
			code = NodeCode(err)
		}
	}
	body := map[string]any{"error": err.Error(), "code": code}
	switch code {
	case "search_changed":
		body["retryable"] = true
	case "conversation_deleted", "conversation_protected", "conversation_busy":
		body["retryable"], body["delivered"] = false, "none"
	case "invalid_token", "forbidden", "bad_request", "not_found", "method_not_allowed", "gone":
		body["retryable"] = false
	case "input_draft", "dialog_open", "prompt_changed":
		body["retryable"], body["delivered"] = false, "none"
	case proto.CodeCodexNotLoggedIn: // refused before anything was typed; only a sign-in on that computer clears it
		body["retryable"], body["delivered"] = false, "none"
	}
	if status == http.StatusUnauthorized || status == http.StatusForbidden {
		body["delivered"] = "none"
	}
	if errors.Is(err, ErrOffline) {
		body["retryable"], body["delivered"] = true, "none"
	}
	return body
}

func StatusCode(status int) string {
	switch status {
	case http.StatusBadRequest:
		return "bad_request"
	case http.StatusUnauthorized:
		return "invalid_token"
	case http.StatusForbidden:
		return "forbidden"
	case http.StatusNotFound:
		return "not_found"
	case http.StatusMethodNotAllowed:
		return "method_not_allowed"
	case http.StatusRequestTimeout, http.StatusGatewayTimeout:
		return "timeout"
	case http.StatusConflict:
		return "conflict"
	case http.StatusGone:
		return "gone"
	case http.StatusRequestEntityTooLarge:
		return "too_large"
	case http.StatusTooManyRequests:
		return "rate_limited"
	default:
		return "internal"
	}
}

func NodeCode(err error) string {
	var ne net.Error
	var re *rpc.Error
	switch {
	case errors.Is(err, context.DeadlineExceeded), errors.As(err, &ne) && ne.Timeout():
		return "timeout"
	case errors.Is(err, ErrOffline), errors.Is(err, rpc.ErrClosed), errors.Is(err, io.EOF), errors.Is(err, io.ErrUnexpectedEOF), errors.As(err, &ne):
		return "node_unreachable"
	case errors.As(err, &re):
		// RPC loses the Go error type; recognize only the exact standard deadline message.
		if re.Message == context.DeadlineExceeded.Error() {
			return "timeout"
		}
	}
	return "internal"
}

// Call writes the error of a call that went to a machine's node (or further): offline is 503, a session or file the
// node says is not there (not_found) 404, any other refusal the node coded 409 with its details, anything else 502.
func Call(w http.ResponseWriter, err error) {
	status := http.StatusBadGateway
	if errors.Is(err, ErrOffline) {
		status = http.StatusServiceUnavailable
	}
	var re *rpc.Error
	if errors.As(err, &re) && re.Code != "" {
		switch re.Code {
		case "conversation_deleted":
			status = http.StatusGone
		case proto.CodeNotFound: // no such session or file there: the client asked for something that is not (#9551)
			status = http.StatusNotFound
		case "conversation_protected":
			status = http.StatusBadRequest
		case "internal", "node_unreachable", "timeout", "delivery_unknown":
			status = http.StatusBadGateway
		case proto.CodeTerminalNotReady: // the node's new tab would not take the start command; nothing ran
			status = http.StatusServiceUnavailable
		default:
			status = http.StatusConflict
		}
	}
	CallStatus(w, status, err)
}

// CallStatus writes err, a node call's error, as Call does but with status: for a route whose clients already read
// Call's status as something else (a to-do's accept, where every app takes a 409 for "already handled", #7343).
func CallStatus(w http.ResponseWriter, status int, err error) {
	WriteBody(w, status, CallStatusBody(w, status, err))
}

// CallStatusBody is the body CallStatus writes, for a caller that adds to it (a to-do's answer says delivered on
// every refusal).
func CallStatusBody(w http.ResponseWriter, status int, err error) map[string]any {
	code := NodeCode(err)
	var re *rpc.Error
	if errors.As(err, &re) && re.Code != "" {
		code = re.Code
	}
	body := Body(status, code, err)
	// delivered stays what the node said (sendsafe.go): the iOS app treats "unknown" as "maybe typed" and stops
	// retrying by itself, so a bare timeout must not claim it. Newer clients read code (timeout, node_unreachable).
	if re != nil {
		// Keep refusal details flat for existing clients, including draft and clearable.
		var data map[string]any
		if json.Unmarshal(re.Data, &data) == nil {
			for key, value := range data {
				if key == "retryable" {
					if _, ok := value.(bool); !ok {
						continue
					}
				}
				if key == "delivered" && value != "none" && value != "unknown" && value != "yes" {
					continue
				}
				body[key] = value
			}
		}
		if re.Code != "" || len(re.Data) > 0 {
			body["error"] = re.Message
		}
		body["code"] = code
		if hubWords(code) {
			explain(body, code, re.Message, i18n.OfWriter(w))
		}
	}
	if re != nil && code == "assistant_control_failed" {
		// The SDK's reason must stay visible in clients that show only the summary.
		body["error"] = i18n.T(i18n.OfWriter(w), "apierr-apierr.assistant_control_failed", i18n.Args{"reason": re.Message})
		if _, hasDetail := body["detail"]; !hasDetail {
			body["detail"] = re.Message
		}
	} else if l := i18n.OfWriter(w); re != nil && code == proto.CodeNotFound && l != "" {
		// What the node says is not there (#9551): a summary in the request's language, its words (which session,
		// which file) the detail. Only here: the hub's own 404s keep the words they wrote.
		if _, hasDetail := body["detail"]; !hasDetail {
			body["detail"] = re.Message
		}
		key := "http-status.not_found"
		if reason, _ := body["reason"].(string); notFoundKeys[reason] != "" {
			key = notFoundKeys[reason]
		}
		body["error"] = i18n.T(l, key)
	} else if !hubWords(code) {
		localizeFailure(body, status, err, i18n.OfWriter(w))
	}
	return body
}

// LocalizedBody preserves all machine-readable outcome fields while translating a known failure summary.
func LocalizedBody(status int, code string, err error, l i18n.Lang) map[string]any {
	body := Body(status, code, err)
	localizeFailure(body, status, err, l)
	return body
}

// LocalizeErrorJSON re-translates a stored JSON error body into l using its machine-readable code/reason/partial
// fields without modifying any other fields (used when replaying stored client_ops receipts under another language).
func LocalizeErrorJSON(l i18n.Lang, body []byte) []byte {
	if l == "" || len(body) == 0 {
		return body
	}
	var m map[string]any
	if json.Unmarshal(body, &m) != nil || m == nil {
		return body
	}
	code, _ := m["code"].(string)
	if code == "" {
		return body
	}
	var key string
	var args i18n.Args
	if m["partial"] == true {
		switch code {
		case "not_delivered":
			if reason, _ := m["reason"].(string); reason == "terminal_finished" {
				key = "rpc-errors.modes_partial_terminal_finished"
			} else {
				key = "rpc-errors.modes_partial_not_delivered"
			}
		case "dialog_open":
			key = "rpc-errors.modes_partial_dialog_open"
		case proto.CodeNodeOutdated:
			key = "rpc-errors.modes_partial_node_outdated"
		default:
			key = "rpc-errors.modes_partial_failed"
		}
	} else {
		switch code {
		case "not_delivered":
			if m["delivered"] != "unknown" {
				reason, _ := m["reason"].(string)
				key = notDeliveredKeys[reason]
			}
		case "dialog_open":
			key = "rpc-errors.dialog_open"
		case "engine_missing":
			key, args = engineMissingWords(m)
		case "delivery_unknown":
			key = "rpc-errors.in_doubt"
			if reason, _ := m["reason"].(string); reason == "guide_retry_refused" {
				dialog, _ := m["dialog"].(string)
				retryCode, _ := m["retry_code"].(string)
				if dialog != "" && retryCode != "" {
					key = "rpc-errors.guide_retry_refused"
					args = i18n.Args{"dialog": dialog, "retry_code": retryCode}
				}
			}
		}
	}
	if key == "" {
		return body
	}
	translated := i18n.T(l, key, args)
	if cur, _ := m["error"].(string); cur == translated {
		return body
	}
	m["error"] = translated
	out, err := json.Marshal(m)
	if err != nil {
		return body
	}
	return out
}

var rpcFailureKeys = map[string]string{
	"rewind_busy":                "rpc-errors.rewind_busy",
	"rewind_unsupported":         "rpc-errors.rewind_unsupported",
	"rewind_mismatch":            "rpc-errors.rewind_mismatch",
	"rewind_nothing":             "rpc-errors.rewind_nothing",
	"rewind_failed":              "rpc-errors.rewind_failed",
	proto.CodeNotLoaded:          "rpc-errors.not_loaded", // a read that would start an agent its engine let go (Paseo, #8132)
	"skill_unavailable":          "rpc-errors.skill_unavailable",
	"timeout":                    "rpc-errors.timeout",
	"node_unreachable":           "rpc-errors.unreachable",
	"internal":                   "rpc-errors.failed",
	"input_draft":                "rpc-errors.input_draft",
	proto.CodeTurnOverBackground: "rpc-errors.turn_over_background", // a Stop that found the turn over (#10828)
	"dialog_open":                "rpc-errors.dialog_open",
	// an owner's message to an Agent with older messages held for it (devices held.go, #10661)
	"held_ahead":      "rpc-errors.held_ahead",
	"held_not_kept":   "rpc-errors.held_not_kept",
	"held_full":       "rpc-errors.held_full",
	"held_start_full": "rpc-errors.held_start_full",
	// the first message to a Codex opened with no first message (node/codex_waiting.go, #4961)
	"codex_not_ready":          "rpc-errors.codex_not_ready",
	"codex_first_text":         "rpc-errors.codex_first_text",
	"codex_first_unconfirmed":  "rpc-errors.codex_first_unconfirmed",
	"codex_waiting":            "rpc-errors.codex_waiting",
	"codex_compact_busy":       "rpc-errors.codex_compact_busy", // a /compact while its app-server runs a turn (node/codex_remote_compact.go, #10188)
	"codex_writer_busy":        "rpc-errors.codex_writer_busy",
	"codex_no_box":             "rpc-errors.codex_no_box",
	"codex_no_box_here":        "rpc-errors.codex_no_box_here", // the same in a pane the user picked (#5504)
	"prompt_changed":           "rpc-errors.prompt_changed",
	"attach_busy":              "rpc-errors.attach_busy",
	proto.CodeTerminalNotReady: "rpc-errors.terminal_not_ready",
	proto.CodeCodexNotLoggedIn: "rpc-errors.codex_not_logged_in", // a start that met Codex's sign-in screen (node/codex_start.go)
	// a start that named the Antigravity companion while it is not connected (node/starthost.go, #13447)
	proto.CodeAgyCompanionOffline: "rpc-errors.agy_companion_offline",
	// a start's new tab stopped on its shell (node/shellsettle.go, #2570)
	proto.CodeShellWaiting:      "rpc-errors.shell_waiting",
	proto.CodeStartLineRejected: "rpc-errors.start_line_rejected",
	"start_tab_gone":            "rpc-errors.start_tab_gone", // the app's answer to its question (node/startcontinue.go, #10365)
	"start_not_waiting":         "rpc-errors.start_not_waiting",
	"choice_not_offered":        "rpc-errors.choice_not_offered",
	proto.CodeSecretEnvGone:     "rpc-errors.secret_env_gone", // a hand-off's values went with the start that took them
	"node_outdated":             "rpc-errors.node_outdated",
	// A private-profile switch is refused before any message reaches the CLI.
	"assistant_launcher_profile_switch":  "rpc-errors.assistant_launcher_profile_switch",
	"assistant_model_unresolved":         "rpc-errors.assistant_model_unresolved",
	"assistant_service_tier_start_only":  "rpc-errors.assistant_service_tier_start_only",
	"assistant_service_tier_unsupported": "rpc-errors.assistant_service_tier_unsupported",
	"assistant_service_tier_refused":     "rpc-errors.assistant_service_tier_refused",
	"agent_speed_unsupported":            "rpc-errors.agent_speed_unsupported",
	"agent_speed_start_only":             "rpc-errors.agent_speed_start_only",
	"agent_speed_model":                  "rpc-errors.agent_speed_model",
	"session_speed_unsupported":          "rpc-errors.session_speed_unsupported",
	"session_speed_refused":              "rpc-errors.session_speed_refused",
	"session_speed_unconfirmed":          "rpc-errors.session_speed_unconfirmed",
	"agent_speed_invalid":                "rpc-errors.agent_speed_invalid",
	"readonly_unsupported":               "rpc-errors.readonly_unsupported",
	"assistant_supervision_runtime":      "rpc-errors.assistant_supervision_runtime",
	"assistant_supervision_account":      "rpc-errors.assistant_supervision_account",
	// a start whose engine runs only its own launchers and has none of the launcher's kind (#8140); the engine's own
	// words, naming the launcher, stay in detail
	proto.CodeLauncherKindMissing: "rpc-errors.launcher_kind_missing",
	"assistant_in_doubt":          "rpc-errors.in_doubt",
	"delivery_unknown":            "rpc-errors.in_doubt",
	// queued messages sent now (#6088)
	"nothing_queued":        "rpc-errors.nothing_queued",
	"send_now_unsupported":  "rpc-errors.send_now_unsupported",
	"send_now_unrecognized": "rpc-errors.send_now_unrecognized", // #2467
	"send_now_in_doubt":     "rpc-errors.in_doubt",
	// queued messages edited where no pane takes the keys: a Claude in its desktop app, a background one (#12789)
	"queue_edit_unsupported": "rpc-errors.queue_edit_unsupported",
	// a new agent's worktree (#6115)
	"not_a_repository":  "rpc-errors.not_a_repository",
	"not_our_worktree":  "rpc-errors.not_our_worktree",
	"worktree_in_use":   "rpc-errors.worktree_in_use",
	"worktree_dirty":    "rpc-errors.worktree_dirty",
	"worktree_unmerged": "rpc-errors.worktree_unmerged",
	// a preview's server with no terminal engine to run in (node/preview_launch.go)
	"preview_no_terminal": "rpc-errors.preview_no_terminal",
	// a space to rename that no engine of the computer lists any more (node/rename.go)
	"space_gone": "rpc-errors.space_gone",
	// a Claude session outside Persona's terminals, through its peer inbox (node/claudepeer.go)
	"peer_unsupported": "rpc-errors.peer_unsupported",
	"peer_unreachable": "rpc-errors.peer_unreachable",
	"peer_too_long":    "rpc-errors.peer_too_long",
	"peer_unconfirmed": "rpc-errors.peer_unconfirmed",
	"peer_held":        "rpc-errors.peer_held", // a bypass permissions session holds it for approval at the computer
	"peer_command":     "rpc-errors.peer_command",
	// a desktop app session nothing runs, which the app did not open in time (node/claudedesktop_open.go)
	"desktop_open_failed": "rpc-errors.desktop_open_failed",
	// the node menus' Git ▸ Pull / Push / Fetch and the Git pane's Pull (#1888); the *_failed ones carry git's words
	"no_directory":        "rpc-errors.no_directory",
	"detached_head":       "rpc-errors.detached_head",
	"no_upstream":         "rpc-errors.no_upstream",
	"uncommitted_changes": "rpc-errors.uncommitted_changes",
	"not_fast_forward":    "rpc-errors.not_fast_forward",
	"push_rejected":       "rpc-errors.push_rejected",
	"pull_failed":         "rpc-errors.pull_failed",
	"push_failed":         "rpc-errors.push_failed",
	"fetch_failed":        "rpc-errors.fetch_failed",
	// session.modes (node/modes.go, #1843)
	proto.CodeModesOnComputer:     "rpc-errors.modes_on_computer",
	proto.CodeModesNotRunning:     "rpc-errors.modes_not_running",
	proto.CodeModesUnsupported:    "rpc-errors.modes_unsupported",
	proto.CodeEffortChangePending: "rpc-errors.effort_change_pending",
	// session.modes on a Codex on the terminal route (node/codex_terminal_modes.go, #12572); the refusal is Codex's words
	proto.CodeModesCodexVersion:     "rpc-errors.modes_codex_version",
	proto.CodeModesCodexNoRollout:   "rpc-errors.modes_codex_no_rollout",
	proto.CodeModesCodexRefused:     "rpc-errors.modes_codex_refused",
	proto.CodeModesCodexUnconfirmed: "rpc-errors.modes_codex_unconfirmed",
	// a message to a stopped agent: read-only, its terminal page's Resume brings it back (node stopped.go, #4754)
	"agent_stopped":      "rpc-errors.agent_stopped",
	"resume_not_allowed": "rpc-errors.resume_not_allowed",
	"stop_not_saved":     "rpc-errors.stop_not_saved",
	"omp_options":        "rpc-errors.omp_options",
	// a call that would drive an agy conversation an agy in a terminal has open (node/agypresence.go, #13603)
	"agy_in_terminal": "rpc-errors.agy_in_terminal",
	// a message to an omp session the node cannot resume: no usable session file (node/ompresume.go)
	"omp_resume_unknown": "rpc-errors.omp_resume_unknown",
	// a resume Orca does not carry out from the phone: an omp session's (node/ompresume.go), and an older node's refusal of
	// every agent but Claude Code (#17044)
	"orca_resume": "rpc-errors.orca_resume",
	// a launcher resume of a pi, Cursor or agy session (node/control.go)
	"work_agent_resume": "rpc-errors.work_agent_resume",
	// a message to a session whose last pane is still open with its agent not in front (node/sendsafe.go notRunningRefusal)
	"agent_not_in_front": "rpc-errors.agent_not_in_front",
	// a resume of a conversation the computer has nothing of (node/control.go resumeKnown, #10941)
	proto.CodeResumeUnknown: "rpc-errors.resume_unknown",
	// a call a stopped omp has nothing to answer with: rename, interrupt, modes, queue, close (node/ompcalls.go)
	"omp_stopped": "rpc-errors.omp_stopped",
	// a start in a directory whose name can't be typed into a terminal (node/hostorca_launch.go)
	proto.CodeDirectoryUntypable: "rpc-errors.directory_untypable",
	// a running Agent's switch to another launcher, picked and carried out by the next message (node/launcher_switch.go)
	proto.CodeSwitchStale:           "rpc-errors.switch_stale",
	proto.CodeSwitchPickModel:       "rpc-errors.switch_pick_model",
	proto.CodeSwitchUnderWay:        "rpc-errors.switch_under_way",
	proto.CodeSwitchUnsupported:     "rpc-errors.switch_unsupported",
	proto.CodeSwitchLauncherMissing: "rpc-errors.switch_launcher_missing",
	proto.CodeSwitchOtherAgent:      "rpc-errors.switch_other_agent",
	proto.CodeSwitchModelLocked:     "rpc-errors.switch_model_locked",
	proto.CodeSwitchConfigDir:       "rpc-errors.switch_config_dir",
	proto.CodeSwitchNotStopped:      "rpc-errors.switch_not_stopped",
	proto.CodeSwitchFailed:          "rpc-errors.switch_failed",
	proto.CodeSwitchFailedStopped:   "rpc-errors.switch_failed_stopped",
	proto.CodeSwitchBusy:            "rpc-errors.switch_busy",
	// a start Orca could not place: no project open there, or the chosen workspace gone (node/hostorca_launch.go)
	"orca_not_registered":        "rpc-errors.orca_not_registered",
	"orca_workspace_unavailable": "rpc-errors.orca_workspace_unavailable",
	// a resume of a session another app's runtime owns, an Orca chat's (node/control.go)
	"runtime_session": "rpc-errors.runtime_session",
	// Orca's Codex launch settings a stock Codex start would follow (node/codex_orca_launch_defaults.go); reasonKeys words
	// each case, this one an older node's refusal without a reason
	"codex_launch_preferences": "rpc-errors.codex_launch_preferences",
	// the terminal browser's actions and a start's new tab (node/panes.go, panes_actions.go, retire.go newTab): the
	// engine's own words, data detail, in the summary
	"terminal_open_failed":    "rpc-errors.terminal_open_failed",
	"tab_close_failed":        "rpc-errors.tab_close_failed",
	"pane_close_failed":       "rpc-errors.pane_close_failed",
	"pane_split_failed":       "rpc-errors.pane_split_failed",
	"tab_name_invalid":        "rpc-errors.tab_name_invalid",
	"tab_gone":                "rpc-errors.tab_gone",
	"pane_gone":               "rpc-errors.pane_gone",
	"pane_has_agent":          "rpc-errors.pane_has_agent",
	"pane_not_at_prompt":      "rpc-errors.pane_not_at_prompt",
	"pane_directory_unknown":  "rpc-errors.pane_directory_unknown",
	"pane_foreground_unknown": "rpc-errors.pane_foreground_unknown",
	// a Close of an Agent that runs in no terminal of the computer and is not one Persona started (node/control.go)
	"close_not_in_terminal": "rpc-errors.close_not_in_terminal",
}

// reasonKeys word a refusal whose code covers several cases by its data reason (the Orca paths', #17057): a close of a
// stopped Agent whose terminal changed (agent_busy, which also stands for other refusals with words of their own), Orca's
// Codex launch settings, a session that is not running for a known reason, a capability the computer's engine lacks.
// The data's detail, engine (an id, named by proto.EngineNames), seconds, program and max fill the words (reasonArgs).
var reasonKeys = map[string]map[string]string{
	"agent_busy": {"terminal_changed": "rpc-errors.agent_busy_terminal_changed"},
	"codex_launch_preferences": {
		"unreadable": "rpc-errors.codex_launch_preferences_unreadable",
		"custom":     "rpc-errors.codex_launch_preferences_custom",
		"shell":      "rpc-errors.codex_launch_preferences_shell",
		"quoting":    "rpc-errors.codex_launch_preferences_quoting",
	},
	"not_running": {
		"directory_gone": "rpc-errors.not_running_directory_gone",
		"resume_timeout": "rpc-errors.not_running_resume_timeout",
		"agent_exited":   "rpc-errors.not_running_agent_exited",
	},
	"pane_foreground_unknown": {"unreadable": "rpc-errors.pane_foreground_unreadable"},
	"unsupported":             {"engine_unsupported": "rpc-errors.engine_unsupported"},
}

// reasonKey is the key reasonKeys words a refusal by, its code and data reason; "" for any other.
func reasonKey(code string, data map[string]any) string {
	reason, _ := data["reason"].(string)
	return reasonKeys[code][reason]
}

// reasonArgs are the arguments a refusal's data gives its words: detail (the engine's or Orca's own words), engine (its
// name), seconds, program and max.
func reasonArgs(data map[string]any) i18n.Args {
	args := i18n.Args{}
	for _, k := range []string{"detail", "program"} {
		if s, _ := data[k].(string); s != "" {
			args[k] = s
		}
	}
	if engine, _ := data["engine"].(string); engine != "" {
		args["engine"] = cmp.Or(proto.EngineNames[engine], engine)
	}
	for _, k := range []string{"seconds", "max"} {
		if n, ok := data[k].(float64); ok {
			args[k] = n
		}
	}
	return args
}

// notFoundKeys word a node's not_found by why its session's history can't be read (data.reason, node/transcript_files.go
// readErr, #11388). One without a known reason (an older node, a runtime's own answer, a missing file) keeps the plain
// summary.
var notFoundKeys = map[string]string{
	proto.NotFoundNoTranscript:   "apierr-apierr.session_no_transcript",
	proto.NotFoundUnknownSession: "apierr-apierr.session_unknown",
}

// bridgeReasonKeys word the refusals of the node's Orca chat bridge by their reason (node/orcabridge.go): Orca is not
// running, turned the request down, may or may not have done it, or no longer has the chat; a chat it closed takes no
// mode change; a permission mode the chat's provider does not take; an Orca that cannot rename chats; a chat Orca can't
// open for that provider on the computer, its chat runtime not installed there or for a reason in Orca's words, naming
// the provider (data program, node/orcabridge_start.go orcaStartErr, #17684).
var bridgeReasonKeys = map[string]string{
	"orca_away":                 "rpc-errors.orca_away",
	"orca_refused":              "rpc-errors.orca_refused",
	"orca_unknown":              "rpc-errors.orca_unknown",
	"orca_session_gone":         "rpc-errors.orca_session_gone",
	"orca_closed":               "rpc-errors.orca_closed",
	"orca_mode_unsupported":     "rpc-errors.orca_mode_unsupported",
	"orca_rename_unsupported":   "rpc-errors.orca_rename_unsupported",
	"orca_start_unsupported":    "rpc-errors.orca_start_unsupported",
	"orca_start_folder":         "rpc-errors.orca_start_folder",
	"orca_start_launcher":       "rpc-errors.orca_start_launcher",
	"orca_chat_runtime_missing": "rpc-errors.orca_chat_runtime_missing",
	"orca_chat_unavailable":     "rpc-errors.orca_chat_unavailable",
	"orca_unsupported":          "rpc-errors.orca_unsupported", // a call the bridge does not take (#17057)
}

// openElsewhereKeys word the node's open_elsewhere refusal by where the session is open (node/openelsewhere.go
// whereOpen). One without a known where (an older node, an Agent runtime's own refusal) keeps its words.
var openElsewhereKeys = map[string]string{
	"desktop":     "rpc-errors.open_elsewhere_desktop",
	"terminal":    "rpc-errors.open_elsewhere_terminal",
	"program":     "rpc-errors.open_elsewhere_program",
	"other":       "rpc-errors.open_elsewhere_other",
	"agents_list": "rpc-errors.open_elsewhere_agents_list",
}

// engineMissingKeys word the node's engine_missing refusal by why its engine could not take the start (data reason,
// node/orcaengine.go orcaMissing, #17037), naming the engine (data engine). One without both (an older node, a
// refusal about the machine's whole engine set) keeps its words.
var engineMissingKeys = map[string]string{
	"not_running":   "rpc-errors.engine_not_running",
	"not_installed": "rpc-errors.engine_not_installed",
	"incompatible":  "rpc-errors.engine_incompatible",
	// the start's engine did not answer (node/control.go workspaceFor), and herdr's autostart failed too; a stopped
	// session's resume with no engine to open it in (node/pi.go); a start naming an engine the computer does not turn on
	// (node/starthost.go) (#17057)
	"unavailable":        "rpc-errors.engine_unavailable",
	"start_failed":       "rpc-errors.engine_start_failed",
	"resume_unavailable": "rpc-errors.engine_resume_unavailable",
	"not_enabled":        "rpc-errors.engine_not_enabled",
}

// engineNoneReason is engine_missing's reason on a computer that turns on no engine at all (node/engine.go
// noEngineRefusal): there is no engine to name.
const engineNoneReason = "none"

// engineMissingWords is the key and arguments of an engine_missing refusal whose data says which engine and why
// (engineMissingKeys), or that there is none (engineNoneReason); "" for one that does not.
func engineMissingWords(data map[string]any) (string, i18n.Args) {
	engine, _ := data["engine"].(string)
	reason, _ := data["reason"].(string)
	if reason == engineNoneReason {
		return "rpc-errors.engine_none", nil
	}
	name := proto.EngineNames[engine]
	if name == "" || engineMissingKeys[reason] == "" {
		return "", nil
	}
	return engineMissingKeys[reason], i18n.Args{"engine": name}
}

// notDeliveredKeys word the node's generic not_delivered refusal by reason (node/sendsafe.go deliveryRefusal), and a
// screen the node could not read before typing (screen_unreadable, #17057); specific refusals that carry their own
// recovery instructions (a cut dialog, a botmux probe failure) omit reason and keep their own words.
var notDeliveredKeys = map[string]string{
	"terminal_refused":  "rpc-errors.not_delivered",
	"terminal_finished": "rpc-errors.not_delivered_finished",
	"screen_unreadable": "rpc-errors.not_delivered_screen_unreadable",
	// botmux's 403 on a send as the owner (node/sendsafe.go botmuxOwnerRefusedError).
	"botmux_owner_unresolved": "rpc-errors.not_delivered_botmux_owner_unresolved",
	"botmux_owner_mismatch":   "rpc-errors.not_delivered_botmux_owner_mismatch",
	"botmux_owner_refused":    "rpc-errors.not_delivered_botmux_owner_refused",
}

func localizeFailure(body map[string]any, status int, err error, l i18n.Lang) {
	if l == "" {
		return
	}
	original, _ := body["error"].(string)
	localized := i18n.Localize(err, l)
	// A typed structured error owns its summary; do not replace it with a generic transport message.
	if _, ok := err.(interface{ Text(i18n.Lang) string }); ok || localized != err.Error() {
		body["error"] = localized
		return
	}
	code, _ := body["code"].(string)
	key := rpcFailureKeys[code]
	args := i18n.Args{"reason": original}
	for k, v := range reasonArgs(body) { // what a refusal's words name: the engine's words, the engine, a program (#17057)
		args[k] = v
	}
	if code == "assistant_model_unresolved" {
		model, _ := body["model"].(string)
		launcher, _ := body["launcher"].(string)
		if model != "" && launcher != "" {
			args["model"], args["launcher"] = model, launcher
		} else {
			key = ""
		}
	}
	if code == "assistant_supervision_runtime" {
		var names []string
		if values, ok := body["launchers"].([]any); ok {
			for _, v := range values {
				if name, ok := v.(string); ok && name != "" {
					names = append(names, name)
				}
			}
		}
		label := strings.Join(names, ", ")
		if label == "" {
			label = i18n.T(l, "rpc-errors.assistant_supervision_none")
		}
		args["launchers"] = label
	}
	if code == "codex_no_box" || code == "codex_no_box_here" {
		var re *rpc.Error
		var detail struct {
			Seconds float64 `json:"seconds"`
		}
		if errors.As(err, &re) && json.Unmarshal(re.Data, &detail) == nil && detail.Seconds > 0 {
			args["seconds"] = detail.Seconds
			if code == "codex_no_box_here" {
				key = "node-codex-start.no_box_here"
			} else {
				key = "node-codex-start.no_box"
			}
		}
	}
	if code == "open_elsewhere" {
		where, _ := body["where"].(string)
		key = openElsewhereKeys[where]
	}
	if code == "engine_missing" {
		if k, a := engineMissingWords(body); k != "" {
			key, args["engine"] = k, a["engine"]
		}
	}
	if code == "not_running" {
		// Only the node's refusal that names the engine (node/sendsafe.go notRunningRefusal, #17044): not_running's other
		// words (a runtime's, a resume that never came up) stay as the node wrote them.
		reason, _ := body["reason"].(string)
		engine, _ := body["engine"].(string)
		if reason == proto.NotRunningEngine && engine != "" {
			key, args["engine"] = "rpc-errors.not_running_engine", engine
		}
	}
	if k := reasonKey(code, body); k != "" {
		key = k
	}
	if body["partial"] == true {
		switch code {
		case "not_delivered":
			if reason, _ := body["reason"].(string); reason == "terminal_finished" {
				key = "rpc-errors.modes_partial_terminal_finished"
			} else {
				key = "rpc-errors.modes_partial_not_delivered"
			}
		case "dialog_open":
			key = "rpc-errors.modes_partial_dialog_open"
		case proto.CodeNodeOutdated:
			key = "rpc-errors.modes_partial_node_outdated"
		default:
			key = "rpc-errors.modes_partial_failed"
		}
	} else if code == "not_delivered" && body["delivered"] != "unknown" {
		reason, _ := body["reason"].(string)
		key = notDeliveredKeys[reason]
	} else if code == "delivery_unknown" {
		if reason, _ := body["reason"].(string); reason == "guide_retry_refused" {
			dialog, _ := body["dialog"].(string)
			retryCode, _ := body["retry_code"].(string)
			if dialog != "" && retryCode != "" {
				key = "rpc-errors.guide_retry_refused"
				args["dialog"], args["retry_code"] = dialog, retryCode
			}
		}
	}
	// A bridge's refusal under a generic code (runtime_away, refused, unknown, session_gone) words its reason; Orca's own
	// words, when it gave any, are the detail (node/orcabridge.go orcaChatErr).
	if reason, _ := body["reason"].(string); bridgeReasonKeys[reason] != "" {
		key = bridgeReasonKeys[reason]
		detail, _ := body["detail"].(string)
		args["detail"] = detail
	}
	if errors.Is(err, ErrOffline) {
		key = "rpc-errors.offline"
	}
	var syntax *json.SyntaxError
	var wrongType *json.UnmarshalTypeError
	if status == http.StatusBadRequest && (errors.As(err, &syntax) || errors.As(err, &wrongType) || errors.Is(err, io.EOF)) {
		key = "http-status.bad_request"
	}
	if key == "" {
		return
	}
	// a summary that names {reason} carries the original words (git's, for a failed pull or push)
	body["error"] = i18n.T(l, key, args)
	switch code {
	case "assistant_launcher_profile_switch", "assistant_model_unresolved", "assistant_service_tier_start_only", "assistant_service_tier_unsupported", "assistant_service_tier_refused":
		// The native diagnostic is not a person-facing instruction; the stable, translated refusal says what to do.
		return
	}
	if _, hasDetail := body["detail"]; !hasDetail && original != body["error"] {
		body["detail"] = original
	}
}

// RPCText localizes only canonical transport strings after a stream has lost the typed error.
func RPCText(l i18n.Lang, text string) string {
	if l == "" {
		return text
	}
	var key string
	switch text {
	case ErrOffline.Error():
		key = "rpc-errors.offline"
	case context.DeadlineExceeded.Error():
		key = "rpc-errors.timeout"
	case rpc.ErrClosed.Error(), io.EOF.Error(), io.ErrUnexpectedEOF.Error():
		key = "rpc-errors.unreachable"
	}
	if key != "" {
		return i18n.T(l, key)
	}
	return i18n.InboxText(l, text)
}

// codeActions are the refusals whose words the hub writes itself, wherever they came from (the assistant, a node),
// with what a client offers to fix each (docs/assistant/onboarding.md 4.6, 5.2).
var codeActions = map[string]string{
	proto.CodeNoMachine:        "add_computer",
	proto.CodeMachinePending:   "confirm_computer",
	proto.CodeClaudeMissing:    "install_claude",
	proto.CodeLauncherMissing:  "connect_model",
	proto.TurnErrorNotLoggedIn: "connect_model",
}

// wordedWithoutAction are refusals the hub also words itself, naming the machine, but with nothing for a client to
// offer: the fix is on that computer (Codex's sign-in, node/codex_start.go).
var wordedWithoutAction = map[string]bool{
	proto.CodeCodexNotLoggedIn: true,
}

// hubWords is whether the hub writes code's words itself (codeActions, wordedWithoutAction) instead of passing on
// the caller's.
func hubWords(code string) bool {
	_, ok := codeActions[code]
	return ok || wordedWithoutAction[code]
}

// OnMachine is err naming the machine (its display name) it was refused on, when it is one of the refusals the hub
// words itself and does not name one yet: what the sentence says instead of "this computer". Anything else is err.
func OnMachine(err error, name string) error {
	var re *rpc.Error
	if name == "" || !errors.As(err, &re) {
		return err
	}
	if !hubWords(re.Code) {
		return err
	}
	data := map[string]any{}
	if len(re.Data) > 0 && json.Unmarshal(re.Data, &data) != nil {
		return err
	}
	if data == nil {
		data = map[string]any{}
	}
	if n, _ := data["machine_name"].(string); n != "" {
		return err
	}
	data["machine_name"] = name
	raw, merr := json.Marshal(data)
	if merr != nil {
		return err
	}
	return &rpc.Error{Code: re.Code, Message: re.Message, Data: raw}
}

// explain makes body say code's refusal in plain words in l (none: the default language): error is the sentence for
// people, detail what the caller wrote (the shell's own words: only for the "Details"), action what to do unless the caller
// said (Action), summary a short line for a row when there is one (Summary). The caller's data may name the machine
// (machine_name, OnMachine; else the text says "this computer"), the launcher, and where it failed (Sentence).
func explain(body map[string]any, code, message string, l i18n.Lang) {
	body["error"] = Sentence(l, code, body)
	if d, _ := body["detail"].(string); d == "" {
		body["detail"] = message
	}
	if a, _ := body["action"].(string); a == "" {
		if a := Action(code, body); a != "" { // wordedWithoutAction: nothing for a client to offer
			body["action"] = a
		}
	}
	if s := Summary(l, code, body); s != "" {
		body["summary"] = s
	}
}

// ActionSwitchLauncher: start with another launcher of the same computer (a task's "Use <launcher> instead", the
// launcher picker), for a launcher the new terminal tab could not run (#10365).
const ActionSwitchLauncher = "switch_launcher"

// Action is what a client offers to fix code's refusal (codeActions), by its data: a launcher the new terminal tab
// could not run (launcher_missing, where "terminal") is the terminal's, not a model to connect: the way out is another
// launcher of that computer (switch_launcher), as Persona-managed launchers are already connected providers (#10365).
func Action(code string, data map[string]any) string {
	if where, _ := data["where"].(string); code == proto.CodeLauncherMissing && where == "terminal" {
		return ActionSwitchLauncher
	}
	return codeActions[code]
}

// Summary is code's refusal in a short line in l, for a list row whose details carry Sentence; "" when it has none:
// a launcher the new terminal tab could not run, naming it and the computer (machine_name, else "this computer").
func Summary(l i18n.Lang, code string, data map[string]any) string {
	where, _ := data["where"].(string)
	if code != proto.CodeLauncherMissing || where != "terminal" {
		return ""
	}
	machine, _ := data["machine_name"].(string)
	launcher, _ := data["launcher"].(string)
	key := "apierr-apierr.launcher_missing_terminal_short"
	if machine == "" {
		key += "_here"
	}
	return i18n.T(l, key, i18n.Args{"machine": machine, "launcher": launcher})
}

// Sentence is the plain words in l of code, one of the refusals the hub words itself ("" for any other): a start refused
// on its request with nothing to name as it is, and codeActions' by their
// data: machine_name (else "this computer"), launcher, and of a start the new tab's shell could not run (where
// "terminal", node/shellmiss.go) what it could not run (missing, kind), that shell (pane_shell) and the shell and rc
// file the launchers are listed in (launcher_shell, rc_file). What a 409 says, and a session's start_error.
func Sentence(l i18n.Lang, code string, data map[string]any) string {
	switch code {
	case "readonly_unsupported", proto.CodeDirectoryUntypable, "omp_options", proto.CodeAgyCompanionOffline: // refusal needs no request parameters
		return i18n.T(l, rpcFailureKeys[code])
	}
	// a start's new tab (node/shellsettle.go, #2570), or the Codex in it on its sign-in screen (node/codex_start.go)
	if code == proto.CodeShellWaiting || code == proto.CodeStartLineRejected || code == proto.CodeCodexNotLoggedIn {
		machine, _ := data["machine_name"].(string)
		line, _ := data["line"].(string)
		key := "apierr-apierr." + code
		if machine == "" {
			key += "_here"
		}
		return i18n.T(l, key, i18n.Args{"machine": machine, "line": line})
	}
	if _, ok := codeActions[code]; !ok {
		return ""
	}
	arg := func(k string) string { s, _ := data[k].(string); return s }
	key := "apierr-apierr." + code
	if arg("where") == "terminal" {
		v := key + "_terminal"
		if k := arg("kind"); (k == "no_such_file" || k == "permission_denied") && i18n.Has(v+"_path") {
			v += "_path" // a path the shell could not run (a managed launcher's personad)
		}
		if i18n.Has(v) {
			key = v
		}
	}
	if arg("machine_name") == "" && i18n.Has(key+"_here") {
		key += "_here" // "this computer"
	}
	pane := arg("pane_shell")
	if pane == "" && arg("where") == "terminal" { // the node could not tell which shell the tab runs
		pane = i18n.T(l, "apierr-apierr.terminal_default_shell")
	}
	return i18n.T(l, key, i18n.Args{"machine": arg("machine_name"), "launcher": arg("launcher"), "missing": arg("missing"),
		"pane_shell": pane, "launcher_shell": arg("launcher_shell"), "rc_file": arg("rc_file")})
}

// Reason is the failure in an error body read back from another hub or service: the original text when the summary
// was localized (localizeFailure moves it to detail), so a reason travels across hops instead of the generic summary.
func Reason(b []byte) string {
	var e struct {
		Error  string          `json:"error"`
		Detail json.RawMessage `json:"detail"`
	}
	if json.Unmarshal(b, &e) != nil {
		return ""
	}
	var detail string
	if json.Unmarshal(e.Detail, &detail) == nil && detail != "" {
		return detail
	}
	return e.Error
}

// Describe is an error body read back for a person with no "Details" to open (the command line): the summary and,
// when the summary was localized or explained away from it, the cause after it (#5728).
func Describe(b []byte) string {
	var e struct {
		Error  string          `json:"error"`
		Detail json.RawMessage `json:"detail"`
	}
	if json.Unmarshal(b, &e) != nil {
		return ""
	}
	var detail string
	if json.Unmarshal(e.Detail, &detail) != nil || detail == "" || detail == e.Error {
		return e.Error
	}
	if e.Error == "" {
		return detail
	}
	return e.Error + "：" + detail
}
