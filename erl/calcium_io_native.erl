%% Erlang/OTP support for Calcium's core.io module (Stage 3 subset).
%% See ../docs/DECISIONS.md D6 for the module.function -> erl/ dispatch
%% convention this file participates in.
-module(calcium_io_native).
-export([say/1, print/1]).

%% io.say/io.println both print a value followed by a newline. Values are
%% rendered with calcium_builtins:to_string, same as a Calcium program
%% calling to_string explicitly - a value isn't printed any differently
%% depending on whether it went through io.say or to_string first.
say(Value) ->
    io:format("~ts~n", [calcium_builtins:to_string(Value)]),
    {ok, 'None'}.

%% io.print prints a value with no trailing newline.
print(Value) ->
    io:format("~ts", [calcium_builtins:to_string(Value)]),
    {ok, 'None'}.
