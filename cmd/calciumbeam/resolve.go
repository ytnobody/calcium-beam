package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/ytnobody/calcium-lang/pkg/ast"
	"github.com/ytnobody/calcium-lang/pkg/lexer"
	"github.com/ytnobody/calcium-lang/pkg/parser"
)

// linkedModule is a resolved local Calcium module: a `use <path>;` that
// named a project-local file, not a core.* stdlib module or a remote
// (Boneyard/GitHub) one - those two cases are out of scope for this phase
// (see the design/implementation-plan comments on
// https://github.com/ytnobody/calcium-beam/issues/1).
type linkedModule struct {
	// Alias is how the importing file refers to this module in a call
	// (`alias.fn(...)`) - the import path's last dotted segment, matching
	// the reference compiler's own moduleName derivation
	// (pkg/compiler/compiler.go's *ast.UseStatement case).
	Alias string
	// Path is the resolved absolute file path - used as the dedup/cycle
	// key, since two different import spellings can name the same file.
	Path    string
	Program *ast.Program
}

// resolveModules finds every local-module `use` statement reachable from
// entry (transitively - a linked module may itself use other local
// modules), parses each exactly once, and returns them in no particular
// order (codegen doesn't need one: each gets a distinct prefix). entryPath
// must be absolute or relative to the current directory; local modules
// resolve relative to the file that imports them, not always entryPath.
func resolveModules(entryPath string, entry *ast.Program) ([]linkedModule, error) {
	entryAbs, err := filepath.Abs(entryPath)
	if err != nil {
		return nil, err
	}

	var mods []linkedModule
	visited := map[string]bool{} // resolved path -> already compiled into mods
	onStack := map[string]bool{} // resolved path -> currently being resolved (cycle guard)
	var stack []string           // for the cycle error message

	var visit func(path string, program *ast.Program) error
	visit = func(path string, program *ast.Program) error {
		onStack[path] = true
		stack = append(stack, path)
		defer func() {
			onStack[path] = false
			stack = stack[:len(stack)-1]
		}()

		dir := filepath.Dir(path)
		for _, stmt := range program.Statements {
			use, ok := stmt.(*ast.UseStatement)
			if !ok {
				continue
			}
			if use.Path.IsRemote || len(use.Path.Parts) == 0 || use.Path.Parts[0] == "core" {
				// Remote (Boneyard/GitHub) modules and core.* stdlib modules
				// are handled elsewhere (phase 2, and codegen's existing
				// stdlibCalls/array dispatch, respectively) - not this
				// resolver's concern.
				continue
			}

			rel := strings.Join(use.Path.Parts, string(filepath.Separator))
			direct := filepath.Join(dir, rel+".ca")
			nested := filepath.Join(dir, rel, "mod.ca")

			var resolved string
			if _, err := os.Stat(direct); err == nil {
				resolved = direct
			} else if _, err := os.Stat(nested); err == nil {
				resolved = nested
			} else {
				return fmt.Errorf("resolve: could not find local module %q - tried %s and %s", use.Path.String(), direct, nested)
			}
			resolved, err := filepath.Abs(resolved)
			if err != nil {
				return err
			}

			if onStack[resolved] {
				return fmt.Errorf("resolve: import cycle: %s", strings.Join(append(stack, resolved), " -> "))
			}
			if visited[resolved] {
				continue
			}
			visited[resolved] = true

			src, err := os.ReadFile(resolved)
			if err != nil {
				return err
			}
			l := lexer.New(string(src))
			p := parser.New(l)
			modProgram := p.ParseProgram()
			if errs := p.Errors(); len(errs) > 0 {
				return fmt.Errorf("resolve: parsing %s: %s", resolved, strings.Join(errs, "; "))
			}

			if err := visit(resolved, modProgram); err != nil {
				return err
			}

			mods = append(mods, linkedModule{
				Alias:   use.Path.Parts[len(use.Path.Parts)-1],
				Path:    resolved,
				Program: modProgram,
			})
		}
		return nil
	}

	if err := visit(entryAbs, entry); err != nil {
		return nil, err
	}
	return mods, nil
}
