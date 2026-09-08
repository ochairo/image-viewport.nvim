//go:build linux && (arm64 || amd64)

package imageprocessor

import (
	"github.com/ochairo/image-viewport.nvim/tools/safefs"
	"crypto/sha256"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"regexp"
	"strings"
	"syscall"

)

const runtimeMarker = ".dotfiles-managed-version"

var releaseName = regexp.MustCompile(`^image-processor-([0-9a-f]{64})$`)

func runtimeFile(parent *os.File, name string, executable bool, visit func(*os.File) error) error {
	fd, err := syscall.Openat(int(parent.Fd()), name, syscall.O_RDONLY|syscall.O_NOFOLLOW|syscall.O_NONBLOCK|syscall.O_CLOEXEC, 0)
	if err != nil {
		return err
	}
	f := os.NewFile(uintptr(fd), name)
	defer f.Close()
	before, err := metadata(f)
	if err != nil {
		return err
	}
	if before.Mode&syscall.S_IFMT != syscall.S_IFREG || before.Uid != uint32(os.Getuid()) || before.Nlink != 1 || before.Mode&07022 != 0 || before.Size > 64<<20 || executable && before.Mode&0100 == 0 {
		return fmt.Errorf("unsafe managed image runtime file: %s", name)
	}
	if err := visit(f); err != nil {
		return err
	}
	after, err := metadata(f)
	if err != nil {
		return err
	}
	if before != after {
		return fmt.Errorf("managed image runtime changed")
	}
	return nil
}

func fingerprintAt(parent *os.File) (string, error) {
	before, err := metadata(parent)
	if err != nil {
		return "", err
	}
	fd, err := syscall.Openat(int(parent.Fd()), ".", syscall.O_RDONLY|syscall.O_DIRECTORY|syscall.O_CLOEXEC, 0)
	if err != nil {
		return "", err
	}
	inventory := os.NewFile(uintptr(fd), "runtime-inventory")
	names, err := inventory.Readdirnames(-1)
	inventory.Close()
	if err != nil {
		return "", err
	}
	wanted := map[string]bool{"image-launch": true, "image-worker": true, "policy.xml": true, "source.sha256": true}
	for _, name := range names {
		if name == runtimeMarker {
			continue
		}
		if !wanted[name] {
			return "", fmt.Errorf("unexpected image runtime member")
		}
		delete(wanted, name)
	}
	if len(wanted) != 0 {
		return "", fmt.Errorf("incomplete image runtime")
	}
	hash := sha256.New()
	for _, name := range []string{"image-launch", "image-worker", "policy.xml", "source.sha256"} {
		err := runtimeFile(parent, name, name == "image-launch" || name == "image-worker", func(f *os.File) error {
			content := sha256.New()
			n, err := io.Copy(content, io.LimitReader(f, (64<<20)+1))
			if err != nil {
				return err
			}
			if n > 64<<20 {
				return fmt.Errorf("runtime file grew beyond limit")
			}
			_, err = fmt.Fprintf(hash, "%s\x00%x\n", name, content.Sum(nil))
			return err
		})
		if err != nil {
			return "", err
		}
	}
	after, err := metadata(parent)
	if err != nil {
		return "", err
	}
	if before != after {
		return "", fmt.Errorf("runtime directory changed")
	}
	return fmt.Sprintf("%x", hash.Sum(nil)), nil
}

// Fingerprint identifies the complete build output before managed publication.
func Fingerprint(path string) (string, error) {
	parent, err := safefs.Directory(path, true)
	if err != nil {
		return "", err
	}
	defer parent.Close()
	return fingerprintAt(parent)
}

func trustedRuntime() (string, error) {
	self, err := os.Executable()
	if err != nil {
		return "", err
	}
	return admitRuntime(self)
}

func admitRuntime(self string) (string, error) {
	canonical, err := filepath.EvalSymlinks(self)
	if err != nil {
		return "", err
	}
	if canonical != self || filepath.Base(self) != "image-launch" {
		return "", fmt.Errorf("image launcher is not in a canonical managed release")
	}
	root := filepath.Dir(self)
	match := releaseName.FindStringSubmatch(filepath.Base(root))
	if match == nil {
		return "", fmt.Errorf("image runtime has no managed identity")
	}
	if filepath.Base(filepath.Dir(root)) != "runtime" {
		return "", fmt.Errorf("image runtime is outside plugin runtime")
	}
	parent, err := safefs.Directory(root, true)
	if err != nil {
		return "", err
	}
	defer parent.Close()
	marker := ""
	if err := runtimeFile(parent, runtimeMarker, false, func(f *os.File) error {
		data, err := io.ReadAll(io.LimitReader(f, 129))
		marker = string(data)
		return err
	}); err != nil {
		return "", err
	}
	if marker != "image-processor:"+match[1]+"\n" {
		return "", fmt.Errorf("image runtime marker differs")
	}
	digest, err := fingerprintAt(parent)
	if err != nil {
		return "", err
	}
	if digest != match[1] {
		return "", fmt.Errorf("image runtime payload differs from its identity")
	}
	selection := filepath.Join(filepath.Dir(root), "current")
	link, err := os.Lstat(selection)
	if err != nil {
		return "", err
	}
	if link.Mode()&os.ModeSymlink == 0 || link.Sys().(*syscall.Stat_t).Uid != uint32(os.Getuid()) {
		return "", fmt.Errorf("image runtime selection is unmanaged")
	}
	selected, err := os.Readlink(selection)
	if err != nil {
		return "", err
	}
	if strings.TrimSpace(selected) != selected || selected != filepath.Base(root) {
		return "", fmt.Errorf("image runtime selection differs")
	}
	source, err := SourceDigest(filepath.Dir(filepath.Dir(root)))
	if err != nil { return "", err }
	if err := runtimeFile(parent, "source.sha256", false, func(f *os.File) error {
		data, err := io.ReadAll(io.LimitReader(f, 66))
		if err != nil { return err }
		if string(data) != source+"\n" { return fmt.Errorf("stale image runtime; run make build") }
		return nil
	}); err != nil { return "", err }
	return root, nil
}
