# Design Decisions

This document records scope and design decisions for `calcium-beam`, in the
same spirit as `calcium-selfhost/docs/DECISIONS.md`: a running log, not a spec.

## D1 — Compile from source via the `calcium-lang` Go frontend, not from `.bone` (**decided**)

`.bone` is a stack-machine bytecode (`specs/bone-format.md`). Reconstructing
`match`/`do`/tail-call structure from jump-threaded bytecode is a real
decompiler and throws away structure (e.g. `OpTailCall` already lost arm
boundaries) that's trivial to keep by starting one step earlier.

`calcium-lang`'s `pkg/lexer`, `pkg/parser`, `pkg/ast`, `pkg/typechecker`,
`pkg/types` have no `internal/` boundary and are a public Go module. `calcium-beam`
imports `calcium-lang` as a library (pinned in `oracle.pin`), parses +
typechecks with the exact same code path the reference compiler uses, and
walks the validated AST to emit Erlang source.

`.bone` stays relevant only as the shape of the *verification* corpus: the
differential harness in `tests/` runs the same `examples/`/`tests/`/`csv/`/`test/`
files from `calcium-lang` through the reference Go VM and through
`calcium-beam`'s generated BEAM programs, comparing observable behavior per
`specs/selfhost-scope.md` §5 — the methodology, not its implementation.

## D2 — Async maps to native BEAM processes, not a reimplemented scheduler (**decided**)

`calcium-selfhost` had to defer its async runtime (D2/D24 there) because
nesting Calcium's `async.*` on top of a *second* host scheduler written in
Calcium, itself running on the Go VM's own goroutine-based scheduler, has a
bad failure mode.

That problem doesn't exist here: BEAM processes are already the host
scheduler. `async.spawn`/`stay`/`expects`, channels, and
`schedule.timeout`/`interval` compile directly to BEAM processes, messages,
and timers — there is no second scheduler to nest.

## D3 — Value representation: mirror `selfhost-scope.md` §3's opaque subset (**decided**)

`TYPE_TASK` → `pid()`, `TYPE_CHANNEL` → a `pid()` fronting a mailbox loop,
`TYPE_EVENT_SOURCE` → a `pid()`/timer ref, `TYPE_SECRET*` → an opaque wrapped
term not inspectable by ordinary pattern matching. Everything else is a
native Erlang term: `success`/`failure` → `{ok, V}`/`{error, E}`, ADTs →
tagged tuples keyed by the **bare** variant name as the tag atom
(`Some(x)` and `Maybe.Some(x)` both construct/match `{'Some', X}`) - a type
qualifier is informational only and never affects the runtime tag, so both
bare and `Type.Variant`-qualified constructors/patterns resolve identically
without needing the type declaration in scope, matching the outcome of
`calcium-lang` PR #185's dual-form matching. Arrays → Erlang lists, tuples →
Erlang tuples, hashes → Erlang maps, strings → UTF-8 binaries.

## D4 — Erlang/OTP local + CI prerequisite (**resolved**)

Installed locally; `pkg/codegen`'s end-to-end test
(`TestBifNameCollisionCompiles`) invokes the real `erlc` when present on
`PATH` and skips otherwise. `.github/workflows/ci.yml` installs OTP via
`erlef/setup-beam`.

## D5 — `func!` unconditionally wraps its body in `{ok, V}` (**decided**)

Verified empirically against the real reference VM (not just read off the
Go source): `OpWrapSuccess` really is unconditional
(`pkg/compiler/compiler.go`'s `if node.IsEffect || hasConstraints { c.emit(bytecode.OpWrapSuccess) }`,
and the opcode itself in `pkg/vm/vm.go` is a plain `value.Success(vm.pop())`,
no idempotency check). A `func!` that wraps a plain `.ca` function call
which already returns `success`/`failure` really does double-wrap:
`func! wrapper(x) = returns_result(x);` prints `success(success(5))`/
`success(failure(negative))`, confirmed against `calciumbeam`'s own `calcium`
build.

The one exception, also confirmed empirically: a `func!` whose body is a
*direct* call to a native primitive or stdlib function (`io.write_file`,
backed by `__write_file`, which itself returns `success(Maybe.None)`/
`failure(err)` in Go) prints exactly one level -
`success(Maybe.None)`, not `success(success(Maybe.None))`. The primitive-
call machinery evidently treats a primitive's own Go-level Success/Failure
specially rather than letting it flow through as an ordinary Calcium value
subject to the same wrap. `functionDecl` in `pkg/codegen/codegen.go`
implements this: wrap in `{ok, V}` unless `fd.Body` is a direct call
dispatched via `builtinCalls`/`stdlibCalls` (D6), via the new
`isDirectPrimitiveOrStdlibCall` helper.

`!>`/`!!!>` (`EffectPipeExpression`) needed no change: the reference
compiler's own comment confirms it ("just call normally - func! already
wraps return in success") - the wrap lives entirely in the callee's
compiled body, not at the pipe call site, and `pipe()` already compiles
both operators identically.

## D6 — stdlib calls are dispatched by `module.function` name, not by resolving `use` (**decided**)

A `use core.io!;` statement has no runtime effect in generated code
(`renderModule` skips `*ast.UseStatement` outright). Instead,
`stdlibCalls` in `pkg/codegen/codegen.go` is a fixed table mapping
`module.function` strings to their `erl/` implementation
(`io.say` → `calcium_io_native:say`, see `erl/calcium_io_native.erl`), checked at the call
site. This avoids implementing Calcium's module resolution/search path for
something that, in `calcium-beam`, is always a fixed, finite set of
standard-library modules - there is no third-party `.ca` module loading to
support here, unlike the reference VM. An unmapped `module.function` is a
hard compile error naming both parts, not a silent miscompile.

## D7 — `head` returns `Maybe`; `tail` returns a raw empty array, not `Maybe` (**decided**)

Checked `pkg/vm/vm.go`'s actual `builtinHead`/`builtinTail` directly rather
than assume they're symmetric: `head([])` returns `'None'`,
`head([H|_])` returns `{'Some', H}` (D3's tagged-tuple convention) - but
`tail([])` returns a plain empty array `[]`, *not* `Maybe.None`, and
`tail([_|T])` returns `T` same as before. `erl/calcium_builtins.erl`
previously pattern-matched `[H|_]`/`[_|T]` directly and crashed on an
empty list instead of matching either reference behavior; fixed with an
explicit `[]` clause for each, returning the correct (and correctly
*different*) shape per function. No stdlib `.ca` file (`array.ca` included)
calls `head` at all, and every `tail` call site already guards the
empty-array case with its own length check before calling it, so this was
a pure bugfix with no observed behavior change for already-working code -
confirmed via `TestHeadTailMaybeSemantics`.

## D8 — Reuse `calcium-lang`'s own stdlib `.ca` source instead of hand-porting (**decided**)

`core.array`'s and `core.math`'s public functions are, with a handful of
exceptions (the ~10 native math primitives, `__floor`/`__sqrt`/...), pure
Calcium source calling only already-implemented built-ins (`map`, `filter`,
`reduce`, `concat`, `range`, `head`, `tail`, `len`). Compiling
`calcium-lang/pkg/vm/stdlib/core/{array,math}.ca` directly through
`calciumbeam` - exactly like user source - gives the full function set for
the cost of the few native primitives, instead of hand-reimplementing ~25
array functions and ~15 math functions one at a time. Verified end-to-end:
`calcium-lang`'s actual `math.ca`, compiled unmodified, runs `abs`/`sign`/
`clamp`/`pow` correctly on BEAM.

`core.string` is expected to work the same way once its ~13 native
`__str_*` primitives exist in `erl/`, since its own public functions are
likewise pure Calcium wrapping those primitives.

## D9 — Generated module names are prefixed to avoid shadowing OTP modules (**decided**)

Compiling `calcium-lang`'s `math.ca` verbatim produces a module literally
named `math`, which shadows Erlang/OTP's own `math` module if both end up
on the same code path (confirmed: loading it made `math:abs/1` resolve to
the generated module instead of the BIF, and `math:sqrt_/1` fail with
`undef` since that function lives in `erl/calcium_primitives.erl`, not the
generated module). The same risk applies to `string`, `lists`, `os`, `io`,
and other common OTP module names a Calcium source file could be named
after. `cmd/calciumbeam/main.go` now derives the Erlang module name as
`calcium_<basename>`, not `<basename>`, closing this for every generated
module - `pkg/codegen.Module` itself still takes an arbitrary name (used
directly by its unit tests) since the hazard is specifically about
file-name-derived module names colliding with real OTP modules.

## D10 — Two correctness bugs found by compiling `array.ca` for real (**fixed**)

Compiling `calcium-lang`'s actual `array.ca` (D8) surfaced two bugs no
hand-written fixture had exercised:

1. **Leading-underscore identifiers aren't valid unquoted Erlang atoms.**
   `array.ca`'s private helpers (`_min`, `_reverse_fn`, `_first_or_neg1`, ...)
   produced `erlc` syntax errors, because an unquoted atom starting with `_`
   lexes as an Erlang *variable*, not a function name. Fixed by having
   `erlAtom` always single-quote its output (`'_min'`), which sidesteps
   every such restriction rather than special-casing leading underscores.
2. **A global function passed as a first-class value needs an explicit fun
   reference.** `reduce(arr, _reverse_fn, [])` passes `_reverse_fn` as a
   value, not a call; `expr()`'s `*ast.Identifier` case previously rendered
   every identifier as a local-variable reference (`V_name`), which is
   wrong for a reference to a top-level function - Erlang needs
   `fun 'name'/Arity` to use a named function as a value. Fixed by tracking
   each top-level function's arity in `generator.globals` (previously just
   a set) and emitting the `fun/Arity` form when an identifier resolves to
   one. The same lookup also closed a related gap in `callTarget`: a
   built-in reached without its own argument list (`filter(arr, pred) |> len`)
   wasn't checked against `builtinCalls`.

Verified end-to-end: `calcium-lang`'s actual `array.ca`, compiled
unmodified, runs `reverse`/`sum`/`sort`/`unique`/`take`/`find`/`zip`/
`chunk`/`count` correctly on BEAM - including `sort`'s recursive quicksort
and higher-order calls through `map`/`filter`/`reduce`.

## D11 — Plan item 1 complete: `core.string` native primitives (**decided**)

Added `erl/calcium_str_native.erl` covering all 15 `__str_*` primitives in
`specs/ffi-primitives.md` §3.3, dispatched via `builtinCalls` (D6), plus
`__int_to_bytes`/`__float_to_bytes` (`erl/calcium_primitives.erl`) for
`core.bytes`. Two things diverged from a direct Go→Erlang port:

- **Module naming collided with the D9 hazard, one level deeper.**
  Compiling `string.ca` produces a module named `calcium_string` -
  *correctly* avoiding the OTP `string` module per D9's `calcium_`
  prefix, but that's exactly the name this plan item's own hand-written
  support module would otherwise have used. Same latent collision for
  `io.ca` → `calcium_io`. Renamed the support modules to
  `calcium_str_native`/`calcium_io_native` (and the erl/ builtins/
  primitives modules are safe only because no stdlib file is literally
  named `builtins.ca`/`primitives.ca` - worth remembering before adding
  another hand-written support module).
- **`__str_split(s, "")` needed a real behavioral fix, not just a
  translation.** `string.ca`'s `reverse` and `words` both rely on
  splitting a string into individual characters via an empty separator -
  which is Go's `strings.Split` special-casing an empty pattern to split
  by rune. Erlang's `string:split/3` does not special-case this the same
  way (it returns the whole string unsplit), so `reverse("hello")` was
  silently a no-op until `split/2` got an explicit `<<>>` clause that
  splits via `unicode:characters_to_list/1` into one-codepoint binaries.

Verified end-to-end (now a permanent test,
`TestCalciumLangStringCaEndToEnd`): `calcium-lang`'s actual `string.ca`,
unmodified, correctly handles multi-byte Unicode (`len`/`char_at`/
`reverse` on `"aアb😀"` all code-point correct, not byte-based) as well as
`split`/`join`/`substring`/`starts_with`/`pad_left`/`words`/etc.

## D12 — `namespace` declarations are a no-op, like `use` and `type` (**decided**)

`core.bytes.ca` opens with `namespace bytes;`, a statement type
(`*ast.NamespaceDeclaration`) not covered by any existing case. It only
affects how *callers* qualify a call (`bytes.to_bytes(...)`); since D6
dispatches `module.function` calls by name regardless of how the module
was declared or imported, there is nothing to emit. Verified end-to-end
(`TestCalciumLangBytesCaEndToEnd`): `calcium-lang`'s actual `bytes.ca`
compiles and its `to_bytes`/`from_bytes`/`byte_len`/`char_count`/
`from_int`/`from_float` all run correctly on BEAM, completing the
`core.{math,array,string,bytes}` stdlib coverage from plan item 1.

## D13 — Plan item 3: differential harness, and what it immediately found (**decided**)

Added `tests/differential/differential_test.go`: for each corpus file, run
it under the pinned reference `calcium` binary (via `go run
github.com/ytnobody/calcium-lang/cmd/calcium`, resolved through the module
dependency - not an assumed sibling checkout) and under `calciumbeam` +
`erlc`/`erl`, and diff stdout. Mirrors `specs/selfhost-scope.md` §5's
methodology (behavior only, pinned reference, never copy the corpus).

Running it against 8 of `calcium-lang`'s own `examples/*.ca` immediately
found and fixed five real gaps no hand-written fixture had hit:

1. **`LambdaExpression` as a bare pipe/call target** (`n |> (r => ...)`,
   used by `factorial.ca`/`fibonacci.ca`/`strings.ca`) wasn't handled by
   `callTarget` - fixed by rendering it as an immediately-invoked fun,
   `(fun(...) -> ... end)(Args)` (the call-site parens are required Erlang
   syntax, not optional).
2. **A bare capitalized identifier in pattern/value position is a
   zero-argument ADT tag** (`None` in `type Maybe = Some(v) | None;`), not
   a binding - `isSimplePattern`/`pattern()`/`expr()`'s `*ast.Identifier`
   cases all treated every identifier as a binding. Fixed by checking
   `isUpper` first in each.
3. **`concat` is variadic in Calcium** (`concat(a, b, c, ...)`) but
   `calcium_builtins:concat/2` is the only arity - Erlang has no variadic
   functions. Fixed by folding 3+ arguments into nested 2-argument calls
   at compile time in `call()` (concatenation is associative, so fold
   order doesn't matter).
4. **`to_string` didn't know Calcium's own display conventions** for ADTs
   (`Tag(v1, v2)` not `{'Tag',v1,v2}`), `success`/`failure`, arrays
   (`[v1, v2]` not Erlang list syntax), or hashes (`{k: v, ...}`) - all
   four now implemented in `calcium_builtins:to_string/1`, and
   `calcium_io_native:say`/`print` now delegate to it instead of
   duplicating a separate, far less complete `to_display/1`.
5. **`erl -noshell`'s `standard_io` defaults to an encoding that can't
   represent most non-ASCII code points** - confirmed directly against
   `erl` (not assumed): `io:format("~ts", [...])` silently prints an
   escaped `\x{2192}` instead of the actual character `→`, while Go's
   stdout has no such restriction. Fixed by emitting
   `io:setopts(standard_io, [{encoding, unicode}])` as `main/0`'s first
   statement in every generated module.

Also added, less dramatically: `keys`/`values` builtins (`maps:keys/values`),
and a polymorphic `calcium_builtins:index/2` for `arr[i]`/`hash[key]` -
Calcium's `[]` syntax covers both and which one applies isn't always known
at the call site in codegen (`index()` previously always emitted
`lists:nth`, breaking on any hash bracket-access), and `string.*` dispatch
entries in `stdlibCalls` pointing straight at the already-implemented
`calcium_str_native`/`calcium_builtins` functions, since `core.string`'s
public functions are 1:1 primitive passthroughs (unlike `core.array`'s,
mostly compiled logic - see D8).

### Two known gaps the harness surfaces and does not yet close

- **Erlang's `#{}` does not preserve insertion order**, unlike Calcium's
  hash literal. `hash.ca` matches except for `keys`/`values`/`to_string`'s
  rendered key order on a multi-key hash. A real fix means revisiting D3's
  hash representation (e.g. an order-preserving association list), which
  has its own tension with arrays also being Erlang lists (a hash-as-
  proplist could structurally collide with an array of 2-tuples) -
  deliberately not rushed; tracked here rather than hacked around.
- **`core.array`'s non-primitive functions aren't reachable via
  `array.foo(...)`** the way `core.string`'s are: `array.ca`'s functions
  are compiled Calcium logic (D8), not primitive passthroughs, so there's
  no native implementation to alias `array.reverse` to the way
  `string.trim` aliases to `calcium_str_native:trim`. Closing this needs
  `calciumbeam` (or at least the differential harness) to auto-compile and
  link `array.ca` as its own module whenever a source file has
  `use core.array;` - a real feature, not a one-line fix, left for a later
  pass rather than hand-porting `array.ca`'s ~20 non-primitive functions
  into `erl/` and abandoning D8 for this one module.

Result: 6/8 of the current corpus matches end-to-end
(`hello.ca`/`factorial.ca`/`fibonacci.ca`/`strings.ca`/`adt_simple.ca`/
`adt.ca`); `hash.ca` and `pipeline.ca` fail for the two reasons above, not
silently - the harness reports per-file pass/fail, not just an aggregate.
Wired into CI as its own job (`.github/workflows/ci.yml`), separate from
the unit-test job, so this honestly-incomplete corpus doesn't block
ordinary development on an unrelated change.
