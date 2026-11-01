package filesync

import (
	"errors"
	"runtime"
	"syscall"
	"testing"
)

type fakeDirectory struct {
	syncErr, closeErr error
	closed            bool
}

func (d *fakeDirectory) Sync() error  { return d.syncErr }
func (d *fakeDirectory) Close() error { d.closed = true; return d.closeErr }

func TestDirectorySyncErrors(t *testing.T) {
	for _, tc := range []struct {
		name              string
		syncErr, closeErr error
		wantError         bool
	}{
		{"unsupported", syscall.ENOTSUP, nil, false},
		{"invalid operation", syscall.EINVAL, nil, false},
		{"not implemented", syscall.ENOSYS, nil, false},
		{"permission", syscall.EACCES, nil, runtime.GOOS != "windows"},
		{"io error", syscall.EIO, nil, true},
		{"close error", syscall.ENOTSUP, syscall.EIO, true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			dir := &fakeDirectory{syncErr: tc.syncErr, closeErr: tc.closeErr}
			err := DirectoryFile(dir)
			if (err != nil) != tc.wantError || !dir.closed || tc.closeErr != nil && !errors.Is(err, tc.closeErr) {
				t.Fatalf("sync = %v, closed = %v", err, dir.closed)
			}
		})
	}
}
