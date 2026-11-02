package executor

// Router resolves a named execution environment to its backend.
type Router struct{ backends map[string]Backend }

func NewRouter(backends map[string]Backend) *Router {
	copied := make(map[string]Backend, len(backends))
	for name, b := range backends {
		copied[name] = b
	}
	return &Router{backends: copied}
}

func (r *Router) Backend(environment string) (Backend, bool) {
	b, ok := r.backends[environment]
	return b, ok
}
