// Package codegen compiles a typechecked Calcium AST (from calcium-lang's
// own Go frontend) into Erlang source text.
//
// Stage 1 scope (see ../../docs/DECISIONS.md D1): literals, arithmetic /
// comparison / logical infix operators, named (non-effect) functions,
// match expressions with literal/wildcard/binding patterns and guards,
// and function calls. ADTs, effects, async, and Secret are later stages.
package codegen

import (
	"fmt"
	"strconv"
	"strings"

	"github.com/ytnobody/calcium-lang/pkg/ast"
)

// Module renders a Calcium program as a single Erlang module named
// moduleName. Top-level function declarations become Erlang functions;
// every other top-level statement is collected into a generated main/0.
func Module(moduleName string, program *ast.Program) (string, error) {
	g := &generator{module: moduleName, globals: map[string]int{}}
	for _, stmt := range program.Statements {
		if fd, ok := stmt.(*ast.FunctionDeclaration); ok {
			g.globals[fd.Name.Value] = len(fd.Parameters)
		}
	}
	return g.renderModule(program)
}

type generator struct {
	module string
	// globals maps every top-level function name to its arity, collected
	// before any body is compiled (functions may call each other regardless
	// of declaration order, including recursively). Used two ways:
	//   - callTarget: tell a call to a named Calcium function (-> an Erlang
	//     atom call) apart from a call through a local variable/parameter
	//     that happens to hold a fun (-> an Erlang variable call) - `fn(x)`
	//     and `V_fn(x)` are different things in Erlang, unlike in Calcium.
	//   - expr's Identifier case: tell a global function referenced as a
	//     plain value (e.g. `reduce(arr, helper, [])`) apart from a local
	//     variable - the former needs an explicit `fun 'helper'/Arity`
	//     reference, which requires knowing the arity.
	globals map[string]int
}

func (g *generator) renderModule(program *ast.Program) (string, error) {
	var funcs []string
	var mainStmts []string
	exports := []string{"main/0"}

	for _, stmt := range program.Statements {
		switch s := stmt.(type) {
		case *ast.FunctionDeclaration:
			src, err := g.functionDecl(s)
			if err != nil {
				return "", err
			}
			funcs = append(funcs, src)
			exports = append(exports, fmt.Sprintf("%s/%d", erlAtom(s.Name.Value), len(s.Parameters)))
		case *ast.UseStatement:
			// Module imports have no runtime effect in generated code: stdlib
			// calls (io.say, ...) are dispatched by name in call(), regardless
			// of whether/how they were imported. See docs/DECISIONS.md D5.
			continue
		case *ast.TypeDeclaration:
			// ADT variants need no declaration in Erlang - a constructor call
			// or pattern is just a tagged tuple, built/matched by name. See
			// docs/DECISIONS.md D3.
			continue
		case *ast.NamespaceDeclaration:
			// `namespace bytes;` (core.bytes.ca) only affects how callers
			// qualify a call (bytes.to_bytes(...)); codegen dispatches
			// module.function calls by name regardless (D6), so there's
			// nothing to emit.
			continue
		default:
			src, err := g.statement(stmt)
			if err != nil {
				return "", err
			}
			if src != "" {
				mainStmts = append(mainStmts, src)
			}
		}
	}

	var b strings.Builder
	fmt.Fprintf(&b, "-module(%s).\n", erlAtom(g.module))
	// A Calcium function name can collide with an Erlang auto-imported BIF
	// (abs/1, length/1, size/1, hd/1, ...), which erlc otherwise rejects as
	// an ambiguous call. Generated code never relies on auto-import (every
	// call site in g.call is to a Calcium-defined function), so disabling it
	// is safe and avoids tracking the BIF list.
	b.WriteString("-compile(no_auto_import).\n")
	fmt.Fprintf(&b, "-export([%s]).\n\n", strings.Join(exports, ", "))

	fmt.Fprintf(&b, "main() ->\n")
	if len(mainStmts) == 0 {
		b.WriteString("    ok.\n\n")
	} else {
		b.WriteString("    " + strings.Join(mainStmts, ",\n    ") + ".\n\n")
	}

	for _, f := range funcs {
		b.WriteString(f)
		b.WriteString("\n\n")
	}

	return b.String(), nil
}

// statement renders a top-level statement as one entry in main/0's body.
// AssignmentStatement becomes an Erlang match binding; an expression
// statement becomes a bare expression, both chained with commas by module().
func (g *generator) statement(stmt ast.Statement) (string, error) {
	switch s := stmt.(type) {
	case *ast.ExpressionStatement:
		return g.expr(s.Expression)
	case *ast.AssignmentStatement:
		val, err := g.expr(s.Value)
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("%s = %s", erlVar(s.Name.Value), val), nil
	default:
		return "", fmt.Errorf("codegen: unsupported top-level statement %T", stmt)
	}
}

func (g *generator) functionDecl(fd *ast.FunctionDeclaration) (string, error) {
	params := make([]string, len(fd.Parameters))
	for i, p := range fd.Parameters {
		params[i] = erlVar(p.Value)
	}

	body, err := g.expr(fd.Body)
	if err != nil {
		return "", fmt.Errorf("codegen: in function %q: %w", fd.Name.Value, err)
	}

	// `func!` (D5): confirmed empirically against the reference VM that
	// OpWrapSuccess at the end of an effect function's body is genuinely
	// unconditional - wrapping a plain .ca function call that already
	// returns success/failure really does double-wrap it
	// (`success(success(5))`). The exception is a direct call to a native
	// primitive or stdlib function (dispatched via builtinCalls/
	// stdlibCalls): those return their value already unwrapped-or-failed
	// (matching pkg/vm/primitives.go returning a raw value or a Go-level
	// Failure that the VM's primitive-call machinery handles specially,
	// not as an ordinary Calcium success/failure flowing through), so
	// wrapping them once here already produces the one level of success(...)
	// the reference actually shows (e.g. io.write_file -> "success(Maybe.None)",
	// not "success(success(Maybe.None))").
	if fd.IsEffect && !isDirectPrimitiveOrStdlibCall(fd.Body) {
		body = fmt.Sprintf("{ok, %s}", body)
	}

	return fmt.Sprintf("%s(%s) ->\n    %s.", erlAtom(fd.Name.Value), strings.Join(params, ", "), body), nil
}

// isDirectPrimitiveOrStdlibCall reports whether e is a call whose target is
// dispatched via builtinCalls or stdlibCalls - see the D5 comment above.
func isDirectPrimitiveOrStdlibCall(e ast.Expression) bool {
	call, ok := e.(*ast.CallExpression)
	if !ok {
		return false
	}
	switch f := call.Function.(type) {
	case *ast.Identifier:
		_, ok := builtinCalls[f.Value]
		return ok
	case *ast.MemberExpression:
		if obj, ok := f.Object.(*ast.Identifier); ok {
			_, ok := stdlibCalls[obj.Value+"."+f.Property.Value]
			return ok
		}
	}
	return false
}

func (g *generator) expr(e ast.Expression) (string, error) {
	switch n := e.(type) {
	case *ast.IntegerLiteral:
		return strconv.FormatInt(n.Value, 10), nil
	case *ast.FloatLiteral:
		return strconv.FormatFloat(n.Value, 'g', -1, 64), nil
	case *ast.BooleanLiteral:
		return strconv.FormatBool(n.Value), nil
	case *ast.StringLiteral:
		return erlBinaryString(n.Value), nil
	case *ast.Identifier:
		// A reference to a global function used as a value (e.g.
		// `reduce(arr, helper, [])`, passing `helper` itself rather than
		// calling it) needs an explicit fun reference - plain `erlAtom(name)`
		// in an expression position is a 0-ary call attempt, not a value.
		if arity, ok := g.globals[n.Value]; ok {
			return fmt.Sprintf("fun %s/%d", erlAtom(n.Value), arity), nil
		}
		return erlVar(n.Value), nil
	case *ast.InfixExpression:
		return g.infix(n)
	case *ast.PrefixExpression:
		return g.prefix(n)
	case *ast.CallExpression:
		return g.call(n)
	case *ast.MatchExpression:
		return g.match(n)
	case *ast.ArrayLiteral:
		return g.arrayLiteral(n)
	case *ast.TupleLiteral:
		return g.tupleLiteral(n)
	case *ast.HashLiteral:
		return g.hashLiteral(n)
	case *ast.IndexExpression:
		return g.index(n)
	case *ast.DoExpression:
		return g.doExpr(n)
	case *ast.PipeExpression:
		return g.pipe(n.Left, n.Right)
	case *ast.EffectPipeExpression:
		// Treated identically to `|>` for now - see the func! note in
		// functionDecl and docs/DECISIONS.md D5 for the deferred
		// success/failure wrapping this skips.
		return g.pipe(n.Left, n.Right)
	case *ast.EffectHandleExpression:
		return g.effectHandle(n)
	case *ast.MemberExpression:
		return g.stdlibValue(n)
	case *ast.SuccessExpression:
		v, err := g.expr(n.Value)
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("{ok, %s}", v), nil
	case *ast.FailureExpression:
		v, err := g.expr(n.Value)
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("{error, %s}", v), nil
	case *ast.LambdaExpression:
		return g.lambda(n)
	default:
		return "", fmt.Errorf("codegen: unsupported expression %T (later stage)", e)
	}
}

func (g *generator) arrayLiteral(n *ast.ArrayLiteral) (string, error) {
	elems, err := g.exprList(n.Elements)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("[%s]", strings.Join(elems, ", ")), nil
}

func (g *generator) tupleLiteral(n *ast.TupleLiteral) (string, error) {
	elems, err := g.exprList(n.Elements)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("{%s}", strings.Join(elems, ", ")), nil
}

// hashLiteral renders a Calcium hash as an Erlang map. The reference parser
// (parseHashPair) already rewrites a bare-identifier key (`{name: ...}`)
// into a *ast.StringLiteral, so every key here is an ordinary expression.
func (g *generator) hashLiteral(n *ast.HashLiteral) (string, error) {
	pairs := make([]string, len(n.Pairs))
	for i, p := range n.Pairs {
		key, err := g.expr(p.Key)
		if err != nil {
			return "", err
		}
		val, err := g.expr(p.Value)
		if err != nil {
			return "", err
		}
		pairs[i] = fmt.Sprintf("%s => %s", key, val)
	}
	return fmt.Sprintf("#{%s}", strings.Join(pairs, ", ")), nil
}

func (g *generator) index(n *ast.IndexExpression) (string, error) {
	left, err := g.expr(n.Left)
	if err != nil {
		return "", err
	}
	idx, err := g.expr(n.Index)
	if err != nil {
		return "", err
	}
	// Calcium arrays are 0-indexed lists; Erlang's lists:nth/2 is 1-indexed.
	return fmt.Sprintf("lists:nth((%s) + 1, %s)", idx, left), nil
}

// doExpr renders a do...end block. Erlang's comma-separated expression
// sequence already evaluates to its last member, so this needs no special
// control-flow construct - just the same statement rendering main/0 uses.
func (g *generator) doExpr(n *ast.DoExpression) (string, error) {
	stmts := make([]string, 0, len(n.Statements)+1)
	for _, s := range n.Statements {
		src, err := g.statement(s)
		if err != nil {
			return "", err
		}
		stmts = append(stmts, src)
	}
	final, err := g.expr(n.FinalExpression)
	if err != nil {
		return "", err
	}
	stmts = append(stmts, final)
	return fmt.Sprintf("(%s)", strings.Join(stmts, ", ")), nil
}

// pipe renders `x |> f` / `x |> f(y)` as `f(x)` / `f(x, y)` - the pipe
// input becomes the first argument, matching the reference compiler's
// desugaring in pkg/compiler/compiler.go.
func (g *generator) pipe(left, right ast.Expression) (string, error) {
	leftSrc, err := g.expr(left)
	if err != nil {
		return "", err
	}
	if call, ok := right.(*ast.CallExpression); ok {
		fnSrc, err := g.callTarget(call.Function)
		if err != nil {
			return "", err
		}
		args, err := g.exprList(call.Arguments)
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("%s(%s)", fnSrc, strings.Join(append([]string{leftSrc}, args...), ", ")), nil
	}
	fnSrc, err := g.callTarget(right)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%s(%s)", fnSrc, leftSrc), nil
}

// effectHandle renders `subject !? { success(v) => ... failure(e) => ... }`
// as an Erlang case over the {ok, V} / {error, E} representation decided in
// docs/DECISIONS.md D3.
func (g *generator) effectHandle(n *ast.EffectHandleExpression) (string, error) {
	subject, err := g.expr(n.Subject)
	if err != nil {
		return "", err
	}
	successBody, err := g.expr(n.SuccessBody)
	if err != nil {
		return "", err
	}
	failureBody, err := g.expr(n.FailureBody)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("case %s of\n        {ok, %s} -> %s;\n        {error, %s} -> %s\n    end",
		subject, erlVar(n.SuccessVar.Value), successBody, erlVar(n.FailureVar.Value), failureBody), nil
}

// lambda renders `(params) => body` as an Erlang anonymous fun.
func (g *generator) lambda(n *ast.LambdaExpression) (string, error) {
	params := make([]string, len(n.Parameters))
	for i, p := range n.Parameters {
		params[i] = erlVar(p.Value)
	}
	body, err := g.expr(n.Body)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("fun(%s) -> %s end", strings.Join(params, ", "), body), nil
}

func (g *generator) exprList(exprs []ast.Expression) ([]string, error) {
	out := make([]string, len(exprs))
	for i, e := range exprs {
		src, err := g.expr(e)
		if err != nil {
			return nil, err
		}
		out[i] = src
	}
	return out, nil
}

func (g *generator) infix(n *ast.InfixExpression) (string, error) {
	left, err := g.expr(n.Left)
	if err != nil {
		return "", err
	}
	right, err := g.expr(n.Right)
	if err != nil {
		return "", err
	}
	op, ok := infixOps[n.Operator]
	if !ok {
		return "", fmt.Errorf("codegen: unsupported operator %q", n.Operator)
	}
	return fmt.Sprintf("(%s %s %s)", left, op, right), nil
}

// infixOps maps Calcium infix operators to their Erlang equivalents.
// `==`/`!=` map to Erlang's exact-match operators (`=:=`/`=/=`), matching
// Calcium's value equality rather than Erlang's type-coercing `==`/`/=`.
var infixOps = map[string]string{
	"+": "+", "-": "-", "*": "*", "/": "/", "%": "rem",
	"==": "=:=", "!=": "=/=",
	"<": "<", ">": ">", "<=": "=<", ">=": ">=",
	"&&": "andalso", "||": "orelse",
	"&": "band", "|": "bor", "^": "bxor", "<<": "bsl", ">>": "bsr",
}

func (g *generator) prefix(n *ast.PrefixExpression) (string, error) {
	right, err := g.expr(n.Right)
	if err != nil {
		return "", err
	}
	switch n.Operator {
	case "-":
		return fmt.Sprintf("(-%s)", right), nil
	case "!":
		return fmt.Sprintf("(not %s)", right), nil
	case "^":
		return fmt.Sprintf("(bnot %s)", right), nil
	default:
		return "", fmt.Errorf("codegen: unsupported prefix operator %q", n.Operator)
	}
}

func (g *generator) call(n *ast.CallExpression) (string, error) {
	args, err := g.exprList(n.Arguments)
	if err != nil {
		return "", err
	}

	if ident, ok := n.Function.(*ast.Identifier); ok {
		// A capitalized name in call position is an ADT constructor, not a
		// function - Calcium functions are lowercase by convention. See
		// docs/DECISIONS.md D3: ADT variants are tagged tuples.
		if isUpper(ident.Value) {
			return adtTuple(ident.Value, args), nil
		}
		// A global built-in (len, head, tail, to_string, ...) is implemented
		// in erl/calcium_builtins.erl rather than compiled as a Calcium
		// function, since there's no corresponding func declaration to walk.
		if target, ok := builtinCalls[ident.Value]; ok {
			return fmt.Sprintf("%s(%s)", target, strings.Join(args, ", ")), nil
		}
	}

	// Type.Variant(...) - a type-qualified ADT constructor, e.g.
	// Maybe.Some(5). See docs/DECISIONS.md D3: the type qualifier is
	// informational only, so this constructs the same tagged tuple as the
	// bare Variant(...) form.
	if m, ok := n.Function.(*ast.MemberExpression); ok {
		if obj, ok := m.Object.(*ast.Identifier); ok && isUpper(obj.Value) && isUpper(m.Property.Value) {
			return adtTuple(m.Property.Value, args), nil
		}
	}

	fnSrc, err := g.callTarget(n.Function)
	if err != nil {
		return "", err
	}
	return fmt.Sprintf("%s(%s)", fnSrc, strings.Join(args, ", ")), nil
}

// callTarget renders the callable part of a call expression (everything
// before the argument list): a direct Calcium function name, or a
// module-qualified stdlib call dispatched by name (docs/DECISIONS.md D6).
func (g *generator) callTarget(fn ast.Expression) (string, error) {
	switch f := fn.(type) {
	case *ast.Identifier:
		if _, ok := g.globals[f.Value]; ok {
			return erlAtom(f.Value), nil
		}
		// A global built-in reached as a pipe/call target without its own
		// argument list, e.g. `filter(arr, pred) |> len` (len applied to
		// the filtered result) - see call()'s identical check.
		if target, ok := builtinCalls[f.Value]; ok {
			return target, nil
		}
		// Not a known top-level function or built-in: a local
		// variable/parameter that happens to hold a fun (e.g. `fn` in
		// `func apply(fn, x) = fn(x)`).
		return erlVar(f.Value), nil
	case *ast.MemberExpression:
		return g.stdlibTarget(f)
	default:
		return "", fmt.Errorf("codegen: unsupported call target %T", fn)
	}
}

// stdlibTarget resolves a `module.function` call target to the Erlang
// module:function it's implemented by in erl/. Unknown module.function pairs
// are a hard error naming both parts, since silently misrouting a stdlib
// call would be a worse failure mode than refusing to compile it.
func (g *generator) stdlibTarget(m *ast.MemberExpression) (string, error) {
	obj, ok := m.Object.(*ast.Identifier)
	if !ok {
		return "", fmt.Errorf("codegen: unsupported member call target %T", m.Object)
	}
	key := obj.Value + "." + m.Property.Value
	target, ok := stdlibCalls[key]
	if !ok {
		return "", fmt.Errorf("codegen: %s is not yet mapped to an erl/ implementation (later stage)", key)
	}
	return target, nil
}

// stdlibValue resolves a `module.member` expression used as a plain value
// rather than a call target - currently only qualified ADT constructors used
// without arguments (e.g. `Maybe.None`).
func (g *generator) stdlibValue(m *ast.MemberExpression) (string, error) {
	obj, ok := m.Object.(*ast.Identifier)
	if ok && isUpper(obj.Value) && isUpper(m.Property.Value) {
		return adtTuple(m.Property.Value, nil), nil
	}
	return "", fmt.Errorf("codegen: unsupported member expression %s.%s (later stage)", describeExpr(m.Object), m.Property.Value)
}

func describeExpr(e ast.Expression) string {
	if id, ok := e.(*ast.Identifier); ok {
		return id.Value
	}
	return fmt.Sprintf("%T", e)
}

// stdlibCalls maps `module.function` to its erl/ implementation. Populated
// incrementally as stages add stdlib coverage; see erl/calcium_io.erl.
var stdlibCalls = map[string]string{
	"io.say":     "calcium_io_native:say",
	"io.println": "calcium_io_native:say",
	"io.print":   "calcium_io_native:print",
}

// builtinCalls maps a global built-in function name (no module qualifier) to
// its erl/ implementation. See erl/calcium_builtins.erl and
// docs/DECISIONS.md D7 for the head/tail/Maybe limitation.
var builtinCalls = map[string]string{
	"len":       "calcium_builtins:len",
	"head":      "calcium_builtins:head",
	"tail":      "calcium_builtins:tail",
	"to_string": "calcium_builtins:to_string",
	"map":       "calcium_builtins:map",
	"filter":    "calcium_builtins:filter",
	"reduce":    "calcium_builtins:reduce",
	"concat":    "calcium_builtins:concat",
	"push":      "calcium_builtins:push",
	"range":     "calcium_builtins:range",
	"get":       "calcium_builtins:get",
	// core.math's native primitives (math.ca calls these directly by name,
	// not as module.function - see docs/DECISIONS.md D8). Delegates
	// straight to Erlang's math module.
	"__floor": "calcium_primitives:floor_",
	"__ceil":  "calcium_primitives:ceil_",
	"__round": "calcium_primitives:round_",
	"__sqrt":  "calcium_primitives:sqrt_",
	"__pow":   "calcium_primitives:pow_",
	"__sin":   "calcium_primitives:sin_",
	"__cos":   "calcium_primitives:cos_",
	"__tan":   "calcium_primitives:tan_",
	"__log":   "calcium_primitives:log_",
	"__exp":   "calcium_primitives:exp_",
	// core.string's native primitives (string.ca calls these directly by
	// name too - see docs/DECISIONS.md D6/the plan's stage 1 entry).
	"__str_len":         "calcium_str_native:len",
	"__str_split":       "calcium_str_native:split",
	"__str_join":        "calcium_str_native:join",
	"__str_trim":        "calcium_str_native:trim",
	"__str_upper":       "calcium_str_native:upper",
	"__str_lower":       "calcium_str_native:lower",
	"__str_replace":     "calcium_str_native:replace",
	"__str_substring":   "calcium_str_native:substring",
	"__str_char_at":     "calcium_str_native:char_at",
	"__str_from_code":   "calcium_str_native:from_code",
	"__str_index_of":    "calcium_str_native:index_of",
	"__str_starts_with": "calcium_str_native:starts_with",
	"__str_ends_with":   "calcium_str_native:ends_with",
	"__str_contains":    "calcium_str_native:contains",
	"__str_to_bytes":    "calcium_str_native:to_bytes",
	"__str_from_bytes":  "calcium_str_native:from_bytes",
	"__int_to_bytes":    "calcium_primitives:int_to_bytes",
	"__float_to_bytes":  "calcium_primitives:float_to_bytes",
}

// isUpper reports whether a Calcium identifier is capitalized, the
// convention this project uses (matching calcium-lang's own grammar rule for
// constructor patterns) to tell an ADT variant/type name apart from a
// function or variable name without needing the type declaration in scope.
func isUpper(s string) bool {
	return s != "" && s[0] >= 'A' && s[0] <= 'Z'
}

// adtTuple renders an ADT constructor call/value as a tagged tuple. The tag
// is always the bare variant name (not type-qualified), so both `Some(x)`
// and `Maybe.Some(x)` construct/match the same representation - mirroring
// calcium-lang PR #185's bare-or-qualified pattern matching.
func adtTuple(variant string, args []string) string {
	tag := "'" + variant + "'"
	if len(args) == 0 {
		return tag
	}
	return fmt.Sprintf("{%s, %s}", tag, strings.Join(args, ", "))
}

// match renders a Calcium match expression as an Erlang case expression.
// Only literal, wildcard (`_`), and plain-binding patterns are supported in
// stage 1; constructor/ADT patterns are a later stage once ADTs are mapped
// to tagged tuples (docs/DECISIONS.md D3).
// match renders a Calcium match expression. It wraps the case in an
// immediately-invoked fun binding the subject to a fresh variable (rather
// than inlining the subject expression into `case ... of`), so a "standard
// case" arm (see matchCase) can refer to the subject's already-evaluated
// value in a guard without re-evaluating a side-effecting subject twice.
func (g *generator) match(n *ast.MatchExpression) (string, error) {
	subject, err := g.expr(n.Subject)
	if err != nil {
		return "", err
	}
	subjectVar := "V__subject"

	var arms []string
	for _, c := range n.Cases {
		arm, err := g.matchCase(c, subjectVar)
		if err != nil {
			return "", err
		}
		arms = append(arms, arm)
	}

	return fmt.Sprintf("(fun(%s) -> case %s of\n        %s\n    end end)(%s)",
		subjectVar, subjectVar, strings.Join(arms, ";\n        "), subject), nil
}

func (g *generator) matchCase(c *ast.MatchCase, subjectVar string) (string, error) {
	pattern, guard, err := g.matchPatternAndGuard(c, subjectVar)
	if err != nil {
		return "", err
	}

	body, err := g.expr(c.Body)
	if err != nil {
		return "", err
	}

	if guard != "" {
		return fmt.Sprintf("%s when %s -> %s", pattern, guard, body), nil
	}
	return fmt.Sprintf("%s -> %s", pattern, body), nil
}

// matchPatternAndGuard renders a case's pattern, and an extra guard beyond
// `c.Guard` when needed. The reference compiler (pkg/compiler/compiler.go's
// compileMatch "standard case" branch) compiles any pattern that is not a
// guard-clause binding, a ConstructorPattern, or `_` as an *expression* and
// compares it for equality against the subject - so an arbitrary boolean
// expression (`n < 0 => ...`) is valid pattern syntax, not just literals.
// pattern() renders the subset that has a direct Erlang pattern shape;
// anything else falls back to `_` with an equality guard against subjectVar.
func (g *generator) matchPatternAndGuard(c *ast.MatchCase, subjectVar string) (pattern string, guard string, err error) {
	if c.Guard != nil {
		pattern, err = g.pattern(c.Pattern, c.IsDefault)
		if err != nil {
			return "", "", err
		}
		guard, err = g.expr(c.Guard)
		if err != nil {
			return "", "", err
		}
		return pattern, guard, nil
	}

	if isSimplePattern(c.Pattern) || c.IsDefault {
		pattern, err = g.pattern(c.Pattern, c.IsDefault)
		if err != nil {
			return "", "", err
		}
		return pattern, "", nil
	}

	exprSrc, err := g.expr(c.Pattern)
	if err != nil {
		return "", "", fmt.Errorf("codegen: unsupported match pattern %T (later stage): %w", c.Pattern, err)
	}
	return "_", fmt.Sprintf("(%s) =:= %s", exprSrc, subjectVar), nil
}

// isSimplePattern reports whether p has a direct Erlang pattern
// representation via pattern(), as opposed to needing the equality-guard
// fallback in matchPatternAndGuard.
func isSimplePattern(p ast.Expression) bool {
	switch p.(type) {
	case *ast.WildcardExpression, *ast.Identifier,
		*ast.IntegerLiteral, *ast.FloatLiteral, *ast.BooleanLiteral, *ast.StringLiteral,
		*ast.PrefixExpression, *ast.ConstructorPattern, *ast.ArrayLiteral, *ast.TupleLiteral:
		return true
	default:
		return false
	}
}

// pattern renders a match arm's pattern. A bare identifier binds (Erlang
// pattern variables bind unconditionally), matching Calcium's own rule that
// a binding name in pattern position always matches and binds.
func (g *generator) pattern(p ast.Expression, isDefault bool) (string, error) {
	if isDefault {
		return "_", nil
	}
	switch n := p.(type) {
	case *ast.WildcardExpression:
		return "_", nil
	case *ast.Identifier:
		return erlVar(n.Value), nil
	case *ast.IntegerLiteral, *ast.FloatLiteral, *ast.BooleanLiteral, *ast.StringLiteral:
		return g.expr(p)
	case *ast.PrefixExpression:
		// A negative numeric literal pattern, e.g. `-1 => ...`.
		return g.expr(p)
	case *ast.ConstructorPattern:
		return g.constructorPattern(n)
	case *ast.ArrayLiteral:
		elems := make([]string, len(n.Elements))
		for i, el := range n.Elements {
			src, err := g.pattern(el, isWildcard(el))
			if err != nil {
				return "", err
			}
			elems[i] = src
		}
		return fmt.Sprintf("[%s]", strings.Join(elems, ", ")), nil
	case *ast.TupleLiteral:
		elems := make([]string, len(n.Elements))
		for i, el := range n.Elements {
			src, err := g.pattern(el, isWildcard(el))
			if err != nil {
				return "", err
			}
			elems[i] = src
		}
		return fmt.Sprintf("{%s}", strings.Join(elems, ", ")), nil
	default:
		return "", fmt.Errorf("codegen: unsupported match pattern %T (later stage)", p)
	}
}

// constructorPattern renders `Variant(f1, f2, ...)` as the tagged-tuple
// pattern matching adtTuple's construction. Fields are restricted by the
// reference parser (parsePatternAtom) to a binding, wildcard, or literal, so
// pattern() handles every case already.
func (g *generator) constructorPattern(cp *ast.ConstructorPattern) (string, error) {
	fields := make([]string, len(cp.Fields))
	for i, f := range cp.Fields {
		src, err := g.pattern(f, isWildcard(f))
		if err != nil {
			return "", err
		}
		fields[i] = src
	}
	if len(fields) == 0 {
		return fmt.Sprintf("'%s'", cp.Name), nil
	}
	return fmt.Sprintf("{'%s', %s}", cp.Name, strings.Join(fields, ", ")), nil
}

func isWildcard(e ast.Expression) bool {
	_, ok := e.(*ast.WildcardExpression)
	return ok
}

// erlAtom lowercases and sanitizes a Calcium identifier for use as an Erlang
// atom (function/module name). Calcium names are already lowercase by
// convention, so this currently only guards against reserved characters.
// erlAtom renders a Calcium function/module name as an Erlang atom. Always
// single-quoted: an unquoted atom has real restrictions an ordinary Calcium
// identifier can violate without looking unusual at all - leading
// underscore is lexed as a *variable*, not an atom, which is exactly what
// calcium-lang's own array.ca's private helpers (`_min`, `_reverse_fn`, ...)
// do; quoting sidesteps the whole restriction instead of special-casing it.
func erlAtom(name string) string {
	name = strings.ReplaceAll(name, "-", "_")
	return "'" + strings.ReplaceAll(name, "'", "\\'") + "'"
}

// erlVar renders a Calcium identifier as an Erlang variable: capitalize the
// first letter (Erlang variables must start uppercase) and replace any
// internal `_`-prefixing ambiguity is a non-issue since Calcium identifiers
// don't start with `_` in pattern position other than the wildcard, handled
// separately in pattern().
func erlVar(name string) string {
	if name == "" {
		return "_"
	}
	return "V_" + name
}

// erlBinaryString renders a Calcium string literal as an Erlang binary,
// since Calcium strings are UTF-8 text and Erlang's binary type (not list of
// integers) is the idiomatic text representation.
func erlBinaryString(s string) string {
	var b strings.Builder
	b.WriteString(`<<"`)
	for _, r := range s {
		switch r {
		case '"':
			b.WriteString(`\"`)
		case '\\':
			b.WriteString(`\\`)
		default:
			b.WriteRune(r)
		}
	}
	b.WriteString(`"/utf8>>`)
	return b.String()
}
