package secure

import (
	"os"

	"github.com/valentin-kaiser/go-core/apperror"
)

// Rotation re-encrypts everything a process keeps under its master key. Stop the process first: it holds the old
// key in memory.
//
// Rotate does not touch the key file until the data is safe: the new key is written next to it first, then reseal
// runs (it has to be atomic, e.g. one transaction or one file replace), then the new key replaces the old one,
// which stays as <path>.old. If the process dies in between, <path>.new is the key of the resealed data.
func Rotate(path string, reseal func(old, next Key) error) error {
	old, err := LoadOrCreateKey(path)
	if err != nil {
		return err
	}
	next, err := GenerateKey()
	if err != nil {
		return err
	}
	if err := os.WriteFile(path+".new", []byte(next.Encode()+"\n"), 0o600); err != nil {
		return err
	}
	if err := reseal(old, next); err != nil {
		_ = os.Remove(path + ".new")
		return apperror.NewError("re-encrypting failed, nothing was changed").AddError(err)
	}
	if err := os.Rename(path, path+".old"); err != nil {
		return apperror.NewErrorf("the data is re-encrypted, move %s.new to %s by hand", path, path).AddError(err)
	}
	if err := os.Rename(path+".new", path); err != nil {
		return apperror.NewErrorf("the data is re-encrypted, move %s.new to %s by hand", path, path).AddError(err)
	}
	return nil
}
