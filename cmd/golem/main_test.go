package main

import (
	"bytes"
	"io"
	"path/filepath"
	"strings"
	"testing"
)

func TestUnknownSubcommandPrintsUsage(t *testing.T) {
	var out, errOut bytes.Buffer
	if code := run([]string{"bogus"}, strings.NewReader(""), &out, &errOut); code != 2 {
		t.Fatalf("code %d", code)
	}
	if !strings.Contains(errOut.String(), "usage: golem [serve|attach|watch|status|demo|service|executor]") {
		t.Fatal(errOut.String())
	}
}

func TestExecutorStdioAnnouncesReady(t *testing.T) {
	dir := t.TempDir()
	var out bytes.Buffer
	if code := run([]string{"executor", "--stdio", "--workspace", dir, "--state", filepath.Join(dir, "state")}, strings.NewReader(""), &out, io.Discard); code != 0 {
		t.Fatalf("code %d", code)
	}
	if strings.TrimSpace(out.String()) != `{"ready":true}` {
		t.Fatal(out.String())
	}
}

// Flags without a subcommand belong to serve: an unknown serve flag is
// reported by serve's flag parser, not as an unknown subcommand.
func TestFlagsWithoutSubcommandRunServe(t *testing.T) {
	var out, errOut bytes.Buffer
	if code := run([]string{"--state", t.TempDir(), "--bogus"}, strings.NewReader(""), &out, &errOut); code != 2 {
		t.Fatalf("code %d", code)
	}
	if !strings.Contains(errOut.String(), "flag provided but not defined: -bogus") || strings.Contains(errOut.String(), "usage: golem [") {
		t.Fatal(errOut.String())
	}
}
