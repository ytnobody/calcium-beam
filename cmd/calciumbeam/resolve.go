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

// linkedModule is a resolved Calcium module: a `use <path>;` that named
// either a project-local file or a Boneyard/GitHub module already fetched
// onto disk by the reference `bone`/`calcium` tooling - this resolver never
// fetches anything itself (see the design/implementation-plan comments on
// https://github.com/ytnobody/calcium-beam/issues/6). A core.* stdlib
// module is still out of scope here (handled by codegen's existing
// stdlibCalls/array dispatch).
type linkedModule struct {
	// Alias is how the importing file refers to this module in a call
	// (`alias.fn(...)`) - the import path's last dotted segment (local) or
	// the derived module name (remote), matching the reference compiler's
	// own moduleName derivation (pkg/compiler/compiler.go's
	// *ast.UseStatement case).
	Alias string
	// Path is the resolved absolute file path - used as the dedup/cycle
	// key, since two different import spellings can name the same file.
	Path    string
	Program *ast.Program
	// IsRemote, Author, Name are set for a Boneyard/GitHub module (author
	// and module/repo name, as derived from the `use` statement) - used by
	// main.go to pick a stable name prefix instead of path-relativizing
	// against the entry file (see docs/DECISIONS.md D20).
	IsRemote     bool
	Author, Name string
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
			if len(use.Path.Parts) > 0 && use.Path.Parts[0] == "core" {
				// core.* stdlib modules are codegen's existing
				// stdlibCalls/array dispatch's concern, not this
				// resolver's.
				continue
			}

			var resolved, alias, author, name string
			if use.Path.IsRemote {
				author, name = remoteAuthorName(use.Path)
				var err error
				resolved, err = locateRemoteModule(dir, use.Path, author, name)
				if err != nil {
					return err
				}
				// The reference compiler trims a "-calcium" suffix from the
				// call-site alias for the raw-URL form only (the cache/
				// calcium_modules directory name itself keeps it, matching
				// pkg/compiler/compiler.go's *ast.UseStatement case) - the
				// author/module form never has it added in the first place.
				alias = strings.TrimSuffix(name, "-calcium")
			} else {
				rel := strings.Join(use.Path.Parts, string(filepath.Separator))
				direct := filepath.Join(dir, rel+".ca")
				nested := filepath.Join(dir, rel, "mod.ca")
				if _, err := os.Stat(direct); err == nil {
					resolved = direct
				} else if _, err := os.Stat(nested); err == nil {
					resolved = nested
				} else {
					return fmt.Errorf("resolve: could not find local module %q - tried %s and %s", use.Path.String(), direct, nested)
				}
				alias = use.Path.Parts[len(use.Path.Parts)-1]
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
				Alias:    alias,
				Path:     resolved,
				Program:  modProgram,
				IsRemote: use.Path.IsRemote,
				Author:   author,
				Name:     name,
			})
		}
		return nil
	}

	if err := visit(entryAbs, entry); err != nil {
		return nil, err
	}
	return mods, nil
}

// remoteAuthorName derives the (author, name) pair used both as the cache
// directory name and (after alias trimming above) the call-site alias -
// matching pkg/compiler/compiler.go's *ast.UseStatement case and
// pkg/vm/cache.go's parseGitHubURL exactly, including that the raw-URL
// form's "name" keeps any "-calcium" suffix (only the alias gets trimmed).
func remoteAuthorName(path *ast.ModulePath) (author, name string) {
	if path.RawURL != "" {
		parts := strings.Split(path.RawURL, "/")
		if len(parts) >= 2 {
			author = parts[len(parts)-2]
			name = parts[len(parts)-1]
		}
		return
	}
	return path.Author, path.Name
}

// locateRemoteModule finds a Boneyard/GitHub module's mod.ca already
// fetched onto disk by the reference bone/calcium tooling - calcium-beam
// never fetches over the network itself (docs/DECISIONS.md D20). Search
// order and locations mirror pkg/vm/stdlib.go directly: the author/module
// form checks calcium_modules/ walked up from startDir (like
// getLocalModuleDirs) before the global cache; the raw-URL form checks
// only the global cache, since LoadGitHubURLModule itself never consults
// calcium_modules/.
func locateRemoteModule(startDir string, path *ast.ModulePath, author, name string) (string, error) {
	var tried []string

	if path.RawURL == "" {
		for dir := startDir; ; {
			candidate := filepath.Join(dir, "calcium_modules", author, name, "mod.ca")
			tried = append(tried, candidate)
			if _, err := os.Stat(candidate); err == nil {
				return candidate, nil
			}
			parent := filepath.Dir(dir)
			if parent == dir {
				break
			}
			dir = parent
		}
	}

	if home, err := os.UserHomeDir(); err == nil {
		candidate := filepath.Join(home, ".calcium", "cache", author, name, "mod.ca")
		tried = append(tried, candidate)
		if _, err := os.Stat(candidate); err == nil {
			return candidate, nil
		}
	}

	return "", fmt.Errorf("resolve: could not find remote module %q - tried %s (run `bone add %s/%s` or `bone add --global %s/%s` first)",
		path.String(), strings.Join(tried, ", "), author, name, author, name)
}
