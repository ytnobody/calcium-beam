package codegen

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ytnobody/calcium-lang/pkg/lexer"
	"github.com/ytnobody/calcium-lang/pkg/parser"
)

func compile(t *testing.T, src string) string {
	t.Helper()
	l := lexer.New(src)
	p := parser.New(l)
	program := p.ParseProgram()
	if errs := p.Errors(); len(errs) > 0 {
		t.Fatalf("parse errors: %v", errs)
	}
	out, err := Module("test", program)
	if err != nil {
		t.Fatalf("codegen: %v", err)
	}
	return out
}

func TestArithmetic(t *testing.T) {
	out := compile(t, "x = 1 + 2 * 3;")
	if !strings.Contains(out, "V_x = (1 + (2 * 3))") {
		t.Errorf("unexpected output:\n%s", out)
	}
}

func TestFunctionDeclaration(t *testing.T) {
	out := compile(t, "func double(n) = n * 2;")
	if !strings.Contains(out, "double(V_n) ->") || !strings.Contains(out, "(V_n * 2)") {
		t.Errorf("unexpected output:\n%s", out)
	}
	if !strings.Contains(out, "double/1") {
		t.Errorf("expected double/1 in exports:\n%s", out)
	}
}

func TestMatchWithGuardAndWildcard(t *testing.T) {
	out := compile(t, `func abs(n) = match n < 0
    true => -n
    _ => n;`)
	if !strings.Contains(out, "case (V_n < 0) of") {
		t.Errorf("expected case expression:\n%s", out)
	}
	if !strings.Contains(out, "true -> (-V_n)") {
		t.Errorf("expected true arm:\n%s", out)
	}
	if !strings.Contains(out, "_ -> V_n") {
		t.Errorf("expected wildcard arm:\n%s", out)
	}
}

func TestMatchWithExplicitGuard(t *testing.T) {
	out := compile(t, `func classify(n) = match n
    x if x > 0 => 1
    _ => 0;`)
	if !strings.Contains(out, "when (V_x > 0) -> 1") {
		t.Errorf("expected guarded arm:\n%s", out)
	}
}

func TestStringLiteral(t *testing.T) {
	out := compile(t, `s = "hello";`)
	if !strings.Contains(out, `<<"hello"/utf8>>`) {
		t.Errorf("unexpected output:\n%s", out)
	}
}

// TestBifNameCollisionCompiles is an end-to-end check with the real erlc:
// a Calcium function named `abs` collides with Erlang's auto-imported
// `erlang:abs/1` BIF, which erlc rejects as an ambiguous call unless
// auto-import is disabled (see the no_auto_import comment in codegen.go).
// Skips if erlc isn't on PATH.
func TestBifNameCollisionCompiles(t *testing.T) {
	erlc, err := exec.LookPath("erlc")
	if err != nil {
		t.Skip("erlc not found, skipping end-to-end BEAM compile check")
	}

	out := compile(t, `func abs(n) = match n < 0
    true => -n
    _ => n;`)

	dir := t.TempDir()
	erlPath := filepath.Join(dir, "test.erl")
	if err := os.WriteFile(erlPath, []byte(out), 0o644); err != nil {
		t.Fatal(err)
	}

	cmd := exec.Command(erlc, "-o", dir, erlPath)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("erlc failed: %v\n%s", err, out)
	}
	if _, err := os.Stat(filepath.Join(dir, "test.beam")); err != nil {
		t.Fatalf("expected test.beam to exist: %v", err)
	}
}

func TestUnsupportedEffectFunctionErrors(t *testing.T) {
	l := lexer.New("func! say(x) = x;")
	p := parser.New(l)
	program := p.ParseProgram()
	if errs := p.Errors(); len(errs) > 0 {
		t.Fatalf("parse errors: %v", errs)
	}
	_, err := Module("test", program)
	if err == nil {
		t.Fatal("expected an error for func! (not yet implemented), got nil")
	}
}
