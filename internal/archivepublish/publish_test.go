package archivepublish

import (
	"crypto/sha256"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"testing"
)

func TestStagingDirPreservesExistingPartial(t *testing.T) {
	oldRoot, data1Root := t.TempDir(), t.TempDir()
	relative := filepath.Join("staging", "modis", "123")
	dir, err := StagingDir(oldRoot, data1Root, relative, "scene.hdf", ".part", ".downloaded")
	if err != nil || dir != filepath.Join(data1Root, relative) {
		t.Fatalf("new file staging=%s: %v", dir, err)
	}
	original := filepath.Join(oldRoot, relative)
	if err := os.MkdirAll(original, 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(original, "scene.hdf.part"), []byte("partial"), 0o600); err != nil {
		t.Fatal(err)
	}
	dir, err = StagingDir(oldRoot, data1Root, relative, "scene.hdf", ".part", ".downloaded")
	if err != nil || dir != original {
		t.Fatalf("legacy partial staging=%s: %v", dir, err)
	}
}

func TestRenamePreservesInodePermissionsAndReceipt(t *testing.T) {
	if runtime.GOOS != "linux" {
		t.Skip("NFS archive rename is enabled on Linux")
	}
	root := t.TempDir()
	t.Setenv(lockEnv, root)
	source := filepath.Join(root, "staging")
	target := filepath.Join(root, "archive.hdf")
	payload := []byte("verified payload")
	if err := os.WriteFile(source, payload, 0o600); err != nil {
		t.Fatal(err)
	}
	before, err := os.Stat(source)
	if err != nil {
		t.Fatal(err)
	}
	digest := fmt.Sprintf("%x", sha256.Sum256(payload))
	unlock, err := Lock(target)
	if err != nil {
		t.Fatal(err)
	}
	defer unlock()
	renamed, err := Rename(source, target, digest, int64(len(payload)), 0o666, 0o664)
	if err != nil || !renamed {
		t.Fatalf("rename=%v: %v", renamed, err)
	}
	after, err := os.Stat(target)
	if err != nil || !os.SameFile(before, after) || after.Mode().Perm() != 0o664 || systemStat(after).Nlink != 1 {
		t.Fatalf("incorrect published inode or permissions: %v, %v", after, err)
	}
	if _, err := os.Lstat(source); !os.IsNotExist(err) {
		t.Fatalf("staging directory still has a name for the inode: %v", err)
	}
	got, err := ReadReceipt(source, target)
	if err != nil || got.SHA256 != digest || got.Size != int64(len(payload)) {
		t.Fatalf("invalid rename receipt: %+v: %v", got, err)
	}
}
