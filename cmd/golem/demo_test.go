package main

import (
	"bytes"
	"context"
	"os/exec"
	"strings"
	"testing"
)

func TestDemoPasses(t *testing.T) {
	if _, err := exec.LookPath("node"); err != nil {
		t.Skip("node is not on PATH")
	}
	var stdout, stderr bytes.Buffer
	if code := runDemo(context.Background(), &stdout, &stderr); code != 0 {
		t.Fatalf("exit %d\nstdout:\n%s\nstderr:\n%s", code, stdout.String(), stderr.String())
	}
	const pass = "PASS: Pi resumed stored work; an independent task finished; the followup stopped the old command; the Router delivered verified replacement output.\n"
	if !strings.HasSuffix(stdout.String(), pass) {
		t.Fatalf("stdout:\n%s", stdout.String())
	}
}
