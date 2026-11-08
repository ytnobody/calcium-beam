%% Erlang/OTP support for core.math's native primitives (__floor, __ceil,
%% etc. in pkg/vm/primitives.go). See ../docs/DECISIONS.md D8: core.math's
%% own public functions are pure Calcium (compiled as ordinary source) and
%% only these ~10 primitives need a native backing.
-module(calcium_primitives).
-export([floor_/1, ceil_/1, round_/1, sqrt_/1, pow_/2,
         sin_/1, cos_/1, tan_/1, log_/1, exp_/1,
         int_to_bytes/1, float_to_bytes/1]).

floor_(N) -> trunc(math:floor(N)).
ceil_(N) -> trunc(math:ceil(N)).
round_(N) -> round(N).
sqrt_(N) -> math:sqrt(N).
pow_(Base, Exp) -> math:pow(Base, Exp).
sin_(N) -> math:sin(N).
cos_(N) -> math:cos(N).
tan_(N) -> math:tan(N).
log_(N) -> math:log(N).
exp_(N) -> math:exp(N).

%% The eight big-endian bytes the .bone format stores integer/float
%% constants as (specs/bone-format.md §2), as an array of ints - matching
%% __int_to_bytes/__float_to_bytes in pkg/vm/primitives.go.
int_to_bytes(N) -> binary_to_list(<<N:64/big-signed-integer>>).

float_to_bytes(F) ->
    binary_to_list(<<F:64/big-float>>).
