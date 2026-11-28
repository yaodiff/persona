package kb

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"
	"time"
)

// Regression tests for #2546: project aliases snowballed across projects and dedupe merged projects the model
// doubted. All names are placeholders.

// A compression's aliases that name another live project, are too generic, or repeat the project's own name are
// not added; the rest are, up to maxProjectAliases.
func TestEvolveProjectAliasesAreValidated(t *testing.T) {
	e := testEngine(t, nil)
	_ = e.Store.PutProject(&Project{Slug: "alpha", Name: "Alpha 发布", Aliases: []string{"阿尔法"}, Status: "active"})
	_ = e.Store.PutProject(&Project{Slug: "beta", Name: "Beta 迁移", Status: "active"})
	c := &Chat{ID: "lark-f1", Kind: "group", Name: "示例群"}
	now := time.Now().UTC()

	slug := e.evolveProject("Beta 迁移", "short", []string{"alpha 发布", "阿尔法", "ss", "项目", "Beta迁移", "贝塔", "贝塔"}, "", c, true, "", now)
	if slug != "beta" {
		t.Fatalf("slug = %q", slug)
	}
	beta, _ := e.Store.Project("beta")
	if strings.Join(beta.Aliases, "|") != "贝塔" {
		t.Fatalf("beta aliases = %v", beta.Aliases)
	}
	// Alpha's own name still finds Alpha, not Beta.
	if p := e.FindProject("阿尔法"); p == nil || p.Slug != "alpha" {
		t.Fatalf("阿尔法 -> %+v", p)
	}

	var many []string
	for i := range 30 {
		many = append(many, fmt.Sprintf("贝塔%02d", i))
	}
	e.evolveProject("Beta 迁移", "short", many, "", c, true, "", now)
	beta, _ = e.Store.Project("beta")
	if len(beta.Aliases) != maxProjectAliases || beta.Aliases[0] != "贝塔" {
		t.Fatalf("beta has %d aliases: %v", len(beta.Aliases), beta.Aliases)
	}
}

// A mention found through an alias does not carry the names of a third project into the one it found.
func TestEvolveProjectDoesNotSnowball(t *testing.T) {
	e := testEngine(t, nil)
	_ = e.Store.PutProject(&Project{Slug: "alpha", Name: "Alpha 发布", Status: "active"})
	_ = e.Store.PutProject(&Project{Slug: "beta", Name: "Beta 迁移", Aliases: []string{"迁移二期"}, Status: "active"})
	c := &Chat{ID: "lark-f1", Kind: "group", Name: "示例群"}
	slug := e.evolveProject("新叫法", "short", []string{"迁移二期", "Alpha 发布"}, "", c, true, "", time.Now().UTC())
	if slug != "beta" {
		t.Fatalf("slug = %q", slug)
	}
	beta, _ := e.Store.Project("beta")
	if slices.Contains(beta.Aliases, "Alpha 发布") {
		t.Fatalf("beta took alpha's name: %v", beta.Aliases)
	}
	if p := e.FindProject("Alpha 发布"); p == nil || p.Slug != "alpha" {
		t.Fatalf("Alpha 发布 -> %+v", p)
	}
}

// When live projects collide, a name beats an alias and among aliases the most recently updated one wins, whatever
// the directory order.
func TestFindProjectPrefersNameThenRecent(t *testing.T) {
	e := testEngine(t, nil)
	old := time.Date(2026, 10, 1, 0, 0, 0, 0, time.UTC)
	put := func(p *Project) { // putEntity keeps Updated as given: the order is fixed by the data, not the clock
		if err := e.Store.putEntity(KindProject, p.Slug, p, &p.Tagging, false); err != nil {
			t.Fatal(err)
		}
	}
	put(&Project{Slug: "a-old", Name: "旧项目", Aliases: []string{"共用名"}, Status: "active", Updated: old})
	put(&Project{Slug: "b-new", Name: "新项目", Aliases: []string{"共用名"}, Status: "active", Updated: old.Add(48 * time.Hour)})
	if p := e.FindProject("共用名"); p == nil || p.Slug != "b-new" {
		t.Fatalf("alias tie -> %+v", p)
	}
	put(&Project{Slug: "z-name", Name: "共用名", Status: "active", Updated: old.Add(-time.Hour)})
	if p := e.FindProject("共用名"); p == nil || p.Slug != "z-name" {
		t.Fatalf("name vs alias -> %+v", p)
	}
}

// A "same" the model is not sure of, or whose reason hedges, is not merged and is kept as different.
func TestDedupeSkipsDoubtfulMerges(t *testing.T) {
	for name, reply := range map[string]string{
		"not sure":              `{"same":true,"sure":false,"keep":"A","reason":"共同人少"}`,
		"no sure":               `{"same":true,"keep":"A","reason":"同一个需求"}`,
		"hedged":                `{"same":true,"sure":true,"keep":"A","reason":"方向是否一致存疑"}`,
		"English uncertain":     `{"same":true,"sure":true,"keep":"A","reason":"Uncertain direction"}`,
		"English not sure":      `{"same":true,"sure":true,"keep":"A","reason":"Not sure it is the same"}`,
		"English perhaps":       `{"same":true,"sure":true,"keep":"A","reason":"Perhaps the same goal"}`,
		"English probably not":  `{"same":true,"sure":true,"keep":"A","reason":"Probably not the same project"}`,
		"English may not":       `{"same":true,"sure":true,"keep":"A","reason":"May not be the same work"}`,
		"English seems":         `{"same":true,"sure":true,"keep":"A","reason":"Seems different"}`,
		"English not same":      `{"same":true,"sure":true,"keep":"A","reason":"Not same project"}`,
		"English not identical": `{"same":true,"sure":true,"keep":"A","reason":"Not identical"}`,
		"English can't tell":    `{"same":true,"sure":true,"keep":"A","reason":"I can't tell if these are the same"}`,
		"English can’t tell":    `{"same":true,"sure":true,"keep":"A","reason":"I can’t tell if these are the same"}`,
		"Japanese uncertain":    `{"same":true,"sure":true,"keep":"A","reason":"同じ仕事か不確か"}`,
		"Japanese possibility":  `{"same":true,"sure":true,"keep":"A","reason":"別の要件かもしれない"}`,
		"Japanese cannot tell":  `{"same":true,"sure":true,"keep":"A","reason":"同一か判断できない"}`,
	} {
		t.Run(name, func(t *testing.T) {
			m := &scriptModel{replies: []string{reply}}
			e := testEngine(t, m)
			people := []string{"lark-u1", "lark-u2", "lark-u3"}
			_ = e.Store.PutProject(&Project{Slug: "a", Name: "示例甲", Status: "active", People: people})
			_ = e.Store.PutProject(&Project{Slug: "b", Name: "示例乙", Status: "active", People: people})
			if n := e.DedupeProjects(context.Background(), 5); n != 0 || m.calls != 1 {
				t.Fatalf("merged %d with %d call(s)", n, m.calls)
			}
			if p, _ := e.Store.Project("b"); !p.Live() {
				t.Fatalf("b merged: %+v", p)
			}
			v := e.Store.dedupeVerdicts()[pairKey("a", "b")]
			if v.Same || !strings.HasPrefix(v.Reason, "存疑") {
				t.Fatalf("verdict = %+v", v)
			}
		})
	}
}

// Merging carries drop's name and aliases, but not the ones another live project goes by, and stays bounded.
func TestMergeProjectsValidatesAliases(t *testing.T) {
	e := testEngine(t, nil)
	var many []string
	for i := range 20 {
		many = append(many, fmt.Sprintf("乙别名%02d", i))
	}
	_ = e.Store.PutProject(&Project{Slug: "keep", Name: "示例甲", Status: "active"})
	_ = e.Store.PutProject(&Project{Slug: "drop", Name: "示例乙", Aliases: append([]string{"示例丙", "丙的别名"}, many...), Status: "active"})
	_ = e.Store.PutProject(&Project{Slug: "third", Name: "示例丙", Aliases: []string{"丙的别名"}, Status: "active"})
	if err := e.MergeProjects("keep", "drop", "test"); err != nil {
		t.Fatal(err)
	}
	keep, _ := e.Store.Project("keep")
	if keep.Aliases[0] != "示例乙" || slices.Contains(keep.Aliases, "示例丙") || slices.Contains(keep.Aliases, "丙的别名") || len(keep.Aliases) != maxProjectAliases {
		t.Fatalf("keep aliases (%d) = %v", len(keep.Aliases), keep.Aliases)
	}
	if p := e.FindProject("丙的别名"); p == nil || p.Slug != "third" {
		t.Fatalf("丙的别名 -> %+v", p)
	}
	if p := e.FindProject("示例乙"); p == nil || p.Slug != "keep" {
		t.Fatalf("示例乙 -> %+v", p)
	}
}

// Aliases collected before the rules are pruned to what the rules allow: not another live project's name, not the
// project's own name, a repeat or too generic, at most maxProjectAliases, plus the names of projects merged into
// it, directly or through another merged project. An alias two live projects share and aliases set by hand stay; recency does not change; a second pass is a no-op.
func TestPruneProjectAliases(t *testing.T) {
	e := testEngine(t, nil)
	aliases := []string{"Beta 迁移", "阿尔法", "阿尔法", "Alpha发布", "ss", "项目", "共享名"}
	for i := range 15 {
		aliases = append(aliases, fmt.Sprintf("别名%02d", i))
	}
	aliases = append(aliases, "旧名", "更旧的名字")
	_ = e.Store.PutProject(&Project{Slug: "alpha", Name: "Alpha 发布", Status: "active", Aliases: aliases})
	_ = e.Store.PutProject(&Project{Slug: "beta", Name: "Beta 迁移", Status: "active", Aliases: []string{"共享名"}})
	_ = e.Store.PutProject(&Project{Slug: "gamma", Name: "Gamma", Status: "active", Aliases: []string{"Alpha 发布", "ss"},
		Tagging: Tagging{LockedFields: []string{"aliases"}}})
	_ = e.Store.PutProject(&Project{Slug: "old", Name: "旧名", Status: "merged", MergedInto: "alpha"})
	_ = e.Store.PutProject(&Project{Slug: "older", Name: "更旧的名字", Status: "merged", MergedInto: "old"})
	before, _ := e.Store.Project("alpha")

	if n := e.PruneProjectAliases(); n != 10 {
		t.Fatalf("pruned %d", n)
	}
	alpha, _ := e.Store.Project("alpha")
	want := []string{"阿尔法", "共享名"}
	for i := range maxProjectAliases - 2 {
		want = append(want, fmt.Sprintf("别名%02d", i))
	}
	want = append(want, "旧名", "更旧的名字")
	if !slices.Equal(alpha.Aliases, want) {
		t.Fatalf("alpha aliases = %v\nwant %v", alpha.Aliases, want)
	}
	if !alpha.Updated.Equal(before.Updated) {
		t.Fatalf("updated moved: %v -> %v", before.Updated, alpha.Updated)
	}
	for slug, want := range map[string]string{"beta": "共享名", "gamma": "Alpha 发布,ss"} {
		if p, _ := e.Store.Project(slug); strings.Join(p.Aliases, ",") != want {
			t.Fatalf("%s aliases = %v", slug, p.Aliases)
		}
	}
	for name, want := range map[string]string{"Beta 迁移": "beta", "更旧的名字": "alpha"} {
		if p := e.FindProject(name); p == nil || p.Slug != want {
			t.Fatalf("%s -> %+v", name, p)
		}
	}
	if n := e.PruneProjectAliases(); n != 0 {
		t.Fatalf("second pass pruned %d", n)
	}
}
