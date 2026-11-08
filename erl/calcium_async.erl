%% Erlang/OTP support for core.async's standalone-usable functions
%% (spawn/all - see docs/DECISIONS.md D14). async.stay/expects (the
%% event-loop DSL) are a separate, larger effort tracked in the plan.
-module(calcium_async).
-export([spawn_/1, all_/1]).

%% async.spawn(fn) runs fn() in a new BEAM process and returns a task
%% handle - just the pid (D3's opaque-handle convention for TYPE_TASK).
%% The process sends its result back to the spawning process tagged with
%% its own pid, so multiple in-flight tasks can be told apart.
spawn_(Fn) ->
    Parent = self(),
    spawn(fun() -> Parent ! {task_result, self(), Fn()} end).

%% async.all(tasks) waits for every task and returns their results in the
%% same order as the input list - matching the reference's per-index wait
%% (pkg/vm/vm.go's builtinAsyncAll), not first-completed-first-returned.
all_(Tasks) ->
    [receive {task_result, Pid, Result} -> Result end || Pid <- Tasks].
