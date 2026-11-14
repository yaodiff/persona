package uistream

import (
	"bytes"
	"testing"

	"github.com/nwverse/persona/sdk/go/wire"
)

// What check hands CheckViewJSON is canonical(raw, true) byte for byte, for
// every view decodeNormalizedDTO accepts, integral numbers written as 1.0 and
// 1e2 included.
func TestNormalizedIsCanonicalWithIntegers(t *testing.T) {
	views := [][]byte{
		[]byte(`{"sections":[{"key":"s","items":[{"type":"progress","key":"p","ratio":1.0,"label":"<b>"}]}]}`),
		[]byte(`{"title":"T\u00e9","sections":[{"key":"s","items":[{"type":"stat","key":"n","value":1e2},{"type":"stat","key":"m","value":-0.50}]}]}`),
		[]byte(` { "sections" : [ ] } `),
	}
	for seed := int64(0); seed < 100; seed++ {
		views = append(views, randomTree(t, seed, 1).ViewJSON())
	}
	accepted := 0
	for i, raw := range views {
		var v wire.View
		got, err := decodeNormalizedDTO(raw, &v)
		if err != nil {
			continue
		}
		accepted++
		if want := canonical(raw, true); !bytes.Equal(got, want) {
			t.Fatalf("view %d:\n got %s\nwant %s", i, got, want)
		}
	}
	if accepted < 100 {
		t.Fatalf("only %d views decoded", accepted)
	}
}
