%% Erlang/OTP support for core.string's native primitives (__str_* in
%% pkg/vm/primitives.go). See ../docs/DECISIONS.md D6 for the dispatch
%% convention. Unlike core.math's primitives (calcium_primitives.erl),
%% which return a raw value or crash, two of these - from_code and
%% from_bytes - have a genuine domain-level success/failure (an invalid
%% code point, an out-of-range byte), so they return {ok, V}/{error, Msg}
%% like the reference implementation; the rest return a raw value, also
%% matching the reference (only __str_from_code/__str_from_bytes wrap in
%% success() there - every other __str_* primitive returns its value
%% directly, Failure() being reserved for a wrong argument count/type that
%% a well-typed caller never triggers).
%%
%% Operates on Unicode code points throughout (string:length/uppercase/
%% lowercase/slice are code-point aware on binaries in modern OTP),
%% matching calcium-lang's rune-based string semantics - except
%% starts_with/ends_with/contains/to_bytes/from_bytes, which are correct at
%% the raw UTF-8 byte level without needing code-point awareness (a byte
%% sequence boundary check), exactly like the reference's byte-based Go
%% implementation of those four.
-module(calcium_str_native).
-export([len/1, split/2, join/2, trim/1, upper/1, lower/1, replace/3,
         substring/2, substring/3, char_at/2, from_code/1, index_of/2,
         starts_with/2, ends_with/2, contains/2, to_bytes/1, from_bytes/1]).

len(S) -> string:length(S).

%% An empty separator splits into individual code points, matching Go's
%% strings.Split(s, "") - which is what calcium-lang's own string.ca's
%% `reverse`/`words` rely on (__str_split(s, "")). Erlang's string:split
%% does not special-case an empty pattern the same way (it returns the
%% whole string unsplit), so this has to be handled explicitly.
split(S, <<>>) -> [<<C/utf8>> || C <- unicode:characters_to_list(S)];
split(S, Sep) -> string:split(S, Sep, all).

join(Arr, Sep) ->
    Strs = lists:map(fun calcium_builtins:to_string/1, Arr),
    iolist_to_binary(lists:join(Sep, Strs)).

trim(S) -> string:trim(S).

upper(S) -> string:uppercase(S).

lower(S) -> string:lowercase(S).

replace(S, Old, New) -> iolist_to_binary(string:replace(S, Old, New, all)).

substring(S, Start) -> substring(S, Start, string:length(S)).

substring(S, Start0, End0) ->
    Len = string:length(S),
    Start1 = clamp_index(Start0, Len),
    End1 = clamp_index(End0, Len),
    if
        Start1 >= End1 -> <<>>;
        true -> string:slice(S, Start1, End1 - Start1)
    end.

%% Mirrors primitiveStrSubstring's negative-index handling exactly: a
%% negative index is adjusted by adding Len *once* (not repeatedly), then
%% clamped into [0, Len].
clamp_index(I, Len) ->
    Adjusted = if I < 0 -> Len + I; true -> I end,
    clamp(Adjusted, Len).

clamp(I, _Len) when I < 0 -> 0;
clamp(I, Len) when I > Len -> Len;
clamp(I, _Len) -> I.

char_at(S, Idx0) ->
    Len = string:length(S),
    Idx = if Idx0 < 0 -> Len + Idx0; true -> Idx0 end,
    if
        Idx < 0 orelse Idx >= Len -> 'None';
        true -> {'Some', string:slice(S, Idx, 1)}
    end.

from_code(Cp) when Cp < 0 orelse Cp > 16#10FFFF ->
    {error, <<"__str_from_code: code point out of range">>};
from_code(Cp) when Cp >= 16#D800, Cp =< 16#DFFF ->
    {error, <<"__str_from_code: code point is a surrogate, use a scalar value">>};
from_code(Cp) ->
    {ok, unicode:characters_to_binary([Cp])}.

index_of(S, Sub) ->
    case string:find(S, Sub) of
        nomatch -> -1;
        Rest -> string:length(S) - string:length(Rest)
    end.

starts_with(S, Prefix) ->
    PLen = byte_size(Prefix),
    case S of
        <<Prefix:PLen/binary, _/binary>> -> true;
        _ -> false
    end.

ends_with(S, Suffix) ->
    SLen = byte_size(Suffix),
    Skip = byte_size(S) - SLen,
    case Skip >= 0 of
        false -> false;
        true ->
            case S of
                <<_:Skip/binary, Suffix:SLen/binary>> -> true;
                _ -> false
            end
    end.

contains(S, Sub) ->
    case binary:match(S, Sub) of
        nomatch -> false;
        _ -> true
    end.

to_bytes(S) -> binary_to_list(S).

from_bytes(Arr) -> from_bytes(Arr, []).

from_bytes([], Acc) -> {ok, list_to_binary(lists:reverse(Acc))};
from_bytes([B | _], _Acc) when not is_integer(B) ->
    {error, <<"__str_from_bytes: every element must be an int">>};
from_bytes([B | _], _Acc) when B < 0; B > 255 ->
    {error, iolist_to_binary(io_lib:format("__str_from_bytes: byte out of range: ~p", [B]))};
from_bytes([B | Rest], Acc) -> from_bytes(Rest, [B | Acc]).
