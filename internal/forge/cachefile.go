package forge

import (
	"errors"
	"fmt"
	"io"
	"io/fs"
	"math/rand/v2"
	"os"
	"path/filepath"
	"strconv"
)

// The cache directory is gl-repos's own, in a place others may have
// written to before it (~/.cache). Nothing there is followed: the
// directory is opened once, after a check that it is a real directory
// and not a symlink, and every file operation is relative to that open
// directory (os.Root), so a path that would leave it, through a symlink
// too, is refused by the kernel rather than checked and then used by
// name. A symlink or any other non-regular file at a cache file's path
// is removed and read as no cache, and a file is read, and its mode
// tightened, only through the descriptor of the file that was checked.
// What remains is the directory itself: looking at it and opening it are
// two steps, so a directory swapped in between is noticed (SameFile) and
// not used.

// errNoCacheFile is a cache file that is none: a planted link, a device,
// or one replaced between the check and the read.
var errNoCacheFile = errors.New("not a regular file")

// errCacheTooLarge is a cache file over MaxOutput bytes: not one gl-repos
// wrote, and not read into memory.
var errCacheTooLarge = errors.New("cache file too large")

// chmod and fchmod are (*os.Root).Chmod and (*os.File).Chmod;
// overridable for tests.
var (
	chmod  = (*os.Root).Chmod
	fchmod = (*os.File).Chmod
)

// ReadCacheFile reads the private file p of the cache directory (0600;
// an older version wrote 0644, which is tightened). It is an error when
// the directory is a symlink or no directory (openCacheDir), p is
// missing, no regular file (a planted symlink, which is removed), cannot
// be made private (then it is removed: a file others may read is no
// cache), cannot be read or is larger than MaxOutput.
func ReadCacheFile(p string) ([]byte, error) {
	dir, err := openCacheDir(filepath.Dir(p), false)
	if err != nil {
		return nil, err
	}
	defer dir.Close()
	name := filepath.Base(p)
	fi, err := dir.Lstat(name)
	if err != nil {
		return nil, err
	}
	if !fi.Mode().IsRegular() {
		dir.Remove(name) // the link itself, not what it points to
		return nil, errNoCacheFile
	}
	f, err := dir.Open(name)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	ff, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !os.SameFile(fi, ff) { // replaced between the check and the open
		return nil, errNoCacheFile
	}
	if err := fchmod(f, 0o600); err != nil {
		dir.Remove(name)
		return nil, err
	}
	// Read as much as one glab answer may be: a bigger file is none
	// gl-repos wrote, and no cache (like one that does not parse).
	data, err := io.ReadAll(io.LimitReader(f, MaxOutput+1))
	if err == nil && int64(len(data)) > MaxOutput {
		return nil, errCacheTooLarge
	}
	return data, err
}

// WriteCacheFile replaces p with data in one step: the cache directory
// is created and made private first (openCacheDir), the data goes into
// a new temporary file of its own in it (0600, created exclusively, so
// concurrent writes never mix and nothing planted at its name is
// written through), which then takes p's place (a symlink there is
// replaced, not followed).
func WriteCacheFile(p string, data []byte) error {
	dir, err := openCacheDir(filepath.Dir(p), true)
	if err != nil {
		return err
	}
	defer dir.Close()
	name := filepath.Base(p)
	f, tmp, err := createTemp(dir, name)
	if err != nil {
		return err
	}
	_, err = f.Write(data)
	if cerr := f.Close(); err == nil {
		err = cerr
	}
	if err == nil {
		err = dir.Rename(tmp, name)
	}
	if err != nil {
		dir.Remove(tmp)
	}
	return err
}

// RemoveCacheFile removes the cache file p, relative to the checked and
// opened cache directory like every other operation there (a planted
// link at p is removed, never followed). Nothing to remove is no error.
func RemoveCacheFile(p string) error {
	dir, err := openCacheDir(filepath.Dir(p), false)
	if err != nil {
		return err
	}
	defer dir.Close()
	if err := dir.Remove(filepath.Base(p)); err != nil && !errors.Is(err, fs.ErrNotExist) {
		return err
	}
	return nil
}

// createTemp is os.CreateTemp inside dir: a new file "<name>.<random>.tmp"
// of its own, 0600 and created exclusively (nothing that exists at the
// name, a planted link included, is opened). It returns the open file
// and its name in dir.
func createTemp(dir *os.Root, name string) (*os.File, string, error) {
	for try := 0; ; try++ {
		tmp := name + "." + strconv.FormatUint(rand.Uint64(), 36) + ".tmp"
		f, err := dir.OpenFile(tmp, os.O_WRONLY|os.O_CREATE|os.O_EXCL, 0o600)
		if err == nil {
			return f, tmp, nil
		}
		if !errors.Is(err, fs.ErrExist) || try >= 100 {
			return nil, "", err
		}
	}
}

// openCacheDir opens the cache directory dir for the relative file
// operations above (every read and write goes through it), after making
// sure it is a real directory of this user's own: created 0700 when
// create is set (reading a cache that does not exist creates nothing),
// refused when its path is a symlink (planted before gl-repos first ran,
// the cache would land wherever it points), and, when it is gl-repos's
// own (the default directory, or one it created), tightened to 0700 when
// an older version made it 0755 (never keep project names where others
// may read them: on a read as on a write, like the files). A directory
// chosen with GL_REPOS_CACHE_DIR keeps its mode: it may be a folder of
// yours that holds more than this cache; the files in it are 0600 all
// the same. The directory that was opened is checked to be the one that
// was looked at, so a swap in between is noticed and nothing is done
// through it. The caller closes it.
func openCacheDir(dir string, create bool) (*os.Root, error) {
	own := dir == defaultCacheDir()
	if create {
		if _, err := os.Lstat(dir); errors.Is(err, fs.ErrNotExist) {
			own = true
		}
		if err := os.MkdirAll(dir, 0o700); err != nil {
			return nil, err
		}
	}
	fi, err := os.Lstat(dir)
	if err != nil {
		return nil, err
	}
	if !fi.IsDir() {
		return nil, fmt.Errorf("cache directory %s is a symlink or no directory: not used (set %s to a real directory)", dir, EnvCacheDir)
	}
	r, err := os.OpenRoot(dir)
	if err != nil {
		return nil, err
	}
	ri, err := r.Stat(".")
	if err == nil && !os.SameFile(fi, ri) {
		err = fmt.Errorf("cache directory %s changed while it was opened: not used", dir)
	}
	if err == nil && own {
		err = chmod(r, ".", 0o700)
	}
	if err != nil {
		r.Close()
		return nil, err
	}
	return r, nil
}
