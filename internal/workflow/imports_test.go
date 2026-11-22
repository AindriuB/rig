package workflow

import (
	"go/build"
	"strings"
	"testing"
)

const module = "github.com/AindriuB/rig/"

// TestImports checks that the package, and everything in this module it
// pulls in, imports neither internal/git nor any store backend: the
// workflow engine knows store types only. Test files may use memstore.
func TestImports(t *testing.T) {
	forbidden := func(path string) bool {
		return path == module+"internal/git" ||
			strings.HasPrefix(path, module+"internal/git/") ||
			strings.HasPrefix(path, module+"internal/store/")
	}
	seen := map[string]bool{}
	var walk func(path, dir string, via []string)
	walk = func(path, dir string, via []string) {
		if seen[path] {
			return
		}
		seen[path] = true
		pkg, err := build.Import(path, dir, 0)
		if err != nil {
			t.Fatalf("import %s: %v", path, err)
		}
		for _, imp := range pkg.Imports {
			if !strings.HasPrefix(imp, module) {
				continue
			}
			chain := append(append([]string(nil), via...), imp)
			if forbidden(imp) {
				t.Errorf("forbidden import: %s", strings.Join(chain, " → "))
				continue
			}
			walk(imp, pkg.Dir, chain)
		}
	}
	walk(".", ".", []string{module + "internal/workflow"})
	if !seen[module+"internal/store"] {
		t.Fatalf("walk did not reach internal/store; saw %v", seen)
	}
}
