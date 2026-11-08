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
tagged tuples keyed by the qualified tag atom (so both bare and
`Type.Variant`-qualified patterns resolve, matching `calcium-lang` PR #185),
arrays/tuples/hashes/strings → list/tuple/map/binary.

## D4 — Erlang/OTP is a hard local + CI prerequisite (**open**, tracked)

No `erlc` in the primary dev environment as of this writing; install is
in progress. CI (`.github/workflows/ci.yml`) will need an Erlang/OTP-enabled
runner image before Stage 1 can be verified end-to-end, not just codegen
unit-tested.
