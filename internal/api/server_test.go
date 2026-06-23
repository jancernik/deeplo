package api

import (
	"os"
	"os/user"
	"path/filepath"
	"strconv"
	"testing"
)

// TestChownToGroupUnknownGroup verifies an unresolvable group name is reported
// as an error rather than silently ignored.
func TestChownToGroupUnknownGroup(t *testing.T) {
	sock := filepath.Join(t.TempDir(), "deeplo.sock")
	if err := os.WriteFile(sock, nil, 0660); err != nil {
		t.Fatal(err)
	}
	if err := chownToGroup(sock, "definitely-not-a-real-group-xyz"); err == nil {
		t.Fatal("expected error for an unknown group")
	}
}

// TestChownToGroupOwnGroup verifies the chown succeeds for a group the current
// user belongs to (which needs no privileges), exercising the real code path.
func TestChownToGroupOwnGroup(t *testing.T) {
	group, err := user.LookupGroupId(strconv.Itoa(os.Getgid()))
	if err != nil {
		t.Skipf("cannot resolve current group: %v", err)
	}
	sock := filepath.Join(t.TempDir(), "deeplo.sock")
	if err := os.WriteFile(sock, nil, 0660); err != nil {
		t.Fatal(err)
	}
	if err := chownToGroup(sock, group.Name); err != nil {
		t.Fatalf("chownToGroup to own group should succeed, got: %v", err)
	}
}
