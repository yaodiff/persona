package assistant

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/prompts"
	"github.com/nwverse/persona/server/internal/proto"
	"github.com/nwverse/persona/server/internal/rpc"
)

type blockingStartHub struct {
	Hub
	entered chan struct{}
	release chan struct{}
}

func (b *blockingStartHub) DeviceCall(ctx context.Context, p DeviceCall) (json.RawMessage, error) {
	if p.Method == DeviceSessionStart {
		select {
		case b.entered <- struct{}{}:
		default:
		}
		<-b.release
	}
	return b.Hub.DeviceCall(ctx, p)
}

func todoHandoffHTTPReq(t *testing.T, s *Service, method, path, body, role string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(method, path, strings.NewReader(body))
	if role != "" {
		req.Header.Set(RoleHeader, role)
	}
	w := httptest.NewRecorder()
	s.ServeHTTP(w, req)
	return w
}

func setupHandoffFixture(t *testing.T, store Store, srcLauncher, srcAgent, srcPerm string, srcSess proto.Session) (*threadEnv, Todo, TaskBrief) {
	t.Helper()
	e := newThreadEnv(t, store)
	ctx := context.Background()
	e.f.launcherList = []proto.Launcher{
		{Name: "claude", Agent: "claude"},
		{Name: "profiles-ag", Agent: "claude"},
		{Name: "codex", Agent: "codex"},
	}
	e.f.ownModels = true

	todo, err := e.s.createTaskTodo(ctx, "Fix auth token refresh race", "Investigate and fix the refresh race.", "m1", "", "")
	if err != nil {
		t.Fatal(err)
	}
	brief, err := e.s.putTaskBrief(ctx, briefRecord{
		StartedBy:    briefByOwner,
		Conversation: "m1",
		Goal:         "Eliminate duplicate refresh calls under concurrent 401s",
		Request:      &BriefRequest{Text: "Fix the refresh race and add regression tests", At: time.Now().UTC()},
		DoneWhen:     []string{"go test -race passes", "no duplicate refresh requests"},
		OutOfBounds:  []string{"Do not change wire schema"},
	}, todo.ID)
	if err != nil {
		t.Fatal(err)
	}
	if err := e.s.linkTodoAgent(ctx, todo.ID, "box", srcSess.SessionID, todo.Title, "/code/app", srcAgent); err != nil {
		t.Fatal(err)
	}
	if st := e.s.todoDB(); st != nil {
		_, err := st.InsertTodoDecision(ctx, db.TodoDecisionRow{
			ID:        "dec_1",
			TodoID:    todo.ID,
			Question:  "How should refresh be serialized?",
			Choice:    "Single-flight per token key",
			Reason:    "Avoids lock contention across unrelated tokens",
			Source:    db.DecisionCard,
			CreatedAt: time.Now().UTC(),
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	srcSess.MachineID = "box"
	srcSess.Cwd = "/code/app"
	srcSess.Agent = srcAgent
	srcSess.Launcher = srcLauncher
	srcSess.PermissionMode = srcPerm
	if srcSess.UpdatedAt.IsZero() {
		srcSess.UpdatedAt = time.Now().UTC()
	}
	e.sessions("box", srcSess)

	e.f.nodes = map[string]nodeAnswer{
		DeviceSessionItems: func(_ string, _ json.RawMessage) (any, error) {
			return map[string]any{
				"items": []proto.Item{
					{Seq: 1, Role: "user", Kind: proto.KindText, Text: "Fix the refresh race in server/auth.go"},
					{Seq: 2, Role: "assistant", Kind: proto.KindToolUse, ToolName: "Edit", ToolUseID: "t1",
						ToolInput: json.RawMessage(`{"file_path":"server/auth.go"}`)},
					{Seq: 3, Role: "assistant", Kind: proto.KindToolUse, ToolName: "Bash", ToolUseID: "t2",
						ToolInput: json.RawMessage(`{"command":"go test ./internal/auth"}`)},
					{Seq: 4, Role: "user", Kind: proto.KindToolResult, ToolUseID: "t2", IsError: true,
						Text: "FAIL github.com/nwverse/persona/server/internal/auth\nexit code: 1"},
					{Seq: 5, Role: "assistant", Kind: proto.KindText, Text: "Identified the race in refreshLocked; updating tests next."},
				},
				"has_more": false,
			}, nil
		},
		"git.review": func(_ string, _ json.RawMessage) (any, error) {
			return proto.GitReview{
				Root:  "/code/app",
				Scope: proto.ReviewUncommitted,
				Base:  proto.GitReviewBase{Hash: "1111222233334444", Short: "1111222", Subject: "base commit"},
				Head:  &proto.GitReviewHead{Hash: "1111222233334444", Short: "1111222", Branch: "fix/auth-race"},
				Files: []proto.GitReviewFile{
					{Path: "server/auth.go", Status: "modified", Added: 18, Deleted: 4},
					{Path: ".env.local", Status: "modified", Refused: true},
				},
			}, nil
		},
	}
	return e, todoNow(e.s, todo.ID), brief
}

// Acceptance Criterion A1:
// On authorized Claude Code -> Codex and Claude profile -> different profile continuations,
// the same to-do retains its brief, decisions, and workspace; the new Agent receives the saved
// handoff before the task prompt; the owner can inspect exact text, generator, source ranges,
// diff base, gaps, and delivery status.
func TestTodoContextHandoffClaudeToCodexAndProfileSwitch(t *testing.T) {
	cases := []struct {
		name           string
		targetLauncher string
		targetAgent    string
	}{
		{name: "claude_to_codex", targetLauncher: "codex", targetAgent: "codex"},
		{name: "claude_profile_to_different_profile", targetLauncher: "profiles-ag", targetAgent: "claude"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			e, todo, brief := setupHandoffFixture(t, nil, "claude", "claude", "acceptEdits", proto.Session{
				SessionID: "agent01",
				Status:    proto.StatusIdle,
				State:     proto.StateIdle,
			})
			ctx := context.Background()
			setSettings(t, e.s, func(v *Settings) { enabled := true; v.AgentTasks = &enabled })

			e.s.mainPlansWait = false
			// The main conversation may select another provider as part of its existing dispatch
			// authority. user_requested records provenance; it is not an extra Start requirement.
			args, _ := json.Marshal(map[string]any{
				"tasks": []map[string]any{{
					"title":          "Continue auth fix on " + tc.targetLauncher,
					"prompt":         "Finish the remaining auth refresh test and run go test -race.",
					"machine":        "box",
					"cwd":            "/code/app",
					"launcher":       tc.targetLauncher,
					"permission":     "acceptEdits",
					"role":           "worker",
					"user_requested": true,
					"continues":      todo.ID,
					"continues_why":  "Switch launcher to continue the same to-do",
				}},
			})
			planRes, err := e.s.toolTaskPlan(ctx, "m1", args)
			if err != nil {
				t.Fatalf("toolTaskPlan: %v", err)
			}
			if !strings.Contains(planRes, "without asking") {
				t.Fatalf("authorized cross-launcher continuation waited for Start: %s", planRes)
			}

			planID := planIDRe.FindString(planRes)
			if planID == "" {
				t.Fatalf("no plan id in response: %s", planRes)
			}
			// Repeating the already-started plan must not dispatch or inject the handoff twice.
			if _, err := e.s.startPlan(ctx, planID, nil, false); !errors.Is(err, errPlanNotOpen) {
				t.Fatalf("repeated start must reject the closed plan: %v", err)
			}
			waitFor(t, "replacement agent linked", func() bool {
				return len(todoNow(e.s, todo.ID).Agents) == 2 && e.s.taskStarting.Load() == 0
			})

			// Same to-do retains brief, workspace, and historical link.
			gotTodo := todoNow(e.s, todo.ID)
			if gotTodo.Supervision == nil || gotTodo.Supervision.Supervisor != "m1" {
				t.Fatalf("handoff started an unsupervised replacement: %+v", gotTodo.Supervision)
			}
			if todoCount(e.s) != 1 {
				t.Fatalf("created duplicate to-do: count=%d", todoCount(e.s))
			}
			keptBrief, ok := e.s.briefOfTodo(todo.ID)
			if !ok || keptBrief.ID != brief.ID {
				t.Fatalf("brief not retained: %+v", keptBrief)
			}
			if gotTodo.Agents[0].ReleasedAt == nil {
				t.Fatalf("predecessor not released into history: %+v", gotTodo.Agents[0])
			}
			replacement := gotTodo.Agents[1]
			if replacement.Cwd != "/code/app" {
				t.Fatalf("workspace not retained: cwd=%q", replacement.Cwd)
			}

			// Inspect via GET /api/v1/todos/{id} and GET /api/v1/todos/{id}/handoffs.
			detailW := todoHandoffHTTPReq(t, e.s, "GET", "/api/v1/todos/"+todo.ID, "", "")
			if detailW.Code != http.StatusOK {
				t.Fatalf("GET /api/v1/todos/%s = %d %s", todo.ID, detailW.Code, detailW.Body.String())
			}
			var detail struct {
				Handoff *TodoContextHandoff `json:"handoff"`
			}
			if err := json.Unmarshal(detailW.Body.Bytes(), &detail); err != nil {
				t.Fatalf("decode detail: %v", err)
			}
			if detail.Handoff == nil {
				t.Fatalf("expected optional handoff on to-do detail: %s", detailW.Body.String())
			}
			h := detail.Handoff
			if h.Generator != handoffGeneratorDeterministicV1 || h.Strategy != handoffStrategyFreshFull {
				t.Fatalf("unexpected generator/strategy: %+v", h)
			}
			if h.DeliveryStatus != handoffDeliveryAccepted || h.TargetSession != replacement.SessionID || h.DeliveredAt == nil {
				t.Fatalf("delivery not recorded as accepted: %+v", h)
			}
			if h.SourceSession != "agent01" || h.SourceLauncher != "claude" || h.TargetLauncher != tc.targetLauncher {
				t.Fatalf("source/target metadata mismatch: %+v", h)
			}
			if !strings.HasPrefix(h.BriefRevision, brief.ID+"@") {
				t.Fatalf("brief_revision mismatch: %q", h.BriefRevision)
			}
			if len(h.SourceRanges) != 1 || h.SourceRanges[0].FromSeq != 1 || h.SourceRanges[0].ToSeq != 5 || h.SourceRanges[0].ItemCount != 5 {
				t.Fatalf("source_ranges mismatch: %+v", h.SourceRanges)
			}
			if h.DiffRef == nil || h.DiffRef.Scope != proto.ReviewUncommitted || h.DiffRef.BaseShort != "1111222" || h.DiffRef.Branch != "fix/auth-race" {
				t.Fatalf("diff_ref mismatch: %+v", h.DiffRef)
			}
			if !strings.Contains(h.Summary, "server/auth.go (+18/-4)") || !strings.Contains(h.Summary, ".env.local [refused]") {
				t.Fatalf("summary missing workspace snapshot or refused marker:\n%s", h.Summary)
			}
			if utf8.RuneCountInString(h.Summary) > todoHandoffMaxRunes {
				t.Fatalf("summary exceeded cap: %d runes", utf8.RuneCountInString(h.Summary))
			}

			// Verify the replacement Agent received the EXACT saved handoff BEFORE the task prompt!
			e.f.mu.Lock()
			starts := append([]map[string]any(nil), e.f.starts...)
			e.f.mu.Unlock()
			if len(starts) != 1 {
				t.Fatalf("expected 1 start, got %d", len(starts))
			}
			sentPrompt, _ := starts[0]["prompt"].(string)
			handoffIdx := strings.Index(sentPrompt, h.Summary)
			taskIdx := strings.Index(sentPrompt, "Finish the remaining auth refresh test")
			if handoffIdx != 0 || taskIdx <= handoffIdx {
				t.Fatalf("saved handoff was not injected before the task prompt: handoffIdx=%d taskIdx=%d", handoffIdx, taskIdx)
			}
			if starts[0]["permission_mode"] != "acceptEdits" {
				t.Fatalf("switch changed permission mode: got %v, want acceptEdits", starts[0]["permission_mode"])
			}

			// GET /api/v1/todos/{id}/handoffs lists the same artifact.
			listW := todoHandoffHTTPReq(t, e.s, "GET", "/api/v1/todos/"+todo.ID+"/handoffs", "", "")
			if listW.Code != http.StatusOK {
				t.Fatalf("GET handoffs = %d %s", listW.Code, listW.Body.String())
			}
			var listed struct {
				Handoffs []TodoContextHandoff `json:"handoffs"`
			}
			if err := json.Unmarshal(listW.Body.Bytes(), &listed); err != nil || len(listed.Handoffs) != 1 || listed.Handoffs[0].ID != h.ID {
				t.Fatalf("handoffs list mismatch: %+v err=%v", listed, err)
			}

			// Extensions cannot inspect handoff content or execute handoffs.
			extDetail := todoHandoffHTTPReq(t, e.s, "GET", "/api/v1/todos/"+todo.ID, "", roleExtension)
			if extDetail.Code == http.StatusOK {
				var extBody struct {
					Handoff *TodoContextHandoff `json:"handoff"`
				}
				_ = json.Unmarshal(extDetail.Body.Bytes(), &extBody)
				if extBody.Handoff != nil {
					t.Fatal("extension token received private handoff on to-do detail")
				}
			}
			if extList := todoHandoffHTTPReq(t, e.s, "GET", "/api/v1/todos/"+todo.ID+"/handoffs", "", roleExtension); extList.Code != http.StatusForbidden {
				t.Fatalf("extension GET handoffs = %d, want 403", extList.Code)
			}
			if extPost := todoHandoffHTTPReq(t, e.s, "POST", "/api/v1/todos/"+todo.ID+"/handoff", `{"launcher":"codex"}`, roleExtension); extPost.Code != http.StatusForbidden {
				t.Fatalf("extension POST handoff = %d, want 403", extPost.Code)
			}
		})
	}
}

// Acceptance Criterion A2 (part 1):
// With 503, expired login, or exhausted quota fixtures, the owner can select an eligible target
// without requiring a response from the old model. Also verifies preview and regeneration superseding.
func TestTodoContextHandoffInterruptionFixturesWithoutOldModelResponse(t *testing.T) {
	fixtures := []struct {
		name string
		sess proto.Session
	}{
		{
			name: "gateway_503",
			sess: proto.Session{
				SessionID:     "agent01",
				Status:        proto.StatusDone,
				State:         proto.StateIdle,
				TurnError:     "API Error: 503 Service Unavailable (upstream pool exhausted)",
				TurnErrorKind: proto.TurnErrorTransient,
				TurnErrorCode: proto.TurnErrorUpstreamUnavailable,
			},
		},
		{
			name: "expired_login",
			sess: proto.Session{
				SessionID:     "agent01",
				Status:        proto.StatusDone,
				State:         proto.StateIdle,
				TurnError:     "Not logged in · Please run /login",
				TurnErrorKind: proto.TurnErrorUser,
				TurnErrorCode: proto.TurnErrorNotLoggedIn,
			},
		},
		{
			name: "exhausted_quota",
			sess: proto.Session{
				SessionID:     "agent01",
				Status:        proto.StatusDone,
				State:         proto.StateIdle,
				TurnError:     "You've hit your usage limit",
				TurnErrorKind: proto.TurnErrorUsageLimit,
				TurnErrorCode: proto.TurnErrorRateLimited,
			},
		},
	}

	for _, tc := range fixtures {
		t.Run(tc.name, func(t *testing.T) {
			e, todo, _ := setupHandoffFixture(t, nil, "claude", "claude", "default", tc.sess)

			// 1. Preview without touching the predecessor.
			prevW := todoHandoffHTTPReq(t, e.s, "POST", "/api/v1/todos/"+todo.ID+"/handoff",
				`{"launcher":"codex","preview":true}`, "")
			if prevW.Code != http.StatusOK {
				t.Fatalf("preview = %d %s", prevW.Code, prevW.Body.String())
			}
			if len(e.f.starts) != 0 || len(e.f.stopped) != 0 {
				t.Fatalf("preview mutated sessions: starts=%d stops=%d", len(e.f.starts), len(e.f.stopped))
			}

			// 2. Owner selects codex directly (no response from old model required).
			body := `{"operation_id":"op_first","launcher":"codex","prompt":"Continue after interruption."}`
			w := todoHandoffHTTPReq(t, e.s, "POST", "/api/v1/todos/"+todo.ID+"/handoff", body, "")
			if w.Code != http.StatusOK {
				t.Fatalf("handoff = %d %s", w.Code, w.Body.String())
			}
			var resp struct {
				Handoff TodoContextHandoff `json:"handoff"`
			}
			if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
				t.Fatal(err)
			}
			if resp.Handoff.DeliveryStatus != handoffDeliveryAccepted || resp.Handoff.TargetSession == "" {
				t.Fatalf("handoff not accepted: %+v", resp.Handoff)
			}
			if !strings.Contains(resp.Handoff.Summary, tc.sess.TurnError) {
				t.Fatalf("summary did not record why predecessor stopped (%q):\n%s", tc.sess.TurnError, resp.Handoff.Summary)
			}

			// 3. Regenerating creates a new artifact superseding the first and preserving both for audit.
			e.sessions("box", proto.Session{
				SessionID:      resp.Handoff.TargetSession,
				MachineID:      "box",
				Cwd:            "/code/app",
				Agent:          "codex",
				Launcher:       "codex",
				PermissionMode: "default",
				Status:         proto.StatusIdle,
				State:          proto.StateIdle,
			})
			w2 := todoHandoffHTTPReq(t, e.s, "POST", "/api/v1/todos/"+todo.ID+"/handoff",
				`{"operation_id":"op_second","launcher":"profiles-ag","regenerate":true}`, "")
			if w2.Code != http.StatusOK {
				t.Fatalf("regenerate handoff = %d %s", w2.Code, w2.Body.String())
			}
			var resp2 struct {
				Handoff TodoContextHandoff `json:"handoff"`
			}
			if err := json.Unmarshal(w2.Body.Bytes(), &resp2); err != nil {
				t.Fatal(err)
			}
			if resp2.Handoff.SupersedesID != resp.Handoff.ID {
				t.Fatalf("supersedes_id = %q, want %q", resp2.Handoff.SupersedesID, resp.Handoff.ID)
			}
			listW := todoHandoffHTTPReq(t, e.s, "GET", "/api/v1/todos/"+todo.ID+"/handoffs", "", "")
			var listed struct {
				Handoffs []TodoContextHandoff `json:"handoffs"`
			}
			_ = json.Unmarshal(listW.Body.Bytes(), &listed)
			if len(listed.Handoffs) != 2 {
				t.Fatalf("expected 2 preserved handoffs for audit, got %d", len(listed.Handoffs))
			}
		})
	}
}

// Acceptance Criterion A2 (part 2):
// A safety refusal or unresolved approval stays visible. A switch never grants approval,
// raises permissions, or replays the blocked action. Defer if the old execution cannot safely
// relinquish the workspace.
func TestTodoContextHandoffSafetyRefusalApprovalPermissionAndQuiescence(t *testing.T) {
	t.Run("safety_refusal_blocks_every_switch_path", func(t *testing.T) {
		safetySess := proto.Session{
			SessionID:     "agent01",
			Status:        proto.StatusDone,
			State:         proto.StateIdle,
			TurnError:     "The model's safeguards flagged this request.",
			TurnErrorKind: proto.TurnErrorUser,
			ModelFallback: &proto.ModelRefusal{
				Subtype:       "model_refusal_fallback",
				OriginalModel: "claude-opus",
				Category:      proto.ModelRecoveryReasonSafeguards,
				Explanation:   "the model's safety classifier refused the request",
			},
		}
		e, todo, _ := setupHandoffFixture(t, nil, "claude", "claude", "default", safetySess)

		// 1. Direct owner handoff refused with 409 safety_refusal.
		w := todoHandoffHTTPReq(t, e.s, "POST", "/api/v1/todos/"+todo.ID+"/handoff",
			`{"operation_id":"op_safety","launcher":"codex"}`, "")
		if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), "safety_refusal") {
			t.Fatalf("expected 409 safety_refusal, got %d %s", w.Code, w.Body.String())
		}

		// 2. task_plan.continues refused before creating a plan card.
		args, _ := json.Marshal(map[string]any{
			"tasks": []map[string]any{{
				"title":         "Try switch after refusal",
				"prompt":        "Continue on codex",
				"machine":       "box",
				"cwd":           "/code/app",
				"launcher":      "codex",
				"role":          "worker",
				"continues":     todo.ID,
				"continues_why": "Attempt switch",
			}},
		})
		if _, err := e.s.toolTaskPlan(context.Background(), "m1", args); err == nil || !strings.Contains(err.Error(), "safety refusal") {
			t.Fatalf("task_plan.continues did not block safety refusal: %v", err)
		}

		// 3. agent_switch and agents_switch refuse safety-refused sessions too.
		ft, _ := switchRig(t)
		safetyRow := agentRow("s1safe0001", "sub", proto.StatusIdle)
		safetyRow.ModelFallback = safetySess.ModelFallback
		safetyRow.TurnError = safetySess.TurnError
		ft.sessions("mac", safetyRow)
		if _, err := switchTool(ft, map[string]any{"machine": "mac", "session_id": "s1safe0001", "launcher": "work", "text": "go on"}); err == nil || !strings.Contains(err.Error(), "safety refusal") {
			t.Fatalf("agent_switch did not block safety refusal: %v", err)
		}
		batchRes, err := batchSwitch(ft, map[string]any{"launcher": "work", "agents": []map[string]any{{"machine": "mac", "session_id": "s1safe0001"}}})
		if err != nil {
			t.Fatal(err)
		}
		if !strings.Contains(batchRes, "refused, nothing changed") || !strings.Contains(batchRes, "safety refusal") {
			t.Fatalf("agents_switch did not refuse safety refusal:\n%s", batchRes)
		}
		if len(nodeCalls(ft.f)) != 0 {
			t.Fatalf("safety refusal triggered node calls: %+v", nodeCalls(ft.f))
		}

		// Predecessor was never stopped and no replacement started.
		if len(e.f.stopped) != 0 || len(e.f.starts) != 0 {
			t.Fatalf("safety refusal triggered stop/start: stops=%d starts=%d", len(e.f.stopped), len(e.f.starts))
		}
	})

	t.Run("unresolved_approval_stays_visible_and_blocks_switch", func(t *testing.T) {
		approvalSess := proto.Session{
			SessionID:           "agent01",
			Status:              proto.StatusBlocked,
			State:               proto.StateNeedsYou,
			PendingInteractions: 1,
		}
		e, todo, _ := setupHandoffFixture(t, nil, "claude", "claude", "default", approvalSess)

		w := todoHandoffHTTPReq(t, e.s, "POST", "/api/v1/todos/"+todo.ID+"/handoff",
			`{"operation_id":"op_approval","launcher":"codex"}`, "")
		if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), "unresolved_approval") {
			t.Fatalf("expected 409 unresolved_approval, got %d %s", w.Code, w.Body.String())
		}
		if len(e.f.stopped) != 0 || len(e.f.starts) != 0 {
			t.Fatalf("unresolved approval switch mutated sessions: stops=%d starts=%d", len(e.f.stopped), len(e.f.starts))
		}

		// agent_switch also refuses when an approval is pending.
		ft, _ := switchRig(t)
		pendingRow := agentRow("s1wait0001", "sub", proto.StatusBlocked)
		pendingRow.PendingInteractions = 1
		ft.sessions("mac", pendingRow)
		if _, err := switchTool(ft, map[string]any{"machine": "mac", "session_id": "s1wait0001", "launcher": "work", "text": "go on"}); err == nil || !strings.Contains(err.Error(), "unresolved approval") {
			t.Fatalf("agent_switch did not block unresolved approval: %v", err)
		}
	})

	t.Run("permission_escalation_refused", func(t *testing.T) {
		e, todo, _ := setupHandoffFixture(t, nil, "claude", "claude", "default", proto.Session{
			SessionID: "agent01",
			Status:    proto.StatusIdle,
			State:     proto.StateIdle,
		})
		w := todoHandoffHTTPReq(t, e.s, "POST", "/api/v1/todos/"+todo.ID+"/handoff",
			`{"operation_id":"op_esc","launcher":"codex","permission":"bypassPermissions"}`, "")
		if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), "permission_escalation") {
			t.Fatalf("expected 409 permission_escalation, got %d %s", w.Code, w.Body.String())
		}
		if len(e.f.starts) != 0 || len(e.f.stopped) != 0 {
			t.Fatalf("permission escalation mutated sessions: stops=%d starts=%d", len(e.f.stopped), len(e.f.starts))
		}
	})

	t.Run("workspace_not_quiescent_defers", func(t *testing.T) {
		e, todo, _ := setupHandoffFixture(t, nil, "claude", "claude", "default", proto.Session{
			SessionID: "agent01",
			Status:    proto.StatusWorking,
			State:     proto.StateWorking,
		})
		e.f.retireErr = errors.New("pane busy: cannot stop cleanly")
		w := todoHandoffHTTPReq(t, e.s, "POST", "/api/v1/todos/"+todo.ID+"/handoff",
			`{"operation_id":"op_defer","launcher":"codex"}`, "")
		if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), "workspace_not_quiescent") {
			t.Fatalf("expected 409 workspace_not_quiescent, got %d %s", w.Code, w.Body.String())
		}
		if len(e.f.starts) != 0 || todoNow(e.s, todo.ID).Agents[0].ReleasedAt != nil {
			t.Fatalf("non-quiescent workspace started replacement or released predecessor")
		}
	})
}

// Acceptance Criterion A3:
// Target startup rejection preserves recoverable task state. Retrying the same operation or racing
// two requests cannot start two active workers or inject context twice. Unknown delivery remains
// pending. Cover missing transcript/diff and legacy-client behavior.
func TestTodoContextHandoffRejectionIdempotencyConcurrencyUnknownAndGaps(t *testing.T) {
	t.Run("target_rejection_preserves_recoverable_state_and_allows_next_target", func(t *testing.T) {
		e, todo, brief := setupHandoffFixture(t, nil, "claude", "claude", "default", proto.Session{
			SessionID: "agent01",
			Status:    proto.StatusDone,
			State:     proto.StateIdle,
		})
		e.f.failStart = &rpc.Error{Code: proto.CodeLauncherMissing, Message: "launcher codex is missing"}
		w := todoHandoffHTTPReq(t, e.s, "POST", "/api/v1/todos/"+todo.ID+"/handoff",
			`{"operation_id":"op_reject","launcher":"codex"}`, "")
		if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), "target_start_rejected") {
			t.Fatalf("expected 409 target_start_rejected, got %d %s", w.Code, w.Body.String())
		}
		var rejected struct {
			Handoff TodoContextHandoff `json:"handoff"`
		}
		_ = json.Unmarshal(w.Body.Bytes(), &rejected)
		if rejected.Handoff.DeliveryStatus != handoffDeliveryRejected || !strings.Contains(rejected.Handoff.DeliveryError, "codex is missing") {
			t.Fatalf("rejected handoff metadata mismatch: %+v", rejected.Handoff)
		}
		// To-do stays open and recoverable with brief and historical link preserved.
		cur := todoNow(e.s, todo.ID)
		if cur.DoneAt != nil || cur.Cancelled() {
			t.Fatalf("target rejection closed to-do: %+v", cur)
		}
		if kept, ok := e.s.briefOfTodo(todo.ID); !ok || kept.ID != brief.ID {
			t.Fatalf("target rejection lost brief: %+v", kept)
		}
		// Owner can recover immediately by selecting another eligible target.
		e.f.failStart = nil
		w2 := todoHandoffHTTPReq(t, e.s, "POST", "/api/v1/todos/"+todo.ID+"/handoff",
			`{"operation_id":"op_recover","launcher":"profiles-ag"}`, "")
		if w2.Code != http.StatusOK {
			t.Fatalf("recovery handoff failed: %d %s", w2.Code, w2.Body.String())
		}
		var recovered struct {
			Handoff TodoContextHandoff `json:"handoff"`
		}
		_ = json.Unmarshal(w2.Body.Bytes(), &recovered)
		if recovered.Handoff.DeliveryStatus != handoffDeliveryAccepted || recovered.Handoff.SupersedesID != rejected.Handoff.ID {
			t.Fatalf("recovery handoff mismatch: %+v", recovered.Handoff)
		}
	})

	t.Run("same_operation_retry_is_idempotent", func(t *testing.T) {
		e, todo, _ := setupHandoffFixture(t, nil, "claude", "claude", "default", proto.Session{
			SessionID: "agent01",
			Status:    proto.StatusDone,
			State:     proto.StateIdle,
		})
		body := `{"operation_id":"op_idem_1","launcher":"codex"}`
		w1 := todoHandoffHTTPReq(t, e.s, "POST", "/api/v1/todos/"+todo.ID+"/handoff", body, "")
		w2 := todoHandoffHTTPReq(t, e.s, "POST", "/api/v1/todos/"+todo.ID+"/handoff", body, "")
		if w1.Code != http.StatusOK || w2.Code != http.StatusOK {
			t.Fatalf("codes = %d, %d", w1.Code, w2.Code)
		}
		var r1, r2 struct {
			Handoff TodoContextHandoff `json:"handoff"`
		}
		_ = json.Unmarshal(w1.Body.Bytes(), &r1)
		_ = json.Unmarshal(w2.Body.Bytes(), &r2)
		if r1.Handoff.ID != r2.Handoff.ID || len(e.f.starts) != 1 {
			t.Fatalf("idempotent retry duplicated work: id1=%s id2=%s starts=%d", r1.Handoff.ID, r2.Handoff.ID, len(e.f.starts))
		}
	})

	t.Run("concurrent_racing_requests_start_at_most_one_worker", func(t *testing.T) {
		e, todo, _ := setupHandoffFixture(t, nil, "claude", "claude", "default", proto.Session{
			SessionID: "agent01",
			Status:    proto.StatusDone,
			State:     proto.StateIdle,
		})
		entered := make(chan struct{}, 1)
		release := make(chan struct{})
		setHub(e.s, &blockingStartHub{Hub: e.f, entered: entered, release: release})

		var wg sync.WaitGroup
		var w1, w2 *httptest.ResponseRecorder
		wg.Add(1)
		go func() {
			defer wg.Done()
			w1 = todoHandoffHTTPReq(t, e.s, "POST", "/api/v1/todos/"+todo.ID+"/handoff",
				`{"operation_id":"op_race_a","launcher":"codex"}`, "")
		}()
		<-entered
		w2 = todoHandoffHTTPReq(t, e.s, "POST", "/api/v1/todos/"+todo.ID+"/handoff",
			`{"operation_id":"op_race_b","launcher":"profiles-ag"}`, "")
		close(release)
		wg.Wait()

		if w1.Code != http.StatusOK {
			t.Fatalf("winner failed: %d %s", w1.Code, w1.Body.String())
		}
		if w2.Code != http.StatusConflict || !strings.Contains(w2.Body.String(), "handoff_in_progress") {
			t.Fatalf("racing request was not serialized: %d %s", w2.Code, w2.Body.String())
		}
		e.f.mu.Lock()
		startsCount := len(e.f.starts)
		e.f.mu.Unlock()
		if startsCount != 1 {
			t.Fatalf("race started %d workers, want 1", startsCount)
		}
	})

	t.Run("unknown_delivery_remains_pending_and_never_replays", func(t *testing.T) {
		e, todo, _ := setupHandoffFixture(t, nil, "claude", "claude", "default", proto.Session{
			SessionID: "agent01",
			Status:    proto.StatusDone,
			State:     proto.StateIdle,
		})
		oldWait, oldPoll := taskStartAdoptWait, taskStartAdoptPoll
		taskStartAdoptWait, taskStartAdoptPoll = 5*time.Millisecond, time.Millisecond
		defer func() { taskStartAdoptWait, taskStartAdoptPoll = oldWait, oldPoll }()

		e.f.failStart = context.DeadlineExceeded
		w := todoHandoffHTTPReq(t, e.s, "POST", "/api/v1/todos/"+todo.ID+"/handoff",
			`{"operation_id":"op_unknown","launcher":"codex"}`, "")
		if w.Code != http.StatusAccepted {
			t.Fatalf("expected 202 for unknown delivery, got %d %s", w.Code, w.Body.String())
		}
		var unknown struct {
			Handoff TodoContextHandoff `json:"handoff"`
		}
		_ = json.Unmarshal(w.Body.Bytes(), &unknown)
		if unknown.Handoff.DeliveryStatus != handoffDeliveryUnknown {
			t.Fatalf("expected delivery_status=unknown, got %+v", unknown.Handoff)
		}
		e.f.mu.Lock()
		startsBeforeRetry := len(e.f.starts)
		e.f.mu.Unlock()
		// Retrying the same operation_id returns the existing unknown handoff and does NOT replay.
		e.f.failStart = nil
		wRetry := todoHandoffHTTPReq(t, e.s, "POST", "/api/v1/todos/"+todo.ID+"/handoff",
			`{"operation_id":"op_unknown","launcher":"codex"}`, "")
		if wRetry.Code != http.StatusOK {
			t.Fatalf("retry of unknown op = %d %s", wRetry.Code, wRetry.Body.String())
		}
		var retried struct {
			Handoff TodoContextHandoff `json:"handoff"`
		}
		_ = json.Unmarshal(wRetry.Body.Bytes(), &retried)
		if retried.Handoff.ID != unknown.Handoff.ID || retried.Handoff.DeliveryStatus != handoffDeliveryUnknown {
			t.Fatalf("unknown op replayed or mutated: %+v", retried.Handoff)
		}
		e.f.mu.Lock()
		startsAfterRetry := len(e.f.starts)
		e.f.mu.Unlock()
		if startsAfterRetry != startsBeforeRetry {
			t.Fatalf("unknown delivery replayed start: before=%d after=%d", startsBeforeRetry, startsAfterRetry)
		}
	})

	t.Run("missing_transcript_and_diff_gaps_with_defer_option", func(t *testing.T) {
		e, todo, _ := setupHandoffFixture(t, nil, "claude", "claude", "default", proto.Session{
			SessionID: "agent01",
			Status:    proto.StatusDone,
			State:     proto.StateIdle,
		})
		e.f.nodes[DeviceSessionItems] = func(_ string, _ json.RawMessage) (any, error) {
			return nil, errors.New("transcript file not found")
		}
		e.f.nodes["git.review"] = func(_ string, _ json.RawMessage) (any, error) {
			return nil, &rpc.Error{Code: proto.CodeNotGit, Message: "not a Git repository"}
		}

		// With defer_on_missing_sources=true, defers before stopping predecessor.
		deferW := todoHandoffHTTPReq(t, e.s, "POST", "/api/v1/todos/"+todo.ID+"/handoff",
			`{"operation_id":"op_gaps_defer","launcher":"codex","defer_on_missing_sources":true}`, "")
		if deferW.Code != http.StatusConflict || !strings.Contains(deferW.Body.String(), "missing_sources") {
			t.Fatalf("expected 409 missing_sources, got %d %s", deferW.Code, deferW.Body.String())
		}
		if len(e.f.stopped) != 0 || len(e.f.starts) != 0 {
			t.Fatalf("deferred handoff mutated sessions")
		}

		// Without defer_on_missing_sources, proceeds and labels both gaps explicitly.
		goW := todoHandoffHTTPReq(t, e.s, "POST", "/api/v1/todos/"+todo.ID+"/handoff",
			`{"operation_id":"op_gaps_go","launcher":"codex"}`, "")
		if goW.Code != http.StatusOK {
			t.Fatalf("handoff with gaps = %d %s", goW.Code, goW.Body.String())
		}
		var got struct {
			Handoff TodoContextHandoff `json:"handoff"`
		}
		_ = json.Unmarshal(goW.Body.Bytes(), &got)
		if len(got.Handoff.MissingSources) < 2 {
			t.Fatalf("expected transcript and git_diff gaps, got %+v", got.Handoff.MissingSources)
		}
		if !strings.Contains(got.Handoff.Summary, "transcript:") || !strings.Contains(got.Handoff.Summary, "git_diff:") {
			t.Fatalf("summary did not label missing sources:\n%s", got.Handoff.Summary)
		}
	})

	t.Run("legacy_client_decodes_detail_and_starts_legacy_plan", func(t *testing.T) {
		e, todo, _ := setupHandoffFixture(t, nil, "claude", "claude", "default", proto.Session{
			SessionID: "agent01",
			Status:    proto.StatusDone,
			State:     proto.StateIdle,
		})
		// Legacy plan saved without any handoff fields still starts via existing endpoint.
		legacy := &TaskPlan{
			ID: "plan_legacy_1", Conversation: "m1", State: planOpen,
			Tasks: []PlanTask{{
				Title: "Legacy continue", Prompt: "Keep going", Machine: "box", Cwd: "/code/app",
				Launcher: "codex", Permission: "default", Continues: todo.ID, ContinuesWhy: "Legacy flow",
			}},
		}
		if err := e.s.savePlan(context.Background(), legacy); err != nil {
			t.Fatal(err)
		}
		startW := todoHandoffHTTPReq(t, e.s, "POST", "/api/v1/task-plans/"+legacy.ID+"/start", `{}`, "")
		if startW.Code != http.StatusOK {
			t.Fatalf("legacy plan start = %d %s", startW.Code, startW.Body.String())
		}
		waitFor(t, "legacy continuation agent linked", func() bool {
			return len(todoNow(e.s, todo.ID).Agents) == 2 && e.s.taskStarting.Load() == 0
		})
		// Legacy client ignoring unknown fields decodes GET /api/v1/todos/{id} cleanly.
		detailW := todoHandoffHTTPReq(t, e.s, "GET", "/api/v1/todos/"+todo.ID, "", "")
		var legacyClient struct {
			Todo json.RawMessage `json:"todo"`
		}
		if err := json.NewDecoder(bytes.NewReader(detailW.Body.Bytes())).Decode(&legacyClient); err != nil || len(legacyClient.Todo) == 0 {
			t.Fatalf("legacy client failed to decode detail: err=%v body=%s", err, detailW.Body.String())
		}
	})
}

func TestTodoContextHandoffPostgresPersistence(t *testing.T) {
	database := taskRecoveryDatabase(t)
	e, todo, _ := setupHandoffFixture(t, database, "claude", "claude", "default", proto.Session{
		SessionID: "agent01",
		Status:    proto.StatusDone,
		State:     proto.StateIdle,
	})
	w := todoHandoffHTTPReq(t, e.s, "POST", "/api/v1/todos/"+todo.ID+"/handoff",
		`{"operation_id":"op_pg_1","launcher":"codex","prompt":"Continue with Postgres persistence."}`, "")
	if w.Code != http.StatusOK {
		t.Fatalf("handoff = %d %s", w.Code, w.Body.String())
	}
	var got struct {
		Handoff TodoContextHandoff `json:"handoff"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &got); err != nil {
		t.Fatal(err)
	}
	// Reload in a brand new Service backed by the same database: handoff survives restarts
	// and preserves idempotency for op_pg_1.
	restarted := newServiceOn(t, e.f, database)
	listW := todoHandoffHTTPReq(t, restarted, "GET", "/api/v1/todos/"+todo.ID+"/handoffs", "", "")
	if listW.Code != http.StatusOK {
		t.Fatalf("restarted GET handoffs = %d %s", listW.Code, listW.Body.String())
	}
	var listed struct {
		Handoffs []TodoContextHandoff `json:"handoffs"`
	}
	if err := json.Unmarshal(listW.Body.Bytes(), &listed); err != nil || len(listed.Handoffs) != 1 {
		t.Fatalf("restarted handoffs mismatch: %+v err=%v", listed, err)
	}
	if listed.Handoffs[0].ID != got.Handoff.ID || listed.Handoffs[0].DeliveryStatus != handoffDeliveryAccepted ||
		listed.Handoffs[0].Summary != got.Handoff.Summary {
		t.Fatalf("restarted handoff did not preserve exact fields: %+v", listed.Handoffs[0])
	}
	if !strings.Contains(listed.Handoffs[0].Summary, "Single-flight per token key") {
		t.Fatalf("persisted handoff summary missing recorded decision:\n%s", listed.Handoffs[0].Summary)
	}
	// Retrying op_pg_1 after restart stays strictly idempotent via the Postgres unique index.
	startsBefore := len(e.f.starts)
	retryW := todoHandoffHTTPReq(t, restarted, "POST", "/api/v1/todos/"+todo.ID+"/handoff",
		`{"operation_id":"op_pg_1","launcher":"codex","prompt":"Continue with Postgres persistence."}`, "")
	if retryW.Code != http.StatusOK {
		t.Fatalf("restarted idempotent retry = %d %s", retryW.Code, retryW.Body.String())
	}
	var retried struct {
		Handoff TodoContextHandoff `json:"handoff"`
	}
	if err := json.Unmarshal(retryW.Body.Bytes(), &retried); err != nil || retried.Handoff.ID != got.Handoff.ID {
		t.Fatalf("restarted retry created new handoff: %+v err=%v", retried, err)
	}
	if len(e.f.starts) != startsBefore {
		t.Fatalf("restarted retry started another worker: before=%d after=%d", startsBefore, len(e.f.starts))
	}
}

// Regression tests for PR #14334 review findings on task_plan ordering and owner handoff validation/state.

func TestTodoContextHandoffPlanContinuationOrderingGuards(t *testing.T) {
	t.Run("inherit_plan_sets_readonly_and_constrains_no_mode_and_mode_launchers", func(t *testing.T) {
		for _, tc := range []struct {
			name            string
			launcher        string
			expectMode      string
			expectRefused   string
			expectNoModeKey bool
		}{
			{name: "no_mode_traex_gets_readonly_constraint", launcher: "traex", expectNoModeKey: true},
			{name: "work_agent_pi_refused_as_readonly_unsupported", launcher: "pi", expectRefused: "readonly_unsupported"},
			{name: "codex_keeps_plan_mode_and_readonly", launcher: "codex", expectMode: "plan"},
			{name: "claude_profile_keeps_plan_mode_and_readonly", launcher: "profiles-ag", expectMode: "plan"},
		} {
			t.Run(tc.name, func(t *testing.T) {
				e, todo, _ := setupHandoffFixture(t, nil, "claude", "claude", "plan", proto.Session{
					SessionID: "agent01",
					Status:    proto.StatusIdle,
					State:     proto.StateIdle,
				})
				e.f.launcherList = append(e.f.launcherList,
					proto.Launcher{Name: "traex", Agent: "traex"},
					proto.Launcher{Name: "pi", Agent: "pi"},
				)
				ctx := context.Background()

				args, _ := json.Marshal(map[string]any{
					"tasks": []map[string]any{{
						"title":          "Continue read-only plan on " + tc.launcher,
						"prompt":         "Audit remaining auth paths without editing.",
						"machine":        "box",
						"cwd":            "/code/app",
						"launcher":       tc.launcher,
						"role":           "worker",
						"user_requested": true,
						"continues":      todo.ID,
						"continues_why":  "Switch launcher while preserving read-only plan mode",
					}},
				})
				planRes, err := e.s.toolTaskPlan(ctx, "m1", args)
				if tc.expectRefused != "" {
					if err == nil || !strings.Contains(err.Error(), tc.expectRefused) {
						var rerr *rpc.Error
						if err == nil || !errors.As(err, &rerr) || rerr.Code != tc.expectRefused {
							t.Fatalf("expected %s refusal, got res=%q err=%v", tc.expectRefused, planRes, err)
						}
					}
					return
				}
				if err != nil {
					t.Fatalf("toolTaskPlan: %v", err)
				}
				if !strings.Contains(planRes, "nothing has started") {
					t.Fatalf("expected owner approval card, got: %s", planRes)
				}
				planID := planIDRe.FindString(planRes)
				plan, err := e.s.loadPlan(ctx, planID)
				if err != nil {
					t.Fatalf("loadPlan: %v", err)
				}
				if !plan.Tasks[0].Readonly {
					t.Fatalf("inheriting plan left Readonly=false on planned task: %+v", plan.Tasks[0])
				}
				if plan.Tasks[0].Permission != "plan" {
					t.Fatalf("expected Permission=plan, got %q", plan.Tasks[0].Permission)
				}

				if _, err := e.s.startPlan(ownerStart(ctx), planID, nil, false); err != nil {
					t.Fatalf("startPlan: %v", err)
				}
				waitFor(t, "replacement agent linked", func() bool {
					return len(todoNow(e.s, todo.ID).Agents) == 2 && e.s.taskStarting.Load() == 0
				})

				e.f.mu.Lock()
				starts := append([]map[string]any(nil), e.f.starts...)
				e.f.mu.Unlock()
				if len(starts) != 1 {
					t.Fatalf("expected 1 start, got %d", len(starts))
				}
				sentPrompt, _ := starts[0]["prompt"].(string)
				roConstraint := prompts.RenderReadonlyTaskConstraint(answerLanguage(e.s.hello(ctx).Locale))
				if !strings.Contains(sentPrompt, roConstraint) {
					t.Fatalf("startTaskAgent skipped read-only prompt constraint:\n%s", sentPrompt)
				}
				if tc.expectNoModeKey {
					if _, hasMode := starts[0]["permission_mode"]; hasMode {
						t.Fatalf("no-mode launcher received permission_mode=%v", starts[0]["permission_mode"])
					}
				} else if starts[0]["permission_mode"] != tc.expectMode {
					t.Fatalf("permission_mode = %v, want %q", starts[0]["permission_mode"], tc.expectMode)
				}
			})
		}
	})

	t.Run("omitted_launcher_resolved_from_agent_new_is_detected_as_provider_switch", func(t *testing.T) {
		store := &profileTaskStore{memStore: &memStore{}, plans: map[string]db.TaskPlanRow{}}
		e, todo, _ := setupHandoffFixture(t, store, "claude", "claude", "default", proto.Session{
			SessionID: "agent01",
			Status:    proto.StatusIdle,
			State:     proto.StateIdle,
		})
		ctx := context.Background()
		setModel(t, e.s, "agent.new", ModelBinding{Provider: "launcher.codex"})
		e.s.mainPlansWait = false

		// Escalating permission with omitted launcher must be refused.
		escArgs, _ := json.Marshal(map[string]any{
			"tasks": []map[string]any{{
				"title":          "Escalate via omitted launcher",
				"prompt":         "Continue with bypassPermissions",
				"machine":        "box",
				"cwd":            "/code/app",
				"permission":     "bypassPermissions",
				"role":           "worker",
				"user_requested": true,
				"continues":      todo.ID,
				"continues_why":  "Continue on default launcher",
			}},
		})
		if _, err := e.s.toolTaskPlan(ctx, "m1", escArgs); err == nil || !strings.Contains(err.Error(), "cannot raise permissions") {
			t.Fatalf("expected permission escalation refusal when omitted launcher resolves to codex, got: %v", err)
		}

		// The supervisor may use the resolved launcher while preserving predecessor permission.
		okArgs, _ := json.Marshal(map[string]any{
			"tasks": []map[string]any{{
				"title":          "Continue via omitted launcher",
				"prompt":         "Continue work",
				"machine":        "box",
				"cwd":            "/code/app",
				"role":           "worker",
				"user_requested": true,
				"continues":      todo.ID,
				"continues_why":  "Continue on default launcher",
			}},
		})
		planRes, err := e.s.toolTaskPlan(ctx, "m1", okArgs)
		if err != nil {
			t.Fatalf("toolTaskPlan: %v", err)
		}
		if strings.Contains(planRes, "nothing has started") {
			t.Fatalf("resolved launcher continuation waited for Start: %s", planRes)
		}
		plan, err := e.s.loadPlan(ctx, planIDRe.FindString(planRes))
		if err != nil {
			t.Fatalf("loadPlan: %v", err)
		}
		if plan.Tasks[0].HandoffOperationID == "" {
			t.Fatalf("expected HandoffOperationID on resolved provider switch: %+v", plan.Tasks[0])
		}
		if plan.Tasks[0].Permission != "default" {
			t.Fatalf("expected inherited Permission=default, got %q", plan.Tasks[0].Permission)
		}
	})

	t.Run("swapped_default_launcher_on_target_machine_is_detected_as_provider_switch", func(t *testing.T) {
		store := &profileTaskStore{memStore: &memStore{}, plans: map[string]db.TaskPlanRow{}}
		e, todo, _ := setupHandoffFixture(t, store, "claude", "claude", "default", proto.Session{
			SessionID: "agent01",
			Status:    proto.StatusIdle,
			State:     proto.StateIdle,
		})
		ctx := context.Background()
		// Machine lacks default launcher "missing-default" and claude; fitPlanTask swaps default to "codex".
		setModel(t, e.s, "agent.new", ModelBinding{Provider: "launcher.missing-default"})
		e.f.launcherList = []proto.Launcher{
			{Name: "codex", Agent: "codex"},
		}
		e.f.devices = []Device{{ID: "box", Name: "box", Online: true, Reachable: true}}
		e.s.mainPlansWait = false

		escArgs, _ := json.Marshal(map[string]any{
			"tasks": []map[string]any{{
				"title":          "Escalate via swapped launcher",
				"prompt":         "Continue with bypassPermissions",
				"machine":        "box",
				"cwd":            "/code/app",
				"permission":     "bypassPermissions",
				"role":           "worker",
				"user_requested": true,
				"continues":      todo.ID,
				"continues_why":  "Continue on swapped launcher",
			}},
		})
		if _, err := e.s.toolTaskPlan(ctx, "m1", escArgs); err == nil || !strings.Contains(err.Error(), "cannot raise permissions") {
			t.Fatalf("expected permission escalation refusal on swapped launcher, got: %v", err)
		}

		okArgs, _ := json.Marshal(map[string]any{
			"tasks": []map[string]any{{
				"title":          "Continue via swapped launcher",
				"prompt":         "Continue work",
				"machine":        "box",
				"cwd":            "/code/app",
				"role":           "worker",
				"user_requested": true,
				"continues":      todo.ID,
				"continues_why":  "Continue on swapped launcher",
			}},
		})
		planRes, err := e.s.toolTaskPlan(ctx, "m1", okArgs)
		if err != nil {
			t.Fatalf("toolTaskPlan: %v", err)
		}
		if strings.Contains(planRes, "nothing has started") {
			t.Fatalf("swapped launcher continuation waited for Start: %s", planRes)
		}
		plan, err := e.s.loadPlan(ctx, planIDRe.FindString(planRes))
		if err != nil {
			t.Fatalf("loadPlan: %v", err)
		}
		if plan.Tasks[0].Launcher != "codex" || plan.Tasks[0].HandoffOperationID == "" || plan.Tasks[0].Permission != "default" {
			t.Fatalf("unexpected swapped plan task: %+v", plan.Tasks[0])
		}
	})

	t.Run("flexible_health_detour_to_other_machine_is_detected_as_provider_switch", func(t *testing.T) {
		e, todo, _ := setupHandoffFixture(t, nil, "claude", "claude", "default", proto.Session{
			SessionID: "agent01",
			Status:    proto.StatusIdle,
			State:     proto.StateIdle,
		})
		ctx := context.Background()
		e.f.devices = []Device{
			{ID: "box", Name: "box", Online: true, Reachable: true, ClaudeVersion: "1.0"},
			{ID: "box2", Name: "box2", Online: true, Reachable: true, ClaudeVersion: "1.0"},
		}
		e.sessions("box2")
		reset := time.Now().Add(2 * time.Hour)
		e.s.recordHealth(ctx, "box", "claude", healthResult{
			err: "You've hit your usage limit", kind: errKindUsageLimit, resetsAt: &reset, source: healthSourceSession,
		})
		e.s.mainPlansWait = false

		escArgs, _ := json.Marshal(map[string]any{
			"tasks": []map[string]any{{
				"title":          "Escalate via flexible detour",
				"prompt":         "Continue with bypassPermissions",
				"machine":        "box",
				"machine_policy": "flexible",
				"cwd":            "/code/app",
				"launcher":       "claude",
				"permission":     "bypassPermissions",
				"role":           "worker",
				"user_requested": true,
				"continues":      todo.ID,
				"continues_why":  "Continue on healthy machine",
			}},
		})
		if _, err := e.s.toolTaskPlan(ctx, "m1", escArgs); err == nil || !strings.Contains(err.Error(), "cannot raise permissions") {
			t.Fatalf("expected permission escalation refusal after healthyPlace detour, got: %v", err)
		}

		okArgs, _ := json.Marshal(map[string]any{
			"tasks": []map[string]any{{
				"title":          "Continue via flexible detour",
				"prompt":         "Continue work",
				"machine":        "box",
				"machine_policy": "flexible",
				"cwd":            "/code/app",
				"launcher":       "claude",
				"role":           "worker",
				"user_requested": true,
				"continues":      todo.ID,
				"continues_why":  "Continue on healthy machine",
			}},
		})
		planRes, err := e.s.toolTaskPlan(ctx, "m1", okArgs)
		if err != nil {
			t.Fatalf("toolTaskPlan: %v", err)
		}
		if !strings.Contains(planRes, "nothing has started") {
			t.Fatalf("health-detoured continuation auto-started without owner approval: %s", planRes)
		}
		plan, err := e.s.loadPlan(ctx, planIDRe.FindString(planRes))
		if err != nil {
			t.Fatalf("loadPlan: %v", err)
		}
		if plan.Tasks[0].Machine != "box2" || plan.Tasks[0].HandoffOperationID == "" || plan.Tasks[0].Permission != "default" {
			t.Fatalf("unexpected detoured plan task: %+v", plan.Tasks[0])
		}
	})
}

func TestTodoContextHandoffOwnerValidationBeforeStoppingPredecessor(t *testing.T) {
	t.Run("missing_launcher_leaves_predecessor_running_and_linked", func(t *testing.T) {
		e, todo, _ := setupHandoffFixture(t, nil, "claude", "claude", "default", proto.Session{
			SessionID: "agent01",
			Status:    proto.StatusWorking,
			State:     proto.StateWorking,
		})
		w := todoHandoffHTTPReq(t, e.s, "POST", "/api/v1/todos/"+todo.ID+"/handoff",
			`{"operation_id":"op_missing","launcher":"codex-typo"}`, "")
		if w.Code != http.StatusConflict {
			t.Fatalf("expected 409 for missing launcher, got %d %s", w.Code, w.Body.String())
		}
		if len(e.f.stopped) != 0 || len(e.f.starts) != 0 {
			t.Fatalf("missing launcher stopped or started agents: stops=%d starts=%d", len(e.f.stopped), len(e.f.starts))
		}
		if todoNow(e.s, todo.ID).Agents[0].ReleasedAt != nil {
			t.Fatalf("missing launcher released predecessor link")
		}
	})

	t.Run("unhealthy_launcher_leaves_predecessor_running_and_linked", func(t *testing.T) {
		e, todo, _ := setupHandoffFixture(t, nil, "claude", "claude", "default", proto.Session{
			SessionID: "agent01",
			Status:    proto.StatusWorking,
			State:     proto.StateWorking,
		})
		reset := time.Now().Add(2 * time.Hour)
		e.s.recordHealth(context.Background(), "box", "codex", healthResult{
			err: "You've hit your usage limit", kind: errKindUsageLimit, resetsAt: &reset, source: healthSourceSession,
		})
		w := todoHandoffHTTPReq(t, e.s, "POST", "/api/v1/todos/"+todo.ID+"/handoff",
			`{"operation_id":"op_unhealthy","launcher":"codex"}`, "")
		if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), "launcher_unhealthy") {
			t.Fatalf("expected 409 launcher_unhealthy, got %d %s", w.Code, w.Body.String())
		}
		if len(e.f.stopped) != 0 || len(e.f.starts) != 0 {
			t.Fatalf("unhealthy launcher stopped or started agents: stops=%d starts=%d", len(e.f.stopped), len(e.f.starts))
		}
		if todoNow(e.s, todo.ID).Agents[0].ReleasedAt != nil {
			t.Fatalf("unhealthy launcher released predecessor link")
		}
	})

	t.Run("incompatible_permission_or_effort_for_no_mode_launcher_leaves_predecessor_running", func(t *testing.T) {
		e, todo, _ := setupHandoffFixture(t, nil, "claude", "claude", "default", proto.Session{
			SessionID: "agent01",
			Status:    proto.StatusWorking,
			State:     proto.StateWorking,
		})
		e.f.launcherList = append(e.f.launcherList,
			proto.Launcher{Name: "traex", Agent: "traex"},
			proto.Launcher{Name: "omp", Agent: "omp"},
		)
		wPerm := todoHandoffHTTPReq(t, e.s, "POST", "/api/v1/todos/"+todo.ID+"/handoff",
			`{"operation_id":"op_traex_perm","launcher":"traex","permission":"default"}`, "")
		if wPerm.Code != http.StatusBadRequest {
			t.Fatalf("expected 400 for traex permission mode, got %d %s", wPerm.Code, wPerm.Body.String())
		}
		wEff := todoHandoffHTTPReq(t, e.s, "POST", "/api/v1/todos/"+todo.ID+"/handoff",
			`{"operation_id":"op_omp_effort","launcher":"omp","effort":"high"}`, "")
		if wEff.Code != http.StatusBadRequest {
			t.Fatalf("expected 400 for omp effort, got %d %s", wEff.Code, wEff.Body.String())
		}
		if len(e.f.stopped) != 0 || len(e.f.starts) != 0 {
			t.Fatalf("incompatible modes stopped or started agents: stops=%d starts=%d", len(e.f.stopped), len(e.f.starts))
		}
		if todoNow(e.s, todo.ID).Agents[0].ReleasedAt != nil {
			t.Fatalf("incompatible modes released predecessor link")
		}
	})

	t.Run("readonly_predecessor_onto_work_agent_refused_before_stopping", func(t *testing.T) {
		e, todo, _ := setupHandoffFixture(t, nil, "claude", "claude", "plan", proto.Session{
			SessionID: "agent01",
			Status:    proto.StatusWorking,
			State:     proto.StateWorking,
		})
		e.f.launcherList = append(e.f.launcherList, proto.Launcher{Name: "pi", Agent: "pi"})
		w := todoHandoffHTTPReq(t, e.s, "POST", "/api/v1/todos/"+todo.ID+"/handoff",
			`{"operation_id":"op_pi_ro","launcher":"pi"}`, "")
		if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), "readonly_unsupported") {
			t.Fatalf("expected 409 readonly_unsupported, got %d %s", w.Code, w.Body.String())
		}
		if len(e.f.stopped) != 0 || len(e.f.starts) != 0 {
			t.Fatalf("readonly refusal stopped or started agents: stops=%d starts=%d", len(e.f.stopped), len(e.f.starts))
		}
		if todoNow(e.s, todo.ID).Agents[0].ReleasedAt != nil {
			t.Fatalf("readonly refusal released predecessor link")
		}
	})
}

type stopHookHub struct {
	Hub
	onStop func(machine string)
}

func (h *stopHookHub) DeviceCall(ctx context.Context, p DeviceCall) (json.RawMessage, error) {
	if p.Method == deviceSessionStop && h.onStop != nil {
		h.onStop(p.Device)
	}
	return h.Hub.DeviceCall(ctx, p)
}

func TestTodoContextHandoffRefreshesMetadataAfterLockAndStop(t *testing.T) {
	e, todo, _ := setupHandoffFixture(t, nil, "claude", "claude", "default", proto.Session{
		SessionID: "agent01",
		Status:    proto.StatusWorking,
		State:     proto.StateWorking,
		TurnError: "stale initial error",
	})
	var priorID string
	setHub(e.s, &stopHookHub{
		Hub: e.f,
		onStop: func(machine string) {
			// Simulate final predecessor failure & an intervening handoff record arriving by the time stop completes.
			e.sessions(machine, proto.Session{
				SessionID:      "agent01",
				MachineID:      machine,
				Cwd:            "/code/app",
				Agent:          "claude",
				Launcher:       "claude",
				PermissionMode: "default",
				Status:         proto.StatusDone,
				State:          proto.StateIdle,
				TurnError:      "API Error: 503 fresh post-stop failure",
				UpdatedAt:      time.Now().UTC(),
			})
			saved, _, err := e.s.persistTodoContextHandoff(context.Background(), TodoContextHandoff{
				OperationID:    "op_intervening_prior",
				TodoID:         todo.ID,
				Summary:        "prior handoff",
				DeliveryStatus: handoffDeliveryRejected,
			})
			if err == nil {
				priorID = saved.ID
			}
		},
	})

	w := todoHandoffHTTPReq(t, e.s, "POST", "/api/v1/todos/"+todo.ID+"/handoff",
		`{"operation_id":"op_fresh_meta","launcher":"codex"}`, "")
	if w.Code != http.StatusOK {
		t.Fatalf("handoff = %d %s", w.Code, w.Body.String())
	}
	var resp struct {
		Handoff TodoContextHandoff `json:"handoff"`
	}
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(resp.Handoff.Summary, "API Error: 503 fresh post-stop failure") {
		t.Fatalf("handoff recorded stale failure instead of post-stop failure:\n%s", resp.Handoff.Summary)
	}
	if priorID == "" || resp.Handoff.SupersedesID != priorID {
		t.Fatalf("supersedes_id = %q, want %q (refreshed under lock/after stop)", resp.Handoff.SupersedesID, priorID)
	}
}

type failingHandoffStore struct {
	Store
	todoStore
	mu                 sync.Mutex
	rows               map[string]db.TodoContextHandoffRow
	byOp               map[string]string
	failUpdateDelivery error
	failLinkAgent      error
	linkAttempts       int
}

func newFailingHandoffStore(base Store, failUpdateDelivery, failLinkAgent error) *failingHandoffStore {
	if base == nil {
		base = &memStore{}
	}
	return &failingHandoffStore{
		Store:              base,
		rows:               map[string]db.TodoContextHandoffRow{},
		byOp:               map[string]string{},
		failUpdateDelivery: failUpdateDelivery,
		failLinkAgent:      failLinkAgent,
	}
}

func (f *failingHandoffStore) CreateTodoContextHandoff(_ context.Context, r db.TodoContextHandoffRow) (*db.TodoContextHandoffRow, bool, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	opKey := r.TodoID + "|" + r.OperationID
	if id, exists := f.byOp[opKey]; exists {
		cp := f.rows[id]
		return &cp, false, nil
	}
	f.rows[r.ID] = r
	f.byOp[opKey] = r.ID
	cp := r
	return &cp, true, nil
}

func (f *failingHandoffStore) UpdateTodoContextHandoffDelivery(_ context.Context, id, targetMachine, targetSession, targetAgent, deliveryStatus, deliveryError string, at time.Time) (*db.TodoContextHandoffRow, error) {
	if f.failUpdateDelivery != nil {
		return nil, f.failUpdateDelivery
	}
	f.mu.Lock()
	defer f.mu.Unlock()
	r, ok := f.rows[id]
	if !ok {
		return nil, errors.New("not found")
	}
	r.TargetMachine = targetMachine
	r.TargetSession = targetSession
	r.TargetAgent = targetAgent
	r.DeliveryStatus = deliveryStatus
	r.DeliveryError = deliveryError
	r.UpdatedAt = at
	f.rows[id] = r
	cp := r
	return &cp, nil
}

func (f *failingHandoffStore) GetTodoContextHandoff(_ context.Context, id string) (*db.TodoContextHandoffRow, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if r, ok := f.rows[id]; ok {
		cp := r
		return &cp, nil
	}
	return nil, nil
}

func (f *failingHandoffStore) GetTodoContextHandoffByOperation(_ context.Context, todoID, operationID string) (*db.TodoContextHandoffRow, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	if id, ok := f.byOp[todoID+"|"+operationID]; ok {
		cp := f.rows[id]
		return &cp, nil
	}
	return nil, nil
}

func (f *failingHandoffStore) ListTodoContextHandoffs(_ context.Context, todoID string) ([]db.TodoContextHandoffRow, error) {
	f.mu.Lock()
	defer f.mu.Unlock()
	var out []db.TodoContextHandoffRow
	for _, r := range f.rows {
		if r.TodoID == todoID {
			out = append(out, r)
		}
	}
	return out, nil
}

func (f *failingHandoffStore) ListTodoDecisions(_ context.Context, _ string, _ bool) ([]db.TodoDecisionRow, error) {
	return nil, nil
}

func (f *failingHandoffStore) LinkTodoAgentWithSource(_ context.Context, _, _, _, _, _, _, _, _, _ string, _ time.Time) ([]string, error) {
	f.mu.Lock()
	f.linkAttempts++
	err := f.failLinkAgent
	f.mu.Unlock()
	if err != nil {
		return nil, err
	}
	return nil, nil
}

func TestTodoContextHandoffReviewRegressions(t *testing.T) {
	t.Run("agents_switch_refuses_and_drops_unresolved_approval", func(t *testing.T) {
		ft, _ := switchRig(t)
		pendingRow := agentRow("s1wait0002", "sub", proto.StatusBlocked)
		pendingRow.PendingInteractions = 1
		ft.sessions("mac", pendingRow)

		// switchPlan refuses immediately instead of queueing.
		plan := ft.s.switchPlan(context.Background(), Device{ID: "mac", Name: "Mac", Reachable: true}, pendingRow, switchTarget{Launcher: "work"})
		if plan.plan != switchRefused || !strings.Contains(plan.why, "unresolved approval") {
			t.Fatalf("agents_switch plan = %+v, want refused for unresolved approval", plan)
		}

		// A queued switch whose session later gains an unresolved approval is dropped by runSwitchQueue.
		ft.s.switchQ.mu.Lock()
		if ft.s.switchQ.items == nil {
			ft.s.switchQ.items = map[string]queuedSwitch{}
		}
		k := switchKey("mac", "s1wait0002")
		ft.s.switchQ.items[k] = queuedSwitch{Machine: "mac", Session: "s1wait0002", From: "sub", Launcher: "work", At: time.Now().UTC()}
		ft.s.switchQ.mu.Unlock()

		ft.s.runSwitchQueue(context.Background(), "mac")
		ft.s.switchQ.mu.Lock()
		_, stillQueued := ft.s.switchQ.items[k]
		ft.s.switchQ.mu.Unlock()
		if stillQueued {
			t.Fatal("runSwitchQueue kept a queued switch after session gained an unresolved approval")
		}
	})

	t.Run("empty_predecessor_permission_fails_closed_and_recovers_plan_readonly", func(t *testing.T) {
		// 1) Empty live PermissionMode without plan permission fails closed against bypassPermissions.
		e, todo, _ := setupHandoffFixture(t, nil, "codex", "codex", "", proto.Session{
			SessionID:      "agent01",
			Status:         proto.StatusDone,
			State:          proto.StateIdle,
			PermissionMode: "",
		})
		w := todoHandoffHTTPReq(t, e.s, "POST", "/api/v1/todos/"+todo.ID+"/handoff",
			`{"operation_id":"op_empty_perm_esc","launcher":"claude","permission":"bypassPermissions"}`, "")
		if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), "permission_escalation") {
			t.Fatalf("empty live permission allowed escalation: %d %s", w.Code, w.Body.String())
		}

		// 2) Readonly plan predecessor with empty live PermissionMode recovers "plan" from current plan assignment.
		ctx := context.Background()
		e2, todo2, _ := setupHandoffFixture(t, nil, "codex", "codex", "", proto.Session{
			SessionID:      "agent01",
			Status:         proto.StatusDone,
			State:          proto.StateIdle,
			PermissionMode: "",
		})
		roPlan := &TaskPlan{
			ID:           "plan_ro_1",
			Conversation: todo2.Origin,
			State:        "started",
			Tasks: []PlanTask{{
				Title:      todo2.Title,
				Machine:    "box",
				Cwd:        "/code/app",
				Launcher:   "codex",
				Readonly:   true,
				Permission: "plan",
				TodoID:     todo2.ID,
			}},
		}
		if err := e2.s.savePlan(ctx, roPlan); err != nil {
			t.Fatal(err)
		}
		e2.s.todos.mu.Lock()
		e2.s.todos.todos[todo2.ID].PlanID = roPlan.ID
		e2.s.todos.mu.Unlock()
		todo2 = todoNow(e2.s, todo2.ID)

		w2 := todoHandoffHTTPReq(t, e2.s, "POST", "/api/v1/todos/"+todo2.ID+"/handoff",
			`{"operation_id":"op_ro_plan_esc","launcher":"claude","permission":"default"}`, "")
		if w2.Code != http.StatusConflict || !strings.Contains(w2.Body.String(), "permission_escalation") {
			t.Fatalf("readonly plan predecessor with empty live PermissionMode allowed default escalation: %d %s", w2.Code, w2.Body.String())
		}
	})

	t.Run("target_start_rejected_does_not_release_predecessor_or_advance_supervision_round", func(t *testing.T) {
		e, todo, _ := setupHandoffFixture(t, nil, "claude", "claude", "default", proto.Session{
			SessionID: "agent01",
			Status:    proto.StatusDone,
			State:     proto.StateIdle,
		})
		e.s.supervise.mu.Lock()
		roundBefore := e.s.supervise.roundEpochs[todo.ID]
		e.s.supervise.mu.Unlock()
		e.f.failStart = &rpc.Error{Code: proto.CodeLauncherMissing, Message: "launcher codex is missing"}

		w := todoHandoffHTTPReq(t, e.s, "POST", "/api/v1/todos/"+todo.ID+"/handoff",
			`{"operation_id":"op_reject_keep_link","launcher":"codex"}`, "")
		if w.Code != http.StatusConflict || !strings.Contains(w.Body.String(), "target_start_rejected") {
			t.Fatalf("expected 409 target_start_rejected, got %d %s", w.Code, w.Body.String())
		}
		cur := todoNow(e.s, todo.ID)
		if len(cur.Agents) == 0 || cur.Agents[0].ReleasedAt != nil {
			t.Fatalf("target_start_rejected released predecessor link (orphaning to-do): %+v", cur.Agents)
		}
		e.s.supervise.mu.Lock()
		roundAfter := e.s.supervise.roundEpochs[todo.ID]
		e.s.supervise.mu.Unlock()
		if roundAfter != roundBefore {
			t.Fatalf("target_start_rejected bumped supervision round from %d to %d", roundBefore, roundAfter)
		}
	})

	t.Run("delivery_record_error_returns_503_with_saved_handoff", func(t *testing.T) {
		e, todo, _ := setupHandoffFixture(t, nil, "claude", "claude", "default", proto.Session{
			SessionID: "agent01",
			Status:    proto.StatusDone,
			State:     proto.StateIdle,
		})
		fs := newFailingHandoffStore(e.s.store(), errors.New("db write failed"), nil)
		e.s.setStore(func() Store { return fs })
		e.f.failStart = &rpc.Error{Code: proto.CodeLauncherMissing, Message: "launcher codex is missing"}

		w := todoHandoffHTTPReq(t, e.s, "POST", "/api/v1/todos/"+todo.ID+"/handoff",
			`{"operation_id":"op_rec_fail","launcher":"codex"}`, "")
		if w.Code != http.StatusServiceUnavailable || !strings.Contains(w.Body.String(), "handoff_delivery_record_failed") {
			t.Fatalf("expected 503 handoff_delivery_record_failed, got %d %s", w.Code, w.Body.String())
		}
		var resp struct {
			Handoff TodoContextHandoff `json:"handoff"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil || resp.Handoff.ID == "" {
			t.Fatalf("expected non-empty saved handoff in 503 response, got %s", w.Body.String())
		}
	})

	t.Run("link_failure_after_start_returns_503_and_records_delivery_unknown", func(t *testing.T) {
		e, todo, _ := setupHandoffFixture(t, nil, "claude", "claude", "default", proto.Session{
			SessionID: "agent01",
			Status:    proto.StatusDone,
			State:     proto.StateIdle,
		})
		fs := newFailingHandoffStore(e.s.store(), nil, errors.New("link write failed"))
		e.s.setStore(func() Store { return fs })

		w := todoHandoffHTTPReq(t, e.s, "POST", "/api/v1/todos/"+todo.ID+"/handoff",
			`{"operation_id":"op_link_fail","launcher":"codex"}`, "")
		if w.Code != http.StatusServiceUnavailable || !strings.Contains(w.Body.String(), "link_failed") {
			t.Fatalf("expected 503 link_failed, got %d %s", w.Code, w.Body.String())
		}
		if fs.linkAttempts < 2 {
			t.Fatalf("linkAttempts = %d, want retry (>=2)", fs.linkAttempts)
		}
		var resp struct {
			Handoff TodoContextHandoff `json:"handoff"`
		}
		if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
			t.Fatal(err)
		}
		if resp.Handoff.DeliveryStatus != handoffDeliveryUnknown {
			t.Fatalf("delivery_status = %q, want %q on link failure", resp.Handoff.DeliveryStatus, handoffDeliveryUnknown)
		}
	})
}

func TestTodoContextHandoffSameEngineCrossMachineAutoStart(t *testing.T) {
	t.Run("main_chat_explicit_same_engine_cross_machine_auto_starts_with_auditable_handoff", func(t *testing.T) {
		e, todo, brief := setupHandoffFixture(t, nil, "claude", "claude", "default", proto.Session{
			SessionID: "agent01",
			Status:    proto.StatusIdle,
			State:     proto.StateIdle,
		})
		ctx := context.Background()
		e.f.devices = []Device{
			{ID: "box", Name: "box", Online: true, Reachable: true, ClaudeVersion: "1.0"},
			{ID: "box2", Name: "box2", Online: true, Reachable: true, ClaudeVersion: "1.0"},
		}
		e.sessions("box2")
		e.s.mainPlansWait = false

		args, _ := json.Marshal(map[string]any{
			"tasks": []map[string]any{{
				"title":          "Continue on box2 with same launcher",
				"prompt":         "Finish auth refresh test on box2.",
				"machine":        "box2",
				"cwd":            "/code/app",
				"launcher":       "claude",
				"role":           "worker",
				"user_requested": false,
				"continues":      todo.ID,
				"continues_why":  "Move task to idle owned machine on same engine",
			}},
		})
		planRes, err := e.s.toolTaskPlan(ctx, "m1", args)
		if err != nil {
			t.Fatalf("toolTaskPlan: %v", err)
		}
		if strings.Contains(planRes, "nothing has started") {
			t.Fatalf("expected same-engine cross-machine continuation from main to auto-start, got: %s", planRes)
		}
		waitFor(t, "replacement agent linked on box2", func() bool {
			return len(todoNow(e.s, todo.ID).Agents) == 2 && e.s.taskStarting.Load() == 0
		})

		gotTodo := todoNow(e.s, todo.ID)
		if gotTodo.Agents[0].ReleasedAt == nil {
			t.Fatalf("predecessor was not released: %+v", gotTodo.Agents[0])
		}
		if gotTodo.Agents[1].MachineID != "box2" {
			t.Fatalf("replacement landed on %q, want box2", gotTodo.Agents[1].MachineID)
		}
		if keptBrief, ok := e.s.briefOfTodo(todo.ID); !ok || keptBrief.ID != brief.ID {
			t.Fatalf("brief not preserved: %+v", keptBrief)
		}
		h := e.s.latestTodoContextHandoff(ctx, todo.ID)
		if h == nil || h.SourceMachine != "box" || h.TargetMachine != "box2" || h.TargetLauncher != "claude" {
			t.Fatalf("expected persisted auditable handoff box->box2, got: %+v", h)
		}
		plan, err := e.s.loadPlan(ctx, planIDRe.FindString(planRes))
		if err != nil {
			t.Fatalf("loadPlan: %v", err)
		}
		if plan.Tasks[0].HandoffOperationID == "" || plan.Tasks[0].HandoffID == "" || plan.Tasks[0].Permission != "default" {
			t.Fatalf("expected handoff provenance and preserved default permission, got: %+v", plan.Tasks[0])
		}
	})

	t.Run("direction_leader_explicit_same_engine_cross_machine_auto_starts", func(t *testing.T) {
		e, _, _ := setupHandoffFixture(t, nil, "claude", "claude", "default", proto.Session{
			SessionID: "agent01",
			Status:    proto.StatusIdle,
			State:     proto.StateIdle,
		})
		ctx := context.Background()
		e.f.devices = []Device{
			{ID: "box", Name: "box", Online: true, Reachable: true, ClaudeVersion: "1.0"},
			{ID: "box2", Name: "box2", Online: true, Reachable: true, ClaudeVersion: "1.0"},
		}
		e.sessions("box2")
		setSettings(t, e.s, func(v *Settings) { v.Leaders = []string{"lead1"} })
		e.sessions("box",
			proto.Session{SessionID: "m1", Title: "Main", Assistant: true, Status: proto.StatusIdle},
			proto.Session{SessionID: "lead1", Title: "Direction Lead", Assistant: true, Status: proto.StatusIdle},
			proto.Session{SessionID: "agent02", MachineID: "box", Cwd: "/code/app", Agent: "claude", Launcher: "claude", PermissionMode: "default", Status: proto.StatusIdle, State: proto.StateIdle},
		)
		leadTodo, err := e.s.createTaskTodo(ctx, "Leader task", "Work.", "lead1", "", "")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := e.s.putTaskBrief(ctx, briefRecord{
			StartedBy: briefByOwner, Conversation: "lead1", Goal: "Finish work", DoneWhen: []string{"tests pass"},
		}, leadTodo.ID); err != nil {
			t.Fatal(err)
		}
		if err := e.s.linkTodoAgent(ctx, leadTodo.ID, "box", "agent02", leadTodo.Title, "/code/app", "claude"); err != nil {
			t.Fatal(err)
		}

		args, _ := json.Marshal(map[string]any{
			"tasks": []map[string]any{{
				"title":          "Leader continues on box2",
				"prompt":         "Continue on box2.",
				"machine":        "box2",
				"cwd":            "/code/app",
				"launcher":       "claude",
				"role":           "worker",
				"user_requested": false,
				"continues":      leadTodo.ID,
				"continues_why":  "Move task to healthy box2",
			}},
		})
		planRes, err := e.s.toolTaskPlan(ctx, "lead1", args)
		if err != nil {
			t.Fatalf("toolTaskPlan: %v", err)
		}
		if strings.Contains(planRes, "nothing has started") {
			t.Fatalf("expected direction leader same-engine cross-machine continuation to auto-start, got: %s", planRes)
		}
		waitFor(t, "leader replacement linked on box2", func() bool {
			return len(todoNow(e.s, leadTodo.ID).Agents) == 2 && e.s.taskStarting.Load() == 0
		})
		if h := e.s.latestTodoContextHandoff(ctx, leadTodo.ID); h == nil || h.TargetMachine != "box2" {
			t.Fatalf("expected auditable handoff recorded for leader continuation, got: %+v", h)
		}
	})

	t.Run("safety_invariants_and_provider_authority_on_cross_machine_continuation", func(t *testing.T) {
		e, todo, _ := setupHandoffFixture(t, nil, "claude", "claude", "default", proto.Session{
			SessionID: "agent01",
			Status:    proto.StatusIdle,
			State:     proto.StateIdle,
		})
		ctx := context.Background()
		e.f.devices = []Device{
			{ID: "box", Name: "box", Online: true, Reachable: true, ClaudeVersion: "1.0"},
			{ID: "box2", Name: "box2", Online: true, Reachable: true, ClaudeVersion: "1.0"},
		}
		e.sessions("box2")
		e.s.mainPlansWait = false

		// 1) Permission escalation on same-engine cross-machine move is still refused fail-closed.
		escArgs, _ := json.Marshal(map[string]any{
			"tasks": []map[string]any{{
				"title":          "Escalate across machines",
				"prompt":         "Continue with bypassPermissions",
				"machine":        "box2",
				"cwd":            "/code/app",
				"launcher":       "claude",
				"permission":     "bypassPermissions",
				"role":           "worker",
				"user_requested": true,
				"continues":      todo.ID,
				"continues_why":  "Try to raise permission on box2",
			}},
		})
		if _, err := e.s.toolTaskPlan(ctx, "m1", escArgs); err == nil || !strings.Contains(err.Error(), "cannot raise permissions") {
			t.Fatalf("expected permission escalation refusal, got: %v", err)
		}

		// 2) owner_step still requires explicit owner Start card even for same-engine cross-machine move.
		ownerStepArgs, _ := json.Marshal(map[string]any{
			"tasks": []map[string]any{{
				"title":          "Outward step on box2",
				"prompt":         "Publish update",
				"machine":        "box2",
				"cwd":            "/code/app",
				"launcher":       "claude",
				"owner_step":     "outward",
				"role":           "worker",
				"user_requested": true,
				"continues":      todo.ID,
				"continues_why":  "Continue with outward owner_step",
			}},
		})
		ownerRes, err := e.s.toolTaskPlan(ctx, "m1", ownerStepArgs)
		if err != nil {
			t.Fatalf("toolTaskPlan owner_step: %v", err)
		}
		if !strings.Contains(ownerRes, "nothing has started") {
			t.Fatalf("owner_step continuation auto-started: %s", ownerRes)
		}

		// 3) Cross-machine AND cross-provider choice is also within the main supervisor's authority.
		crossProvArgs, _ := json.Marshal(map[string]any{
			"tasks": []map[string]any{{
				"title":          "Switch to codex on box2",
				"prompt":         "Continue with codex",
				"machine":        "box2",
				"cwd":            "/code/app",
				"launcher":       "codex",
				"role":           "worker",
				"user_requested": true,
				"continues":      todo.ID,
				"continues_why":  "Switch engine and machine",
			}},
		})
		crossRes, err := e.s.toolTaskPlan(ctx, "m1", crossProvArgs)
		if err != nil {
			t.Fatalf("toolTaskPlan cross-provider: %v", err)
		}
		if strings.Contains(crossRes, "nothing has started") {
			t.Fatalf("authorized provider continuation waited for Start: %s", crossRes)
		}

		waitFor(t, "cross-provider replacement linked", func() bool {
			return len(todoNow(e.s, todo.ID).Agents) == 2 && e.s.taskStarting.Load() == 0
		})

		// 4) Non-orchestrator (ordinary conversation) moving across machines on same launcher still waits for Start.
		e.sessions("box",
			proto.Session{SessionID: "m1", Title: "Main", Assistant: true, Status: proto.StatusIdle},
			proto.Session{SessionID: "ord1", Title: "Ordinary", Assistant: true, Status: proto.StatusIdle},
			proto.Session{SessionID: "agent01", MachineID: "box", Cwd: "/code/app", Agent: "claude", Launcher: "claude", PermissionMode: "default", Status: proto.StatusIdle, State: proto.StateIdle},
		)
		ordTodo, err := e.s.createTaskTodo(ctx, "Ordinary task", "Work.", "ord1", "", "")
		if err != nil {
			t.Fatal(err)
		}
		if _, err := e.s.putTaskBrief(ctx, briefRecord{
			StartedBy: briefByOwner, Conversation: "ord1", Goal: "Do work", DoneWhen: []string{"done"},
		}, ordTodo.ID); err != nil {
			t.Fatal(err)
		}
		if err := e.s.linkTodoAgent(ctx, ordTodo.ID, "box", "agent01", ordTodo.Title, "/code/app", "claude"); err != nil {
			t.Fatal(err)
		}
		ordArgs, _ := json.Marshal(map[string]any{
			"tasks": []map[string]any{{
				"title":          "Ordinary moves to box2",
				"prompt":         "Continue on box2",
				"machine":        "box2",
				"cwd":            "/code/app",
				"launcher":       "claude",
				"role":           "worker",
				"user_requested": true,
				"continues":      ordTodo.ID,
				"continues_why":  "Move to box2",
			}},
		})
		ordRes, err := e.s.toolTaskPlan(ctx, "ord1", ordArgs)
		if err != nil {
			t.Fatalf("toolTaskPlan ordinary: %v", err)
		}
		if !strings.Contains(ordRes, "nothing has started") {
			t.Fatalf("ordinary conversation cross-machine continuation auto-started: %s", ordRes)
		}
	})
}

// Launcher selection belongs to the supervisor's existing dispatch authority. These are the three
// observed switch shapes, including provenance-only user_requested and a read-only retry. No new
// owner permission, duplicate to-do or unaudited handoff may result from that choice.
func TestTodoContextHandoffAuthorizedLauncherSwitches(t *testing.T) {
	for _, tc := range []struct {
		name     string
		source   string
		agent    string
		target   string
		readonly bool
		mixed    bool
	}{
		{name: "gateway_to_agy", source: "gateway-claude", agent: "claude", target: "agy"},
		{name: "codex_gateway_to_agy", source: "gateway-codex", agent: "codex", target: "agy"},
		{name: "agy_to_claude", source: "agy", agent: "agy", target: "claude"},
		{name: "readonly_plan_retry", source: "gateway-claude", agent: "claude", target: "agy", readonly: true},
		{name: "mixed_new_and_continuations", source: "gateway-claude", agent: "claude", target: "agy", mixed: true},
	} {
		for _, leader := range []bool{false, true} {
			for _, requested := range []bool{false, true} {
				name := tc.name
				if leader {
					name += "/leader"
				} else {
					name += "/main"
				}
				if requested {
					name += "/user_requested"
				}
				t.Run(name, func(t *testing.T) {
					e, todo, brief := setupHandoffFixture(t, nil, tc.source, tc.agent, "bypassPermissions", proto.Session{
						SessionID: "agent01", Status: proto.StatusIdle, State: proto.StateIdle,
					})
					e.s.mainPlansWait = false
					if leader {
						setSettings(t, e.s, func(v *Settings) { v.Main, v.Leaders = "other-main", []string{"m1"} })
					}
					e.f.launcherList = append(e.f.launcherList,
						proto.Launcher{Name: "gateway-claude", Agent: "claude"},
						proto.Launcher{Name: "gateway-codex", Agent: "codex"},
						proto.Launcher{Name: "agy", Agent: "agy", Source: proto.HostAgy},
					)
					ctx := context.Background()
					continued := map[string]any{
						"title": "Continue the refresh fix", "prompt": "Finish the regression test.",
						"machine": "box", "cwd": "/code/app", "launcher": tc.target,
						"continues": todo.ID, "continues_why": "Use an available launcher",
						"user_requested": requested, "role": "worker",
					}
					if tc.readonly {
						continued["readonly"], continued["permission"] = true, "plan"
					}
					tasks := []map[string]any{continued}
					wantTodos := 1
					if tc.mixed {
						second, err := e.s.createTaskTodo(ctx, "Check refresh diagnostics", "Inspect diagnostics.", "m1", "", "")
						if err != nil {
							t.Fatal(err)
						}
						if _, err := e.s.putTaskBrief(ctx, briefRecord{StartedBy: briefByOwner, Conversation: "m1", Goal: "Inspect diagnostics", DoneWhen: []string{"diagnosis recorded"}}, second.ID); err != nil {
							t.Fatal(err)
						}
						if err := e.s.linkTodoAgent(ctx, second.ID, "box", "agent02", second.Title, "/code/diagnostics", "codex"); err != nil {
							t.Fatal(err)
						}
						e.sessions("box",
							proto.Session{SessionID: "agent01", MachineID: "box", Cwd: "/code/app", Launcher: tc.source, Agent: tc.agent, PermissionMode: "bypassPermissions", Status: proto.StatusIdle, State: proto.StateIdle},
							proto.Session{SessionID: "agent02", MachineID: "box", Cwd: "/code/diagnostics", Launcher: "gateway-codex", Agent: "codex", PermissionMode: "default", Status: proto.StatusIdle, State: proto.StateIdle},
						)
						tasks = append(tasks, map[string]any{
							"title": second.Title, "prompt": "Continue diagnostics.", "machine": "box", "cwd": "/code/diagnostics",
							"launcher": "agy", "continues": second.ID, "continues_why": "Use an available launcher", "role": "worker",
						}, map[string]any{
							"title": "Independent verification", "prompt": "Verify refresh behavior.", "goal": "Verify refresh behavior",
							"done_when": []string{"verification recorded"}, "machine": "box", "cwd": "/code/verification", "launcher": "claude", "role": "worker",
						})
						wantTodos = 3
					}
					raw, err := json.Marshal(map[string]any{"tasks": tasks})
					if err != nil {
						t.Fatal(err)
					}
					res, err := e.s.toolTaskPlan(ctx, "m1", raw)
					if err != nil || !strings.Contains(res, "without asking") {
						t.Fatalf("authorized switch did not auto-start: %s %v", res, err)
					}
					planID := planIDRe.FindString(res)
					waitFor(t, "authorized plan dispatched", func() bool {
						return len(todoNow(e.s, todo.ID).Agents) == 2 && e.s.taskStarting.Load() == 0
					})
					plan, err := e.s.loadPlan(ctx, planID)
					if err != nil || plan.State != planStarted {
						t.Fatalf("plan not started: %+v %v", plan, err)
					}
					for _, task := range plan.Tasks {
						if task.Error != "" {
							t.Fatalf("dispatch failed: %+v", task)
						}
						if task.Continues == "" {
							continue
						}
						h := e.s.latestTodoContextHandoff(ctx, task.TodoID)
						if task.TodoID != task.Continues || h == nil || h.ID != task.HandoffID || h.OperationID != task.HandoffOperationID || h.DeliveryStatus != handoffDeliveryAccepted || len(h.SourceRanges) == 0 || h.DiffRef == nil {
							t.Fatalf("continuation audit broken: task=%+v handoff=%+v", task, h)
						}
					}
					if tc.readonly && (!plan.Tasks[0].Readonly || plan.Tasks[0].Permission != "plan") {
						t.Fatalf("read-only retry lost its constraint: %+v", plan.Tasks[0])
					}
					if kept, ok := e.s.briefOfTodo(todo.ID); !ok || kept.ID != brief.ID {
						t.Fatalf("brief lost: %+v", kept)
					}
					// Retrying the same plan must not create another todo, replacement or audit record.
					if _, err := e.s.startPlan(ctx, planID, nil, false); !errors.Is(err, errPlanNotOpen) {
						t.Fatalf("repeated start must reject the closed plan: %v", err)
					}
					if todoCount(e.s) != wantTodos || len(taskStartMachines(e.f)) != len(tasks) || len(todoNow(e.s, todo.ID).Agents) != 2 {
						t.Fatalf("duplicate dispatch: todos=%d starts=%v", todoCount(e.s), taskStartMachines(e.f))
					}
					if handoffs, err := e.s.listTodoContextHandoffs(ctx, todo.ID); err != nil || len(handoffs) != 1 {
						t.Fatalf("duplicate audit: %+v %v", handoffs, err)
					}
					for _, push := range e.f.pushed() {
						if push.CardID == planID {
							t.Fatalf("authorized continuation produced owner push: %+v", push)
						}
					}
					if c := cardsToday(t, e.s)[cardPlanWaiting]; c.N != 0 {
						t.Fatalf("category-zero plan was emitted: %+v", c)
					}
				})
			}
		}
	}
}
