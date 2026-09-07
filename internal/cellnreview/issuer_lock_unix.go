//go:build linux || darwin

package cellnreview

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"
)

func lockIssuer(root string) (func(), error) {
	f, err := os.OpenFile(filepath.Join(root, ".sympozium-issuer.lock"), os.O_CREATE|os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	if err := syscall.Flock(int(f.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		f.Close()
		return nil, fmt.Errorf("host issuer is busy or cannot be locked")
	}
	return func() { _ = f.Close() }, nil
}
