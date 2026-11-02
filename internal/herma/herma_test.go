package herma

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"
	"time"
)

func TestPrinciplesRefreshAndRetainLastReadOnOutage(t *testing.T) {
	version, offline, reads := "first", false, 0
	clock := time.Unix(0, 0)
	var warnings []string
	run := func(_ context.Context, args []string) (string, error) {
		reads++
		scope := "global"
		if i := slices.Index(args, "--project"); i >= 0 {
			scope = args[i+1]
			if offline {
				return "", errors.New("offline")
			}
		}
		return scope + ": " + version + "\n", nil
	}
	p, err := LoadPrinciples(context.Background(), run, "golem-project", 5*time.Second,
		func() time.Time { return clock }, func(m string) { warnings = append(warnings, m) })
	if err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	current := func() string {
		t.Helper()
		text, err := p.Current(ctx)
		if err != nil {
			t.Fatal(err)
		}
		return text
	}
	if got := current(); got != "global: first\n\ngolem-project: first" {
		t.Fatalf("got %q", got)
	}
	version = "second"
	if got := current(); !strings.Contains(got, "first") {
		t.Fatalf("a read inside the TTL must come from the cache: %q", got)
	}
	if reads != 2 {
		t.Fatalf("reads %d", reads)
	}
	clock = clock.Add(5001 * time.Millisecond)
	if got := current(); !strings.Contains(got, "second") {
		t.Fatalf("got %q", got)
	}
	offline, version = true, "partial"
	clock = time.Unix(11, 0)
	if got := current(); got != "global: second\n\ngolem-project: second" {
		t.Fatalf("an incomplete read must keep the last complete one: %q", got)
	}
	clock = time.Unix(17, 0)
	current()
	if len(warnings) != 1 || warnings[0] != "Golem: Herma is unavailable; using the last successfully loaded principles." {
		t.Fatalf("warnings %q", warnings)
	}
}

func TestLoadPrinciplesRequiresOneSuccessfulRead(t *testing.T) {
	var calls [][]string
	run := func(_ context.Context, args []string) (string, error) {
		calls = append(calls, args)
		return "", errors.New("offline")
	}
	_, err := LoadPrinciples(context.Background(), run, "", time.Second, time.Now, func(string) { t.Fatal("no warning before a load") })
	if err == nil || err.Error() != "Could not load accepted Herma principles. Check the Herma connection and agent credentials." {
		t.Fatalf("err %v", err)
	}
	if len(calls) != 1 || !slices.Equal(calls[0], []string{"principles"}) {
		t.Fatalf("calls %q", calls)
	}
}

func TestMemoryScopesRecallAndProposesWithStableRequestIDs(t *testing.T) {
	var calls [][]string
	m := NewMemory(func(_ context.Context, args []string) (string, error) {
		calls = append(calls, args)
		return "{}", nil
	}, "rec_project")
	ctx := context.Background()
	if out, err := m.Recall(ctx, "architecture"); err != nil || out != "{}" {
		t.Fatalf("recall %q %v", out, err)
	}
	if _, err := m.Get(ctx, "rec_fact"); err != nil {
		t.Fatal(err)
	}
	for range 2 {
		if _, err := m.Propose(ctx, "Decision", "Evidence", "golem:test", "same-key"); err != nil {
			t.Fatal(err)
		}
	}
	want := [][]string{
		{"recall", "architecture", "--project", "rec_project", "--limit", "8", "--max-bytes", "12000"},
		{"get", "rec_fact"},
		{"create", "--kind", "knowledge", "--status", "proposed", "--title", "Decision", "--body", "Evidence",
			"--sources", "golem:test", "--request-id", "same-key", "--project", "rec_project"},
	}
	want = append(want, want[2])
	if !slices.EqualFunc(calls, want, slices.Equal) {
		t.Fatalf("calls\n%q\nwant\n%q", calls, want)
	}
	if slices.Contains(calls[0], "--include-proposed") {
		t.Fatal("recall must not include proposals")
	}

	failing := NewMemory(func(context.Context, []string) (string, error) { return "", errors.New("offline") }, "")
	_, err := failing.Recall(ctx, "x")
	if err == nil || err.Error() != "Herma memory is unavailable. A proposed write may need retrying with the same request ID." {
		t.Fatalf("err %v", err)
	}
	if errors.Unwrap(err) == nil {
		t.Fatal("the cause must stay available for logging")
	}
}

func TestCLIArgumentsPrefixURL(t *testing.T) {
	got := arguments("https://herma.example", []string{"recall", "q"})
	want := []string{"--url", "https://herma.example", "recall", "q"}
	if strings.Join(got, " ") != strings.Join(want, " ") {
		t.Fatalf("arguments = %v, want %v", got, want)
	}
}

func TestDiscoverProjectFromWorkspace(t *testing.T) {
	root := t.TempDir()
	write := func(dir, name, body string) {
		t.Helper()
		if err := os.MkdirAll(dir, 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(filepath.Join(dir, name), []byte(body), 0o644); err != nil {
			t.Fatal(err)
		}
	}
	// A bound checkout: the binding at the repo root is found from a nested workspace.
	repo := filepath.Join(root, "repo")
	write(repo, ".herma-project.json", `{"project_id":"rec_repo","max_bytes":10000}`)
	if err := os.MkdirAll(filepath.Join(repo, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if got, err := DiscoverProject(filepath.Join(repo, "cmd", "deep")); err != nil || got != "rec_repo" {
		t.Fatalf("nested workspace: %q %v", got, err)
	}
	// A binding above the Git boundary is never used.
	write(root, ".herma-project.json", `{"project_id":"rec_outside"}`)
	other := filepath.Join(root, "other")
	if err := os.MkdirAll(filepath.Join(other, ".git"), 0o755); err != nil {
		t.Fatal(err)
	}
	if got, err := DiscoverProject(filepath.Join(other, "src")); err != nil || got != "" {
		t.Fatalf("beyond the boundary: %q %v", got, err)
	}
	// A malformed binding is an error, not a silent global scope.
	bad := filepath.Join(root, "bad")
	write(bad, ".herma-project.json", `{not json`)
	if _, err := DiscoverProject(bad); err == nil {
		t.Fatal("malformed binding accepted")
	}
}

func TestEnvironmentCarriesTheConfiguredIdentity(t *testing.T) {
	t.Setenv("HERMA_IDENTITY", "ambient")
	if env := environment("golem-agent"); !slices.Contains(env, "HERMA_IDENTITY=golem-agent") || slices.Contains(env, "HERMA_IDENTITY=ambient") {
		t.Fatalf("configured identity must replace the ambient one: %v", env)
	}
	if env := environment(""); !slices.Contains(env, "HERMA_IDENTITY=ambient") {
		t.Fatalf("without a configured identity the ambient one stays: %v", env)
	}
}
