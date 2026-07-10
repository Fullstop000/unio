package driver

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/Fullstop000/unio/errs"
)

func TestResolveExecutableFound(t *testing.T) {
	// Create a temp executable and put its dir on PATH.
	dir := t.TempDir()
	bin := filepath.Join(dir, "unio-fake-agent")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir+string(os.PathListSeparator)+os.Getenv("PATH"))

	path, aerr := ResolveExecutable(AgentSpec{ExecutablePath: "unio-fake-agent"})
	if aerr != nil {
		t.Fatalf("expected resolution, got %v", aerr)
	}
	if path == "" {
		t.Fatal("expected a non-empty resolved path")
	}
	if !IsInstalled(AgentSpec{ExecutablePath: "unio-fake-agent"}) {
		t.Fatal("IsInstalled should be true")
	}
}

func TestResolveExecutableAltCommand(t *testing.T) {
	dir := t.TempDir()
	bin := filepath.Join(dir, "traecli")
	if err := os.WriteFile(bin, []byte("#!/bin/sh\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	t.Setenv("PATH", dir)

	// Primary "trae-cli" missing, alt "traecli" present.
	path, aerr := ResolveExecutable(AgentSpec{ExecutablePath: "trae-cli", AltCommands: []string{"traecli"}})
	if aerr != nil {
		t.Fatalf("alt command should resolve, got %v", aerr)
	}
	if filepath.Base(path) != "traecli" {
		t.Fatalf("expected traecli, got %s", path)
	}
}

func TestResolveExecutableNotInstalled(t *testing.T) {
	t.Setenv("PATH", t.TempDir()) // empty dir → nothing resolves

	_, aerr := ResolveExecutable(AgentSpec{ExecutablePath: "definitely-not-a-real-binary-xyz"})
	if aerr == nil {
		t.Fatal("expected a not_installed error")
	}
	if aerr.Kind != errs.KindNotInstalled {
		t.Fatalf("expected not_installed kind, got %s", aerr.Kind)
	}
	// Contract: hosts can branch on the category via errors.Is.
	var target error = aerr
	if kind, ok := errs.KindOf(target); !ok || kind != ErrNotInstalled {
		t.Fatalf("KindOf should report not_installed, got %s ok=%v", kind, ok)
	}
	// The message names the missing command so a host can tell the user.
	if !strings.Contains(aerr.Msg, "definitely-not-a-real-binary-xyz") {
		t.Fatalf("error should name the missing command, got %q", aerr.Msg)
	}
}

func TestResolveExecutableNoneConfigured(t *testing.T) {
	_, aerr := ResolveExecutable(AgentSpec{})
	if aerr == nil || aerr.Kind != errs.KindNotInstalled {
		t.Fatalf("empty spec should be not_installed, got %v", aerr)
	}
}
