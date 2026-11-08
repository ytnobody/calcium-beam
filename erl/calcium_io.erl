%% Erlang/OTP support for Calcium's core.io module (Stage 3 subset).
%% See ../docs/DECISIONS.md D6 for the module.function -> erl/ dispatch
%% convention this file participates in.
-module(calcium_io).
-export([say/1, print/1]).

%% io.say/io.println both print a value followed by a newline.
say(Value) ->
    io:format("~ts~n", [to_display(Value)]),
    {ok, 'None'}.

%% io.print prints a value with no trailing newline.
print(Value) ->
    io:format("~ts", [to_display(Value)]),
    {ok, 'None'}.

%% to_display renders a Calcium value the way to_string would. Binaries
%% (Calcium strings) print verbatim; everything else uses Erlang's own
%% term formatting, which is a stand-in until Stage 2/3's to_string
%% semantics are ported here.
to_display(Value) when is_binary(Value) ->
    Value;
to_display(Value) ->
    io_lib:format("~p", [Value]).
