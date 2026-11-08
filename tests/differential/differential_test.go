// Package differential is the Stage 6 harness (see the project plan and
// specs/selfhost-scope.md §5's methodology in calcium-lang, which this
// mirrors): for each corpus file, run it under the pinned reference
// `calcium` binary and under calciumbeam + erlc/erl, and compare observable
// output. Behavior only, not byte-identical bytecode - exactly
// selfhost-scope.md §5's own rule.
//
// The corpus is intentionally small and will grow as more language
// features land; a file not yet supported is reported as a compile
// failure, not silently skipped, so the "N/M programs match" count stays
// honest.
package differential

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// corpus lists calcium-lang/examples/*.ca files already expected to
// compile and match. Grows alongside calciumbeam's feature coverage -
// see docs/DECISIONS.md for what's supported so far.
var corpus = []string{
	"hello.ca",
	"factorial.ca",
	"fibonacci.ca",
	"hash.ca",
	"strings.ca",
	"adt_simple.ca",
	"adt.ca",
	"pipeline.ca",
	"async_all.ca",
}

func TestCorpus(t *testing.T) {
	erlc, err := exec.LookPath("erlc")
	if err != nil {
		t.Skip("erlc not found, skipping differential corpus")
	}
	erl, err := exec.LookPath("erl")
	if err != nil {
		t.Skip("erl not found, skipping differential corpus")
	}

	calciumLangDir, err := exec.Command("go", "list", "-m", "-f", "{{.Dir}}", "github.com/ytnobody/calcium-lang").Output()
	if err != nil {
		t.Skipf("could not resolve the calcium-lang module: %v", err)
	}
	examplesDir := filepath.Join(strings.TrimSpace(string(calciumLangDir)), "examples")

	erlDir, err := filepath.Abs(filepath.Join("..", "..", "erl"))
	if err != nil {
		t.Fatal(err)
	}

	matched, total := 0, len(corpus)
	for _, name := range corpus {
		t.Run(name, func(t *testing.T) {
			caFile := filepath.Join(examplesDir, name)

			refOut, refErr := runReference(t, caFile)
			if refErr != nil {
				t.Fatalf("reference run failed (should never happen for a corpus file - fix the corpus, not calciumbeam): %v", refErr)
			}

			beamOut, err := runBeam(t, caFile, erlDir, erlc, erl)
			if err != nil {
				t.Fatalf("calciumbeam did not produce matching output: %v\n--- reference ---\n%s\n--- calciumbeam ---\n%s", err, refOut, beamOut)
			}

			if refOut != beamOut {
				t.Fatalf("output mismatch:\n--- reference ---\n%s\n--- calciumbeam ---\n%s", refOut, beamOut)
			}
			matched++
		})
	}
	t.Logf("%d/%d corpus programs matched", matched, total)
}

// runReference runs a .ca file under the pinned reference calcium binary
// (resolved via the module dependency in go.mod/go.sum, not an assumed
// sibling checkout) and returns its stdout.
func runReference(t *testing.T, caFile string) (string, error) {
	t.Helper()
	out, err := exec.Command("go", "run", "github.com/ytnobody/calcium-lang/cmd/calcium", caFile).CombinedOutput()
	if err != nil {
		return string(out), err
	}
	return string(out), nil
}

// runBeam compiles a .ca file with calciumbeam, builds the result against
// every erl/ support module, and runs it on real BEAM, returning stdout.
func runBeam(t *testing.T, caFile, erlDir, erlc, erl string) (string, error) {
	t.Helper()
	dir := t.TempDir()

	moduleName := "calcium_" + strings.TrimSuffix(filepath.Base(caFile), filepath.Ext(caFile))
	compileOut, err := exec.Command("go", "run", "../../cmd/calciumbeam", caFile).CombinedOutput()
	if err != nil {
		return string(compileOut), err
	}
	// calciumbeam writes "<module>.erl" into the current directory - move
	// it into the temp build dir alongside the support modules.
	genErl := moduleName + ".erl"
	if err := os.Rename(genErl, filepath.Join(dir, genErl)); err != nil {
		return "", err
	}

	support, err := filepath.Glob(filepath.Join(erlDir, "*.erl"))
	if err != nil {
		return "", err
	}
	args := append([]string{"-o", dir}, support...)
	args = append(args, filepath.Join(dir, genErl))
	if out, err := exec.Command(erlc, args...).CombinedOutput(); err != nil {
		return string(out), err
	}

	out, err := exec.Command(erl, "-noshell", "-pa", dir, "-eval", moduleName+":main(), halt().").CombinedOutput()
	return string(out), err
}
