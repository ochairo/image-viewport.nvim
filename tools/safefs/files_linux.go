//go:build linux

// Package safefs reads bounded regular source through directory descriptors.
package safefs

import (
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"strconv"
	"syscall"
)

func Read(root, relative string, limit int64) ([]byte, error) {
	if relative == "." || !filepath.IsLocal(relative) || filepath.Clean(relative) != relative {
		return nil, fmt.Errorf("invalid source path")
	}
	parent, err := Directory(root, false)
	if err != nil { return nil, err }
	fd := int(parent.Fd())
	parts := strings.Split(relative, "/")
	for _, part := range parts[:len(parts)-1] {
		next, err := syscall.Openat(fd, part, syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_NOFOLLOW|syscall.O_CLOEXEC, 0)
		parent.Close()
		if err != nil {
			return nil, err
		}
		parent = os.NewFile(uintptr(next), "source-directory")
		fd = next
	}
	defer parent.Close()
	leaf, err := syscall.Openat(fd, parts[len(parts)-1], syscall.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK|syscall.O_CLOEXEC, 0)
	if err != nil {
		return nil, err
	}
	f := os.NewFile(uintptr(leaf), relative)
	defer f.Close()
	var before, after syscall.Stat_t
	if err := syscall.Fstat(leaf, &before); err != nil {
		return nil, err
	}
	if before.Mode&syscall.S_IFMT != syscall.S_IFREG || before.Nlink != 1 || before.Size > limit {
		return nil, fmt.Errorf("unsafe or oversized source")
	}
	data, err := io.ReadAll(io.LimitReader(f, limit+1))
	if err != nil {
		return nil, err
	}
	if err := syscall.Fstat(leaf, &after); err != nil {
		return nil, err
	}
	before.Atim, after.Atim = syscall.Timespec{}, syscall.Timespec{}
	if before != after || int64(len(data)) != before.Size {
		return nil, fmt.Errorf("source changed during read")
	}
	return data, nil
}

// NewDestination refuses existing output and symlink ancestry. The returned Root
// confines subsequent writes even if the caller-owned directory is renamed.
type Destination struct {
	*os.Root
	parent *os.File
	created *os.File
	path string
}

func (d *Destination) Close() error {
	err := d.Root.Close()
	d.created.Close()
	d.parent.Close()
	return err
}

func (d *Destination) Validate() error {
	actual, err := os.Lstat(d.path)
	if err != nil { return err }
	expected, err := d.created.Stat()
	if err != nil { return err }
	if !actual.IsDir() || !os.SameFile(actual,expected) { return fmt.Errorf("output directory identity changed") }
	return nil
}

func NewDestination(path string) (*Destination, error) {
	return newDestination(path,nil)
}

// The private seam runs only in deterministic substitution tests.
func newDestination(path string, afterCreate func()) (*Destination, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path { return nil, fmt.Errorf("destination must be absolute and normalized") }
	parent, err := Directory(filepath.Dir(path),false)
	if err != nil { return nil, err }
	success := false
	defer func() { if !success { parent.Close() } }()
	name := filepath.Base(path)
	if err := syscall.Mkdirat(int(parent.Fd()),name,0700); err != nil { return nil, err }
	if afterCreate != nil { afterCreate() }
	created, err := ChildDirectory(parent,name,true)
	if err != nil { return nil, err }
	defer func() { if !success { created.Close() } }()
	// OpenRoot follows only this kernel-owned descriptor reference, never a
	// replaceable output pathname. The admitted directory stays open until Close.
	root, err := os.OpenRoot("/proc/self/fd/"+strconv.Itoa(int(created.Fd())))
	if err != nil { return nil, err }
	d := &Destination{Root:root,parent:parent,created:created,path:path}
	if err := d.Validate(); err != nil { root.Close(); return nil, err }
	success = true
	return d,nil
}
