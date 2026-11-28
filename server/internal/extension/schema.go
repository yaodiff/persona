package extension

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"net/mail"
	"net/url"
	"regexp"
	"slices"
	"strings"
	"unicode/utf8"

	"github.com/nwverse/persona/server/internal/i18n"
	"github.com/nwverse/persona/server/internal/lazyre"
)

// ConfigMost is the largest configuration value kept.
const ConfigMost = 64 << 10

// Allowed configuration property section hints for grouping settings in clients.
const (
	SectionConnection = "connection"
	SectionChannel    = "channel"
	SectionInbox      = "inbox"
	SectionAdvanced   = "advanced"
)

// Schema is a plugin's configuration in the settings-form subset, its properties in manifest order. Entry lists are
// editable only for clients that opt in; other clients receive a compatibility projection.
type Schema struct {
	Fields []Field `json:"fields"`
}

// Field is one property, as the form draws it.
type Field struct {
	Name        string `json:"name"`
	Type        string `json:"type"` // string | integer | number | boolean | array | object
	Title       string `json:"title,omitempty"`
	Description string `json:"description,omitempty"`
	// Section is an optional UI grouping hint (connection | channel | inbox | advanced).
	Section  string          `json:"section,omitempty"`
	Default  json.RawMessage `json:"default,omitempty"`
	Required bool            `json:"required,omitempty"`
	Enum     []Choice        `json:"enum,omitempty"`
	Format   string          `json:"format,omitempty"` // uri | email | hostname | machine | launcher
	// OS, for format machine: only machines of these systems (x-os: darwin, linux, windows); none, any.
	OS []string `json:"os,omitempty"`
	// Confirm (x-confirm): what a form asks before a switch is turned on (a boolean, or a machine of a machine list),
	// for settings with a cost the user should agree to (the plugin cua's "Don't auto-lock while the extension is on", #1507).
	Confirm     string   `json:"confirm,omitempty"`
	Pattern     string   `json:"pattern,omitempty"`
	MinLength   *int     `json:"min_length,omitempty"`
	MaxLength   *int     `json:"max_length,omitempty"`
	Minimum     *float64 `json:"minimum,omitempty"`
	Maximum     *float64 `json:"maximum,omitempty"`
	Items       *Field   `json:"items,omitempty"` // array: its string or flat-entry object field
	MinItems    *int     `json:"min_items,omitempty"`
	MaxItems    *int     `json:"max_items,omitempty"`
	UniqueItems bool     `json:"unique_items,omitempty"`
	Fields      []Field  `json:"fields,omitempty"` // object: its properties
	// Secret (x-secret) is the one write-only secret each entry of an object list owns.
	Secret *SecretSpec `json:"secret,omitempty"`
	// From (x-from) maps entry fields to the fixed top-level fields an upgrade moves into its first entry.
	From map[string]string `json:"-"`
	// Entry marks the flat object held by an entry list; its reserved _id is host-owned.
	Entry bool `json:"-"`
	// ClearsSecret marks an entry address whose configured-network target change clears that entry's secret on save.
	ClearsSecret bool `json:"clears_secret,omitempty"`
	// Step (x-step) is an integer stepper's increment, not a constraint on configuration values.
	Step *int64 `json:"step,omitempty"`

	pattern *regexp.Regexp
}

// Choice is one value of an enum, with its label.
type Choice struct {
	Value json.RawMessage `json:"value"`
	Title string          `json:"title,omitempty"`
}

var (
	describing = []string{"title", "description", "default", "x-confirm", "section"}
	keysFor    = map[string][]string{
		"string":  {"enum", "oneOf", "format", "x-os", "pattern", "minLength", "maxLength"},
		"integer": {"enum", "oneOf", "minimum", "maximum", "x-step"},
		"number":  {"enum", "oneOf", "minimum", "maximum"},
		"boolean": {},
		"array":   {"items", "minItems", "maxItems", "uniqueItems", "x-secret", "x-from"},
		"object":  {"properties", "required"},
	}
	// machine: one of the hub's machines, by id: a form draws a list of them as a switch each and one of them as a
	// picker (x-os narrows both). launcher: one of the hub's launchers, by name: a form draws a picker of them.
	formats      = []string{"uri", "email", "hostname", "machine", "launcher"}
	sections     = []string{SectionConnection, SectionChannel, SectionInbox, SectionAdvanced}
	systems      = []string{"darwin", "linux", "windows"}
	hostnameLike = lazyre.New(`^([a-zA-Z0-9]([a-zA-Z0-9-]{0,61}[a-zA-Z0-9])?\.)*[a-zA-Z0-9]([a-zA-Z0-9-]{0,61}[a-zA-Z0-9])?$`)
)

// ParseSchema reads a configuration schema, refusing anything outside the subset; nil, nil when raw is empty.
func ParseSchema(raw json.RawMessage) (*Schema, error) {
	if len(bytes.TrimSpace(raw)) == 0 {
		return nil, nil
	}
	var root map[string]json.RawMessage
	if err := json.Unmarshal(raw, &root); err != nil {
		return nil, errors.New("an object")
	}
	for k := range root {
		if !slices.Contains([]string{"$schema", "type", "properties", "required", "title", "description"}, k) {
			return nil, fmt.Errorf("%q is not in the subset the forms draw", k)
		}
	}
	if typeOf(root) != "object" {
		return nil, errors.New(`the root is {"type": "object", "properties": {…}}`)
	}
	fields, err := parseProperties(root, "", 0)
	if err != nil {
		return nil, err
	}
	if err := validateEntryMigrationSources(fields); err != nil {
		return nil, err
	}
	return &Schema{Fields: fields}, nil
}

func typeOf(m map[string]json.RawMessage) string {
	var t string
	_ = json.Unmarshal(m["type"], &t)
	return t
}

func parseProperties(obj map[string]json.RawMessage, at string, depth int) ([]Field, error) {
	names, values, err := ordered(obj["properties"])
	if err != nil {
		return nil, fmt.Errorf("%sproperties: %v", at, err)
	}
	var required []string
	if raw, ok := obj["required"]; ok {
		if err := json.Unmarshal(raw, &required); err != nil {
			return nil, fmt.Errorf("%srequired: a list of names", at)
		}
	}
	for _, r := range required {
		if !slices.Contains(names, r) {
			return nil, fmt.Errorf("%srequired: %q is not a property", at, r)
		}
	}
	fields := make([]Field, 0, len(names))
	for i, name := range names {
		f, err := parseField(name, values[i], at+name+".", depth)
		if err != nil {
			return nil, err
		}
		f.Required = slices.Contains(required, name)
		fields = append(fields, f)
	}
	return fields, nil
}

// ordered are an object's keys in the order written, with their values.
func ordered(raw json.RawMessage) ([]string, []json.RawMessage, error) {
	if len(raw) == 0 {
		return nil, nil, errors.New("missing")
	}
	dec := json.NewDecoder(bytes.NewReader(raw))
	if t, err := dec.Token(); err != nil || t != json.Delim('{') {
		return nil, nil, errors.New("an object")
	}
	var names []string
	var values []json.RawMessage
	for dec.More() {
		t, err := dec.Token()
		if err != nil {
			return nil, nil, err
		}
		name := t.(string)
		var v json.RawMessage
		if err := dec.Decode(&v); err != nil {
			return nil, nil, err
		}
		if slices.Contains(names, name) {
			return nil, nil, fmt.Errorf("%q twice", name)
		}
		names = append(names, name)
		values = append(values, v)
	}
	return names, values, nil
}

var fieldName = lazyre.New(`^[A-Za-z_][A-Za-z0-9_-]{0,63}$`)

func parseField(name string, raw json.RawMessage, at string, depth int) (Field, error) {
	return parseFieldMode(name, raw, at, depth, false)
}

func parseFieldMode(name string, raw json.RawMessage, at string, depth int, entryItem bool) (Field, error) {
	f := Field{Name: name}
	fail := func(format string, args ...any) (Field, error) {
		return Field{}, fmt.Errorf(strings.TrimSuffix(at, ".")+": "+format, args...)
	}
	if !fieldName.MatchString(name) {
		return fail("not a usable property name")
	}
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil {
		return fail("an object")
	}
	f.Type = typeOf(m)
	allowed, ok := keysFor[f.Type]
	if !ok {
		return fail("type %q: string, integer, number, boolean, array or object", f.Type)
	}
	for k := range m {
		if k != "type" && !slices.Contains(describing, k) && !slices.Contains(allowed, k) {
			return fail("%q is not in the subset the forms draw for a %s", k, f.Type)
		}
	}
	str := func(k string, out *string) error {
		if raw, ok := m[k]; ok {
			if err := json.Unmarshal(raw, out); err != nil {
				return fmt.Errorf("%s: a string", k)
			}
		}
		return nil
	}
	num := func(k string) (*float64, error) {
		raw, ok := m[k]
		if !ok {
			return nil, nil
		}
		var v float64
		if err := json.Unmarshal(raw, &v); err != nil {
			return nil, fmt.Errorf("%s: a number", k)
		}
		return &v, nil
	}
	count := func(k string) (*int, error) {
		raw, ok := m[k]
		if !ok {
			return nil, nil
		}
		var v int
		if err := json.Unmarshal(raw, &v); err != nil || v < 0 {
			return nil, fmt.Errorf("%s: a whole number, 0 or more", k)
		}
		return &v, nil
	}
	var errs []error
	errs = append(errs, str("title", &f.Title), str("description", &f.Description), str("format", &f.Format), str("pattern", &f.Pattern), str("x-confirm", &f.Confirm))
	if raw, ok := m["section"]; ok {
		if bytes.Equal(bytes.TrimSpace(raw), []byte("null")) || json.Unmarshal(raw, &f.Section) != nil {
			errs = append(errs, errors.New("section: a string"))
		}
	}
	var err error
	if f.MinLength, err = count("minLength"); err != nil {
		errs = append(errs, err)
	}
	if f.MaxLength, err = count("maxLength"); err != nil {
		errs = append(errs, err)
	}
	if f.MinItems, err = count("minItems"); err != nil {
		errs = append(errs, err)
	}
	if f.MaxItems, err = count("maxItems"); err != nil {
		errs = append(errs, err)
	}
	if f.Minimum, err = num("minimum"); err != nil {
		errs = append(errs, err)
	}
	if f.Maximum, err = num("maximum"); err != nil {
		errs = append(errs, err)
	}
	if raw, ok := m["x-step"]; ok {
		// Keep the hint exact for clients that decode JSON numbers as doubles.
		var step int64
		if err := json.Unmarshal(raw, &step); err != nil || step < 1 || step > 9007199254740991 {
			errs = append(errs, errors.New("x-step: a whole number from 1 to 9007199254740991"))
		} else {
			f.Step = &step
		}
	}
	if raw, ok := m["uniqueItems"]; ok && json.Unmarshal(raw, &f.UniqueItems) != nil {
		errs = append(errs, errors.New("uniqueItems: true or false"))
	}
	if raw, ok := m["x-secret"]; ok {
		var spec SecretSpec
		if err := strict(raw, &spec); err != nil {
			errs = append(errs, fmt.Errorf("x-secret: %v", err))
		} else {
			f.Secret = &spec
		}
	}
	if raw, ok := m["x-from"]; ok {
		if err := json.Unmarshal(raw, &f.From); err != nil {
			errs = append(errs, errors.New("x-from: an object of entry field to old field names"))
		}
	}
	if err := errors.Join(errs...); err != nil {
		return fail("%v", err)
	}
	if f.Format != "" && !slices.Contains(formats, f.Format) {
		return fail("format %q: uri, email, hostname, machine or launcher", f.Format)
	}
	if f.Section != "" && !slices.Contains(sections, f.Section) {
		return fail("section %q: connection, channel, inbox or advanced", f.Section)
	}
	if raw, ok := m["x-os"]; ok {
		if f.Format != "machine" {
			return fail("x-os: only with format machine")
		}
		if json.Unmarshal(raw, &f.OS) != nil || len(f.OS) == 0 || slices.ContainsFunc(f.OS, func(os string) bool { return !slices.Contains(systems, os) }) {
			return fail("x-os: a list of darwin, linux, windows")
		}
	}
	if f.Pattern != "" {
		re, err := regexp.Compile(f.Pattern)
		if err != nil {
			return fail("pattern: %v", err)
		}
		f.pattern = re
	}
	if _, has := m["enum"]; has {
		var values []json.RawMessage
		if err := json.Unmarshal(m["enum"], &values); err != nil || len(values) == 0 {
			return fail("enum: a list of values")
		}
		for _, v := range values {
			f.Enum = append(f.Enum, Choice{Value: v})
		}
	}
	if _, has := m["oneOf"]; has {
		if f.Enum != nil {
			return fail("enum and oneOf: one of them")
		}
		var choices []map[string]json.RawMessage
		if err := json.Unmarshal(m["oneOf"], &choices); err != nil || len(choices) == 0 {
			return fail(`oneOf: only [{"const": …, "title": "…"}] (an enum with labels)`)
		}
		for _, c := range choices {
			v, ok := c["const"]
			var title string
			if !ok || len(c) > 2 || (len(c) == 2 && json.Unmarshal(c["title"], &title) != nil) {
				return fail(`oneOf: only [{"const": …, "title": "…"}] (an enum with labels)`)
			}
			f.Enum = append(f.Enum, Choice{Value: v, Title: title})
		}
	}
	switch f.Type {
	case "array":
		raw, ok := m["items"]
		if !ok {
			return fail("items: required")
		}
		item, err := parseFieldMode("items", raw, at+"items.", depth+1, depth == 0)
		if err != nil {
			return Field{}, err
		}
		if item.Type != "string" && !(item.Type == "object" && depth == 0) {
			return fail("items: only strings, or one flat object in a top-level entry list")
		}
		if item.Type == "object" {
			item.Entry = true
		}
		f.Items = &item
	case "object":
		if depth > 0 && !entryItem {
			return fail("an object only at the first level or as a top-level array's entry")
		}
		f.Entry = entryItem
		var err error
		if f.Fields, err = parseProperties(m, at, depth+1); err != nil {
			return Field{}, err
		}
		for _, sub := range f.Fields {
			if sub.Type == "object" {
				return fail("an object only at the first level or as a top-level array's entry")
			}
		}
	}
	for _, c := range f.Enum {
		if err := f.check(c.Value, at); err != nil {
			return fail("enum value %s: %v", c.Value, err)
		}
	}
	if raw, ok := m["default"]; ok {
		if err := f.check(raw, at); err != nil {
			return fail("default: %v", err)
		}
		f.Default = raw
	}
	if err := validateEntryList(f); err != nil {
		return fail("%v", err)
	}
	return f, nil
}

// FieldError is a configuration value that does not fit: where (dotted) and why.
type FieldError struct {
	cause   error
	Path    string `json:"path"`
	Message string `json:"message"`
}

func (e *FieldError) Error() string { return e.Path + ": " + e.Message }

// Check holds a configuration value to the schema: an object with only the schema's properties, each of the right
// kind, the required ones present (or defaulted). A nil schema takes only an empty object.
func (s *Schema) Check(raw json.RawMessage) error {
	if len(raw) > ConfigMost {
		return fieldError("", i18n.ErrEnglish("extension-status.schema_bytes", i18n.Args{"count": len(raw), "max": ConfigMost}))
	}
	if len(bytes.TrimSpace(raw)) == 0 {
		raw = json.RawMessage(`{}`)
	}
	var fields []Field
	if s != nil {
		fields = s.Fields
	}
	if err := checkObject(fields, raw, "", false); err != nil {
		return err
	}
	return checkEntryNamespace(fields, raw)
}

// CheckSaved checks the saved values but lets required fields added by a new version be missing, leaving them to
// needs_setup.
func (s *Schema) CheckSaved(raw json.RawMessage) error {
	if s == nil {
		return s.Check(raw)
	}
	copy := *s
	copy.Fields = optionalFields(s.Fields)
	return copy.Check(raw)
}

// CheckPartial holds a proposed part of a configuration to the schema: every supplied property is known and has the
// right kind, bounds and choices, while required properties it does not name are left for the existing configuration.
func (s *Schema) CheckPartial(raw json.RawMessage) error {
	if s == nil {
		return s.Check(raw)
	}
	copy := *s
	copy.Fields = optionalFields(s.Fields)
	return copy.Check(raw)
}

func optionalFields(fields []Field) []Field {
	out := slices.Clone(fields)
	for i := range out {
		out[i] = optionalField(out[i])
	}
	return out
}

func optionalField(f Field) Field {
	f.Required = false
	f.Fields = optionalFields(f.Fields)
	if f.Items != nil {
		item := optionalField(*f.Items)
		f.Items = &item
	}
	return f
}

// CheckPreservingEntries checks an older client's ordinary fields strictly while applying CheckSaved's missing-required
// allowance only to entry lists it cannot edit. Their saved values are still type-, identity- and bounds-checked.
func (s *Schema) CheckPreservingEntries(raw json.RawMessage) error {
	if s == nil {
		return s.Check(raw)
	}
	copy := *s
	copy.Fields = slices.Clone(s.Fields)
	for i, field := range copy.Fields {
		if !field.EntryList() {
			continue
		}
		field.Required = false
		item := *field.Items
		item.Required = false
		item.Fields = slices.Clone(item.Fields)
		for j := range item.Fields {
			item.Fields[j].Required = false
		}
		field.Items = &item
		copy.Fields[i] = field
	}
	return copy.Check(raw)
}

func checkObject(fields []Field, raw json.RawMessage, at string, entry bool) error {
	var m map[string]json.RawMessage
	if err := json.Unmarshal(raw, &m); err != nil || m == nil {
		return fieldError(strings.TrimSuffix(at, "."), i18n.Err("extension-status.schema_object"))
	}
	for k := range m {
		if entry && k == entryIDField {
			continue
		}
		if !slices.ContainsFunc(fields, func(f Field) bool { return f.Name == k }) {
			return fieldError(at+k, i18n.Err("extension-status.schema_unknown_field"))
		}
	}
	if entry {
		var id string
		rawID, ok := m[entryIDField]
		if !ok || json.Unmarshal(rawID, &id) != nil || !secretPattern.MatchString(id) {
			return fieldError(at+entryIDField, i18n.ErrEnglish("extension-status.schema_entry_id"))
		}
	}
	for _, f := range fields {
		v, ok := m[f.Name]
		if !ok || string(v) == "null" {
			if f.Required && f.Default == nil {
				return fieldError(at+f.Name, i18n.Err("extension-status.schema_required"))
			}
			continue
		}
		if err := f.check(v, at+f.Name); err != nil {
			var fe *FieldError
			if errors.As(err, &fe) {
				return fe
			}
			return fieldError(at+f.Name, err)
		}
	}
	return nil
}

// check holds one value to the field.
func (f *Field) check(raw json.RawMessage, at string) error {
	switch f.Type {
	case "object":
		return checkObject(f.Fields, raw, at+".", f.Entry)
	case "array":
		var items []json.RawMessage
		if err := json.Unmarshal(raw, &items); err != nil {
			return i18n.Err("extension-status.schema_array")
		}
		if f.MinItems != nil && len(items) < *f.MinItems {
			return i18n.Err("extension-status.schema_min_items", i18n.Args{"count": *f.MinItems})
		}
		if f.MaxItems != nil && len(items) > *f.MaxItems {
			return i18n.Err("extension-status.schema_max_items", i18n.Args{"count": *f.MaxItems})
		}
		entryList := f.EntryList()
		seen := map[string]bool{}
		seenIDs := map[string]bool{}
		for i, item := range items {
			if err := f.Items.check(item, fmt.Sprintf("%s[%d]", at, i)); err != nil {
				if entryList {
					var fe *FieldError
					if errors.As(err, &fe) {
						return fe
					}
				}
				return i18n.Err("extension-status.schema_item", i18n.Args{"count": i + 1, "err": err})
			}
			if f.UniqueItems && seen[string(item)] {
				return i18n.Err("extension-status.schema_duplicate", i18n.Args{"count": i + 1})
			}
			if entryList {
				var row map[string]json.RawMessage
				_ = json.Unmarshal(item, &row)
				id := string(row[entryIDField])
				if seenIDs[id] {
					return fieldError(fmt.Sprintf("%s[%d].%s", at, i, entryIDField), i18n.ErrEnglish("extension-status.schema_entry_id_duplicate"))
				}
				seenIDs[id] = true
			}
			seen[string(item)] = true
		}
		return nil
	case "boolean":
		var b bool
		if json.Unmarshal(raw, &b) != nil {
			return i18n.Err("extension-status.schema_boolean")
		}
		return nil
	case "integer", "number":
		var n float64
		if json.Unmarshal(raw, &n) != nil {
			return i18n.Err("extension-status.schema_number")
		}
		if f.Type == "integer" && n != math.Trunc(n) {
			return i18n.Err("extension-status.schema_integer")
		}
		if f.Minimum != nil && n < *f.Minimum {
			return i18n.Err("extension-status.schema_minimum", i18n.Args{"min": *f.Minimum})
		}
		if f.Maximum != nil && n > *f.Maximum {
			return i18n.Err("extension-status.schema_maximum", i18n.Args{"max": *f.Maximum})
		}
	case "string":
		var s string
		if json.Unmarshal(raw, &s) != nil {
			return i18n.Err("extension-status.schema_string")
		}
		n := utf8.RuneCountInString(s)
		if f.MinLength != nil && n < *f.MinLength {
			return i18n.Err("extension-status.schema_min_length", i18n.Args{"count": *f.MinLength})
		}
		if f.MaxLength != nil && n > *f.MaxLength {
			return i18n.Err("extension-status.schema_max_length", i18n.Args{"count": *f.MaxLength})
		}
		if f.pattern != nil && !f.pattern.MatchString(s) {
			return i18n.Err("extension-status.schema_format")
		}
		switch f.Format {
		case "uri":
			if u, err := url.Parse(s); err != nil || u.Scheme == "" || u.Host == "" {
				return i18n.Err("extension-status.schema_uri")
			}
		case "email":
			if a, err := mail.ParseAddress(s); err != nil || a.Address != s {
				return i18n.Err("extension-status.schema_email")
			}
		case "hostname":
			if !hostnameLike.MatchString(s) {
				return i18n.Err("extension-status.schema_hostname")
			}
		case "machine":
			if strings.TrimSpace(s) != s || s == "" {
				return i18n.Err("extension-status.schema_machine")
			}
		case "launcher":
			if strings.TrimSpace(s) != s || s == "" {
				return i18n.Err("extension-status.schema_launcher")
			}
		}
	}
	if len(f.Enum) > 0 && !slices.ContainsFunc(f.Enum, func(c Choice) bool { return sameJSON(c.Value, raw) }) {
		return i18n.Err("extension-status.schema_enum")
	}
	return nil
}

func sameJSON(a, b json.RawMessage) bool {
	var x, y any
	if json.Unmarshal(a, &x) != nil || json.Unmarshal(b, &y) != nil {
		return false
	}
	ab, _ := json.Marshal(x)
	bb, _ := json.Marshal(y)
	return bytes.Equal(ab, bb)
}

// Effective is a configuration with the schema's defaults filled in where it has no value: what the plugin gets.
func (s *Schema) Effective(raw json.RawMessage) json.RawMessage {
	m := map[string]json.RawMessage{}
	_ = json.Unmarshal(raw, &m)
	if s != nil {
		fill(s.Fields, m)
	}
	b, _ := json.Marshal(m)
	return b
}

func fill(fields []Field, m map[string]json.RawMessage) {
	for _, f := range fields {
		v, ok := m[f.Name]
		if f.Type == "object" {
			sub := map[string]json.RawMessage{}
			if ok {
				_ = json.Unmarshal(v, &sub)
			}
			fill(f.Fields, sub)
			if len(sub) > 0 {
				m[f.Name], _ = json.Marshal(sub)
			}
			continue
		}
		if (!ok || string(v) == "null") && f.Default != nil {
			m[f.Name] = f.Default
		}
	}
}
