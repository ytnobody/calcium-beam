package main

import "testing"

func TestRemoteModulePrefixStable(t *testing.T) {
	got := remoteModulePrefix("ytnobody", "json")
	if got != "ytnobody_json_" {
		t.Errorf("expected %q, got %q", "ytnobody_json_", got)
	}
}

func TestRemoteModulePrefixSanitizesSuffix(t *testing.T) {
	// A raw-URL import keeps the "-calcium" suffix in Name (see
	// resolve.go's remoteAuthorName) - the prefix still has to be a valid
	// Erlang identifier fragment.
	got := remoteModulePrefix("ytnobody", "json-calcium")
	if got != "ytnobody_json_calcium_" {
		t.Errorf("expected %q, got %q", "ytnobody_json_calcium_", got)
	}
}
