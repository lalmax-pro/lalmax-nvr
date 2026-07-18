package api

import (
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
)

type setupDirectoryEntry struct {
	Name string `json:"name"`
	Path string `json:"path"`
}

// handleSetupDirectories provides a small server-side directory browser for
// the first-run page. A browser's native directory picker cannot expose the
// server's absolute path, so this endpoint returns directories visible to the
// NVR process instead.
func (h *Handler) handleSetupDirectories(w http.ResponseWriter, r *http.Request) {
	if h.config != nil && strings.TrimSpace(h.config.Auth.PasswordHash) != "" {
		writeError(w, http.StatusConflict, "setup already completed")
		return
	}

	requested := strings.TrimSpace(r.URL.Query().Get("path"))
	dir, err := setupBrowsePath(requested)
	if err != nil {
		writeError(w, http.StatusBadRequest, err.Error())
		return
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		writeError(w, http.StatusBadRequest, "directory cannot be read")
		return
	}

	items := make([]setupDirectoryEntry, 0, len(entries))
	for _, entry := range entries {
		if !entry.IsDir() || strings.HasPrefix(entry.Name(), ".") {
			continue
		}
		items = append(items, setupDirectoryEntry{
			Name: entry.Name(),
			Path: filepath.Join(dir, entry.Name()),
		})
	}
	sort.Slice(items, func(i, j int) bool {
		return strings.ToLower(items[i].Name) < strings.ToLower(items[j].Name)
	})

	parent := ""
	if candidate := filepath.Dir(dir); candidate != dir && setupBrowseAllowed(candidate) {
		parent = candidate
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"path":    dir,
		"parent":  parent,
		"entries": items,
	})
}

func setupBrowsePath(requested string) (string, error) {
	if requested == "" {
		cwd, err := os.Getwd()
		if err != nil {
			return "", err
		}
		return filepath.Abs(cwd)
	}

	path, err := filepath.Abs(filepath.Clean(requested))
	if err != nil {
		return "", err
	}
	info, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	if !info.IsDir() {
		return "", os.ErrInvalid
	}
	if !setupBrowseAllowed(path) {
		return "", os.ErrPermission
	}
	return path, nil
}

func setupBrowseRoots() []string {
	roots := make([]string, 0, 4)
	add := func(path string) {
		if strings.TrimSpace(path) == "" {
			return
		}
		abs, err := filepath.Abs(filepath.Clean(path))
		if err != nil {
			return
		}
		for _, existing := range roots {
			if existing == abs {
				return
			}
		}
		if info, err := os.Stat(abs); err == nil && info.IsDir() {
			roots = append(roots, abs)
		}
	}

	if cwd, err := os.Getwd(); err == nil {
		add(cwd)
	}
	add(os.Getenv("NVR_DATA_DIR"))
	add("/data")
	add("/var/lib/lalmax-nvr")
	return roots
}

func setupBrowseAllowed(path string) bool {
	path, err := filepath.Abs(filepath.Clean(path))
	if err != nil {
		return false
	}
	for _, root := range setupBrowseRoots() {
		rel, err := filepath.Rel(root, path)
		if err == nil && rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator)) && !filepath.IsAbs(rel) {
			return true
		}
	}
	return false
}
