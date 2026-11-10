package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ytnobody/calcium-lang/pkg/ast"
	"github.com/ytnobody/calcium-lang/pkg/lexer"
	"github.com/ytnobody/calcium-lang/pkg/parser"
)

func parseSrc(t *testing.T, src string) *ast.Program {
	t.Helper()
	l := lexer.New(src)
	p := parser.New(l)
	program := p.ParseProgram()
	if errs := p.Errors(); len(errs) > 0 {
		t.Fatalf("parse errors: %v", errs)
	}
	return program
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatal(err)
	}
}

func TestResolveModulesSingleLocalFile(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "greet.ca"), `func hello(name) = concat("Hello, ", name);`)
	entryPath := filepath.Join(dir, "main.ca")
	entry := parseSrc(t, `use greet; greet.hello("world") !> io.say;`)

	mods, err := resolveModules(entryPath, entry)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(mods) != 1 {
		t.Fatalf("expected 1 linked module, got %d", len(mods))
	}
	if mods[0].Alias != "greet" {
		t.Errorf("expected alias %q, got %q", "greet", mods[0].Alias)
	}
}

func TestResolveModulesNestedModCa(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "utils", "helper", "mod.ca"), `func double(n) = n * 2;`)
	entryPath := filepath.Join(dir, "main.ca")
	entry := parseSrc(t, `use utils.helper; utils.helper.double(5) !> io.say;`)

	mods, err := resolveModules(entryPath, entry)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(mods) != 1 || mods[0].Alias != "helper" {
		t.Fatalf("expected one module aliased %q, got %+v", "helper", mods)
	}
}

func TestResolveModulesSkipsCoreStdlib(t *testing.T) {
	dir := t.TempDir()
	entryPath := filepath.Join(dir, "main.ca")
	entry := parseSrc(t, `use core.io!; "hi" !> io.say;`)

	mods, err := resolveModules(entryPath, entry)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(mods) != 0 {
		t.Fatalf("expected core.* to be skipped entirely, got %+v", mods)
	}
}

func TestResolveModulesDedupesSharedImport(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "shared.ca"), `func id(x) = x;`)
	writeFile(t, filepath.Join(dir, "a.ca"), `use shared;`)
	writeFile(t, filepath.Join(dir, "b.ca"), `use shared;`)
	entryPath := filepath.Join(dir, "main.ca")
	entry := parseSrc(t, `use a; use b;`)

	mods, err := resolveModules(entryPath, entry)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	// a.ca, b.ca, and shared.ca - shared.ca must appear exactly once even
	// though both a.ca and b.ca import it.
	count := 0
	for _, m := range mods {
		if m.Alias == "shared" {
			count++
		}
	}
	if count != 1 {
		t.Fatalf("expected shared.ca to be linked exactly once, got %d times in %+v", count, mods)
	}
	if len(mods) != 3 {
		t.Fatalf("expected 3 linked modules (a, b, shared), got %d: %+v", len(mods), mods)
	}
}

func TestResolveModulesDetectsCycle(t *testing.T) {
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "a.ca"), `use b;`)
	writeFile(t, filepath.Join(dir, "b.ca"), `use a;`)
	entryPath := filepath.Join(dir, "main.ca")
	entry := parseSrc(t, `use a;`)

	_, err := resolveModules(entryPath, entry)
	if err == nil || !strings.Contains(err.Error(), "import cycle") {
		t.Fatalf("expected an import cycle error, got %v", err)
	}
}

func TestResolveModulesMissingFile(t *testing.T) {
	dir := t.TempDir()
	entryPath := filepath.Join(dir, "main.ca")
	entry := parseSrc(t, `use doesnotexist;`)

	_, err := resolveModules(entryPath, entry)
	if err == nil {
		t.Fatal("expected an error for a module that doesn't exist")
	}
	if !strings.Contains(err.Error(), "doesnotexist.ca") || !strings.Contains(err.Error(), filepath.Join("doesnotexist", "mod.ca")) {
		t.Errorf("expected the error to name both tried paths, got: %v", err)
	}
}
