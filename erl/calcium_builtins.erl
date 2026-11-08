%% Erlang/OTP support for Calcium's always-available built-in functions
%% (Stage 2/3 subset: only what the current fixtures and reused stdlib
%% source exercise). See ../docs/DECISIONS.md D6 for the dispatch
%% convention.
-module(calcium_builtins).
-export([len/1, head/1, tail/1, to_string/1,
         map/2, filter/2, reduce/3, concat/2, push/2, range/2,
         get/2, get/3]).

len(X) when is_list(X) -> length(X);
len(X) when is_binary(X) -> string:length(X);
len(X) when is_map(X) -> maps:size(X);
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
get(Map, Key) when is_map(Map) ->
    case maps:find(Key, Map) of
        {ok, V} -> {'Some', V};
        error -> 'None'
    end.

get(Arr, Key, Default) when is_list(Arr), is_integer(Key) ->
    if
        Key >= 0 andalso Key < length(Arr) -> lists:nth(Key + 1, Arr);
        true -> Default
    end;
get(Map, Key, Default) when is_map(Map) ->
    maps:get(Key, Map, Default).
