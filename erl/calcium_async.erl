%% Erlang/OTP support for core.async. See docs/DECISIONS.md D14/D15 for
%% the architecture notes (most of core.async is plain function/property
%% dispatch, not dedicated bytecode - only async.stay is special syntax)
%% and the deliberate limitations of this implementation (one active
%% stay loop at a time, no stay-state variable access inside callbacks,
%% task-list ignored in the loop's exit condition, async.cancel only
%% handles a Handler).
-module(calcium_async).
-export([spawn_/1, all_/1, expects/2, prop/2, invoke/3, continue/1,
         leave/1, cancel/1, stay_loop/1]).

%% Task = {task, Ref}, with {Ref, Status, Result} in a shared ETS table -
%% a bare pid can't be queried for "has this completed yet" without
%% consuming its mailbox message, which .status/.result need to do
%% without disturbing async.all's own receive.
tasks_table() -> ensure_table(calcium_tasks).
handlers_table() -> ensure_table(calcium_handlers).

ensure_table(Name) ->
    case ets:info(Name) of
        undefined -> ets:new(Name, [named_table, public, set]);
        _ -> Name
    end.

%% async.spawn(fn) runs fn() in a new BEAM process and returns a task
%% handle. The result is delivered both via ETS (for .status/.result)
%% and via a tagged message to the parent (for async.all, keyed by Ref
%% rather than the worker's pid so both can coexist).
spawn_(Fn) ->
    tasks_table(),
    Ref = make_ref(),
    ets:insert(calcium_tasks, {Ref, pending, undefined}),
    Parent = self(),
    spawn(fun() ->
        Result = Fn(),
        ets:insert(calcium_tasks, {Ref, completed, Result}),
        Parent ! {task_result, Ref, Result}
    end),
    {task, Ref}.

%% async.all(tasks) waits for every task and returns their results in the
%% same order as the input list - matching the reference's per-index wait
%% (pkg/vm/vm.go's builtinAsyncAll), not first-completed-first-returned.
all_(Tasks) ->
    [receive {task_result, R, Result} -> Result end || {task, R} <- Tasks].

%% async.expects(source, callback) or async.expects(callback, source) -
%% argument order is flexible in the reference (builtinAsyncExpects swaps
%% if the first argument is a function), so accept both here too rather
%% than require callers to get the order right.
expects(A, B) ->
    handlers_table(),
    {Source, Callback} = case is_function(A) of
        true -> {B, A};
        false -> {A, B}
    end,
    Ref = make_ref(),
    ets:insert(calcium_handlers, {Ref, dormant, Source, Callback}),
    {handler, Ref}.

%% prop/2 is the runtime dispatcher behind bare `.` access
%% (pkg/codegen's stdlibValue fallback) - person.name (hash field),
%% handler.status, task.status/.result/.done all share Calcium's `.`
%% syntax, and which one applies isn't known at the call site.
prop({handler, Ref}, <<"status">>) ->
    [{Ref, Status, _, _}] = ets:lookup(calcium_handlers, Ref),
    atom_to_binary(Status, utf8);
prop({task, Ref}, <<"status">>) ->
    [{Ref, Status, _}] = ets:lookup(calcium_tasks, Ref),
    atom_to_binary(Status, utf8);
prop({task, Ref}, <<"result">>) ->
    [{Ref, _, Result}] = ets:lookup(calcium_tasks, Ref),
    Result;
%% task.done (an EventSource for use with async.expects/stay) is not
%% implemented - no current example needs it, and getting its message
%% shape right (task_result vs the source_fired shape stay_loop expects)
%% needs its own verification rather than a guess. See docs/DECISIONS.md D15.
prop(Map, Name) when is_map(Map) ->
    maps:get(Name, Map).

%% invoke/3 is prop/2's counterpart for a method *call*
%% (`handler.ready()`), dispatched the same way.
invoke({handler, Ref}, <<"ready">>, []) ->
    ets:update_element(calcium_handlers, Ref, {2, active}),
    {handler, Ref};
invoke({handler, Ref}, <<"pause">>, []) ->
    ets:update_element(calcium_handlers, Ref, {2, paused}),
    {handler, Ref};
invoke({handler, Ref}, <<"resume">>, []) ->
    [{Ref, Status, _, _}] = ets:lookup(calcium_handlers, Ref),
    case Status of
        paused -> ets:update_element(calcium_handlers, Ref, {2, active});
        _ -> ok
    end,
    {handler, Ref};
invoke({handler, Ref}, <<"reset">>, []) ->
    ets:update_element(calcium_handlers, Ref, {2, dormant}),
    {handler, Ref}.

%% async.continue(updates)/async.leave(value), called from inside an
%% expects() callback. The reference mutates vm.currentStay() - ambient,
%% single-VM state reachable because everything runs on one thread; the
%% nearest Erlang equivalent for "state visible to the caller of this
%% callback without an explicit parameter" is the process dictionary,
%% legitimate here because it's transient and scoped to one
%% stay_loop call's callback dispatch, not business state. stay_loop
%% reads it back after the callback returns.
continue(Updates) ->
    put(stay_signal, {continue, Updates}),
    'None'.

leave(V) ->
    put(stay_signal, {leave, V}),
    V.

%% async.cancel(handler) - only a Handler, not a Task (no current example
%% cancels a task; see docs/DECISIONS.md D15).
cancel({handler, Ref}) ->
    ets:update_element(calcium_handlers, Ref, {2, cancelled}),
    'None'.

%% stay_loop(State) is async.stay's event loop. Reads *every* active
%% handler, not just ones belonging to "this" stay - correct as long as
%% only one stay runs at a time (true of every current example; see
%% D15's known limitations for what a nested/concurrent fix would need).
stay_loop(State) ->
    handlers_table(),
    Active = [H || {_, active, _, _} = H <- ets:tab2list(calcium_handlers)],
    case Active of
        [] ->
            'None';
        _ ->
            receive
                {source_fired, SourceRef, Value} ->
                    case find_handler(SourceRef, Active) of
                        {Ref, Callback} ->
                            erase(stay_signal),
                            Callback(Value),
                            case get(stay_signal) of
                                {leave, LeaveValue} -> LeaveValue;
                                {continue, Updates} -> stay_loop(maps:merge(State, Updates));
                                undefined -> stay_loop(State)
                            end;
                        not_found ->
                            stay_loop(State)
                    end;
                {task_result, _, _} ->
                    % A spawned task's result, not a schedule.timeout/
                    % interval event - no active handler watches it
                    % directly in the examples this covers, so just keep
                    % looping (task-list-aware exit is scoped out, D15).
                    stay_loop(State)
            end
    end.

find_handler(SourceRef, Active) ->
    case [{{Ref}, Callback} || {Ref, _, {source, S}, Callback} <- Active, S =:= SourceRef] of
        [{{Ref}, Callback} | _] -> {Ref, Callback};
        [] -> not_found
    end.
