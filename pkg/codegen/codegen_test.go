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

// TestEffectFunctionCompilesWithoutWrapping documents the current D5
// limitation: func! compiles exactly like func, with no automatic
// success(...) wrapping.
func TestEffectFunctionCompilesWithoutWrapping(t *testing.T) {
	out := compile(t, "func! identity(x) = x;")
	if !strings.Contains(out, "identity(V_x) ->\n    V_x.") {
		t.Errorf("expected unwrapped body:\n%s", out)
	}
}

func TestADTConstructAndMatch(t *testing.T) {
	out := compile(t, `type Tree = Leaf(value) | Node(left, right);

func depth(t) = match t
    Leaf(v) => 1
    Node(l, r) => 1 + depth(l);

x = Leaf(5);`)
	if !strings.Contains(out, "V_x = {'Leaf', 5}") {
		t.Errorf("expected tagged-tuple construction:\n%s", out)
	}
	if !strings.Contains(out, "{'Leaf', V_v} -> 1") {
		t.Errorf("expected tagged-tuple pattern:\n%s", out)
	}
	if !strings.Contains(out, "{'Node', V_l, V_r} ->") {
		t.Errorf("expected multi-field tagged-tuple pattern:\n%s", out)
	}
	// type Tree itself should not appear in the emitted module.
	if strings.Contains(out, "Tree") {
		t.Errorf("did not expect the type name to leak into output:\n%s", out)
	}
}

func TestSuccessFailureAndMaybe(t *testing.T) {
	out := compile(t, `a = success(1);
b = failure("bad");
c = Maybe.None;
d = Maybe.Some(5);`)
	for _, want := range []string{
		"V_a = {ok, 1}",
		`V_b = {error, <<"bad"/utf8>>}`,
		"V_c = 'None'",
		"V_d = {'Some', 5}",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("expected %q in output:\n%s", want, out)
		}
	}
}

func TestCollectionLiterals(t *testing.T) {
	out := compile(t, `a = [1, 2, 3];
b = (1, "x", true);
c = {name: "calcium"};`)
	if !strings.Contains(out, "V_a = [1, 2, 3]") {
		t.Errorf("expected array literal:\n%s", out)
	}
	if !strings.Contains(out, `V_b = {1, <<"x"/utf8>>, true}`) {
		t.Errorf("expected tuple literal:\n%s", out)
	}
	// The reference parser rewrites a bare-identifier hash key into a
	// string literal (parseHashPair), so the key renders as a binary too.
	if !strings.Contains(out, `V_c = #{<<"name"/utf8>> => <<"calcium"/utf8>>}`) {
		t.Errorf("expected hash/map literal:\n%s", out)
	}
}

func TestPipeAndEffectPipe(t *testing.T) {
	out := compile(t, `func double(n) = n * 2;
x = 5 |> double;
"hi" !> io.say;`)
	if !strings.Contains(out, "double(5)") {
		t.Errorf("expected pipe desugared to a call:\n%s", out)
	}
	if !strings.Contains(out, `calcium_io:say(<<"hi"/utf8>>)`) {
		t.Errorf("expected effect pipe dispatched to calcium_io:say:\n%s", out)
	}
}

func TestDoExpression(t *testing.T) {
	out := compile(t, `x = do
    a = 1;
    a + 1
end;`)
	if !strings.Contains(out, "V_x = (V_a = 1, (V_a + 1))") {
		t.Errorf("unexpected do-expression output:\n%s", out)
	}
}

func TestEffectHandle(t *testing.T) {
	out := compile(t, `func use_value(v) = v;
func handle_error(e) = 0;
result = success(1);
y = result !? {
    success(v) => use_value(v)
    failure(e) => handle_error(e)
};`)
	if !strings.Contains(out, "{ok, V_v} -> use_value(V_v)") {
		t.Errorf("expected success arm:\n%s", out)
	}
	if !strings.Contains(out, "{error, V_e} -> handle_error(V_e)") {
		t.Errorf("expected failure arm:\n%s", out)
	}
}

func TestUnmappedStdlibCallErrors(t *testing.T) {
	l := lexer.New("os.exit(0);")
	p := parser.New(l)
	program := p.ParseProgram()
	if errs := p.Errors(); len(errs) > 0 {
		t.Fatalf("parse errors: %v", errs)
	}
	_, err := Module("test", program)
	if err == nil || !strings.Contains(err.Error(), "os.exit") {
		t.Fatalf("expected an error naming os.exit, got %v", err)
	}
}

// TestStage2EndToEnd compiles an ADT/array/do/pipe fixture and runs it on
// the real BEAM via erlc + erl, alongside the erl/ support modules it calls
// into. Skips if erlc isn't on PATH.
func TestStage2EndToEnd(t *testing.T) {
	erlc, err := exec.LookPath("erlc")
	if err != nil {
		t.Skip("erlc not found, skipping end-to-end BEAM compile check")
	}
	erl, err := exec.LookPath("erl")
	if err != nil {
		t.Skip("erl not found, skipping end-to-end BEAM compile check")
	}

	out := compile(t, `func sum_helper(arr, acc) = match len(arr)
    0 => acc
    _ => sum_helper(tail(arr), acc + head(arr));

func sum(arr) = sum_helper(arr, 0);

to_string(sum([1, 2, 3, 4])) !> io.say;`)

	dir := t.TempDir()
	write := func(name, content string) {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	write("test.erl", out)

	repoRoot, err := filepath.Abs(filepath.Join("..", "..", "erl"))
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(erlc, "-o", dir,
		filepath.Join(repoRoot, "calcium_io.erl"),
		filepath.Join(repoRoot, "calcium_builtins.erl"),
		filepath.Join(dir, "test.erl"),
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("erlc failed: %v\n%s", err, out)
	}

	runCmd := exec.Command(erl, "-noshell", "-pa", dir, "-eval", "test:main(), halt().")
	runOut, err := runCmd.CombinedOutput()
	if err != nil {
		t.Fatalf("erl run failed: %v\n%s", err, runOut)
	}
	if !strings.Contains(string(runOut), "10") {
		t.Errorf("expected output to contain the sum 10, got:\n%s", runOut)
	}
}
