package kb

import (
	"fmt"
	"slices"
	"sync"
	"testing"
	"time"
)

// Archiving and history imports note a sender's chats (under chatMu) while compression links projects and a person's
// edit arrives over RPC: each rereads the person under personMu, so none writes back a stale copy over the others'.
func TestPersonUpdatesKeepEachOthersLinks(t *testing.T) {
	e := testEngine(t, nil)
	st := e.Store
	if err := st.PutPerson(&Person{ID: "lark-u1", Source: "lark", SourceID: "u1", Name: "A"}); err != nil {
		t.Fatal(err)
	}
	const n = 40
	at := time.Now().Add(-time.Hour)
	slugs := make([]string, n)
	var wg sync.WaitGroup
	for i := range n {
		wg.Add(3)
		go func() { // archive / history import
			defer wg.Done()
			c := &Chat{ID: fmt.Sprintf("lark-c%d", i)}
			st.chatMu.Lock()
			e.notePerson(c, "lark", SourceMessage{SenderID: "u1", SenderName: "A", At: at})
			st.chatMu.Unlock()
		}()
		go func() { // compression's project linking
			defer wg.Done()
			c := &Chat{ID: fmt.Sprintf("lark-g%d", i), People: []string{"lark-u1"}}
			slugs[i] = e.evolveProject(fmt.Sprintf("Project %d", i), "short", nil, "", c, true, "", at)
		}()
		go func() { // an edit
			defer wg.Done()
			if _, err := e.Apply(KindPerson, "lark-u1", Update{Note: fmt.Sprintf("note %d", i), Lock: new(bool)}); err != nil {
				t.Error(err)
			}
		}()
	}
	wg.Wait()
	p, ok := st.Person("lark-u1")
	if !ok {
		t.Fatal("person gone")
	}
	lost := 0
	for i := range n {
		if !slices.Contains(p.Chats, fmt.Sprintf("lark-c%d", i)) {
			lost++
		}
		if slugs[i] == "" || !slices.Contains(p.Projects, slugs[i]) {
			lost++
		}
	}
	if lost > 0 {
		t.Fatalf("%d links lost: chats %d projects %d", lost, len(p.Chats), len(p.Projects))
	}
}

// A person only another connector knows is left alone, and one not on disk is created only by notePerson.
func TestUpdatePersonCreatesOnlyWhenAsked(t *testing.T) {
	e := testEngine(t, nil)
	st := e.Store
	p, written, err := st.updatePerson("lark-x", false, nil, func(*Person) bool { return true })
	if p != nil || written || err != nil {
		t.Fatalf("missing person: %v %v %v", p, written, err)
	}
	if _, ok := st.Person("lark-x"); ok {
		t.Fatal("written without create")
	}
	c := &Chat{ID: "lark-c1", Connector: "a"}
	e.notePerson(c, "lark", SourceMessage{SenderID: "x", SenderName: "X", At: time.Now()})
	if got, ok := st.Person("lark-x"); !ok || !slices.Contains(got.Chats, "lark-c1") || !slices.Contains(c.People, "lark-x") {
		t.Fatalf("notePerson did not create and link: %+v %v", got, c.People)
	}
	other := &Chat{ID: "lark-c2", Connector: "b"}
	e.notePerson(other, "lark", SourceMessage{SenderID: "x", SenderName: "X", At: time.Now()})
	if got, _ := st.Person("lark-x"); slices.Contains(got.Chats, "lark-c2") || slices.Contains(other.People, "lark-x") {
		t.Fatalf("another connector's chat linked: %v %v", got.Chats, other.People)
	}
}

// A person's edit that links a project keeps a chat the pipeline noted while the project was written: the person is
// read again after the project write, not written back from the copy read before it.
func TestApplyPersonKeepsAChatNotedMeanwhile(t *testing.T) {
	e := testEngine(t, nil)
	st := e.Store
	if err := st.PutPerson(&Person{ID: "lark-u1", Source: "lark", SourceID: "u1", Name: "A", Chats: []string{"lark-c0"}}); err != nil {
		t.Fatal(err)
	}
	if err := st.PutProject(&Project{Slug: "launch", Name: "Launch"}); err != nil {
		t.Fatal(err)
	}
	var once sync.Once
	e.Changed = func(c Change) {
		if c.Kind == KindProject && c.ID == "launch" {
			once.Do(func() {
				st.chatMu.Lock()
				defer st.chatMu.Unlock()
				e.notePerson(&Chat{ID: "lark-c1"}, "lark", SourceMessage{SenderID: "u1", SenderName: "A", At: time.Now()})
			})
		}
	}
	e.ObserveChanges()
	if _, err := e.Apply(KindPerson, "lark-u1", Update{Note: "edited", Projects: []string{"launch"}}); err != nil {
		t.Fatal(err)
	}
	p, _ := st.Person("lark-u1")
	if !slices.Contains(p.Chats, "lark-c1") || !slices.Contains(p.Projects, "launch") || p.Note != "edited" {
		t.Fatalf("person after edit: chats %v projects %v note %q", p.Chats, p.Projects, p.Note)
	}
}

// Archiving (without chatMu) and a history import (under chatMu) add a sender's hints for one person at once: the
// handles are reread and written under personMu with the person, so neither writes back a list missing the other's.
func TestPersonHandlesKeepEachOthersHints(t *testing.T) {
	e := testEngine(t, nil)
	st := e.Store
	e.HandleKey = []byte("0123456789abcdef0123456789abcdef")
	at := time.Now().Add(-time.Hour)
	if err := st.PutPerson(&Person{ID: "lark-u1", Source: "lark", SourceID: "u1", Name: "A", Chats: []string{"lark-c"}, LastSeen: at}); err != nil {
		t.Fatal(err)
	}
	const n = 100
	note := func(hint string) {
		e.notePerson(&Chat{ID: "lark-c"}, "lark", SourceMessage{SenderID: "u1", SenderName: "A", At: at,
			SenderHandles: []Handle{{Kind: HandleEmail, Value: hint}}})
	}
	start := make(chan struct{})
	var wg sync.WaitGroup
	for i := range n {
		wg.Add(2)
		go func() { // history import
			defer wg.Done()
			<-start
			st.chatMu.Lock()
			defer st.chatMu.Unlock()
			note(fmt.Sprintf("history-%d@example.com", i))
		}()
		go func() { // archive
			defer wg.Done()
			<-start
			note(fmt.Sprintf("archive-%d@example.com", i))
		}()
	}
	close(start)
	wg.Wait()
	if got := len(st.PersonHandles("lark-u1")); got != 2*n {
		t.Fatalf("kept %d handles, want %d", got, 2*n)
	}
}
