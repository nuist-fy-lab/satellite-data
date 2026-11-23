// Package archivepublish supports verified, metadata-only publication on data1.
package archivepublish

import (
	"bytes"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"syscall"

	"golang.org/x/sys/unix"
)

const (
	lockEnv = "SATELLITE_ARCHIVE_RENAME_LOCK_DIR"
)

// StagingDir prefers data1 for a new file and preserves an existing local transfer.
func StagingDir(oldRoot, newRoot, relative, name string, suffixes ...string) (string, error) {
	original := filepath.Join(oldRoot, relative)
	if newRoot == "" {
		return original, nil
	}
	for _, suffix := range suffixes {
		if _, err := os.Lstat(filepath.Join(original, name+suffix)); err == nil {
			return original, nil
		} else if !errors.Is(err, os.ErrNotExist) {
			return "", err
		}
	}
	if !filepath.IsAbs(newRoot) {
		return "", errors.New("new staging root must be absolute")
	}
	if info, err := os.Stat(newRoot); err != nil {
		return "", err
	} else if !info.IsDir() {
		return "", errors.New("new staging root is not a directory")
	}
	return filepath.Join(newRoot, relative), nil
}

// Lock serializes both applications' publication to the same canonical path.
// The configured archive trees are exclusively written by these publishers.
func Lock(target string) (func(), error) {
	dir := os.Getenv(lockEnv)
	if dir == "" {
		return func() {}, nil
	}
	parent, err := filepath.EvalSymlinks(filepath.Dir(target))
	if err != nil {
		return nil, err
	}
	key := sha256.Sum256([]byte(filepath.Join(parent, filepath.Base(target))))
	lock, err := os.OpenFile(filepath.Join(dir, fmt.Sprintf("%02x.lock", key[0])), os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	for {
		err := unix.Flock(int(lock.Fd()), unix.LOCK_EX)
		if errors.Is(err, unix.EINTR) {
			continue
		}
		if err != nil {
			_ = lock.Close()
			return nil, err
		}
		return func() { _ = lock.Close() }, nil
	}
}

type Receipt struct {
	Target string
	SHA256 string
	Size   int64
}

func receiptPath(source string) string {
	key := sha256.Sum256([]byte(filepath.Base(source)))
	return filepath.Join(filepath.Dir(source), ".archive-rename-"+hex.EncodeToString(key[:])+".json")
}

func HasReceipt(source string) bool {
	_, err := os.Stat(receiptPath(source))
	return err == nil
}

func ReadReceipt(source, target string) (Receipt, error) {
	data, err := os.ReadFile(receiptPath(source))
	if err != nil {
		return Receipt{}, err
	}
	var receipt Receipt
	err = json.Unmarshal(data, &receipt)
	if err != nil {
		return Receipt{}, err
	}
	if receipt.Target != target || len(receipt.SHA256) != 64 || receipt.Size <= 0 {
		return Receipt{}, errors.New("archive rename receipt does not match the expected target")
	}
	return receipt, nil
}

func Forget(source string) {
	_ = os.Remove(receiptPath(source))
}

// Rename uses the verified inode if staging and the destination share data1.
// The caller holds Lock and has handled any existing destination. A receipt
// lets the verifier recover publication after interruption, before DB commit.
func Rename(source, target, digest string, size int64, mode os.FileMode, exactMode os.FileMode) (bool, error) {
	dir := os.Getenv(lockEnv)
	if dir == "" || runtime.GOOS != "linux" {
		return false, nil
	}
	src, err := os.Open(source)
	if err != nil {
		return false, err
	}
	defer src.Close()
	info, err := src.Stat()
	if err != nil {
		return false, err
	}
	parent := filepath.Dir(target)
	dest, err := os.Stat(parent)
	if err != nil {
		return false, err
	}
	lockDir, err := os.Stat(dir)
	if err != nil {
		return false, err
	}
	a, b, c := systemStat(info), systemStat(dest), systemStat(lockDir)
	if a.Dev != b.Dev || b.Dev != c.Dev {
		return false, nil
	}
	if !info.Mode().IsRegular() || info.Size() != size || size <= 0 || len(digest) != 64 {
		return false, errors.New("verified staging size or type changed")
	}
	if err := applyArchivePermissions(src, parent, mode, exactMode); err != nil {
		return false, err
	}
	if err := src.Sync(); err != nil {
		return false, err
	}
	if err := saveReceipt(source, Receipt{Target: target, SHA256: digest, Size: size}); err != nil {
		return false, err
	}
	if _, err := os.Lstat(target); err == nil {
		return false, os.ErrExist
	} else if !errors.Is(err, os.ErrNotExist) {
		return false, err
	}
	current, err := os.Lstat(source)
	if err != nil {
		return false, err
	}
	if !os.SameFile(info, current) || current.Size() != info.Size() || current.ModTime() != info.ModTime() {
		return false, errors.New("verified staging changed before rename")
	}
	if err := os.Rename(source, target); err != nil {
		return false, err
	}
	return true, SyncRenameDirectories(source, target)
}

func saveReceipt(source string, receipt Receipt) error {
	data, err := json.Marshal(receipt)
	if err != nil {
		return err
	}
	path := receiptPath(source)
	tmp, err := os.CreateTemp(filepath.Dir(source), ".rename-receipt-*.json")
	if err != nil {
		return err
	}
	defer os.Remove(tmp.Name())
	_, writeErr := tmp.Write(data)
	if err := errors.Join(writeErr, tmp.Sync(), tmp.Close()); err != nil {
		return err
	}
	if err := os.Rename(tmp.Name(), path); err != nil {
		return err
	}
	return syncDir(filepath.Dir(source))
}

func SyncRenameDirectories(source, target string) error {
	return errors.Join(syncDir(filepath.Dir(target)), syncDir(filepath.Dir(source)))
}

func syncDir(path string) error {
	file, err := os.Open(path)
	if err != nil {
		return err
	}
	defer file.Close()
	return file.Sync()
}

func systemStat(info os.FileInfo) *syscall.Stat_t {
	return info.Sys().(*syscall.Stat_t)
}

func applyArchivePermissions(source *os.File, parent string, mode, exactMode os.FileMode) error {
	// A zero-length reference inherits destination permissions exactly as the
	// regular copy path does. It is hidden and removed before publication.
	var nonce [12]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return err
	}
	template, err := os.OpenFile(filepath.Join(parent, ".archive-permissions-"+hex.EncodeToString(nonce[:])), os.O_RDWR|os.O_CREATE|os.O_EXCL, mode)
	if err != nil {
		return err
	}
	defer template.Close()
	defer os.Remove(template.Name())
	if exactMode != 0 {
		if err := template.Chmod(exactMode); err != nil {
			return err
		}
	}
	info, err := template.Stat()
	if err != nil {
		return err
	}
	current, err := source.Stat()
	if err != nil {
		return err
	}
	a, b := systemStat(current), systemStat(info)
	if a.Uid != b.Uid {
		return errors.New("verified staging has a different owner than the archive")
	}
	if a.Gid != b.Gid {
		if err := source.Chown(-1, int(b.Gid)); err != nil {
			return err
		}
	}
	if current.Mode().Perm() != info.Mode().Perm() {
		if err := source.Chmod(info.Mode().Perm()); err != nil {
			return err
		}
	}
	for _, name := range []string{"system.nfs4_acl", "system.posix_acl_access"} {
		wanted, supported, err := acl(template, name)
		if err != nil {
			return err
		}
		if !supported {
			continue
		}
		present, _, err := acl(source, name)
		if err != nil {
			return err
		}
		if bytes.Equal(present, wanted) {
			continue
		}
		if len(wanted) == 0 {
			err = unix.Fremovexattr(int(source.Fd()), name)
		} else {
			err = unix.Fsetxattr(int(source.Fd()), name, wanted, 0)
		}
		if err != nil {
			return fmt.Errorf("set inherited archive ACL: %w", err)
		}
		applied, _, err := acl(source, name)
		if err != nil {
			return err
		}
		if !bytes.Equal(applied, wanted) {
			return errors.New("archive ACL did not match inherited permissions")
		}
	}
	return nil
}

func acl(file *os.File, name string) ([]byte, bool, error) {
	disk := int(file.Fd())
	size, err := unix.Fgetxattr(disk, name, nil)
	if errors.Is(err, unix.ENOTSUP) || errors.Is(err, unix.EINVAL) || errors.Is(err, unix.EPERM) && strings.HasPrefix(name, "system.posix") {
		return nil, false, nil
	}
	if errors.Is(err, unix.ENODATA) {
		return nil, true, nil
	}
	if err != nil {
		return nil, false, err
	}
	data := make([]byte, size)
	n, err := unix.Fgetxattr(disk, name, data)
	if err != nil {
		return nil, false, err
	}
	return data[:n], true, nil
}
