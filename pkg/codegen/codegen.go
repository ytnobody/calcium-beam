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
	g := &generator{module: moduleName}
	return g.renderModule(program)
}

type generator struct {
	module string
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
	if fd.IsEffect {
		return "", fmt.Errorf("codegen: func! (effect functions) is a later stage, got %q", fd.Name.Value)
	}

	params := make([]string, len(fd.Parameters))
	for i, p := range fd.Parameters {
		params[i] = erlVar(p.Value)
	}

	body, err := g.expr(fd.Body)
	if err != nil {
		return "", fmt.Errorf("codegen: in function %q: %w", fd.Name.Value, err)
	}

	return fmt.Sprintf("%s(%s) ->\n    %s.", erlAtom(fd.Name.Value), strings.Join(params, ", "), body), nil
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
		return erlVar(n.Value), nil
	case *ast.InfixExpression:
		return g.infix(n)
	case *ast.PrefixExpression:
		return g.prefix(n)
	case *ast.CallExpression:
		return g.call(n)
	case *ast.MatchExpression:
		return g.match(n)
	default:
		return "", fmt.Errorf("codegen: unsupported expression %T (later stage)", e)
	}
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
	fn, ok := n.Function.(*ast.Identifier)
	if !ok {
		return "", fmt.Errorf("codegen: only direct named calls are supported in stage 1, got %T", n.Function)
	}
	args := make([]string, len(n.Arguments))
	for i, a := range n.Arguments {
		arg, err := g.expr(a)
		if err != nil {
			return "", err
		}
		args[i] = arg
	}
	return fmt.Sprintf("%s(%s)", erlAtom(fn.Value), strings.Join(args, ", ")), nil
}

// match renders a Calcium match expression as an Erlang case expression.
// Only literal, wildcard (`_`), and plain-binding patterns are supported in
// stage 1; constructor/ADT patterns are a later stage once ADTs are mapped
// to tagged tuples (docs/DECISIONS.md D3).
func (g *generator) match(n *ast.MatchExpression) (string, error) {
	subject, err := g.expr(n.Subject)
	if err != nil {
		return "", err
	}

	var arms []string
	for _, c := range n.Cases {
		arm, err := g.matchCase(c)
		if err != nil {
			return "", err
		}
		arms = append(arms, arm)
	}

	return fmt.Sprintf("case %s of\n        %s\n    end", subject, strings.Join(arms, ";\n        ")), nil
}

func (g *generator) matchCase(c *ast.MatchCase) (string, error) {
	pattern, err := g.pattern(c.Pattern, c.IsDefault)
	if err != nil {
		return "", err
	}

	body, err := g.expr(c.Body)
	if err != nil {
		return "", err
	}

	if c.Guard != nil {
		guard, err := g.expr(c.Guard)
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("%s when %s -> %s", pattern, guard, body), nil
	}
	return fmt.Sprintf("%s -> %s", pattern, body), nil
}

// pattern renders a match arm's pattern. A bare identifier binds (Erlang
// pattern variables bind unconditionally), matching Calcium's own rule that
// a binding name in pattern position always matches and binds.
func (g *generator) pattern(p ast.Expression, isDefault bool) (string, error) {
	if isDefault {
		return "_", nil
	}
	switch n := p.(type) {
	case *ast.Identifier:
		return erlVar(n.Value), nil
	case *ast.IntegerLiteral, *ast.FloatLiteral, *ast.BooleanLiteral, *ast.StringLiteral:
		return g.expr(p)
	case *ast.PrefixExpression:
		// A negative numeric literal pattern, e.g. `-1 => ...`.
		return g.expr(p)
	default:
		return "", fmt.Errorf("codegen: unsupported match pattern %T (constructor/ADT patterns are a later stage)", p)
	}
}

// erlAtom lowercases and sanitizes a Calcium identifier for use as an Erlang
// atom (function/module name). Calcium names are already lowercase by
// convention, so this currently only guards against reserved characters.
func erlAtom(name string) string {
	return strings.ReplaceAll(name, "-", "_")
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
