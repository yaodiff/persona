package assistant

import (
	"context"
	"strings"
	"testing"

	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/proto"
)

// admissionFleetRows builds session snapshot rows of the given statuses for one machine.
func admissionFleetRows(statuses ...string) []proto.Session {
	rows := make([]proto.Session, 0, len(statuses))
	for i, st := range statuses {
		rows = append(rows, proto.Session{SessionID: "fs" + string(rune('a'+i)), Status: st})
	}
	return rows
}

// A machine whose OS process count is at the cap admits a new start when the fleet snapshot shows those sessions idle:
// idle, done, blocked and offline sessions keep their processes but run no turn, so only working sessions occupy slots.
func TestAdmissionProcsIdleProcessesDoNotFillSlots(t *testing.T) {
	s, _, d := admissionHub(t)
	healthyIdle(d)
	d.machines["busy"] = admissionMachine(8)
	// Load and memory healthy, but 20 claude processes already run (the default cap).
	d.samples["busy"] = admissionSample(1.0, 16*1024*1024*1024, 2*1024*1024*1024, 20)
	// The fleet snapshot sees all 20 sessions, but only one works a turn; the rest are idle in one form or another.
	statuses := []string{proto.StatusWorking}
	for i := 0; i < 19; i++ {
		statuses = append(statuses, []string{proto.StatusIdle, proto.StatusDone, proto.StatusBlocked, proto.StatusOffline}[i%4])
	}
	s.fleetKeepPrevious("busy", admissionFleetRows(statuses...))

	release, err := s.admitAgentStart(context.Background(), "busy")
	if err != nil {
		t.Fatalf("idle processes must not fill the working-session cap, got %v", err)
	}
	release()
}

// When the working sessions themselves reach the cap, the start is refused with the working count, not the process count.
func TestAdmissionProcsWorkingSessionsHitCap(t *testing.T) {
	s, _, d := admissionHub(t)
	healthyIdle(d)
	d.machines["busy"] = admissionMachine(8)
	// The node counts 20 OS processes, but the fleet snapshot shows 20 genuinely working sessions.
	d.samples["busy"] = admissionSample(1.0, 16*1024*1024*1024, 2*1024*1024*1024, 20)
	statuses := make([]string, 20)
	for i := range statuses {
		statuses[i] = proto.StatusWorking
	}
	s.fleetKeepPrevious("busy", admissionFleetRows(statuses...))

	_, err := s.admitAgentStart(context.Background(), "busy")
	if err == nil || !strings.Contains(err.Error(), admissionText(s, "admission_procs", i18n.Args{
		"count": 20, "limit": 20})) {
		t.Fatalf("20 working sessions must hit the cap, got %v", err)
	}
}

// Non-Linux nodes may not report an OS process count; a fleet snapshot still bounds working sessions (#7823).
func TestAdmissionProcsWithoutOSTelemetryUsesFleetSnapshot(t *testing.T) {
	t.Run("working sessions hit the cap", func(t *testing.T) {
		s, _, d := admissionHub(t)
		healthyIdle(d)
		d.machines["busy"] = admissionMachine(8)
		d.samples["busy"] = admissionSample(1.0, 16*1024*1024*1024, 2*1024*1024*1024, 1)
		d.samples["busy"].ClaudeProcs = nil
		statuses := make([]string, 20)
		for i := range statuses {
			statuses[i] = proto.StatusWorking
		}
		s.fleetKeepPrevious("busy", admissionFleetRows(statuses...))

		_, err := s.admitAgentStart(context.Background(), "busy")
		if err == nil || !strings.Contains(err.Error(), admissionText(s, "admission_procs", i18n.Args{
			"count": 20, "limit": 20})) {
			t.Fatalf("nil OS telemetry must not bypass 20 working sessions, got %v", err)
		}
	})

	t.Run("an all-idle snapshot is a real zero", func(t *testing.T) {
		s, _, d := admissionHub(t)
		healthyIdle(d)
		d.machines["busy"] = admissionMachine(8)
		d.samples["busy"] = admissionSample(1.0, 16*1024*1024*1024, 2*1024*1024*1024, 1)
		d.samples["busy"].ClaudeProcs = nil
		s.fleetKeepPrevious("busy", admissionFleetRows(proto.StatusIdle, proto.StatusOffline))

		release, err := s.admitAgentStart(context.Background(), "busy")
		if err != nil {
			t.Fatalf("nil OS telemetry with an all-idle snapshot must admit, got %v", err)
		}
		release()
	})
}

// A reporting machine whose fleet snapshot lists zero working sessions but many idle ones also admits: an empty working
// count is a real zero, not a missing snapshot.
func TestAdmissionProcsAllIdleSnapshotIsZero(t *testing.T) {
	s, _, d := admissionHub(t)
	healthyIdle(d)
	d.machines["busy"] = admissionMachine(8)
	d.samples["busy"] = admissionSample(1.0, 16*1024*1024*1024, 2*1024*1024*1024, 20)
	s.fleetKeepPrevious("busy", admissionFleetRows(proto.StatusIdle, proto.StatusIdle, proto.StatusOffline))

	release, err := s.admitAgentStart(context.Background(), "busy")
	if err != nil {
		t.Fatalf("an all-idle snapshot must read as zero working sessions, got %v", err)
	}
	release()
}

// Without any fleet snapshot for the machine, the gate falls back to the node's OS process count and stays fail-closed.
func TestAdmissionProcsFallbackWithoutFleetSnapshot(t *testing.T) {
	s, _, d := admissionHub(t)
	healthyIdle(d)
	d.machines["busy"] = admissionMachine(8)
	d.samples["busy"] = admissionSample(1.0, 16*1024*1024*1024, 2*1024*1024*1024, 20)
	// No fleetKeepPrevious call: the fleet has never seen this machine.
	if _, ok := s.workingSessions("busy"); ok {
		t.Fatal("a machine with no snapshot must report ok=false")
	}
	if _, err := s.admitAgentStart(context.Background(), "busy"); err == nil ||
		!strings.Contains(err.Error(), admissionText(s, "admission_procs", i18n.Args{
			"count": 20, "limit": 20})) {
		t.Fatalf("without a fleet snapshot the OS process count must still deny, got %v", err)
	}
}
