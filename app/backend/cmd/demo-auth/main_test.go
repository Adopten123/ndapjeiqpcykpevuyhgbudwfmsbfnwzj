package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"rzdarctica.local/backend/internal/core/demoauth"
)

func TestTwelveDistinctSeedUsers(t *testing.T) {
	now := time.Date(2026, 11, 2, 12, 0, 0, 0, time.UTC)
	users := seedUsers(now)
	if len(users) != 12 {
		t.Fatal("need twelve demo identities")
	}
	seen := map[string]bool{}
	for _, u := range users {
		if seen[u.ID] || u.OrganizationID == "" || len(u.Subjects) == 0 || !u.ValidFrom.Equal(now.Add(-time.Minute)) || !u.ValidUntil.Equal(now.Add(24*time.Hour)) {
			t.Fatal("bad seeded identity")
		}
		seen[u.ID] = true
	}
}

func TestPrivatePathsRejectEscapeExistingAndSymlink(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git required for ignored-path guard")
	}
	root := t.TempDir()
	if out, err := exec.Command("git", "-C", root, "init", "--quiet").CombinedOutput(); err != nil {
		t.Fatalf("init: %s %v", out, err)
	}
	if err := os.WriteFile(filepath.Join(root, ".gitignore"), []byte(".local/\n"), 0600); err != nil {
		t.Fatal(err)
	}
	for _, candidate := range []string{"", "auth.json", ".local/../../escape.json", filepath.Join(t.TempDir(), "outside.json")} {
		if _, err := privatePath(root, candidate); err == nil {
			t.Fatalf("unsafe path accepted: %s", candidate)
		}
	}
	path, err := privatePath(root, ".local/auth.private.json")
	if err != nil {
		resolved, detail := resolvedPath(filepath.Join(root, ".local", "auth.private.json"))
		t.Fatalf("private path: %v; resolved %s: %v", err, resolved, detail)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte("unchanged"), 0600); err != nil {
		t.Fatal(err)
	}
	if _, err := privatePath(root, path); err == nil {
		t.Fatal("existing file accepted")
	}
	data, _ := os.ReadFile(path)
	if string(data) != "unchanged" {
		t.Fatal("guard modified existing secret")
	}
	outside := t.TempDir()
	link := filepath.Join(root, ".local", "escape")
	if err := os.Symlink(outside, link); err == nil {
		if _, err := privatePath(root, filepath.Join(link, "secret.json")); err == nil {
			t.Fatal("symlink escaped workspace")
		}
	}
}

func TestGeneratorCreatesPrivateConfigWithoutPrintingKeysAndNeverOverwrites(t *testing.T) {
	if _, err := exec.LookPath("git"); err != nil {
		t.Skip("git required for ignored-path guard")
	}
	root := t.TempDir()
	if _, err := exec.Command("git", "-C", root, "init", "--quiet").CombinedOutput(); err != nil {
		t.Fatal("git init failed")
	}
	if err := os.WriteFile(filepath.Join(root, ".gitignore"), []byte(".local/\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(root, "app", "backend"), 0700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(root, "app", "backend", "go.mod"), []byte("module test.local/demo\n"), 0600); err != nil {
		t.Fatal(err)
	}
	t.Chdir(root)
	var stdout bytes.Buffer
	if err := run([]string{"-valid-for", "168h"}, &stdout); err != nil {
		t.Fatal(err)
	}
	configPath := filepath.Join(root, ".local", "approval-auth.private.json")
	credentialsPath := filepath.Join(root, ".local", "approval-credentials.private.json")
	configData, err := os.ReadFile(configPath)
	if err != nil {
		t.Fatal(err)
	}
	credentialsData, err := os.ReadFile(credentialsPath)
	if err != nil {
		t.Fatal(err)
	}
	var config struct {
		Scope string `json:"scope"`
		Users []struct {
			demoauth.User
			TokenSHA256 string `json:"token_sha256"`
		} `json:"users"`
	}
	var credentials struct {
		Users []struct {
			ID        string `json:"id"`
			AccessKey string `json:"access_key"`
		} `json:"users"`
	}
	if json.Unmarshal(configData, &config) != nil || json.Unmarshal(credentialsData, &credentials) != nil || len(config.Users) != 12 || len(credentials.Users) != 12 {
		t.Fatal("invalid generated files")
	}
	if _, err := demoauth.Load(configPath); err != nil {
		t.Fatal(err)
	}
	for i, entry := range credentials.Users {
		if len(entry.AccessKey) != 43 || strings.Contains(stdout.String(), entry.AccessKey) || bytes.Contains(configData, []byte(entry.AccessKey)) {
			t.Fatal("plaintext credential leaked or wrong entropy encoding")
		}
		hash := sha256.Sum256([]byte(entry.AccessKey))
		if config.Users[i].ID != entry.ID || config.Users[i].TokenSHA256 != hex.EncodeToString(hash[:]) || config.Users[i].ValidUntil.Sub(config.Users[i].ValidFrom) != 168*time.Hour+time.Minute {
			t.Fatal("generated identity/hash/validity mismatch")
		}
	}
	if len(strings.Split(strings.TrimSpace(stdout.String()), "\n")) != 2 {
		t.Fatal("stdout must contain only two paths")
	}
	stdout.Reset()
	if err := run(nil, &stdout); err == nil || stdout.Len() != 0 {
		t.Fatal("generator overwrote or printed existing credentials")
	}
	after, _ := os.ReadFile(configPath)
	if !bytes.Equal(configData, after) {
		t.Fatal("existing config changed")
	}
	after, _ = os.ReadFile(credentialsPath)
	if !bytes.Equal(credentialsData, after) {
		t.Fatal("existing keys changed")
	}
	for _, value := range []string{"0", "-1h", "169h", "invalid"} {
		if err := run([]string{"-valid-for", value}, &stdout); err == nil {
			t.Fatal("invalid key validity accepted")
		}
	}
}
