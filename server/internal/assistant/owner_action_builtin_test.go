package assistant

import (
	"context"
	"encoding/json"
	"errors"
	"reflect"
	"strings"
	"testing"
	"time"

	"github.com/nwverse/persona/server/internal/proto"
)

// ownerBuiltinTestStore adds the same revision-checked replacement primitive as
// Postgres without changing the older model fixtures' fallback write path.
type ownerBuiltinTestStore struct {
	*downStore
	afterPutSettings func(map[string]any)
}

func (st *ownerBuiltinTestStore) PutSettings(ctx context.Context, values map[string]any) error {
	if st.down() {
		return errors.New("database unavailable")
	}
	if err := st.downStore.PutSettings(ctx, values); err != nil {
		return err
	}
	if st.afterPutSettings != nil {
		hook := st.afterPutSettings
		st.afterPutSettings = nil
		hook(values)
	}
	return nil
}

func (st *ownerBuiltinTestStore) CompareSetting(_ context.Context, key string, revision int64, value any) (bool, error) {
	if st.down() {
		return false, errors.New("database unavailable")
	}
	raw, err := json.Marshal(value)
	if err != nil {
		return false, err
	}
	st.memStore.mu.Lock()
	defer st.memStore.mu.Unlock()
	var current struct {
		Revision int64 `json:"revision"`
	}
	if len(st.memStore.vals[key]) > 0 {
		if err := json.Unmarshal(st.memStore.vals[key], &current); err != nil {
			return false, err
		}
	}
	if current.Revision != revision {
		return false, nil
	}
	if st.memStore.vals == nil {
		st.memStore.vals = map[string][]byte{}
	}
	st.memStore.vals[key] = raw
	st.memStore.puts++
	return true, nil
}

func ownerBuiltinService(t *testing.T) (*Service, *fakeHub, *downStore, *ownerActionBook) {
	t.Helper()
	s, f, st := managedProfileService(t)
	cas := &ownerBuiltinTestStore{downStore: st}
	s.setStore(func() Store { return cas })
	on := true
	settings := &Settings{MachineID: "server", Main: "main", OwnerReplyApproval: &on, PersistentOwnerActionCards: &on}
	f.settings = settings
	f.zshrc = map[string][]proto.Launcher{"server": {{Name: "gw", Model: "live-old"}}}
	if err := st.PutSetting(context.Background(), settingsKey, settings); err != nil {
		t.Fatal(err)
	}
	book, ok := newOwnerActionBook(cas)
	if !ok {
		t.Fatal("owner built-in test store needs CAS")
	}
	return s, f, st, book
}

type ownerBuiltinPrepareResult struct {
	text string
	err  error
}

func prepareOwnerBuiltins(t *testing.T, s *Service, book *ownerActionBook, items []map[string]any, allow bool) (ownerActionBatch, string, []proto.Interaction) {
	t.Helper()
	args := managedJSON(t, map[string]any{"items": items})
	done := make(chan ownerBuiltinPrepareResult, 1)
	go func() {
		text, err := s.toolOwnerActionPrepare(context.Background(), "server", "main", json.RawMessage(args))
		done <- ownerBuiltinPrepareResult{text: text, err: err}
	}()
	var cards []proto.Interaction
	var early *ownerBuiltinPrepareResult
	ownerActionPoll(t, 3*time.Second, func() bool {
		select {
		case result := <-done:
			early = &result
			return true
		default:
		}
		cards = s.pendingInteractions()
		return len(cards) >= len(items)+1
	})
	if early != nil {
		t.Fatalf("built-in preparation failed before its cards: %v (%s)", early.err, early.text)
	}
	if len(cards) < len(items)+1 {
		t.Fatalf("pending built-in cards = %+v, want %d", cards, len(items)+1)
	}
	var batchCard proto.Interaction
	for _, card := range cards {
		if card.ToolName == ownerActionBatchTool {
			batchCard = card
			break
		}
	}
	if batchCard.ID == "" {
		t.Fatalf("no built-in batch card: %+v", cards)
	}
	if err := s.resolveApproved(context.Background(), batchCard.ID, proto.Resolution{Allow: &allow}, nil); err != nil {
		t.Fatal(err)
	}
	select {
	case result := <-done:
		if result.err != nil {
			t.Fatal(result.err)
		}
		batch, found, err := book.batch(context.Background(), batchCard.ApprovalBatchID)
		if err != nil || !found {
			t.Fatalf("read approved built-in batch: found=%v err=%v", found, err)
		}
		return batch, result.text, cards
	case <-time.After(5 * time.Second):
		t.Fatal("built-in batch decision did not release owner_action_prepare")
		return ownerActionBatch{}, "", nil
	}
}

func callOwnerBuiltin(t *testing.T, s *Service, item ownerActionItem) proto.ToolResult {
	t.Helper()
	done := make(chan proto.ToolResult, 1)
	go func() {
		done <- s.Tool(context.Background(), ToolCall{Name: item.Tool, Arguments: json.RawMessage(item.Canonical), MachineID: "server", SessionID: "main"})
	}()
	select {
	case result := <-done:
		return result
	case <-time.After(5 * time.Second):
		t.Fatalf("built-in item %s opened an unexpected second approval or hung", item.ID)
		return proto.ToolResult{}
	}
}

func ownerBuiltinInput(tool string, input map[string]any) map[string]any {
	return map[string]any{"tool": tool, "input": input}
}

func ownerRevision(value int64) *int64 { return &value }

func TestOwnerBuiltinFiveItemsAdvanceCatalogAndSwitchRollback(t *testing.T) {
	s, _, st, book := ownerBuiltinService(t)
	ctx := context.Background()
	live := modelRecord{Revision: 3, Binding: ModelBinding{Provider: "launcher.gw", Model: "live-old"}}
	if err := st.PutSetting(ctx, modelRecordKey("agent.new"), live); err != nil {
		t.Fatal(err)
	}
	storeProfiles(t, st,
		ModelProfile{ID: "performance", Name: "Performance", Purposes: map[string]ModelBinding{"agent.new": {Provider: "launcher.gw", Model: "performance-old"}}},
		ModelProfile{ID: "economy", Name: "Economy", Purposes: map[string]ModelBinding{"agent.new": {Provider: "launcher.gw", Model: "economy-old"}}})
	if err := st.PutSetting(ctx, modelProfileStateKey, modelProfileState{Active: "economy"}); err != nil {
		t.Fatal(err)
	}
	items := []map[string]any{
		ownerBuiltinInput("persona_settings", map[string]any{"action": "set", "key": modelProfileBindingsKey, "value": map[string]any{
			"profile": "performance", "purposes": map[string]any{"agent.new": map[string]any{"provider": "launcher.gw", "model": "performance-batch"}}}}),
		ownerBuiltinInput("persona_settings", map[string]any{"action": "set", "key": modelProfileBindingsKey, "value": map[string]any{
			"profile": "economy", "purposes": map[string]any{"agent.new": map[string]any{"provider": "launcher.gw", "model": "economy-batch"}}}}),
		ownerBuiltinInput("persona_settings", map[string]any{"action": "set", "key": "model_profiles.tiers", "value": map[string]any{
			"tier": "batch", "profiles": []string{"performance"}, "launcher": "gw", "model": "tier-batch"}}),
		ownerBuiltinInput("persona_settings", map[string]any{"action": "set", "key": "assistant.limit_fallbacks", "value": []string{"gw"}}),
		ownerBuiltinInput("model_profile_switch", map[string]any{"id": "performance"}),
	}
	batch, instruction, cards := prepareOwnerBuiltins(t, s, book, items, true)
	if len(batch.Items) != 5 || !strings.Contains(instruction, "stop at the first error") {
		t.Fatalf("five-item approval result: %+v\n%s", batch, instruction)
	}
	var batchCard proto.Interaction
	for _, card := range cards {
		if card.ToolName == ownerActionBatchTool {
			batchCard = card
		}
	}
	for _, wanted := range []string{"performance-old", "performance-batch", "economy-old", "economy-batch", "tier-batch", "Item 5/5"} {
		if !strings.Contains(batchCard.Summary, wanted) {
			t.Errorf("batch card lacks %q:\n%s", wanted, batchCard.Summary)
		}
	}
	wantRevisions := []*int64{ownerRevision(1), ownerRevision(2), ownerRevision(3), nil, ownerRevision(4)}
	for i, item := range batch.Items {
		if item.Revision == nil != (wantRevisions[i] == nil) || item.Revision != nil && *item.Revision != *wantRevisions[i] {
			t.Errorf("item %d revision = %v, want %v", i+1, item.Revision, wantRevisions[i])
		}
		if item.Tool == "model_profile_switch" {
			duplicate := item
			duplicate.Canonical = `{"id":"economy","id":"performance"}`
			if result := callOwnerBuiltin(t, s, duplicate); !result.IsError || !strings.Contains(result.Content[0].Text, "duplicate key") {
				t.Fatalf("duplicate-key spelling bypassed the exact switch grant: %+v", result)
			}
		}
		result := callOwnerBuiltin(t, s, item)
		if result.IsError || !strings.Contains(managedToolText(t, result), "completed") {
			t.Fatalf("item %d execution: %+v", i+1, result)
		}
		if pending := s.pendingInteractions(); len(pending) != 0 {
			t.Fatalf("item %d opened another card: %+v", i+1, pending)
		}
	}
	view := profilesOf(t, s)
	if view.Revision != 4 || view.Active != "performance" {
		t.Fatalf("catalog after five items: revision=%d active=%s", view.Revision, view.Active)
	}
	performance := managedProfile(t, s, "performance")
	economy := managedProfile(t, s, "economy")
	if performance.Purposes["agent.new"].Model != "performance-batch" || economy.Purposes["agent.new"].Model != "economy-batch" ||
		performance.Tiers["batch"].Model != "tier-batch" {
		t.Fatalf("planned sequential edits were not saved: performance=%+v economy=%+v", performance, economy)
	}
	if current, err := s.modelRecord(ctx, "agent.new"); err != nil || current.Binding.Model != "performance-batch" {
		t.Fatalf("switch did not apply the profile edited earlier in the same batch: %+v %v", current, err)
	}
	if got := limitFallbacksOf(s.settings(ctx)); !reflect.DeepEqual(got, []string{"gw"}) {
		t.Fatalf("ordinary setting in the batch = %v", got)
	}
	stored, _, err := book.batch(ctx, batch.ID)
	if err != nil {
		t.Fatal(err)
	}
	for i, item := range stored.Items {
		if item.State != ownerActionConsumed || item.Outcome != "done" {
			t.Errorf("item %d audit = %s/%s", i+1, item.State, item.Outcome)
		}
	}
	state, err := s.readModelProfileState(ctx)
	if err != nil || len(state.Events) != 4 {
		t.Fatalf("per-item model history: %+v %v", state.Events, err)
	}
	for i, action := range []string{"save", "save", "save", "switch"} {
		if state.Events[i].Action != action || len(state.Events[i].Changed) == 0 {
			t.Errorf("event %d = %+v", i, state.Events[i])
		}
	}

	rollback, _, rollbackCards := prepareOwnerBuiltins(t, s, book, []map[string]any{ownerBuiltinInput("model_profile_rollback", map[string]any{})}, true)
	if len(rollback.Items) != 1 || rollback.Items[0].Revision == nil || *rollback.Items[0].Revision != 4 {
		t.Fatalf("rollback approval revision: %+v", rollback.Items)
	}
	if !strings.Contains(rollbackCards[0].Summary, "performance-batch") {
		t.Fatalf("rollback card lacks its exact before image: %+v", rollbackCards)
	}
	if result := callOwnerBuiltin(t, s, rollback.Items[0]); result.IsError {
		t.Fatalf("rollback: %+v", result)
	}
	if current, err := s.modelRecord(ctx, "agent.new"); err != nil || current.Binding.Model != "live-old" {
		t.Fatalf("rollback did not restore the exact before value: %+v %v", current, err)
	}
}

func TestOwnerBuiltinExternalCatalogWriteStopsAtMiddleItem(t *testing.T) {
	s, _, st, book := ownerBuiltinService(t)
	storeProfiles(t, st,
		ModelProfile{ID: "performance", Name: "Performance"},
		ModelProfile{ID: "economy", Name: "Economy"})
	items := []map[string]any{
		ownerBuiltinInput("persona_settings", map[string]any{"action": "set", "key": modelProfileBindingsKey, "value": map[string]any{
			"profile": "performance", "purposes": map[string]any{"agent.new": map[string]any{"provider": "launcher.gw", "model": "batch-one"}}}}),
		ownerBuiltinInput("persona_settings", map[string]any{"action": "set", "key": modelProfileBindingsKey, "value": map[string]any{
			"profile": "economy", "purposes": map[string]any{"agent.new": map[string]any{"provider": "launcher.gw", "model": "batch-two"}}}}),
		ownerBuiltinInput("persona_settings", map[string]any{"action": "set", "key": "model_profiles.tiers", "value": map[string]any{
			"tier": "later", "profiles": []string{"performance"}, "launcher": "gw", "model": "later-model"}}),
	}
	batch, _, _ := prepareOwnerBuiltins(t, s, book, items, true)
	if result := callOwnerBuiltin(t, s, batch.Items[0]); result.IsError {
		t.Fatalf("first item: %+v", result)
	}
	// This is not the batch's own write. It advances the catalog while item two's
	// exact grant is waiting, so item two and every later grant must stop.
	if _, err := s.saveModelProfile(context.Background(), nil, "economy", ProfileActor{Kind: "device"}, func(p *ModelProfile, _ bool) error {
		p.Description = "external-edit"
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	failed := callOwnerBuiltin(t, s, batch.Items[1])
	if !failed.IsError {
		t.Fatalf("external write was silently rebased: %+v", failed)
	}
	text := failed.Content[0].Text
	for _, wanted := range []string{"item 2/3", "Completed items: [1]", "Items not executed: [3]"} {
		if !strings.Contains(text, wanted) {
			t.Errorf("partial failure lacks %q: %s", wanted, text)
		}
	}
	if managedProfile(t, s, "performance").Purposes["agent.new"].Model != "batch-one" ||
		managedProfile(t, s, "economy").Description != "external-edit" || len(managedProfile(t, s, "economy").Purposes) != 0 {
		t.Fatalf("partial result has wrong saved values")
	}
	if _, exists := managedProfile(t, s, "performance").Tiers["later"]; exists {
		t.Fatal("later item executed after the middle conflict")
	}
	if result := callOwnerBuiltin(t, s, batch.Items[2]); !result.IsError || !strings.Contains(result.Content[0].Text, "stopped") {
		t.Fatalf("stopped later item bypassed the batch failure: %+v", result)
	}
	stored, _, err := book.batch(context.Background(), batch.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.FailedItem != batch.Items[1].ID || stored.Items[0].Outcome != "done" || stored.Items[1].Outcome != "failed" ||
		stored.Items[2].State != ownerActionStopped || stored.Items[2].Outcome != "stopped" {
		t.Fatalf("partial execution audit: %+v", stored)
	}
}

func TestOwnerBuiltinDenialValidationAndRedaction(t *testing.T) {
	s, _, st, book := ownerBuiltinService(t)
	storeProfiles(t, st, ModelProfile{ID: "performance", Name: "Performance", Purposes: map[string]ModelBinding{
		"agent.new": {Provider: "launcher.gw", Model: "old"}}})
	before := managedBusinessSnapshot(t, s, st)
	items := []map[string]any{
		ownerBuiltinInput("persona_settings", map[string]any{"action": "set", "key": modelProfileBindingsKey, "value": map[string]any{
			"profile": "performance", "purposes": map[string]any{"agent.new": map[string]any{"provider": "launcher.gw", "model": "denied"}}}}),
		ownerBuiltinInput("model_profile_switch", map[string]any{"id": "performance"}),
	}
	batch, text, _ := prepareOwnerBuiltins(t, s, book, items, false)
	if !strings.Contains(text, "denied") || !reflect.DeepEqual(before, managedBusinessSnapshot(t, s, st)) {
		t.Fatalf("denial changed model state: %s", text)
	}
	for _, item := range batch.Items {
		if item.State != ownerActionDenied || item.ConsumedAt != nil {
			t.Errorf("denied item = %+v", item)
		}
		if result := callOwnerBuiltin(t, s, item); !result.IsError || !strings.Contains(result.Content[0].Text, "denied") {
			t.Errorf("denied exact call bypassed its decision: %+v", result)
		}
	}
	duplicate := batch.Items[1]
	duplicate.Canonical = `{"id":"economy","id":"performance"}`
	if result := callOwnerBuiltin(t, s, duplicate); !result.IsError || !strings.Contains(result.Content[0].Text, "duplicate key") {
		t.Fatalf("duplicate-key spelling bypassed denial: %+v", result)
	}
	if !reflect.DeepEqual(before, managedBusinessSnapshot(t, s, st)) {
		t.Fatal("direct calls after denial changed model state")
	}
	for _, item := range []map[string]any{
		ownerBuiltinInput("persona_settings", map[string]any{"action": "get", "key": modelProfileBindingsKey}),
		ownerBuiltinInput("model_profiles_list", map[string]any{}),
		ownerBuiltinInput("persona_settings", map[string]any{"action": "set", "key": "not_a_setting", "value": true}),
	} {
		if _, err := s.toolOwnerActionPrepare(context.Background(), "server", "main", json.RawMessage(managedJSON(t, map[string]any{"items": []map[string]any{item}}))); err == nil {
			t.Errorf("unsupported/read-only item was accepted: %+v", item)
		}
	}
	display, err := ownerActionItemDisplay(ownerActionItem{Tool: "persona_settings", Canonical: `{"action":"set","key":"assistant.notes","value":{"api_key":"raw-secret","note":"visible"}}`, Display: "before → after"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(display, "raw-secret") || !strings.Contains(display, `"api_key": "***"`) || !strings.Contains(display, "before → after") {
		t.Fatalf("built-in display did not retain RedactArgs: %s", display)
	}
}

func TestOwnerBuiltinValidationFailureAfterGrantStopsBatch(t *testing.T) {
	s, _, st, book := ownerBuiltinService(t)
	storeProfiles(t, st, ModelProfile{ID: "performance", Name: "Performance"}, ModelProfile{ID: "economy", Name: "Economy"})
	items := []map[string]any{
		ownerBuiltinInput("persona_settings", map[string]any{"action": "set", "key": modelProfileBindingsKey, "value": map[string]any{
			"profile": "performance", "purposes": map[string]any{"agent.new": map[string]any{"provider": "launcher.gw", "model": "batch-one"}}}}),
		ownerBuiltinInput("persona_settings", map[string]any{"action": "set", "key": modelProfileBindingsKey, "value": map[string]any{
			"profile": "economy", "purposes": map[string]any{"agent.new": map[string]any{"provider": "launcher.gw", "model": "batch-two"}}}}),
	}
	batch, _, _ := prepareOwnerBuiltins(t, s, book, items, true)
	if err := s.deleteModelProfile(context.Background(), nil, "performance", ProfileActor{Kind: "device"}); err != nil {
		t.Fatal(err)
	}
	result := callOwnerBuiltin(t, s, batch.Items[0])
	if !result.IsError || !strings.Contains(result.Content[0].Text, "item 1/2") || !strings.Contains(result.Content[0].Text, "Items not executed: [2]") {
		t.Fatalf("pre-apply validation failure did not record the stopping point: %+v", result)
	}
	stored, _, err := book.batch(context.Background(), batch.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.FailedItem != batch.Items[0].ID || stored.Items[0].State != ownerActionConsumed || stored.Items[0].Outcome != "failed" ||
		stored.Items[1].State != ownerActionStopped || stored.Items[1].Outcome != "stopped" {
		t.Fatalf("validation failure audit: %+v", stored)
	}
}

func TestOwnerBuiltinCommittedSaveWithReadbackConflictIsCompleted(t *testing.T) {
	s, _, st, book := ownerBuiltinService(t)
	storeProfiles(t, st, ModelProfile{ID: "performance", Name: "Performance"}, ModelProfile{ID: "economy", Name: "Economy"})
	items := []map[string]any{
		ownerBuiltinInput("persona_settings", map[string]any{"action": "set", "key": modelProfileBindingsKey, "value": map[string]any{
			"profile": "performance", "purposes": map[string]any{"agent.new": map[string]any{"provider": "launcher.gw", "model": "batch-one"}}}}),
		ownerBuiltinInput("persona_settings", map[string]any{"action": "set", "key": modelProfileBindingsKey, "value": map[string]any{
			"profile": "economy", "purposes": map[string]any{"agent.new": map[string]any{"provider": "launcher.gw", "model": "batch-two"}}}}),
	}
	batch, _, _ := prepareOwnerBuiltins(t, s, book, items, true)
	var hookErr error
	s.store().(*ownerBuiltinTestStore).afterPutSettings = func(values map[string]any) {
		if _, ok := values[modelProfilesKey]; !ok {
			return
		}
		var ps modelProfiles
		if _, err := st.Setting(context.Background(), modelProfilesKey, &ps); err != nil {
			hookErr = err
			return
		}
		// Simulate an independent writer after the atomic save and before its
		// read-back. The approved purposes stay committed; a new field differs.
		ps.Profiles[0].Description = "external-after-save"
		ps.Revision++
		hookErr = st.memStore.PutSetting(context.Background(), modelProfilesKey, ps)
	}
	result := callOwnerBuiltin(t, s, batch.Items[0])
	if hookErr != nil {
		t.Fatal(hookErr)
	}
	if !result.IsError || !strings.Contains(result.Content[0].Text, "Completed items: [1]") ||
		!strings.Contains(result.Content[0].Text, "Items not executed: [2]") || !strings.Contains(result.Content[0].Text, "stopping item completed") {
		t.Fatalf("committed save/read-back conflict was misreported: %+v", result)
	}
	performance := managedProfile(t, s, "performance")
	if performance.Purposes["agent.new"].Model != "batch-one" || performance.Description != "external-after-save" {
		t.Fatalf("committed value or external read-back edit was lost: %+v", performance)
	}
	stored, _, err := book.batch(context.Background(), batch.ID)
	if err != nil {
		t.Fatal(err)
	}
	if stored.FailedItem != batch.Items[0].ID || stored.Items[0].Outcome != "done" || stored.Items[1].State != ownerActionStopped {
		t.Fatalf("completed/read-back failure audit: %+v", stored)
	}
}

func TestOwnerBuiltinRejectsEditingAnEmptyProfileCatalog(t *testing.T) {
	s, _, st, _ := ownerBuiltinService(t)
	items := []map[string]any{ownerBuiltinInput("persona_settings", map[string]any{"action": "set", "key": modelProfileBindingsKey, "value": map[string]any{
		"profile": "performance", "purposes": map[string]any{"agent.new": map[string]any{"provider": "launcher.gw", "model": "first-save"}}}})}
	args := json.RawMessage(managedJSON(t, map[string]any{"items": items}))
	before, puts := profileStoreSnapshot(st.memStore)
	if _, err := s.toolOwnerActionPrepare(context.Background(), "server", "main", args); !errors.Is(err, errModelProfileEmpty) {
		t.Fatalf("empty catalog approval: %v", err)
	}
	requireProfileStoreUnchanged(t, st.memStore, before, puts)
	if len(s.pendingInteractions()) != 0 {
		t.Fatal("an edit of the empty catalog opened a confirmation card")
	}
}

func TestOwnerBuiltinOrphanTaskClearOnly(t *testing.T) {
	s, _, st, book := ownerBuiltinService(t)
	setTasks(s)
	key := modelKey("task.gone")
	record := modelRecord{Revision: 7, Binding: ModelBinding{Provider: "launcher.gw", Model: "orphan-old", Effort: "low"}}
	if err := st.PutSetting(context.Background(), modelRecordKey("task.gone"), record); err != nil {
		t.Fatal(err)
	}
	batch, _, cards := prepareOwnerBuiltins(t, s, book, []map[string]any{
		ownerBuiltinInput("persona_settings", map[string]any{"action": "set", "key": key, "value": nil}),
	}, true)
	if len(cards) != 2 || !strings.Contains(cards[0].Summary+cards[1].Summary, "orphan-old") {
		t.Fatalf("orphan clear card lacks exact before image: %+v", cards)
	}
	if result := callOwnerBuiltin(t, s, batch.Items[0]); result.IsError {
		t.Fatalf("orphan clear: %+v", result)
	}
	var cleared modelRecord
	if found, err := st.Setting(context.Background(), modelRecordKey("task.gone"), &cleared); err != nil || !found ||
		cleared.Revision != 8 || cleared.Binding != (ModelBinding{}) {
		t.Fatalf("orphan override after approved clear: found=%v record=%+v err=%v", found, cleared, err)
	}

	if err := st.PutSetting(context.Background(), modelRecordKey("task.other-gone"), modelRecord{Revision: 4,
		Binding: ModelBinding{Provider: "launcher.gw", Model: "still-present"}}); err != nil {
		t.Fatal(err)
	}
	before := managedBusinessSnapshot(t, s, st)
	result := callOwnerBuiltin(t, s, ownerActionItem{Tool: "persona_settings", Canonical: managedJSON(t, map[string]any{
		"action": "set", "key": modelKey("task.other-gone"), "value": "launcher.gw"})})
	if !result.IsError || !strings.Contains(result.Content[0].Text, "only value null") {
		t.Fatalf("nonexistent task accepted a non-empty binding: %+v", result)
	}
	if !reflect.DeepEqual(before, managedBusinessSnapshot(t, s, st)) {
		t.Fatal("rejected non-empty orphan binding changed model state")
	}
}
