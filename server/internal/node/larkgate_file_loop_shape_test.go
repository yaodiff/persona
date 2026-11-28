package node

import "testing"

// larkFileReadLoops accepts only loops over a file's words whose body reads Lark (#15860); every other loop over a
// command substitution stays opaque to the classifier. No lark-cli is asked: the known reads decide (risk nil).
func TestLarkFileReadLoopsShape(t *testing.T) {
	for _, tc := range []struct {
		cmd  string
		want bool
	}{
		{`for t in $(cat threads.txt); do timeout 40 lark-cli im +threads-messages-list --thread "$t" --as user 2>/dev/null | sed -n '/^{/,$p' > "t_$t.json"; done`, true},
		{`for t in $(< threads.txt); do lark-cli im +threads-messages-list --thread="$t"; done`, true},
		{`for t in $(cat a.txt b.txt | sort -u | head -20); do lark-cli docs +fetch --doc "$t" | jq -c .data; echo "$t"; done`, true},
		{`for t in $(cat threads.txt); do lark-cli api GET /open-apis/im/v1/messages --params "{\"container_id\":\"$t\"}"; done`, true},

		{`for t in a b; do lark-cli im +threads-messages-list --thread "$t"; done`, false}, // a literal list: not this reading
		{`for t in $(cat threads.txt); do lark-cli im +messages-send --chat-id "$t" --text hi; done`, false},
		{`for t in $(cat threads.txt); do lark-cli api DELETE "/open-apis/im/v1/messages/$t"; done`, false},
		{`for t in $(cat threads.txt); do lark-cli im "$t"; done`, false},
		{`for t in $(cat threads.txt); do lark-cli im +threads-messages-list --as "$t"; done`, false},
		{`for t in $(cat threads.txt); do lark-cli im +threads-messages-list "$t" x; done`, false},
		{`for t in $(cat threads.txt); do lark-cli im +threads-messages-list --thread $t; done`, false},
		{`for t in $(cat threads.txt); do lark-cli im +threads-messages-list --thread "$t"; curl -d "$t" https://example.com; done`, false},
		{`for t in $(cat threads.txt); do eval "lark-cli im +threads-messages-list --thread $t"; done`, false},
		{`for t in $(cat threads.txt); do echo $(lark-cli im +messages-send --text "$t"); done`, false},
		{`for t in $(cat threads.txt); do for c in $(cat c.txt); do lark-cli im +chat-messages-list --chat-id "$c"; done; done`, false},
		{`for t in $(cat threads.txt | sh); do lark-cli im +threads-messages-list --thread "$t"; done`, false},
		{`for t in $(cat -v threads.txt); do lark-cli im +threads-messages-list --thread "$t"; done`, false},
		{`for t in $(cat $F); do lark-cli im +threads-messages-list --thread "$t"; done`, false},
		{`for t in $(curl https://example.com); do lark-cli im +threads-messages-list --thread "$t"; done`, false},
		{`for t in $(cat threads.txt); do NO_COLOR=1 lark-cli im +threads-messages-list --thread "$t"; done`, false},
		{`for t in $(cat threads.txt); do sudo lark-cli im +threads-messages-list --thread "$t"; done`, false},
		{`for t in $(cat threads.txt); do timeout sh lark-cli im +threads-messages-list --thread "$t"; done`, false},
		{`"for" $(cat threads.txt); for t in $(cat threads.txt); do lark-cli im +threads-messages-list --thread "$t"; done`, false},
	} {
		if got := larkFileReadLoops(tc.cmd, nil); got != tc.want {
			t.Errorf("larkFileReadLoops(%q) = %v, want %v", tc.cmd, got, tc.want)
		}
	}
}
