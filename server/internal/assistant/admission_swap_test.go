package assistant

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/i18n"
)

const gib = int64(1024 * 1024 * 1024)

// swapSample is a healthy 8-core reading (light load, two working sessions) with memUsed of 16 GiB in use and the swap
// figures given, so only the swap line can refuse it.
func swapSample(d *resourceDevs, memUsed, swapUsed, swapTotal int64) {
	d.machines["busy"] = admissionMachine(8)
	s := admissionSample(1.0, 16*gib, memUsed, 2)
	s.SwapUsed, s.SwapTotal = swapUsed, swapTotal
	d.samples["busy"] = s
}

// pinAdmission replaces the cached assistant.admission setting with raw, written as the owner would write it.
func pinAdmission(t *testing.T, s *Service, raw string) {
	t.Helper()
	var cfg AdmissionSettings
	if err := json.Unmarshal([]byte(raw), &cfg); err != nil {
		t.Fatal(err)
	}
	s.admission.cached, s.admission.at = &cfg, time.Now()
}

func swapRefusal(s *Service, percent int, used, total, available int64) string {
	return admissionText(s, "admission_swap", i18n.Args{"percent": percent, "used": humanBytes(used),
		"total": humanBytes(total), "available": humanBytes(available)})
}

// Swap nearly full while memory is tight refuses the start and says why; the memory line alone would let it through
// (3 GiB available is above the 2 GiB line), which is how a 16 GB Mac kept admitting Agents while it swapped (#16757).
func TestAdmissionSwapDenied(t *testing.T) {
	s, f, d := admissionHub(t)
	healthyIdle(d)
	swapSample(d, 13*gib, 58*gib/10, 6*gib)
	ctx := context.Background()

	task := admissionPlanTask("busy", machineFixed)
	_, err := s.admitTaskStart(ctx, task)
	if err == nil {
		t.Fatal("a computer with swap 96% used and 3 GiB free was admitted")
	}
	if want := swapRefusal(s, 96, 58*gib/10, 6*gib, 3*gib); !strings.Contains(err.Error(), want) {
		t.Fatalf("want a swap refusal %q, got %q", want, err.Error())
	}
	if task.Machine != "busy" {
		t.Fatalf("a fixed task must not move: %+v", task)
	}
	if starts := taskStartMachines(f); len(starts) != 0 {
		t.Fatalf("a refused start reached a node: %v", starts)
	}
	// The recommendation reads the same line: the swapping computer is never the one recommended.
	if got, ok := s.recommendMachine(ctx, "", nil); !ok || got.ID != "idle" {
		t.Fatalf("recommendation = %+v %v, want idle", got, ok)
	}
}

// Below the line, with stale swap but plenty of free memory, and with no swap reading at all, the line never refuses.
func TestAdmissionSwapAllowed(t *testing.T) {
	for _, c := range []struct {
		name                         string
		memUsed, swapUsed, swapTotal int64
	}{
		{"swap below the line", 13 * gib, 4 * gib, 6 * gib},         // 66%
		{"swap just below the line", 13 * gib, 79 * gib / 100, gib}, // 79%
		{"stale swap, memory free", 6 * gib, 6 * gib, 6 * gib},      // 100% swap, 10 GiB free
		{"a quarter free is enough", 12 * gib, 6 * gib, 6 * gib},    // 4 GiB of 16 available
		{"no swap configured", 13 * gib, 0, 0},                      // Linux without swap
		{"no swap reading", 13 * gib, 5 * gib, 0},                   // total unreadable
		{"swap reading out of range", 13 * gib, 7 * gib, 6 * gib},   // used above total
		{"negative swap reading", 13 * gib, -1, 6 * gib},            // garbage
		{"swap configured but unused", 13 * gib, 0, 6 * gib},
	} {
		t.Run(c.name, func(t *testing.T) {
			s, _, d := admissionHub(t)
			healthyIdle(d)
			swapSample(d, c.memUsed, c.swapUsed, c.swapTotal)
			task := admissionPlanTask("busy", machineFixed)
			if _, err := s.admitTaskStart(context.Background(), task); err != nil {
				t.Fatalf("admitted start refused: %v", err)
			}
			if task.Machine != "busy" {
				t.Fatalf("an admitted task moved to %q", task.Machine)
			}
		})
	}
}

// max_swap_percent is adjustable for every computer and per computer, and a value out of (0, 100] keeps the default,
// as every other line of the setting does.
func TestAdmissionSwapSetting(t *testing.T) {
	for _, c := range []struct {
		name, setting string
		swapUsed      int64 // of 10 GiB
		refused       bool
	}{
		{"default refuses at 80%", `{}`, 8 * gib, true},
		{"default allows 70%", `{}`, 7 * gib, false},
		{"raised for every computer", `{"max_swap_percent":95}`, 9 * gib, false},
		{"lowered for every computer", `{"max_swap_percent":50}`, 6 * gib, true},
		{"per computer wins", `{"max_swap_percent":95,"machines":{"busy":{"max_swap_percent":50}}}`, 6 * gib, true},
		{"per computer raised", `{"machines":{"busy":{"max_swap_percent":100}}}`, 9 * gib, false},
		{"another computer's entry does not apply", `{"machines":{"idle":{"max_swap_percent":99}}}`, 9 * gib, true},
		{"above 100 keeps the default", `{"max_swap_percent":150}`, 8 * gib, true},
		{"zero keeps the default", `{"max_swap_percent":0}`, 8 * gib, true},
		{"negative keeps the default", `{"machines":{"busy":{"max_swap_percent":-5}}}`, 8 * gib, true},
	} {
		t.Run(c.name, func(t *testing.T) {
			s, _, d := admissionHub(t)
			healthyIdle(d)
			pinAdmission(t, s, c.setting)
			swapSample(d, 13*gib, c.swapUsed, 10*gib)
			_, err := s.admitTaskStart(context.Background(), admissionPlanTask("busy", machineFixed))
			if c.refused != (err != nil) {
				t.Fatalf("refused=%v, want %v (err %v)", err != nil, c.refused, err)
			}
			if err != nil && !strings.Contains(err.Error(), admissionText(s, "admission_swap", i18n.Args{
				"percent": int(c.swapUsed * 100 / (10 * gib)), "used": humanBytes(c.swapUsed),
				"total": humanBytes(10 * gib), "available": humanBytes(3 * gib)})) {
				t.Fatalf("want a swap refusal, got %v", err)
			}
		})
	}
}

// The approval card names the swap line when the setting changes it, for every computer and for one computer.
func TestAdmissionChangeCardSwapLine(t *testing.T) {
	got := admissionCardFor(t, i18n.Lang("en"), `{}`,
		`{"max_swap_percent":90,"machines":{"laptop":{"max_swap_percent":70}}}`, "")
	want := []string{
		"Execution Agent admission gate:",
		"- Swap in use that stops new Agents, every computer: 80% (default) → 90%",
		"- Swap in use that stops new Agents, laptop: 80% (default) → 70%",
	}
	if strings.Join(got, "\n") != strings.Join(want, "\n") {
		t.Fatalf("card:\n%s\nwant:\n%s", strings.Join(got, "\n"), strings.Join(want, "\n"))
	}
}
