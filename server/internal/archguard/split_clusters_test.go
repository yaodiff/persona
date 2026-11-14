package archguard

import (
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
)

// A cluster that moved out of a big package (#7891) stays out. Each row is one moved cluster: the old package's directory,
// the subpackage it went to, and the file-name prefixes the cluster's files had. The old package may not get such a
// file back (test files may stay), and may not declare again a name the subpackage exports. The subpackage's own size is
// not limited, and neither is the old package's: only the moved parts must not flow back. Add a row in every PR that
// moves a cluster; never remove one.
var movedClusters = []struct {
	oldPkg, subPkg string
	filePrefixes   []string
}{
	{"node", "node/clipshare", []string{"clipshare"}},
	{"node", "node/iphone", []string{"iphone"}},
	{"node", "node/nodekit", nil}, // helpers shared by the clusters: no file prefix, names only
	{"assistant", "assistant/chatsearch", []string{"search_text"}},
}

const clusterHint = "put the code in the subpackage instead (docs/server/architecture.md, tracking issue #7891)"

func TestMovedClustersStayOut(t *testing.T) {
	for _, c := range movedClusters {
		oldDir := filepath.FromSlash(c.oldPkg)
		files, err := filepath.Glob(filepath.Join("..", oldDir, "*.go"))
		if err != nil || len(files) == 0 {
			t.Fatalf("no files in %s: %v", c.oldPkg, err)
		}
		declared := map[string]string{} // top-level name of the old package -> file
		for _, f := range files {
			base := filepath.Base(f)
			if strings.HasSuffix(base, "_test.go") {
				continue
			}
			for _, p := range c.filePrefixes {
				if strings.HasPrefix(base, p) {
					t.Errorf("internal/%s/%s is back: its %q cluster lives in internal/%s; %s", c.oldPkg, base, p, c.subPkg, clusterHint)
				}
			}
			for _, n := range topLevelNames(t, f, false) {
				declared[n] = base
			}
		}
		subFiles, err := filepath.Glob(filepath.Join("..", filepath.FromSlash(c.subPkg), "*.go"))
		if err != nil || len(subFiles) == 0 {
			t.Fatalf("no files in %s: %v", c.subPkg, err)
		}
		for _, f := range subFiles {
			if strings.HasSuffix(f, "_test.go") {
				continue
			}
			for _, n := range topLevelNames(t, f, true) {
				if where, ok := declared[n]; ok {
					t.Errorf("internal/%s/%s declares %s again, which internal/%s exports; %s", c.oldPkg, where, n, c.subPkg, clusterHint)
				}
			}
		}
	}
}

// topLevelNames lists a file's package-level types, functions, constants and variables (no methods); only the exported ones when exported is set.
func topLevelNames(t *testing.T, path string, exported bool) []string {
	t.Helper()
	src, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	f, err := parser.ParseFile(token.NewFileSet(), path, src, parser.SkipObjectResolution)
	if err != nil {
		t.Fatal(err)
	}
	var out []string
	add := func(id *ast.Ident) {
		if id.Name != "_" && id.Name != "init" && (!exported || id.IsExported()) && !slices.Contains(out, id.Name) {
			out = append(out, id.Name)
		}
	}
	for _, d := range f.Decls {
		switch d := d.(type) {
		case *ast.FuncDecl:
			if d.Recv == nil {
				add(d.Name)
			}
		case *ast.GenDecl:
			for _, s := range d.Specs {
				switch s := s.(type) {
				case *ast.TypeSpec:
					add(s.Name)
				case *ast.ValueSpec:
					for _, n := range s.Names {
						add(n)
					}
				}
			}
		}
	}
	return out
}
