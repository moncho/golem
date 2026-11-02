package herma

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
)

// bindingFile is what `herma project bind` writes at a checkout's root.
const bindingFile = ".herma-project.json"

// DiscoverProject returns the project ID bound to the workspace: the nearest
// .herma-project.json at or above the directory, stopping at the first
// directory that holds a .git entry, as the herma CLI does. No binding gives
// "" and no error; a malformed binding is an error so a project scope is never
// silently dropped.
func DiscoverProject(workspace string) (string, error) {
	dir, err := filepath.Abs(workspace)
	if err != nil {
		return "", err
	}
	for {
		b, err := os.ReadFile(filepath.Join(dir, bindingFile))
		if err == nil {
			var binding struct {
				ProjectID string `json:"project_id"`
			}
			if json.Unmarshal(b, &binding) != nil || binding.ProjectID == "" {
				return "", fmt.Errorf("malformed herma binding in %s", dir)
			}
			return binding.ProjectID, nil
		}
		if !errors.Is(err, os.ErrNotExist) {
			return "", err
		}
		if _, err := os.Lstat(filepath.Join(dir, ".git")); err == nil {
			return "", nil
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", nil
		}
		dir = parent
	}
}
