package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/db"
	"github.com/nwverse/persona/server/internal/db/dbtest"
)

// The transport fake uses real database receipts; hub/send_process_test.go tests
// the production sendOnce transport separately. Every phase is a new OS process.
type retryProcessHub struct {
	*fakeHub
	database *db.DB
	effects  string
	crash    bool
	outcome  string
}

func (h *retryProcessHub) DeviceCall(ctx context.Context, p DeviceCall) (json.RawMessage, error) {
	if p.Method != DeviceSessionSendOnce {
		return h.fakeHub.DeviceCall(ctx, p)
	}
	var a map[string]any
	if err := json.Unmarshal(p.Params, &a); err != nil {
		return nil, err
	}
	id, _ := a["client_msg_id"].(string)
	state, err := h.database.ClaimSend(ctx, id, "box", "s1")
	if err != nil {
		return nil, err
	}
	if state == "delivered" {
		return json.RawMessage(`{}`), nil
	}
	if state != "" {
		return nil, errors.New("delivery unknown")
	}
	f, err := os.OpenFile(h.effects, os.O_CREATE|os.O_APPEND|os.O_WRONLY, 0600)
	if err != nil {
		return nil, err
	}
	_, err = f.WriteString(id + "\n")
	_ = f.Close()
	if err != nil {
		return nil, err
	}
	if h.crash && h.outcome == "sending" {
		os.Exit(23)
	}
	state = "delivered"
	if h.outcome == "unknown" {
		state = "unknown"
	}
	if err := h.database.SettleSend(ctx, id, state); err != nil {
		return nil, err
	}
	if h.crash {
		os.Exit(23)
	} // accepted, before manager acknowledgement
	return json.RawMessage(`{}`), nil
}

func TestModelRetryProcessRestart(t *testing.T) {
	if os.Getenv(dbtest.Env) == "" {
		t.Skip(dbtest.Env + " not set")
	}
	for _, main := range []bool{false, true} {
		name := "agent"
		if main {
			name = "assistant"
		}
		for _, outcome := range []string{"pending", "delivered", "unknown", "sending"} {
			t.Run(name+"/"+outcome, func(t *testing.T) {
				dsn := newMigratedDatabase(t, db.AllSets)
				dir := t.TempDir()
				turn := strconv.FormatInt(time.Now().UnixNano(), 10)
				first := "seed-crash"
				if outcome == "pending" {
					first = "seed"
				}
				for _, phase := range []string{first, "recover", "recover", "stop", "cancelled"} {
					ctx, cancel := context.WithTimeout(t.Context(), 30*time.Second)
					cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestModelRetryProcessHelper$", "-test.v")
					cmd.Env = append(os.Environ(), "PERSONA_RETRY_DSN="+dsn, "PERSONA_RETRY_PHASE="+phase, "PERSONA_RETRY_EFFECTS="+filepath.Join(dir, "effects"), "PERSONA_RETRY_MAIN="+name, "PERSONA_RETRY_TURN="+turn, "PERSONA_RETRY_OUTCOME="+outcome)
					out, err := cmd.CombinedOutput()
					cancel()
					if phase == "seed-crash" {
						var exit *exec.ExitError
						if !errors.As(err, &exit) || exit.ExitCode() != 23 {
							t.Fatalf("crash phase: %v\n%s", err, out)
						}
					} else if err != nil {
						t.Fatalf("%s: %v\n%s", phase, err, out)
					}
				}
			})
		}
	}
}

func TestModelRetryProcessHelper(t *testing.T) {
	phase := os.Getenv("PERSONA_RETRY_PHASE")
	if phase == "" {
		t.Skip("subprocess helper")
	}
	ctx := context.Background()
	database, err := db.Open(ctx, os.Getenv("PERSONA_RETRY_DSN"), db.AllSets)
	if err != nil {
		t.Fatal(err)
	}
	defer database.Close()
	f := newFakeHub()
	hub := &retryProcessHub{fakeHub: f, database: database, effects: os.Getenv("PERSONA_RETRY_EFFECTS"), crash: phase == "seed-crash", outcome: os.Getenv("PERSONA_RETRY_OUTCOME")}
	s := newServiceOn(t, f, database)
	s.hub = hub
	s.work.switches = &TriageSwitches{}
	c := apiErrorTurn("s1", "503 Service Unavailable")
	c.Assistant = os.Getenv("PERSONA_RETRY_MAIN") == "assistant"
	ns, err := strconv.ParseInt(os.Getenv("PERSONA_RETRY_TURN"), 10, 64)
	if err != nil {
		t.Fatal(err)
	}
	c.UpdatedAt = time.Unix(0, ns).UTC()
	f.setSessions("box", c)
	if phase == "seed" || phase == "seed-crash" {
		triage(t, s, "s1")
		if phase == "seed" {
			return
		}
	} else if !s.loadWork(ctx) {
		t.Fatal("pending work not loaded")
	}
	e := entryOf(t, s, "s1")
	if phase == "stop" {
		// A new failure schedules another retry; Stop must persist its retirement.
		e.WakeAt = ptrRetryTime(time.Now())
		e.Work.WakeAt = e.WakeAt
		s.putWork(ctx, e)
		s.UserStopped(ctx, SessionRef{MachineID: "box", SessionID: "s1"})
	} else {
		var wg sync.WaitGroup
		for range 10 {
			wg.Add(1)
			go func() {
				defer wg.Done()
				s.workWake(ctx, s.newWorkView(ctx), [2]string{"box", "s1"}, time.Now().Add(time.Hour))
			}()
		}
		wg.Wait()
	}
	b, err := os.ReadFile(hub.effects)
	if err != nil {
		t.Fatal(err)
	}
	n := 0
	for _, ch := range b {
		if ch == '\n' {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("side effects=%d: %s", n, b)
	}
	got := entryOf(t, s, "s1")
	uncertain := hub.outcome == "unknown" || hub.outcome == "sending"
	if phase == "recover" && uncertain {
		if got.WakeAt == nil {
			t.Fatal("uncertain send lost pending retry")
		}
		return
	}
	if got.WakeAt != nil {
		t.Fatalf("retry remains pending after %s: %+v", phase, got)
	}
	if phase == "cancelled" && got.Work.RetiredAt == nil {
		t.Fatal("Stop lost on restart")
	}
}

func ptrRetryTime(at time.Time) *time.Time { return &at }
