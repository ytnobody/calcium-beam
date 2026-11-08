%% Erlang/OTP support for core.schedule. See docs/DECISIONS.md D14/D15.
-module(calcium_schedule).
-export([timeout_/1, interval_/1]).

%% schedule.timeout(ms) starts counting down immediately at creation
%% (matching the reference's builtinScheduleTimeout, which starts its
%% goroutine right away, not lazily at handler.ready()), targeting
%% self() - correct as long as the caller is the process that will
%% become the stay loop, true in every current example.
timeout_(Ms) ->
    Ref = make_ref(),
    erlang:send_after(Ms, self(), {source_fired, Ref, none}),
    {source, Ref}.

%% schedule.interval(ms) likewise starts immediately and repeats.
interval_(Ms) ->
    Ref = make_ref(),
    {ok, _TimerRef} = timer:apply_interval(Ms, erlang, send,
                                            [self(), {source_fired, Ref, none}]),
    {source, Ref}.
