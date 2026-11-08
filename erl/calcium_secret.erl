%% Erlang/OTP support for Secret/SecretCapability. See docs/DECISIONS.md
%% D16 for what was verified directly against the reference (not assumed
%% from the opcode table) and this implementation's known limitations.
-module(calcium_secret).
-export([wrap/1, map/2, capability/2, test_sink/2, with/2, check_and_call/2]).

%% Secret = {secret, Ref}, payload in a shared ETS table. Equality is
%% `Ref1 =:= Ref2` wherever two Secret values are compared (Erlang's
%% default tuple/term equality on refs - never the payload), matching
%% the reference's pointer-identity equality (pkg/value/value.go:
%% `v.AsSecret() == other.AsSecret()`) - not a timing-safe comparison
%% algorithm, because the payload is never looked at in the first place.
secrets_table() ->
    case ets:info(calcium_secrets) of
        undefined -> ets:new(calcium_secrets, [named_table, public, set]);
        _ -> calcium_secrets
    end.

wrap(V) ->
    secrets_table(),
    Ref = make_ref(),
    ets:insert(calcium_secrets, {Ref, V}),
    {secret, Ref}.

%% Secret.map(secret, fn) applies fn to the unwrapped payload and
%% re-wraps the result. pkg/codegen's compile-time purity check (direct
%% reference to a known func! function - see call()'s Secret.map
%% handling) matches the reference's own static check exactly; the
%% reference's *additional* runtime check (inspecting a closure's
%% IsEffect flag to catch a lambda wrapping an effect call) has no
%% equivalent here - compiled closures are plain Erlang funs with no
%% such flag. See docs/DECISIONS.md D16 limitation #1.
map({secret, Ref}, Fn) ->
    [{Ref, V}] = ets:lookup(calcium_secrets, Ref),
    wrap(Fn(V)).

%% Secret.capability(sink_name, scope) - the sink name is derived from
%% the referenced function's syntactic identity at the call site in
%% pkg/codegen (mirroring the reference's secretSinkName), not evaluated
%% as a value - by the time this runs, SinkName is already a string.
%% The reference's "only the entry module may call this" check collapses
%% to always-true here (no multi-module linking exists yet - D16).
capability(SinkName, Scope) ->
    {secret_capability, SinkName, Scope}.

%% test_sink/2 is NOT a real trusted sink - calciumbeam has no HTTP or
%% process-spawning primitives yet to back one (D16). It exists only to
%% verify the capability-gating machinery (Secret.capability/Secret.with/
%% !!!>/the CALCIUM_SECRET_WRITE_ALLOW gate) end-to-end without pretending
%% real I/O support exists. Takes the *unwrapped* secret value first,
%% then scope - matching pipe()'s "piped value becomes the first
%% argument" convention (`secret !!!> test_sink(scope)` -> `test_sink(secret, scope)`).
test_sink(SecretValue, Scope) ->
    {ok, {test_sink_received, Scope, byte_size(term_to_binary(SecretValue))}}.

%% Secret.with(cap) { body } pushes cap onto the current capability
%% stack (held in the process dictionary - the nearest Erlang
%% equivalent of the reference's vm.secretCaps, ambient single-VM
%% state) and pops it again in an `after` clause, so the stack unwinds
%% correctly even if Body throws - the same guarantee the reference
%% gets from truncating vm.secretCaps at frame return (vm.go ~line
%% 491-501), not something `try`/`after` was added here to approximate.
with(Cap, Body) ->
    Caps0 = case get(secret_caps) of undefined -> []; C -> C end,
    put(secret_caps, [Cap | Caps0]),
    try
        Body()
    after
        put(secret_caps, Caps0)
    end.

%% check_and_call/2 backs `!!!>`: verifies the current capability stack
%% authorizes SinkName (mirroring OpCheckSecretCapability) and the
%% CALCIUM_SECRET_WRITE_ALLOW host policy gate (mirroring
%% isModuleAllowed, collapsed to program-level - D16) before running
%% Call.
check_and_call(SinkName, Call) ->
    Caps = case get(secret_caps) of undefined -> []; C -> C end,
    HasCap = lists:any(fun({secret_capability, SN, _}) -> SN =:= SinkName;
                           (_) -> false
                        end, Caps),
    case HasCap of
        false ->
            error({secret_capability_missing, SinkName});
        true ->
            case os:getenv("CALCIUM_SECRET_WRITE_ALLOW") of
                false -> error({secret_write_denied, SinkName});
                "" -> error({secret_write_denied, SinkName});
                _ -> Call()
            end
    end.
