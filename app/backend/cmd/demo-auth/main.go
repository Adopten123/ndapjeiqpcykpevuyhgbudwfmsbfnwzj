// demo-auth создаёт отдельные ключи учебных субъектов, не печатая их в терминал.
package main

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"rzdarctica.local/backend/internal/core/demoauth"
)

func seedUsers(now time.Time) []demoauth.User {
	rows := []struct {
		id, name, role, org string
		subjects            []string
	}{
		{"coordinator.demo", "Координатор учебной цепочки", "coordinator", "COORD", []string{"*"}},
		{"auditor.demo", "Аудитор учебной цепочки", "auditor", "AUDIT", []string{"*"}},
		{"marine.demo", "Представитель морских рейсов", "participant", "SEA", []string{"SERVICE-A", "SERVICE-B"}},
		{"railway.demo", "Представитель железной дороги", "participant", "RAIL", []string{"WAIT-O", "RAIL-P3", "RAIL-P4"}},
		{"port-m.demo", "Представитель порта отправления", "participant", "PORT-M", []string{"PORT-M"}},
		{"port-d.demo", "Представитель порта назначения", "participant", "PORT-D", []string{"PORT-D"}},
		{"last-mile.demo", "Представитель последней мили", "participant", "LAST-MILE", []string{"LAST-MILE"}},
		{"shipper.demo", "Представитель отправителя", "participant", "ACCESS", []string{"ACCESS"}},
		{"recipient-p1.demo", "Получатель партии P1", "participant", "RECIPIENT-P1", []string{"RECIPIENT-P1"}},
		{"recipient-p2.demo", "Получатель партии P2", "participant", "RECIPIENT-P2", []string{"RECIPIENT-P2"}},
		{"recipient-p3.demo", "Получатель партии P3", "participant", "RECIPIENT-P3", []string{"RECIPIENT-P3"}},
		{"recipient-p4.demo", "Получатель партии P4", "participant", "RECIPIENT-P4", []string{"RECIPIENT-P4"}},
	}
	users := make([]demoauth.User, 0, len(rows))
	for _, row := range rows {
		users = append(users, demoauth.User{ID: row.id, Name: row.name, Role: row.role, OrganizationID: row.org, Subjects: row.subjects, ValidFrom: now.Add(-time.Minute).UTC(), ValidUntil: now.Add(24 * time.Hour).UTC()})
	}
	return users
}

func workspace() (string, error) {
	dir, err := os.Getwd()
	if err != nil {
		return "", errors.New("не удалось определить рабочую папку")
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, ".git")); err == nil {
			if _, err := os.Stat(filepath.Join(dir, "app", "backend", "go.mod")); err == nil {
				return canonicalExistingPath(dir)
			}
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", errors.New("запустите генератор внутри репозитория проекта")
		}
		dir = parent
	}
}

func resolvedPath(path string) (string, error) {
	absolute, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	ancestor := absolute
	parts := []string{}
	for {
		_, err := os.Lstat(ancestor)
		if err == nil {
			break
		}
		if !errors.Is(err, os.ErrNotExist) {
			return "", err
		}
		parts = append(parts, filepath.Base(ancestor))
		parent := filepath.Dir(ancestor)
		if parent == ancestor {
			return "", errors.New("не удалось разрешить путь")
		}
		ancestor = parent
	}
	ancestor, err = canonicalExistingPath(ancestor)
	if err != nil {
		return "", err
	}
	for i := len(parts) - 1; i >= 0; i-- {
		ancestor = filepath.Join(ancestor, parts[i])
	}
	return ancestor, nil
}

func privatePath(root, candidate string) (string, error) {
	if strings.TrimSpace(candidate) == "" {
		return "", errors.New("путь выходного файла пуст")
	}
	if !filepath.IsAbs(candidate) {
		candidate = filepath.Join(root, candidate)
	}
	resolved, err := resolvedPath(candidate)
	if err != nil {
		return "", errors.New("не удалось разрешить выходной путь")
	}
	local := filepath.Join(root, ".local")
	relative, err := filepath.Rel(local, resolved)
	if err != nil || relative == "." || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) || filepath.IsAbs(relative) {
		return "", errors.New("секреты разрешены только внутри .local текущего репозитория")
	}
	if err := exec.Command("git", "-C", root, "check-ignore", "--quiet", "--", resolved).Run(); err != nil {
		return "", errors.New("выходной файл должен быть исключён из Git")
	}
	if _, err := os.Lstat(resolved); !errors.Is(err, os.ErrNotExist) {
		return "", errors.New("выходной путь уже занят или недоступен; перезапись запрещена")
	}
	return resolved, nil
}

func run(args []string, stdout io.Writer) error {
	flags := flag.NewFlagSet("demo-auth", flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	output := flags.String("output", ".local/approval-auth.private.json", "")
	credentials := flags.String("credentials", ".local/approval-credentials.private.json", "")
	validFor := flags.Duration("valid-for", 24*time.Hour, "")
	if err := flags.Parse(args); err != nil || flags.NArg() != 0 {
		return errors.New("нужны параметры -output и -credentials с путями внутри .local")
	}
	if *validFor <= 0 || *validFor > 168*time.Hour {
		return errors.New("срок учебных ключей должен быть больше нуля и не более168 часов")
	}
	root, err := workspace()
	if err != nil {
		return err
	}
	configPath, err := privatePath(root, *output)
	if err != nil {
		return err
	}
	credentialsPath, err := privatePath(root, *credentials)
	if err != nil {
		return err
	}
	if strings.EqualFold(configPath, credentialsPath) {
		return errors.New("нужны два разных выходных файла")
	}
	type configEntry struct {
		demoauth.User
		TokenSHA256 string `json:"token_sha256"`
	}
	type keyEntry struct {
		ID        string `json:"id"`
		Name      string `json:"name"`
		AccessKey string `json:"access_key"`
	}
	config := struct {
		Scope string        `json:"scope"`
		Users []configEntry `json:"users"`
	}{Scope: demoauth.Scope}
	keys := struct {
		Scope  string     `json:"scope"`
		Notice string     `json:"notice"`
		Users  []keyEntry `json:"users"`
	}{Scope: demoauth.Scope, Notice: fmt.Sprintf("Только локальные учебные ключи на%s. Не публиковать и не отправлять в Git.", *validFor)}
	now := time.Now()
	for _, user := range seedUsers(now) {
		user.ValidUntil = now.Add(*validFor).UTC()
		var raw [32]byte
		if _, err := rand.Read(raw[:]); err != nil {
			return errors.New("не удалось создать случайные учебные ключи")
		}
		key := base64.RawURLEncoding.EncodeToString(raw[:])
		hash := sha256.Sum256([]byte(key))
		config.Users = append(config.Users, configEntry{User: user, TokenSHA256: hex.EncodeToString(hash[:])})
		keys.Users = append(keys.Users, keyEntry{ID: user.ID, Name: user.Name, AccessKey: key})
	}
	configData, err := json.MarshalIndent(config, "", "  ")
	if err != nil {
		return errors.New("не удалось подготовить конфигурацию")
	}
	keysData, err := json.MarshalIndent(keys, "", "  ")
	if err != nil {
		return errors.New("не удалось подготовить учебные реквизиты")
	}
	for _, path := range []string{configPath, credentialsPath} {
		if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
			return errors.New("не удалось создать закрытую локальную папку")
		}
		checked, err := privatePath(root, path)
		if err != nil || checked != path {
			return errors.New("выходной путь изменился; запись отменена")
		}
	}
	configFile, err := os.OpenFile(configPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		return errors.New("не удалось создать конфигурацию без перезаписи")
	}
	keysFile, err := os.OpenFile(credentialsPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0600)
	if err != nil {
		configFile.Close()
		os.Remove(configPath)
		return errors.New("не удалось создать реквизиты без перезаписи")
	}
	ok := false
	defer func() {
		configFile.Close()
		keysFile.Close()
		if !ok {
			os.Remove(configPath)
			os.Remove(credentialsPath)
		}
	}()
	if _, err = configFile.Write(append(configData, '\n')); err != nil {
		return errors.New("ошибка записи конфигурации")
	}
	if _, err = keysFile.Write(append(keysData, '\n')); err != nil {
		return errors.New("ошибка записи реквизитов")
	}
	if err = configFile.Sync(); err != nil {
		return errors.New("ошибка сохранения конфигурации")
	}
	if err = keysFile.Sync(); err != nil {
		return errors.New("ошибка сохранения реквизитов")
	}
	ok = true
	fmt.Fprintln(stdout, configPath)
	fmt.Fprintln(stdout, credentialsPath)
	return nil
}

func main() {
	if err := run(os.Args[1:], os.Stdout); err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
}
