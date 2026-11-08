//go:build windows

package main

import (
	"errors"
	"os"
	"strings"
	"syscall"
	"unsafe"
)

// Получение окончательного пути через открытый объект не требует перечисления
// всех родительских каталогов Windows и разрешает junction/symlink самого пути.
func canonicalExistingPath(path string) (string, error) {
	f, err := os.Open(path)
	if err != nil {
		return "", err
	}
	defer f.Close()
	proc := syscall.NewLazyDLL("kernel32.dll").NewProc("GetFinalPathNameByHandleW")
	buffer := make([]uint16, 32768)
	n, _, callErr := proc.Call(f.Fd(), uintptr(unsafe.Pointer(&buffer[0])), uintptr(len(buffer)), 0)
	if n == 0 {
		return "", callErr
	}
	if n >= uintptr(len(buffer)) {
		return "", errors.New("resolved path is too long")
	}
	resolved := syscall.UTF16ToString(buffer[:n])
	if strings.HasPrefix(resolved, `\\?\UNC\`) {
		return `\\` + strings.TrimPrefix(resolved, `\\?\UNC\`), nil
	}
	return strings.TrimPrefix(resolved, `\\?\`), nil
}
