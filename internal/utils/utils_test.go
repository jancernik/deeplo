package utils

import (
	"os"
	"path/filepath"
	"testing"
)

func TestShortSha(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"long sha truncated to 8", "0123456789abcdef", "01234567"},
		{"exactly 8 unchanged", "0123abcd", "0123abcd"},
		{"shorter than 8 unchanged", "abc", "abc"},
		{"empty", "", ""},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			if got := ShortSha(testCase.in); got != testCase.want {
				t.Errorf("ShortSha(%q) = %q, want %q", testCase.in, got, testCase.want)
			}
		})
	}
}

func TestAtomicWrite_CreatesFileWithContentAndPerm(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "data.txt")
	content := []byte("hello world")

	if err := AtomicWrite(path, content, 0640); err != nil {
		t.Fatalf("AtomicWrite: %v", err)
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read back: %v", err)
	}
	if string(got) != string(content) {
		t.Errorf("content = %q, want %q", got, content)
	}
}

func TestAtomicWrite_OverwritesExisting(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "data.txt")

	if err := os.WriteFile(path, []byte("old contents that are longer"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := AtomicWrite(path, []byte("new"), 0644); err != nil {
		t.Fatalf("AtomicWrite: %v", err)
	}

	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != "new" {
		t.Errorf("content = %q, want %q", got, "new")
	}
}

// TestAtomicWrite_LeavesNoTempFiles verifies the temp file is renamed away (or
// cleaned up), so a successful write leaves exactly one file behind.
func TestAtomicWrite_LeavesNoTempFiles(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "data.txt")

	if err := AtomicWrite(path, []byte("x"), 0644); err != nil {
		t.Fatalf("AtomicWrite: %v", err)
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	if len(entries) != 1 {
		var names []string
		for _, e := range entries {
			names = append(names, e.Name())
		}
		t.Fatalf("expected exactly 1 file, got %v", names)
	}
	if entries[0].Name() != "data.txt" {
		t.Errorf("unexpected leftover file %q", entries[0].Name())
	}
}

// TestAtomicWrite_NonexistentDir verifies a write into a missing directory fails
// (rather than silently creating it) since the temp file can't be created there.
func TestAtomicWrite_NonexistentDir(t *testing.T) {
	path := filepath.Join(t.TempDir(), "missing", "data.txt")
	if err := AtomicWrite(path, []byte("x"), 0644); err == nil {
		t.Fatal("expected error writing into a nonexistent directory")
	}
}

// TestAtomicWrite_RenameFailureCleansUp verifies that when the final rename fails
// (here the target path is an existing directory), the error is surfaced and the
// temp file is removed rather than left behind.
func TestAtomicWrite_RenameFailureCleansUp(t *testing.T) {
	dir := t.TempDir()
	target := filepath.Join(dir, "iam-a-dir")
	if err := os.Mkdir(target, 0755); err != nil {
		t.Fatal(err)
	}

	if err := AtomicWrite(target, []byte("x"), 0644); err == nil {
		t.Fatal("expected error renaming over an existing directory")
	}

	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatal(err)
	}
	// Only the directory itself should remain; the temp file must be cleaned up.
	if len(entries) != 1 || entries[0].Name() != "iam-a-dir" {
		var names []string
		for _, entry := range entries {
			names = append(names, entry.Name())
		}
		t.Fatalf("temp file not cleaned up, dir contains %v", names)
	}
}
