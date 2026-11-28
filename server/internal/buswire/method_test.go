package buswire_test

import (
	"context"
	"encoding/json"
	"testing"

	"github.com/nwverse/persona/server/internal/buswire"
)

type callerFunc func(context.Context, string, any, any) error

func (f callerFunc) Call(ctx context.Context, name string, params, out any) error {
	return f(ctx, name, params, out)
}

func TestCallDecodesTheTypedResult(t *testing.T) {
	type query struct {
		Limit int `json:"limit"`
	}
	type result struct {
		Count int `json:"count"`
	}
	m := buswire.Method[query, result]{Name: "example.list"}
	ctx := t.Context()
	c := callerFunc(func(gotCtx context.Context, name string, params, out any) error {
		raw, err := json.Marshal(params)
		if gotCtx != ctx || name != "example.list" || err != nil || string(raw) != `{"limit":2}` {
			t.Fatalf("call = %s %s (%v)", name, raw, err)
		}
		return json.Unmarshal([]byte(`{"count":2,"future_field":true}`), out)
	})

	if got, err := buswire.Call(ctx, c, m, query{Limit: 2}); err != nil || got.Count != 2 {
		t.Fatalf("result = %+v, %v", got, err)
	}
}

func TestCallWithNoneUsesAnEmptyObjectAndNilDestination(t *testing.T) {
	m := buswire.Method[buswire.None, buswire.None]{Name: "example.ack"}
	c := callerFunc(func(_ context.Context, _ string, params, out any) error {
		raw, err := json.Marshal(params)
		if string(raw) != "{}" || err != nil || out != nil {
			t.Fatalf("call = %s (%v), destination type %T", raw, err, out)
		}
		return context.Canceled
	})

	if _, err := buswire.Call(t.Context(), c, m, buswire.None{}); err != context.Canceled {
		t.Fatalf("call error = %v, want context.Canceled", err)
	}
}
