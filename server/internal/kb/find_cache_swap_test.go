package kb

import (
	"bytes"
	"os"
	"reflect"
	"testing"
	"time"
)

// A file swapped for same-size text between the cache's stat and its read, then restored under its own modification
// time, must still be searched as it is: the cache keeps a text only under the stat of the file it was read from.
func TestFindCacheKeepsNoTextReadAfterASwap(t *testing.T) {
	for _, tt := range []struct {
		name string
		swap func(t *testing.T, p string, body []byte, at time.Time)
	}{
		{name: "rewritten", swap: func(t *testing.T, p string, body []byte, at time.Time) {
			writeAt(t, p, body, at)
		}},
		{name: "replaced", swap: func(t *testing.T, p string, body []byte, at time.Time) {
			writeAt(t, p+".new", body, at)
			if err := os.Rename(p+".new", p); err != nil {
				t.Fatal(err)
			}
		}},
	} {
		t.Run(tt.name, func(t *testing.T) {
			st, err := NewStore(t.TempDir())
			if err != nil {
				t.Fatal(err)
			}
			c := &Chat{ID: "lark-a", Name: "Team", Kind: "group"}
			if err := st.PutChat(c); err != nil {
				t.Fatal(err)
			}
			at := time.Date(2026, 10, 20, 9, 0, 0, 0, time.Local)
			if err := st.AppendMessages(c, []Line{{At: at, Sender: "A", Text: "alpha ships today"}}); err != nil {
				t.Fatal(err)
			}
			if err := ageFiles(st.Dir, time.Hour); err != nil {
				t.Fatal(err)
			}
			p := st.path(KindChat, c.ID, "messages", "2026-10-20.md")
			first, err := os.ReadFile(p)
			if err != nil {
				t.Fatal(err)
			}
			fi, err := os.Stat(p)
			if err != nil {
				t.Fatal(err)
			}
			second := bytes.ReplaceAll(first, []byte("alpha"), []byte("bravo"))

			e := &Engine{Store: st}
			swapped := false
			findReadHook = func(path string) {
				if path == p && !swapped {
					swapped = true
					tt.swap(t, p, second, fi.ModTime().Add(time.Minute))
				}
			}
			t.Cleanup(func() { findReadHook = nil })
			e.Search("alpha", true, 10)
			if !swapped {
				t.Fatal("the search did not read the day file")
			}
			writeAt(t, p, first, fi.ModTime()) // the first file comes back under its own modification time

			fresh := &Engine{Store: st}
			for _, q := range []string{"alpha", "bravo"} {
				if got, want := e.Search(q, true, 10), fresh.Search(q, true, 10); !reflect.DeepEqual(got, want) {
					t.Fatalf("Search(%q): cached %+v, read %+v", q, got, want)
				}
				if got, want := e.Find(q, 10), fresh.Find(q, 10); !reflect.DeepEqual(got, want) {
					t.Fatalf("Find(%q): cached %+v, read %+v", q, got, want)
				}
			}
			if r := e.Find("alpha", 10); len(r) != 1 || r[0].Count != 1 {
				t.Fatalf("Find(alpha): %+v", r)
			}
		})
	}
}

func writeAt(t *testing.T, p string, body []byte, at time.Time) {
	t.Helper()
	if err := os.WriteFile(p, body, 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.Chtimes(p, at, at); err != nil {
		t.Fatal(err)
	}
}
