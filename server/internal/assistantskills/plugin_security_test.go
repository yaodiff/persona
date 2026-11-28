package assistantskills

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

func TestApplyPluginsRejectsUnsafeSkillNamesAndHasZeroSideEffects(t *testing.T) {
	dir := t.TempDir()
	keepDir := filepath.Join(dir, "keep-skill")
	if err := os.MkdirAll(keepDir, 0o755); err != nil {
		t.Fatal(err)
	}
	keepFile := filepath.Join(keepDir, "SKILL.md")
	if err := os.WriteFile(keepFile, []byte("keep\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	badNames := []string{
		"",
		"/",
		".",
		"..",
		".hidden",
		"a/b",
		`a\b`,
		"a,victim=x",
		"a=b",
		"a:b",
		"con",
		"CON.txt",
		"CON .txt",
		"NUL .md",
		"trailing.",
		"trailing ",
		" persona-office",
		"my skill",
		"my\tskill",
		"my\u00a0skill",
		"my\u3000skill",
		"bad\x00name",
		"bad\nname",
	}
	for _, name := range badNames {
		if _, err := ApplyPlugins(dir, []PluginSkill{{
			Name:   name,
			Plugin: "p",
			Digest: "d1",
			Files:  map[string][]byte{"SKILL.md": []byte("x\n")},
		}}); err == nil {
			t.Errorf("ApplyPlugins accepted unsafe skill name %q", name)
		}
	}
	// Existing directory contents must not have been damaged (e.g. by Name == "/").
	if b, err := os.ReadFile(keepFile); err != nil || string(b) != "keep\n" {
		t.Fatalf("existing plugin skill was damaged: %q, err=%v", b, err)
	}

	// Pre-validation failure must not even create a non-existent target dir.
	nonexistent := filepath.Join(t.TempDir(), "never-created")
	if _, err := ApplyPlugins(nonexistent, []PluginSkill{{
		Name:   " persona-office",
		Plugin: "p",
		Digest: "d1",
		Files:  map[string][]byte{"SKILL.md": []byte("x\n")},
	}}); err == nil {
		t.Fatal("expected error for leading-space skill name")
	}
	if _, err := os.Stat(nonexistent); !os.IsNotExist(err) {
		t.Fatalf("ApplyPlugins created directory on invalid input: stat err=%v", err)
	}

	// Mid-batch atomicity: an invalid second skill must not write the first skill or prune keep-skill.
	if _, err := ApplyPlugins(dir, []PluginSkill{
		{Name: "first-valid", Plugin: "p", Digest: "d1", Files: map[string][]byte{"SKILL.md": []byte("ok\n")}},
		{Name: "second-bad", Plugin: "p", Digest: "d2", Files: map[string][]byte{"../escape": []byte("bad\n")}},
	}); err == nil {
		t.Fatal("expected mid-batch error")
	}
	if _, err := os.Stat(filepath.Join(dir, "first-valid")); !os.IsNotExist(err) {
		t.Fatalf("first-valid was written despite second skill failing validation: err=%v", err)
	}
	if b, err := os.ReadFile(keepFile); err != nil || string(b) != "keep\n" {
		t.Fatalf("keep-skill was damaged on mid-batch failure: %q, err=%v", b, err)
	}
}

func TestApplyPluginsRejectsStampInjectionAndEmptyMetadata(t *testing.T) {
	dir := t.TempDir()
	badMeta := []struct {
		plugin, digest string
	}{
		{"", "d1"},
		{"p", ""},
		{"p,../victim=x", "d1"},
		{"p=x", "d1"},
		{"p\nx", "d1"},
		{"p x", "d1"},
		{"p\u00a0x", "d1"},
		{"p", "d1,../victim=x"},
		{"p", "d1=x"},
		{"p", "d1\nx"},
		{"p", "d1\rx"},
	}
	for _, tc := range badMeta {
		if _, err := ApplyPlugins(dir, []PluginSkill{{
			Name:   "valid-skill",
			Plugin: tc.plugin,
			Digest: tc.digest,
			Files:  map[string][]byte{"SKILL.md": []byte("ok\n")},
		}}); err == nil {
			t.Errorf("ApplyPlugins accepted invalid metadata plugin=%q digest=%q", tc.plugin, tc.digest)
		}
	}

	// safeStampValue keeps canonical "<plugin> <digest>" and hashes unsafe/oversized values.
	if got := safeStampValue("example d1\n"); got != "example d1" {
		t.Fatalf("safeStampValue(normal) = %q, want %q", got, "example d1")
	}
	for _, raw := range []string{
		"a,b",
		"a=b",
		"a\tb",
		"a\u00a0b",
		"a\x00b",
		string([]byte{0xff, 0xfe}),
		strings.Repeat("x", 513),
	} {
		if got := safeStampValue(raw); !strings.HasPrefix(got, "sha256:") {
			t.Errorf("safeStampValue(%q) = %q, want sha256: prefix", raw, got)
		}
	}
}

func TestApplyPluginsRejectsUnsafeRelPathsDuplicatesAndPreservesNFCNFD(t *testing.T) {
	dir := t.TempDir()
	badRels := []string{
		"",
		"/",
		"../escape",
		`..\escape`,
		"C:drive.txt",
		".persona-plugin",
		".PERSONA-PLUGIN",
		".persona-other",
		"subdir/",
		"CON.txt",
		"CON .txt",
		"sub/NUL .md",
		"a\x00b",
	}
	for _, rel := range badRels {
		if _, err := ApplyPlugins(dir, []PluginSkill{{
			Name:   "my-skill",
			Plugin: "p",
			Digest: "d1",
			Files: map[string][]byte{
				"SKILL.md": []byte("ok\n"),
				rel:        []byte("bad\n"),
			},
		}}); err == nil {
			t.Errorf("ApplyPlugins accepted unsafe file path %q", rel)
		}
	}
	// File vs directory conflict ("a" and "a/b") must be rejected.
	if _, err := ApplyPlugins(dir, []PluginSkill{{
		Name:   "file-dir-conflict",
		Plugin: "p",
		Digest: "d1",
		Files: map[string][]byte{
			"a":   []byte("file\n"),
			"a/b": []byte("nested\n"),
		},
	}}); err == nil {
		t.Error("ApplyPlugins accepted file-vs-directory collision (a vs a/b)")
	}
	// Failed ApplyPlugins must not leave orphan staging directories behind.
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	for _, e := range entries {
		if strings.HasPrefix(e.Name(), ".stage-") || strings.HasSuffix(e.Name(), ".new") {
			t.Fatalf("orphan staging entry left after failed ApplyPlugins: %q", e.Name())
		}
	}

	// Duplicate case-folded and NFC/NFD-equivalent skill names in one batch must be rejected.
	if _, err := ApplyPlugins(dir, []PluginSkill{
		{Name: "dup-skill", Plugin: "p", Digest: "d1", Files: map[string][]byte{"SKILL.md": []byte("a\n")}},
		{Name: "DUP-SKILL", Plugin: "p", Digest: "d2", Files: map[string][]byte{"SKILL.md": []byte("b\n")}},
	}); err == nil {
		t.Error("ApplyPlugins accepted duplicate case-folded skill names")
	}
	if _, err := ApplyPlugins(dir, []PluginSkill{
		{Name: "caf\u00e9", Plugin: "p", Digest: "d1", Files: map[string][]byte{"SKILL.md": []byte("a\n")}},
		{Name: "cafe\u0301", Plugin: "p", Digest: "d2", Files: map[string][]byte{"SKILL.md": []byte("b\n")}},
	}); err == nil {
		t.Error("ApplyPlugins accepted NFC/NFD-colliding skill names")
	}

	// Case-folded duplicate file paths within one skill must be rejected.
	if _, err := ApplyPlugins(dir, []PluginSkill{{
		Name:   "case-skill",
		Plugin: "p",
		Digest: "d1",
		Files: map[string][]byte{
			"SKILL.md": []byte("a\n"),
			"skill.md": []byte("b\n"),
		},
	}}); err == nil {
		t.Error("ApplyPlugins accepted case-colliding file paths")
	}

	// Empty non-nil Files map should create the skill directory and stamp cleanly instead of failing ENOENT.
	if _, err := ApplyPlugins(dir, []PluginSkill{{
		Name:   "empty-files",
		Plugin: "p",
		Digest: "d1",
		Files:  map[string][]byte{},
	}}); err != nil {
		t.Fatalf("ApplyPlugins with empty Files map failed: %v", err)
	}

	// NFD skill name must survive ApplyPlugins pruning (no self-deletion on NFC/NFD-normalizing filesystems).
	nfdName := "cafe\u0301"
	if _, err := ApplyPlugins(dir, []PluginSkill{{
		Name:   nfdName,
		Plugin: "p",
		Digest: "d1",
		Files:  map[string][]byte{"SKILL.md": []byte("coffee\n")},
	}}); err != nil {
		t.Fatalf("ApplyPlugins(NFD) failed: %v", err)
	}
	if b, err := os.ReadFile(filepath.Join(dir, nfdName, "SKILL.md")); err != nil || string(b) != "coffee\n" {
		t.Fatalf("NFD skill was lost or pruned: %q, err=%v", b, err)
	}
	if need, err := ApplyPlugins(dir, []PluginSkill{{Name: nfdName, Plugin: "p", Digest: "d1"}}); err != nil || len(need) != 0 {
		t.Fatalf("ApplyPlugins(NFD, Files:nil) = need=%v err=%v, want empty need", need, err)
	}
}

func TestInstallPluginsCaseCollisionTamperedStampAndSymlinkHardening(t *testing.T) {
	parent := t.TempDir()
	victim := filepath.Join(parent, "victim")
	if err := os.MkdirAll(victim, 0o755); err != nil {
		t.Fatal(err)
	}
	victimFile := filepath.Join(victim, "data.txt")
	if err := os.WriteFile(victimFile, []byte("precious\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	assistantDir := filepath.Join(parent, "assistant-skills")
	pluginsDir := filepath.Join(parent, "plugins")
	if err := os.MkdirAll(assistantDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(pluginsDir, 0o755); err != nil {
		t.Fatal(err)
	}

	// Non-plugin local dir, built-in dir, and symlinked-.persona-plugin dir inside assistantDir.
	localDir := filepath.Join(assistantDir, "local-user-dir")
	if err := os.MkdirAll(localDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(localDir, "SKILL.md"), []byte("local\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	builtinDir := filepath.Join(assistantDir, "persona-office")
	if err := os.MkdirAll(builtinDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(builtinDir, "SKILL.md"), []byte("builtin\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	forgedDir := filepath.Join(assistantDir, "forged-link-stamp")
	if err := os.MkdirAll(forgedDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(victimFile, filepath.Join(forgedDir, pluginStamp)); err != nil {
		t.Fatal(err)
	}

	// Tampered .persona-extension-skills containing traversal, non-plugin dirs, built-in, and missing '='.
	tampered := "../victim=p d1,local-user-dir=p d1,persona-office=p d1,PERSONA-VIEWS=p d1,forged-link-stamp=p d1,no-equals-entry\n"
	if err := os.WriteFile(filepath.Join(assistantDir, pluginsCopied), []byte(tampered), 0o644); err != nil {
		t.Fatal(err)
	}

	// Plugins dir has case-colliding built-in ("PERSONA-VIEWS") and user skill ("MY-SKILL").
	if _, err := ApplyPlugins(pluginsDir, []PluginSkill{
		{Name: "PERSONA-VIEWS", Plugin: "evil", Digest: "d1", Files: map[string][]byte{"SKILL.md": []byte("evil-views\n")}},
		{Name: "MY-SKILL", Plugin: "evil", Digest: "d1", Files: map[string][]byte{"SKILL.md": []byte("evil-user\n")}},
		{Name: "real-plugin", Plugin: "good", Digest: "d1", Files: map[string][]byte{"SKILL.md": []byte("real\n")}},
	}); err != nil {
		t.Fatalf("ApplyPlugins: %v", err)
	}

	if err := installPlugins(assistantDir, pluginsDir, []string{"my-skill"}); err != nil {
		t.Fatalf("installPlugins: %v", err)
	}
	if b, err := os.ReadFile(victimFile); err != nil || string(b) != "precious\n" {
		t.Fatalf("victim outside assistantDir damaged: %q, err=%v", b, err)
	}
	if b, err := os.ReadFile(filepath.Join(localDir, "SKILL.md")); err != nil || string(b) != "local\n" {
		t.Fatalf("non-plugin local dir deleted via tampered stamp: %q, err=%v", b, err)
	}
	if b, err := os.ReadFile(filepath.Join(builtinDir, "SKILL.md")); err != nil || string(b) != "builtin\n" {
		t.Fatalf("built-in dir deleted via tampered stamp: %q, err=%v", b, err)
	}
	if _, err := os.Lstat(forgedDir); err != nil {
		t.Fatalf("dir with symlinked .persona-plugin was deleted: %v", err)
	}
	if _, err := os.Lstat(filepath.Join(assistantDir, "PERSONA-VIEWS")); !os.IsNotExist(err) {
		t.Fatalf("case-colliding built-in PERSONA-VIEWS was installed: err=%v", err)
	}
	if _, err := os.Lstat(filepath.Join(assistantDir, "MY-SKILL")); !os.IsNotExist(err) {
		t.Fatalf("case-colliding user skill MY-SKILL was installed: err=%v", err)
	}
	if b, err := os.ReadFile(filepath.Join(assistantDir, "real-plugin", "SKILL.md")); err != nil || string(b) != "real\n" {
		t.Fatalf("real-plugin was not installed: %q, err=%v", b, err)
	}

	// Symlink at .persona-extension-skills must be atomically replaced without writing to its target.
	stampTarget := filepath.Join(parent, "external-target.txt")
	if err := os.WriteFile(stampTarget, []byte("untouched\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	stampPath := filepath.Join(assistantDir, pluginsCopied)
	_ = os.Remove(stampPath)
	if err := os.Symlink(stampTarget, stampPath); err != nil {
		t.Fatal(err)
	}
	if err := installPlugins(assistantDir, pluginsDir, []string{"my-skill"}); err != nil {
		t.Fatalf("installPlugins with symlinked stamp: %v", err)
	}
	if b, err := os.ReadFile(stampTarget); err != nil || string(b) != "untouched\n" {
		t.Fatalf("installPlugins wrote through symlinked .persona-extension-skills: %q, err=%v", b, err)
	}
	if st, err := os.Lstat(stampPath); err != nil || !st.Mode().IsRegular() {
		t.Fatalf(".persona-extension-skills was not replaced with a regular file: st=%v err=%v", st, err)
	}

	// Removing real-plugin from pluginsDir must still cleanly remove genuine plugin copies.
	if _, err := ApplyPlugins(pluginsDir, []PluginSkill{}); err != nil {
		t.Fatal(err)
	}
	if err := installPlugins(assistantDir, pluginsDir, []string{"my-skill"}); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(filepath.Join(assistantDir, "real-plugin")); !os.IsNotExist(err) {
		t.Fatalf("removed genuine plugin copy was not deleted: err=%v", err)
	}
}

func TestInstallPluginsWorkGuideCaseAndCaseVariantRename(t *testing.T) {
	parent := t.TempDir()
	assistantDir := filepath.Join(parent, "assistant")
	pluginsDir := filepath.Join(parent, "plugins")
	if err := os.MkdirAll(assistantDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(pluginsDir, 0o755); err != nil {
		t.Fatal(err)
	}

	// Case-variant "PERSONA-WORK" must NOT be installed (only exact workGuideName may override).
	if _, err := ApplyPlugins(pluginsDir, []PluginSkill{
		{Name: "PERSONA-WORK", Plugin: "evil", Digest: "d1", Files: map[string][]byte{"SKILL.md": []byte("evil-work\n")}},
	}); err != nil {
		t.Fatal(err)
	}
	if err := installPlugins(assistantDir, pluginsDir, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(filepath.Join(assistantDir, "PERSONA-WORK")); !os.IsNotExist(err) {
		t.Fatalf("case-variant PERSONA-WORK was installed: err=%v", err)
	}

	// Exact "persona-work" installs and uninstalls cleanly via installPlugins.
	if _, err := ApplyPlugins(pluginsDir, []PluginSkill{
		{Name: workGuideName, Plugin: "ext", Digest: "d1", Files: map[string][]byte{"SKILL.md": []byte("plugin-work\n")}},
	}); err != nil {
		t.Fatal(err)
	}
	if err := installPlugins(assistantDir, pluginsDir, nil); err != nil {
		t.Fatal(err)
	}
	if b, err := os.ReadFile(filepath.Join(assistantDir, workGuideName, "SKILL.md")); err != nil || string(b) != "plugin-work\n" {
		t.Fatalf("exact persona-work plugin was not installed: %q, err=%v", b, err)
	}
	if _, err := ApplyPlugins(pluginsDir, []PluginSkill{}); err != nil {
		t.Fatal(err)
	}
	if err := installPlugins(assistantDir, pluginsDir, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := os.Lstat(filepath.Join(assistantDir, workGuideName)); !os.IsNotExist(err) {
		t.Fatalf("exact persona-work plugin copy was not uninstalled: err=%v", err)
	}

	// Case-variant rename (MySkill -> myskill) must leave ONLY exact "myskill" in both PluginDir and assistantDir.
	if _, err := ApplyPlugins(pluginsDir, []PluginSkill{
		{Name: "MySkill", Plugin: "p", Digest: "d1", Files: map[string][]byte{"SKILL.md": []byte("v1\n")}},
	}); err != nil {
		t.Fatal(err)
	}
	if err := installPlugins(assistantDir, pluginsDir, nil); err != nil {
		t.Fatal(err)
	}
	if _, err := ApplyPlugins(pluginsDir, []PluginSkill{
		{Name: "myskill", Plugin: "p", Digest: "d2", Files: map[string][]byte{"SKILL.md": []byte("v2\n")}},
	}); err != nil {
		t.Fatal(err)
	}
	if err := installPlugins(assistantDir, pluginsDir, nil); err != nil {
		t.Fatal(err)
	}
	for _, checkDir := range []string{pluginsDir, assistantDir} {
		entries, err := os.ReadDir(checkDir)
		if err != nil {
			t.Fatal(err)
		}
		var dirs []string
		for _, e := range entries {
			if e.IsDir() && !strings.HasPrefix(e.Name(), ".") {
				dirs = append(dirs, e.Name())
			}
		}
		if !slices.Equal(dirs, []string{"myskill"}) {
			t.Fatalf("%s dirs after MySkill->myskill rename = %v, want [myskill]", checkDir, dirs)
		}
		if b, err := os.ReadFile(filepath.Join(checkDir, "myskill", "SKILL.md")); err != nil || string(b) != "v2\n" {
			t.Fatalf("%s/myskill/SKILL.md = %q, err=%v; want v2", checkDir, b, err)
		}
	}
}

func TestApplyPluginsDoesNotTrustSymlinkedSkillOrStamp(t *testing.T) {
	parent := t.TempDir()
	outside := filepath.Join(parent, "outside-skill")
	if err := os.MkdirAll(outside, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(outside, pluginStamp), []byte("p d1\n"), 0o644); err != nil {
		t.Fatal(err)
	}

	pluginsDir := filepath.Join(parent, "plugins")
	if err := os.MkdirAll(pluginsDir, 0o755); err != nil {
		t.Fatal(err)
	}
	// Case 1: PluginDir/link-skill is a symlink to an external dir containing matching stamp.
	if err := os.Symlink(outside, filepath.Join(pluginsDir, "link-skill")); err != nil {
		t.Fatal(err)
	}
	// Case 2: PluginDir/link-stamp is a real dir whose .persona-plugin is a symlink.
	linkStampDir := filepath.Join(pluginsDir, "link-stamp")
	if err := os.MkdirAll(linkStampDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(filepath.Join(outside, pluginStamp), filepath.Join(linkStampDir, pluginStamp)); err != nil {
		t.Fatal(err)
	}

	need, err := ApplyPlugins(pluginsDir, []PluginSkill{
		{Name: "link-skill", Plugin: "p", Digest: "d1"},
		{Name: "link-stamp", Plugin: "p", Digest: "d1"},
	})
	if err != nil {
		t.Fatalf("ApplyPlugins: %v", err)
	}
	if !slices.Equal(need, []string{"link-skill", "link-stamp"}) {
		t.Fatalf("need = %v, want [link-skill link-stamp]", need)
	}
}

func TestReferencesScannerHandlesMalformedAndBenignTags(t *testing.T) {
	// Malformed or non-canonical skill tags must fail closed with ErrReference.
	reject := []string{
		`<persona-context kind='skill' ref='abc' bad=>Review</persona-context> hello`,
		`<persona-context kind=skill ref=abc>Review</persona-context>`,
		`<persona-context bad= kind="skill" ref="abc">Review</persona-context>`,
		`<persona-context bad=kind="skill" ref="abc">Review</persona-context>`,
		`<persona-context bad= kind='skill' ref='abc'>Review</persona-context>`,
		`<persona-context bad= kind=skill ref=abc>Review</persona-context>`,
		`<persona-context bad= =kind="skill" ref="abc">Review</persona-context>`,
		`<persona-context title="x"kind='skill' bad=>Review</persona-context>`,
		`<persona-context title="x"kind="skill" url="https://example.com/?a=1&b=2">Review</persona-context>`,
		`<persona-context title="a > b" kind="skill" ref="">Review</persona-context>`,
		`<persona-context title="a > b" kind="skill" bad=>Review</persona-context>`,
		`<persona-context kind='skill ref='abc'>Review</persona-context>`,
		`<persona-context kind="skill ref="abc">Review</persona-context>`,
		`<persona-context kind='skill>Review</persona-context>`,
		`<persona-context kind='skill>Review</persona-context> it's a > b`,
		`<persona-context kind="SKILL" ref="abc">Review</persona-context>`,
		`<persona-context KIND="skill" ref="abc">Review</persona-context>`,
		`<persona-context kind='SKILL' bad=>Review</persona-context>`,
		`<persona-context ns:kind='skill' bad=>Review</persona-context>`,
		`<persona-context a:b:kind='skill' bad=>Review</persona-context>`,
	}
	for _, text := range reject {
		if _, err := References(text); err == nil {
			t.Errorf("References silently accepted/ignored malformed skill tag: %s", text)
		}
	}

	// Benign non-skill tags with unescaped '&' (causing XML parse failure), empty kind, or
	// "kind=skill" inside another attribute's quoted value must NOT false-positive.
	benign := []string{
		`<persona-context kind="" bad=>Review</persona-context> hi`,
		`<persona-context kind="web" title="fix kind=skill" url="https://example.com/?a=1&b=2">Web</persona-context> hi`,
		`<persona-context kind="web" title='kind="skill"' url="https://example.com/?a=1&b=2">Web</persona-context> hi`,
		`<persona-context title="a > b" kind="web" url="https://example.com/?a=1&b=2">Web</persona-context> hi`,
		`<persona-context kind="skill-doc" url="https://example.com/?a=1&b=2">Doc</persona-context> hi`,
		`<persona-context kind="skill guide" url="https://example.com/?a=1&b=2">Guide</persona-context> hi`,
	}
	for _, text := range benign {
		refs, err := References(text)
		if err != nil || len(refs) != 0 {
			t.Errorf("References false-positived on benign tag %q: refs=%v err=%v", text, refs, err)
		}
	}

	// Valid canonical skill selections (including literal '>' inside quoted attributes and legacy tag) parse cleanly.
	refs, err := References(`<persona-context kind="skill" title="Review" ref="abc123">Review</persona-context> go`)
	if err != nil || !slices.Equal(refs, []string{"abc123"}) {
		t.Fatalf("References(valid) = %v, %v; want [abc123]", refs, err)
	}
	refs, err = References(`<persona-context title="a > b" kind="skill" ref="gt1">Review</persona-context><jarvis-context title="x > y" kind="skill" ref="gt2">Legacy</jarvis-context> go`)
	if err != nil || !slices.Equal(refs, []string{"gt1", "gt2"}) {
		t.Fatalf("References(title with '>') = %v, %v; want [gt1 gt2]", refs, err)
	}
}
