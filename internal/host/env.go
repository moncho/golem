package host

import "os"

// Env returns the allowlisted variables of this process as KEY=value.
func Env() []string {
	var env []string
	for _, key := range []string{"PATH", "HOME", "USER", "SHELL", "TMPDIR", "LANG", "LC_ALL", "TERM"} {
		if v, ok := os.LookupEnv(key); ok {
			env = append(env, key+"="+v)
		}
	}
	return env
}
