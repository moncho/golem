package host

import (
	"crypto/sha256"
	_ "embed"
	"encoding/hex"
	"os"
	"path/filepath"
)

// bundle is the application host built by `make bundle`.
//
//go:embed host.mjs
var bundle []byte

// Extract writes the embedded host to <state>/host/host-<hash>.mjs, mode 0600,
// unless that file already exists, and returns its path.
func Extract(state string) (string, error) {
	sum := sha256.Sum256(bundle)
	dir := filepath.Join(state, "host")
	path := filepath.Join(dir, "host-"+hex.EncodeToString(sum[:])[:16]+".mjs")
	if _, err := os.Stat(path); err == nil {
		return path, nil
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", err
	}
	tmp, err := os.CreateTemp(dir, ".host-*")
	if err != nil {
		return "", err
	}
	defer os.Remove(tmp.Name())
	if _, err := tmp.Write(bundle); err != nil {
		tmp.Close()
		return "", err
	}
	if err := tmp.Chmod(0o600); err != nil {
		tmp.Close()
		return "", err
	}
	if err := tmp.Close(); err != nil {
		return "", err
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return "", err
	}
	return path, nil
}
