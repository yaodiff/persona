package assistantwire

import (
	"bytes"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"testing"
)

func TestIntegrationProjectionSamplesRoundTrip(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("..", "..", "..", "spec", "fixtures", "integrations-projection", "views.json"))
	if err != nil {
		t.Fatal(err)
	}
	var samples struct {
		Cases []struct {
			Name     string                     `json:"name"`
			Expected map[string]json.RawMessage `json:"expected"`
		} `json:"cases"`
	}
	if err := json.Unmarshal(data, &samples); err != nil {
		t.Fatal(err)
	}
	for _, sample := range samples.Cases {
		t.Run(sample.Name, func(t *testing.T) {
			for key, definition := range map[string]string{"catalog": "IntegrationCatalog", "detail": "IntegrationDetail"} {
				raw, ok := sample.Expected[key]
				if !ok {
					continue
				}
				view := integrationsProjectionDefs[definition]()
				decoder := json.NewDecoder(bytes.NewReader(raw))
				decoder.DisallowUnknownFields()
				if err := decoder.Decode(view); err != nil {
					t.Fatal(err)
				}
				got, err := json.Marshal(view)
				if err != nil {
					t.Fatal(err)
				}
				var original, encoded any
				if err := json.Unmarshal(raw, &original); err != nil {
					t.Fatal(err)
				}
				if err := json.Unmarshal(got, &encoded); err != nil {
					t.Fatal(err)
				}
				if !reflect.DeepEqual(original, encoded) {
					t.Fatalf("%s changed from %s to %s", key, raw, got)
				}
			}
		})
	}
}

func TestIntegrationProjectionEnumsKeepUnknownValues(t *testing.T) {
	var v IntegrationCatalogItem
	if err := json.Unmarshal([]byte(`{"capabilities":["data","future_capability"]}`), &v); err != nil {
		t.Fatal(err)
	}
	if len(v.Capabilities) != 2 || !v.Capabilities[0].Valid() || v.Capabilities[1].Valid() || v.Capabilities[1] != "future_capability" {
		t.Fatalf("unknown capability %+v", v.Capabilities)
	}
}
