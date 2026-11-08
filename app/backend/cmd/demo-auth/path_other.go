//go:build !windows

package main

import "path/filepath"

func canonicalExistingPath(path string) (string, error) { return filepath.EvalSymlinks(path) }
