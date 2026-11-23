package spoolbudget

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

// ErrFull signals backpressure. The transfer must preserve its partial file.
var ErrFull = errors.New("shared GPFS staging budget exhausted")

const (
	configEnv = "SATELLITE_GPFS_STAGING_BUDGET"
	limit     = int64(100_000_000_000)
	rescan    = 30 * time.Second
)

type config struct {
	Mount     string   `json:"mount"`
	Roots     []string `json:"roots"`
	StatePath string   `json:"state_path,omitempty"`
}

type state struct {
	Config  [32]byte
	Used    int64
	Scanned time.Time
}

type writer struct {
	ctx    context.Context
	file   *os.File
	config config
	key    [32]byte
	path   string
	device uint64
	block  int64
}

// Wrap covers append writes on GPFS. Both services must use the same config
// file on the host's runtime filesystem. On data1 the file is returned directly.
func Wrap(ctx context.Context, file *os.File) (io.Writer, error) {
	path := strings.TrimSpace(os.Getenv(configEnv))
	if path == "" {
		return file, nil
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var cfg config
	if err := json.Unmarshal(data, &cfg); err != nil {
		return nil, err
	}
	if !filepath.IsAbs(cfg.Mount) || len(cfg.Roots) < 2 {
		return nil, errors.New("GPFS staging budget requires a mount and both staging roots")
	}
	mountInfo, err := os.Stat(cfg.Mount)
	if err != nil {
		return nil, err
	}
	if cfg.StatePath == "" {
		cfg.StatePath = path
	} else if !filepath.IsAbs(cfg.StatePath) {
		return nil, errors.New("GPFS staging budget state path must be absolute")
	}
	fileInfo, err := file.Stat()
	if err != nil {
		return nil, err
	}
	device := deviceID(mountInfo)
	if deviceID(fileInfo) != device {
		return file, nil
	}
	absolute, err := filepath.EvalSymlinks(file.Name())
	if err != nil {
		return nil, err
	}
	found := false
	for _, root := range cfg.Roots {
		if !filepath.IsAbs(root) || filepath.Clean(root) == filepath.Clean(cfg.Mount) {
			return nil, fmt.Errorf("invalid staging root %q", root)
		}
		realRoot, err := filepath.EvalSymlinks(root)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return nil, err
		}
		if strings.HasPrefix(absolute, realRoot+string(filepath.Separator)) {
			found = true
		}
	}
	if !found {
		return nil, fmt.Errorf("GPFS staging file is outside the budget roots: %s", absolute)
	}
	var fsstat unix.Statfs_t
	if err := unix.Statfs(cfg.Mount, &fsstat); err != nil {
		return nil, err
	}
	return &writer{ctx: ctx, file: file, config: cfg, key: sha256.Sum256(data),
		path: cfg.StatePath, device: device, block: max(1, int64(fsstat.Bsize))}, nil
}

// Rename publishes a completed staging file without racing a budget scan.
func Rename(ctx context.Context, source, target string) error {
	if strings.TrimSpace(os.Getenv(configEnv)) == "" {
		return os.Rename(source, target)
	}
	file, err := os.Open(source)
	if err != nil {
		return err
	}
	wrapped, err := Wrap(ctx, file)
	_ = file.Close()
	if err != nil {
		return err
	}
	if guarded, ok := wrapped.(*writer); ok {
		lock, err := guarded.lock()
		if err != nil {
			return err
		}
		defer lock.Close()
	}
	return os.Rename(source, target)
}

func (w *writer) lock() (*os.File, error) {
	lock, err := os.OpenFile(w.path+".lock", os.O_CREATE|os.O_RDWR, 0o600)
	if err != nil {
		return nil, err
	}
	for w.ctx.Err() == nil {
		err = unix.Flock(int(lock.Fd()), unix.LOCK_EX|unix.LOCK_NB)
		switch {
		case err == nil:
			return lock, nil
		case errors.Is(err, unix.EWOULDBLOCK), errors.Is(err, unix.EINTR):
			select {
			case <-w.ctx.Done():
			case <-time.After(20 * time.Millisecond):
			}
		default:
			_ = lock.Close()
			return nil, err
		}
	}
	_ = lock.Close()
	return nil, w.ctx.Err()
}

func (w *writer) Write(p []byte) (int, error) {
	lock, err := w.lock()
	if err != nil {
		return 0, err
	}
	defer lock.Close()
	info, err := w.file.Stat()
	if err != nil {
		return 0, err
	}
	growth := max(0, w.round(info.Size()+int64(len(p)))-w.charge(info))
	var current state
	data, err := os.ReadFile(w.path + ".state")
	if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return 0, err
	}
	cached := json.Unmarshal(data, &current) == nil && current.Config == w.key &&
		current.Used >= 0 && !current.Scanned.IsZero()
	if !cached || (current.Used >= limit || growth > limit-current.Used) && time.Since(current.Scanned) >= rescan {
		used, err := w.scan()
		if err != nil {
			return 0, err
		}
		current = state{Config: w.key, Used: used, Scanned: time.Now()}
		if err := w.save(current); err != nil {
			return 0, err
		}
	}
	if current.Used >= limit || growth > limit-current.Used {
		return 0, ErrFull
	}
	// Charge before writing. A crash or a short write can overcount, which
	// the next scan fixes. No other writer can scan during the write.
	if growth > 0 {
		current.Used += growth
		if err := w.save(current); err != nil {
			return 0, err
		}
	}
	return w.file.Write(p)
}

func (w *writer) save(value state) error {
	data, err := json.Marshal(value)
	if err != nil {
		return err
	}
	path := w.path + ".state"
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o600); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}

func (w *writer) scan() (int64, error) {
	var used int64
	for _, root := range w.config.Roots {
		resolved, err := filepath.EvalSymlinks(root)
		if errors.Is(err, fs.ErrNotExist) {
			continue
		}
		if err != nil {
			return 0, err
		}
		err = filepath.WalkDir(resolved, func(path string, entry fs.DirEntry, err error) error {
			if errors.Is(err, fs.ErrNotExist) {
				// Archival can remove a file or an empty directory.
				return nil
			}
			if err != nil {
				return err
			}
			if err := w.ctx.Err(); err != nil {
				return err
			}
			info, err := entry.Info()
			if errors.Is(err, fs.ErrNotExist) {
				return nil
			}
			if err != nil {
				return err
			}
			if deviceID(info) != w.device {
				if entry.IsDir() {
					return filepath.SkipDir
				}
				return nil
			}
			if info.Mode().IsRegular() && !strings.HasSuffix(path, ".json") && !strings.HasSuffix(path, ".json.tmp") {
				used += w.charge(info)
			}
			return nil
		})
		if err != nil {
			return 0, err
		}
	}
	return used, nil
}

func deviceID(info fs.FileInfo) uint64 {
	return uint64(info.Sys().(*syscall.Stat_t).Dev)
}

func (w *writer) round(size int64) int64 {
	return (size + w.block - 1) / w.block * w.block
}

func (w *writer) charge(info fs.FileInfo) int64 {
	blocks := info.Sys().(*syscall.Stat_t).Blocks
	return max(w.round(info.Size()), blocks*512)
}
