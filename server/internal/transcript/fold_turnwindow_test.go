package transcript

import (
	"fmt"
	"testing"
)

// turnWindowFile writes a Claude transcript of users (indexes into the replies that are user text instead) among n
// lines and opens it in a small window, so TurnWindow has to parse back through ensureTurns.
func turnWindowFile(t *testing.T, n int, users ...int) *File {
	t.Helper()
	defer func(w int64) { windowBytes = w }(windowBytes)
	windowBytes = 4 << 10
	isUser := map[int]bool{}
	for _, u := range users {
		isUser[u] = true
	}
	l := &tline{}
	parent := ""
	for i := range n {
		id := fmt.Sprintf("l%d", i)
		if isUser[i] {
			l.user(id, parent, fmt.Sprintf("ask %d", i), 1)
		} else {
			l.reply(id, parent, fmt.Sprintf("reply %d", i), 1)
		}
		parent = id
	}
	f := NewFile(writeLines(t, l))
	if _, _, err := f.RefreshSpan(); err != nil {
		t.Fatal(err)
	}
	if f.base == 0 {
		t.Fatal("the file was not read in a window")
	}
	return f
}

func lastSeqOf(t *testing.T, f *File) int {
	t.Helper()
	f.mu.Lock()
	defer f.mu.Unlock()
	if len(f.items) == 0 {
		t.Fatal("no items")
	}
	return f.items[len(f.items)-1].Seq
}

// When no user item lies within the bound before firstSeq, the prefix is exactly the newest maxContextItems items
// before it, however much ensureTurns parsed back.
func TestTurnWindowCapsPrefixWithoutEnclosingUser(t *testing.T) {
	for _, tc := range []struct {
		name  string
		users []int
	}{
		{"no user at all", nil},
		{"user beyond the bound", []int{0}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			f := turnWindowFile(t, 3000, tc.users...)
			last := lastSeqOf(t, f)
			prefix, suffix := f.TurnWindow(last, last)
			if len(prefix) != 2000 {
				t.Fatalf("prefix has %d items, want 2000", len(prefix))
			}
			if len(suffix) != 0 {
				t.Fatalf("suffix has %d items, want none", len(suffix))
			}
			if got, want := prefix[len(prefix)-1].Text, "reply 2998"; got != want {
				t.Fatalf("prefix ends with %q, want %q", got, want)
			}
			if got, want := prefix[0].Text, "reply 999"; got != want {
				t.Fatalf("prefix starts with %q, want %q (the newest 2000 items)", got, want)
			}
			for i := 1; i < len(prefix); i++ {
				if prefix[i].Seq <= prefix[i-1].Seq {
					t.Fatalf("prefix out of order at %d: %d after %d", i, prefix[i].Seq, prefix[i-1].Seq)
				}
			}
		})
	}
}

// An enclosing user item within the bound still starts the prefix.
func TestTurnWindowPrefixStartsAtEnclosingUser(t *testing.T) {
	f := turnWindowFile(t, 3000, 0, 1500)
	last := lastSeqOf(t, f)
	prefix, _ := f.TurnWindow(last, last)
	if len(prefix) != 1499 {
		t.Fatalf("prefix has %d items, want 1499", len(prefix))
	}
	if !isBotmuxUserItem(prefix[0]) || prefix[0].Text != "ask 1500" {
		t.Fatalf("prefix starts with %+v, want the user item ask 1500", prefix[0])
	}
	if got := prefix[len(prefix)-1].Text; got != "reply 2998" {
		t.Fatalf("prefix ends with %q", got)
	}
}
