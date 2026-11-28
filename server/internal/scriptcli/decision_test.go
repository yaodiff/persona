package scriptcli

import (
	"bytes"
	"context"
	"encoding/json"
	"strings"
	"testing"

	"github.com/nwverse/persona/server/internal/assistantwire"
)

func decisionResult(f *fake, stdin string, args ...string) result {
	var out, errOut bytes.Buffer
	code := f.runner(false).Decision(context.Background(), args, strings.NewReader(stdin), &out, &errOut)
	return result{code, out.String(), errOut.String()}
}

func decisionReplyJSON(t *testing.T, state string) string {
	t.Helper()
	raw, err := json.Marshal(assistantwire.OutsideDecisionReply{Decision: &assistantwire.OutsideDecision{
		ID: "odc_example", AskTodoID: "todo_od_example", State: state, Title: "Publish?",
	}})
	if err != nil {
		t.Fatal(err)
	}
	return string(raw)
}

func TestDecisionRaiseReadsDocumentAndReturnsImmediately(t *testing.T) {
	f := &fake{reply: decisionReplyJSON(t, assistantwire.OutsideDecisionPending)}
	document := `{"ref":"nightly-1","title":"Publish?","background":"Checks passed","why_owner":"priority",` +
		`"options":[{"label":"Hold","leads_to":"Nothing goes out"},{"label":"Publish","leads_to":"Users receive it"}],` +
		`"recommend":"Hold","cost_of_waiting":"The next build waits","reversible":true,"allow_default":false}`
	got := decisionResult(f, document, "raise", "--file", "-")
	if got.code != ExitOK || strings.TrimSpace(got.out) != decisionReplyJSON(t, "pending") || got.errOut != "" {
		t.Fatalf("%+v", got)
	}
	req := f.reqs[0]
	if req["kind"] != assistantwire.MethodDecisionRaise || req["pane"] != "w1:p2" {
		t.Fatalf("request: %v", req)
	}
	in := input(t, f)
	decision, _ := in["decision"].(map[string]any)
	if in["cwd"] != "/work/app" || decision["ref"] != "nightly-1" || decision["allow_default"] != false {
		t.Fatalf("raise input: %v", in)
	}
}

func TestDecisionGetAndWithdraw(t *testing.T) {
	for _, action := range []string{"get", "withdraw"} {
		t.Run(action, func(t *testing.T) {
			state := assistantwire.OutsideDecisionPending
			if action == "withdraw" {
				state = assistantwire.OutsideDecisionWithdrawn
			}
			f := &fake{reply: decisionReplyJSON(t, state)}
			got := decisionResult(f, "", action, "odc_example")
			if got.code != ExitOK || len(f.reqs) != 1 || f.reqs[0]["kind"] != "decision."+action || f.reqs[0]["pane"] != "w1:p2" {
				t.Fatalf("%+v, request %v", got, f.reqs)
			}
			if in := input(t, f); len(in) != 1 || in["id"] != "odc_example" {
				t.Fatalf("input: %v", in)
			}
			var reply assistantwire.OutsideDecisionReply
			if json.Unmarshal([]byte(got.out), &reply) != nil || reply.Decision == nil || reply.Decision.State != state {
				t.Fatalf("output: %s", got.out)
			}
		})
	}
}

func TestDecisionExitCodesAndUsage(t *testing.T) {
	for _, tc := range []struct {
		name, reply string
		code        int
	}{
		{"invalid", `{"code":"invalid_decision","error":"options are required"}`, ExitUsage},
		{"not found", `{"code":"decision_not_found","error":"decision not found"}`, ExitDenied},
		{"parent not found", `{"code":"todo_not_found","error":"to-do not found"}`, ExitDenied},
		{"closed", `{"code":"decision_closed","error":"already answered"}`, ExitDenied},
		{"forbidden", `{"code":"forbidden","error":"this folder is blocked in Persona"}`, ExitDenied},
		{"unavailable", `{"code":"unavailable","error":"manager offline"}`, ExitUnreachable},
		{"old node", `{"error":"unknown kind"}`, ExitUnreachable},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := decisionResult(&fake{reply: tc.reply}, "", "get", "odc_example"); got.code != tc.code || got.errOut == "" {
				t.Fatalf("%+v, want exit %d", got, tc.code)
			}
		})
	}
	for name, args := range map[string][]string{
		"unknown action": {"wait", "odc_example"},
		"raise no file":  {"raise"},
		"get no id":      {"get"},
		"get two ids":    {"get", "one", "two"},
	} {
		t.Run(name, func(t *testing.T) {
			if got := decisionResult(&fake{}, "", args...); got.code != ExitUsage {
				t.Fatalf("%+v", got)
			}
		})
	}
	if got := decisionResult(&fake{}, "{", "raise", "--file", "-"); got.code != ExitUsage {
		t.Fatalf("malformed JSON: %+v", got)
	}
}
