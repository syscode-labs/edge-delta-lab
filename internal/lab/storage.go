package lab

import (
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"syscall"
)

func SyncDir(path string) error {
	f, e := os.Open(path)
	if e != nil {
		return e
	}
	defer f.Close()
	return f.Sync()
}

// EnsureDir syncs each newly created directory's parent. This is a POSIX durability
// protocol, not a promise about storage hardware that lies about flush completion.
func EnsureDir(path string) error {
	path = filepath.Clean(path)
	st, e := os.Stat(path)
	if e == nil {
		if !st.IsDir() {
			return errors.New("not a directory: " + path)
		}
		return nil
	}
	if !os.IsNotExist(e) {
		return e
	}
	parent := filepath.Dir(path)
	if e = EnsureDir(parent); e != nil {
		return e
	}
	if e = os.Mkdir(path, 0750); e != nil && !os.IsExist(e) {
		return e
	}
	return SyncDir(parent)
}
func AtomicWrite(path string, b []byte, mode fs.FileMode) error {
	dir := filepath.Dir(path)
	if e := EnsureDir(dir); e != nil {
		return e
	}
	f, e := os.CreateTemp(dir, ".tmp-")
	if e != nil {
		return e
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	defer f.Close()
	if e = f.Chmod(mode); e != nil {
		return e
	}
	if _, e = f.Write(b); e != nil {
		return e
	}
	if e = f.Sync(); e != nil {
		return e
	}
	if e = f.Close(); e != nil {
		return e
	}
	if e = os.Rename(tmp, path); e != nil {
		return e
	}
	return SyncDir(dir)
}
func LockState(path string) (*os.File, error) {
	if e := EnsureDir(path); e != nil {
		return nil, e
	}
	f, e := os.OpenFile(filepath.Join(path, "agent.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if e != nil {
		return nil, e
	}
	if e = syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); e != nil {
		f.Close()
		return nil, errors.New("another agent holds this state directory")
	}
	return f, nil
}
func UnlockState(f *os.File) { _ = syscall.Flock(int(f.Fd()), syscall.LOCK_UN); _ = f.Close() }
func AvailableBytes(path string) (int64, error) {
	var s syscall.Statfs_t
	e := syscall.Statfs(path, &s)
	return int64(s.Bavail) * int64(s.Bsize), e
}
func ValidChunk(state string, c Chunk) bool {
	h, n, e := FileHash(CachePath(state, c))
	return e == nil && n == c.Size && h == c.SHA256
}

// A killed writer can leave uncommitted temporary files. Nothing references these.
func CleanTemps(root string) error {
	return filepath.WalkDir(root, func(path string, d fs.DirEntry, e error) error {
		if e != nil {
			return e
		}
		if !d.IsDir() && len(d.Name()) >= 5 && d.Name()[:5] == ".tmp-" {
			return os.Remove(path)
		}
		return nil
	})
}
