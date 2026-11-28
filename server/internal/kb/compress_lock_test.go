package kb

import (
	"context"
	"encoding/json"
	"errors"
	"slices"
	"strings"
	"testing"
	"time"
)

// A chat the user took off every project with lock_lists is held to no project: compression links it into none of the
// projects it names afterwards, new ones included, nor its people, and still logs the update. A chat linked by hand
// and then taken off with lock_lists false is not held (its list is still locked from the link), and linking a held
// chat to a project by hand lifts the hold.
func TestCompressionLeavesAHeldChatOffProjects(t *testing.T) {
	st, err := NewStore(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	y, m, d := time.Now().AddDate(0, 0, -1).Date()
	t0 := time.Date(y, m, d, 12, 0, 0, 0, time.Local)
	src := &fakeSource{
		chats: []SourceChat{{ID: "mix", Name: "杂烩群", Kind: "group", UpdatedAt: t0}, {ID: "team", Name: "B 项目群", Kind: "group", UpdatedAt: t0}},
		msgs: map[string][]SourceMessage{
			"mix":  {msg(0, "张三", "A 周五灰度", t0)},
			"team": {msg(0, "李四", "B 定了", t0)},
		},
	}
	project := "A 上线"
	model := editModel{run: func(_ string, out any) error {
		return json.Unmarshal([]byte(`{"summary":"## 这是什么\n什么都聊","one_line":"杂","importance":"P2","relevance":50,
"projects":[{"name":"`+project+`","kind":"short","update":"`+project+` 有进展"}]}`), out)
	}}
	e := &Engine{Store: st, Sources: []Source{src}, Model: model, Owner: "我"}
	compress := func(id string) {
		t.Helper()
		c, ok := st.Chat(id)
		if !ok {
			t.Fatalf("no chat %s", id)
		}
		if err := e.Compress(context.Background(), c); err != nil {
			t.Fatal(err)
		}
	}
	more := func(chat string, pos int, text string) {
		t.Helper()
		at := t0.Add(time.Duration(pos) * time.Minute)
		src.msgs[chat] = append(src.msgs[chat], msg(pos, "张三", text, at))
		for i := range src.chats {
			if src.chats[i].ID == chat {
				src.chats[i].UpdatedAt = at
			}
		}
		if _, err := e.Archive(context.Background()); err != nil {
			t.Fatal(err)
		}
	}
	if _, err := e.Archive(context.Background()); err != nil {
		t.Fatal(err)
	}
	compress("lark-mix")
	if p, ok := st.Project("a-上线"); !ok || !slices.Contains(p.Chats, "lark-mix") {
		t.Fatalf("first compression did not link: %+v %v", p, ok)
	}

	c0, _ := st.Chat("lark-mix") // as an automatic writer read it, before the hold
	// The user takes the chat off every project and holds its list.
	applyUser(t, e, KindChat, "lark-mix", UserUpdate{Update: Update{RemoveProjects: []string{"a-上线"}}, LockLists: new(true)})
	more("mix", 1, "B 也要做")
	project = "B 上线"
	compress("lark-mix")
	p, ok := st.Project("b-上线")
	if !ok {
		t.Fatal("B 上线 not created")
	}
	if slices.Contains(p.Chats, "lark-mix") || len(p.People) != 0 {
		t.Fatalf("a chat held off projects was linked: chats %v, people %v", p.Chats, p.People)
	}
	if log := strings.Join(st.Log("b-上线", 5), "\n"); !strings.Contains(log, "B 上线 有进展") || !p.Dirty {
		t.Fatalf("the update was not logged (dirty %v): %q", p.Dirty, log)
	}
	if per, _ := st.Person("lark-u-张三"); slices.Contains(per.Projects, "b-上线") {
		t.Fatalf("张三 joined B through the held chat: %v", per.Projects)
	}
	if c, _ := st.Chat("lark-mix"); len(c.Projects) != 0 || !c.HeldOffProjects {
		t.Fatalf("after compression: projects %v, held %v", c.Projects, c.HeldOffProjects)
	}

	// An automatic write from a chat read before the hold keeps it.
	stale := *c0
	if err := st.PutChat(&stale); err != nil {
		t.Fatal(err)
	}
	if c, _ := st.Chat("lark-mix"); !c.HeldOffProjects {
		t.Fatal("an automatic write from a stale chat dropped the hold")
	}
	// An extension cannot link it: the user holds the chat's projects, so the write is refused and the hold stays.
	_, err = e.ApplyExtension(KindProject, "b-上线", Update{Chats: []string{"lark-mix"}, Lock: new(false)}, ExtensionWrite{Extension: "curator"})
	if conflict := (*ConflictError)(nil); !errors.As(err, &conflict) {
		t.Fatalf("an extension linked a held chat: %v", err)
	}
	if c, _ := st.Chat("lark-mix"); !c.HeldOffProjects {
		t.Fatal("a refused extension link lifted the hold")
	}

	// Linked by hand (which locks the list), then taken off without lock_lists: not held, so it links to B.
	applyUser(t, e, KindChat, "lark-team", UserUpdate{Update: Update{Projects: []string{"a-上线"}}})
	applyUser(t, e, KindChat, "lark-team", UserUpdate{Update: Update{RemoveProjects: []string{"a-上线"}}, LockLists: new(false)})
	if c, _ := st.Chat("lark-team"); !taggingFieldLocked(&c.Tagging, "projects") || len(c.Projects) != 0 || c.HeldOffProjects {
		t.Fatalf("lark-team: locked %v, projects %v, held %v", c.LockedFields, c.Projects, c.HeldOffProjects)
	}
	compress("lark-team")
	if p, _ = st.Project("b-上线"); !slices.Contains(p.Chats, "lark-team") {
		t.Fatalf("a chat the user did not hold was not linked: %v", p.Chats)
	}

	// Linking the held chat to a project by hand, from the project's side, lifts the hold.
	applyUser(t, e, KindProject, "b-上线", UserUpdate{Update: Update{Chats: []string{"lark-mix"}}})
	if c, _ := st.Chat("lark-mix"); c.HeldOffProjects {
		t.Fatal("linking by hand left the hold")
	}
	more("mix", 2, "C 开始")
	project = "C 上线"
	compress("lark-mix")
	if p, ok = st.Project("c-上线"); !ok || !slices.Contains(p.Chats, "lark-mix") {
		t.Fatalf("after the hold was lifted: %+v %v", p, ok)
	}
}
