%% Erlang/OTP support for Calcium's always-available built-in functions
%% (Stage 2 subset: only what the current fixtures exercise). See
%% ../docs/DECISIONS.md D6 for the dispatch convention and D7 for the
%% Maybe-wrapping this intentionally defers.
-module(calcium_builtins).
-export([len/1, head/1, tail/1, to_string/1]).

len(X) when is_list(X) -> length(X);
len(X) when is_binary(X) -> string:length(X);
len(X) when is_map(X) -> maps:size(X);
len(X) when is_tuple(X) -> tuple_size(X).

%% head/tail return the raw value/crash on an empty array rather than
%% Maybe.Some/Maybe.None, unlike the reference implementation - see
%% docs/DECISIONS.md D7.
head([H | _]) -> H.

tail([_ | T]) -> T.

to_string(X) when is_binary(X) -> X;
to_string(X) when is_integer(X) -> integer_to_binary(X);
to_string(X) when is_float(X) -> float_to_binary(X, [{decimals, 6}, compact]);
to_string(true) -> <<"true">>;
to_string(false) -> <<"false">>;
to_string(X) when is_atom(X) -> atom_to_binary(X, utf8);
to_string(X) -> unicode:characters_to_binary(io_lib:format("~p", [X])).
