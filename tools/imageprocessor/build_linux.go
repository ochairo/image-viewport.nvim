//go:build linux && (arm64 || amd64)

package imageprocessor

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"syscall"
	"time"
	"unsafe"

	"github.com/ochairo/image-viewport.nvim/tools/safefs"
	"github.com/ochairo/image-viewport.nvim/tools/supervisor"
)

// SourceDigest binds a release to the complete current Go source inventory.
func SourceDigest(root string) (string, error) {
	parent, err := safefs.Directory(root, true)
	if err != nil { return "", err }
	defer parent.Close()
	names := []string{"tools/go.mod", "scripts/build", "scripts/tool", "scripts/go-supervise.sh", "runtime/policy.xml"}
	err = filepath.WalkDir(filepath.Join(root, "tools"), func(path string, entry fs.DirEntry, err error) error {
		if err != nil { return err }
		if !entry.IsDir() && !entry.Type().IsRegular() { return fmt.Errorf("nonregular build source") }
		if filepath.Ext(path) == ".go" {
			name, err := filepath.Rel(root, path)
			if err != nil { return err }
			names = append(names, name)
		}
		return nil
	})
	if err != nil { return "", err }
	if len(names) > 10000 { return "", fmt.Errorf("build source inventory exceeds bound") }
	sort.Strings(names)
	hash := sha256.New()
	var total int
	for _, name := range names {
		data, err := safefs.Read(root, name, 4<<20)
		if err != nil { return "", err }
		total += len(data)
		if total > 64<<20 { return "", fmt.Errorf("build source exceeds bound") }
		fmt.Fprintf(hash, "%s\x00%x\n", name, sha256.Sum256(data))
	}
	return fmt.Sprintf("%x", hash.Sum(nil)), nil
}

// Build creates a complete release before selecting it. Old releases remain
// available; errors never replace the current selector with partial output.
func Build(ctx context.Context, root string) error {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Minute)
	defer cancel()
	root, err := filepath.Abs(root)
	if err != nil { return err }
	root, err = filepath.EvalSymlinks(root)
	if err != nil { return err }
	parentPath := filepath.Join(root, "runtime")
	parent, err := safefs.Directory(parentPath, true)
	if err != nil { return err }
	defer parent.Close()
	fd, err := syscall.Openat(int(parent.Fd()), ".build-lock", syscall.O_RDWR|syscall.O_CREAT|syscall.O_NOFOLLOW|syscall.O_NONBLOCK|syscall.O_CLOEXEC, 0600)
	if err != nil { return err }
	lock := os.NewFile(uintptr(fd), "build-lock")
	defer lock.Close()
	state, err := metadata(lock)
	if err != nil { return err }
	if state.Mode&syscall.S_IFMT != syscall.S_IFREG || state.Uid != uint32(os.Getuid()) || state.Nlink != 1 || state.Mode&07777 != 0600 { return fmt.Errorf("unsafe build lock") }
	if err := syscall.Flock(fd, syscall.LOCK_EX|syscall.LOCK_NB); err != nil { return fmt.Errorf("another build owns the runtime: %w", err) }
	defer syscall.Flock(fd, syscall.LOCK_UN)
	before, err := SourceDigest(root)
	if err != nil { return err }
	selected, err := selection(parentPath)
	if err != nil { return err }
	stage, err := os.MkdirTemp(parentPath, ".build-")
	if err != nil { return err }
	stageInfo, err := os.Lstat(stage)
	if err != nil { return err }
	defer func() {
		current, err := os.Lstat(stage)
		if err == nil && os.SameFile(stageInfo, current) && current.IsDir() { _ = os.RemoveAll(stage) }
	}()
	cache, err := os.MkdirTemp("", "image-go-build-")
	if err != nil { return err }
	defer os.RemoveAll(cache)
	for _, name := range []string{"image-launch", "image-worker"} {
		args := []string{ filepath.Join(runtime.GOROOT(), "bin/go"), "-C", filepath.Join(root, "tools"), "build", "-trimpath", "-buildvcs=false", "-o", filepath.Join(stage, name), "./cmd/"+name}
		env := []string{"PATH=/usr/bin:/bin", "HOME="+cache, "GOROOT="+runtime.GOROOT(), "GOCACHE="+filepath.Join(cache, "cache"), "GOWORK=off", "GOFLAGS=", "GOENV=off", "GOTOOLCHAIN=local", "GOTELEMETRY=off", "GOPROXY=off", "GOSUMDB=off", "CGO_ENABLED=0"}
		statuses, err := supervisor.Supervise(ctx, [][]string{args}, supervisor.Options{Environment:env, CWD:root, Deadline:5*time.Minute})
		if err != nil { return fmt.Errorf("build %s: %w",name,err) }
		if statuses[0] != 0 { return fmt.Errorf("build %s failed with status %d",name,statuses[0]) }
		if err := os.Chmod(filepath.Join(stage, name), 0700); err != nil { return err }
	}
	policy, err := safefs.Read(root, "runtime/policy.xml", 1<<20)
	if err != nil { return err }
	if err := os.WriteFile(filepath.Join(stage, "policy.xml"), policy, 0600); err != nil { return err }
	if err := os.WriteFile(filepath.Join(stage, "source.sha256"), []byte(before+"\n"), 0600); err != nil { return err }
	after, err := SourceDigest(root)
	if err != nil { return err }
	if before != after { return fmt.Errorf("source changed during build") }
	digest, err := Fingerprint(stage)
	if err != nil { return err }
	name := "image-processor-"+digest
	if err := os.WriteFile(filepath.Join(stage, runtimeMarker), []byte("image-processor:"+digest+"\n"), 0600); err != nil { return err }
	// Validate reuse rather than overwrite an existing content-addressed name.
	target := filepath.Join(parentPath, name)
	if _, err := os.Lstat(target); err == nil {
		actual, err := Fingerprint(target)
		if err != nil || actual != digest { return fmt.Errorf("foreign or modified release occupies build destination") }
		marker, err := safefs.Read(target, runtimeMarker, 128)
		if err != nil || string(marker) != "image-processor:"+digest+"\n" { return fmt.Errorf("release marker differs") }
	} else if !os.IsNotExist(err) { return err
	} else if err := renameRelease(parent, filepath.Base(stage), name); err != nil { return err }
	current, err := selection(parentPath)
	if err != nil || current != selected { return fmt.Errorf("selection changed during build") }
	if err := ctx.Err(); err != nil { return err }
	var random [16]byte
	if _, err := rand.Read(random[:]); err != nil { return err }
	linkName := fmt.Sprintf(".select-%x", random)
	// Root operations remain confined to the admitted directory.
	output, err := os.OpenRoot(parentPath)
	if err != nil { return err }
	defer output.Close()
	if err := output.Symlink(name, linkName); err != nil { return err }
	defer output.Remove(linkName)
	if err := output.Rename(linkName, "current"); err != nil { return err }
	return parent.Sync()
}

func selection(parent string) (string, error) {
	path := filepath.Join(parent, "current")
	info, err := os.Lstat(path)
	if os.IsNotExist(err) { return "", nil }
	if err != nil { return "", err }
	if info.Mode()&os.ModeSymlink == 0 || info.Sys().(*syscall.Stat_t).Uid != uint32(os.Getuid()) { return "", fmt.Errorf("foreign runtime selector") }
	name, err := os.Readlink(path)
	if err != nil { return "", err }
	if strings.Contains(name, "/") || !releaseName.MatchString(name) { return "", fmt.Errorf("invalid runtime selector") }
	digest, err := Fingerprint(filepath.Join(parent, name))
	if err != nil || name != "image-processor-"+digest { return "", fmt.Errorf("selected runtime is corrupted") }
	marker, err := safefs.Read(filepath.Join(parent, name), runtimeMarker, 128)
	if err != nil || string(marker) != "image-processor:"+digest+"\n" { return "", fmt.Errorf("selected runtime marker differs") }
	return name, nil
}

// RENAME_NOREPLACE makes foreign destination admission atomic with publication.
func renameRelease(parent *os.File, from, to string) error {
	a, err := syscall.BytePtrFromString(from)
	if err != nil { return err }
	b, err := syscall.BytePtrFromString(to)
	if err != nil { return err }
	_, _, errno := syscall.Syscall6(renameAt2Call, parent.Fd(), uintptr(unsafe.Pointer(a)), parent.Fd(), uintptr(unsafe.Pointer(b)), 1, 0)
	if errno != 0 { return errno }
	return nil
}
