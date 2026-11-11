package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// TestLocalModuleCLIEndToEnd exercises the full calciumbeam CLI path (not
// just the codegen package directly): resolve -> compile -> erlc -> erl,
// for a real two-file project-local module program on disk (D19).
func TestLocalModuleCLIEndToEnd(t *testing.T) {
	erlcPath, err := exec.LookPath("erlc")
	if err != nil {
		t.Skip("erlc not found, skipping end-to-end BEAM compile check")
	}
	erlPath, err := exec.LookPath("erl")
	if err != nil {
		t.Skip("erl not found, skipping end-to-end BEAM compile check")
	}

	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "main.ca"), `use core.io!;
use greet;

greet.hello("world") !> io.say;
`)
	writeFile(t, filepath.Join(dir, "greet.ca"), `func hello(name) = concat("Hello, ", name);`)

	entryPath := filepath.Join(dir, "main.ca")
	if err := run(entryPath); err != nil {
		t.Fatalf("calciumbeam run: %v", err)
	}
	// run() writes "<module>.erl" into the current working directory, not
	// entryPath's directory (same convention cmd/calciumbeam/main.go's
	// existing output path already uses) - move it into a build dir
	// alongside erl/'s support modules.
	genErl := "calcium_main.erl"
	t.Cleanup(func() { os.Remove(genErl) })

	erlDir, err := filepath.Abs("../../erl")
	if err != nil {
		t.Fatal(err)
	}
	buildDir := t.TempDir()
	args := []string{"-o", buildDir, filepath.Join(erlDir, "calcium_builtins.erl"), filepath.Join(erlDir, "calcium_io_native.erl"), genErl}
	if out, err := exec.Command(erlcPath, args...).CombinedOutput(); err != nil {
		t.Fatalf("erlc failed: %v\n%s", err, out)
	}

	out, err := exec.Command(erlPath, "-noshell", "-pa", buildDir, "-eval", "calcium_main:main(), halt().").CombinedOutput()
	if err != nil {
		t.Fatalf("erl run failed: %v\n%s", err, out)
	}
	if strings.TrimSpace(string(out)) != "Hello, world" {
		t.Errorf("expected %q, got %q", "Hello, world", strings.TrimSpace(string(out)))
	}
}

// TestRemoteModuleCLIEndToEnd exercises the full calciumbeam CLI path for a
// Boneyard/GitHub-style import (`use author/module;`) already fetched onto
// disk - proving D19's LinkedModule/WithLocalModules machinery (D19)
// handles a remote-sourced module identically to a local one, per the
// design/implementation-plan comments on issue #6. No network access is
// involved: the module is placed directly into calcium_modules/, exactly
// where `bone add` would have left it.
func TestRemoteModuleCLIEndToEnd(t *testing.T) {
	erlcPath, err := exec.LookPath("erlc")
	if err != nil {
		t.Skip("erlc not found, skipping end-to-end BEAM compile check")
	}
	erlPath, err := exec.LookPath("erl")
	if err != nil {
		t.Skip("erl not found, skipping end-to-end BEAM compile check")
	}

	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "main.ca"), `use core.io!;
use ytnobody/greet;

greet.hello("world") !> io.say;
`)
	writeFile(t, filepath.Join(dir, "calcium_modules", "ytnobody", "greet", "mod.ca"), `func hello(name) = concat("Hi, ", name);`)

	entryPath := filepath.Join(dir, "main.ca")
	if err := run(entryPath); err != nil {
		t.Fatalf("calciumbeam run: %v", err)
	}
	genErl := "calcium_main.erl"
	t.Cleanup(func() { os.Remove(genErl) })

	erlDir, err := filepath.Abs("../../erl")
	if err != nil {
		t.Fatal(err)
	}
	buildDir := t.TempDir()
	args := []string{"-o", buildDir, filepath.Join(erlDir, "calcium_builtins.erl"), filepath.Join(erlDir, "calcium_io_native.erl"), genErl}
	if out, err := exec.Command(erlcPath, args...).CombinedOutput(); err != nil {
		t.Fatalf("erlc failed: %v\n%s", err, out)
	}

	out, err := exec.Command(erlPath, "-noshell", "-pa", buildDir, "-eval", "calcium_main:main(), halt().").CombinedOutput()
	if err != nil {
		t.Fatalf("erl run failed: %v\n%s", err, out)
	}
	if strings.TrimSpace(string(out)) != "Hi, world" {
		t.Errorf("expected %q, got %q", "Hi, world", strings.TrimSpace(string(out)))
	}
}
