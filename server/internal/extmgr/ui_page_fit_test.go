package extmgr

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math/rand"
	"os"
	"path/filepath"
	"sort"
	"testing"

	"github.com/nwverse/persona/server/internal/extension"
	"github.com/nwverse/persona/server/internal/i18n"
)

type fitInput struct {
	name    string
	raw     []byte
	slot    extension.UISlot
	version int
	lang    i18n.Lang
}

func fitInputs(t *testing.T) []fitInput {
	t.Helper()
	dir := filepath.Join("..", "..", "..", "schema", "extension", "examples")
	names, _ := filepath.Glob(filepath.Join(dir, "View.*.json"))
	var views [][]byte
	for _, name := range names {
		raw, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		views = append(views, raw)
	}
	// A switch row reads on or off in the app's language below UI version 6, and
	// a card (compact slot) has no search: these tell language and slot apart.
	views = append(views, renderView(2, 3, ""),
		[]byte(`{"sections":[{"key":"s","items":[{"type":"row","key":"r","title":"Light","control":{"kind":"switch","name":"light","on":true,"action":{"id":"toggle"}}}]}]}`),
		[]byte(`{"search":{"placeholder":"Find","action":{"id":"find"}},"sections":[{"key":"s","items":[{"type":"text","key":"t","text":"hi"}]}]}`))
	names = append(names, "bench", "switch", "search")
	var out []fitInput
	for i, raw := range views {
		for _, slot := range extension.UISlots {
			for version := 1; version <= extension.UIVersion; version++ {
				for _, lang := range []i18n.Lang{i18n.En, i18n.Lang("zh-Hans")} {
					out = append(out, fitInput{name: names[i], raw: raw, slot: slot, version: version, lang: lang})
				}
			}
		}
	}
	return out
}

// The memo answers exactly as FitNative, errors included, whatever the order of
// the inputs; and the inputs differ in every key the memo compares.
func TestPageFitMemoMatchesFitNative(t *testing.T) {
	inputs := fitInputs(t)
	want := map[int]string{}
	differs := map[string]bool{}
	byKey := map[string]string{}
	for i, in := range inputs {
		fitted, err := extension.FitNative(in.raw, in.slot, in.version, in.lang)
		want[i] = string(fitted) + "|" + errText(err)
		// Record, per pair of inputs differing in one key only, whether the answer differs.
		for key, other := range map[string]string{
			"slot":    string(in.raw) + "|" + string(rune(in.version)) + "|" + string(in.lang),
			"version": string(in.raw) + "|" + in.slot.Name + "|" + string(in.lang),
			"lang":    string(in.raw) + "|" + in.slot.Name + "|" + string(rune(in.version)),
		} {
			if prev, ok := byKey[key+other]; ok && prev != want[i] {
				differs[key] = true
			}
			byKey[key+other] = want[i]
		}
	}
	for _, key := range []string{"slot", "version", "lang"} {
		if !differs[key] {
			t.Fatalf("no two inputs differing only in %s fit differently: the comparison proves nothing", key)
		}
	}
	// The memo keeps one answer, so a wrong key shows only when two inputs that
	// differ in that key alone come one after the other: walk the inputs with
	// each key innermost in turn, then in a shuffled order.
	var orders [][]int
	for _, key := range []func(fitInput) string{
		func(in fitInput) string {
			return string(in.raw) + in.slot.Name + string(rune(in.version)) + string(in.lang)
		},
		func(in fitInput) string {
			return string(in.raw) + in.slot.Name + string(in.lang) + string(rune(in.version))
		},
		func(in fitInput) string {
			return string(in.raw) + string(rune(in.version)) + string(in.lang) + in.slot.Name
		},
	} {
		order := make([]int, len(inputs))
		for i := range order {
			order[i] = i
		}
		sort.SliceStable(order, func(a, b int) bool { return key(inputs[order[a]]) < key(inputs[order[b]]) })
		orders = append(orders, order)
	}
	orders = append(orders, rand.New(rand.NewSource(7)).Perm(len(inputs)))
	var memo pageFitMemo
	for _, order := range orders {
		for _, i := range order {
			in := inputs[i]
			fitted, err := memo.fitNative(in.raw, in.slot, in.version, in.lang)
			if got := string(fitted) + "|" + errText(err); got != want[i] {
				t.Fatalf("%s %s v%d %s: memo %q, FitNative %q", in.name, in.slot.Name, in.version, in.lang, got, want[i])
			}
			// The same input right after gives the same answer.
			again, err := memo.fitNative(in.raw, in.slot, in.version, in.lang)
			if got := string(again) + "|" + errText(err); got != want[i] {
				t.Fatalf("%s repeated: %q, want %q", in.name, got, want[i])
			}
		}
	}
}

// The same input twice is fitted once: the second answer is the first's bytes.
func TestPageFitMemoKeepsTheAnswer(t *testing.T) {
	var memo pageFitMemo
	raw := renderView(2, 3, "")
	first, err := memo.fitNative(raw, extension.UISlots[0], extension.UIVersion, i18n.En)
	if err != nil {
		t.Fatal(err)
	}
	second, err := memo.fitNative(bytes.Clone(raw), extension.UISlots[0], extension.UIVersion, i18n.En)
	if err != nil || len(first) == 0 || &first[0] != &second[0] {
		t.Fatalf("the same input was fitted again: %v", err)
	}
	// A refused view is not kept.
	bad := json.RawMessage(`{"sections":[{"key":"s","items":[{"type":"no-such-component","key":"x"}]}]}`)
	if _, err := memo.fitNative(bad, extension.UISlots[0], extension.UIVersion, i18n.En); err == nil {
		t.Fatal("an unknown component was fitted")
	}
	if f := memo.last.Load(); !bytes.Equal(f.raw, raw) {
		t.Fatal("a refused view replaced the kept answer")
	}
	// A nil memo fits every time.
	var none *pageFitMemo
	if _, err := none.fitNative(raw, extension.UISlots[0], extension.UIVersion, i18n.En); err != nil {
		t.Fatal(err)
	}
}

func errText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// BenchmarkPagePrepareRedraw redraws an unchanged page the way a source does,
// each time with a new native version, with the session's memo and without.
func BenchmarkPagePrepareRedraw(b *testing.B) {
	for _, size := range []struct{ sections, items int }{{4, 8}, {10, 25}} {
		for _, memo := range []bool{false, true} {
			name := fmt.Sprintf("%dx%d/memo=%v", size.sections, size.items, memo)
			b.Run(name, func(b *testing.B) {
				h := &Manager{}
				view := renderView(size.sections, size.items, "")
				var fit *pageFitMemo
				if memo {
					fit = &pageFitMemo{}
				}
				state := renderState(nil, view, "page")
				state.fit = fit
				tree, _, err := h.pagePrepare(state, false, "")
				if err != nil {
					b.Fatal(err)
				}
				b.ReportAllocs()
				for i := 0; b.Loop(); i++ {
					state := renderState(tree, view, "page")
					state.fit, state.native.Version = fit, int64(i+2)
					if next, frame, err := h.pagePrepare(state, false, ""); err != nil || frame != nil || next != tree {
						b.Fatal(err)
					}
				}
			})
		}
	}
}
