package db

import (
	"io/fs"
	"regexp"
	"slices"
	"strings"
	"testing"

	"github.com/nwverse/persona/server/internal/bus"
)

// checkValues is the list a migration's CHECK (column IN (...)) constraint named name allows, read from the embedded
// migrations of set ("hub" or "manager"). It fails the test when no migration, or more than one, adds that constraint.
func checkValues(t *testing.T, set, name string) []string {
	t.Helper()
	pattern := regexp.MustCompile(`(?is)CONSTRAINT\s+` + regexp.QuoteMeta(name) + `\s+CHECK\s*\(\s*\w+\s+IN\s*\(([^)]*)\)\s*\)`)
	files, err := fs.Glob(migrations, "migrations/"+set+"/*.sql")
	if err != nil {
		t.Fatal(err)
	}
	var found [][]string
	for _, file := range files {
		raw, err := fs.ReadFile(migrations, file)
		if err != nil {
			t.Fatal(err)
		}
		for _, m := range pattern.FindAllStringSubmatch(string(raw), -1) {
			var values []string
			for _, v := range regexp.MustCompile(`'((?:[^']|'')*)'`).FindAllStringSubmatch(m[1], -1) {
				values = append(values, strings.ReplaceAll(v[1], "''", "'"))
			}
			found = append(found, values)
		}
	}
	if len(found) != 1 {
		t.Fatalf("%s: %d migrations add constraint %s, want 1", set, len(found), name)
	}
	return found[0]
}

// assertCheck fails when constraint name allows other values than want, in any order.
func assertCheck(t *testing.T, set, name string, want ...string) {
	t.Helper()
	got := slices.Clone(checkValues(t, set, name))
	want = slices.Clone(want)
	slices.Sort(got)
	slices.Sort(want)
	if !slices.Equal(got, want) {
		t.Errorf("%s %s allows %q, the code writes %q: change the code and add a migration with the new CHECK together",
			set, name, got, want)
	}
}

// TestStorageChecksMatchTheCode keeps the CHECKs on enum-like columns (#15397, docs/plans/plan-typed-core.md step P9)
// and the Go constants they list together, for the constants this package can see; the assistant's own are in
// assistant.TestStorageChecksMatchTheCode.
func TestStorageChecksMatchTheCode(t *testing.T) {
	assertCheck(t, "hub", "bus_identities_role_check", bus.RoleManager)
	for _, set := range []string{"hub", "manager"} {
		assertCheck(t, set, "sent_messages_state_check", SendStateSending, SendStateDelivered, SendStateUnknown, SendStateLater)
	}
	assertCheck(t, "manager", "todos_kind_check", "", KindConcern)
	assertCheck(t, "manager", "project_review_deliveries_outcome_check", "", "self_source",
		"review_receipt_triage_only", "review_receipt_facts_recorded", "review_receipt_no_change",
		"review_receipt_needs_evidence", "review_receipt_missing", "send_echo_missing", "send_unconfirmed",
		"target_not_ready", "target_history_unavailable", "review_receipt_unconfirmed", "waiting_for_review_receipt")
}
