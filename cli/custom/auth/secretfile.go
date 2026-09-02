package auth

import (
	"os"
	"path/filepath"
)

// WriteSecretFile replaces a secret-bearing file through a temp file in the
// same directory, so a crash or a concurrent reader never sees a half-written
// credentials.json — truncating the real file in place can lose every stored
// credential, not only the one being written. bartolo's own credentials writer
// does the same; it is unexported there, hence the copy.
func WriteSecretFile(path string, data []byte) error {
	dir := filepath.Dir(path)
	f, err := os.CreateTemp(dir, ".tmp-*")
	if err != nil {
		return err
	}
	tmp := f.Name()
	defer os.Remove(tmp)
	if err := f.Chmod(0o600); err != nil {
		f.Close()
		return err
	}
	if _, err := f.Write(data); err != nil {
		f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		f.Close()
		return err
	}
	if err := f.Close(); err != nil {
		return err
	}
	return os.Rename(tmp, path)
}
