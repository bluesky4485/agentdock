//go:build windows

package installer

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"golang.org/x/sys/windows"
)

func lockWindowsStableFile(t *testing.T, path string, share uint32) windows.Handle {
	t.Helper()
	ptr, err := windows.UTF16PtrFromString(path)
	if err != nil {
		t.Fatal(err)
	}
	handle, err := windows.CreateFile(ptr, windows.GENERIC_READ, share, nil,
		windows.OPEN_EXISTING, windows.FILE_ATTRIBUTE_NORMAL, 0)
	if err != nil {
		t.Fatal(err)
	}
	return handle
}

func TestCopyWindowsStableFileSkipsIdenticalRunningShim(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "next.exe")
	target := filepath.Join(dir, "agentdock-tray.exe")
	for _, path := range []string{source, target} {
		if err := os.WriteFile(path, []byte("same shim"), 0o755); err != nil {
			t.Fatal(err)
		}
	}
	handle := lockWindowsStableFile(t, target, windows.FILE_SHARE_READ)
	defer windows.CloseHandle(handle)

	if err := copyWindowsStableFile(source, target, 0o755); err != nil {
		t.Fatalf("unchanged shim should not be overwritten: %v", err)
	}
}

func TestCopyWindowsStableFileWaitsForTransientSharingViolation(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "next.exe")
	target := filepath.Join(dir, "agentdock-tray.exe")
	if err := os.WriteFile(source, []byte("new shim"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte("old shim"), 0o755); err != nil {
		t.Fatal(err)
	}
	handle := lockWindowsStableFile(t, target, windows.FILE_SHARE_READ)
	released := make(chan struct{})
	go func() {
		time.Sleep(600 * time.Millisecond)
		windows.CloseHandle(handle)
		close(released)
	}()
	defer func() { <-released }()

	if err := copyWindowsStableFile(source, target, 0o755); err != nil {
		t.Fatalf("transient sharing violation should be retried: %v", err)
	}
	installed, err := os.ReadFile(target)
	if err != nil || string(installed) != "new shim" {
		t.Fatalf("unexpected shim content %q: %v", installed, err)
	}
}

func TestCopyWindowsStableFileRejectsPersistentLock(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "next.exe")
	target := filepath.Join(dir, "agentdock-tray.exe")
	if err := os.WriteFile(source, []byte("new shim"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(target, []byte("old shim"), 0o755); err != nil {
		t.Fatal(err)
	}
	handle := lockWindowsStableFile(t, target, windows.FILE_SHARE_READ)
	defer windows.CloseHandle(handle)

	err := copyWindowsStableFile(source, target, 0o755)
	if err == nil || !strings.Contains(err.Error(), "超时") {
		t.Fatalf("persistent lock should fail closed with diagnostics: %v", err)
	}
	installed, readErr := os.ReadFile(target)
	if readErr != nil || string(installed) != "old shim" {
		t.Fatalf("persistent lock must preserve existing shim %q: %v", installed, readErr)
	}
}
