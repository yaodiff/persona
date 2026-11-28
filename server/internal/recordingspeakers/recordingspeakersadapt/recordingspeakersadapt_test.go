package recordingspeakersadapt_test

import (
	"encoding/json"
	"testing"

	"github.com/nwverse/persona/server/internal/domainkit/domaintest"
	"github.com/nwverse/persona/server/internal/recordingspeakers/recordingspeakersadapt"
)

var fixtures = []string{
	`{"next":1,"edits":{}}`,
	`{"next":6,"people":[{"id":"s1","name":"Ada"},{"id":"s2","name":"Grace","me":true},{"id":"s3"},{"id":"s4","merged_into":"s1"},{"id":"s5","minor":true}],"edits":{"name":3,"merge":1}}`,
}

func TestFixturesReplayUnchanged(t *testing.T) {
	for _, fixture := range fixtures {
		var r recordingspeakersadapt.Record
		if err := json.Unmarshal([]byte(fixture), &r); err != nil {
			t.Fatal(err)
		}
		x := domaintest.Ok(t, recordingspeakersadapt.Restore(r))
		got, err := json.Marshal(recordingspeakersadapt.Store(x))
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != fixture {
			t.Errorf("replayed\n%s\nas\n%s", fixture, got)
		}
	}
}

func TestRestoreRefusesWhatTheStoreCannotHold(t *testing.T) {
	cases := []struct {
		name string
		raw  string
		want string
	}{
		{
			name: "bad speaker id",
			raw:  `{"next":2,"people":[{"id":"bad"}]}`,
			want: "invalid recordingspeakers.speaker.format speaker",
		},
		{
			name: "next not greater than max id",
			raw:  `{"next":2,"people":[{"id":"s2"}]}`,
			want: "invalid recordingspeakers.roster.next_too_small next",
		},
		{
			name: "merge chain refused",
			raw:  `{"next":4,"people":[{"id":"s1"},{"id":"s2","merged_into":"s1"},{"id":"s3","merged_into":"s2"}]}`,
			want: "invalid recordingspeakers.roster.merge_chain mergedInto",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var r recordingspeakersadapt.Record
			if err := json.Unmarshal([]byte(tc.raw), &r); err != nil {
				t.Fatal(err)
			}
			got := domaintest.Fails(t, recordingspeakersadapt.Restore(r))
			if got != tc.want {
				t.Errorf("got %q, want %q", got, tc.want)
			}
		})
	}
}
