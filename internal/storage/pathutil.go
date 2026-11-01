// SPDX-License-Identifier: MIT

package storage

import (
	"fmt"
	"path/filepath"
	"strings"
)

// ValidatePath resolves targetPath relative to baseDir and ensures the result
// is contained within baseDir. It returns the cleaned absolute path on success,
// or an error if the path escapes the base directory.
//
// targetPath may be absolute or relative:
//   - Absolute paths are checked directly against baseDir.
//   - Relative paths are joined with baseDir first.
//
// This is the canonical path validation function for all file access in lalmax-nvr.
func ValidatePath(baseDir, targetPath string) (string, error) {
	baseDirAbs, err := filepath.Abs(baseDir)
	if err != nil {
		return "", fmt.Errorf("pathutil: failed to resolve base directory %q: %w", baseDir, err)
	}

	// If targetPath is already absolute, use it directly; otherwise join with baseDir.
	var resolvedPath string
	if filepath.IsAbs(targetPath) {
		resolvedPath = filepath.Clean(targetPath)
	} else {
		resolvedPath = filepath.Join(baseDirAbs, targetPath)
	}

	// Canonicalize to absolute path. Keep this lexical form for the return
	// value so callers can compare it with the root they passed in.
	absPath, err := filepath.Abs(resolvedPath)
	if err != nil {
		return "", fmt.Errorf("pathutil: failed to resolve path %q: %w", targetPath, err)
	}

	// Compare symlink-resolved paths. A missing file still has to follow
	// symlinks in its existing parents, or /var vs /private/var looks like
	// an escape on macOS.
	baseCanon, err := canonicalPath(baseDirAbs)
	if err != nil {
		return "", fmt.Errorf("pathutil: failed to resolve base directory %q: %w", baseDir, err)
	}
	targetCanon, err := canonicalPath(absPath)
	if err != nil {
		return "", fmt.Errorf("pathutil: failed to resolve path %q: %w", targetPath, err)
	}

	rel, err := filepath.Rel(baseCanon, targetCanon)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return "", fmt.Errorf("pathutil: path %q resolves outside storage root %q", targetPath, baseDirAbs)
	}

	return absPath, nil
}

// canonicalPath resolves symlinks through the nearest existing ancestor.
// EvalSymlinks fails for a path that has not been created yet.
func canonicalPath(abs string) (string, error) {
	if resolved, err := filepath.EvalSymlinks(abs); err == nil {
		return resolved, nil
	}
	parent := filepath.Dir(abs)
	if parent == abs {
		return abs, nil
	}
	resolvedParent, err := canonicalPath(parent)
	if err != nil {
		return "", err
	}
	return filepath.Join(resolvedParent, filepath.Base(abs)), nil
}
