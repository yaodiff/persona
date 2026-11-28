package kb

import (
	"context"
	"testing"
	"time"
)

// Regression tests for #5824: models copied the shown form of a known project (「名字（别名、…）：一句话」) as its
// name, which made new projects that dedupe then merged the real ones into. All names are placeholders.

func TestDisplayBases(t *testing.T) {
	cases := map[string][]string{
		"Alpha 发布（阿尔法、AlphaRelease）":  {"Alpha 发布"},
		"Alpha 发布（阿尔法、甲）（Beta 迁移、贝塔）": {"Alpha 发布（阿尔法、甲）", "Alpha 发布"},
		"Alpha 发布（阿尔法）：灰度推进中":         {"Alpha 发布（阿尔法）", "Alpha 发布"},
		"Alpha 发布": nil,
		"(只有括号)":   nil,
	}
	for in, want := range cases {
		got := displayBases(in)
		if len(got) != len(want) {
			t.Fatalf("%q: %q, want %q", in, got, want)
		}
		for i := range got {
			if got[i] != want[i] {
				t.Fatalf("%q: %q, want %q", in, got, want)
			}
		}
	}
}

func TestShownName(t *testing.T) {
	cases := map[string]string{
		"Alpha 发布（阿尔法、AlphaRelease）": "Alpha 发布",
		"Alpha 发布（阿尔法、甲）（贝塔、乙）":      "Alpha 发布",
		"Alpha 发布（阿尔法）：灰度推进中":        "Alpha 发布（阿尔法）",
		"会议分身（内置版）":                  "会议分身（内置版）",   // one group without 、 can be a real name
		"无（示例群、另一个群）":                "无（示例群、另一个群）", // what is left would be too generic
		"Beta 迁移":                    "Beta 迁移",
	}
	for in, want := range cases {
		if got := shownName(in); got != want {
			t.Fatalf("%q: %q, want %q", in, got, want)
		}
	}
}

// A mention named in the shown form finds the project it shows and makes no new one.
func TestEvolveProjectShownFormFindsTheProject(t *testing.T) {
	e := testEngine(t, nil)
	_ = e.Store.PutProject(&Project{Slug: "alpha", Name: "Alpha 发布", Aliases: []string{"阿尔法", "甲"}, Status: "active"})
	_ = e.Store.PutProject(&Project{Slug: "beta", Name: "Beta 迁移", Status: "active"})
	c := &Chat{ID: "lark-f1", Kind: "group", Name: "示例群"}
	now := time.Now().UTC()
	for _, name := range []string{"Alpha 发布（阿尔法、甲）", "Alpha 发布（阿尔法、甲）（Beta 迁移、贝塔）", "Alpha 发布（阿尔法、甲）：灰度推进中"} {
		if slug := e.evolveProject(name, "short", nil, "", c, true, "进展", now); slug != "alpha" {
			t.Fatalf("%q -> %q", name, slug)
		}
	}
	if n := len(e.Store.Projects()); n != 2 {
		t.Fatalf("%d projects", n)
	}
	alpha, _ := e.Store.Project("alpha")
	for _, a := range alpha.Aliases {
		if a != "阿尔法" && a != "甲" {
			t.Fatalf("alpha took %q", a)
		}
	}
	// A new project named in the shown form gets the bare name.
	if slug := e.evolveProject("Gamma 上线（伽马、丙）", "short", nil, "", c, true, "", now); slug != Slug("Gamma 上线") {
		t.Fatalf("new project slug %q", slug)
	}
	// A generic base does not stand for the project named by it.
	_ = e.Store.PutProject(&Project{Slug: "无", Name: "无", Status: "active"})
	if p := e.FindProject("无（示例群）"); p != nil {
		t.Fatalf("无（示例群） -> %s", p.Slug)
	}
}

// Dedupe does not ask about (or merge) a project and one named in its shown form.
func TestDedupeSkipsShownFormPairs(t *testing.T) {
	m := &captureModel{reply: `{"same":true,"sure":true,"keep":"A","reason":"同一件事"}`}
	e := testEngine(t, m)
	people := []string{"p1", "p2", "p3"}
	_ = e.Store.PutProject(&Project{Slug: "copy", Name: "Alpha 发布（阿尔法、甲）", People: people, Status: "active"})
	_ = e.Store.PutProject(&Project{Slug: "alpha", Name: "Alpha 发布", People: people, Status: "active"})
	if n := e.DedupeProjects(context.Background(), 4); n != 0 {
		t.Fatalf("merged %d", n)
	}
	if len(m.calls) != 0 {
		t.Fatalf("asked the model %d times", len(m.calls))
	}
	for _, s := range []string{"copy", "alpha"} {
		if p, _ := e.Store.Project(s); !p.Live() {
			t.Fatalf("%s merged", s)
		}
	}
}
