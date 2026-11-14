package wire

import (
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

// DecodeCheckedViewJSON refuses exactly what CheckViewJSON refuses, with the
// same error, and gives the View a fresh decode of raw gives.
func TestDecodeCheckedViewJSONIsCheckViewJSON(t *testing.T) {
	names, _ := filepath.Glob(filepath.Join("..", "..", "..", "schema", "extension", "examples", "View.*.json"))
	var views [][]byte
	for _, name := range names {
		raw, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		views = append(views, raw)
	}
	views = append(views,
		[]byte(`{"sections":[{"key":"s","items":[{"type":"field","key":"f","name":"`+ReservedName+`x","kind":"text","label":"Hub"}]}]}`),
		[]byte(`{"sections":[{"key":"s","items":[{"type":"field","key":"f","name":"plain","kind":"text","label":"Mine"}]}]}`),
		[]byte(`{"sections":[`), []byte(`[]`))
	accepted, refused := 0, 0
	for i, raw := range views {
		got, err := DecodeCheckedViewJSON(raw)
		want := CheckViewJSON(raw)
		if (err == nil) != (want == nil) || err != nil && err.Error() != want.Error() {
			t.Fatalf("view %d: %v, CheckViewJSON %v", i, err, want)
		}
		if err != nil {
			refused++
			continue
		}
		accepted++
		var fresh View
		if json.Unmarshal(raw, &fresh) != nil || !reflect.DeepEqual(got, fresh) {
			t.Fatalf("view %d: the checked View is not raw decoded", i)
		}
	}
	if accepted < 5 || refused < 2 {
		t.Fatalf("accepted %d, refused %d", accepted, refused)
	}
}
