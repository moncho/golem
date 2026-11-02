package executor

import (
	"crypto/subtle"
	"encoding/json"
	"errors"
	"net/http"
	"strconv"
	"strings"
)

// Handler exposes an execution endpoint. The caller supplies TLS or a private tunnel.
func Handler(backend Backend, token string) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		fail := func(code int, name, message string) {
			w.WriteHeader(code)
			_ = json.NewEncoder(w).Encode(map[string]any{"error": map[string]string{"code": name, "message": message}})
		}
		if token == "" || subtle.ConstantTimeCompare([]byte(r.Header.Get("Authorization")), []byte("Bearer "+token)) != 1 {
			fail(401, "unauthorized", "Authentication required.")
			return
		}
		parts := strings.Split(strings.Trim(r.URL.Path, "/"), "/")
		if len(parts) < 2 || len(parts) > 3 || parts[0] != "jobs" || !validID.MatchString(parts[1]) {
			fail(404, "not_found", "Unknown endpoint.")
			return
		}
		id := parts[1]
		var result any
		var err error
		switch {
		case len(parts) == 2 && r.Method == "PUT":
			var s Spec
			if json.NewDecoder(http.MaxBytesReader(w, r.Body, 1<<20)).Decode(&s) != nil || s.ID != id {
				fail(400, "invalid_request", "Invalid command request.")
				return
			}
			result, err = backend.Start(r.Context(), s)
		case len(parts) == 2 && r.Method == "GET":
			result, err = backend.Poll(r.Context(), id)
		case len(parts) == 3 && parts[2] == "stop" && r.Method == "POST":
			err = backend.Stop(r.Context(), id)
			result = map[string]bool{"stopped": err == nil}
		case len(parts) == 3 && parts[2] == "output" && r.Method == "GET":
			offset, e1 := strconv.ParseInt(r.URL.Query().Get("offset"), 10, 64)
			limit, e2 := strconv.Atoi(r.URL.Query().Get("limit"))
			if e1 != nil || e2 != nil {
				fail(400, "invalid_request", "Invalid output range.")
				return
			}
			result, err = backend.Output(r.Context(), id, offset, limit)
		default:
			fail(405, "method_not_allowed", "Unsupported operation.")
			return
		}
		if errors.Is(err, ErrUnavailable) {
			fail(503, "unavailable", "Execution environment unavailable.")
			return
		}
		if errors.Is(err, ErrOutputExpired) {
			fail(410, "expired", "Output no longer retained.")
			return
		}
		if err != nil {
			fail(400, "execution_failed", "Execution request could not be completed.")
			return
		}
		_ = json.NewEncoder(w).Encode(result)
	})
}
