//go:build !windows

package main

import (
	"errors"
	"os"

	"golang.org/x/sys/unix"
)

func pushLockFileNB(f *os.File) error {
	for {
		err := unix.Flock(int(f.Fd()), unix.LOCK_EX|unix.LOCK_NB)
		switch {
		case err == nil:
			return nil
		case errors.Is(err, unix.EINTR):
			continue
		case errors.Is(err, unix.EWOULDBLOCK):
			return errPushBusy
		}
		return err
	}
}

func pushUnlockFile(f *os.File) { _ = unix.Flock(int(f.Fd()), unix.LOCK_UN) }
