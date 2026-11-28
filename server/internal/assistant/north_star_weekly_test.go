package assistant

import (
	"bytes"
	"context"
	"encoding/json"
	"testing"

	"github.com/nwverse/persona/server/internal/db"
)

func northStarWeeklyDB(t *testing.T) (*Service, *db.DB, *db.DB, string) {
	t.Helper()
	s, manager, dsn := northStarDBOn(t, newMigratedDatabase(t, db.AllSets))
	hub, err := db.Open(context.Background(), dsn, db.HubSet)
	mustDo(t, err)
	t.Cleanup(hub.Close)
	return s, manager, hub, dsn
}

func TestNorthStarWeeklyCommandEndsAtReadingDayAcrossClockChange(t *testing.T) {
	_, _, _, dsn := northStarWeeklyDB(t)
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("PERSONA_HOME", home)
	t.Setenv("PERSONA_DATABASE_URL", dsn)
	var stdout, stderr bytes.Buffer
	err := NorthStarCommand(context.Background(), []string{
		"--weekly", "--date", "2026-11-04", "--tz", "America/New_York",
	}, &stdout, &stderr)
	if err != nil {
		t.Fatalf("weekly readout failed: %v (%s)", err, stderr.String())
	}
	var out struct {
		ReadingDay string `json:"reading_day"`
		TimeZone   string `json:"time_zone"`
		Weeks      []struct {
			Start string `json:"start"`
			End   string `json:"end"`
		} `json:"weeks"`
	}
	if err := json.Unmarshal(stdout.Bytes(), &out); err != nil {
		t.Fatalf("weekly output is not JSON: %v\n%s", err, stdout.String())
	}
	if out.ReadingDay != "2026-11-04" || out.TimeZone != "America/New_York" {
		t.Fatalf("reading context: %+v", out)
	}
	want := [][2]string{
		{"2026-10-07", "2026-10-13"},
		{"2026-10-14", "2026-10-20"},
		{"2026-10-21", "2026-10-27"},
		{"2026-10-28", "2026-11-03"},
	}
	if len(out.Weeks) != len(want) {
		t.Fatalf("got %d weeks, want %d: %+v", len(out.Weeks), len(want), out.Weeks)
	}
	for i, week := range out.Weeks {
		if week.Start != want[i][0] || week.End != want[i][1] {
			t.Errorf("week %d = %s..%s, want %s..%s", i, week.Start, week.End, want[i][0], want[i][1])
		}
	}
}
