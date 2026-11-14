package uistream

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// parseNodeOracle is parseNode as it was before the children's field was
// left out of the properties made canonical.
func parseNodeOracle(raw []byte, kind, rootKey string) *node {
	var props map[string]json.RawMessage
	_ = json.Unmarshal(raw, &props)
	for k, v := range props {
		props[k] = canonical(v, false)
	}
	n := &node{kind: kind, props: props, key: rootKey}
	if kind != "view" {
		_ = json.Unmarshal(props["key"], &n.key)
	}
	field := n.childField()
	if field != "" {
		var children []json.RawMessage
		_ = json.Unmarshal(props[field], &children)
		delete(props, field)
		childKind := map[string]string{"view": "section", "section": "item", "item": "tile"}[kind]
		for _, child := range children {
			n.children = append(n.children, parseNodeOracle(child, childKind, ""))
		}
	}
	return n
}

// describe writes everything a node holds: kind, key, each property's bytes
// in key order, and its children in order.
func describe(n *node) string {
	if n == nil {
		return "nil"
	}
	var b strings.Builder
	keys := make([]string, 0, len(n.props))
	for k := range n.props {
		keys = append(keys, k)
	}
	sortStrings(keys)
	fmt.Fprintf(&b, "(%s %q props=%v", n.kind, n.key, n.props == nil)
	for _, k := range keys {
		fmt.Fprintf(&b, " %q=%s", k, n.props[k])
	}
	b.WriteString(" [")
	for _, c := range n.children {
		b.WriteString(describe(c))
	}
	b.WriteString("])")
	return b.String()
}

func sortStrings(s []string) {
	for i := 1; i < len(s); i++ {
		for j := i; j > 0 && s[j] < s[j-1]; j-- {
			s[j], s[j-1] = s[j-1], s[j]
		}
	}
}

// parseNode builds exactly the node the previous one did, for the example
// snapshots, random trees, and sources written in non-canonical forms.
func TestParseNodeMatchesOracle(t *testing.T) {
	var views [][]byte
	names, _ := filepath.Glob(filepath.Join("..", "..", "..", "..", "schema", "extension", "examples", "*Snapshot*.json"))
	more, _ := filepath.Glob(filepath.Join("..", "..", "..", "..", "schema", "extension", "examples", "traces", "*Snapshot*.json"))
	for _, name := range append(names, more...) {
		raw, err := os.ReadFile(name)
		if err != nil {
			t.Fatal(err)
		}
		var frame struct {
			View json.RawMessage `json:"view"`
		}
		if json.Unmarshal(raw, &frame) == nil && frame.View != nil {
			views = append(views, frame.View)
		}
	}
	for seed := int64(0); seed < 100; seed++ {
		views = append(views, randomTree(t, seed, 1).ViewJSON())
	}
	views = append(views,
		[]byte(`{ "sections" : [ { "key" : "s" , "items" : [ { "type" : "progress" , "key" : "\u0061" , "ratio" : 1.0 , "label" : "<b>&amp;" } ] } ] , "title" : "T\u00e9" }`),
		[]byte(`{"sections":[{"key":"s","items":[{"type":"gr\u0069d","key":"g","tiles":[{"type":"stat","key":"t1","value":1e2},{"type":"stat","key":"t2","value":-0.50}]}]}]}`),
		[]byte(`{"sections":[{"key":"s","items":[{"type":"text","key":"x","tiles":[{"key":"kept-as-a-property"}]}]}]}`),
		[]byte(`{"sections":null}`), []byte(`{"sections":{"key":"not-a-list"}}`), []byte(`[]`), []byte(`null`),
	)
	if len(views) < 110 {
		t.Fatalf("only %d views", len(views))
	}
	for i, raw := range views {
		for _, kind := range []string{"view", "section", "item"} {
			got, want := describe(parseNode(raw, kind, "page")), describe(parseNodeOracle(raw, kind, "page"))
			if got != want {
				t.Fatalf("view %d as %s:\n got %s\nwant %s", i, kind, got, want)
			}
		}
	}
}
