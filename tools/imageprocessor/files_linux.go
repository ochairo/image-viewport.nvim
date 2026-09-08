//go:build linux && (arm64 || amd64)

package imageprocessor

import (
	"github.com/ochairo/image-viewport.nvim/tools/safefs"
	"context"
	"crypto/rand"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"syscall"
	"unsafe"

)

var managedPNG = regexp.MustCompile(`^[0-9a-f]{64}\.png$`)

func memfd() (*os.File, error) {
	name, err := syscall.BytePtrFromString("dotfiles-image-source")
	if err != nil {
		return nil, err
	}
	fd, _, errno := syscall.Syscall(memfdCreateCall, uintptr(unsafe.Pointer(name)), 3, 0)
	if errno != 0 {
		return nil, errno
	}
	return os.NewFile(fd, "image-snapshot"), nil
}

func seal(f *os.File) error {
	_, _, errno := syscall.Syscall(syscall.SYS_FCNTL, f.Fd(), 0x409, 0xf)
	if errno != 0 {
		return errno
	}
	_, err := f.Seek(0, io.SeekStart)
	return err
}

func metadata(f *os.File) (syscall.Stat_t, error) {
	var state syscall.Stat_t
	err := syscall.Fstat(int(f.Fd()), &state)
	state.Atim = syscall.Timespec{}
	return state, err
}

func sealedSnapshot(ctx context.Context, path string) (*os.File, error) {
	canonical, err := filepath.EvalSymlinks(path)
	if err != nil {
		return nil, err
	}
	fd, err := syscall.Open(canonical, syscall.O_RDONLY|syscall.O_CLOEXEC|syscall.O_NOFOLLOW|syscall.O_NONBLOCK, 0)
	if err != nil {
		return nil, err
	}
	source := os.NewFile(uintptr(fd), "image-input")
	defer source.Close()
	before, err := metadata(source)
	if err != nil {
		return nil, err
	}
	if before.Mode&syscall.S_IFMT != syscall.S_IFREG || before.Nlink < 1 || before.Size < 1 || before.Size > maximumInput {
		return nil, fmt.Errorf("input is not a regular file within the reviewed size limit")
	}
	snapshot, err := memfd()
	if err != nil {
		return nil, err
	}
	success := false
	defer func() {
		if !success {
			snapshot.Close()
		}
	}()
	buffer := make([]byte, 1<<20)
	var copied int64
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		n, err := source.Read(buffer)
		if n > 0 {
			copied += int64(n)
			if copied > maximumInput {
				return nil, fmt.Errorf("input changed beyond the reviewed limit")
			}
			if _, err := snapshot.Write(buffer[:n]); err != nil {
				return nil, err
			}
		}
		if err == io.EOF {
			break
		}
		if err != nil {
			return nil, err
		}
	}
	after, err := metadata(source)
	if err != nil {
		return nil, err
	}
	if copied != before.Size || before != after {
		return nil, fmt.Errorf("input changed while it was being captured")
	}
	if err := seal(snapshot); err != nil {
		return nil, err
	}
	success = true
	return snapshot, nil
}

type staging struct {
	parent, output *os.File
	name, target   string
	published      bool
}

func newStaging(path string) (*staging, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path || !managedPNG.MatchString(filepath.Base(path)) {
		return nil, fmt.Errorf("output path is not managed")
	}
	parent, err := safefs.Directory(filepath.Dir(path), true)
	if err != nil {
		return nil, err
	}
	stage, err := stagingAt(parent, filepath.Base(path))
	if err != nil {
		parent.Close()
	}
	return stage, err
}

// stagingAt takes ownership of parent only on success.
func stagingAt(parent *os.File, target string) (*staging, error) {
	state, err := metadata(parent)
	if err != nil {
		return nil, err
	}
	if state.Mode&syscall.S_IFMT != syscall.S_IFDIR || state.Uid != uint32(os.Getuid()) || state.Mode&07777 != 0700 || !managedPNG.MatchString(target) {
		return nil, fmt.Errorf("output directory or name is not private and managed")
	}
	var nonce [8]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return nil, err
	}
	name := fmt.Sprintf(".%s.%d.%x.new", target, os.Getpid(), nonce)
	fd, err := syscall.Openat(int(parent.Fd()), name, syscall.O_RDWR|syscall.O_CREAT|syscall.O_EXCL|syscall.O_CLOEXEC|syscall.O_NOFOLLOW, 0600)
	if err != nil {
		return nil, err
	}
	return &staging{parent: parent, output: os.NewFile(uintptr(fd), name), name: name, target: target}, nil
}

func (s *staging) path(name string) string {
	return fmt.Sprintf("/proc/self/fd/%d/%s", s.parent.Fd(), name)
}

func (s *staging) ownedStage() error {
	entry, err := os.Lstat(s.path(s.name))
	if err != nil {
		return err
	}
	held, err := metadata(s.output)
	if err != nil {
		return err
	}
	actual := entry.Sys().(*syscall.Stat_t)
	if actual.Dev != held.Dev || actual.Ino != held.Ino || actual.Mode&syscall.S_IFMT != syscall.S_IFREG {
		return fmt.Errorf("staging output was replaced")
	}
	return nil
}

func (s *staging) close() {
	if !s.published && s.ownedStage() == nil {
		_ = syscall.Unlinkat(int(s.parent.Fd()), s.name)
	}
	s.output.Close()
	s.parent.Close()
}

func (s *staging) publish(ctx context.Context) error {
	state, err := metadata(s.output)
	if err != nil {
		return err
	}
	if state.Mode&syscall.S_IFMT != syscall.S_IFREG || state.Uid != uint32(os.Getuid()) || state.Mode&07777 != 0600 || state.Nlink != 1 || state.Size < int64(len(pngSignature)) || state.Size > maximumOutput {
		return fmt.Errorf("sandbox output metadata is invalid")
	}
	var header [8]byte
	if _, err := s.output.ReadAt(header[:], 0); err != nil {
		return err
	}
	if string(header[:]) != pngSignature {
		return fmt.Errorf("sandbox output is not PNG")
	}
	if err := s.output.Sync(); err != nil {
		return err
	}
	if existing, err := os.Lstat(s.path(s.target)); err == nil {
		actual := existing.Sys().(*syscall.Stat_t)
		if actual.Mode&syscall.S_IFMT != syscall.S_IFREG || actual.Uid != uint32(os.Getuid()) || actual.Mode&07777 != 0600 || actual.Nlink != 1 {
			return fmt.Errorf("existing managed output is unsafe")
		}
	} else if !os.IsNotExist(err) {
		return err
	}
	if err := s.ownedStage(); err != nil {
		return err
	}
	if err := ctx.Err(); err != nil {
		return err
	}
	if err := syscall.Renameat(int(s.parent.Fd()), s.name, int(s.parent.Fd()), s.target); err != nil {
		return err
	}
	s.published = true
	return s.parent.Sync()
}
