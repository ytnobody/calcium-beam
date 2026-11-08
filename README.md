# calcium-beam

Compiles [Calcium](https://github.com/ytnobody/calcium-lang) source to
Erlang, built for the BEAM virtual machine.

Status: early scaffolding. See `docs/DECISIONS.md` for scope and design
decisions, and `specs/selfhost-scope.md` / `specs/bone-format.md` /
`specs/ffi-primitives.md` in `calcium-lang` for the contracts this project
works against.

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

## Status

Stage 1 (core language: literals, arithmetic, functions, match, tail calls)
is in progress. See `docs/DECISIONS.md` and the plan this project started
from for the full staged scope.
