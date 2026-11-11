// calciumbeam compiles a Calcium source file to Erlang source, using
// calcium-lang's own lexer/parser/typechecker as a library (see
// docs/DECISIONS.md D1).
package main

import (
	"fmt"
	"os"
	"os/exec"
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

	// Prefixed so a source file named e.g. math.ca or string.ca doesn't
	// generate a module that shadows Erlang/OTP's own math/string/... when
	// both end up on the same code path - see docs/DECISIONS.md D9.
	moduleName := "calcium_" + strings.TrimSuffix(filepath.Base(path), filepath.Ext(path))

	var opts []codegen.Option
	// core.array's non-primitive functions need calcium-lang's own
	// array.ca auto-linked in (D18) - only bother resolving and reading it
	// when the source might actually need it, since that costs a `go
	// list` round trip.
	if strings.Contains(string(src), "array.") {
		if arraySrc, err := readStdlibFile("array.ca"); err == nil {
			opts = append(opts, codegen.WithArraySource(arraySrc))
		}
	}

	// Project-local modules (D19) - unconditional, unlike array.ca above:
	// resolution is a few extra in-process lexer/parser passes over
	// already-local files, not a `go list` shell-out, so there's nothing
	// worth gating behind a substring check. Returns an empty list (no
	// extra cost beyond one scan of the entry program's own statements)
	// when nothing is imported.
	mods, err := resolveModules(path, program)
	if err != nil {
		return err
	}
	if len(mods) > 0 {
		entryDir := filepath.Dir(path)
		linked := make([]codegen.LinkedModule, len(mods))
		for i, m := range mods {
			prefix := modulePrefix(entryDir, m.Path)
			if m.IsRemote {
				prefix = remoteModulePrefix(m.Author, m.Name)
			}
			linked[i] = codegen.LinkedModule{Alias: m.Alias, Prefix: prefix, Program: m.Program}
		}
		opts = append(opts, codegen.WithLocalModules(linked))
	}

	erl, err := codegen.Module(moduleName, program, opts...)
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

// modulePrefix derives a linked module's Erlang name prefix from its
// resolved path, relative to the entry file's directory when possible (so
// two sibling projects compiled separately don't produce prefixes that
// depend on an absolute filesystem layout) - see docs/DECISIONS.md D19.
func modulePrefix(entryDir, resolvedPath string) string {
	rel, err := filepath.Rel(entryDir, resolvedPath)
	if err != nil || strings.HasPrefix(rel, "..") {
		rel = resolvedPath
	}
	rel = strings.TrimSuffix(rel, filepath.Ext(rel))
	return sanitizePrefix(rel)
}

// sanitizePrefix turns an arbitrary path-like string into a valid Erlang
// identifier fragment - shared by modulePrefix (local modules, relative to
// the entry file) and remoteModulePrefix (remote modules, which have no
// such relative path to begin with - see docs/DECISIONS.md D20).
func sanitizePrefix(s string) string {
	s = strings.Map(func(r rune) rune {
		switch r {
		case '/', '\\', '.', '-':
			return '_'
		}
		return r
	}, s)
	return strings.Trim(s, "_") + "_"
}

// remoteModulePrefix derives a Boneyard/GitHub module's Erlang name prefix
// directly from its (author, name) pair, not its on-disk location -
// calcium_modules/ and the global cache aren't "near" the entry file in any
// meaningful relative sense, and two projects importing the same remote
// module should get the same stable prefix regardless of where either
// project's entry file lives.
func remoteModulePrefix(author, name string) string {
	return sanitizePrefix(author + "_" + name)
}

// readStdlibFile reads a file from calcium-lang's pkg/vm/stdlib/core,
// resolving the module's location the same way tests/differential does
// (via `go list -m`, not an assumed sibling checkout).
func readStdlibFile(name string) (string, error) {
	out, err := exec.Command("go", "list", "-m", "-f", "{{.Dir}}", "github.com/ytnobody/calcium-lang").Output()
	if err != nil {
		return "", err
	}
	dir := strings.TrimSpace(string(out))
	src, err := os.ReadFile(filepath.Join(dir, "pkg", "vm", "stdlib", "core", name))
	if err != nil {
		return "", err
	}
	return string(src), nil
}
