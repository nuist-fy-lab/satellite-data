package transfer

import (
	"context"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

	"github.com/nuist-fy-lab/satellite-data/internal/model"
)

func TestDownloadInheritsDefaultArchiveACL(t *testing.T) {
	if _, err := exec.LookPath("setfacl"); err != nil {
		if os.Getenv("CI") == "true" {
			t.Fatal("CI must install ACL tools")
		}
		t.Skip("setfacl is unavailable")
	}
	root := t.TempDir()
	if err := os.Chmod(root, os.ModeSetgid|0o775); err != nil {
		t.Fatal(err)
	}
	acl := "d:u::rwx,d:g::rwx,d:m::rwx,d:o::r-x,d:u:65534:r-x"
	if output, err := exec.Command("setfacl", "-m", acl, root).CombinedOutput(); err != nil {
		t.Fatalf("set default ACL: %v: %s", err, output)
	}
	oldMask := syscall.Umask(0o077)
	t.Cleanup(func() { syscall.Umask(oldMask) })
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte("satellite archive bytes"))
	}))
	defer server.Close()
	dir := filepath.Join(root, "source", "2026", "09")
	result, err := NewHTTP(server.Client()).Download(context.Background(), model.Asset{Name: "file.nc", URL: server.URL}, dir)
	if err != nil {
		t.Fatal(err)
	}
	info, err := os.Stat(result.Path)
	if err != nil || info.Mode().Perm() != 0o664 {
		t.Fatalf("archive file must be group writable and public readable: %v (%v)", info, err)
	}
	for path := dir; path != root; path = filepath.Dir(path) {
		info, err := os.Stat(path)
		if err != nil || info.Mode().Perm() != 0o775 || info.Mode()&os.ModeSetgid == 0 {
			t.Fatalf("archive directory must inherit setgid and shared access: %v (%v)", info, err)
		}
	}
	output, err := exec.Command("getfacl", "-cpn", result.Path).CombinedOutput()
	if err != nil || !strings.Contains(string(output), "user:65534:r-x") || !strings.Contains(string(output), "mask::rw-") {
		t.Fatalf("named parent ACL was not inherited: %v: %s", err, output)
	}
}
