// Package uistream implements the bounded, immutable native keyed-tree core.
// It does not authorize owners, execute callbacks, fit views or advertise capabilities.
// Keep received JSON at this boundary: generated Component's legacy omitempty codec
// is a projection, not a lossless canonical store.
package uistream

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"math/big"
	"reflect"
	"strconv"
	"strings"
	"sync"
	"unicode/utf8"

	"github.com/nwverse/persona/sdk/go/wire"
)

const (
	FrameMost            = 1 << 20
	RequestMost          = 64 << 10
	OperationsMost       = 256
	NodesMost            = 300
	DepthMost            = 16
	VersionMost    int64 = 1<<53 - 1
)

// ErrSnapshotRequired marks rejected frames/batches; callers recover with a fresh
// authorized snapshot, never by skipping an operation or retrying a write.
var ErrSnapshotRequired = errors.New("UI stream snapshot required")

func rejected(err error) error { return fmt.Errorf("%w: %v", ErrSnapshotRequired, err) }

type node struct {
	key, kind string // view, section, item, tile (not the native component tag)
	props     map[string]json.RawMessage
	children  []*node
}

// Tree is immutable and safe for concurrent readers. All accessors return copies.
// A caller serializes updates and binds the initial session/epoch to its principal
// and runtime generation. Opaque IDs alone never authorize access.
type Tree struct {
	header wire.UIStreamSnapshot
	root   *node
	// view is root's serialization, filled once check accepts the tree; a
	// checked tree never changes, so every later reader shares these bytes.
	view json.RawMessage
	// ownFrame: DecodeSnapshot made the tree from its own frame (MarshalJSON),
	// byte for byte, so decoding that frame again gives the same tree.
	ownFrame bool
}

// Decodes reports whether DecodeSnapshot(raw) gives t again: t was decoded
// from its own frame and raw is that frame byte for byte. DecodeSnapshot reads
// only raw, so a caller holding t need not decode and check raw again.
func (t *Tree) Decodes(raw []byte) bool {
	if t == nil || !t.ownFrame {
		return false
	}
	frame, err := t.MarshalJSON()
	return err == nil && bytes.Equal(frame, raw)
}

// Delta retains the validated raw presence of operation payloads alongside the
// generated frame contract. Frame is only a detached DTO projection; serialize
// Delta itself to preserve optional false/zero/empty properties.
type Delta struct {
	frame wire.UIStreamDelta
	ops   []map[string]json.RawMessage
}

func (t *Tree) Session() string { return t.header.Session }
func (t *Tree) Epoch() string   { return t.header.Epoch }
func (t *Tree) Version() int64  { return t.header.Version }
func (t *Tree) RootKey() string { return t.header.RootKey }

// ViewJSON returns a detached lossless native View. Snapshot returns the generated
// DTO projection for reading/fitting; serialize Tree itself, never this projection.
func (t *Tree) ViewJSON() json.RawMessage { return bytes.Clone(t.viewJSON()) }

// viewJSON is ViewJSON without the defensive copy, for readers in this
// package that never modify or retain the bytes.
func (t *Tree) viewJSON() json.RawMessage {
	if t.view != nil {
		return t.view
	}
	return t.root.json()
}
func (t *Tree) Snapshot() wire.UIStreamSnapshot {
	out := t.header
	_ = decodeDTO(t.viewJSON(), &out.View)
	return out
}
func (t *Tree) MarshalJSON() ([]byte, error) {
	if t == nil || t.root == nil {
		return nil, errors.New("uninitialized tree")
	}
	raw, err := json.Marshal(t.header)
	if err != nil {
		return nil, err
	}
	var obj map[string]json.RawMessage
	_ = json.Unmarshal(raw, &obj)
	obj["view"] = t.viewJSON()
	return json.Marshal(obj)
}
func (d *Delta) Frame() wire.UIStreamDelta {
	var out wire.UIStreamDelta
	raw, _ := d.MarshalJSON()
	_ = decodeDTO(raw, &out)
	return out
}
func (d *Delta) MarshalJSON() ([]byte, error) {
	// Marshal only a generated envelope; override payloads from the lossless store.
	raw, err := json.Marshal(d.frame)
	if err != nil {
		return nil, err
	}
	var obj map[string]json.RawMessage
	_ = json.Unmarshal(raw, &obj)
	obj["operations"], err = json.Marshal(d.ops)
	if err != nil {
		return nil, err
	}
	return json.Marshal(obj)
}

// DecodeSnapshot bounds and validates source shape before any DTO normalization,
// then checks the entire native tree. Use AcceptSnapshot on an existing tree.
func DecodeSnapshot(raw []byte) (*Tree, error) {
	if err := validateRaw("UIStreamSnapshot", raw, FrameMost); err != nil {
		return nil, rejected(err)
	}
	var frame wire.UIStreamSnapshot
	if err := decodeDTO(raw, &frame); err != nil {
		return nil, rejected(err)
	}
	var obj map[string]json.RawMessage
	_ = json.Unmarshal(raw, &obj)
	root := parseNode(obj["view"], "view", frame.RootKey)
	frame.View = wire.View{} // never retain a mutable DTO tree internally
	t := &Tree{header: frame, root: root}
	if err := t.check(raw); err != nil { // raw passed validateRaw above
		return nil, rejected(err)
	}
	return t, nil
}

// AcceptSnapshot refuses other sessions/epochs and stale or conflicting same-
// version snapshots. Rotate runtime epochs by opening a new authorized Tree,
// not by passing arbitrary guest-chosen epochs to this method.
func (t *Tree) AcceptSnapshot(raw []byte) (*Tree, error) {
	if t == nil || t.root == nil {
		return nil, rejected(errors.New("uninitialized tree"))
	}
	next, err := DecodeSnapshot(raw)
	if err != nil {
		return nil, err
	}
	if next.Session() != t.Session() || next.Epoch() != t.Epoch() || next.RootKey() != t.RootKey() || next.Version() < t.Version() {
		return nil, rejected(errors.New("snapshot identity or version mismatch"))
	}
	if next.Version() == t.Version() && !bytes.Equal(next.viewJSON(), t.viewJSON()) {
		return nil, rejected(errors.New("conflicting same-version snapshot"))
	}
	return next, nil
}

func DecodeDelta(raw []byte) (*Delta, error) {
	if err := validateRaw("UIStreamDelta", raw, FrameMost); err != nil {
		return nil, rejected(err)
	}
	var frame wire.UIStreamDelta
	if err := decodeDTO(raw, &frame); err != nil {
		return nil, rejected(err)
	}
	var obj map[string]json.RawMessage
	_ = json.Unmarshal(raw, &obj)
	var ops []map[string]json.RawMessage
	_ = json.Unmarshal(obj["operations"], &ops)
	for _, op := range ops {
		for k, v := range op {
			op[k] = canonical(v, false)
		}
	}
	frame.Operations = nil
	return &Delta{frame: frame, ops: ops}, nil
}

// ValidatePageSnapshot checks original source JSON before a fitter or DTO can
// normalize duplicate fields, invalid UTF-8, lone surrogates or native presence.
func ValidatePageSnapshot(raw []byte) error {
	if err := validateRaw("UIPageSnapshot", raw, FrameMost); err != nil {
		return err
	}
	var source map[string]json.RawMessage
	if err := json.Unmarshal(raw, &source); err != nil {
		return err
	}
	framed, err := json.Marshal(map[string]any{"protocol": 1, "session": "source-validation", "epoch": "source-validation", "version": 1, "root_key": source["rootKey"], "view": source["view"]})
	if err != nil {
		return err
	}
	_, err = DecodeSnapshot(framed)
	return err
}

// DecodePageSnapshot returns a detached source header/native DTO after validating
// the original bytes. Callers retain raw view JSON rather than serializing this DTO.
func DecodePageSnapshot(raw []byte) (wire.UIPageSnapshot, error) {
	var out wire.UIPageSnapshot
	if err := ValidatePageSnapshot(raw); err != nil {
		return out, err
	}
	err := decodeDTO(raw, &out)
	return out, err
}

// DecodeClientEnvelope applies the same raw byte, number, duplicate-member and
// exact-shape boundary to the canonical main-app WebSocket request envelope.
func DecodeClientEnvelope(raw []byte) (wire.UIStreamClientEnvelope, error) {
	var out wire.UIStreamClientEnvelope
	if err := validateRaw("UIStreamClientEnvelope", raw, RequestMost); err != nil {
		return out, err
	}
	err := decodeDTO(raw, &out)
	return out, err
}

// ValidateActionResult checks original bounded presentation JSON, before DTO projection.
// It carries no callback, tree mutation or result-based authority.
func ValidateActionResult(raw []byte) error {
	return validateRaw("UIStreamActionResult", raw, RequestMost)
}

// DecodeRequest checks the 64 KiB boundary before parsing. Authorization,
// lifecycle quotas, deduplication and anti-replay fences remain caller duties.
func DecodeRequest(raw []byte) (wire.UIStreamRequest, error) {
	var out wire.UIStreamRequest
	if err := validateRaw("UIStreamRequest", raw, RequestMost); err != nil {
		return out, err
	}
	if err := decodeDTO(raw, &out); err != nil {
		return out, err
	}
	if len(out.State) > wire.StateMost {
		return out, errors.New("state exceeds UTF-8 byte limit")
	}
	return out, nil
}
func DecodeEvent(raw []byte) (wire.UIStreamEvent, error) {
	var out wire.UIStreamEvent
	if err := validateRaw("UIStreamEvent", raw, RequestMost); err != nil {
		return out, err
	}
	err := decodeDTO(raw, &out)
	return out, err
}

// validateRaw walks tokens first: bounds, UTF-8, duplicate members and nesting
// must be checked before schema/DTO parsers can discard source distinctions.
func validateRaw(def string, raw []byte, most int) error {
	if len(raw) > most {
		return fmt.Errorf("frame exceeds %d bytes", most)
	}
	if !utf8.Valid(raw) {
		return errors.New("invalid UTF-8")
	}
	if err := checkUnicodeEscapes(raw); err != nil {
		return err
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	if err := scanValue(dec, 0); err != nil {
		return err
	}
	if _, err := dec.Token(); err != io.EOF {
		return errors.New("trailing JSON data")
	}
	return wire.ValidateJSON(def, raw)
}
func scanValue(dec *json.Decoder, depth int) error {
	tok, err := dec.Token()
	if err != nil {
		return err
	}
	if number, ok := tok.(json.Number); ok {
		// Native DTOs draw finite float64 values. Bound exponent conversion before
		// exact integer normalization can allocate an enormous numerator/denominator.
		if _, err := strconv.ParseFloat(string(number), 64); err != nil {
			return errors.New("number outside finite native range")
		}
	}
	delim, ok := tok.(json.Delim)
	if !ok {
		return nil
	}
	if depth >= DepthMost {
		return errors.New("JSON depth exceeds limit")
	}
	switch delim {
	case '{':
		seen := map[string]bool{}
		for dec.More() {
			key, err := dec.Token()
			if err != nil {
				return err
			}
			name, ok := key.(string)
			if !ok {
				return errors.New("object key is not a string")
			}
			if seen[name] {
				return fmt.Errorf("duplicate JSON member %q", name)
			}
			seen[name] = true
			if err := scanValue(dec, depth+1); err != nil {
				return err
			}
		}
	case '[':
		for dec.More() {
			if err := scanValue(dec, depth+1); err != nil {
				return err
			}
		}
	default:
		return errors.New("unexpected delimiter")
	}
	_, err = dec.Token()
	return err
}

// DTO integer fields accept schema-valid integral literals such as 1.0 or 1e0;
// normalize only the detached semantic projection, not the lossless source.
func canonical(raw []byte, integers bool) json.RawMessage {
	var value any
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	_ = dec.Decode(&value)
	if integers {
		value = integerProjection(value)
	}
	out, _ := json.Marshal(value)
	return out
}
func integerProjection(v any) any {
	switch x := v.(type) {
	case json.Number:
		if n, _, err := big.ParseFloat(string(x), 10, uint(len(x))*4+64, big.ToNearestEven); err == nil && n.IsInt() && n.MantExp(nil) <= 64 {
			integer, _ := n.Int(nil)
			return json.Number(integer.String())
		}
	case map[string]any:
		for k, c := range x {
			x[k] = integerProjection(c)
		}
	case []any:
		for i, c := range x {
			x[i] = integerProjection(c)
		}
	}
	return v
}
func decodeDTO(raw []byte, out any) error {
	_, err := decodeNormalizedDTO(raw, out)
	return err
}

// decodeNormalizedDTO is decodeDTO that also gives the normalized bytes it
// decoded out from, which are canonical(raw, true) once it succeeds.
func decodeNormalizedDTO(raw []byte, out any) (json.RawMessage, error) {
	var source any
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.UseNumber()
	if err := dec.Decode(&source); err != nil {
		return nil, err
	}
	if err := exactShape(source, reflect.TypeOf(out)); err != nil {
		return nil, err
	}
	normalized, err := json.Marshal(integerProjection(source))
	if err != nil {
		return nil, err
	}
	dec = json.NewDecoder(bytes.NewReader(normalized))
	dec.DisallowUnknownFields()
	return normalized, dec.Decode(out)
}

var shapeFields sync.Map // reflect.Type -> exact generated JSON name/type map
var rawMessageType = reflect.TypeFor[json.RawMessage]()

// encoding/json's DisallowUnknownFields still accepts case-insensitive aliases.
// Validate exact nested names against GENERATED DTO tags before normalization;
// the stream schema separately restricts each flat Component's variant fields.
func exactShape(source any, typ reflect.Type) error {
	for typ.Kind() == reflect.Pointer {
		typ = typ.Elem()
	}
	if typ == rawMessageType {
		return nil
	}
	switch typ.Kind() {
	case reflect.Struct:
		obj, ok := source.(map[string]any)
		if !ok {
			return nil
		} // schema/DTO codec validates scalar shapes
		cached, ok := shapeFields.Load(typ)
		if !ok {
			fields := map[string]reflect.Type{}
			for i := 0; i < typ.NumField(); i++ {
				field := typ.Field(i)
				name := strings.Split(field.Tag.Get("json"), ",")[0]
				if name != "" && name != "-" {
					fields[name] = field.Type
				}
			}
			cached, _ = shapeFields.LoadOrStore(typ, fields)
		}
		fields := cached.(map[string]reflect.Type)
		for name, value := range obj {
			field, ok := fields[name]
			if !ok {
				return fmt.Errorf("unknown native JSON property %q", name)
			}
			if err := exactShape(value, field); err != nil {
				return err
			}
		}
	case reflect.Slice, reflect.Array:
		if values, ok := source.([]any); ok {
			for _, value := range values {
				if err := exactShape(value, typ.Elem()); err != nil {
					return err
				}
			}
		}
	case reflect.Map:
		if values, ok := source.(map[string]any); ok {
			for _, value := range values {
				if err := exactShape(value, typ.Elem()); err != nil {
					return err
				}
			}
		}
	}
	return nil
}

func parseNode(raw []byte, kind, rootKey string) *node {
	var props map[string]json.RawMessage
	_ = json.Unmarshal(raw, &props)
	n := &node{kind: kind, props: props, key: rootKey}
	// The children's field comes out before the other properties are made
	// canonical: each child makes its own properties canonical, and canonical
	// is idempotent, so the subtree is not encoded again at every level.
	field := n.childField()
	var children []json.RawMessage
	if field != "" {
		_ = json.Unmarshal(props[field], &children)
		delete(props, field)
	}
	for k, v := range props {
		props[k] = canonical(v, false)
	}
	if kind != "view" {
		_ = json.Unmarshal(props["key"], &n.key)
	}
	if field != "" {
		childKind := map[string]string{"view": "section", "section": "item", "item": "tile"}[kind]
		for _, child := range children {
			n.children = append(n.children, parseNode(child, childKind, ""))
		}
	}
	return n
}
func (n *node) childField() string {
	switch n.kind {
	case "view":
		return "sections"
	case "section":
		return "items"
	case "item":
		tag := text(n.props["type"])
		if tag == "grid" || tag == "carousel" {
			return "tiles"
		}
	}
	return ""
}
func text(raw []byte) string { var s string; _ = json.Unmarshal(raw, &s); return s }
func (n *node) json() json.RawMessage {
	props := make(map[string]json.RawMessage, len(n.props)+1)
	for k, v := range n.props {
		props[k] = v
	}
	if field := n.childField(); field != "" {
		children := make([]json.RawMessage, 0, len(n.children))
		for _, c := range n.children {
			children = append(children, c.json())
		}
		props[field], _ = json.Marshal(children)
	}
	out, _ := json.Marshal(props)
	return out
}
func (n *node) clone() *node {
	out := &node{key: n.key, kind: n.kind, props: n.props} // immutable payload maps are shared
	for _, c := range n.children {
		out.children = append(out.children, c.clone())
	}
	return out
}
func (n *node) find(key string) (found, parent *node, index int) {
	if n.key == key {
		return n, nil, 0
	}
	for i, c := range n.children {
		if c.key == key {
			return c, n, i
		}
		if f, p, j := c.find(key); f != nil {
			return f, p, j
		}
	}
	return nil, nil, 0
}

// check checks the whole tree. validated is a frame the caller already put
// through validateRaw as a UIStreamSnapshot (nil: none); validateRaw reads
// only the bytes, so the tree's own frame is not validated again when it is
// the same frame.
func (t *Tree) check(validated []byte) error {
	// Serialize the finished tree once; the frame check below and every
	// accessor after it reuse these bytes.
	t.view = t.root.json()
	frame, err := t.MarshalJSON()
	if err != nil {
		return err
	}
	if validated == nil || !bytes.Equal(frame, validated) {
		if err := validateRaw("UIStreamSnapshot", frame, FrameMost); err != nil {
			return err
		}
	} else {
		t.ownFrame = true // only a tree check accepts is returned
	}
	raw := t.view
	// Unknown native nested fields must not hide code/config behind legacy schemas
	// that intentionally tolerate additional properties on snapshot-only models.
	var v wire.View
	normalized, err := decodeNormalizedDTO(raw, &v)
	if err != nil {
		return err
	}
	// CheckViewJSON's checks on the source, not Component.MarshalJSON:
	// normalized is canonical(raw, true), made by the same decode, projection
	// and encoding. The frame above held to UIStreamSnapshot, whose view is
	// View narrowed, and normalized only writes integral numbers as integers,
	// so its View schema walk is not repeated.
	if err := wire.CheckValidatedViewJSON(normalized); err != nil {
		return err
	}
	if err := checkBindings(&v); err != nil {
		return err
	}
	if err := checkSourcePresence(t.view); err != nil { // t.view is t.root.json(), made above
		return err
	}
	seen := map[string]bool{}
	count := 0
	var visit func(*node, int) error
	visit = func(n *node, depth int) error {
		count++
		if count > NodesMost || depth > DepthMost {
			return errors.New("materialized tree bounds exceeded")
		}
		if err := wire.ValidateJSON("UIStreamKey", n.propsKey()); err != nil {
			return err
		}
		if seen[n.key] {
			return fmt.Errorf("duplicate key %q", n.key)
		}
		seen[n.key] = true
		for _, c := range n.children {
			if err := visit(c, depth+1); err != nil {
				return err
			}
		}
		return nil
	}
	if err := visit(t.root, 1); err != nil {
		return err
	}
	ids := map[string]bool{}
	for _, a := range wire.Actions(&v) {
		if err := wire.ValidateJSON("UIStreamID", mustJSON(a.ID)); err != nil {
			return err
		}
		if ids[a.ID] {
			return fmt.Errorf("duplicate action id %q", a.ID)
		}
		ids[a.ID] = true
	}
	return nil
}
func (n *node) propsKey() []byte     { return mustJSON(n.key) }
func mustJSON(v any) json.RawMessage { out, _ := json.Marshal(v); return out }
