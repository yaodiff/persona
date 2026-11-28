package assistant

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"slices"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/nwverse/persona/server/internal/assistantwire"
	"github.com/nwverse/persona/server/internal/bus"
	"github.com/nwverse/persona/server/internal/buswire"
	"github.com/nwverse/persona/server/internal/kb"
	"github.com/nwverse/persona/server/internal/recordingwire"
	"github.com/nwverse/persona/server/internal/rpc"
)

// kbDecodeParams is strict so a misspelled update cannot silently succeed without applying the intended change.
func kbDecodeParams(method string, raw json.RawMessage, out any) error {
	if len(raw) > 1<<20 || !bytes.HasPrefix(bytes.TrimSpace(raw), []byte("{")) {
		return kbInvalidParams(method, "expected an object of at most 1 MiB")
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if err := d.Decode(out); err != nil {
		return kbInvalidParams(method, err.Error())
	}
	if err := d.Decode(new(any)); err != io.EOF {
		return kbInvalidParams(method, "expected one object")
	}
	return nil
}

func kbInvalidParams(method, message string) error {
	return &rpc.Error{Code: bus.CodeInvalidParams, Message: method + ": " + message}
}

func kbRPCKind(kind string) bool {
	switch kind {
	case kb.KindProject, kb.KindChat, kb.KindPerson, kb.KindDoc, kb.KindMeeting, kb.KindEvent, kb.KindRecording, kb.KindTopic:
		return true
	}
	return false
}

func kbRPCError(err error) error {
	var conflict *kb.ConflictError
	if errors.As(err, &conflict) {
		data, _ := json.Marshal(assistantwire.KBConflict{Owners: conflict.Owners})
		return &rpc.Error{Code: assistantwire.CodeKBConflict, Message: "kb.update: " + conflict.Error(), Data: data}
	}
	if errors.Is(err, kb.ErrNotFound) {
		return &rpc.Error{Code: "not_found", Message: err.Error()}
	}
	if errors.Is(err, kb.ErrInvalidProfile) || errors.Is(err, kb.ErrInvalidProjectSource) || errors.Is(err, kb.ErrReferenceOnly) {
		return &rpc.Error{Code: bus.CodeInvalidParams, Message: "kb.update: " + err.Error()}
	}
	return kbError(err)
}

// handleKBRPC binds the request and reply types while keeping the existing error prefix for enveloped calls.
func handleKBRPC[P, R any](ctx context.Context, _ buswire.Method[P, R], errorMethod string, raw json.RawMessage, run func(context.Context, P) (*R, error)) (any, bool, error) {
	var params P
	if err := kbDecodeParams(errorMethod, raw, &params); err != nil {
		return nil, true, err
	}
	result, err := run(ctx, params)
	if result == nil {
		return nil, true, err
	}
	return *result, true, err
}

// kbRPCRequest shares the engine with the apps and MCP tools, without invoking an HTTP handler or exposing agents.
func (s *Service) kbRPCRequest(ctx context.Context, method string, raw json.RawMessage) (any, bool, error) {
	switch method {
	case assistantwire.MethodKBProviderCheckpoint:
		return handleKBRPC(ctx, assistantwire.KBProviderCheckpointMethod, method, raw, s.kbProviderCheckpointRPC)
	case assistantwire.MethodKBProviderSentMarkers:
		return handleKBRPC(ctx, assistantwire.KBProviderSentMarkersMethod, method, raw, s.kbProviderSentMarkersRPC)
	case assistantwire.MethodKBList:
		return handleKBRPC(ctx, assistantwire.KBListMethod, method, raw, s.kbListRPC)
	case assistantwire.MethodKBRecent:
		return handleKBRPC(ctx, assistantwire.KBRecentMethod, method, raw, s.kbRecentRPC)
	case assistantwire.MethodKBGet:
		return handleKBRPC(ctx, assistantwire.KBGetMethod, method, raw, s.kbGetRPC)
	case assistantwire.MethodKBDay, assistantwire.MethodKBExtensionDay:
		descriptor := assistantwire.KBDayMethod
		sentOrigin := method == assistantwire.MethodKBExtensionDay
		if sentOrigin {
			descriptor = assistantwire.KBExtensionDayMethod
		}
		return handleKBRPC(ctx, descriptor, assistantwire.MethodKBDay, raw, func(ctx context.Context, p assistantwire.KBDayParams) (*assistantwire.KBDayResult, error) {
			return s.kbDayRPC(ctx, p, sentOrigin)
		})
	case assistantwire.MethodKBSearch:
		return handleKBRPC(ctx, assistantwire.KBSearchMethod, method, raw, s.kbSearchRPC)
	case assistantwire.MethodKBUpdate:
		if s.kbReadOnly(ctx) {
			return nil, true, kbError(kb.ErrReadOnly)
		}
		return handleKBRPC(ctx, assistantwire.KBUpdateMethod, method, raw, func(ctx context.Context, p assistantwire.KBUpdateParams) (*assistantwire.KBUpdateResult, error) {
			return s.kbUpdateRPC(ctx, p, "")
		})
	case assistantwire.MethodKBExtensionUpdate:
		return handleKBRPC(ctx, assistantwire.KBExtensionUpdateMethod, method, raw, s.kbExtensionUpdateRPC)
	case assistantwire.MethodKBExtensionPutRecording:
		return handleKBRPC(ctx, assistantwire.KBExtensionPutRecordingMethod, assistantwire.MethodKBPutRecording, raw, s.kbPutRecording)
	case assistantwire.MethodKBPutRecording: // without the envelope it is nobody's: the apps and the hub have no such write
		return nil, true, kbInvalidParams(method, "only an extension writes its own recordings")
	default:
		return nil, false, nil
	}
}

func (s *Service) kbListRPC(ctx context.Context, list assistantwire.KBListParams) (*assistantwire.KBListResult, error) {
	const method = assistantwire.MethodKBList
	if !kbRPCKind(list.Kind) || list.Limit < 0 || list.Limit > 200 || list.Offset < 0 {
		return nil, kbInvalidParams(method, "kind must name an entity kind; limit must be between 0 and 200; offset must be nonnegative")
	}
	if list.Limit == 0 {
		list.Limit = 30
	}
	var since time.Time
	if list.Since != "" {
		var err error
		if since, err = time.Parse(time.RFC3339, list.Since); err != nil {
			return nil, kbInvalidParams(method, "since must be an RFC 3339 time")
		}
	}
	e, err := s.kbGet(ctx)
	if err != nil {
		return nil, kbRPCError(err)
	}
	all := e.List(list.Kind, "")
	items := all
	if list.Tag != "" {
		items = e.List(list.Kind, list.Tag)
	}
	if list.Since != "" {
		items = slices.DeleteFunc(slices.Clone(items), func(en kb.Entry) bool { return en.Updated.Before(since) })
	}
	if items == nil {
		items = []kb.Entry{}
	}
	total := len(items)
	start := min(list.Offset, total)
	end := start + min(total-start, list.Limit)
	items = items[start:end]
	var next *int
	if end < total {
		next = &end
	}
	tags := kb.KindTags(all, 30)
	if tags == nil {
		tags = []string{}
	}
	return &assistantwire.KBListResult{Items: items, Tags: tags, Total: total, NextOffset: next}, nil
}

func (s *Service) kbRecentRPC(ctx context.Context, recent assistantwire.KBRecentParams) (*assistantwire.KBRecentResult, error) {
	const method = assistantwire.MethodKBRecent
	since, err := time.Parse(time.RFC3339, recent.Since)
	if err != nil {
		return nil, kbInvalidParams(method, "since must be an RFC 3339 time")
	}
	var until time.Time
	if recent.Until != "" {
		if until, err = time.Parse(time.RFC3339, recent.Until); err != nil {
			return nil, kbInvalidParams(method, "until must be an RFC 3339 time")
		}
	}
	if len(recent.Kinds) == 0 {
		recent.Kinds = []string{kb.KindChat, kb.KindDoc, kb.KindMeeting, kb.KindEvent, kb.KindRecording}
	}
	for _, k := range recent.Kinds {
		if !kbRPCKind(k) {
			return nil, kbInvalidParams(method, "kinds must name entity kinds")
		}
	}
	if recent.Limit < 0 || recent.Limit > 200 || recent.Offset < 0 {
		return nil, kbInvalidParams(method, "limit must be between 0 and 200; offset must be nonnegative")
	}
	if recent.Limit == 0 {
		recent.Limit = 50
	}
	e, err := s.kbGet(ctx)
	if err != nil {
		return nil, kbRPCError(err)
	}
	items := []kb.Entry{}
	for _, k := range slices.Compact(slices.Sorted(slices.Values(recent.Kinds))) {
		for _, en := range e.List(k, "") {
			if !en.Updated.Before(since) && (until.IsZero() || en.Updated.Before(until)) {
				items = append(items, en)
			}
		}
	}
	slices.SortStableFunc(items, func(a, b kb.Entry) int { return b.Updated.Compare(a.Updated) })
	total := len(items)
	start := min(recent.Offset, total)
	end := start + min(total-start, recent.Limit)
	var next *int
	if end < total {
		next = &end
	}
	return &assistantwire.KBRecentResult{Items: items[start:end], Total: total, NextOffset: next}, nil
}

func (s *Service) kbGetRPC(ctx context.Context, get assistantwire.KBGetParams) (*assistantwire.KBGetResult, error) {
	const method = assistantwire.MethodKBGet
	if !kbRPCKind(get.Kind) || strings.TrimSpace(get.ID) == "" {
		return nil, kbInvalidParams(method, "kind and id are required")
	}
	if get.RecentChars == nil {
		n := 12000
		get.RecentChars = &n
	}
	if *get.RecentChars < 0 || *get.RecentChars > 20000 {
		return nil, kbInvalidParams(method, "recent_chars must be between 0 and 20000")
	}
	e, err := s.kbGet(ctx)
	if err != nil {
		return nil, kbRPCError(err)
	}
	detail, err := e.Get(get.Kind, get.ID, *get.RecentChars)
	return &assistantwire.KBGetResult{Detail: detail}, kbRPCError(err)
}

func (s *Service) kbDayRPC(ctx context.Context, day assistantwire.KBDayParams, sentOrigin bool) (*assistantwire.KBDayResult, error) {
	const method = assistantwire.MethodKBDay
	if strings.TrimSpace(day.ID) == "" {
		return nil, kbInvalidParams(method, "id is required")
	}
	if _, err := time.Parse("2006-01-02", day.Day); err != nil {
		return nil, kbInvalidParams(method, "day must be YYYY-MM-DD")
	}
	e, err := s.kbGet(ctx)
	if err != nil {
		return nil, kbRPCError(err)
	}
	if sentOrigin && s.ui != nil { // without the knowledge-ui module a day carries no sent marks
		out, err := s.kbDaySent(ctx, e, day)
		return &out, kbRPCError(err)
	}
	messages, prev, next, err := e.Day(day.ID, day.Day)
	if messages == nil {
		messages = []kb.DayMessage{}
	}
	return &assistantwire.KBDayResult{SentOrigin: "unavailable", Day: day.Day, Messages: messages, Prev: prev, Next: next}, kbRPCError(err)
}

func (s *Service) kbSearchRPC(ctx context.Context, search assistantwire.KBSearchParams) (*assistantwire.KBSearchResult, error) {
	const method = assistantwire.MethodKBSearch
	if strings.TrimSpace(search.Query) == "" || search.Limit < 0 || search.Limit > 200 {
		return nil, kbInvalidParams(method, "query is required; limit must be between 0 and 200")
	}
	if search.Limit == 0 {
		search.Limit = 40
	}
	e, err := s.kbGet(ctx)
	if err != nil {
		return nil, kbRPCError(err)
	}
	hits := e.Search(search.Query, search.Messages == nil || *search.Messages, search.Limit)
	if hits == nil {
		hits = []kb.Hit{}
	}
	return &assistantwire.KBSearchResult{Hits: hits}, nil
}

func (s *Service) kbExtensionUpdateRPC(ctx context.Context, env assistantwire.KBExtensionUpdate) (*assistantwire.KBUpdateResult, error) {
	if strings.TrimSpace(env.Extension) == "" {
		return nil, kbInvalidParams(assistantwire.MethodKBExtensionUpdate, "extension is required")
	}
	if s.kbReadOnly(ctx) {
		return nil, kbError(kb.ErrReadOnly)
	}
	var update assistantwire.KBUpdateParams
	if err := kbDecodeParams(assistantwire.MethodKBUpdate, env.Params, &update); err != nil {
		return nil, err
	}
	return s.kbUpdateRPC(ctx, update, env.StorageKey())
}

func (s *Service) kbUpdateRPC(ctx context.Context, update assistantwire.KBUpdateParams, extension string) (*assistantwire.KBUpdateResult, error) {
	const method = assistantwire.MethodKBUpdate
	if !kbRPCKind(update.Kind) || strings.TrimSpace(update.ID) == "" {
		return nil, kbInvalidParams(method, "kind and id are required")
	}
	if extension == "" && (len(update.Evidence) > 0 || len(update.Unlock) > 0) {
		return nil, kbInvalidParams(method, "evidence and unlock are for extensions' writes")
	}
	if msg := kbCheckEvidence(update.Evidence); msg != "" {
		return nil, kbInvalidParams(method, msg)
	}
	for _, f := range update.Unlock {
		if !kb.LockableField(f) {
			return nil, kbInvalidParams(method, "unlock: unknown field "+f)
		}
	}
	e, err := s.kbGet(ctx)
	if err != nil {
		return nil, kbRPCError(err)
	}
	if extension != "" {
		res, err := e.ApplyExtension(update.Kind, update.ID, update.Update,
			kb.ExtensionWrite{Extension: extension, Evidence: update.Evidence, Unlock: update.Unlock})
		if err != nil {
			return nil, kbRPCError(err)
		}
		return &assistantwire.KBUpdateResult{ID: res.ID, Before: res.Before, Ignored: res.Ignored}, nil
	}
	ignored := kb.Unsupported(update.Kind, update.Update)
	id, err := e.Apply(update.Kind, update.ID, update.Update)
	return &assistantwire.KBUpdateResult{ID: id, Ignored: ignored}, kbRPCError(err)
}

// kbPutRecording is an extension's kb.put_recording, in the manager's envelope that names the extension.
func (s *Service) kbPutRecording(ctx context.Context, env assistantwire.KBExtensionUpdate) (*recordingwire.RecordingWriteResult, error) {
	const method = assistantwire.MethodKBPutRecording
	if strings.TrimSpace(env.Extension) == "" {
		return nil, kbInvalidParams(method, "extension is required")
	}
	var w recordingwire.RecordingWrite
	if err := kbDecodeParams(method, env.Params, &w); err != nil {
		return nil, err
	}
	if s.kbReadOnly(ctx) {
		return nil, kbError(kb.ErrReadOnly)
	}
	e, err := s.kbGet(ctx)
	if err != nil {
		return nil, kbRPCError(err)
	}
	res, err := e.PutExtensionRecording(w, env.StorageKey(), time.Now())
	switch {
	case err == nil:
		return &res, nil
	case errors.Is(err, kb.ErrNotRecordingWriter):
		return nil, &rpc.Error{Code: assistantwire.CodeKBNotWriter, Message: method + ": " + err.Error()}
	case errors.Is(err, kb.ErrRecordingDeleted):
		return nil, &rpc.Error{Code: assistantwire.CodeKBDeleted, Message: method + ": " + err.Error()}
	case errors.Is(err, kb.ErrBadRecording):
		return nil, kbInvalidParams(method, err.Error())
	}
	return nil, kbRPCError(err)
}

// kbCheckEvidence is why an update's evidence is invalid; "" when it is fine.
func kbCheckEvidence(list []kb.Evidence) string {
	if len(list) > 20 {
		return "at most 20 evidence entries"
	}
	for _, ev := range list {
		if strings.TrimSpace(ev.Chat) == "" || ev.N < 0 || utf8.RuneCountInString(ev.Note) > 200 {
			return "evidence needs a chat, a nonnegative n and a note of at most 200 characters"
		}
		if _, err := time.Parse("2006-01-02", ev.Day); err != nil {
			return "evidence day must be YYYY-MM-DD"
		}
	}
	return ""
}
