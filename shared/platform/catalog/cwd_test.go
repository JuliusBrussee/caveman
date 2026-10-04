package catalog_test

import (
	"fmt"
	"os"
	"os/exec"
	"strings"
	"testing"

	"github.com/JuliusBrussee/caveman/shared/platform/catalog"
)

const cwdProbeEnv = "CAVEMAN_CATALOG_CWD_PROBE"

// catalogProbe renders the lookups `caveman learn` depends on: context windows,
// prices, and the catalog size.
func catalogProbe() string {
	window, ok := catalog.ContextWindowTokens("anthropic", "claude-opus-5")
	price, version := catalog.Price("anthropic", "claude-opus-5")
	return fmt.Sprintf("rows=%d window=%d/%v price=%+v version=%s", len(catalog.List()), window, ok, price, version)
}

func TestMain(m *testing.M) {
	if os.Getenv(cwdProbeEnv) == "1" {
		fmt.Print(catalogProbe())
		os.Exit(0)
	}
	os.Exit(m.Run())
}

// A binary launched outside the repo once found no catalog file by walking up
// from its working directory, so every window fell back to a provider default
// and every model went unpriced. The catalog is embedded; the working directory
// must not change a single lookup.
func TestCatalogLookupsDoNotDependOnWorkingDirectory(t *testing.T) {
	inside := catalogProbe()
	if strings.Contains(inside, "rows=0") || !strings.Contains(inside, "window=1000000/true") {
		t.Fatalf("catalog not loaded inside the repo: %s", inside)
	}
	cmd := exec.Command(os.Args[0], "-test.run=^$")
	cmd.Dir = t.TempDir()
	cmd.Env = append(os.Environ(), cwdProbeEnv+"=1", "CAVE_CATALOG_PATH=")
	out, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("probe from temp cwd: %v\n%s", err, out)
	}
	if got := string(out); got != inside {
		t.Fatalf("catalog lookups differ by cwd:\n inside repo: %s\n temp cwd:    %s", inside, got)
	}
}
