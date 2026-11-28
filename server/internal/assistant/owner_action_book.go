package assistant

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"sort"
	"strings"
	"time"
)

// Owner-action approvals are exact, one-shot capabilities for operations the
// hub itself prepared. They are deliberately separate from standing
// authorizations and from the detached command continuation book.
const (
	ownerActionBookKey       = "assistant.owner_action_approvals"
	ownerActionSchemaVersion = 1
	ownerActionPending       = "pending"
	ownerActionGranted       = "granted"
	ownerActionDenied        = "denied"
	ownerActionCancelled     = "cancelled"
	ownerActionConsumed      = "consumed"
	ownerActionStopped       = "stopped"

	ownerActionMaxItems    = 20
	ownerActionMaxInput    = 256 << 10
	ownerActionMaxBatch    = 1 << 20
	ownerActionMaxActive   = 128
	ownerActionTerminalFor = 30 * 24 * time.Hour
	ownerActionCASRetries  = 64
)

var (
	errOwnerActionProof    = errors.New("owner-action approval proof is not valid for this target")
	errOwnerActionConflict = errors.New("owner-action approvals changed concurrently; try again")
)

type ownerActionCAS interface {
	CompareSetting(context.Context, string, int64, any) (bool, error)
}

type ownerActionBook struct {
	store Store
	cas   ownerActionCAS
}

func newOwnerActionBook(store Store) (*ownerActionBook, bool) {
	cas, ok := store.(ownerActionCAS)
	if !ok || store == nil {
		return nil, false
	}
	return &ownerActionBook{store: store, cas: cas}, true
}

type ownerActionDocument struct {
	Revision  int64                          `json:"revision"`
	Version   int                            `json:"version"`
	Batches   []ownerActionBatch             `json:"batches,omitempty"`
	Decisions map[string]ownerActionDecision `json:"decisions,omitempty"`
}

type ownerActionBatch struct {
	ID         string            `json:"id"`
	Version    int               `json:"version"`
	MachineID  string            `json:"machine_id"`
	SessionID  string            `json:"session_id"`
	CreatedAt  time.Time         `json:"created_at"`
	Persistent bool              `json:"persistent,omitempty"`
	FailedItem string            `json:"failed_item,omitempty"`
	Failure    string            `json:"failure,omitempty"`
	Items      []ownerActionItem `json:"items"`
}

type ownerActionItem struct {
	ID            string     `json:"id"`
	Tool          string     `json:"tool"`
	Canonical     string     `json:"canonical"`
	Fingerprint   string     `json:"fingerprint"`
	State         string     `json:"state"`
	Display       string     `json:"display,omitempty"`
	Revision      *int64     `json:"revision,omitempty"`
	Outcome       string     `json:"outcome,omitempty"`
	DecisionEvent string     `json:"decision_event,omitempty"`
	DecidedAt     *time.Time `json:"decided_at,omitempty"`
	ConsumedAt    *time.Time `json:"consumed_at,omitempty"`
}

// ownerActionDecision binds one authenticated owner event to one target and
// answer. Reusing the event for another batch, item, or answer is not a second
// approval, even if an adapter accidentally hands the proof over twice.
type ownerActionDecision struct {
	BatchID string `json:"batch_id"`
	ItemID  string `json:"item_id,omitempty"`
	Allow   bool   `json:"allow"`
}

type ownerActionDraft struct {
	Tool     string
	Input    json.RawMessage
	Display  string
	Revision *int64
}

// ownerReplyProof contains only facts established independently by an
// authenticated channel adapter. Model text and request parameters cannot set
// these facts directly. ItemID empty means the batch; non-empty means exactly
// that item in BatchID.
type ownerReplyProof struct {
	AuthenticatedOwner bool
	DirectParent       bool
	ExactPhrase        bool
	EventID            string
	BatchID            string
	ItemID             string
}

type ownerActionDecisionResult struct {
	BatchID         string
	ItemIDs         []string // newly granted by this event
	ApprovedItemIDs []string // every granted or already consumed item in the batch
	Complete        bool
	Allow           bool
}

func (book *ownerActionBook) read(ctx context.Context) (ownerActionDocument, error) {
	doc := ownerActionDocument{Version: ownerActionSchemaVersion}
	if book == nil || book.store == nil || book.cas == nil {
		return doc, errors.New("owner-action approvals need a persistent revision-aware database")
	}
	if _, err := book.store.Setting(ctx, ownerActionBookKey, &doc); err != nil {
		return doc, err
	}
	if doc.Version == 0 {
		doc.Version = ownerActionSchemaVersion
	}
	if doc.Version != ownerActionSchemaVersion {
		return doc, fmt.Errorf("unsupported owner-action approval schema %d", doc.Version)
	}
	return doc, nil
}

// canonicalOwnerAction produces the bytes shown and approved. Object keys are
// sorted and insignificant whitespace is removed; strings, numbers, array order
// and every recipient/body field remain significant. Duplicate keys are
// rejected rather than silently choosing one of two possible payloads.
func canonicalOwnerAction(raw json.RawMessage) (json.RawMessage, error) {
	if len(raw) == 0 || len(raw) > ownerActionMaxInput {
		return nil, errors.New("owner-action input must contain 1 to 262144 bytes")
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	out, err := canonicalOwnerValue(dec, 0)
	if err != nil {
		return nil, err
	}
	if _, err := dec.Token(); !errors.Is(err, io.EOF) {
		return nil, errors.New("owner-action input must contain exactly one JSON value")
	}
	return out, nil
}

type ownerActionField struct {
	key   string
	value json.RawMessage
}

func canonicalOwnerValue(dec *json.Decoder, depth int) (json.RawMessage, error) {
	if depth > 100 {
		return nil, errors.New("owner-action input is nested too deeply")
	}
	token, err := dec.Token()
	if err != nil {
		return nil, err
	}
	delim, compound := token.(json.Delim)
	if !compound {
		raw, err := json.Marshal(token)
		return json.RawMessage(raw), err
	}
	switch delim {
	case '{':
		fields := []ownerActionField{}
		seen := map[string]bool{}
		for dec.More() {
			keyToken, err := dec.Token()
			if err != nil {
				return nil, err
			}
			key, ok := keyToken.(string)
			if !ok {
				return nil, errors.New("owner-action object key is not a string")
			}
			if seen[key] {
				return nil, fmt.Errorf("owner-action object contains duplicate key %q", key)
			}
			seen[key] = true
			value, err := canonicalOwnerValue(dec, depth+1)
			if err != nil {
				return nil, err
			}
			fields = append(fields, ownerActionField{key: key, value: value})
		}
		if end, err := dec.Token(); err != nil || end != json.Delim('}') {
			return nil, errors.New("owner-action object is not complete")
		}
		sort.Slice(fields, func(i, j int) bool { return fields[i].key < fields[j].key })
		var out bytes.Buffer
		out.WriteByte('{')
		for i, field := range fields {
			if i > 0 {
				out.WriteByte(',')
			}
			key, _ := json.Marshal(field.key)
			out.Write(key)
			out.WriteByte(':')
			out.Write(field.value)
		}
		out.WriteByte('}')
		return out.Bytes(), nil
	case '[':
		values := []json.RawMessage{}
		for dec.More() {
			value, err := canonicalOwnerValue(dec, depth+1)
			if err != nil {
				return nil, err
			}
			values = append(values, value)
		}
		if end, err := dec.Token(); err != nil || end != json.Delim(']') {
			return nil, errors.New("owner-action array is not complete")
		}
		var out bytes.Buffer
		out.WriteByte('[')
		for i, value := range values {
			if i > 0 {
				out.WriteByte(',')
			}
			out.Write(value)
		}
		out.WriteByte(']')
		return out.Bytes(), nil
	default:
		return nil, errors.New("owner-action input has an unexpected delimiter")
	}
}

func ownerActionDigest(canonical json.RawMessage) string {
	sum := sha256.Sum256(canonical)
	return hex.EncodeToString(sum[:])
}

func (book *ownerActionBook) create(ctx context.Context, machine, session string, drafts []ownerActionDraft) (ownerActionBatch, error) {
	return book.createPersistent(ctx, machine, session, drafts, false)
}

func (book *ownerActionBook) createPersistent(ctx context.Context, machine, session string, drafts []ownerActionDraft, persistent bool) (ownerActionBatch, error) {
	if strings.TrimSpace(machine) == "" || strings.TrimSpace(session) == "" {
		return ownerActionBatch{}, errors.New("owner-action batch needs its machine and conversation")
	}
	if len(drafts) == 0 || len(drafts) > ownerActionMaxItems {
		return ownerActionBatch{}, fmt.Errorf("owner-action batch must contain 1 to %d items", ownerActionMaxItems)
	}
	batch := ownerActionBatch{
		ID:         "oab_" + randomHex(12),
		Version:    ownerActionSchemaVersion,
		MachineID:  machine,
		SessionID:  session,
		CreatedAt:  time.Now().UTC(),
		Persistent: persistent,
		Items:      make([]ownerActionItem, 0, len(drafts)),
	}
	total := 0
	for _, draft := range drafts {
		if strings.TrimSpace(draft.Tool) == "" {
			return ownerActionBatch{}, errors.New("owner-action item needs a tool")
		}
		canonical, err := canonicalOwnerAction(draft.Input)
		if err != nil {
			return ownerActionBatch{}, err
		}
		if len(bytes.TrimSpace(canonical)) == 0 || canonical[0] != '{' {
			return ownerActionBatch{}, errors.New("owner-action input must be a JSON object")
		}
		total += len(canonical)
		if total > ownerActionMaxBatch {
			return ownerActionBatch{}, errors.New("owner-action batch is too large")
		}
		fingerprint := ownerActionDigest(canonical)
		var revision *int64
		if draft.Revision != nil {
			value := *draft.Revision
			revision = &value
		}
		batch.Items = append(batch.Items, ownerActionItem{
			ID:          "oai_" + randomHex(12),
			Tool:        draft.Tool,
			Canonical:   string(canonical),
			Fingerprint: fingerprint,
			State:       ownerActionPending,
			Display:     draft.Display,
			Revision:    revision,
		})
	}
	for attempt := 0; attempt < ownerActionCASRetries; attempt++ {
		doc, err := book.read(ctx)
		if err != nil {
			return ownerActionBatch{}, err
		}
		pruneOwnerActionDocument(&doc, time.Now().UTC())
		active := 0
		for _, old := range doc.Batches {
			if ownerActionBatchActive(old) {
				active++
			}
		}
		if active >= ownerActionMaxActive {
			return ownerActionBatch{}, errors.New("too many unanswered owner-action batches")
		}
		doc.Batches = append(doc.Batches, batch)
		doc.Revision++
		saved, err := book.cas.CompareSetting(ctx, ownerActionBookKey, doc.Revision-1, doc)
		if err != nil {
			return ownerActionBatch{}, err
		}
		if saved {
			return batch, nil
		}
	}
	return ownerActionBatch{}, errOwnerActionConflict
}

func (book *ownerActionBook) decide(ctx context.Context, proof ownerReplyProof, allow bool) (ownerActionDecisionResult, error) {
	result := ownerActionDecisionResult{BatchID: proof.BatchID}
	if !proof.AuthenticatedOwner || !proof.DirectParent || !proof.ExactPhrase || strings.TrimSpace(proof.EventID) == "" ||
		strings.TrimSpace(proof.BatchID) == "" {
		return result, errOwnerActionProof
	}
	for attempt := 0; attempt < ownerActionCASRetries; attempt++ {
		doc, err := book.read(ctx)
		if err != nil {
			return result, err
		}
		if previous, ok := doc.Decisions[proof.EventID]; ok {
			if previous.BatchID != proof.BatchID || previous.ItemID != proof.ItemID || previous.Allow != allow {
				return result, errOwnerActionProof
			}
			return result, nil // an idempotent redelivery: no new grant
		}
		batchIndex := -1
		for i := range doc.Batches {
			if doc.Batches[i].ID == proof.BatchID {
				batchIndex = i
				break
			}
		}
		if batchIndex < 0 {
			return result, errOwnerActionProof
		}
		batch := &doc.Batches[batchIndex]
		itemIndexes := []int{}
		if proof.ItemID != "" {
			for i := range batch.Items {
				if batch.Items[i].ID == proof.ItemID {
					itemIndexes = append(itemIndexes, i)
					break
				}
			}
			if len(itemIndexes) == 0 {
				return result, errOwnerActionProof
			}
		} else {
			for i := range batch.Items {
				itemIndexes = append(itemIndexes, i)
			}
		}
		now := time.Now().UTC()
		for _, i := range itemIndexes {
			item := &batch.Items[i]
			if item.State != ownerActionPending {
				continue // the first terminal decision wins; denial never revokes a grant
			}
			item.State = ownerActionDenied
			if allow {
				item.State = ownerActionGranted
				result.ItemIDs = append(result.ItemIDs, item.ID)
			}
			item.DecisionEvent = proof.EventID
			item.DecidedAt = &now
		}
		if doc.Decisions == nil {
			doc.Decisions = map[string]ownerActionDecision{}
		}
		ownerActionFillResult(*batch, &result)
		doc.Decisions[proof.EventID] = ownerActionDecision{BatchID: proof.BatchID, ItemID: proof.ItemID, Allow: allow}
		doc.Revision++
		saved, err := book.cas.CompareSetting(ctx, ownerActionBookKey, doc.Revision-1, doc)
		if err != nil {
			return ownerActionDecisionResult{BatchID: proof.BatchID}, err
		}
		if saved {
			return result, nil
		}
		result.ItemIDs = nil
		result.ApprovedItemIDs = nil
		result.Complete = false
		result.Allow = false
	}
	return result, errOwnerActionConflict
}

func (book *ownerActionBook) proposalOutcome(ctx context.Context, batchID, itemID string) (allow, settled bool, err error) {
	doc, err := book.read(ctx)
	if err != nil {
		return false, false, err
	}
	for _, batch := range doc.Batches {
		if batch.ID != batchID {
			continue
		}
		if itemID != "" {
			for _, item := range batch.Items {
				if item.ID != itemID {
					continue
				}
				switch item.State {
				case ownerActionPending:
					return false, false, nil
				case ownerActionGranted, ownerActionConsumed:
					return true, true, nil
				case ownerActionDenied, ownerActionCancelled:
					return false, true, nil
				default:
					return false, false, fmt.Errorf("owner-action item %s has unknown state %q", item.ID, item.State)
				}
			}
			return false, false, errOwnerActionProof
		}
		var result ownerActionDecisionResult
		ownerActionFillResult(batch, &result)
		if !result.Complete {
			return false, false, nil
		}
		return result.Allow, true, nil
	}
	return false, false, errOwnerActionProof
}

func ownerActionFillResult(batch ownerActionBatch, result *ownerActionDecisionResult) {
	result.BatchID = batch.ID
	result.Complete = true
	for _, item := range batch.Items {
		switch item.State {
		case ownerActionPending:
			result.Complete = false
		case ownerActionGranted, ownerActionConsumed:
			result.Allow = true
			result.ApprovedItemIDs = append(result.ApprovedItemIDs, item.ID)
		}
	}
}

func (book *ownerActionBook) batch(ctx context.Context, id string) (ownerActionBatch, bool, error) {
	doc, err := book.read(ctx)
	if err != nil {
		return ownerActionBatch{}, false, err
	}
	for _, batch := range doc.Batches {
		if batch.ID == id {
			return batch, true, nil
		}
	}
	return ownerActionBatch{}, false, nil
}

type ownerActionTargetOrigin struct {
	MachineID string
	SessionID string
	CreatedAt time.Time
	Terminal  bool
}

// targetOrigin resolves a deterministic batch/item interaction ID back to its
// durable origin and decision state. An existing but still-pending proposal is
// not an idempotent terminal reply: its card may be temporarily unrestored.
func (book *ownerActionBook) targetOrigin(ctx context.Context, interactionID string) (ownerActionTargetOrigin, bool, error) {
	doc, err := book.read(ctx)
	if err != nil {
		return ownerActionTargetOrigin{}, false, err
	}
	for _, batch := range doc.Batches {
		origin := ownerActionTargetOrigin{MachineID: batch.MachineID, SessionID: batch.SessionID, CreatedAt: batch.CreatedAt}
		if ownerActionInteractionID(batch.ID, "") == interactionID {
			var result ownerActionDecisionResult
			ownerActionFillResult(batch, &result)
			origin.Terminal = result.Complete
			return origin, true, nil
		}
		for _, item := range batch.Items {
			if ownerActionInteractionID(batch.ID, item.ID) == interactionID {
				origin.Terminal = item.State != ownerActionPending
				return origin, true, nil
			}
		}
	}
	return ownerActionTargetOrigin{}, false, nil
}

// cancelNonpersistentPending reconciles proposals whose original caller and
// short-lived cards disappeared with the previous manager process. Persistent
// proposals remain answerable, and an existing grant is never revoked or
// replayed: its ordinary connector caller still owns the one consumption.
func (book *ownerActionBook) cancelNonpersistentPending(ctx context.Context) error {
	for attempt := 0; attempt < ownerActionCASRetries; attempt++ {
		doc, err := book.read(ctx)
		if err != nil {
			return err
		}
		changed := false
		now := time.Now().UTC()
		for bi := range doc.Batches {
			batch := &doc.Batches[bi]
			if batch.Persistent {
				continue
			}
			for i := range batch.Items {
				if batch.Items[i].State == ownerActionPending {
					batch.Items[i].State = ownerActionCancelled
					batch.Items[i].DecidedAt = &now
					changed = true
				}
			}
		}
		if !changed {
			return nil
		}
		doc.Revision++
		saved, err := book.cas.CompareSetting(ctx, ownerActionBookKey, doc.Revision-1, doc)
		if err != nil {
			return err
		}
		if saved {
			return nil
		}
	}
	return errOwnerActionConflict
}

// cancelPending ends only proposals that have no owner decision yet. Existing
// grants and consumption records deliberately survive a caller going away.
func (book *ownerActionBook) cancelPending(ctx context.Context, batchID string) error {
	for attempt := 0; attempt < ownerActionCASRetries; attempt++ {
		doc, err := book.read(ctx)
		if err != nil {
			return err
		}
		changed := false
		for bi := range doc.Batches {
			batch := &doc.Batches[bi]
			if batch.ID != batchID {
				continue
			}
			now := time.Now().UTC()
			for i := range batch.Items {
				if batch.Items[i].State == ownerActionPending {
					batch.Items[i].State = ownerActionCancelled
					batch.Items[i].DecidedAt = &now
					changed = true
				}
			}
			break
		}
		if !changed {
			return nil
		}
		doc.Revision++
		saved, err := book.cas.CompareSetting(ctx, ownerActionBookKey, doc.Revision-1, doc)
		if err != nil {
			return err
		}
		if saved {
			return nil
		}
	}
	return errOwnerActionConflict
}

// consume returns Allow only after the durable state change succeeds. SHA-256
// is an index; equality of the complete canonical bytes is the authority.
func (book *ownerActionBook) consume(ctx context.Context, machine, session, tool string, input json.RawMessage) (bool, error) {
	canonical, err := canonicalOwnerAction(input)
	if err != nil {
		return false, nil // not a prepared exact operation: use the ordinary card
	}
	fingerprint := ownerActionDigest(canonical)
	for attempt := 0; attempt < ownerActionCASRetries; attempt++ {
		doc, err := book.read(ctx)
		if err != nil {
			return false, err
		}
		found := false
		for bi := range doc.Batches {
			batch := &doc.Batches[bi]
			if batch.MachineID != machine || batch.SessionID != session {
				continue
			}
			for ii := range batch.Items {
				item := &batch.Items[ii]
				if item.State != ownerActionGranted || item.Tool != tool || item.Fingerprint != fingerprint ||
					!bytes.Equal([]byte(item.Canonical), canonical) {
					continue
				}
				now := time.Now().UTC()
				item.State = ownerActionConsumed
				item.ConsumedAt = &now
				found = true
				break
			}
			if found {
				break
			}
		}
		if !found {
			return false, nil
		}
		doc.Revision++
		saved, err := book.cas.CompareSetting(ctx, ownerActionBookKey, doc.Revision-1, doc)
		if err != nil {
			return false, err
		}
		if saved {
			return true, nil
		}
	}
	return false, errOwnerActionConflict
}

// ownerActionExecution identifies one granted built-in call and its position in the approved batch.
type ownerActionExecution struct {
	BatchID  string
	Item     ownerActionItem
	Position int
	Total    int
}

// consumeBuiltin atomically spends one exact built-in grant. Within a batch, an
// approved built-in item cannot overtake an earlier granted or running built-in.
// A recorded failure blocks every later grant instead of allowing silent progress.
func (book *ownerActionBook) consumeBuiltin(ctx context.Context, machine, session, tool string, input json.RawMessage) (*ownerActionExecution, error) {
	canonical, err := canonicalOwnerAction(input)
	if err != nil {
		// The built-in decoders historically accepted duplicate object keys. Do
		// not let a non-canonical spelling select a pending/denied operation or
		// execute once here and again through an unconsumed exact grant.
		return nil, fmt.Errorf("built-in owner-action input is not an unambiguous JSON object: %w", err)
	}
	fingerprint := ownerActionDigest(canonical)
	for attempt := 0; attempt < ownerActionCASRetries; attempt++ {
		doc, err := book.read(ctx)
		if err != nil {
			return nil, err
		}
		var execution *ownerActionExecution
		for bi := range doc.Batches {
			batch := &doc.Batches[bi]
			if batch.MachineID != machine || batch.SessionID != session {
				continue
			}
			for ii := range batch.Items {
				item := &batch.Items[ii]
				if item.State != ownerActionGranted || item.Tool != tool || item.Fingerprint != fingerprint ||
					!bytes.Equal([]byte(item.Canonical), canonical) {
					continue
				}
				if batch.FailedItem != "" {
					return nil, fmt.Errorf("owner-action batch %s already stopped at %s: %s", batch.ID, batch.FailedItem, batch.Failure)
				}
				for earlier := 0; earlier < ii; earlier++ {
					if !ownerBuiltinWrite(batch.Items[earlier].Tool) {
						continue
					}
					state := batch.Items[earlier].State
					if state == ownerActionGranted || state == ownerActionConsumed && batch.Items[earlier].Outcome == "running" {
						return nil, fmt.Errorf("owner-action batch %s must execute item %d before item %d", batch.ID, earlier+1, ii+1)
					}
				}
				now := time.Now().UTC()
				item.State = ownerActionConsumed
				item.Outcome = "running"
				item.ConsumedAt = &now
				execution = &ownerActionExecution{BatchID: batch.ID, Item: *item, Position: ii + 1, Total: len(batch.Items)}
				break
			}
			if execution != nil {
				break
			}
		}
		if execution == nil {
			// A built-in's ordinary direct path may not itself have a card (switch
			// and rollback). Never let the exact call bypass a pending, denied,
			// stopped or already consumed durable decision. A fresh batch is a new
			// explicit operation and can grant the same canonical bytes again.
			for _, batch := range doc.Batches {
				if batch.MachineID != machine || batch.SessionID != session {
					continue
				}
				for _, item := range batch.Items {
					if ownerBuiltinWrite(item.Tool) && item.Tool == tool && item.Fingerprint == fingerprint &&
						bytes.Equal([]byte(item.Canonical), canonical) {
						return nil, fmt.Errorf("exact built-in call is %s in batch %s; wait for its decision or prepare a fresh batch instead of bypassing it", item.State, batch.ID)
					}
				}
			}
			return nil, nil
		}
		doc.Revision++
		saved, err := book.cas.CompareSetting(ctx, ownerActionBookKey, doc.Revision-1, doc)
		if err != nil {
			return nil, err
		}
		if saved {
			return execution, nil
		}
	}
	return nil, errOwnerActionConflict
}

// finishBuiltin records success, or the exact partial stopping point. Later
// unused grants become stopped; a consumed grant is never replayed.
func (book *ownerActionBook) finishBuiltin(ctx context.Context, execution ownerActionExecution, runErr error, completed bool) (string, error) {
	for attempt := 0; attempt < ownerActionCASRetries; attempt++ {
		doc, err := book.read(ctx)
		if err != nil {
			return "", err
		}
		changed := false
		var report string
		for bi := range doc.Batches {
			batch := &doc.Batches[bi]
			if batch.ID != execution.BatchID {
				continue
			}
			for ii := range batch.Items {
				item := &batch.Items[ii]
				if item.ID != execution.Item.ID {
					continue
				}
				if item.State != ownerActionConsumed || item.Outcome != "running" {
					return "", errOwnerActionConflict
				}
				if runErr == nil {
					item.Outcome = "done"
				} else {
					item.Outcome = "failed"
					if completed {
						item.Outcome = "done"
					}
					batch.FailedItem = item.ID
					batch.Failure = runErr.Error()
					if len(batch.Failure) > 2000 {
						batch.Failure = batch.Failure[:2000]
					}
					for later := ii + 1; later < len(batch.Items); later++ {
						if batch.Items[later].State == ownerActionGranted {
							batch.Items[later].State = ownerActionStopped
							batch.Items[later].Outcome = "stopped"
						}
					}
				}
				changed = true
				if runErr != nil {
					doneItems, notExecuted := []int{}, []int{}
					for i, candidate := range batch.Items {
						switch candidate.Outcome {
						case "done":
							doneItems = append(doneItems, i+1)
						case "stopped":
							notExecuted = append(notExecuted, i+1)
						}
					}
					report = fmt.Sprintf("Built-in batch stopped at item %d/%d (%s): %v. Completed items: %v. Items not executed: %v.",
						ii+1, len(batch.Items), item.ID, runErr, doneItems, notExecuted)
					if completed {
						report += " The stopping item completed; its read-back failed."
					}
				}
				break
			}
			break
		}
		if !changed {
			return "", errOwnerActionProof
		}
		doc.Revision++
		saved, err := book.cas.CompareSetting(ctx, ownerActionBookKey, doc.Revision-1, doc)
		if err != nil {
			return "", err
		}
		if saved {
			return report, nil
		}
	}
	return "", errOwnerActionConflict
}

func ownerActionBatchActive(batch ownerActionBatch) bool {
	for _, item := range batch.Items {
		if item.State == ownerActionPending || item.State == ownerActionGranted {
			return true
		}
	}
	return false
}

// Cleanup can remove only terminal history. Pending and unused grants have no
// TTL: expiring one would silently undo an owner's recorded decision.
func pruneOwnerActionDocument(doc *ownerActionDocument, now time.Time) {
	kept := doc.Batches[:0]
	liveEvents := map[string]bool{}
	for _, batch := range doc.Batches {
		terminal := !ownerActionBatchActive(batch)
		old := terminal && len(batch.Items) > 0
		for _, item := range batch.Items {
			if item.State == ownerActionPending || item.State == ownerActionGranted || item.DecidedAt == nil ||
				now.Sub(*item.DecidedAt) < ownerActionTerminalFor {
				old = false
			}
			if item.DecisionEvent != "" && (item.State == ownerActionPending || item.State == ownerActionGranted) {
				liveEvents[item.DecisionEvent] = true
			}
		}
		if !old {
			kept = append(kept, batch)
			for _, item := range batch.Items {
				if item.DecisionEvent != "" {
					liveEvents[item.DecisionEvent] = true
				}
			}
		}
	}
	doc.Batches = kept
	for event := range doc.Decisions {
		if !liveEvents[event] {
			delete(doc.Decisions, event)
		}
	}
}
