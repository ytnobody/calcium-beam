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

## D5 — `func!` and `!>` don't yet wrap in `success`/`failure` (**open**, tracked)

The reference compiler wraps a `func!` body's return value in `success(...)`
automatically unless the body already produced a Result (`pkg/compiler/compiler.go`
around the `hasConstraints`/`node.IsEffect` check). `calcium-beam` currently
compiles `func!` identically to `func` - no automatic wrap - so an effect
function's result isn't yet a `{ok, _}` unless its body says so explicitly.
`!>`/`!!!>` (`EffectPipeExpression`) are likewise compiled exactly like `|>`
for now. Not a correctness problem for the programs Stage 1/2 target (none
rely on implicit effect wrapping yet), but must be resolved before Stage 3
claims effect support.

## D6 — stdlib calls are dispatched by `module.function` name, not by resolving `use` (**decided**)

A `use core.io!;` statement has no runtime effect in generated code
(`renderModule` skips `*ast.UseStatement` outright). Instead,
`stdlibCalls` in `pkg/codegen/codegen.go` is a fixed table mapping
`module.function` strings to their `erl/` implementation
(`io.say` → `calcium_io:say`, see `erl/calcium_io.erl`), checked at the call
site. This avoids implementing Calcium's module resolution/search path for
something that, in `calcium-beam`, is always a fixed, finite set of
standard-library modules - there is no third-party `.ca` module loading to
support here, unlike the reference VM. An unmapped `module.function` is a
hard compile error naming both parts, not a silent miscompile.

## D7 — `head`/`tail` don't yet return `Maybe` (**open**, tracked)

The reference implementation's `head(array)` returns `Maybe.Some(value)` /
`Maybe.None` (see `calcium-lang`'s `Null` → `Maybe` breaking change), and
`tail` on an empty array is presumably similarly guarded. `erl/calcium_builtins.erl`'s
`head/1`/`tail/1` currently pattern-match `[H|_]`/`[_|T]` directly and crash
on an empty list instead. Acceptable for Stage 2 fixtures (none call `head`/`tail`
on an empty array), but must be fixed - wrapping in `{ok, 'Some', V}`/`'None'`
per D3's tagged-tuple convention - before claiming general array stdlib support.
