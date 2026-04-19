package importtest

import (
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"testing"

	"golang.org/x/tools/go/packages"
)

// allowedImports encodes module-boundary rules. Keys are Go-module roots
// (each matches a go.mod in the repo); values are the project-internal
// module roots that module may import. Standard library and external
// third-party packages are always allowed.
//
// Rules:
//   - app and worker must not import each other.
//   - shared must not import app or worker.
//   - Only the root module (cmd/, test/) may import all three submodules.
var allowedImports = map[string][]string{
	"github.com/Ivantseng123/agentdock/app":    {"github.com/Ivantseng123/agentdock/shared"},
	"github.com/Ivantseng123/agentdock/worker": {"github.com/Ivantseng123/agentdock/shared"},
	"github.com/Ivantseng123/agentdock/shared": {},
	"github.com/Ivantseng123/agentdock": {
		"github.com/Ivantseng123/agentdock/app",
		"github.com/Ivantseng123/agentdock/worker",
		"github.com/Ivantseng123/agentdock/shared",
	},
}

const projectPrefix = "github.com/Ivantseng123/agentdock"

// moduleRoot returns the longest-matching module-root key for a package path.
// Needed because the root module's prefix is a substring of every submodule
// prefix; without longest-match we'd assign "app" packages to root.
func moduleRoot(pkgPath string) string {
	var best string
	for root := range allowedImports {
		if pkgPath == root || strings.HasPrefix(pkgPath, root+"/") {
			if len(root) > len(best) {
				best = root
			}
		}
	}
	return best
}

func TestImportDirection(t *testing.T) {
	_, thisFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	repoRoot := filepath.Dir(filepath.Dir(thisFile))

	modules := []struct {
		name string
		root string
		dir  string
	}{
		{"root", "github.com/Ivantseng123/agentdock", repoRoot},
		{"app", "github.com/Ivantseng123/agentdock/app", filepath.Join(repoRoot, "app")},
		{"worker", "github.com/Ivantseng123/agentdock/worker", filepath.Join(repoRoot, "worker")},
		{"shared", "github.com/Ivantseng123/agentdock/shared", filepath.Join(repoRoot, "shared")},
	}

	for _, m := range modules {
		t.Run(m.name, func(t *testing.T) {
			cfg := &packages.Config{
				Mode: packages.NeedImports | packages.NeedName,
				Dir:  m.dir,
				Tests: false,
			}
			pkgs, err := packages.Load(cfg, "./...")
			if err != nil {
				t.Fatalf("packages.Load(%s): %v", m.dir, err)
			}
			if packages.PrintErrors(pkgs) > 0 {
				t.Fatalf("packages.Load reported errors in %s", m.dir)
			}

			allowed := allowedImports[m.root]
			allowedSet := make(map[string]struct{}, len(allowed))
			for _, a := range allowed {
				allowedSet[a] = struct{}{}
			}

			var violations []string
			for _, pkg := range pkgs {
				pkgModule := moduleRoot(pkg.PkgPath)
				if pkgModule != m.root {
					// packages.Load with Dir=submodule should only return
					// packages in that module, but guard anyway.
					continue
				}
				for importedPath := range pkg.Imports {
					if !strings.HasPrefix(importedPath, projectPrefix) {
						continue
					}
					importedMod := moduleRoot(importedPath)
					if importedMod == m.root {
						continue
					}
					if _, ok := allowedSet[importedMod]; ok {
						continue
					}
					violations = append(violations,
						pkg.PkgPath+" -> "+importedPath+" (disallowed; "+m.name+" may import only "+strings.Join(allowed, ", ")+")")
				}
			}
			sort.Strings(violations)
			for _, v := range violations {
				t.Error(v)
			}
		})
	}
}
