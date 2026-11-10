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

- ~~Erlang's `#{}` does not preserve insertion order, unlike Calcium's hash
  literal~~ - **fixed, see D17.**
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

## D14 — Plan item 4, phase 1: `async.spawn`/`async.all` (**decided**)

Checked how async actually compiles before assuming the `OpSpawn`/
`OpTimeout`/`OpInterval`/etc. opcodes in `specs/bone-format.md` applied:
they don't, for most of `core.async`. `grep`-ing `pkg/compiler/compiler.go`
for `OpSpawn` turns up nothing - `async.spawn`, `async.expects`,
`async.cancel`, `async.all`, and `async.channel` are all plain functions
registered on the `core.async` module (`pkg/vm/vm.go:445-456`), reached
through ordinary `OpCall`, not dedicated bytecode. Only `async.stay(state)
{ ... }` is special syntax compiled via `OpStayBegin`/`OpStayEnd`/
`OpStayGetState`/`OpContinue`/`OpLeave` (confirmed via
`compileStayExpression`) - the opcode table's other async entries are
vestigial as far as the current compiler is concerned.

This splits Stage 4's work more unevenly than the plan assumed:
`spawn`/`all` (and, not yet done, `channel`/`cancel`/`timeout`/`interval`)
are ordinary `stdlibCalls` dispatches needing no codegen changes - added
`erl/calcium_async.erl`'s `spawn_/1` (spawns a process, result delivered
as `{task_result, Pid, Result}`, `TYPE_TASK`'s opaque handle is just the
`pid()` per D3) and `all_/1` (waits per-task in input order, matching
`pkg/vm/vm.go`'s `builtinAsyncAll`). `async.stay`/`expects` is the real
remaining work (a stateful event-loop DSL - `executeStayBegin`/
`runStayLoop`/`executeContinue`/`executeLeave` in `pkg/vm/vm.go`), and
every corpus example beyond the simplest (`async_all.ca`) needs it -
`async_spawn.ca` itself uses `async.stay`+`expects` to wait on a spawned
task's result, confirming it's not an optional nicety, just something
`async.all` alone can route around. Tracked as its own follow-up, not
assumed to be a smaller or separable piece than it is.

Also fixed, found while verifying `async_all.ca` end-to-end rather than a
feature of async itself: **`+` is polymorphic in Calcium** - if either
operand is a string, it concatenates with the other side auto-stringified
(`pkg/vm/vm.go`'s `executeBinaryOp` checks this before the numeric
branch), not just numeric addition. `"Results: " + to_string(results)`
crashed with `badarith` until `infix()` routed `+` through a new
`calcium_builtins:add/2` runtime dispatch instead of always emitting
Erlang's numeric `+` - which value's operand type isn't always knowable
at compile time, same reasoning as `get`/`index`'s runtime dispatch.

`async_all.ca` now matches end-to-end, added to the differential corpus
(7/9 now).

## D15 — Plan item 4, phase 2: `async.stay`/`expects`/Handler/Task (**decided**)

Implemented per the design in the session's plan (read `pkg/vm/vm.go`'s
`executeStayBegin`/`runStayLoop`/`executeContinue`/`executeLeave`,
`builtinAsyncExpects`/`Continue`/`Leave`/`Cancel`, `builtinScheduleTimeout`/
`Interval`, and `OpGetMember`'s `TYPE_TASK`/`TYPE_HANDLER` cases directly
before designing, not assumed from the opcode table):

- **Handler = `{handler, Ref}`, Task = `{task, Ref}`**, both backed by a
  shared ETS table (`calcium_handlers`/`calcium_tasks` in
  `erl/calcium_async.erl`) holding mutable status/result - Erlang terms
  are immutable, but `.pause()`/`.resume()`/`async.cancel` mutate a
  Handler's status, and a Task's status/result must be queryable
  (`task.status`, `task.result`) without consuming the `{task_result,
  Ref, Result}` message `async.all` also needs.
- **`person.name` (hash field), `handler.status`/`task.result` (property),
  and `handler.ready()` (method)** all share Calcium's `.` syntax, same
  as `get`/`index`/`add` before them - dispatched at runtime via
  `calcium_builtins:prop/2`/`invoke/3`, which fall through to
  `calcium_async` for a Handler/Task tuple and to `maps:get` for a hash.
  `pkg/codegen`'s `call()` only takes this fallback for a method call
  whose object *isn't* a known `core.*` module name
  (`stdlibModuleObjects`, hand-listed rather than derived purely from
  `stdlibCalls`'s current keys) - otherwise `os.exit(...)` with an
  unimplemented function would silently become a runtime `invoke/3`
  crash instead of the clear "not yet mapped" compile error D6
  established.
- **`async.stay(k: v, ...) { body }`** (`*ast.StayExpression` - real
  special syntax, unlike most of `core.async`, see D14) compiles to an
  immediately-invoked fun: build the initial state map, run the body
  statements (whose only job is side-effecting `expects()`/`ready()`
  calls), then call `calcium_async:stay_loop/1`. `executeStayEnd` wraps
  the loop's result in `success(...)` *unconditionally* - not gated on
  an effect-function check like D5's `func!` wrap - so `g.stay` applies
  `{ok, ...}` directly rather than reusing `functionDecl`'s logic.
- **`async.continue`/`async.leave`, called from inside an `expects()`
  callback, signal back via the process dictionary** (`put(stay_signal,
  ...)`before the callback returns, read by `stay_loop` after it
  returns) - the reference achieves the equivalent by mutating
  `vm.currentStay()`, ambient single-VM state with no Erlang analogue;
  the process dictionary is the pragmatic equivalent for state scoped to
  one callback-dispatch call, not promoted to a general pattern elsewhere.

Verified end-to-end against the real examples, not just unit tests:
`async_handler.ca` and `async_cancel.ca` (no `stay` at all - pure Handler
lifecycle) match exactly. `async_timeout.ca` and `async_interval.ca`
(full `stay`+`schedule.timeout`/`interval`+`expects`+`ready`+`leave`)
also match exactly. `async_spawn.ca` matches on every line except one:
right after spawning a trivial `() => 42` task, the reference's `task.status`
reads `"pending"`, this implementation's reads `"completed"` - BEAM's
scheduler runs a freshly spawned lightweight process fast enough that a
trivial task is often done before the parent's very next instruction,
where Go's goroutine scheduling usually hasn't gotten there yet. This is
the example querying a task's status immediately after spawning it
racing the task's own completion - inherently non-deterministic in
*both* runtimes (the reference isn't guaranteed `"pending"` either, just
usually), not a logic bug to chase by artificially slowing `spawn_`
down. `async_handler.ca`/`async_cancel.ca`/`async_timeout.ca`/
`async_interval.ca` are added to the differential corpus;
`async_spawn.ca` is deliberately left out of it for this reason.

### Known limitations (deliberate scope cuts, not oversights - see the plan)

1. One active `stay` loop at a time - `stay_loop` reads every active
   handler in the shared ETS table, not scoped to "this" stay block.
2. No `OpStayGetState` equivalent - a stay-declared state variable isn't
   readable inside the body or a callback (state is still threaded
   correctly through `continue`, just not exposed to identifier lookup
   yet). No current example reads one.
3. A stay loop's exit condition checks only "no active handlers", not
   the reference's "no active handlers *and* no pending tasks".
4. `async.cancel` only handles a Handler, not a Task.
5. `task.done` (an EventSource for `expects`/`stay`), stdin/EOF sources,
   and channel-backed event sources aren't implemented.

## D16 - Stage 5: Secret/capability (decided, design-first per the earlier agreement)

Implemented per the session's own plan, written before any code from
reading `pkg/value/value.go`'s Secret/SecretCapability types and
`pkg/vm/vm.go`'s opcodes/host policy directly - not assumed from the
opcode table. No calcium-lang example or test exercises Secret at all
(checked), so every claim below was additionally verified against the
real reference binary with hand-written fixtures before implementing
the calcium-beam side, not just read off the Go source.

- Secret equality is pointer identity, not a timing-safe comparison
  algorithm. `value.go`'s `SecretVal` allocates a fresh `*Secret` per
  call; `Equals` is a bare Go pointer compare. Confirmed directly:
  `Secret.wrap("hello") == Secret.wrap("hello")` is `false` on the
  reference binary, `w1 == w1` is `true`. There's no payload-comparison
  timing surface to defend against in the first place, because the
  payload is never looked at - `{secret, Ref}` with `Ref1 =:= Ref2`
  reproduces this exactly (an ETS-backed opaque value, same pattern as
  Handler/Task in D15).
- `to_string` renders `<secret>`, never the payload - the one property a
  differential-style check can actually verify directly. Added to
  `calcium_builtins:to_string/1`, and `calcium_io_native:say`/`print`
  already delegate to it (D13), so `io.say`ing a Secret is covered too.
- **`Secret.capability`/`!>`'s Secret check are both special syntax, not
  ordinary calls** - confirmed by reading `pkg/compiler/compiler.go`
  directly: `Secret.capability(sinkFn, scope)`'s first argument is
  compiled as a *derived name string* (`secretSinkName`), never
  evaluated as a value, and the same derivation is applied to `!!!>`'s
  right-hand side so the two match. Replicated with a simplified
  `secretSinkName` in `pkg/codegen` (no local-function scope/index
  disambiguation needed - every sink in this implementation's scope is
  a module-level reference, not a local closure).
- **Verified directly against the reference, and it contradicted my own
  working assumption**: plain `|>` is *never* checked for a Secret
  operand - only `!>`/`!!!>` are (`isSecretExpr` is called exclusively
  from the `EffectPipeExpression` compile path, three call sites, zero
  in `PipeExpression`'s). `s |> noop` runs cleanly on the reference;
  `s !> io.say` is rejected with "Secret cannot be passed to '!>'; use
  the secret-authorized '!!!>' pipeline". Implemented to match exactly -
  `pipe()` itself needed no change; the check lives only in the
  `EffectPipeExpression` case before it delegates to `pipe()`.
- `Secret.with(cap) { body }` pushes/pops the process dictionary's
  `secret_caps` stack via `try ... after ... end`, giving the same
  unwind guarantee the reference gets from truncating `vm.secretCaps` at
  frame return (`vm.go` ~line 491-501) - not something added to
  approximate that guarantee, a structural consequence of `after`.
- `!!!>` (`calcium_secret:check_and_call/2`) checks the capability stack
  for a matching sink name *and* `CALCIUM_SECRET_WRITE_ALLOW`
  (deny-by-default) before calling the sink. Verified end-to-end in all
  three directions: denied with the env var unset, denied with a
  mismatched capability sink name even with the env var set, allowed
  with both a matching capability and the env var set.
- **`Secret.capability`'s "entry module only" check and
  `CALCIUM_SECRET_WRITE_ALLOW`'s module-ID matching both collapse to
  program-level**, since `calciumbeam` has no multi-module linking (D8
  compiles separate single-file programs, never one program importing
  another's compiled code) - every compiled program trivially *is* the
  only module. Documented as a collapsed check, not silently dropped;
  revisit if multi-module linking is ever added. Still true after D19:
  local-module linking inlines every linked `.ca` file's functions into
  the *same single Erlang module* - one compiled program is still
  structurally one module, not several, for this check's purposes.

### A real, net gap relative to the reference (not glossed over)

`Secret.map`'s purity check in the reference is both a compile-time
heuristic (`isEffectExpression`, direct reference to a known `func!`
function - rejects, returns `(false, false)` "can't determine" for a
lambda) *and* a separate runtime check (`vm.isEffectValue(fn)` in
`OpSecretMap`, which inspects a closure's `IsEffect` flag and *does*
catch a lambda wrapping an effect call). This implementation replicates
only the compile-time half (`effectFuncs` tracking + a check in
`secretCall`'s `"map"` case) - compiled Erlang closures are plain funs
with no equivalent flag, so **a lambda passed to `Secret.map` that
itself calls an effect function is not rejected here**, where the
reference's runtime check would catch it. This is specifically narrower
than the reference for that one shape, not a parity claim.

### Other known limitations

- No real trusted sink. `erl/calcium_secret.erl`'s `test_sink/2`
  (dispatched as `test.secret_sink` in `stdlibCalls`) exists only to
  verify the capability-gating machinery end-to-end - it is explicitly
  not real I/O, and `calciumbeam` has no HTTP/process-spawning
  primitives yet to back `http.post_secret`/`os.proc_write_secret`.
- `isSecretExpr`'s syntactic heuristic (ported directly) has the same
  gaps the reference's own version has - not full taint tracking,
  defeated by passing a Secret through an untracked intermediate call in
  both implementations equally.

## D17 — Hash insertion order: `{hash, Pairs}`, not a bare Erlang map (**decided**)

D13 flagged Erlang's `#{}` not preserving insertion order as a known gap
against the reference's Go-map-based Hash. Not a security concern — this is
display/iteration order, unrelated to hash-flood-DoS seed randomization,
which is about the hash *function*, not insertion order.

Fixed by changing the Hash representation from a bare `#{...}` to
`{hash, [{K1, V1}, {K2, V2}, ...]}` — an ordered pair list wrapped in a tag,
so it can't be confused with a tuple-ADT value or a plain array. `hashLiteral`
emits it directly in source order; `calcium_builtins`'s `len`/`to_string`/
`get/2,3`/`keys`/`values`/`index`/`prop` all match on `{hash, Pairs}` and use
`lists:keyfind/3` instead of `maps:find`/`maps:get`. O(n) lookup instead of
Erlang maps' near-O(1) — not a concern at the sizes `calciumbeam`'s current
corpus and stdlib reuse (D8) exercise; revisit if a hash-heavy workload ever
shows up.

Differential corpus: 12/13 (`hash.ca` now matches exactly). Only
`pipeline.ca`'s `core.array` auto-linking gap remains (see D13).

## D18 — `core.array` auto-linking: inline calcium-lang's own array.ca (**decided**)

D13 flagged `core.array`'s non-primitive functions (`reverse`, `sum`,
`take`, `sort`, ...) as unreachable via `array.foo(...)`, since unlike
`core.string`'s functions they're compiled Calcium logic (D8), not
primitive passthroughs, so there's no native `erl/` implementation to
alias them to.

Fixed by auto-linking: when a program's source mentions `array.` at all,
`cmd/calciumbeam` resolves calcium-lang's own `pkg/vm/stdlib/core/array.ca`
(the same `go list -m` resolution `tests/differential` already used, not an
assumed sibling checkout) and passes it to `codegen.Module` via the new
`WithArraySource` option. `stdlibTarget` tries this lazily
(`ensureArrayLinked`) the first time an `array.<fn>` call isn't one of
`stdlibCalls`'s fixed entries: it lexes/parses `array.ca` once and compiles
every top-level function (including private helpers like `_reverse_fn`)
into the same output module, each name prefixed `array_` (a new
`generator.namePrefix` field, applied everywhere a global function name is
emitted or referenced) so they can't collide with the program's own
functions. All of array.ca's functions get compiled and exported
unconditionally once linking triggers — not just the ones actually called —
since `array.ca` is small and this avoids needing dead-code elimination or
a second pass to know which helpers a given function transitively needs.

Differential corpus: 13/13. `continue-on-error` removed from the CI
`differential` job — it's a real gate now.

## D19 — Project-local Calcium modules (`use mymodule;`) (**decided**)

See https://github.com/ytnobody/calcium-beam/issues/1 for the full design and
implementation-plan discussion this implements. Checked against the
reference directly (`specs/module-import-spec.md`, `pkg/compiler/compiler.go`'s
`*ast.UseStatement` case): the reference resolves and loads every module
**dynamically at runtime** (`OpLoadModule`, with its own in-memory/local/
global-cache/remote-fetch resolution order and a `bone` package-manager
story). That's the right model for a bytecode VM with a cache/fetch step
already built in; it's the wrong one to copy onto an AOT Erlang target. Per
D1/D2's precedent (reuse the frontend exactly, adapt the backend to BEAM
rather than mirror VM internals 1:1), this generalizes D18's compile-time
inlining instead.

**Scope: phase 1 only** - project-local modules (`use mymodule;` ->
`./mymodule.ca` or `./mymodule/mod.ca`, resolved relative to the importing
file). Boneyard/GitHub external modules, version constraints, and `pub use
... as` re-exports are explicitly out of scope (tracked as a phase 2 in
issue #1) - none of these exist as a real dependency-fetching story in
`calciumbeam` today.

### What was built

- `cmd/calciumbeam/resolve.go`: finds every local-module `use` statement
  reachable from the entry file (transitively - a linked module may itself
  `use` other local modules), parses each exactly once (dedup by resolved
  absolute path, not import spelling), and detects import cycles.
- `pkg/codegen`'s `LinkedModule`/`WithLocalModules` generalize D18's single
  hardcoded `"array_"`-prefix inlining (`ensureArrayLinked`) to any number
  of resolved modules, each getting its own sanitized-path-derived prefix
  (`generator.namePrefix`, already existing from D18). Linked **eagerly**
  in `Module()`, not lazily like array.ca - the resolver has already paid
  the cost of parsing every file to discover the import graph, so there's
  no "only compile it if used" benefit left to defer.
- A linked module's own `func!` functions propagate into `effectFuncs`
  under their prefixed name, and `Secret.map`'s compile-time purity check
  (D16) was extended to also recognize a module-qualified reference
  (`helper.someEffectFn`), not just a bare identifier - a real gap D18
  never exposed, since `array.ca` declares no effect functions.
- Sub-generators share one `localModules` map, so a linked module that
  itself imports another linked module resolves correctly, relying on the
  resolver's dependency-first ordering (a module's own dependencies are
  always linked - and so already present in the shared map - before it
  is).
- `cmd/calciumbeam/main.go` calls the resolver unconditionally (no
  substring-gate, unlike array.ca's `go list` shell-out) - cheap, since
  it's just a few extra in-process lexer/parser passes over files already
  on disk, returning immediately with nothing to do for a program with no
  local `use` statements.

Verified end-to-end on real BEAM (not just generated-text assertions): a
two-file program (`main.ca` importing `greet.ca`) compiled, `erlc`'d, and
run via both a direct codegen-level test and a full CLI-level test
(`cmd/calciumbeam/e2e_test.go`) covering resolve -> codegen -> erlc -> erl.

### Known limitations (phase 1, stated plainly)

- No Boneyard/GitHub external modules, no version constraints/lock file,
  no `pub`/visibility keyword (the reference doesn't have the latter
  either yet - still "Future Enhancements" in its own spec).
- Still inlines every linked module into one output `.erl` file, not real
  multi-module Erlang/OTP (`other_module:fn(Args)` across separate
  compiled modules) - simplest, and consistent with D8's "no multi-module
  linking" scope at the Erlang-module level specifically (see the D16
  addendum above). Revisit only if/when calciumbeam needs to produce
  something closer to a normal multi-module OTP release.
