package assistant

import (
	"context"
	"encoding/json"
	"net/http"
	"slices"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/identity"
)

func TestTodosV2DirectionVisibilityPrecedenceAndInheritance(t *testing.T) {
	base := time.Date(2026, 11, 27, 10, 0, 0, 0, time.UTC)
	directions := map[string]string{
		"dir_a": "Alpha",
		"dir_b": "Beta",
		"dir_c": "Gamma",
	}

	items := []listedTodo{
		// 1. Explicit direction_id wins over supervisor, origin, and parent.
		{
			Todo: Todo{
				ID:          "explicit_wins",
				DirectionID: " dir_c ",
				Supervision: &Supervision{Supervisor: "dir_a"},
				Origin:      "dir_b",
				PartOf:      "sup_parent",
				CreatedAt:   base,
			},
		},
		// 2. supervision.supervisor (in directions) wins over origin (in directions) and parent.
		{
			Todo: Todo{
				ID:          "sup_wins",
				Supervision: &Supervision{Supervisor: "dir_a"},
				Origin:      "dir_b",
				PartOf:      "origin_parent",
				CreatedAt:   base,
			},
		},
		// 3. origin (in directions) wins over parent when supervisor is unset or unknown.
		{
			Todo: Todo{
				ID:          "origin_wins",
				Supervision: &Supervision{Supervisor: "conv_main"},
				Origin:      "dir_b",
				PartOf:      "sup_parent",
				CreatedAt:   base,
			},
		},
		// Ancestors for step 4.
		{
			Todo: Todo{
				ID:          "sup_parent",
				Supervision: &Supervision{Supervisor: "dir_a"},
				CreatedAt:   base,
			},
		},
		{
			Todo: Todo{
				ID:        "origin_parent",
				Origin:    "dir_b",
				CreatedAt: base,
			},
		},
		// 4. Multi-level part_of ancestor chain inheritance.
		{
			Todo: Todo{
				ID:        "mid_undirected",
				PartOf:    "sup_parent",
				CreatedAt: base,
			},
		},
		{
			Todo: Todo{
				ID:        "leaf_inherited",
				PartOf:    "mid_undirected",
				CreatedAt: base,
			},
		},
		// 4b. Cycle safety: undirected cycle stays empty.
		{
			Todo: Todo{
				ID:        "cyc_empty_a",
				PartOf:    "cyc_empty_b",
				CreatedAt: base,
			},
		},
		{
			Todo: Todo{
				ID:        "cyc_empty_b",
				PartOf:    "cyc_empty_a",
				CreatedAt: base,
			},
		},
		// 4c. Cycle safety: directed cycle propagates without looping.
		{
			Todo: Todo{
				ID:          "cyc_dir_a",
				DirectionID: "dir_a",
				PartOf:      "cyc_dir_b",
				CreatedAt:   base,
			},
		},
		{
			Todo: Todo{
				ID:        "cyc_dir_b",
				PartOf:    "cyc_dir_a",
				CreatedAt: base,
			},
		},
		// 5a. Unanimous subtree child fallback for umbrella parent + propagation to undirected sibling.
		{
			Todo: Todo{
				ID:        "umbrella_unanimous",
				CreatedAt: base,
			},
		},
		{
			Todo: Todo{
				ID:          "umbrella_child_1",
				PartOf:      "umbrella_unanimous",
				Supervision: &Supervision{Supervisor: "dir_b"},
				CreatedAt:   base,
			},
		},
		{
			Todo: Todo{
				ID:        "umbrella_child_2",
				PartOf:    "umbrella_unanimous",
				Origin:    "dir_b",
				CreatedAt: base,
			},
		},
		{
			Todo: Todo{
				ID:        "umbrella_sibling_undirected",
				PartOf:    "umbrella_unanimous",
				CreatedAt: base,
			},
		},
		{
			Todo: Todo{
				ID:        "umbrella_grandchild_undirected",
				PartOf:    "umbrella_sibling_undirected",
				CreatedAt: base,
			},
		},
		// 5b. Multi-direction children leave umbrella parent and undirected siblings empty.
		{
			Todo: Todo{
				ID:        "umbrella_mixed",
				CreatedAt: base,
			},
		},
		{
			Todo: Todo{
				ID:          "mixed_child_a",
				PartOf:      "umbrella_mixed",
				DirectionID: "dir_a",
				CreatedAt:   base,
			},
		},
		{
			Todo: Todo{
				ID:          "mixed_child_b",
				PartOf:      "umbrella_mixed",
				DirectionID: "dir_b",
				CreatedAt:   base,
			},
		},
		{
			Todo: Todo{
				ID:        "mixed_sibling_undirected",
				PartOf:    "umbrella_mixed",
				CreatedAt: base,
			},
		},
		// 5c. Child with explicit direction NOT in directions map must not trigger umbrella fallback.
		{
			Todo: Todo{
				ID:        "umbrella_unknown_dir",
				CreatedAt: base,
			},
		},
		{
			Todo: Todo{
				ID:          "unknown_dir_child",
				PartOf:      "umbrella_unknown_dir",
				DirectionID: "dir_unlisted",
				CreatedAt:   base,
			},
		},
	}

	want := map[string]string{
		"explicit_wins":                  "dir_c",
		"sup_wins":                       "dir_a",
		"origin_wins":                    "dir_b",
		"sup_parent":                     "dir_a",
		"origin_parent":                  "dir_b",
		"mid_undirected":                 "dir_a",
		"leaf_inherited":                 "dir_a",
		"cyc_empty_a":                    "",
		"cyc_empty_b":                    "",
		"cyc_dir_a":                      "dir_a",
		"cyc_dir_b":                      "dir_a",
		"umbrella_unanimous":             "dir_b",
		"umbrella_child_1":               "dir_b",
		"umbrella_child_2":               "dir_b",
		"umbrella_sibling_undirected":    "dir_b",
		"umbrella_grandchild_undirected": "dir_b",
		"umbrella_mixed":                 "",
		"mixed_child_a":                  "dir_a",
		"mixed_child_b":                  "dir_b",
		"mixed_sibling_undirected":       "",
		"umbrella_unknown_dir":           "",
		"unknown_dir_child":              "dir_unlisted",
	}

	assertResolved := func(label string, got map[string]string) {
		t.Helper()
		for id, w := range want {
			if got[id] != w {
				t.Errorf("%s[%s] = %q, want %q", label, id, got[id], w)
			}
		}
		for id, g := range got {
			if _, ok := want[id]; !ok {
				t.Errorf("%s: unexpected key %s = %q", label, id, g)
			}
		}
	}

	s := &Service{}
	assertResolved("resolveEffectiveDirections", s.directionOwners(items).resolveEffectiveDirections(items, directions))

	// Verify order-independence by reversing the input slice.
	rev := slices.Clone(items)
	slices.Reverse(rev)
	assertResolved("resolveEffectiveDirections(reversed)", s.directionOwners(rev).resolveEffectiveDirections(rev, directions))
}

func TestTodosV2DetailInheritsDirectionOnlyFromVisibleAncestors(t *testing.T) {
	e := treeEnv(t)
	setBlockersTodosV2Flag(t, e.s, true, false)
	parent := e.task("Parent from a private space", func(todo *Todo) {
		todo.DirectionID = "conv_lead_a"
		todo.Agents = append(todo.Agents, TodoAgent{
			MachineID: "server", SessionID: "private", Cwd: identity.HealthDir("/home/owner"),
		})
	})
	child := e.task("Child outside the private space", func(todo *Todo) { todo.PartOf = parent })
	public := e.task("Visible parent", func(todo *Todo) { todo.DirectionID = "conv_lead_b" })
	publicChild := e.task("Visible child", func(todo *Todo) { todo.PartOf = public })

	read := func(role, id string) (int, listedTodo) {
		t.Helper()
		w := doTopologyHTTP(e.s, role, http.MethodGet, "/api/v1/todos/"+id, "")
		var payload struct{ Todo listedTodo }
		if err := json.Unmarshal(w.Body.Bytes(), &payload); err != nil {
			t.Fatal(err)
		}
		return w.Code, payload.Todo
	}
	if code, _ := read(roleExtension, parent); code != http.StatusNotFound {
		t.Fatalf("private parent status = %d", code)
	}
	if code, row := read("", child); code != http.StatusOK || row.EffectiveDirectionName != "Android parity" {
		t.Fatalf("owner detail: status=%d direction=%q", code, row.EffectiveDirectionName)
	}
	if code, row := read(roleExtension, child); code != http.StatusOK || row.EffectiveDirectionID != "" || row.EffectiveDirectionName != "" {
		t.Fatalf("extension detail: status=%d direction=%q (%q)", code, row.EffectiveDirectionID, row.EffectiveDirectionName)
	}
	if code, row := read(roleExtension, publicChild); code != http.StatusOK || row.EffectiveDirectionName != "Push relay" {
		t.Fatalf("extension detail with visible parent: status=%d direction=%q", code, row.EffectiveDirectionName)
	}
}

func TestTodosV2DirectionVisibilityHTTPListPaginatedAndSingle(t *testing.T) {
	e := treeEnv(t)
	s := e.s

	// Seed parent/child topologies across two known directions (conv_lead_a="Android parity", conv_lead_b="Push relay").
	explicitParentID := e.task("Explicit parent", func(todo *Todo) {
		todo.DirectionID = "conv_lead_a"
	})
	inheritedChildID := e.task("Inherited child", func(todo *Todo) {
		todo.PartOf = explicitParentID
	})

	umbrellaID := e.task("Umbrella goal", func(todo *Todo) {})
	umbrellaChildID := e.task("Umbrella directed child", func(todo *Todo) {
		todo.PartOf = umbrellaID
		todo.Supervision = &Supervision{Supervisor: "conv_lead_b"}
	})
	umbrellaSiblingID := e.task("Umbrella undirected sibling", func(todo *Todo) {
		todo.PartOf = umbrellaID
	})

	mixedParentID := e.task("Mixed umbrella", func(todo *Todo) {})
	mixedChildAID := e.task("Mixed child A", func(todo *Todo) {
		todo.PartOf = mixedParentID
		todo.DirectionID = "conv_lead_a"
	})
	mixedChildBID := e.task("Mixed child B", func(todo *Todo) {
		todo.PartOf = mixedParentID
		todo.DirectionID = "conv_lead_b"
	})

	standaloneID := e.task("Standalone undirected", func(todo *Todo) {})

	// Helper to assert that raw JSON objects have no effective_direction_* keys when todos_v2=false.
	assertNoEffectiveDirectionKeys := func(label string, rawItems []json.RawMessage) {
		t.Helper()
		for _, raw := range rawItems {
			var obj map[string]json.RawMessage
			if err := json.Unmarshal(raw, &obj); err != nil {
				t.Fatalf("%s: decode item: %v", label, err)
			}
			if _, has := obj["effective_direction_id"]; has {
				t.Fatalf("%s: unexpected effective_direction_id in %s", label, string(raw))
			}
			if _, has := obj["effective_direction_name"]; has {
				t.Fatalf("%s: unexpected effective_direction_name in %s", label, string(raw))
			}
		}
	}

	// 1. todos_v2=false: full list, paginated list, and single todo must omit effective_direction_*.
	offList := doTodoHTTP(s, http.MethodGet, "/api/v1/todos", "")
	if offList.Code != http.StatusOK {
		t.Fatalf("GET /api/v1/todos (off): %d %s", offList.Code, offList.Body.String())
	}
	var offListPayload struct {
		Todos []json.RawMessage `json:"todos"`
	}
	if err := json.Unmarshal(offList.Body.Bytes(), &offListPayload); err != nil {
		t.Fatalf("decode off list: %v", err)
	}
	assertNoEffectiveDirectionKeys("todos_v2=false full list", offListPayload.Todos)

	offPaged := doTodoHTTP(s, http.MethodGet, "/api/v1/todos?limit=3", "")
	if offPaged.Code != http.StatusOK {
		t.Fatalf("GET /api/v1/todos?limit=3 (off): %d %s", offPaged.Code, offPaged.Body.String())
	}
	var offPagedPayload struct {
		Todos []json.RawMessage `json:"todos"`
	}
	if err := json.Unmarshal(offPaged.Body.Bytes(), &offPagedPayload); err != nil {
		t.Fatalf("decode off paged: %v", err)
	}
	assertNoEffectiveDirectionKeys("todos_v2=false paged", offPagedPayload.Todos)

	offSingle := doTodoHTTP(s, http.MethodGet, "/api/v1/todos/"+inheritedChildID, "")
	if offSingle.Code != http.StatusOK {
		t.Fatalf("GET /api/v1/todos/%s (off): %d %s", inheritedChildID, offSingle.Code, offSingle.Body.String())
	}
	var offSinglePayload struct {
		Todo json.RawMessage `json:"todo"`
	}
	if err := json.Unmarshal(offSingle.Body.Bytes(), &offSinglePayload); err != nil {
		t.Fatalf("decode off single: %v", err)
	}
	assertNoEffectiveDirectionKeys("todos_v2=false single", []json.RawMessage{offSinglePayload.Todo})

	// 2. Enable todos_v2=true.
	on := true
	setSettings(t, s, func(v *Settings) { v.TodosV2 = &on })

	type wantDir struct {
		id   string
		name string
	}
	wantByID := map[string]wantDir{
		explicitParentID:  {id: "conv_lead_a", name: "Android parity"},
		inheritedChildID:  {id: "conv_lead_a", name: "Android parity"},
		umbrellaID:        {id: "conv_lead_b", name: "Push relay"},
		umbrellaChildID:   {id: "conv_lead_b", name: "Push relay"},
		umbrellaSiblingID: {id: "conv_lead_b", name: "Push relay"},
		mixedParentID:     {id: "", name: ""},
		mixedChildAID:     {id: "conv_lead_a", name: "Android parity"},
		mixedChildBID:     {id: "conv_lead_b", name: "Push relay"},
		standaloneID:      {id: "", name: ""},
	}

	// 2a. Full list + board.direction_groups agreement.
	onList := doTodoHTTP(s, http.MethodGet, "/api/v1/todos", "")
	if onList.Code != http.StatusOK {
		t.Fatalf("GET /api/v1/todos (on): %d %s", onList.Code, onList.Body.String())
	}
	var onListPayload struct {
		Todos []listedTodo         `json:"todos"`
		Board *todoBoardProjection `json:"board"`
	}
	if err := json.Unmarshal(onList.Body.Bytes(), &onListPayload); err != nil {
		t.Fatalf("decode on list: %v", err)
	}
	if onListPayload.Board == nil {
		t.Fatal("todos_v2=true expected board in GET /api/v1/todos")
	}
	byID := make(map[string]listedTodo, len(onListPayload.Todos))
	for _, row := range onListPayload.Todos {
		byID[row.ID] = row
	}
	for id, want := range wantByID {
		row, ok := byID[id]
		if !ok {
			t.Fatalf("missing todo %s in GET /api/v1/todos", id)
		}
		if row.EffectiveDirectionID != want.id || row.EffectiveDirectionName != want.name {
			t.Errorf("GET /api/v1/todos[%s]: got (%q, %q), want (%q, %q)",
				id, row.EffectiveDirectionID, row.EffectiveDirectionName, want.id, want.name)
		}
	}
	// Verify board.direction_groups agrees with listedTodo.EffectiveDirectionID for every todo.
	boardDirOf := make(map[string]string)
	for _, g := range onListPayload.Board.DirectionGroups {
		for _, tid := range g.TodoIDs {
			boardDirOf[tid] = g.DirectionID
		}
	}
	for id, want := range wantByID {
		if boardDirOf[id] != want.id {
			t.Errorf("board.direction_groups[%s] = %q, want %q", id, boardDirOf[id], want.id)
		}
	}

	// 2b. Bounded closed list (?closed_limit=1) preserves effective_direction_* on returned items.
	onBounded := doTodoHTTP(s, http.MethodGet, "/api/v1/todos?closed_limit=1", "")
	if onBounded.Code != http.StatusOK {
		t.Fatalf("GET /api/v1/todos?closed_limit=1: %d %s", onBounded.Code, onBounded.Body.String())
	}
	var onBoundedPayload struct {
		Todos []listedTodo `json:"todos"`
	}
	if err := json.Unmarshal(onBounded.Body.Bytes(), &onBoundedPayload); err != nil {
		t.Fatalf("decode bounded: %v", err)
	}
	for _, row := range onBoundedPayload.Todos {
		if want, ok := wantByID[row.ID]; ok {
			if row.EffectiveDirectionID != want.id || row.EffectiveDirectionName != want.name {
				t.Errorf("bounded[%s]: got (%q, %q), want (%q, %q)",
					row.ID, row.EffectiveDirectionID, row.EffectiveDirectionName, want.id, want.name)
			}
		}
	}

	// 2c. Paginated list (?limit=2 + cursor walk) preserves effective_direction_* across pages.
	seenPaged := make(map[string]listedTodo)
	cursor := ""
	for page := 0; page < 10; page++ {
		path := "/api/v1/todos?limit=2"
		if cursor != "" {
			path += "&cursor=" + cursor
		}
		resp := doTodoHTTP(s, http.MethodGet, path, "")
		if resp.Code != http.StatusOK {
			t.Fatalf("GET %s: %d %s", path, resp.Code, resp.Body.String())
		}
		var pagePayload struct {
			Todos      []listedTodo `json:"todos"`
			NextCursor string       `json:"next_cursor"`
			HasMore    bool         `json:"has_more"`
		}
		if err := json.Unmarshal(resp.Body.Bytes(), &pagePayload); err != nil {
			t.Fatalf("decode page %d: %v", page, err)
		}
		for _, row := range pagePayload.Todos {
			seenPaged[row.ID] = row
		}
		if !pagePayload.HasMore {
			break
		}
		cursor = pagePayload.NextCursor
	}
	for id, want := range wantByID {
		row, ok := seenPaged[id]
		if !ok {
			t.Fatalf("paginated walk missing todo %s", id)
		}
		if row.EffectiveDirectionID != want.id || row.EffectiveDirectionName != want.name {
			t.Errorf("paginated[%s]: got (%q, %q), want (%q, %q)",
				id, row.EffectiveDirectionID, row.EffectiveDirectionName, want.id, want.name)
		}
	}

	// 2d. Single-item GET /api/v1/todos/{id} resolves against the full snapshot (inheritance + umbrella fallback).
	for _, id := range []string{inheritedChildID, umbrellaID, umbrellaSiblingID, mixedParentID, standaloneID} {
		resp := doTodoHTTP(s, http.MethodGet, "/api/v1/todos/"+id, "")
		if resp.Code != http.StatusOK {
			t.Fatalf("GET /api/v1/todos/%s: %d %s", id, resp.Code, resp.Body.String())
		}
		var singlePayload struct {
			Todo listedTodo `json:"todo"`
		}
		if err := json.Unmarshal(resp.Body.Bytes(), &singlePayload); err != nil {
			t.Fatalf("decode single %s: %v", id, err)
		}
		want := wantByID[id]
		if singlePayload.Todo.EffectiveDirectionID != want.id || singlePayload.Todo.EffectiveDirectionName != want.name {
			t.Errorf("GET /api/v1/todos/%s: got (%q, %q), want (%q, %q)",
				id, singlePayload.Todo.EffectiveDirectionID, singlePayload.Todo.EffectiveDirectionName, want.id, want.name)
		}
	}

	// 3. Rollback todos_v2=false: single and list omit effective_direction_* again.
	off := false
	setSettings(t, s, func(v *Settings) { v.TodosV2 = &off })
	rbSingle := doTodoHTTP(s, http.MethodGet, "/api/v1/todos/"+inheritedChildID, "")
	if rbSingle.Code != http.StatusOK {
		t.Fatalf("rollback GET /api/v1/todos/%s: %d %s", inheritedChildID, rbSingle.Code, rbSingle.Body.String())
	}
	var rbSinglePayload struct {
		Todo json.RawMessage `json:"todo"`
	}
	if err := json.Unmarshal(rbSingle.Body.Bytes(), &rbSinglePayload); err != nil {
		t.Fatalf("decode rollback single: %v", err)
	}
	assertNoEffectiveDirectionKeys("todos_v2 rollback single", []json.RawMessage{rbSinglePayload.Todo})
}

func TestTodosV2DirectionVisibilityTodoCreateAutoPopulate(t *testing.T) {
	ctx := context.Background()
	e := treeEnv(t)
	s := e.s

	parentWithDirID := e.task("Parent with direction", func(todo *Todo) {
		todo.DirectionID = "conv_lead_b"
	})

	callCreate := func(caller string, payload map[string]any) Todo {
		t.Helper()
		raw, err := json.Marshal(payload)
		if err != nil {
			t.Fatalf("marshal args: %v", err)
		}
		out, err := s.toolTodo(ctx, "mac", caller, "todo_create", raw)
		if err != nil {
			t.Fatalf("todo_create(caller=%s, payload=%s): %v", caller, string(raw), err)
		}
		title, _ := payload["title"].(string)
		created, ok := todoByTitle(s, title)
		if !ok {
			t.Fatalf("created todo %q not found (output=%s)", title, out)
		}
		return created
	}

	// 1. todos_v2=false: leader and part_of creates leave direction_id empty (legacy unchanged).
	legacyLeader := callCreate("conv_lead_a", map[string]any{"title": "Legacy leader create"})
	if legacyLeader.DirectionID != "" {
		t.Fatalf("todos_v2=false leader todo_create: direction_id = %q, want empty", legacyLeader.DirectionID)
	}
	legacyPiece := callCreate("conv_main", map[string]any{"title": "Legacy piece create", "part_of": parentWithDirID})
	if legacyPiece.DirectionID != "" {
		t.Fatalf("todos_v2=false part_of todo_create: direction_id = %q, want empty", legacyPiece.DirectionID)
	}

	// 2. Enable todos_v2=true.
	on := true
	setSettings(t, s, func(v *Settings) { v.TodosV2 = &on })

	// 2a. Leader without direction_id defaults to caller conversation ID.
	v2Leader := callCreate("conv_lead_a", map[string]any{"title": "V2 leader default"})
	if v2Leader.DirectionID != "conv_lead_a" {
		t.Fatalf("todos_v2=true leader todo_create: direction_id = %q, want %q", v2Leader.DirectionID, "conv_lead_a")
	}

	// 2b. Leader with explicit direction_id keeps the explicit value.
	v2LeaderExplicit := callCreate("conv_lead_a", map[string]any{"title": "V2 leader explicit", "direction_id": "conv_lead_b"})
	if v2LeaderExplicit.DirectionID != "conv_lead_b" {
		t.Fatalf("todos_v2=true leader explicit direction_id = %q, want %q", v2LeaderExplicit.DirectionID, "conv_lead_b")
	}

	// 2c. Non-leader with part_of inherits parent's direction_id.
	v2PieceInherit := callCreate("conv_main", map[string]any{"title": "V2 piece inherit", "part_of": parentWithDirID})
	if v2PieceInherit.DirectionID != "conv_lead_b" {
		t.Fatalf("todos_v2=true part_of todo_create: direction_id = %q, want %q", v2PieceInherit.DirectionID, "conv_lead_b")
	}

	// 2d. Non-leader with part_of AND explicit direction_id keeps explicit value.
	v2PieceExplicit := callCreate("conv_main", map[string]any{"title": "V2 piece explicit", "part_of": parentWithDirID, "direction_id": "conv_lead_a"})
	if v2PieceExplicit.DirectionID != "conv_lead_a" {
		t.Fatalf("todos_v2=true part_of explicit direction_id = %q, want %q", v2PieceExplicit.DirectionID, "conv_lead_a")
	}

	// 2e. Non-leader without part_of or direction_id leaves direction_id empty.
	v2Plain := callCreate("conv_main", map[string]any{"title": "V2 main plain"})
	if v2Plain.DirectionID != "" {
		t.Fatalf("todos_v2=true main plain todo_create: direction_id = %q, want empty", v2Plain.DirectionID)
	}
}
