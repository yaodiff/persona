package uistream

import (
	"testing"

	"github.com/nwverse/persona/sdk/go/wire"
)

// check no longer walks the View schema over the normalized view. For every
// tree check accepts (decoded or applied, numbers written as 1.0 and 1e2 too),
// that walk, and all of CheckViewJSON, would have passed.
func TestCheckedTreesPassCheckViewJSON(t *testing.T) {
	var trees []*Tree
	for _, name := range []string{"UIStreamSnapshot.keyed.json", "UIStreamSnapshot.after-operations.json", "UIStreamSnapshot.required-empty.json"} {
		trees = append(trees, treeFromFixture(t, name))
	}
	for seed := int64(0); seed < 150; seed++ {
		a, b := randomTree(t, seed, 1), randomTree(t, seed+997, 2)
		trees = append(trees, a, b)
		if d, _, err := Diff(a, b); err == nil {
			if applied, err := Apply(a, d); err == nil {
				trees = append(trees, applied)
			}
		}
	}
	for _, view := range []string{
		`{"sections":[{"key":"s","items":[{"type":"progress","key":"p","label":"L","ratio":1.0}]}]}`,
		`{"sections":[{"key":"s","items":[{"type":"progress","key":"p","label":"L","ratio":1e0},{"type":"progress","key":"q","label":"M","ratio":100e-2}]}]}`,
		`{"refresh_after":6e1,"sections":[{"key":"s","items":[{"type":"text","key":"t","text":"<b>&amp;"}]}]}`,
	} {
		tree, err := DecodeSnapshot(managerFrame(t, view))
		if err != nil {
			t.Fatalf("%s: %v", view, err)
		}
		trees = append(trees, tree)
	}
	if len(trees) < 300 {
		t.Fatalf("only %d trees", len(trees))
	}
	for i, tree := range trees {
		if err := wire.CheckViewJSON(canonical(tree.viewJSON(), true)); err != nil {
			t.Fatalf("tree %d: check accepted a view CheckViewJSON refuses: %v", i, err)
		}
	}
}
