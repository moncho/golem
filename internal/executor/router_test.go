package executor

import "testing"

func TestRouterResolvesEnvironments(t *testing.T) {
	local := localForTest(t)
	remote := NewRemote("http://127.0.0.1:1", "t")
	r := NewRouter(map[string]Backend{"local": local, "remote": remote})
	if b, ok := r.Backend("local"); !ok || b != Backend(local) {
		t.Fatal("local")
	}
	if b, ok := r.Backend("remote"); !ok || b != Backend(remote) {
		t.Fatal("remote")
	}
	if _, ok := r.Backend("other"); ok {
		t.Fatal("other")
	}
}
