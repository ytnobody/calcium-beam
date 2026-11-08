%% Erlang/OTP support for Calcium's always-available built-in functions
%% (Stage 2/3 subset: only what the current fixtures and reused stdlib
%% source exercise). See ../docs/DECISIONS.md D6 for the dispatch
%% convention.
-module(calcium_builtins).
-export([len/1, head/1, tail/1, to_string/1,
         map/2, filter/2, reduce/3, concat/2, push/2, range/2,
         get/2, get/3, keys/1, values/1, index/2, add/2, prop/2, invoke/3]).

len({hash, Pairs}) -> length(Pairs);
len(X) when is_list(X) -> length(X);
len(X) when is_binary(X) -> string:length(X);
len(X) when is_tuple(X) -> tuple_size(X).

%% head returns Maybe.Some(H)/Maybe.None (pkg/vm/vm.go's builtinHead);
%% tail returns a raw empty array on an empty input, never Maybe
%% (builtinTail) - these two differ from each other, not a typo.
head([]) -> 'None';
head([H | _]) -> {'Some', H}.

tail([]) -> [];
tail([_ | T]) -> T.

to_string(X) when is_binary(X) -> X;
to_string(X) when is_integer(X) -> integer_to_binary(X);
to_string(X) when is_float(X) -> float_to_binary(X, [{decimals, 6}, compact]);
to_string(true) -> <<"true">>;
to_string(false) -> <<"false">>;
to_string({ok, V}) -> <<"success(", (to_string(V))/binary, ")">>;
to_string({error, V}) -> <<"failure(", (to_string(V))/binary, ")">>;
%% Never render a Secret's payload - matching pkg/value/value.go's
%% String() for TYPE_SECRET exactly (always "<secret>", regardless of
%% what's wrapped). This is the one property a differential check can
%% actually verify directly (see docs/DECISIONS.md D16).
to_string({secret, _}) -> <<"<secret>">>;
to_string({secret_capability, _, _}) -> <<"<secret capability>">>;
to_string(X) when is_list(X) ->
    Strs = lists:map(fun to_string/1, X),
    <<"[", (iolist_to_binary(lists:join(<<", ">>, Strs)))/binary, "]">>;
%% {hash, Pairs} is an ordered pair list (see hashLiteral in pkg/codegen),
%% not an Erlang map - #{} does not preserve insertion order, unlike the
%% reference's Go-map-based Hash. Must come before the generic ADT-tuple
%% clause below, since {hash, [...]} would otherwise match it.
to_string({hash, Pairs}) ->
    Strs = [<<(to_string(K))/binary, ": ", (to_string(V))/binary>> || {K, V} <- Pairs],
    <<"{", (iolist_to_binary(lists:join(<<", ">>, Strs)))/binary, "}">>;
%% An ADT value (D3's tagged-tuple convention): render as Calcium's own
%% `Tag(v1, v2, ...)` display, matching the reference's Value.String(),
%% not Erlang's {'Tag',v1,v2} term syntax.
to_string(X) when is_tuple(X), is_atom(element(1, X)) ->
    [Tag | Fields] = tuple_to_list(X),
    Strs = lists:map(fun to_string/1, Fields),
    <<(atom_to_binary(Tag, utf8))/binary, "(", (iolist_to_binary(lists:join(<<", ">>, Strs)))/binary, ")">>;
to_string(X) when is_atom(X) -> atom_to_binary(X, utf8);
to_string(X) -> unicode:characters_to_binary(io_lib:format("~p", [X])).

map(Arr, Fun) -> lists:map(Fun, Arr).

filter(Arr, Pred) -> lists:filter(Pred, Arr).

%% Calcium's reduce(arr, fn, init) calls fn(acc, x) - accumulator first,
%% unlike lists:foldl's Fun(Elem, AccIn), so the argument order is flipped
%% here rather than asking every ported stdlib function to know about it.
reduce(Arr, Fun, Init) ->
    lists:foldl(fun(X, Acc) -> Fun(Acc, X) end, Init, Arr).

%% concat is polymorphic over arrays and strings in Calcium (array.ca uses
%% it for both). Erlang keeps these as distinct types, so dispatch on the
%% first argument's type.
concat(A, B) when is_list(A) -> A ++ B;
concat(A, B) when is_binary(A) -> <<A/binary, B/binary>>.

%% push appends at the end (O(n)), matching the reference implementation's
%% immutable-cons-list cost noted in calcium-lang's selfhost-scope.md.
push(Arr, X) -> Arr ++ [X].

%% range(start, end) - end-exclusive, matching every array.ca caller
%% (range(0, len(arr)), range(start, end), ...).
range(Start, End) when Start >= End -> [];
range(Start, End) -> lists:seq(Start, End - 1).

%% get/2 returns Maybe.Some(V)/Maybe.None; get/3 returns a raw default.
%% Dispatches on collection type: a list is index-addressed (int key), a
%% map is key-addressed.
get(Arr, Key) when is_list(Arr), is_integer(Key) ->
    if
        Key >= 0 andalso Key < length(Arr) -> {'Some', lists:nth(Key + 1, Arr)};
        true -> 'None'
    end;
get({hash, Pairs}, Key) ->
    case lists:keyfind(Key, 1, Pairs) of
        {_, V} -> {'Some', V};
        false -> 'None'
    end.

get(Arr, Key, Default) when is_list(Arr), is_integer(Key) ->
    if
        Key >= 0 andalso Key < length(Arr) -> lists:nth(Key + 1, Arr);
        true -> Default
    end;
get({hash, Pairs}, Key, Default) ->
    case lists:keyfind(Key, 1, Pairs) of
        {_, V} -> V;
        false -> Default
    end.

%% {hash, Pairs} preserves insertion order, matching the reference's
%% Go-map-based Hash exactly - see hashLiteral in pkg/codegen.
keys({hash, Pairs}) -> [K || {K, _} <- Pairs].
values({hash, Pairs}) -> [V || {_, V} <- Pairs].

%% `arr[i]`/`hash[key]` share Calcium's `[]` syntax but are different
%% Erlang operations - which one applies isn't known at the call site in
%% codegen, so dispatch here instead (same reasoning as get/2,3).
index(Arr, I) when is_list(Arr), is_integer(I) -> lists:nth(I + 1, Arr);
index({hash, Pairs}, Key) -> {_, V} = lists:keyfind(Key, 1, Pairs), V.

%% `+` is polymorphic in Calcium: if either operand is a string, it's
%% concatenation with the other side auto-stringified, not just numeric
%% addition (pkg/vm/vm.go's executeBinaryOp checks this before the
%% numeric branch).
add(A, B) when is_binary(A); is_binary(B) ->
    <<(to_string(A))/binary, (to_string(B))/binary>>;
add(A, B) -> A + B.

%% `.` access (`person.name`, `handler.status`, `task.result`) shares
%% one syntax in Calcium for several different runtime operations; which
%% one applies isn't known at the call site in pkg/codegen, so dispatch
%% here on the object's actual shape - a hash falls through to its pair
%% list, anything else (a Handler/Task tuple so far) delegates to
%% calcium_async.
prop({hash, Pairs}, Name) -> {_, V} = lists:keyfind(Name, 1, Pairs), V;
prop(Obj, Name) -> calcium_async:prop(Obj, Name).

%% Method-call counterpart (`handler.ready()`) - same dispatch idea, for
%% a call rather than a bare value access.
invoke(Obj, Name, Args) -> calcium_async:invoke(Obj, Name, Args).
