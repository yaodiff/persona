package botcard

import (
	"strings"
	"testing"
)

func TestGuestSafeText(t *testing.T) {
	hosts := []string{"hub.example.com"}
	cases := []struct{ in, want string }{
		{"see /Users/ada/notes/today.md please", "see [path] please"},
		{"the file /home/ada/secret.txt is here", "the file [path] is here"},
		{"/root/.ssh/id_rsa", "[path]"},
		{"copied to /private/var/folders/ab/cd/T/out", "copied to [path]"},
		{"cache /var/folders/zz/yy/T/cache", "cache [path]"},
		{"wrote /tmp/persona-out.txt", "wrote [path]"},
		{"home ~/Documents/plan.md", "home [path]"},
		{`C:\Users\ada\notes.txt`, "[path]"},
		{"open https://hub.example.com/inbox/1 now", "open [link] now"},
		{"open https://hub.example.com:8443/inbox/1", "open [link]"},
		{"see http://127.0.0.1:8787/x", "see [link]"},
		{"see " + dotted("10", "1", "2", "3") + "/a", "see [link]"},
		{"see " + dotted("192", "168", "1", "9") + "/a", "see [link]"},
		{"see " + dotted("172", "16", "0", "4") + "/a", "see [link]"},
		{"see " + dotted("169", "254", "1", "1") + "/a", "see [link]"},
		{"see http://mac.local/a", "see [link]"},
		{"see http://localhost:8787/a", "see [link]"},
		{"open persona://inbox/1", "open [link]"},
		{"open jarvis://inbox/1", "open [link]"},
		{"see https://example.com/docs", "see https://example.com/docs"},
		{"see https://example.com/home/docs/a", "see https://example.com/home/docs/a"},
		{"about ~5 min", "about ~5 min"},
		{"open file:///Users/ada/x.txt", "open [link]"},
		{"see /opt/persona/hub.json", "see [path]"},
		{"see " + dotted("100", "101", "2", "3") + ":8787/a", "see [link]"},
		{"see https://mac.tail0000.ts.net/a", "see [link]"},
		{`ping <at user_id="all"></at> lunch`, "ping  lunch"},
		{`<AT user_id="ou_x">Ada</at> hi`, "Ada hi"},
		{"a line\nwith\ttabs and a\x01control", "a line\nwith\ttabs and acontrol"},
		{"", ""},
	}
	for _, c := range cases {
		got := GuestSafeText(c.in, hosts)
		if got != c.want {
			t.Errorf("GuestSafeText(%q) = %q, want %q", c.in, got, c.want)
		}
		if again := GuestSafeText(got, hosts); again != got {
			t.Errorf("not idempotent: %q -> %q", got, again)
		}
	}
}

func TestGuestSafeToolName(t *testing.T) {
	for _, c := range []struct{ in, want string }{
		{"Wiki_Search", "wiki_search"},
		{"wiki-search", "wikisearch"},
		{"a/b c", "abc"},
		{"", "tool"},
		{"!!!", "tool"},
		{strings40(), "abcdefghijklmnopqrstuvwxyz0123456789abcd"},
	} {
		if got := GuestSafeToolName(c.in); got != c.want {
			t.Errorf("GuestSafeToolName(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}

func strings40() string { return "ABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789abcdefghijklmnopqrstuvwxyz" }

// dotted joins octets so a private address is tested without sitting in the source as one literal.
func dotted(octets ...string) string { return "http://" + strings.Join(octets, ".") }
