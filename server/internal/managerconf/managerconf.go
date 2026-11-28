// Package managerconf is a manager's own configuration file, manager.json (docs/operations/config.md "manager.json"): the
// sections of the configuration that only the manager uses (connectors, extensions, bots, mail sign-ins, Lark,
// usage sources), which the built-in manager used to read and write in the hub's hub.json. A manager made in the app
// keeps them in the manager.json of its own directory, next to its identity (id, hub, token); the built-in manager,
// which the hub starts, in DefaultDir. MoveExcept takes the sections out of hub.json once.
package managerconf

import (
	"bytes"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/nwverse/persona/server/internal/assistantwire"
)

// File is a manager's configuration file in its directory.
const File = "manager.json"

// DefaultDir is the built-in manager's directory (the assistant the hub starts): managers/assistant in the data
// directory home, where a manager made in the app has its own (managers/<id>). Its manager.json holds no identity: the
// hub hands that manager its token itself.
func DefaultDir(home string) string {
	return filepath.Join(home, "managers", assistantwire.AssistantID)
}

// DefaultFile is the built-in manager's manager.json.
func DefaultFile(home string) string { return filepath.Join(DefaultDir(home), File) }

// Moved are the sections of hub.json that are the manager's: MoveExcept takes them into manager.json, and only the
// manager reads them there. A key with a dot is one key of a section whose other keys stay the hub's
// ("assistant.owner": the hub keeps assistant.external). A section added here later is moved by the next start
// (Marker lists what was).
var Moved = []string{
	"lark", "lark_gate", "connector_gate", "connector_auto_confirm", "github", "imessage", "google", "microsoft", "bots",
	"extensions", "plugins", "trusted_extension_markets", "extension_market", "extension_runtime", "ui_page_sessions", "usage", "sent_ledger_days", "sent_recall_hours",
	"agents", "kb", "ai", "inbox", "hook_timeout_minutes",
	"assistant.owner", "assistant.notes", "assistant.machine", "assistant.launcher", "assistant.memory_dreaming",
	"assistant.complete_agentless_todos", "assistant.auto_approve_reversible_settings", "assistant.supervision_batch_minutes",
	"assistant.task_execution_wait_seconds", "assistant.task_execution_legacy_fallback", "assistant.node_update_handoff_grace_seconds",
}

// MarkerKey is manager.json's record of the move: the sections it took over from hub.json (copied, or kept as
// manager.json had them), when, and for each the copy of those sections of hub.json kept beside it then. A section
// listed there is
// never moved again; one hub.json did not have stays unlisted, so an older build switched back that writes it there
// has it moved by the next start.
const MarkerKey = "moved_from_hub_json"

// Marker is MarkerKey's value.
type Marker struct {
	Keys []string  `json:"keys"`
	At   time.Time `json:"at"`
	// Backups are the copies of hub.json's sections, in the same directory, by the section moved when it was kept.
	Backups map[string]string `json:"backups,omitempty"`
	// FromHub are the HelloSections the hub handed over at the handshake that MoveFrom went through (copied, or kept as
	// manager.json had them), never again; HubBackups the copies of those it copied, as the hub had them, by key.
	FromHub    []string          `json:"from_hub,omitempty"`
	HubBackups map[string]string `json:"hub_backups,omitempty"`
}

// backupPrefix names the copies of hub.json's sections MoveExcept keeps (the node's path guard refuses every file
// named hub.json*). They hold only the sections taken over, never the rest of hub.json: the hub's token and database
// password do not belong in a manager's directory.
const backupPrefix = "hub.json.before-manager-json-"

// Report is what MoveExcept did.
type Report struct {
	// Moved are the sections it copied into manager.json this time.
	Moved []string
	// Backup is the copy of the sections it took over, as hub.json had them (a path; "" when it took over nothing).
	Backup string
	// Ignored are sections it took over that manager.json had already, with another value: manager.json's is kept
	// and read, hub.json's is not used (Backup has it).
	Ignored []string
	// Stale are sections hub.json has changed since they were moved: hub.json's copy is not read, manager.json's is.
	Stale []string
	// Untrimmed are the copies of a whole hub.json an earlier build kept that could not be cut down to their
	// sections (trimBackups), with why.
	Untrimmed []string
}

// MoveExcept copies the Moved sections of hubDir's hub.json that manager.json at file has not taken over yet into it,
// once, and records it (MarkerKey); a section manager.json has already keeps manager.json's value (Ignored when
// hub.json's differs). hub.json itself is not changed: an older build switched back still finds what it read before
// (later edits are manager.json's only). No hub.json, or nothing new to take over: manager.json is not touched. Stale
// lists the sections taken over whose value in hub.json is no longer what it was then (a hand edit of hub.json, or an
// older build's, which nothing reads).
//
// The keys in drop (a dotted one: that key of its section) are left out of hub.json: a built-in manager the hub did
// not start leaves HelloSections out, which the hub hands it from its own hub.json at the handshake (MoveFrom), not
// this machine's.
func MoveExcept(hubDir, file string, drop []string) (Report, error) {
	var rep Report
	hubPath := filepath.Join(hubDir, "hub.json")
	hubRaw, err := ReadFile(hubPath)
	if err == nil && hubRaw == nil {
		// Nothing to take over; the copies an earlier start kept are still cut down (no manager.json: none).
		if _, err := os.Lstat(file); err != nil {
			return rep, nil
		}
		err := editIf(file, func(o *Object) (bool, error) {
			m, err := readMarker(o)
			if err != nil {
				return false, fmt.Errorf("%s: %w", file, err)
			}
			rep.Untrimmed = trimBackups(filepath.Dir(file), m)
			return false, nil
		})
		return rep, err
	}
	if err != nil {
		return rep, err
	}
	hub, err := ParseObject(hubRaw)
	if err != nil {
		return rep, errors.New(hubPath + ": " + err.Error())
	}
	if len(drop) > 0 {
		if hub, err = ParseObject(Without(hub, drop)); err != nil {
			return rep, err
		}
	}
	return take(file, hub, Moved, true)
}

// HelloSections are what of hub.json the hub used to hand the built-in manager in its hello (before config.own).
// A built-in manager the hub did not start (assistant.external, or a service of its own) leaves them out of its own
// move (MoveExcept): its machine's hub.json may be a stub or a copy, and the hub's values were the ones in
// effect. The hub hands them over raw at the handshake instead (Legacy, assistantwire.Hello.LegacyConfig), and the
// manager takes them over once (MoveFrom). Of inbox only the models and the AI twin's machine came that way; the rest
// of it was read on the manager's own machine, and moves with MoveExcept.
var HelloSections = []string{"agents", "kb", "ai", "hook_timeout_minutes",
	"assistant.owner", "assistant.notes", "assistant.machine", "assistant.launcher",
	"inbox.triage_ai", "inbox.judge_ai", "inbox.agent.machine"}

// HubOnly are the HelloSections a built-in manager the hub did not start leaves out of its own move: all but
// inbox.agent.machine, which the AI twin always ran on as its own machine's hub.json said (the hello's only named the
// model purpose's machine). The hub's copy of it is still offered (MoveFrom), and taken only when that one has none.
var HubOnly = []string{"agents", "kb", "ai", "hook_timeout_minutes",
	"assistant.owner", "assistant.notes", "assistant.machine", "assistant.launcher",
	"inbox.triage_ai", "inbox.judge_ai"}

// InHello says whether the Moved key k is, or holds, one of the HelloSections.
func InHello(k string) bool {
	for _, h := range HelloSections {
		if h == k || strings.HasPrefix(h, k+".") {
			return true
		}
	}
	return false
}

// Legacy is the HelloSections hub.json's raw bytes have, as a JSON object; nil when it has none or does not read.
func Legacy(hubRaw []byte) []byte {
	o, err := ParseObject(hubRaw)
	if err != nil {
		return nil
	}
	b := Only(o, HelloSections)
	if string(b) == "{}" {
		return nil
	}
	return b
}

// MoveFrom takes the HelloSections the hub handed over (Legacy) into the manager.json at file, once each (the
// marker's FromHub): one manager.json lacks is copied in (Moved, a copy kept beside it as the hub had it), one it has
// keeps its value (Ignored when the hub's differs). A later change of the hub's is not taken (manager.json is the one
// to change).
func MoveFrom(legacy []byte, file string) (Report, error) {
	hub, err := ParseObject(legacy)
	if err != nil {
		return Report{}, fmt.Errorf("the hub's sections: %w", err)
	}
	var rep Report
	now := time.Now().UTC()
	err = editIf(file, func(o *Object) (bool, error) {
		m, err := readMarker(o)
		if err != nil {
			return false, fmt.Errorf("%s: %w", file, err)
		}
		var seen []string
		for _, k := range HelloSections {
			v, inHub := getPath(hub, k)
			if !inHub || slices.Contains(m.FromHub, k) {
				continue
			}
			seen = append(seen, k)
			if have, mine := getPath(o, k); !mine {
				if err := setPath(o, k, v); err != nil {
					return false, err
				}
				rep.Moved = append(rep.Moved, k)
			} else if !sameJSON(have, v) {
				rep.Ignored = append(rep.Ignored, k)
			}
		}
		if len(seen) == 0 {
			return false, nil
		}
		dir := filepath.Dir(file)
		if len(rep.Moved) > 0 {
			name := backupPrefix + now.Format("20060102T150405.000000000Z")
			if err := WriteFileAtomic(filepath.Join(dir, name), Only(hub, rep.Moved), 0o600); err != nil {
				return false, err
			}
			rep.Backup = filepath.Join(dir, name)
			if m.HubBackups == nil {
				m.HubBackups = map[string]string{}
			}
			for _, k := range rep.Moved {
				m.HubBackups[k] = name
			}
		}
		m.FromHub = append(m.FromHub, seen...)
		raw, err := MarshalNoEscape(m)
		if err != nil {
			return false, err
		}
		o.SetRaw(MarkerKey, raw)
		return true, nil
	})
	return rep, err
}

// take copies keys of hub that manager.json at file has not taken over yet into it, records them in the marker, and
// keeps a copy of what it took beside it (MoveExcept). withStale: say which sections hub has changed since they moved.
func take(file string, hub *Object, keys []string, withStale bool) (Report, error) {
	var rep Report
	now := time.Now().UTC()
	err := editIf(file, func(o *Object) (bool, error) {
		m, err := readMarker(o)
		if err != nil {
			return false, fmt.Errorf("%s: %w", file, err)
		}
		if withStale {
			rep.Stale = staleBy(filepath.Dir(file), m.Keys, m.Backups, hub)
		}
		rep.Untrimmed = trimBackups(filepath.Dir(file), m)
		var took []string
		for _, k := range keys {
			v, inHub := getPath(hub, k)
			if slices.Contains(m.Keys, k) || !inHub {
				continue
			}
			took = append(took, k)
			if have, mine := getPath(o, k); !mine {
				if err := setPath(o, k, v); err != nil {
					return false, err
				}
				rep.Moved = append(rep.Moved, k)
			} else if !sameJSON(have, v) {
				rep.Ignored = append(rep.Ignored, k)
			}
		}
		if len(took) == 0 {
			return false, nil
		}
		name := backupPrefix + now.Format("20060102T150405.000000000Z")
		if err := WriteFileAtomic(filepath.Join(filepath.Dir(file), name), Only(hub, took), 0o600); err != nil {
			return false, err
		}
		rep.Backup = filepath.Join(filepath.Dir(file), name)
		if m.Backups == nil {
			m.Backups = map[string]string{}
		}
		for _, k := range took {
			m.Backups[k] = name
		}
		m.Keys = append(m.Keys, took...)
		m.At = now
		raw, err := MarshalNoEscape(m)
		if err != nil {
			return false, err
		}
		o.SetRaw(MarkerKey, raw)
		return true, nil
	})
	return rep, err
}

// readMarker is manager.json's MarkerKey (none: the zero Marker).
func readMarker(o *Object) (Marker, error) {
	var m Marker
	if raw, ok := o.Get(MarkerKey); ok {
		if err := json.Unmarshal(raw, &m); err != nil {
			return m, errors.New(MarkerKey + ": " + err.Error())
		}
	}
	return m, nil
}

// backupName is name when it names one of MoveExcept's copies, directly in the manager's directory; else "": a
// marker edited by hand or broken never points MoveExcept at another file there (manager.json itself, a hub.json).
func backupName(name string) string {
	if name != "" && name == filepath.Base(name) && strings.HasPrefix(name, backupPrefix) {
		return name
	}
	return ""
}

// trimBackups cuts the copies of hub.json in dir that hold more than the sections taken from them (an earlier build
// kept hub.json whole) down to those sections, so the hub's token and database password do not stay in the
// manager's directory: a copy the marker names, to the sections it names it for; one it doesn't (its manager.json
// was not written then), to the Moved ones. A temporary file one of them was being written to when a start was cut
// short (WriteFileAtomic's, beside it) is removed: MoveExcept writes them only under the directory's lock, which this
// holds. What it could not cut is returned, with why (a copy that is not JSON, or not a plain file, is left as it
// is, and said).
func trimBackups(dir string, m Marker) []string {
	byName := map[string][]string{}
	for _, k := range m.Keys {
		if name := backupName(m.Backups[k]); name != "" {
			byName[name] = append(byName[name], k)
		}
	}
	for _, k := range m.FromHub {
		if name := backupName(m.HubBackups[k]); name != "" {
			byName[name] = append(byName[name], k)
		}
	}
	entries, err := os.ReadDir(dir)
	if err != nil {
		return nil
	}
	var failed []string
	for _, e := range entries {
		name := e.Name()
		if backupName(name) == "" {
			continue
		}
		path := filepath.Join(dir, name)
		if strings.Contains(name, tempInfix) {
			if err := os.Remove(path); err != nil {
				failed = append(failed, path+": "+err.Error())
			}
			continue
		}
		if !e.Type().IsRegular() {
			failed = append(failed, path+": not a plain file")
			continue
		}
		keys, listed := byName[name]
		if !listed {
			keys = Moved
		}
		b, err := os.ReadFile(path)
		if err != nil {
			failed = append(failed, path+": "+err.Error())
			continue
		}
		o, err := ParseObject(b)
		if err != nil {
			failed = append(failed, path+": "+err.Error())
			continue
		}
		cut := Only(o, keys)
		if bytes.Equal(cut, o.Marshal()) {
			continue
		}
		if err := WriteFileAtomic(path, cut, 0o600); err != nil {
			failed = append(failed, path+": "+err.Error())
		}
	}
	return failed
}

// staleBy are the keys taken over (by the copies kept of them) whose value in hub differs from the copy kept then (or
// which hub has dropped: nothing to warn about, it is not read either way).
func staleBy(dir string, keys []string, backups map[string]string, hub *Object) []string {
	copies := map[string]*Object{}
	var out []string
	for _, k := range keys {
		name := backupName(backups[k])
		if name == "" {
			continue
		}
		then, ok := copies[name]
		if !ok {
			if b, err := os.ReadFile(filepath.Join(dir, name)); err == nil {
				then, _ = ParseObject(b)
			}
			copies[name] = then
		}
		if then == nil {
			continue
		}
		now, okNow := getPath(hub, k)
		was, okThen := getPath(then, k)
		if okNow && (!okThen || !sameJSON(now, was)) {
			out = append(out, k)
		}
	}
	return out
}

// sameJSON says whether a and b are the same JSON value, whatever their spacing or the order of their keys.
func sameJSON(a, b json.RawMessage) bool {
	ca, errA := canonicalJSON(a)
	cb, errB := canonicalJSON(b)
	if errA != nil || errB != nil {
		return bytes.Equal(a, b)
	}
	return bytes.Equal(ca, cb)
}

// canonicalJSON is raw re-encoded with its objects' keys sorted and its numbers as written.
func canonicalJSON(raw json.RawMessage) ([]byte, error) {
	d := json.NewDecoder(bytes.NewReader(raw))
	d.UseNumber()
	var v any
	if err := d.Decode(&v); err != nil {
		return nil, err
	}
	return json.Marshal(v)
}

// In are the Moved sections the JSON object raw (a hub.json) has, in Moved's order; none when it does not read.
func In(raw []byte) []string {
	o, err := ParseObject(raw)
	if err != nil {
		return nil
	}
	var out []string
	for _, k := range Moved {
		if _, ok := getPath(o, k); ok {
			out = append(out, k)
		}
	}
	return out
}

// Sections reads Moved and explicitly requested manager-only keys at file as a JSON object, for a
// manager's configuration struct to unmarshal; identity and the marker are left out.
func Sections(file string, local ...string) ([]byte, error) {
	b, err := readConfig(file)
	if err != nil {
		return nil, err
	}
	if b == nil {
		return []byte("{}"), nil
	}
	o, err := ParseObject(b)
	if err != nil {
		return nil, errors.New(file + ": " + err.Error())
	}
	for _, k := range Moved { // a section split by its dotted keys that is not an object would read as none of them
		if section, _, dotted := strings.Cut(k, "."); dotted {
			if _, ok := o.Get(section); ok {
				if _, err := o.Object(section); err != nil {
					return nil, fmt.Errorf("%s: %w", file, err)
				}
			}
		}
	}
	return Only(o, append(slices.Clone(Moved), local...)), nil
}

// Only is o with only the keys in keys (a dotted one: that key of the section), compact.
func Only(o *Object, keys []string) []byte {
	out := &Object{vals: map[string]json.RawMessage{}}
	for _, k := range o.keys {
		if slices.Contains(keys, k) {
			out.SetRaw(k, o.vals[k])
			continue
		}
		var sub []string
		for _, key := range keys {
			if section, rest, ok := strings.Cut(key, "."); ok && section == k {
				sub = append(sub, rest)
			}
		}
		if nested, err := o.Object(k); err == nil && len(sub) > 0 {
			if part, _ := ParseObject(Only(nested, sub)); len(part.keys) > 0 {
				out.SetRaw(k, part.Marshal())
			}
		}
	}
	return out.Marshal()
}

// Without is o without the keys in keys (a dotted one: that key of the section), compact.
func Without(o *Object, keys []string) []byte {
	out := &Object{vals: map[string]json.RawMessage{}}
	for _, k := range o.keys {
		if slices.Contains(keys, k) {
			continue
		}
		var sub []string
		for _, key := range keys {
			if section, rest, ok := strings.Cut(key, "."); ok && section == k {
				sub = append(sub, rest)
			}
		}
		if nested, err := o.Object(k); err == nil && len(sub) > 0 {
			out.SetRaw(k, Without(nested, sub))
			continue
		}
		out.SetRaw(k, o.vals[k])
	}
	return out.Marshal()
}

// getPath is key's value in o; a dotted key is that key of the section (absent when the section is not an object).
func getPath(o *Object, key string) (json.RawMessage, bool) {
	section, rest, dotted := strings.Cut(key, ".")
	if !dotted {
		return o.Get(key)
	}
	if _, ok := o.Get(section); !ok {
		return nil, false
	}
	nested, err := o.Object(section)
	if err != nil {
		return nil, false
	}
	return getPath(nested, rest)
}

// setPath sets key in o; a dotted key sets that key of the section (made when missing).
func setPath(o *Object, key string, v json.RawMessage) error {
	section, rest, dotted := strings.Cut(key, ".")
	if !dotted {
		o.SetRaw(key, v)
		return nil
	}
	nested, err := o.Object(section)
	if err != nil {
		return err
	}
	if err := setPath(nested, rest, v); err != nil {
		return err
	}
	o.SetObject(section, nested)
	return nil
}
