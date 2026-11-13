package nodestatetest_test

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/medik8s/common/pkg/nodestate"
	"github.com/medik8s/common/pkg/nodestate/nodestatetest"
)

// TestTempStateDirWorksAsAStateDirectory is the contract the helper promises:
// a Store over the returned directory, on the real filesystem and under the
// default security policy, writes and reads without a host-path refusal.
func TestTempStateDirWorksAsAStateDirectory(t *testing.T) {
	dir := nodestatetest.TempStateDir(t)

	// Private: no mode bits the default policy forbids in a state directory.
	fi, err := os.Stat(dir)
	if err != nil {
		t.Fatalf("stat %s: %v", dir, err)
	}
	if bad := fi.Mode().Perm() & 0o077; bad != 0 {
		t.Errorf("mode %#o sets %#o, which other users can act on", fi.Mode().Perm(), bad)
	}

	// A real path: resolving it again is a fixed point, so the store's own
	// resolution check sees the directory itself, not a link to it.
	resolved, err := filepath.EvalSymlinks(dir)
	if err != nil {
		t.Fatalf("resolve %s: %v", dir, err)
	}
	if resolved != dir {
		t.Errorf("TempStateDir returned %s, which resolves to %s", dir, resolved)
	}

	store, err := nodestate.New(nodestate.ComponentSBR, nodestate.WithDir(dir))
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := store.Write(context.Background(), nodestate.NewDocument(nodestate.ComponentSBR)); err != nil {
		t.Fatalf("Write: %v", err)
	}
	res, err := store.Load(context.Background())
	if err != nil {
		t.Fatalf("Load: %v", err)
	}
	if res.Status != nodestate.StatusOK {
		t.Errorf("Status = %s, want %s", res.Status, nodestate.StatusOK)
	}
}
