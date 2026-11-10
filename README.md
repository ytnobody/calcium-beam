# calcium-beam

Compiles [Calcium](https://github.com/ytnobody/calcium-lang) source to
Erlang, built for the BEAM virtual machine.

See `docs/DECISIONS.md` for scope and design decisions, and
`specs/selfhost-scope.md` / `specs/bone-format.md` / `specs/ffi-primitives.md`
in `calcium-lang` for the contracts this project works against.

## Why BEAM

- Proper tail calls are a platform guarantee, matching Calcium's TCO
  semantics directly.
- BEAM processes are the host scheduler, so `async.spawn`/`stay`/`expects`,
  channels, and timers compile to native BEAM primitives instead of a second
  scheduler nested inside a host one.
- `success`/`failure` is Erlang's `{ok, V}`/`{error, E}` convention by
  another name; ADTs are naturally tagged tuples.

## Architecture

`calcium-beam` imports `calcium-lang`'s Go frontend (`pkg/lexer`, `pkg/parser`,
`pkg/ast`, `pkg/typechecker`) as a library, pinned in `oracle.pin`. It parses
and typechecks a `.ca` file with the exact same code path the reference
compiler uses, then walks the validated AST to emit Erlang source (`.erl`),
which `erlc` compiles to real `.beam` files — no second bytecode interpreter
in the loop.

```
cmd/calciumbeam/   CLI entry point
pkg/codegen/       AST -> Erlang source emitter
erl/               Erlang/OTP support library (native-boundary primitives,
                   Secret/capability runtime, async process helpers)
tests/             differential corpus runner against the pinned calcium-lang
docs/DECISIONS.md  scope and design decisions
```

## Usage

```sh
go build -o calciumbeam ./cmd/calciumbeam
./calciumbeam path/to/program.ca          # writes calcium_program.erl

erlc -o <outdir> erl/*.erl calcium_program.erl
erl -noshell -pa <outdir> -eval "calcium_program:main(), halt()."
```

## Status

All five planned stages are implemented:

1. `core.{math,array,string,bytes}` — compiled from `calcium-lang`'s own
   stdlib `.ca` sources unmodified, backed by native Erlang primitives.
2. Core language semantics — `func!` success-wrapping, ADTs, pattern match,
   `head`/`tail` Maybe semantics, pipes.
3. A differential test harness (`tests/differential`) that runs the same
   `.ca` program under the pinned reference `calcium` binary and under
   `calciumbeam` + `erlc`/`erl`, and diffs the output. The full corpus
   (13/13) matches exactly and the CI job is a real gate, not
   `continue-on-error`.
4. `async.spawn`/`all`/`expects`/`stay`/`continue`/`leave`/`cancel` —
   compiled to native BEAM processes and messages, not a reimplemented
   scheduler.
5. `Secret`/`SecretCapability` — capability-gated effects (`!!!>`,
   `Secret.with`), with its own design note and known limitations in
   `docs/DECISIONS.md` (D16).

Each stage's known limitations are stated explicitly in `docs/DECISIONS.md`
rather than left implicit — read it before relying on this for anything
beyond the documented scope.
