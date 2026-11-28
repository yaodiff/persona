package toolcli

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"unicode/utf8"

	"github.com/nwverse/persona/sdk/go/wire"
	"github.com/nwverse/persona/server/internal/proto"
)

type storeFileField struct {
	Name   string `json:"name"`
	Type   string `json:"type,omitempty"`
	File   string `json:"file,omitempty"`
	Delete bool   `json:"delete,omitempty"`
}
type storeFileManifest struct {
	Operation string           `json:"operation"`
	Vault     string           `json:"vault"`
	Item      string           `json:"item,omitempty"`
	Title     string           `json:"title,omitempty"`
	Category  string           `json:"category,omitempty"`
	Fields    []storeFileField `json:"fields"`
}

// readPrivateFile refuses links, devices and group/world-readable files before reading, and verifies that
// the opened file is the one inspected. Errors are deliberately constant (no file contents or OS messages).
func readPrivateFile(path string, most int) ([]byte, error) {
	bad := errors.New("could not read a private regular source file (0600 required)")
	st, err := os.Lstat(path)
	if err != nil || !st.Mode().IsRegular() || st.Mode().Perm()&0077 != 0 {
		return nil, bad
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, bad
	}
	defer f.Close()
	actual, err := f.Stat()
	if err != nil || !os.SameFile(st, actual) || !actual.Mode().IsRegular() || actual.Mode().Perm()&0077 != 0 {
		return nil, bad
	}
	b, err := io.ReadAll(io.LimitReader(f, int64(most+1)))
	if err != nil || len(b) > most {
		clear(b)
		return nil, bad
	}
	return b, nil
}
func storeManifest(raw []byte) (storeFileManifest, wire.SecretStoreTarget, error) {
	var m storeFileManifest
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if d.Decode(&m) != nil || d.Decode(new(any)) != io.EOF {
		return m, wire.SecretStoreTarget{}, errors.New("invalid secret storage file manifest; fields accept name, type, file and delete only")
	}
	t := wire.SecretStoreTarget{Operation: m.Operation, Vault: m.Vault, Item: m.Item, Title: m.Title, Category: m.Category}
	for _, f := range m.Fields {
		if f.Delete {
			if m.Operation != "edit" || f.File != "" || f.Type != "" {
				return m, t, errors.New("delete fields are allowed only on edit without type or file")
			}
			t.Fields = append(t.Fields, wire.SecretStoreField{Name: f.Name, Delete: true})
			continue
		}
		if f.File == "" {
			if m.Operation != "edit" || f.Type == "" {
				return m, t, errors.New("every secret field requires a local file, or on edit an explicit type to update in place")
			}
			t.Fields = append(t.Fields, wire.SecretStoreField{Name: f.Name, Type: f.Type, KeepValue: true})
			continue
		}
		typ := f.Type
		if typ == "" {
			typ = "CONCEALED"
		}
		t.Fields = append(t.Fields, wire.SecretStoreField{Name: f.Name, Type: typ})
	}
	return m, t, t.Validate()
}
func secretStore(ctx context.Context, args []string, stdin io.Reader, stdout, stderr io.Writer) int {
	fail := failer{name: "personad tool secret_store", stderr: stderr}
	var raw []byte
	var err error
	switch {
	case len(args) == 2 && args[0] == "--from-file":
		if args[1] == "-" {
			raw, err = readStdin(stdin)
		} else {
			raw, err = readPrivateFile(args[1], 64<<10)
		}
	case len(args) == 1:
		if args[0] == "-" {
			raw, err = readStdin(stdin)
		} else {
			raw = []byte(args[0])
		}
	default:
		return fail.fail(exitUsage, "personad tool secret_store --from-file <private metadata manifest | ->", 0)
	}
	if err != nil {
		return fail.fail(exitUsage, "could not read the secret storage manifest", 0)
	}
	defer clear(raw)
	m, target, err := storeManifest(raw)
	if err != nil {
		return fail.fail(exitUsage, err.Error(), 0)
	}
	res, err := call(ctx, "secret_store", map[string]any{"phase": "prepare", "target": target})
	if err != nil {
		if ctx.Err() != nil && !errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return fail.unreachable(ctx, err)
		}
		return fail.fail(exitUnavailable, "secret storage approval could not be reached", 0)
	}
	if res.IsError {
		code, retry := classify(res)
		return fail.fail(code, firstText(res), retry)
	}
	var key wire.SecretStoreResult
	if json.Unmarshal([]byte(firstText(res)), &key) != nil || key.KeyID == "" || key.Key == "" || key.Target.Validate() != nil {
		return fail.fail(exitFailed, "invalid secret storage preparation", 0)
	}
	// Only the metadata may have been canonicalized; fields must be exactly those whose files were named.
	wanted, _ := json.Marshal(target.Fields)
	got, _ := json.Marshal(key.Target.Fields)
	if !bytes.Equal(wanted, got) {
		return fail.fail(exitFailed, "secret storage fields changed", 0)
	}
	values := make([]proto.SecretStoreValue, 0, len(m.Fields))
	defer func() {
		for _, v := range values {
			clear(v.Value)
		}
	}()
	for _, f := range m.Fields {
		if f.Delete || f.File == "" {
			continue
		}
		b, e := readPrivateFile(f.File, wire.SecretStoreValueMost)
		if e != nil || len(b) == 0 || !utf8.Valid(b) || bytes.IndexByte(b, 0) >= 0 {
			clear(b)
			return fail.fail(exitUsage, "a field file is unavailable, empty, too large or not UTF-8 text; no values were sent", 0)
		}
		values = append(values, proto.SecretStoreValue{Name: f.Name, Value: b})
	}
	sealed, err := proto.SealSecretStore(key.Key, key.KeyID, values)
	if err != nil {
		return fail.fail(exitFailed, "secret storage encryption failed", 0)
	}
	defer clear(sealed)
	res, err = call(ctx, "secret_store", map[string]any{"phase": "commit", "target": key.Target, "key_id": key.KeyID, "sealed": base64.StdEncoding.EncodeToString(sealed)})
	if err != nil {
		if ctx.Err() != nil && !errors.Is(ctx.Err(), context.DeadlineExceeded) {
			return fail.fail(exitInterrupted, "interrupted: secret storage result unavailable; check metadata before retrying", 0)
		}
		return fail.fail(exitUnavailable, "secret storage result unavailable; check metadata before retrying", 0)
	}
	if res.IsError {
		code, retry := classify(res)
		return fail.fail(code, firstText(res), retry)
	}
	fmt.Fprintln(stdout, firstText(res))
	return exitOK
}
