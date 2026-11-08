// calciumbeam compiles a Calcium source file to Erlang source, using
// calcium-lang's own lexer/parser/typechecker as a library (see
// docs/DECISIONS.md D1).
package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/ytnobody/calcium-beam/pkg/codegen"
	"github.com/ytnobody/calcium-lang/pkg/lexer"
	"github.com/ytnobody/calcium-lang/pkg/parser"
	"github.com/ytnobody/calcium-lang/pkg/typechecker"
)

func main() {
	if len(os.Args) < 2 {
		fmt.Fprintln(os.Stderr, "usage: calciumbeam <file.ca>")
		os.Exit(1)
	}
	if err := run(os.Args[1]); err != nil {
		fmt.Fprintf(os.Stderr, "calciumbeam: %s\n", err)
		os.Exit(1)
	}
}

func run(path string) error {
	src, err := os.ReadFile(path)
	if err != nil {
		return err
	}

	l := lexer.New(string(src))
	p := parser.New(l)
	program := p.ParseProgram()
	if errs := p.Errors(); len(errs) > 0 {
		return fmt.Errorf("parse errors:\n%s", strings.Join(errs, "\n"))
	}

	if errs := typechecker.Check(program); len(errs) > 0 {
		lines := make([]string, len(errs))
		for i, e := range errs {
			lines[i] = e.Error()
		}
		return fmt.Errorf("type errors:\n%s", strings.Join(lines, "\n"))
	}

	moduleName := strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))
	erl, err := codegen.Module(moduleName, program)
	if err != nil {
		return err
	}

	outPath := moduleName + ".erl"
	if err := os.WriteFile(outPath, []byte(erl), 0o644); err != nil {
		return err
	}
	fmt.Printf("wrote %s\n", outPath)
	return nil
}
