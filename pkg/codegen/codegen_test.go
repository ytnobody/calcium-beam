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
	if !strings.Contains(out, "V_x = calcium_builtins:add(1, (2 * 3))") {
		t.Errorf("unexpected output:\n%s", out)
	}
}

func TestFunctionDeclaration(t *testing.T) {
	out := compile(t, "func double(n) = n * 2;")
	if !strings.Contains(out, "'double'(V_n) ->") || !strings.Contains(out, "(V_n * 2)") {
		t.Errorf("unexpected output:\n%s", out)
	}
	if !strings.Contains(out, "'double'/1") {
		t.Errorf("expected double/1 in exports:\n%s", out)
	}
}

func TestMatchWithGuardAndWildcard(t *testing.T) {
	out := compile(t, `func abs(n) = match n < 0
    true => -n
    _ => n;`)
	if !strings.Contains(out, "case V__subject of") || !strings.Contains(out, "(V_n < 0))") {
		t.Errorf("expected case expression over the bound subject:\n%s", out)
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

// TestMatchWithComputedPattern covers the reference compiler's "standard
// case" semantics (pkg/compiler/compiler.go's compileMatch): a pattern that
// isn't a literal/wildcard/constructor is compiled as an expression and
// compared for equality against the subject, so `match true / n < 0 => ...`
// is valid - the `sign` idiom in calcium-lang's core/math.ca.
func TestMatchWithComputedPattern(t *testing.T) {
	out := compile(t, `func sign(n) = match true
    n < 0 => -1
    n > 0 => 1
    true => 0;`)
	if !strings.Contains(out, "_ when ((V_n < 0)) =:= V__subject -> (-1)") {
		t.Errorf("expected equality-guard fallback for the first arm:\n%s", out)
	}
	if !strings.Contains(out, "_ when ((V_n > 0)) =:= V__subject -> 1") {
		t.Errorf("expected equality-guard fallback for the second arm:\n%s", out)
	}
	if !strings.Contains(out, "true -> 0") {
		t.Errorf("expected the literal true arm to still use a direct pattern:\n%s", out)
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

// TestEffectFunctionWrapsResult covers D5 (closed): func! unconditionally
// wraps its body in {ok, V} - confirmed against the real reference VM to
// be genuinely unconditional (a func! wrapping a call that already
// produces success/failure really does double-wrap: success(success(5))).
func TestEffectFunctionWrapsResult(t *testing.T) {
	out := compile(t, "func! identity(x) = x;")
	if !strings.Contains(out, "'identity'(V_x) ->\n    {ok, V_x}.") {
		t.Errorf("expected the body wrapped in {ok, _}:\n%s", out)
	}
}

// TestEffectFunctionWrappingPrimitiveCallIsNotDoubled is the exception to
// the above: a func! whose body is a direct call to a native primitive or
// stdlib function isn't double-wrapped, because that primitive already
// returns its value in the shape the reference VM's primitive-call
// machinery expects (raw on success, a Go-level failure handled specially -
// not an ordinary Calcium success/failure flowing through). Confirmed
// against the reference: io.write_file (func! wrapping __write_file, which
// itself returns success(Maybe.None)/failure(err) in Go) actually prints
// "success(Maybe.None)", not "success(success(Maybe.None))".
func TestEffectFunctionWrappingPrimitiveCallIsNotDoubled(t *testing.T) {
	out := compile(t, "func! identity(x) = len(x);")
	if !strings.Contains(out, "'identity'(V_x) ->\n    calcium_builtins:len(V_x).") {
		t.Errorf("expected the primitive call left unwrapped:\n%s", out)
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
	if !strings.Contains(out, "'double'(5)") {
		t.Errorf("expected pipe desugared to a call:\n%s", out)
	}
	if !strings.Contains(out, `calcium_io_native:say(<<"hi"/utf8>>)`) {
		t.Errorf("expected effect pipe dispatched to calcium_io_native:say:\n%s", out)
	}
}

func TestDoExpression(t *testing.T) {
	out := compile(t, `x = do
    a = 1;
    a + 1
end;`)
	if !strings.Contains(out, "V_x = (V_a = 1, calcium_builtins:add(V_a, 1))") {
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
	if !strings.Contains(out, "{ok, V_v} -> 'use_value'(V_v)") {
		t.Errorf("expected success arm:\n%s", out)
	}
	if !strings.Contains(out, "{error, V_e} -> 'handle_error'(V_e)") {
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

	// Indexes with arr[0] rather than head(arr), matching how array.ca
	// itself avoids head (head returns Maybe.Some/None - see D7 - arr[0]
	// doesn't).
	out := compile(t, `func sum_helper(arr, acc) = match len(arr)
    0 => acc
    _ => sum_helper(tail(arr), acc + arr[0]);

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
		filepath.Join(repoRoot, "calcium_io_native.erl"),
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

// TestLeadingUnderscoreGlobalIsQuoted is a regression test for D10 #1: an
// unquoted Erlang atom starting with `_` lexes as a variable, not a
// function name - exactly what calcium-lang's array.ca's private helpers
// (_min, _reverse_fn, ...) are named.
func TestLeadingUnderscoreGlobalIsQuoted(t *testing.T) {
	out := compile(t, "func _helper(x) = x + 1;")
	if !strings.Contains(out, "'_helper'(V_x) ->") {
		t.Errorf("expected a quoted atom for the leading-underscore function name:\n%s", out)
	}
}

// TestGlobalFunctionPassedAsValue is a regression test for D10 #2: a
// top-level function referenced as a value (not called directly) needs an
// explicit `fun 'name'/Arity` reference in Erlang.
func TestGlobalFunctionPassedAsValue(t *testing.T) {
	out := compile(t, `func inc(x) = x + 1;
func apply_twice(fn, x) = fn(fn(x));
y = apply_twice(inc, 1);`)
	if !strings.Contains(out, "V_y = 'apply_twice'(fun 'inc'/1, 1)") {
		t.Errorf("expected a fun/arity reference for the global passed as a value:\n%s", out)
	}
	// Inside apply_twice's own body, `fn` is a parameter (a local variable
	// holding a fun), so it must be called as a variable, not an atom.
	if !strings.Contains(out, "V_fn(V_fn(V_x))") {
		t.Errorf("expected calls through the local fun-valued parameter:\n%s", out)
	}
}

// TestCalciumLangArrayCaEndToEnd compiles calcium-lang's actual array.ca
// stdlib source (unmodified) and runs several of its ~25 functions on real
// BEAM - see docs/DECISIONS.md D8 (reuse calcium-lang's own stdlib .ca
// source) and D10 (the two bugs this specifically surfaced). Skips if
// erlc/erl aren't on PATH, or if the calcium-lang module isn't resolvable
// (e.g. no network access for `go list` to fetch it).
// compileStdlibFile locates and compiles one of calcium-lang's own stdlib
// .ca files via the imported Go module (not an assumed sibling checkout),
// skipping the test if erlc/erl aren't available or the module can't be
// resolved (e.g. no network access for `go list` to fetch it).
func compileStdlibFile(t *testing.T, relPath, moduleName string, supportFiles ...string) (dir string, erlc, erl string) {
	t.Helper()
	erlcPath, err := exec.LookPath("erlc")
	if err != nil {
		t.Skip("erlc not found, skipping end-to-end BEAM compile check")
	}
	erlPath, err := exec.LookPath("erl")
	if err != nil {
		t.Skip("erl not found, skipping end-to-end BEAM compile check")
	}

	out, err := exec.Command("go", "list", "-m", "-f", "{{.Dir}}", "github.com/ytnobody/calcium-lang").Output()
	if err != nil {
		t.Skipf("could not resolve the calcium-lang module: %v", err)
	}
	src, err := os.ReadFile(filepath.Join(strings.TrimSpace(string(out)), relPath))
	if err != nil {
		t.Skipf("could not read %s: %v", relPath, err)
	}

	l := lexer.New(string(src))
	p := parser.New(l)
	program := p.ParseProgram()
	if errs := p.Errors(); len(errs) > 0 {
		t.Fatalf("parse errors in %s: %v", relPath, errs)
	}
	erlSrc, err := Module(moduleName, program)
	if err != nil {
		t.Fatalf("codegen: %v", err)
	}

	dir = t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, moduleName+".erl"), []byte(erlSrc), 0o644); err != nil {
		t.Fatal(err)
	}

	repoRoot, err := filepath.Abs(filepath.Join("..", "..", "erl"))
	if err != nil {
		t.Fatal(err)
	}
	args := []string{"-o", dir}
	for _, f := range supportFiles {
		args = append(args, filepath.Join(repoRoot, f))
	}
	args = append(args, filepath.Join(dir, moduleName+".erl"))
	if out, err := exec.Command(erlcPath, args...).CombinedOutput(); err != nil {
		t.Fatalf("erlc failed: %v\n%s", err, out)
	}

	return dir, erlcPath, erlPath
}

func TestCalciumLangArrayCaEndToEnd(t *testing.T) {
	dir, _, erl := compileStdlibFile(t, "pkg/vm/stdlib/core/array.ca", "calcium_array", "calcium_builtins.erl")

	runOut, err := exec.Command(erl, "-noshell", "-pa", dir, "-eval",
		`io:format("~p ~p ~p~n", [calcium_array:reverse([1,2,3]), calcium_array:sum([1,2,3,4]), calcium_array:sort([3,1,2])]), halt().`).CombinedOutput()
	if err != nil {
		t.Fatalf("erl run failed: %v\n%s", err, runOut)
	}
	if !strings.Contains(string(runOut), "[3,2,1] 10 [1,2,3]") {
		t.Errorf("unexpected output:\n%s", runOut)
	}
}

func TestCalciumLangMathCaEndToEnd(t *testing.T) {
	dir, _, erl := compileStdlibFile(t, "pkg/vm/stdlib/core/math.ca", "calcium_math", "calcium_primitives.erl")

	runOut, err := exec.Command(erl, "-noshell", "-pa", dir, "-eval",
		`io:format("~p ~p ~p~n", [calcium_math:abs(-5), calcium_math:sign(-3), calcium_math:clamp(15,0,10)]), halt().`).CombinedOutput()
	if err != nil {
		t.Fatalf("erl run failed: %v\n%s", err, runOut)
	}
	if !strings.Contains(string(runOut), "5 -1 10") {
		t.Errorf("unexpected output:\n%s", runOut)
	}
}

func TestCalciumLangStringCaEndToEnd(t *testing.T) {
	dir, _, erl := compileStdlibFile(t, "pkg/vm/stdlib/core/string.ca", "calcium_string", "calcium_str_native.erl", "calcium_builtins.erl")

	runOut, err := exec.Command(erl, "-noshell", "-pa", dir, "-eval",
		`io:format("~p ~p ~p~n", [calcium_string:reverse(<<"hello">>), calcium_string:words(<<"  a b  ">>), calcium_string:pad_left(<<"1">>, 3, <<"0">>)]), halt().`).CombinedOutput()
	if err != nil {
		t.Fatalf("erl run failed: %v\n%s", err, runOut)
	}
	// reverse relies on __str_split(s, "") splitting into individual code
	// points - see docs/DECISIONS.md D11: Erlang's string:split doesn't
	// special-case an empty separator the way Go's strings.Split does.
	if !strings.Contains(string(runOut), `<<"olleh">> [<<"a">>,<<"b">>] <<"001">>`) {
		t.Errorf("unexpected output:\n%s", runOut)
	}
}

func TestCalciumLangBytesCaEndToEnd(t *testing.T) {
	dir, _, erl := compileStdlibFile(t, "pkg/vm/stdlib/core/bytes.ca", "calcium_bytes",
		"calcium_str_native.erl", "calcium_primitives.erl", "calcium_builtins.erl")

	runOut, err := exec.Command(erl, "-noshell", "-pa", dir, "-eval",
		`io:format("~p ~p~n", [calcium_bytes:to_bytes(<<"AB">>), calcium_bytes:from_int(1)]), halt().`).CombinedOutput()
	if err != nil {
		t.Fatalf("erl run failed: %v\n%s", err, runOut)
	}
	// io:format's ~p prints a list of printable ASCII bytes as a quoted
	// string ("AB"), not [65,66] - cosmetic, not a value difference.
	if !strings.Contains(string(runOut), `"AB" [0,0,0,0,0,0,0,1]`) {
		t.Errorf("unexpected output:\n%s", runOut)
	}
}

// TestHeadTailMaybeSemantics covers D7 (closed): head returns
// Maybe.Some(H)/Maybe.None (pkg/vm/vm.go's builtinHead), while tail
// returns a raw empty array on empty input, never Maybe (builtinTail) -
// confirmed these two differ from each other in the reference, not
// assumed to match.
func TestHeadTailMaybeSemantics(t *testing.T) {
	erlc, err := exec.LookPath("erlc")
	if err != nil {
		t.Skip("erlc not found, skipping end-to-end BEAM compile check")
	}
	erl, err := exec.LookPath("erl")
	if err != nil {
		t.Skip("erl not found, skipping end-to-end BEAM compile check")
	}

	dir := t.TempDir()
	repoRoot, err := filepath.Abs(filepath.Join("..", "..", "erl"))
	if err != nil {
		t.Fatal(err)
	}
	if out, err := exec.Command(erlc, "-o", dir, filepath.Join(repoRoot, "calcium_builtins.erl")).CombinedOutput(); err != nil {
		t.Fatalf("erlc failed: %v\n%s", err, out)
	}

	runOut, err := exec.Command(erl, "-noshell", "-pa", dir, "-eval",
		`io:format("~p ~p ~p ~p~n", [calcium_builtins:head([1,2,3]), calcium_builtins:head([]), calcium_builtins:tail([1,2,3]), calcium_builtins:tail([])]), halt().`).CombinedOutput()
	if err != nil {
		t.Fatalf("erl run failed: %v\n%s", err, runOut)
	}
	if !strings.Contains(string(runOut), "{'Some',1} 'None' [2,3] []") {
		t.Errorf("unexpected output:\n%s", runOut)
	}
}

// --- Secret/capability (D16) ---

func TestSecretWrapAndMap(t *testing.T) {
	out := compile(t, `s = Secret.wrap("x");
func id(v) = v;
r = Secret.map(s, id);`)
	if !strings.Contains(out, `V_s = calcium_secret:wrap(<<"x"/utf8>>)`) {
		t.Errorf("expected Secret.wrap dispatch:\n%s", out)
	}
	if !strings.Contains(out, "V_r = calcium_secret:map(V_s, fun 'id'/1)") {
		t.Errorf("expected Secret.map dispatch:\n%s", out)
	}
}

func TestSecretMapRejectsEffectFunctionAtCompileTime(t *testing.T) {
	l := lexer.New(`func! leaky(x) = x;
s = Secret.wrap("x");
r = Secret.map(s, leaky);`)
	p := parser.New(l)
	program := p.ParseProgram()
	if errs := p.Errors(); len(errs) > 0 {
		t.Fatalf("parse errors: %v", errs)
	}
	_, err := Module("test", program)
	if err == nil || !strings.Contains(err.Error(), "leaky") {
		t.Fatalf("expected a compile error naming the effect function, got %v", err)
	}
}

func TestSecretCapability(t *testing.T) {
	out := compile(t, `cap = Secret.capability(test.secret_sink, {scope: "x"});`)
	if !strings.Contains(out, `V_cap = calcium_secret:capability(<<"test.secret_sink"/utf8>>, #{<<"scope"/utf8>> => <<"x"/utf8>>})`) {
		t.Errorf("expected Secret.capability dispatch with the derived sink name:\n%s", out)
	}
}

// TestPlainPipeAllowsSecret documents a finding from verifying against
// the real reference binary (D16): plain `|>` is never checked for a
// Secret operand - only `!>`/`!!!>` are, since a pure function has no
// effect boundary to leak through. Confirmed empirically before
// implementing, not assumed from the opcode table.
func TestPlainPipeAllowsSecret(t *testing.T) {
	out := compile(t, `s = Secret.wrap("x");
func noop(v) = v;
r = s |> noop;`)
	if !strings.Contains(out, "V_r = 'noop'(V_s)") {
		t.Errorf("expected plain pipe to compile without rejecting the Secret:\n%s", out)
	}
}

// TestEffectPipeRejectsSecretWithoutAuthorization is a regression test
// for the one case that *is* rejected: `!>` (not `!!!>`) with a
// Secret-shaped left side - confirmed against the reference to produce
// exactly this error message.
func TestEffectPipeRejectsSecretWithoutAuthorization(t *testing.T) {
	l := lexer.New(`use core.io!;
s = Secret.wrap("x");
s !> io.say;`)
	p := parser.New(l)
	program := p.ParseProgram()
	if errs := p.Errors(); len(errs) > 0 {
		t.Fatalf("parse errors: %v", errs)
	}
	_, err := Module("test", program)
	if err == nil || !strings.Contains(err.Error(), "!!!>") {
		t.Fatalf("expected a compile error pointing at '!!!>', got %v", err)
	}
}

// TestSecretEndToEnd compiles and runs the full Secret/capability
// pipeline on real BEAM: wrap, equality (never comparing payloads),
// to_string redaction, Secret.capability/.with/!!!>, and the
// CALCIUM_SECRET_WRITE_ALLOW gate (both denied and allowed) - mirroring
// each fixture verified against the real reference binary in D16's
// design and this stage's implementation. test.secret_sink is a
// test-only placeholder (calciumbeam has no real HTTP/process
// primitives to back a trusted sink yet).
func TestSecretEndToEnd(t *testing.T) {
	erlc, err := exec.LookPath("erlc")
	if err != nil {
		t.Skip("erlc not found, skipping end-to-end BEAM compile check")
	}
	erl, err := exec.LookPath("erl")
	if err != nil {
		t.Skip("erl not found, skipping end-to-end BEAM compile check")
	}

	out := compile(t, `use core.io!;
s = Secret.wrap("hello");
to_string(s) !> io.say;
w1 = Secret.wrap("hello");
w2 = Secret.wrap("hello");
to_string(w1 == w2) !> io.say;
to_string(w1 == w1) !> io.say;
cap = Secret.capability(test.secret_sink, {scope: "x"});
result = Secret.with(cap) {
    s !!!> test.secret_sink({scope: "x"})
};
to_string(result) !> io.say;`)

	dir := t.TempDir()
	erlPath := filepath.Join(dir, "test.erl")
	if err := os.WriteFile(erlPath, []byte(out), 0o644); err != nil {
		t.Fatal(err)
	}
	repoRoot, err := filepath.Abs(filepath.Join("..", "..", "erl"))
	if err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(erlc, "-o", dir,
		filepath.Join(repoRoot, "calcium_secret.erl"),
		filepath.Join(repoRoot, "calcium_io_native.erl"),
		filepath.Join(repoRoot, "calcium_builtins.erl"),
		erlPath,
	)
	if out, err := cmd.CombinedOutput(); err != nil {
		t.Fatalf("erlc failed: %v\n%s", err, out)
	}

	// Denied: CALCIUM_SECRET_WRITE_ALLOW unset.
	deniedCmd := exec.Command(erl, "-noshell", "-pa", dir, "-eval", "test:main(), halt().")
	deniedCmd.Env = append(os.Environ(), "CALCIUM_SECRET_WRITE_ALLOW=")
	deniedOut, _ := deniedCmd.CombinedOutput()
	if !strings.Contains(string(deniedOut), "secret_write_denied") {
		t.Errorf("expected denial without CALCIUM_SECRET_WRITE_ALLOW set:\n%s", deniedOut)
	}

	// Allowed: CALCIUM_SECRET_WRITE_ALLOW set.
	allowedCmd := exec.Command(erl, "-noshell", "-pa", dir, "-eval", "test:main(), halt().")
	allowedCmd.Env = append(os.Environ(), "CALCIUM_SECRET_WRITE_ALLOW=entry")
	allowedOut, err := allowedCmd.CombinedOutput()
	if err != nil {
		t.Fatalf("erl run failed: %v\n%s", err, allowedOut)
	}
	got := string(allowedOut)
	if !strings.Contains(got, "<secret>\nfalse\ntrue\n") {
		t.Errorf("expected redacted display and reference-identity equality:\n%s", got)
	}
	if !strings.Contains(got, "success(") || !strings.Contains(got, "test_sink_received") {
		t.Errorf("expected the trusted sink to run once authorized:\n%s", got)
	}
}
