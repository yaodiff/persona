package wire

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// streamViewInputs are views to validate as a stream's: the examples, the
// stream snapshots' views, and edge cases for numbers and names.
func streamViewInputs(t *testing.T) [][]byte {
	t.Helper()
	dir := filepath.Join("..", "..", "..", "schema", "extension", "examples")
	var out [][]byte
	for _, pattern := range []string{"View.*.json", "UIStreamSnapshot.*.json", "traces/UIStreamSnapshot.*.json"} {
		names, _ := filepath.Glob(filepath.Join(dir, pattern))
		for _, name := range names {
			raw, err := os.ReadFile(name)
			if err != nil {
				t.Fatal(err)
			}
			var frame struct {
				View json.RawMessage `json:"view"`
			}
			if json.Unmarshal(raw, &frame) == nil && frame.View != nil {
				raw = frame.View
			}
			out = append(out, raw)
		}
	}
	item := func(s string) []byte {
		return []byte(`{"sections":[{"key":"s","items":[` + s + `]}]}`)
	}
	for _, ratio := range []string{"0.5", "1", "1.0", "1e0", "100e-2", "1.0000000000000001", "1.00001", "-0", "-1e-400", "1e309"} {
		out = append(out, item(`{"type":"progress","key":"p","label":"L","ratio":`+ratio+`}`))
	}
	for _, n := range []string{"300", "301"} {
		var items []byte
		count := 0
		fmt.Sscan(n, &count)
		for i := 0; i < count; i++ {
			if i > 0 {
				items = append(items, ',')
			}
			items = append(items, fmt.Sprintf(`{"type":"text","key":"t%d","text":"x"}`, i)...)
		}
		out = append(out, []byte(`{"sections":[{"key":"s","items":[`+string(items)+`]}]}`))
	}
	out = append(out,
		item(`{"type":"field","key":"f","name":"`+ReservedName+`x","kind":"text","label":"Hub"}`),
		item(`{"type":"row","key":"r","title":"T","control":{"kind":"slider","name":"v","value":1e309,"min":0,"max":1e310}}`),
		item(`{"type":"row","key":"r","title":"T","control":{"kind":"slider","name":"v","value":5,"min":0,"max":10}}`),
		[]byte(`{"pages":{"a":{}},"sections":[]}`), []byte(`{"sections":[],"assistant_only":true}`),
		[]byte(`{"state":"`+string(make([]byte, 0))+`","sections":[]}`), []byte(`{"sections":[`), []byte(`[]`), []byte(`null`))
	return out
}

// DecodeCheckedStreamViewJSON refuses exactly what validating UIStreamView and
// then CheckViewJSON refused, and gives the View a fresh decode gives.
func TestDecodeCheckedStreamViewJSONIsBoth(t *testing.T) {
	accepted, refused := 0, 0
	for i, raw := range streamViewInputs(t) {
		got, err := DecodeCheckedStreamViewJSON(raw)
		want := ValidateJSON("UIStreamView", raw)
		if want == nil {
			want = CheckViewJSON(raw)
		}
		if (err == nil) != (want == nil) {
			t.Fatalf("input %d %.80s: %v, before %v", i, raw, err, want)
		}
		if err != nil {
			if err.Error() != want.Error() {
				t.Fatalf("input %d %.80s: refused as before, but now %q (was %q)", i, raw, err, want)
			}
			refused++
			continue
		}
		accepted++
		var fresh View
		if json.Unmarshal(raw, &fresh) != nil || !reflect.DeepEqual(got, fresh) {
			t.Fatalf("input %d: the checked View is not raw decoded", i)
		}
	}
	t.Logf("accepted %d, refused %d", accepted, refused)
	if accepted < 10 || refused < 5 {
		t.Fatalf("accepted %d, refused %d", accepted, refused)
	}
}

// For a view that holds to View, CheckValidatedViewJSON answers as
// CheckViewJSON does, refusals and their messages included.
func TestCheckValidatedViewJSONIsCheckViewJSON(t *testing.T) {
	compared, refused := 0, 0
	for i, raw := range streamViewInputs(t) {
		if ValidateJSON("View", raw) != nil {
			continue
		}
		compared++
		got, want := CheckValidatedViewJSON(raw), CheckViewJSON(raw)
		if (got == nil) != (want == nil) || got != nil && got.Error() != want.Error() {
			t.Fatalf("input %d %.80s: %v, CheckViewJSON %v", i, raw, got, want)
		}
		if got != nil {
			refused++
		}
	}
	if compared < 10 || refused < 1 {
		t.Fatalf("compared %d, refused %d", compared, refused)
	}
}
