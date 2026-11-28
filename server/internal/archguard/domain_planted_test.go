package archguard

import (
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/nwverse/persona/server/internal/archguard/domainrules"
)

// plantedDir holds the domain rules' planted cases: a tree laid out like server/, with one failing case and one
// compliant twin per rule. The domain kit is read from the server itself.
const plantedDir = "testdata/domainrules/planted"

// plantedBareIDs is the wire-types baseline's bare-id names for the planted tree: none, so every bare ID field fails.
var plantedBareIDs = map[string]bool{}

// plantedContracts is contracts.txt for the planted tree.
var plantedContracts = []string{"internal/assistantwire", "internal/badwire", "internal/listedlib"}

// plantedFiles is the planted tree's files, by path under its root, and the domain kit's production files and hooks
// from the server, under internal/domainkit.
func plantedFiles(t *testing.T) map[string]string {
	t.Helper()
	files := map[string]string{}
	read := func(root, prefix string, keep func(rel string) bool) {
		err := filepath.WalkDir(root, func(path string, d fs.DirEntry, err error) error {
			if err != nil || d.IsDir() || !strings.HasSuffix(path, ".go") {
				return err
			}
			rel, err := filepath.Rel(root, path)
			if err != nil {
				return err
			}
			rel = prefix + filepath.ToSlash(rel)
			if !keep(rel) {
				return nil
			}
			text, err := os.ReadFile(path)
			files[rel] = string(text)
			return err
		})
		if err != nil {
			t.Fatal(err)
		}
	}
	read(plantedDir, "", func(string) bool { return true })
	read(filepath.Join(serverDir, "internal", "domainkit"), "internal/domainkit/", func(rel string) bool {
		return !strings.HasSuffix(rel, "_test.go") || strings.HasSuffix(rel, "/rules_test.go")
	})
	return files
}

// domainCase runs every rule over files (paths under a root laid out like server/) with the domain kit beside them,
// and gives each finding as "<rule> <file>:<line>" and each count as "<rule> <package> <n>", sorted.
func domainCase(t *testing.T, files map[string]string, contracts []string) ([]string, []domainrules.Finding,
	[]domainrules.Count) {
	t.Helper()
	all := plantedKit(t)
	for rel, text := range files {
		all[rel] = text
	}
	pkgs, err := domainrules.LoadFiles(serverModule, all)
	if err != nil {
		t.Fatal(err)
	}
	idx := domainrules.NewIndex(serverModule, pkgs, contracts).WithBareIDs(plantedBareIDs)
	findings, _, err := domainrules.Run(pkgs, idx, "")
	if err != nil {
		t.Fatal(err)
	}
	counts, err := domainrules.Ratchets(pkgs, idx)
	if err != nil {
		t.Fatal(err)
	}
	var got []string
	for _, f := range findings {
		got = append(got, f.Rule+" "+f.Pos.Filename+":"+strconv.Itoa(f.Pos.Line))
	}
	for _, c := range counts {
		got = append(got, c.Rule+" "+c.Key+" "+strconv.Itoa(c.N))
	}
	slices.Sort(got)
	return got, findings, counts
}

// plantedKit is the domain kit's production files and hooks, under internal/domainkit.
func plantedKit(t *testing.T) map[string]string {
	t.Helper()
	files := map[string]string{}
	for rel, text := range plantedFiles(t) {
		if strings.HasPrefix(rel, "internal/domainkit/") {
			files[rel] = text
		}
	}
	return files
}

// TestPlantedFindings runs the rules over the planted tree: each failing case gives exactly its findings, each
// compliant twin none, every rule has a failing case, every message carries RULE, WHY, FIX, GOOD and the doc section,
// and every GOOD path exists.
func TestPlantedFindings(t *testing.T) {
	got, findings, _ := domainCase(t, plantedFiles(t), plantedContracts)
	if !slices.Equal(got, plantedWant) {
		t.Errorf("planted findings differ:\n  missing: %q\n  extra:   %q", missing(plantedWant, got), missing(got, plantedWant))
	}
	seen := map[string]bool{}
	for _, g := range got {
		seen[strings.Fields(g)[0]] = true
	}
	const doc = `see docs/server/architecture.md "Domain model"`
	for _, f := range findings {
		if !strings.HasPrefix(f.Msg, "RULE "+f.Rule+" ") || !strings.Contains(f.Msg, ". WHY ") ||
			!strings.Contains(f.Msg, ". FIX ") || !strings.Contains(f.Msg, ". GOOD ") || !strings.HasSuffix(f.Msg, doc) {
			t.Errorf("message without RULE/WHY/FIX/GOOD/citation: %s", f.Msg)
		}
	}
	for _, id := range domainrules.RuleIDs() {
		if !seen[id] {
			t.Errorf("rule %s has no planted failing case", id)
		}
		good := domainrules.GoodPath(id)
		if _, err := os.Stat(filepath.Join(serverDir, "..", filepath.FromSlash(good))); err != nil {
			t.Errorf("rule %s's GOOD path %s does not exist", id, good)
		}
	}
}

// missing is what of want got lacks, as a multiset.
func missing(want, got []string) []string {
	left := slices.Clone(got)
	var out []string
	for _, w := range want {
		if i := slices.Index(left, w); i >= 0 {
			left = slices.Delete(left, i, i+1)
		} else {
			out = append(out, w)
		}
	}
	return out
}

// plantedWant is every finding and count the planted tree gives, as "<rule> <file>:<line>" and "<rule> <package>
// <n>", sorted. The compliant twins appear nowhere in it.
var plantedWant = []string{
	"adapter-exported-result internal/assistant/fleetcount/badadapt/badadapt.go:17",
	"adapter-exported-result internal/assistant/fleetcount/badadapt/badadapt.go:18",
	"adapter-exported-result internal/assistant/fleetcount/badadapt/badadapt.go:22",
	"adapter-exported-result internal/assistant/fleetcount/badadapt/badadapt.go:30",
	"adapter-exported-result internal/assistant/fleetcount/badadapt/badadapt.go:35",
	"adapter-exported-result internal/assistant/fleetcount/badadapt/contracts.go:11",
	"adapter-exported-result internal/assistant/fleetcount/badadapt/contracts.go:16",
	"adapter-exported-result internal/assistant/fleetcount/badadapt/contracts.go:27",
	"adapter-exported-result internal/assistant/fleetcount/badadapt/contracts.go:32",
	"adapter-exported-result internal/assistant/fleetcount/badadapt/contracts.go:42",
	"adapter-exported-result internal/assistant/fleetcount/badadapt/contracts.go:47",
	"adapter-exported-result internal/assistant/fleetcount/badadapt/foreign.go:14",
	"adapter-exported-result internal/assistant/fleetcount/badadapt/foreign.go:19",
	"adapter-exported-result internal/assistant/fleetcount/badadapt/foreign.go:24",
	"adapter-exported-result internal/assistant/fleetcount/badadapt/foreign.go:29",
	"adapter-exported-result internal/assistant/fleetcount/badadapt/foreign.go:34",
	"adapter-exported-result internal/assistant/fleetcount/badadapt/outparams.go:11",
	"adapter-exported-result internal/assistant/fleetcount/badadapt/outparams.go:12",
	"adapter-exported-result internal/assistant/fleetcount/badadapt/outparams.go:16",
	"adapter-exported-result internal/assistant/fleetcount/badadapt/outparams.go:21",
	"adapter-exported-result internal/assistant/fleetcount/badadapt/outparams.go:26",
	"adapter-exported-result internal/assistant/fleetcount/badadapt/outparams.go:34",
	"adapter-exported-result internal/assistant/fleetcount/badadapt/outparams.go:39",
	"adapter-exported-result internal/assistant/fleetcount/badadapt/outparams.go:44",
	"adapter-exported-result internal/assistant/fleetcount/badadapt/outparams.go:51",
	"adapter-exported-result internal/assistant/fleetcount/badadapt/outparams.go:58",
	"adapter-exported-result internal/assistant/fleetcount/badadapt/outparams.go:61",
	"adapter-exported-result internal/assistant/fleetcount/badadapt/outparams.go:70",
	"adapter-exported-result internal/assistant/notadapt/notadapt.go:11",
	"adapter-exported-result internal/assistant/notadapt/notadapt.go:14",
	"adapter-exported-result internal/assistant/notadapt/notadapt.go:17",
	"adapter-exported-result internal/assistant/notadapt/notadapt.go:20",
	"adapter-import internal/assistant/fleetcount/badadapt/badadapt.go:9",
	"adapter-name internal/assistant/fleetcount/badadapt/badadapt.go:16",
	"adapter-no-application internal/assistant/fleetcount/badadapt/badadapt.go:7",
	"app-bridge-call internal/assistant/badapp 1",
	"app-exported-record internal/assistant/badapp 1",
	"app-exported-record internal/assistant/badseam 1",
	"app-exported-record internal/badlib 1",
	"app-lifecycle-write internal/assistant/badapp 2",
	"app-named-primitive internal/assistant/badapp 1",
	"app-named-primitive internal/assistant/notadapt 2",
	"app-named-primitive internal/badlib 1",
	"contract-bare-id internal/badwire/badwire.go:12",
	"core-accessor internal/assistant/badcore/accessor.go:10",
	"core-accessor internal/assistant/badcore/accessor.go:13",
	"core-accessor internal/assistant/badcore/accessor.go:16",
	"core-accessor internal/assistant/badcore/accessor.go:19",
	"core-accessor internal/assistant/badcore/accessor.go:25",
	"core-accessor internal/assistant/badcore/codecmethod.go:6",
	"core-accessor internal/assistant/badseam/badseam.go:34",
	"core-accessor internal/assistant/badseam/badseam.go:38",
	"core-accessor internal/assistant/badseam/badseam.go:43",
	"core-accessor internal/assistant/badseam/badseam.go:44",
	"core-accessor internal/assistant/badseam/badseam.go:45",
	"core-accessor internal/assistant/badseam/hook_linux.go:5",
	"core-alias internal/assistant/badcore/alias.go:3",
	"core-any internal/assistant/badcore/any.go:4",
	"core-bare-type internal/assistant/badcore/baretype.go:3",
	"core-clock-call internal/assistant/badcore/clock.go:5",
	"core-codec-method internal/assistant/badcore/codecmethod.go:6",
	"core-collection internal/assistant/badcore/collection.go:4",
	"core-collection internal/assistant/badcore/kitwrapped.go:24",
	"core-collection internal/assistant/badcore/localname.go:25",
	"core-collection internal/assistant/badcore/stdnamed.go:18",
	"core-constructor internal/assistant/badcore/constructor.go:4",
	"core-constructor internal/assistant/badcore/constructor.go:7",
	"core-distinct-shape internal/assistant/badcore/fieldname.go:7",
	"core-doc internal/assistant/nodoc/nodoc.go:1",
	"core-effect internal/assistant/badcore/effect.go:3",
	"core-effect internal/assistant/badcore/effect.go:5",
	"core-exported-const internal/assistant/badcore/exportedconst.go:3",
	"core-exported-field internal/assistant/badcore/exportedfield.go:5",
	"core-fail-family internal/assistant/badcore/failfamily.go:6",
	"core-fail-family internal/assistant/nofamily/nofamily.go:1",
	"core-field-name internal/assistant/badcore/fieldname.go:4",
	"core-field-name internal/assistant/badcore/fieldname.go:7",
	"core-hook internal/assistant/hookless/hookless.go:1",
	"core-import internal/assistant/badcore/clock.go:3",
	"core-import internal/assistant/badcore/import.go:3",
	"core-importers internal/badwire/badwire.go:6",
	"core-interface-param internal/assistant/badcore/interfaceparam.go:4",
	"core-interface-param internal/assistant/badcore/localname.go:28",
	"core-map-range internal/assistant/badcore/maprange.go:5",
	"core-outcome-return internal/assistant/badcore/outcomereturn.go:10",
	"core-outcome-return internal/assistant/badcore/outcomereturn.go:14",
	"core-outcome-return internal/assistant/badcore/outcomereturn.go:20",
	"core-outcome-return internal/assistant/badcore/outcomereturn.go:25",
	"core-outcome-return internal/assistant/badcore/outcomereturn.go:26",
	"core-package-var internal/assistant/badcore/packagevar.go:3",
	"core-params internal/assistant/badcore/params.go:4",
	"core-part-name internal/assistant/badcore/partname.go:6",
	"core-pointer-receiver internal/assistant/badcore/pointerreceiver.go:4",
	"core-primitive-field internal/assistant/badcore/kitwrapped.go:11",
	"core-primitive-field internal/assistant/badcore/kitwrapped.go:12",
	"core-primitive-field internal/assistant/badcore/localname.go:17",
	"core-primitive-field internal/assistant/badcore/localname.go:18",
	"core-primitive-field internal/assistant/badcore/primitivefield.go:5",
	"core-primitive-field internal/assistant/badcore/primitivefield.go:6",
	"core-primitive-field internal/assistant/badcore/stdnamed.go:10",
	"core-primitive-field internal/assistant/badcore/stdnamed.go:11",
	"core-primitive-signature internal/assistant/badcore/codecmethod.go:6",
	"core-primitive-signature internal/assistant/badcore/kitwrapped.go:16",
	"core-primitive-signature internal/assistant/badcore/kitwrapped.go:21",
	"core-primitive-signature internal/assistant/badcore/kitwrapped.go:28",
	"core-primitive-signature internal/assistant/badcore/localname.go:22",
	"core-primitive-signature internal/assistant/badcore/primitivesignature.go:4",
	"core-primitive-signature internal/assistant/badcore/stdnamed.go:15",
	"core-primitive-signature internal/assistant/badcore/stdnamed.go:18",
	"core-public-evolve internal/assistant/badcore/publicevolve.go:4",
	"core-receiver-write internal/assistant/badcore/receiverwrite.go:7",
	"core-service-name internal/assistant/badcore/servicename.go:4",
	"core-tag internal/assistant/badcore/tag.go:4",
	"core-test-package internal/assistant/badcore/whitebox_test.go:1",
	"core-type-param internal/assistant/badcore/typeparam.go:4",
	"core-visitor-name internal/assistant/badcore/visitorname.go:5",
	"core-wire-method internal/assistant/badcore/wiremethod.go:9",
	"core-zero-doc internal/assistant/badcore/zerodoc.go:4",
	"layer-application internal/assistant/pure/pure.go:3",
	"layer-contract internal/listedlib/listedlib.go:3",
	"layer-contract internal/notlisted/notlisted.go:3",
	"layer-declared internal/nolayer/nolayer.go:1",
	"layer-declared internal/typo/typo.go:3",
	"layer-kit internal/mykit/mykit.go:3",
	"layer-library internal/db/usage.go:3",
	"layer-one internal/twolayers/b.go:3",
	"layer-shared internal/assistant/sharedhere/sharedhere.go:3",
	"layer-shared internal/badkernel/badkernel.go:10",
	"layer-shared internal/badkernel/badkernel.go:13",
	"layer-shared internal/badkernel/badkernel.go:16",
	"layer-testkit cmd/fixtures/main.go:4",
	"layer-testkit internal/assistant/badapp/testkit.go:3",
	"out-alias internal/assistant/badapp/alias.go:5",
	"out-clock-read internal/assistant/badapp/clockread.go:10",
	"out-code-mint internal/assistant/badapp/codemint.go:5",
	"out-conversion internal/assistant/badapp/conversion.go:6",
	"out-dot-import internal/assistant/badapp/dotimport.go:3",
	"out-literal internal/assistant/badapp/literal.go:5",
	"out-log-use internal/assistant/badapp/loguse.go:11",
	"out-parse-call internal/assistant/badapp/parsecall.go:6",
	"out-rules-import internal/assistant/badapp/rulesimport.go:3",
	"out-slot-mint internal/assistant/badapp/slotmint.go:5",
	"out-unsafe internal/assistant/badapp/unsafe.go:11",
	"out-unsafe internal/assistant/badapp/unsafe.go:5",
	"out-visitor-impl internal/assistant/badapp/genericvisitor.go:10",
	"out-visitor-impl internal/assistant/badapp/genericvisitor.go:7",
	"out-visitor-impl internal/assistant/badapp/genericvisitor.go:8",
	"out-visitor-impl internal/assistant/badapp/genericvisitor.go:9",
	"out-visitor-impl internal/assistant/badapp/visitorimpl.go:12",
	"out-visitor-impl internal/assistant/badapp/visitorimpl.go:17",
	"out-visitor-impl internal/assistant/badapp/visitorimpl.go:18",
	"out-wire-ref internal/assistant/badapp/wireref.go:6",
	"path-no-core internal/db/usage.go:3",
	"path-no-core internal/hub/core.go:3",
	"seam-any internal/assistant/badseam/badseam.go:15",
	"seam-any internal/assistant/badseam/badseam.go:18",
	"seam-any internal/assistant/badseam/badseam.go:19",
	"seam-any internal/assistant/badseam/badseam.go:22",
	"seam-any internal/assistant/badseam/badseam.go:23",
	"seam-any internal/assistant/badseam/badseam.go:24",
	"seam-any internal/assistant/badseam/badseam.go:28",
	"seam-exported-field internal/assistant/badseam/badseam.go:11",
}

// TestDomainModelExamples keeps docs/server/architecture.md "Domain model" and the planted tree together: every Go
// example in the section is a compliant twin, copied verbatim from a planted file whose package has no finding, and
// the section's rule table names every rule.
func TestDomainModelExamples(t *testing.T) {
	doc, err := os.ReadFile(filepath.Join(serverDir, "..", "docs", "server", "architecture.md"))
	if err != nil {
		t.Fatal(err)
	}
	section, ok := strings.CutPrefix(string(doc[strings.Index(string(doc), "\n## Domain model\n")+1:]), "## Domain model\n")
	if !ok {
		t.Fatal(`docs/server/architecture.md has no "## Domain model" section`)
	}
	if end := strings.Index(section, "\n## "); end >= 0 {
		section = section[:end]
	}
	_, findings, _ := domainCase(t, plantedFiles(t), plantedContracts)
	failing := map[string]bool{}
	for _, f := range findings {
		failing[f.Pos.Filename[:strings.LastIndex(f.Pos.Filename, "/")]] = true
	}
	files := plantedFiles(t)
	blocks := strings.Split(section, "```go\n")[1:]
	if len(blocks) == 0 {
		t.Fatal("the Domain model section has no Go example")
	}
	for _, b := range blocks {
		example := b[:strings.Index(b, "```")]
		var found []string
		twin := false
		for rel, text := range files {
			if strings.Contains(text, example) && !strings.HasPrefix(rel, "internal/domainkit/") {
				found = append(found, rel)
				twin = twin || !failing[rel[:strings.LastIndex(rel, "/")]]
			}
		}
		switch {
		case len(found) == 0:
			t.Errorf("this example is in no planted file; copy it from a compliant twin:\n%s", example)
		case !twin:
			slices.Sort(found)
			t.Errorf("this example comes only from %s, whose packages have findings; copy it from a compliant twin:\n%s",
				strings.Join(found, ", "), example)
		}
	}
	for _, id := range domainrules.RuleIDs() {
		if !strings.Contains(section, "| `"+id+"` |") {
			t.Errorf("the Domain model section's rule table has no row for %s", id)
		}
	}
}
