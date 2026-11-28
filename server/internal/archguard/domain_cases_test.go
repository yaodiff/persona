package archguard

import (
	"slices"
	"strings"
	"testing"

	"github.com/nwverse/persona/server/internal/archguard/domainrules"
)

// plantedDirs is the planted tree's files in the given package directories (and not their subpackages).
func plantedDirs(t *testing.T, dirs ...string) map[string]string {
	t.Helper()
	out := map[string]string{}
	for rel, text := range plantedFiles(t) {
		dir := rel[:strings.LastIndex(rel, "/")]
		if slices.Contains(dirs, dir) {
			out[rel] = text
		}
	}
	return out
}

// hook is a package's rules_test.go.
func hook(name string) string {
	return "package " + name + "_test\n\nimport (\n\t\"testing\"\n\n\t\"github.com/nwverse/persona/server/internal/archguard/domainrules\"\n)\n\n" +
		"func TestDomainRules(t *testing.T) { domainrules.Check(t) }\n"
}

// pkgFiles is a package at dir declaring layer, with a package comment, the hook, and body as its one file.
func pkgFiles(dir, layer, body string) map[string]string {
	name := dir[strings.LastIndex(dir, "/")+1:]
	return map[string]string{
		dir + "/doc.go":          "// Package " + name + " is a case.\n//\n//persona:layer " + layer + "\npackage " + name + "\n",
		dir + "/rules_test.go":   hook(name),
		dir + "/" + name + ".go": "package " + name + "\n\n" + body,
	}
}

// app is an application package at dir whose one file has body after an effect use.
func app(dir, imports, body string) map[string]string {
	files := pkgFiles(dir, "application", "import (\n\t\"sync\"\n"+imports+")\n\nfunc locked(mu *sync.Mutex) { mu.Lock() }\n\n"+body)
	delete(files, dir+"/rules_test.go")
	return files
}

// merged is the files of every map, later ones winning.
func merged(maps ...map[string]string) map[string]string {
	out := map[string]string{}
	for _, m := range maps {
		for k, v := range m {
			out[k] = v
		}
	}
	return out
}

// rules is the rule of each finding and count a case gives, sorted.
func rules(got []string) []string {
	out := []string{}
	for _, g := range got {
		out = append(out, strings.Fields(g)[0])
	}
	slices.Sort(out)
	return out
}

const fleetImport = "\t\"github.com/nwverse/persona/server/internal/assistant/fleetcount\"\n"

// The in-memory cases: what each one adds to a tree of compliant packages fails (or passes) exactly as written.
func TestDomainRuleCases(t *testing.T) {
	fleet := plantedDirs(t, "internal/agentref", "internal/assistant/fleetcount")
	quiet := plantedDirs(t, "internal/assistant/quiethours")
	// The adapter reads the declarations of the contract types it names, so the contract comes with it.
	refAdapt := plantedDirs(t, "internal/agentref/agentrefadapt", "internal/assistantwire")
	// contract is the contract assistantwire with body as its one file; adapt is an adapter of fleetcount's beside it.
	contract := func(body string) map[string]string {
		files := pkgFiles("internal/assistantwire", "contract", body)
		delete(files, "internal/assistantwire/rules_test.go")
		return files
	}
	adapt := func(wire map[string]string, body string) map[string]string {
		return merged(fleet, wire, pkgFiles("internal/assistant/fleetcount/sampleadapt", "adapter", "import (\n"+fleetImport+
			"\t\"github.com/nwverse/persona/server/internal/assistantwire\"\n)\n\n"+body))
	}
	core := func(body string) map[string]string { return pkgFiles("internal/assistant/sample", "core", body) }
	for _, c := range []struct {
		name  string
		files map[string]string
		want  []string
	}{
		{"an undeclared new package", map[string]string{"internal/fresh/fresh.go": "package fresh\n"},
			[]string{"layer-declared"}},
		{"an application package without effects", pkgFiles("internal/calc", "application",
			"// Sum is a plus b.\nfunc Sum(a, b int) int { return a + b }\n"), []string{"layer-application"}},
		{"an application package with only a context.Context", pkgFiles("internal/calc", "application",
			"import \"context\"\n\n// Run stops when ctx does.\nfunc Run(ctx context.Context) error { return ctx.Err() }\n"),
			[]string{"layer-application"}},
		{"kit outside domainkit", pkgFiles("internal/words", "kit", ""), []string{"layer-kit"}},
		// The kit is every core's import, so its own code is held to a core's purity: no import beyond the computing
		// standard library and the kit (instant's time aside), no effect and no clock call.
		{"impure code in the kit", pkgFiles("internal/domainkit/extra", "kit", "import (\n\t\"os\"\n\t\"time\"\n\n"+
			"\t\"github.com/nwverse/persona/server/internal/proto\"\n)\n\nfunc read() bool {\n\tgo func() {}()\n"+
			"\treturn os.Getenv(\"X\") == \"\" && time.Now().IsZero() && proto.Version == \"\"\n}\n"),
			[]string{"core-clock-call", "core-effect", "core-import", "core-import", "core-import"}},
		// Every other core rule runs over the kit too: only kitAPI's lines, each one needed by the kit's listed API,
		// leave a rule alone.
		{"a setter, a constant, a tag, a map range and a stray function in the kit", pkgFiles("internal/domainkit/extra",
			"kit", "// Slot is a place. The zero Slot is the first.\ntype Slot struct{ slot int32 }\n\n"+
				"// Max is the last place.\nconst Max = 1<<31 - 1\n\n// Advance moves s on.\nfunc (s *Slot) Advance() { s.slot++ }\n\n"+
				"// Row is a stored place. The zero Row is the first.\ntype Row struct {\n\trow Slot `db:\"first\"`\n}\n\n"+
				"func count(m map[uint8]bool) int {\n\tn := 0\n\tfor range m {\n\t\tn++\n\t}\n\treturn n\n}\n\n"+
				"// First is the first place.\nfunc First() Slot { return Slot{} }\n"),
			[]string{"core-constructor", "core-exported-const", "core-map-range", "core-pointer-receiver", "core-tag"}},
		{"two layers in one package", merged(pkgFiles("internal/two", "library", ""), map[string]string{
			"internal/two/b.go": "// Package two again.\n//\n//persona:layer contract\npackage two\n"}),
			[]string{"layer-contract", "layer-one"}},
		{"core shared declaring a builder", pkgFiles("internal/shared", "core shared",
			"// Tag is a tag. The zero Tag is none.\ntype Tag struct{ tag uint8 }\n\n"+
				"// TagBuilder builds one. The zero TagBuilder is empty.\ntype TagBuilder struct{ tagBuilder Tag }\n"),
			[]string{"layer-shared"}},
		{"a missing hook", map[string]string{"internal/assistant/nohook/doc.go": "// Package nohook is a core.\n//\n" +
			"//persona:layer core\npackage nohook\n"}, []string{"core-hook"}},
		{"dot-import and alias", merged(fleet, app("internal/assistant/sample", ". "+fleetImport,
			"var working = StateWorking()\n"), map[string]string{"internal/assistant/sample/alias.go": "package sample\n\n" +
			"import \"github.com/nwverse/persona/server/internal/assistant/fleetcount\"\n\ntype State = fleetcount.State\n"}),
			[]string{"out-alias", "out-dot-import"}},
		{"a zero literal in a test", merged(fleet, app("internal/assistant/sample", "", ""), map[string]string{
			"internal/assistant/sample/sample_test.go": "package sample\n\nimport \"github.com/nwverse/persona/server/internal/" +
				"assistant/fleetcount\"\n\nvar zero = fleetcount.State{}\n"}), []string{"out-literal"}},
		{"a conversion fleetcount.Role(x)", merged(fleet, app("internal/assistant/sample", fleetImport,
			"func role(s fleetcount.State) fleetcount.Role { return fleetcount.Role(s) }\n")), []string{"out-conversion"}},
		{"a method value m.Wire", merged(fleet, app("internal/assistant/sample", fleetImport,
			"func count(n fleetcount.RowCount) func() int { return n.Wire }\n")), []string{"out-wire-ref"}},
		{"a method expression agentref.MachineID.Wire", merged(fleet, app("internal/assistant/sample",
			"\t\"github.com/nwverse/persona/server/internal/agentref\"\n", "var unwrap = agentref.MachineID.Wire\n")),
			[]string{"out-wire-ref"}},
		{"pkg.Wire{} where pkg is an import passes", merged(fleet, app("internal/assistant/sample",
			"\t\"github.com/nwverse/persona/server/internal/assistantwire\"\n", "var w = assistantwire.Wire{}\n")), []string{}},
		{"a visitor method in application code", merged(fleet, app("internal/assistant/sample",
			fleetImport+"\t\"github.com/nwverse/persona/server/internal/domainkit/instant\"\n"+
				"\t\"github.com/nwverse/persona/server/internal/domainkit/slot\"\n",
			"type view struct{ rows int }\n\nfunc (x *view) VisitAgent(a slot.Slot, w fleetcount.Why, s instant.Instant) {}\n")),
			[]string{"out-visitor-impl"}},
		{"a parameterless visitor method in application code", merged(fleet, app("internal/assistant/sample",
			fleetImport,
			"type whyView struct{ held bool }\n\nfunc (x *whyView) VisitNeedsYou() { x.held = false }\nfunc (x *whyView) VisitHeld() { x.held = true }\n")),
			[]string{"out-visitor-impl", "out-visitor-impl"}},
		{"a parameterless visitor split from the file that imports the core", merged(fleet, app("internal/assistant/sample",
			fleetImport,
			"func check(w fleetcount.Why) { var v whyView; w.Accept(&v) }\n"), map[string]string{
			"internal/assistant/sample/view.go": "package sample\n\ntype whyView struct{ held bool }\n\nfunc (x *whyView) VisitNeedsYou() { x.held = false }\nfunc (x *whyView) VisitHeld() { x.held = true }\n",
		}), []string{"out-visitor-impl", "out-visitor-impl"}},
		{"an unrelated parameterless Visit method in application code passes", merged(fleet, app("internal/assistant/sample",
			fleetImport,
			"type other struct{}\n\nfunc (other) VisitNothing() {}\nfunc (other) VisitHeld() bool { return false }\n")),
			[]string{}},
		// A core's own black-box tests implement its visitors, whatever the kit's or a shared kernel's types they take
		// (quiethours' VisitHoldUntil takes only an instant.Instant); another domain package's visitor in such a test is
		// a finding against that package, one that takes only the kit's types included.
		{"a core's own test visiting it with the kit's types passes", merged(fleet, quiet, map[string]string{
			"internal/assistant/quiethours/visit_test.go": "package quiethours_test\n\n" +
				"import \"github.com/nwverse/persona/server/internal/domainkit/instant\"\n\n" +
				"type held struct{ at instant.Instant }\n\nfunc (h *held) VisitDeliver() {}\n\n" +
				"func (h *held) VisitHoldUntil(until instant.Instant) { h.at = until }\n"}), []string{}},
		{"a core's own test visiting it with a shared kernel's type passes", merged(fleet, map[string]string{
			"internal/assistant/fleetcount/asker.go": "package fleetcount\n\n" +
				"import \"github.com/nwverse/persona/server/internal/agentref\"\n\n" +
				"// AskerVisitor is told which session asked.\ntype AskerVisitor interface {\n\tVisitAsker(asker agentref.Ref)\n}\n",
			"internal/assistant/fleetcount/asker_test.go": "package fleetcount_test\n\n" +
				"import \"github.com/nwverse/persona/server/internal/agentref\"\n\n" +
				"type asked struct{ by agentref.Ref }\n\nfunc (a *asked) VisitAsker(asker agentref.Ref) { a.by = asker }\n"}),
			[]string{}},
		{"a core's own test visiting another core with the kit's types is a finding", merged(fleet, quiet, map[string]string{
			"internal/assistant/fleetcount/hold_visit_test.go": "package fleetcount_test\n\nimport (\n" +
				"\t\"github.com/nwverse/persona/server/internal/assistant/quiethours\"\n" +
				"\t\"github.com/nwverse/persona/server/internal/domainkit/instant\"\n)\n\n" +
				"type held struct{ at instant.Instant }\n\nfunc (h *held) VisitDeliver() {}\n\n" +
				"func (h *held) VisitHoldUntil(until instant.Instant) { h.at = until }\n\n" +
				"var _ quiethours.DecisionVisitor = (*held)(nil)\n"}),
			[]string{"out-visitor-impl", "out-visitor-impl"}},
		{"a core's own test visiting another core is a finding", merged(fleet, map[string]string{
			"internal/assistant/fleetcount/ref_visit_test.go": "package fleetcount_test\n\n" +
				"import \"github.com/nwverse/persona/server/internal/agentref\"\n\ntype seen struct{ m agentref.MachineID }\n\n" +
				"func (r *seen) VisitRef(m agentref.MachineID, _ agentref.SessionID) { r.m = m }\n"}),
			[]string{"out-visitor-impl"}},
		// A generic type's Visit method names no core, and the file that declares it need not import one: in a package
		// another file of which imports a core or the kit, it implements a visitor all the same, undeclared code included.
		{"a generic visitor split from its use in undeclared code", merged(fleet, map[string]string{
			"internal/legacy/peek.go": "package legacy\n\ntype peek[T any] struct{ total *T }\n\n" +
				"func (p peek[T]) VisitTotal(total T) { *p.total = total }\n",
			"internal/legacy/legacy.go": "package legacy\n\nimport \"github.com/nwverse/persona/server/internal/assistant/" +
				"fleetcount\"\n\nvar total peek[fleetcount.RowCount]\n"}), []string{"layer-declared", "out-visitor-impl"}},
		{"a generic Visit method in a package that imports no core passes", map[string]string{
			"internal/legacy/peek.go": "package legacy\n\ntype peek[T any] struct{ total *T }\n\n" +
				"func (p peek[T]) VisitTotal(total T) { *p.total = total }\n"}, []string{"layer-declared"}},
		{"a type parameter in a core", core("func same[H comparable](a, b H) bool { return a == b }\n"),
			[]string{"core-type-param"}},
		{"a package var in a core", core("var seen int\n"), []string{"core-package-var"}},
		// Every clock selector the timeline's own purity test banned before core-clock-call replaced it, called or not.
		{"clock calls in a core", core("import \"time\"\n\nfunc clocks() {\n\tnow, since, until := time.Now(), time.Since, time.Until\n" +
			"\tafter, afterFunc, tick := time.After, time.AfterFunc, time.Tick\n\ttimer, ticker := time.NewTimer, time.NewTicker\n" +
			"\tzone, err := time.LoadLocation(\"UTC\")\n\ttime.Sleep(0)\n" +
			"\t_, _, _, _, _, _, _, _, _, _ = now, since, until, after, afterFunc, tick, timer, ticker, zone, err\n}\n"),
			[]string{"core-clock-call", "core-clock-call", "core-clock-call", "core-clock-call", "core-clock-call",
				"core-clock-call", "core-clock-call", "core-clock-call", "core-clock-call", "core-clock-call", "core-import"}},
		{"a map range in a core", core("func count(m map[uint8]bool) int {\n\tn := 0\n\tfor range m {\n\t\tn++\n\t}\n\treturn n\n}\n"),
			[]string{"core-map-range"}},
		{"a receiver index write", core("// Tags is a list. The zero Tags is none.\ntype Tags struct{ tags []Tag }\n\n" +
			"// Tag is a tag. The zero Tag is none.\ntype Tag struct{ tag uint8 }\n\n" +
			"func (t Tags) first(g Tag) Tags {\n\tt.tags[0] = g\n\treturn t\n}\n"), []string{"core-receiver-write"}},
		{"a getter with a dummy parameter", core("// Note is a note. The zero Note is none.\ntype Note struct {\n\ttitle Tag\n\tbody  Tag\n}\n\n" +
			"// Tag is a tag. The zero Tag is none.\ntype Tag struct{ tag uint8 }\n\n" +
			"// Title hands the title out.\nfunc (n Note) Title(_ Tag) Tag { return n.title }\n"), []string{"core-accessor"}},
		{"Decide with a sealed Command passes", plantedDirs(t, "internal/assistant/inbox/item"), []string{}},
		{"another package's interface as a parameter", merged(fleet, core("import \"github.com/nwverse/persona/server/internal/agentref\"\n\n"+
			"// Tag is a tag. The zero Tag is none.\ntype Tag struct{ tag uint8 }\n\n"+
			"// Tell tells v nothing.\nfunc (g Tag) Tell(v agentref.RefVisitor) {}\n")), []string{"core-interface-param"}},
		{"standard-library named primitives and collections in a core", core("import (\n\t\"bytes\"\n\t\"iter\"\n\ts \"sort\"\n\t\"strings\"\n)\n\n" +
			"// Tag is a tag. The zero Tag is none.\ntype Tag struct{ tag uint8 }\n\n" +
			"type rawLines s.StringSlice\n\n" +
			"// Note holds raw stdlib types. The zero Note is none.\ntype Note struct {\n\tlines rawLines\n\tbuf   strings.Builder\n\traw   bytes.Buffer\n\tseq   iter.Seq[Tag]\n}\n\n" +
			"// NewNote takes a stdlib named slice.\nfunc NewNote(lines s.IntSlice, t Tag) Note { return Note{} }\n\n" +
			"// Add takes a stdlib named slice.\nfunc (n Note) Add(lines s.Float64Slice) Note { return n }\n"),
			[]string{"core-collection", "core-primitive-field", "core-primitive-field", "core-primitive-field",
				"core-primitive-field", "core-primitive-signature", "core-primitive-signature"}},
		{"iter.Seq as a parameter reports core-interface-param only", core("import \"iter\"\n\n" +
			"// Tag is a tag. The zero Tag is none.\ntype Tag struct{ tag uint8 }\n\n" +
			"// Each walks tags.\nfunc (g Tag) Each(seq iter.Seq[Tag]) Tag { return g }\n"),
			[]string{"core-interface-param"}},
		{"the hub importing a core", merged(fleet, app("internal/hub", fleetImport, "var badge = fleetcount.SurfaceBadge()\n")),
			[]string{"path-no-core"}},
		// An adapter's Wire hands application code a domain value's raw shape, called or passed on; tests are not counted.
		{"an adapter's Wire passed on in undeclared code", merged(fleet, refAdapt,
			map[string]string{"internal/legacy/legacy.go": "package legacy\n\nimport \"github.com/nwverse/persona/server/internal/" +
				"agentref/agentrefadapt\"\n\nvar unwrap = agentrefadapt.Wire\n"}), []string{"app-bridge-call", "layer-declared"}},
		{"an adapter's Wire in a test passes", merged(fleet, refAdapt,
			app("internal/assistant/sample", "", ""), map[string]string{"internal/assistant/sample/sample_test.go": "package sample\n\n" +
				"import (\n\t\"github.com/nwverse/persona/server/internal/agentref\"\n\t\"github.com/nwverse/persona/server/internal/" +
				"agentref/agentrefadapt\"\n)\n\nfunc wire(r agentref.Ref) { _ = agentrefadapt.Wire(r) }\n"}), []string{}},
		// A call written as a statement drops its results when the parsed declarations say it has some: another
		// core's method, an interface method; one they cannot tell (the standard library's, a name declared both
		// ways) is left alone.
		{"calls written as statements in a core", merged(fleet, core("import \"github.com/nwverse/persona/server/internal/agentref\"\n\n"+
			"// Tag is a tag. The zero Tag is none.\ntype Tag struct{ tag uint8 }\n\n"+
			"// next is what follows a tag.\ntype next interface{ following() Tag }\n\n"+
			"func skip(n next, r agentref.Ref) {\n\tr.IsSame(r)\n\tn.following()\n}\n")),
			[]string{"core-outcome-return", "core-outcome-return"}},
		{"calls the declarations cannot resolve pass", core("import \"strings\"\n\n" +
			"// Tag is a tag. The zero Tag is none.\ntype Tag struct{ tag uint8 }\n\nfunc (t Tag) mark() {}\n\n" +
			"// marker is something marked.\ntype marker interface{ mark() Tag }\n\n" +
			"func keep(m marker, raw string) {\n\tstrings.ToUpper(raw)\n\tm.mark()\n}\n"), []string{}},
		{"a library importing the kit", pkgFiles("internal/lib", "library",
			"import \"github.com/nwverse/persona/server/internal/domainkit/instant\"\n\nvar zero instant.Instant\n"),
			[]string{"layer-library"}},
		// An adapter reads the contract types it names from their declarations: what their methods and funcs take
		// (and, handed out, return) is what an adapter may hand out, or it is a way out for raw values.
		{"a contract interface embedding one that takes raw text, as a parameter", adapt(contract(
			"// Records keeps raw records.\ntype Records interface{ Put(key string) error }\n\n"+
				"// Store is the records and a close.\ntype Store interface {\n\tRecords\n\tClose() error\n}\n"),
			"func Save(w fleetcount.Why, s assistantwire.Store) error { return s.Close() }\n"),
			[]string{"adapter-exported-result"}},
		{"a pointer to a contract interface", adapt(contract(
			"// Records keeps raw records.\ntype Records interface{ Put(key string) error }\n"),
			"func Save(w fleetcount.Why, s *assistantwire.Records) {}\n"), []string{"adapter-exported-result"}},
		{"a contract func type declared in a generated file, as a result", adapt(merged(contract(""), map[string]string{
			"internal/assistantwire/lookup_gen.go": "// Code generated by a case. DO NOT EDIT.\n\npackage assistantwire\n\n" +
				"// Lookup finds raw text.\ntype Lookup func(key string) string\n"}),
			"func Look(w fleetcount.Why) assistantwire.Lookup { return nil }\n"), []string{"adapter-exported-result"}},
		{"a contract type the tree does not declare", adapt(contract(""),
			"func Gone(w fleetcount.Why) assistantwire.Missing { return assistantwire.Missing{} }\n"),
			[]string{"adapter-exported-result"}},
		{"a contract struct holding a standard interface, as a result", adapt(contract("import \"io\"\n\n"+
			"// Upload is a body to send.\ntype Upload struct{ Body io.Reader }\n"),
			"func Up(w fleetcount.Why) assistantwire.Upload { return assistantwire.Upload{} }\n"),
			[]string{"adapter-exported-result"}},
		{"a callback whose result the adapter can write into", adapt(contract(""),
			"func Get(w fleetcount.Why, next func() map[string]string) {}\n"), []string{"adapter-exported-result"}},
		{"contract data and an interface over contracts pass", adapt(contract("import \"time\"\n\n"+
			"// Row is a stored row.\ntype Row struct {\n\tWhy  string\n\tAt   time.Time\n\tTags map[string]string\n}\n\n"+
			"// Rows takes rows.\ntype Rows interface {\n\tPut(r Row) error\n\tLast() (Row, error)\n}\n"),
			"func Put(w fleetcount.Why, rows assistantwire.Rows) error { return rows.Put(assistantwire.Row{}) }\n\n"+
				"func Fill(w fleetcount.Why, r *assistantwire.Row) {}\n\n"+
				"func Rows(w fleetcount.Why) (assistantwire.Rows, []assistantwire.Row) { return nil, nil }\n"), []string{}},
	} {
		t.Run(c.name, func(t *testing.T) {
			got, findings, _ := domainCase(t, c.files, []string{"internal/assistantwire"})
			if have := rules(got); !slices.Equal(have, c.want) {
				var msgs []string
				for _, f := range findings {
					msgs = append(msgs, f.String())
				}
				t.Errorf("got %q, want %q:\n%s", have, c.want, strings.Join(msgs, "\n"))
			}
		})
	}
}

// legacyCore is a core that predates the rules (tokenuse, one of the five legacyCores names): two package variables,
// a frozen import of time, and an application package that builds one of its types by literal.
func legacyCore() map[string]string {
	return merged(pkgFiles("internal/tokenuse", "core", "import \"time\"\n\nvar a, b int\n\nfunc since(t time.Time) bool { return t.IsZero() }\n\n"+
		"// Row is a row. The zero Row is none.\ntype Row struct{ row uint8 }\n"),
		app("internal/user", "\t\"github.com/nwverse/persona/server/internal/tokenuse\"\n", "var r = tokenuse.Row{}\n"))
}

// judge runs every rule over files and holds them to the lists' text.
func judge(t *testing.T, files map[string]string, undeclared, edges, baseline string, base *domainrules.Base) domainrules.Verdict {
	t.Helper()
	_, findings, counts := domainCase(t, files, nil)
	lists := domainrules.ParseLists("", undeclared, edges, baseline)
	all := merged(plantedKit(t), files)
	pkgs, err := domainrules.LoadFiles(serverModule, all)
	if err != nil {
		t.Fatal(err)
	}
	return domainrules.Judge(findings, counts, pkgs, lists, base, "")
}

// baseOf is the merge base a case is held to: files measured as TestDomainLayers measures the merge base's tree, the
// lists it had, and the files the change moved since.
func baseOf(t *testing.T, files map[string]string, undeclared, edges, baseline string, moves ...domainrules.Move) *domainrules.Base {
	t.Helper()
	_, findings, counts := domainCase(t, files, nil)
	return &domainrules.Base{Measure: domainrules.Measured(findings, counts),
		Lists: domainrules.ParseLists("", undeclared, edges, baseline), Moves: moves}
}

// failing is the failures that contain want, and whether every failure is one of them.
func failing(v domainrules.Verdict, want ...string) bool {
	if len(v.Failures) != len(want) {
		return false
	}
	for i, w := range want {
		if !strings.Contains(v.Failures[i], w) {
			return false
		}
	}
	return true
}

// The lists hold the legacy code: counts that only fall (with a base, never above the merge base's either), frozen
// imports and frozen undeclared packages, each reporting what can be tidied.
func TestDomainLists(t *testing.T) {
	files := legacyCore()
	const edges = "internal/tokenuse imports time\n"
	const baseline = "internal/tokenuse core-package-var 2\ninternal/tokenuse out-literal 1\n"
	if v := judge(t, files, "", edges, baseline, nil); len(v.Failures)+len(v.Notices) != 0 {
		t.Errorf("the legacy core at its baseline: %q, %q", v.Failures, v.Notices)
	}

	// A rise fails and names the findings; a fall is a notice saying how to lower.
	v := judge(t, files, "", edges, "internal/tokenuse core-package-var 1\ninternal/tokenuse out-literal 1\n", nil)
	if len(v.Failures) != 3 || !strings.Contains(v.Failures[2], "2 now, above its limit (baseline 1)") {
		t.Errorf("a baseline rise: %q", v.Failures)
	}
	v = judge(t, files, "", edges, "internal/tokenuse core-package-var 3\ninternal/tokenuse out-literal 1\n", nil)
	if len(v.Failures) != 0 || len(v.Notices) != 1 || !strings.Contains(v.Notices[0], "lower_baselines.py --only domain-rules") {
		t.Errorf("a count under its baseline: %q, %q", v.Failures, v.Notices)
	}

	// The importer's literal counts against the legacy core it builds.
	v = judge(t, files, "", edges, "internal/tokenuse core-package-var 2\n", nil)
	if len(v.Failures) != 2 || !strings.Contains(v.Failures[0], "RULE out-literal") ||
		!strings.Contains(v.Failures[1], "internal/tokenuse out-literal: 1 now") {
		t.Errorf("an importer literal of a legacy core: %q", v.Failures)
	}

	// With a base, a count may not rise above the merge base's.
	fewer := merged(files, map[string]string{"internal/tokenuse/tokenuse.go": strings.Replace(
		files["internal/tokenuse/tokenuse.go"], "var a, b int", "var a int", 1)})
	v = judge(t, files, "", edges, baseline, baseOf(t, fewer, "", edges, baseline))
	if len(v.Failures) != 3 || !strings.Contains(v.Failures[2], "merge base 1, baseline 2") {
		t.Errorf("a rise above the merge base: %q", v.Failures)
	}

	// A swapped import fails, and the frozen one that left is a notice.
	swapped := merged(files, map[string]string{"internal/tokenuse/tokenuse.go": strings.Replace(files["internal/tokenuse/tokenuse.go"],
		"import \"time\"\n\nvar a, b int\n\nfunc since(t time.Time) bool { return t.IsZero() }\n",
		"import \"os\"\n\nvar a, b int\n\nfunc since(name string) bool { return os.Getenv(name) == \"\" }\n", 1)})
	v = judge(t, swapped, "", edges, baseline, nil)
	if len(v.Failures) != 1 || !strings.Contains(v.Failures[0], "RULE core-import internal/tokenuse/tokenuse.go imports os") ||
		!strings.Contains(v.Failures[0], "\"Domain model\". If the import only moved here with its package and legacy_edges.txt lists it under the old path, python3 scripts/test/name_freezes.py moves carries its line.") ||
		len(v.Notices) != 1 || !strings.Contains(v.Notices[0], "imports time no longer happens") {
		t.Errorf("a swapped legacy import: %q, %q", v.Failures, v.Notices)
	}

	// A rule without a count takes no baseline line.
	v = judge(t, files, "", edges, "internal/tokenuse core-hook 1\n"+baseline, nil)
	if len(v.Failures) != 1 || !strings.Contains(v.Failures[0], "core-hook takes no count") {
		t.Errorf("a strict rule in the baseline: %q", v.Failures)
	}

	// Undeclared packages: a frozen one passes, a new one fails, one that declares now or is gone is a notice.
	fresh := map[string]string{"internal/old/old.go": "package old\n", "internal/fresh/fresh.go": "package fresh\n"}
	v = judge(t, merged(files, fresh), "internal/gone\ninternal/old\ninternal/tokenuse\ninternal/user\ninternal/waiting #77\n", edges, baseline, nil)
	if len(v.Failures) != 1 || !strings.Contains(v.Failures[0], "RULE layer-declared internal/fresh declares no layer") ||
		!strings.Contains(v.Failures[0], "Domain model\". If the package only moved here and undeclared.txt lists its old path, python3 scripts/test/name_freezes.py moves carries its line.") ||
		len(v.Notices) != 4 {
		t.Errorf("frozen undeclared packages: %q, %q", v.Failures, v.Notices)
	}
}

// No change grants itself room: core, seam and outside counts are kept only for the five legacy cores, and with a
// base a count line, an allowance or a frozen name the merge base's lists lack gives nothing and fails.
func TestDomainListsGiveNoNewRoom(t *testing.T) {
	legacy := legacyCore()
	const edges = "internal/tokenuse imports time\n"
	const baseline = "internal/tokenuse core-package-var 2\ninternal/tokenuse out-literal 1\n"

	// A core declared since the rules landed: its findings take no line, base or not.
	newCore := merged(legacy, pkgFiles("internal/zzcore", "core",
		"// Note is a note. The zero Note is none.\ntype Note struct {\n\tTitle string\n\tsize  uint8\n}\n"))
	lines := baseline + "internal/zzcore core-exported-field 1\ninternal/zzcore core-primitive-field 2\n"
	sorted := "internal/tokenuse core-package-var 2\ninternal/tokenuse out-literal 1\ninternal/zzcore core-exported-field 1\n" +
		"internal/zzcore core-primitive-field 2\n"
	if lines != sorted {
		t.Fatal("the case's baseline is out of order")
	}
	lists := domainrules.ParseLists("", "", edges, lines)
	if len(lists.Problems) != 2 || !strings.Contains(lists.Problems[0], "line 3 counts core-exported-field for internal/zzcore: "+
		"core-*, seam-* and out-* counts hold only the cores that predate the rules (internal/assistant/chatsearch, "+
		"internal/assistant/todo/timeline, internal/conversationlog, internal/sessionhistory, internal/tokenuse)") {
		t.Errorf("a new core's count lines: %q", lists.Problems)
	}
	v := judge(t, newCore, "", edges, lines, baseOf(t, legacy, "", edges, baseline))
	if !failing(v, "line 3 counts core-exported-field", "line 4 counts core-primitive-field",
		"RULE core-exported-field", "internal/zzcore core-exported-field: 1 now, above its limit (baseline 0)",
		"RULE core-primitive-field", "RULE core-primitive-field",
		"internal/zzcore core-primitive-field: 2 now, above its limit (baseline 0)") {
		t.Errorf("a new core with lines of its own, with a base: %q", v.Failures)
	}

	// An application package's count line it wrote itself holds without a base (the line is all there is), and with
	// one the merge base's count of zero holds it.
	seats := merged(legacy, app("internal/zznewapp", "", "// Seat is a record.\ntype Seat struct{ Name string }\n"))
	own := baseline + "internal/zznewapp app-exported-record 1\n"
	if v := judge(t, seats, "", edges, own, nil); len(v.Failures) != 0 {
		t.Errorf("an application count line without a base: %q", v.Failures)
	}
	v = judge(t, seats, "", edges, own, baseOf(t, legacy, "", edges, baseline))
	if !failing(v, "domain_layers/baseline.txt line 3: internal/zznewapp app-exported-record gives room for 1, more "+
		"than the 0 the merge base's baseline gives: a count's line only falls",
		"RULE app-exported-record internal/zznewapp adds an exported-field record (count 1 > 0)") {
		t.Errorf("an application count line the merge base lacks: %q", v.Failures)
	}

	// A count line raised by hand: the merge base's count holds the code, and the line itself fails, so it gives no
	// room to a later run without a base either.
	raised := "internal/tokenuse core-package-var 5\ninternal/tokenuse out-literal 1\n"
	v = judge(t, legacy, "", edges, raised, baseOf(t, legacy, "", edges, baseline))
	if !failing(v, "domain_layers/baseline.txt line 1: internal/tokenuse core-package-var gives room for 5, more than "+
		"the 2 the merge base's baseline gives") {
		t.Errorf("a count line raised by hand: %q", v.Failures)
	}

	// An allowance the change wrote for its own type: no room, and its line fails.
	withSeat := merged(legacy, app("internal/user", "\t\"github.com/nwverse/persona/server/internal/tokenuse\"\n",
		"var r = tokenuse.Row{}\n\n// Seat is a record.\ntype Seat struct{ Name string }\n"))
	allowed := baseline + "internal/user app-exported-record Seat #13999\n"
	v = judge(t, withSeat, "", edges, allowed, baseOf(t, legacy, "", edges, baseline))
	if !failing(v, "domain_layers/baseline.txt line 3: the allowance for Seat (#13999) is not on the merge base's baseline",
		"RULE app-exported-record internal/user adds an exported-field record (count 1 > 0)") {
		t.Errorf("an allowance the merge base lacks: %q", v.Failures)
	}

	// A frozen name the change wrote: an undeclared package of its own, an import of its own.
	bare := merged(legacy, map[string]string{"internal/zzbare/zzbare.go": "package zzbare\n"})
	v = judge(t, bare, "internal/tokenuse\ninternal/user\ninternal/zzbare\n", edges, baseline,
		baseOf(t, legacy, "internal/tokenuse\ninternal/user\n", edges, baseline))
	if !failing(v, "domain_layers/undeclared.txt line 3: internal/zzbare is not on the merge base's list") {
		t.Errorf("an undeclared package the merge base lacks: %q", v.Failures)
	}
	osImport := merged(legacy, map[string]string{"internal/tokenuse/env.go": "package tokenuse\n\nimport \"os\"\n\n" +
		"func set(name string) bool { return os.Getenv(name) != \"\" }\n"})
	v = judge(t, osImport, "", "internal/tokenuse imports os\n"+edges, baseline, baseOf(t, legacy, "", edges, baseline))
	if !failing(v, "domain_layers/legacy_edges.txt line 1: internal/tokenuse imports os is not on the merge base's list") {
		t.Errorf("a frozen import the merge base lacks: %q", v.Failures)
	}
}

// An allowance is one open PR's room: it names the type the PR adds and counts only while that type is in the
// package, so no other change can spend it. Once the type is on main the count takes it in; with a base, the merge
// base's count holds what it has already, and only an allowance its baseline lists gives room.
func TestDomainAllowances(t *testing.T) {
	legacy := legacyCore()
	files := merged(legacy, app("internal/seats", "", "// Seat is a record.\ntype Seat struct{ Name string }\n"))
	const lines = "internal/tokenuse core-package-var 2\ninternal/tokenuse out-literal 1\n"
	const edges = "internal/tokenuse imports time\n"
	const id = "internal/seats app-exported-record"
	v := judge(t, files, "", edges, lines, nil)
	if len(v.Failures) != 1 || !strings.Contains(v.Failures[0], "RULE app-exported-record internal/seats adds an exported-field record (count 1 > 0)") ||
		!strings.Contains(v.Failures[0], "If its code only moved here, python3 scripts/test/name_freezes.py moves carries its lines.") {
		t.Errorf("a new record with no line: %q", v.Failures)
	}
	v = judge(t, files, "", edges, id+" Seat #4512\n"+lines, nil)
	if len(v.Failures) != 0 || len(v.Notices) != 1 || !strings.Contains(v.Notices[0], "Seat (#4512) is in internal/seats now: take it into the count with python3 scripts/ci/lower_baselines.py --only domain-rules") {
		t.Errorf("the allowed type: %q, %q", v.Failures, v.Notices)
	}
	// The room is the named type's: another change's record does not get it.
	v = judge(t, files, "", edges, id+" Desk #4512\n"+lines, nil)
	if len(v.Failures) != 1 || !strings.Contains(v.Failures[0], "(count 1 > 0)") || len(v.Notices) != 1 ||
		!strings.Contains(v.Notices[0], "internal/seats app-exported-record allows Desk (#4512), not here yet; once a type's PRs have all closed, python3 scripts/test/name_freezes.py pending --write --only domain_layers_baseline drops its line") {
		t.Errorf("another type than the allowed one: %q, %q", v.Failures, v.Notices)
	}
	// With a base: the merge base without the type, whose baseline lists the allowance, leaves the room; one that has
	// the type (the PR merged) holds the count.
	v = judge(t, files, "", edges, id+" Seat #4512\n"+lines, baseOf(t, legacy, "", edges, id+" Seat #4512\n"+lines))
	if len(v.Failures) != 0 {
		t.Errorf("the allowed type, the merge base without it: %q", v.Failures)
	}
	more := merged(files, map[string]string{"internal/seats/desk.go": "package seats\n\n// Desk is another record.\ntype Desk struct{ Name string }\n"})
	landed := id + " 1\n" + id + " Seat #4512\n" + lines
	v = judge(t, more, "", edges, landed, baseOf(t, files, "", edges, landed))
	if len(v.Failures) != 1 || !strings.Contains(v.Failures[0], "(count 2 > 1)") {
		t.Errorf("a record beside an allowed type the merge base has: %q", v.Failures)
	}
	// Taking a landed type into its count (lower_baselines.py) keeps the room the same, so it passes with a base.
	v = judge(t, files, "", edges, id+" 1\n"+lines, baseOf(t, files, "", edges, id+" Seat #4512\n"+lines))
	if len(v.Failures) != 0 {
		t.Errorf("an allowance taken into its count: %q", v.Failures)
	}
	// Only the counts of named types take an allowance.
	lists := domainrules.ParseLists("", "", "", "internal/tokenuse core-package-var Seat #4512\n")
	if len(lists.Problems) != 1 || !strings.Contains(lists.Problems[0], "is an allowance for core-package-var, which takes none") {
		t.Errorf("an allowance for a finding: %q", lists.Problems)
	}
}

// Code that moves carries its lines: name_freezes.py moves (domainrules.Carried) renames a moved package's frozen
// name and counts along git's renames, and with a base the packages files moved between are held to their total at
// the merge base. A move that also adds a record still fails, and so does a package that only claims to have moved.
func TestDomainMoves(t *testing.T) {
	legacy := legacyCore()
	const edges = "internal/tokenuse imports time\n"
	const coreLines = "internal/tokenuse core-package-var 2\ninternal/tokenuse out-literal 1\n"
	old := merged(legacy, map[string]string{
		"internal/iphonectl/a.go": "package iphonectl\n\n// A is a record.\ntype A struct{ Name string }\n",
		"internal/iphonectl/b.go": "package iphonectl\n\n// B is a record.\ntype B struct{ Name string }\n\n// Mode is a mode.\ntype Mode string\n",
	})
	const undeclared = "internal/iphonectl\n"
	baseline := "internal/iphonectl app-exported-record 2\ninternal/iphonectl app-named-primitive 1\n" + coreLines
	moved := func(files map[string]string, to map[string]string) map[string]string {
		out := merged(files)
		for from, dest := range to {
			text := out[from]
			delete(out, from)
			pkg := dest[strings.LastIndex(dest[:strings.LastIndex(dest, "/")], "/")+1 : strings.LastIndex(dest, "/")]
			out[dest] = strings.Replace(text, "package iphonectl", "package "+pkg, 1)
		}
		return out
	}
	carry := func(files map[string]string, moves []domainrules.Move) domainrules.ListTexts {
		_, findings, counts := domainCase(t, files, nil)
		pkgs, err := domainrules.LoadFiles(serverModule, merged(plantedKit(t), files))
		if err != nil {
			t.Fatal(err)
		}
		texts := domainrules.ListTexts{Undeclared: "# frozen\n" + undeclared, Edges: edges, Baseline: "# counts\n" + baseline}
		lists := domainrules.ParseLists("", texts.Undeclared, texts.Edges, texts.Baseline)
		return domainrules.Carried(texts, lists, findings, counts, pkgs, moves)
	}
	base := func(moves ...domainrules.Move) *domainrules.Base {
		return baseOf(t, old, undeclared, edges, baseline, moves...)
	}

	// A whole package moves: before its lines are carried it fails, and the messages say how they follow it.
	whole := moved(old, map[string]string{"internal/iphonectl/a.go": "internal/modules/phone/iphonectl/a.go",
		"internal/iphonectl/b.go": "internal/modules/phone/iphonectl/b.go"})
	moves := domainrules.Moves([][2]string{{"internal/iphonectl/a.go", "internal/modules/phone/iphonectl/a.go"},
		{"internal/iphonectl/b.go", "internal/modules/phone/iphonectl/b.go"}})
	v := judge(t, whole, undeclared, edges, baseline, base(moves...))
	if !failing(v, "RULE layer-declared internal/modules/phone/iphonectl declares no layer",
		"RULE app-exported-record internal/modules/phone/iphonectl adds an exported-field record (count 2 > 0)",
		"RULE app-named-primitive internal/modules/phone/iphonectl adds a named primitive type (count 1 > 0)") ||
		!strings.Contains(v.Failures[0], "python3 scripts/test/name_freezes.py moves carries its line") {
		t.Errorf("a moved package before its lines are carried: %q", v.Failures)
	}
	got := carry(whole, moves)
	if got.Undeclared != "# frozen\ninternal/modules/phone/iphonectl\n" ||
		got.Baseline != "# counts\ninternal/modules/phone/iphonectl app-exported-record 2\n"+
			"internal/modules/phone/iphonectl app-named-primitive 1\n"+coreLines || got.Edges != edges {
		t.Errorf("the carried lists: %q", got)
	}
	v = judge(t, whole, got.Undeclared, got.Edges, got.Baseline, base(moves...))
	if len(v.Failures) != 0 {
		t.Errorf("a moved package with its lines carried: %q", v.Failures)
	}

	// A move that adds a record: the lines carry what moved, not the new record, and the check fails on it.
	grown := merged(whole, map[string]string{"internal/modules/phone/iphonectl/c.go": "package iphonectl\n\n// C is new.\ntype C struct{ Name string }\n"})
	got = carry(grown, moves)
	if !strings.Contains(got.Baseline, "internal/iphonectl app-exported-record 2\n") {
		t.Errorf("a group that rose keeps its lines: %q", got.Baseline)
	}
	v = judge(t, grown, "internal/modules/phone/iphonectl\n", edges,
		"internal/modules/phone/iphonectl app-exported-record 3\ninternal/modules/phone/iphonectl app-named-primitive 1\n"+coreLines, base(moves...))
	if !failing(v, "domain_layers/baseline.txt line 1: internal/modules/phone/iphonectl app-exported-record gives room for 3, more than the 2",
		"RULE app-exported-record internal/iphonectl, internal/modules/phone/iphonectl add an exported-field record (count 3 > 2 together)",
		"internal/iphonectl, internal/modules/phone/iphonectl app-exported-record: 3 now together, above their 2 at the merge base") {
		t.Errorf("a move that adds a record, its line raised by hand: %q", v.Failures)
	}

	// A split: each package the files went to keeps its own count, and together they hold the merge base's.
	split := moved(old, map[string]string{"internal/iphonectl/a.go": "internal/iphonewire/a.go",
		"internal/iphonectl/b.go": "internal/modules/phone/iphonectl/b.go"})
	splitMoves := domainrules.Moves([][2]string{{"internal/iphonectl/a.go", "internal/iphonewire/a.go"},
		{"internal/iphonectl/b.go", "internal/modules/phone/iphonectl/b.go"}})
	got = carry(split, splitMoves)
	if got.Undeclared != "# frozen\ninternal/iphonewire\ninternal/modules/phone/iphonectl\n" ||
		got.Baseline != "# counts\ninternal/iphonewire app-exported-record 1\ninternal/modules/phone/iphonectl app-exported-record 1\n"+
			"internal/modules/phone/iphonectl app-named-primitive 1\n"+coreLines {
		t.Errorf("the lists carried along a split: %q", got)
	}
	v = judge(t, split, got.Undeclared, got.Edges, got.Baseline, base(splitMoves...))
	if len(v.Failures) != 0 {
		t.Errorf("a split with its lines carried: %q", v.Failures)
	}

	// A package that did not move gets no carried line, and one that only claims a moved package's line fails.
	fresh := merged(old, map[string]string{"internal/zzother/x.go": "package zzother\n\n// X is a record.\ntype X struct{ Name string }\n"})
	if got := carry(fresh, nil); got.Undeclared != "# frozen\n"+undeclared || got.Baseline != "# counts\n"+baseline {
		t.Errorf("no move, nothing carried: %q", got)
	}
	v = judge(t, fresh, "internal/iphonectl\ninternal/zzother\n", edges, baseline+"internal/zzother app-exported-record 1\n", base())
	if !failing(v, "domain_layers/undeclared.txt line 2: internal/zzother is not on the merge base's list",
		"domain_layers/baseline.txt line 5: internal/zzother app-exported-record gives room for 1, more than the 0",
		"RULE app-exported-record internal/zzother adds an exported-field record (count 1 > 0)") {
		t.Errorf("a package that only claims a line: %q", v.Failures)
	}
}

// The lists' format, and lowering: counts fall to the tree's, an allowance whose type landed is taken into its count,
// one still waiting stays, and a count gone to zero leaves; no limit rises.
func TestDomainListFormat(t *testing.T) {
	for _, c := range []struct{ baseline, problem string }{
		{"b app-bridge-call 1\na app-bridge-call 1\n", "line 2 (a app-bridge-call) is out of order"},
		{"a app-bridge-call 1\na app-bridge-call 2\n", "line 2 repeats a app-bridge-call (line 1)"},
		{"internal/zzcore core-any 1\n", "line 1 counts core-any for internal/zzcore: core-*, seam-* and out-* counts hold only"},
		{"internal/zzseam seam-any 1\n", "line 1 counts seam-any for internal/zzseam"},
		{"internal/zzapp out-wire-ref 1\n", "line 1 counts out-wire-ref for internal/zzapp"},
		{"a app-exported-record B #1\na app-exported-record A #2\n", "line 2 (a app-exported-record) is out of order"},
		{"a app-exported-record A #1\na app-exported-record 2\n", "line 2 (a app-exported-record) is out of order"},
		{"a app-exported-record A #1\na app-exported-record A #2\n", "line 2 repeats a app-exported-record A (line 1)"},
		{"a no-such-rule 1\n", "line 1 names no rule: no-such-rule"},
		{"a core-any one\n", "line 1 is neither <key> <rule> <count> nor an allowance"},
		{"a core-any 4 #12\n", "line 1 is neither <key> <rule> <count> nor an allowance"},
		{"a app-exported-record Seat\n", "line 1 is neither <key> <rule> <count> nor an allowance"},
		{"a app-exported-record Seat 12\n", "line 1 is neither <key> <rule> <count> nor an allowance"},
	} {
		lists := domainrules.ParseLists("", "", "", c.baseline)
		if !slices.ContainsFunc(lists.Problems, func(p string) bool { return strings.Contains(p, c.problem) }) {
			t.Errorf("%q: problems %q, want %q", c.baseline, lists.Problems, c.problem)
		}
	}
	lists := domainrules.ParseLists("", "b\na\n", "", "")
	if len(lists.Problems) != 1 || !strings.Contains(lists.Problems[0], "line 2 (a) is out of order") {
		t.Errorf("an unsorted freeze list: %q", lists.Problems)
	}
	const text = "# header\n\na app-bridge-call 3\na app-lifecycle-write 2\nb app-exported-record 4\nb app-exported-record Landed #12\n" +
		"b app-exported-record Waiting #13 #15\nc app-bridge-call 1\nd app-named-primitive Fresh #14\ne app-named-primitive 2\n" +
		"e app-named-primitive Early #16\n"
	m := domainrules.Measure{
		Counts: map[string]int{"a app-bridge-call": 2, "a app-lifecycle-write": 5, "b app-exported-record": 5,
			"d app-named-primitive": 1, "e app-named-primitive": 2},
		Names: map[string]map[string]bool{"b app-exported-record": {"Landed": true, "Other": true},
			"d app-named-primitive": {"Fresh": true}, "e app-named-primitive": {"Early": true, "Kept": true}},
	}
	lists = domainrules.ParseLists("", "", "", text)
	if len(lists.Problems) != 0 {
		t.Fatalf("problems: %q", lists.Problems)
	}
	want := "# header\na app-bridge-call 2\na app-lifecycle-write 2\nb app-exported-record 5\nb app-exported-record Waiting #13 #15\n" +
		"d app-named-primitive 1\ne app-named-primitive 2\n"
	if lowered := lists.Baseline.Lowered(text, m); lowered != want {
		t.Errorf("Lowered = %q, want %q", lowered, want)
	}
}

// The hook checks one package: its own findings and the ones its importers' code counts against it, never another
// package's.
func TestDomainHookScope(t *testing.T) {
	all := merged(plantedKit(t), plantedFiles(t))
	pkgs, err := domainrules.LoadFiles(serverModule, all)
	if err != nil {
		t.Fatal(err)
	}
	idx := domainrules.NewIndex(serverModule, pkgs, plantedContracts).WithBareIDs(plantedBareIDs)
	for _, c := range []struct {
		scope string
		want  []string // the rules of its failures, sorted
	}{
		{"internal/assistant/hookless", []string{"core-hook"}},
		{"internal/assistant/inbox/item", []string{}},
		{"internal/assistant/quiethours", []string{}},
		{"internal/assistant/nofamily", []string{"core-fail-family"}},
		// fleetcount's own code passes; what its importers do to it is its hook's business too, the planted
		// parameterless visitors (badapp/visitorimpl.go's whyView) included.
		{"internal/assistant/fleetcount", []string{"core-importers", "out-alias", "out-conversion", "out-dot-import",
			"out-literal", "out-unsafe", "out-unsafe", "out-visitor-impl", "out-visitor-impl", "out-visitor-impl",
			"out-visitor-impl", "out-visitor-impl", "out-visitor-impl", "out-visitor-impl", "path-no-core"}},
	} {
		findings, _, err := domainrules.Run(pkgs, idx, c.scope)
		if err != nil {
			t.Fatal(err)
		}
		v := domainrules.Judge(findings, nil, pkgs, domainrules.Lists{}, nil, c.scope)
		got := []string{}
		for _, f := range v.Failures {
			if _, rest, ok := strings.Cut(f, "RULE "); ok {
				got = append(got, strings.Fields(rest)[0])
			}
		}
		slices.Sort(got)
		if !slices.Equal(got, c.want) {
			t.Errorf("the hook in %s: %q, want %q", c.scope, v.Failures, c.want)
		}
	}
}
