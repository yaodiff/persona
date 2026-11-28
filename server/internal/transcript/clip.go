package transcript

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"strings"

	"github.com/nwverse/persona/server/internal/proto"
)

// snippetLine is the first line of s, at most n runes.
func snippetLine(s string, n int) string {
	s, _, _ = strings.Cut(strings.TrimSpace(s), "\n")
	r := []rune(strings.TrimSpace(s))
	if len(r) > n {
		return string(r[:n]) + "…"
	}
	return string(r)
}

func clip(s string) string { return clipTo(s, maxTextLen) }

// clipTo cuts s to at most n bytes on a UTF-8 boundary and notes the original size.
func clipTo(s string, n int) string {
	if len(s) <= n {
		return s
	}
	cut, note := clipParts(s, n)
	return s[:cut] + note
}

// The Go and web file-change summaries recognize this suffix in tool inputs
// (clippedNote; web/src/lib/turn-changes.ts).
const (
	clipNotePrefix = "\n…（已截断，共 "
	clipNoteSuffix = " 字节）"
	clipNoteFormat = clipNotePrefix + "%d" + clipNoteSuffix
)

// ClipNote returns the suffix that marks a tool input string as truncated from total bytes.
func ClipNote(total int) string { return fmt.Sprintf(clipNoteFormat, total) }

// ApproximateClipNote marks a tool input string whose original byte count is estimated.
func ApproximateClipNote(total int) string {
	return fmt.Sprintf(clipNotePrefix+"≈%d"+clipNoteSuffix, total)
}

// clipParts is where clipTo cuts s (len(s) > n) and the note it adds.
func clipParts(s string, n int) (cut int, note string) {
	cut = n
	for cut > 0 && (s[cut]&0xC0) == 0x80 {
		cut--
	}
	return cut, ClipNote(len(s))
}

// light is the copy of an item that pages and pushes carry; cut says something was left out (fetch it with Full).
// Long text ends in a line that says it was cut, so apps that do not know FullSize still show it truncated, not whole.
// Only a tool's output has a "View full text" button in the app, so only its note says to tap for the whole.
func light(it proto.Item, keepImage bool) (_ proto.Item, cut bool) {
	limit, note := pageTextLen, "\n…（已截断，共 %d 字节）"
	if it.Kind == proto.KindToolResult {
		limit, note = pageOutputLen, "\n…（已截断，点开看全文，共 %d 字节）"
	}
	if len(it.Text) > limit && it.Role == "user" && it.Kind == proto.KindText {
		recordRawTextHash(&it, it.Text)
		if t, ok := lightContexts(it.Text, limit); ok { // what the user wrote stays whole, the blocks' bodies are cut
			it.FullSize, it.Text = len(it.Text), t
			cut = true
		}
	}
	if len(it.Text) > limit {
		if it.Role == "user" && it.Kind == proto.KindText {
			recordRawTextHash(&it, it.Text)
		}
		end := pageCut(it.Text, limit)
		it.FullSize = len(it.Text)
		it.Text = it.Text[:end] + fmt.Sprintf(note, len(it.Text))
		cut = true
	}
	// Quoted questions have their own budget. For a multi-answer reply the plain Text
	// remains a readable preview even if the complete list needs the full-item endpoint.
	quoteBytes := 0
	for _, reply := range it.QuestionReplies {
		quoteBytes += len(reply.Question) + len(reply.Answer)
	}
	if quoteBytes > pageTextLen {
		if len(it.QuestionReplies) == 1 {
			it.QuestionReplies = []proto.QuestionReply{{Question: clipTo(it.QuestionReplies[0].Question, pageTextLen)}}
		} else {
			it.QuestionReplies = nil
		}
		it.QuestionRepliesCut, cut = true, true
	}
	if it.Image != nil && it.Image.Data != "" && !keepImage {
		it.Image = it.Image.Placeholder()
		cut = true
	}
	return it, cut
}

// recordRawTextHash captures the SHA-256 of raw (and its \r\n-trimmed form) on it before truncation,
// preserving the first (most complete) hash if it was already recorded at parse time.
func recordRawTextHash(it *proto.Item, raw string) {
	if it.RawTextSHA256 != "" || raw == "" {
		return
	}
	it.RawTextSHA256 = sha256Hex(raw)
	if trimmed := strings.TrimRight(raw, "\r\n"); trimmed != raw {
		it.RawTextTrimmedSHA256 = sha256Hex(trimmed)
	}
}

// clipUserText clips user prompt text at maxTextLen while preserving the untruncated SHA-256 for anchor matching.
func clipUserText(it *proto.Item, raw string) {
	if len(raw) > maxTextLen {
		recordRawTextHash(it, raw)
	}
	it.Text = clip(raw)
}

// LightItem applies the page and push limits to an item from a native transcript reader.
func LightItem(it proto.Item) proto.Item {
	it, _ = light(it, false)
	return it
}

// ClipToolInput applies the transcript's wire size limit to a tool's arguments.
func ClipToolInput(input json.RawMessage) json.RawMessage {
	return clipJSON(input)
}

// pageCut is where light cuts text longer than limit: s[:pageCut] is what the page keeps. It ends on a character, at
// most limit bytes in, and on a whole line (before its newline) when one ends in the last 512 bytes of that. An
// outcome's tail picks up from here (outputTail).
func pageCut(s string, limit int) int {
	end := limit
	for end > 0 && (s[end]&0xC0) == 0x80 {
		end--
	}
	if nl := strings.LastIndexByte(s[:end], '\n'); nl > end-512 { // end on a whole line when one is near
		end = nl
	}
	return end
}

const (
	minInputShare = 256 // a long string in a tool input is never cut shorter than this
	clipNoteRoom  = 48  // what clipTo's note adds to a cut string, as JSON
)

// decodeInputObject keeps the ordinary float64 behavior, but retries with json.Number when a valid number exceeds its
// range (for example 1e1000). Such an object still deserves the whole-input bound rather than an oversized pass-through.
func decodeInputObject(raw []byte) (map[string]any, error) {
	var m map[string]any
	if err := json.Unmarshal(raw, &m); err == nil {
		return m, nil
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	if err := dec.Decode(&m); err != nil {
		return nil, err
	}
	if err := dec.Decode(&struct{}{}); err != io.EOF {
		return nil, fmt.Errorf("tool input has trailing JSON")
	}
	return m, nil
}

// clipJSON shortens a tool input object in three passes. Long strings anywhere are cut first: a Write's content, but also
// push_files' files[].content, MultiEdit's edits[].new_string or an MCP call's nested blocks, which went out whole in
// every page and push. If short values still keep the encoded object over budget, boundInputJSON drops array tails and
// then object fields while preserving every surviving value's shape. A value that is not an object is left as it is: a
// Claude tool_use's input and an MCP call's arguments are always objects.
func clipJSON(raw json.RawMessage) json.RawMessage {
	if len(raw) <= maxInputLen {
		return raw
	}
	m, err := decodeInputObject(raw)
	if err != nil {
		return raw
	}
	b := marshalPlain(mapStrings(m, func(s string) string { return clipTo(s, maxInputLen/2) }))
	if len(b) <= maxInputLen {
		return b
	}
	// Still too long (many medium strings): the strings over minInputShare share evenly what the rest leaves. Shorter
	// strings, keys and other values survive this pass, and a share is never under minInputShare. If their total still
	// exceeds maxInputLen, the third pass removes whole values rather than cutting any string below that floor.
	long, kept := 0, 0
	rest := marshalPlain(mapStrings(m, func(s string) string {
		if len(s) <= minInputShare {
			return s
		}
		long, kept = long+1, kept+min(len(s), maxInputLen/2)
		return ""
	}))
	if long == 0 {
		return boundInputJSON(b, len(raw)) // short values and keys still need the whole-input bound
	}
	// JSON escapes make a kept byte cost more than one: scale the share by what the first pass's strings cost.
	share := (maxInputLen - len(rest)) * kept / (len(b) - len(rest)) / long
	share = min(max(share-clipNoteRoom, minInputShare), maxInputLen/2)
	b = marshalPlain(mapStrings(m, func(s string) string { return clipIfShorter(s, share) }))
	if len(b) <= maxInputLen {
		return b
	}
	return boundInputJSON(b, len(raw))
}

// marshalPlain is v as JSON with < > & as they are, as the input came: json.Marshal writes each as six bytes, and every
// page and push carried them so, 160 KB for a 61 KB input of HTML (#2907). U+2028 and U+2029 are still escaped, as
// encoding/json does without HTML escaping. A decoded JSON value always encodes.
func marshalPlain(v any) []byte {
	var b bytes.Buffer
	enc := json.NewEncoder(&b)
	enc.SetEscapeHTML(false)
	_ = enc.Encode(v)
	return bytes.TrimSuffix(b.Bytes(), []byte("\n"))
}

// clipIfShorter is clipTo(s, n) if that is shorter as JSON, which is what a tool input carries, and s if not. A string
// more than clipNoteRoom over n always is: every byte cut costs at least one byte of JSON and the note at most
// clipNoteRoom. An unescaped ASCII string stays whole up to the note's JSON content length beyond n (37 bytes with
// a three-digit original size). Escapes change that threshold: quotes and backslashes cost two bytes each, control
// characters up to six, so such a tail of the same raw length may cost more than the note.
func clipIfShorter(s string, n int) string {
	if len(s) <= n {
		return s
	}
	if len(s) > n+clipNoteRoom {
		return clipTo(s, n)
	}
	cut, note := clipParts(s, n)
	// The cut is on a UTF-8 boundary, so JSON encodes the shared prefix identically in both strings, even next to
	// an escape. That prefix and the surrounding quotes cancel: only the dropped tail and note need marshalling.
	if jsonLen(s[cut:]) > jsonLen(note) {
		return s[:cut] + note
	}
	return s
}

// jsonLen is the length of s written as a JSON string, as clipJSON writes it.
func jsonLen(s string) int {
	return len(marshalPlain(s))
}

// mapStrings is a copy of v, a decoded JSON value, with f applied to every string in it (object keys are kept).
func mapStrings(v any, f func(string) string) any {
	switch v := v.(type) {
	case string:
		return f(v)
	case map[string]any:
		out := make(map[string]any, len(v))
		for k, e := range v {
			out[k] = mapStrings(e, f)
		}
		return out
	case []any:
		out := make([]any, len(v))
		for i, e := range v {
			out[i] = mapStrings(e, f)
		}
		return out
	}
	return v
}
