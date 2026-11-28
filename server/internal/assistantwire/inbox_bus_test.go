package assistantwire

import (
	"encoding/json"
	"testing"
)

// inbox.post's "on" is a JSON bool the caller may leave out: left out and null only read the switch, true and false
// set it first, and anything else is bad params, as the *bool it replaced read them.
func TestInboxPostSetReadsABoolOrNothing(t *testing.T) {
	for _, c := range []struct {
		params string
		want   InboxPostSet
	}{
		{`{}`, InboxPostLeftOut},
		{`{"on":null}`, InboxPostLeftOut},
		{`{"on":true}`, InboxPostOn},
		{`{"on":false}`, InboxPostOff},
	} {
		var p InboxPostParams
		if err := json.Unmarshal([]byte(c.params), &p); err != nil || p.On != c.want {
			t.Errorf("%s: %v, %v; want %v", c.params, p.On, err, c.want)
		}
	}
	for _, bad := range []string{`{"on":"yes"}`, `{"on":1}`} {
		var p InboxPostParams
		if err := json.Unmarshal([]byte(bad), &p); err == nil {
			t.Errorf("%s decoded as %v, want an error", bad, p.On)
		}
	}
	for _, c := range []struct {
		p    InboxPostParams
		want string
	}{
		{InboxPostParams{}, `{}`},
		{InboxPostParams{On: InboxPostOn}, `{"on":true}`},
		{InboxPostParams{On: InboxPostOff}, `{"on":false}`},
	} {
		if b, err := json.Marshal(c.p); err != nil || string(b) != c.want {
			t.Errorf("%v encodes %s, %v; want %s", c.p.On, b, err, c.want)
		}
	}
}
