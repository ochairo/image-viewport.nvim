//go:build linux && (amd64 || arm64)

// Directory admission validates canonical ancestry without following links.
package safefs

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"syscall"
)


func metadata(f *os.File) (syscall.Stat_t, error) {
	var s syscall.Stat_t
	err := syscall.Fstat(int(f.Fd()), &s)
	return s, err
}

func directoryMetadata(s syscall.Stat_t, private bool) error {
	if s.Mode&syscall.S_IFMT != syscall.S_IFDIR {
		return fmt.Errorf("non-directory in managed ancestry")
	}
	if s.Uid != uint32(os.Getuid()) && (private || s.Uid != 0) {
		return fmt.Errorf("foreign directory owner")
	}
	sticky := !private && s.Uid == 0 && s.Mode&syscall.S_ISVTX != 0
	if s.Mode&0022 != 0 && !sticky {
		return fmt.Errorf("writable shared directory in managed ancestry")
	}
	if s.Mode&06000 != 0 {
		return fmt.Errorf("special directory mode")
	}
	return nil
}

// ChildDirectory opens a real directory relative to an admitted parent FD.
func ChildDirectory(parent *os.File, name string, private bool) (*os.File, error) {
	fd, err := syscall.Openat(int(parent.Fd()), name, syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	f := os.NewFile(uintptr(fd), name)
	s, err := metadata(f)
	if err == nil {
		err = directoryMetadata(s, private)
	}
	if err != nil {
		f.Close()
		return nil, err
	}
	return f, nil
}

// Directory rejects noncanonical paths and validates every existing ancestor.
func Directory(path string, private bool) (*os.File, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return nil, fmt.Errorf("path must be absolute and normalized")
	}
	fd, err := syscall.Open("/", syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	current := os.NewFile(uintptr(fd), "/")
	s, err := metadata(current)
	if err == nil {
		err = directoryMetadata(s, false)
	}
	if err != nil {
		current.Close()
		return nil, err
	}
	if path != "/" {
		for _, part := range strings.Split(strings.TrimPrefix(path, "/"), "/") {
			next, err := ChildDirectory(current, part, false)
			current.Close()
			if err != nil {
				return nil, err
			}
			current = next
		}
	}
	s, err = metadata(current)
	if err == nil {
		err = directoryMetadata(s, private)
	}
	if err != nil {
		current.Close()
		return nil, err
	}
	return current, nil
}

