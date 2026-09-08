//go:build linux && (arm64 || amd64)

package imageprocessor

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

func TestSealedSnapshot(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root, "input")
	if err := os.WriteFile(source, []byte(pngSignature+"fixture"), 0600); err != nil {
		t.Fatal(err)
	}
	snapshot, err := sealedSnapshot(context.Background(), source)
	if err != nil {
		t.Fatal(err)
	}
	defer snapshot.Close()
	if err := os.WriteFile(source, []byte("changed"), 0600); err != nil {
		t.Fatal(err)
	}
	data, err := io.ReadAll(snapshot)
	if err != nil || string(data) != pngSignature+"fixture" {
		t.Fatalf("snapshot: %q %v", data, err)
	}
	if _, err := snapshot.WriteAt([]byte("x"), 0); err == nil {
		t.Fatal("snapshot is writable")
	}
	if err := snapshot.Truncate(0); err == nil {
		t.Fatal("snapshot can shrink")
	}
	if err := snapshot.Truncate(maximumInput); err == nil {
		t.Fatal("snapshot can grow")
	}
	for _, kind := range []string{"empty", "large", "fifo", "directory", "cancelled"} {
		t.Run(kind, func(t *testing.T) {
			path := filepath.Join(root, kind)
			ctx := context.Background()
			switch kind {
			case "fifo":
				if err := syscall.Mkfifo(path, 0600); err != nil {
					t.Fatal(err)
				}
			case "directory":
				if err := os.Mkdir(path, 0700); err != nil {
					t.Fatal(err)
				}
			default:
				if err := os.WriteFile(path, []byte{}, 0600); err != nil {
					t.Fatal(err)
				}
				if kind == "large" {
					if err := os.Truncate(path, maximumInput+1); err != nil {
						t.Fatal(err)
					}
				}
				if kind == "cancelled" {
					if err := os.WriteFile(path, []byte("x"), 0600); err != nil {
						t.Fatal(err)
					}
					var cancel context.CancelFunc
					ctx, cancel = context.WithCancel(ctx)
					cancel()
				}
			}
			f, err := sealedSnapshot(ctx, path)
			if err == nil {
				f.Close()
				t.Fatal("accepted invalid snapshot")
			}
		})
	}
}

func TestOutputPublication(t *testing.T) {
	for _, kind := range []string{"valid", "existing", "invalid-png", "mode", "hardlink", "symlink-target", "replaced-stage", "cancelled"} {
		t.Run(kind, func(t *testing.T) {
			root := t.TempDir()
			if err := os.Chmod(root, 0700); err != nil {
				t.Fatal(err)
			}
			parent, err := os.Open(root)
			if err != nil {
				t.Fatal(err)
			}
			target := strings.Repeat("a", 64) + ".png"
			stage, err := stagingAt(parent, target)
			if err != nil {
				parent.Close()
				t.Fatal(err)
			}
			defer stage.close()
			if _, err := stage.output.WriteString(pngSignature + "data"); err != nil {
				t.Fatal(err)
			}
			ctx := context.Background()
			check := func(err error) {
				t.Helper()
				if err != nil {
					t.Fatal(err)
				}
			}
			switch kind {
			case "existing":
				check(os.WriteFile(filepath.Join(root, target), []byte("old"), 0600))
			case "invalid-png":
				_, err := stage.output.WriteAt([]byte("invalid!"), 0)
				check(err)
			case "mode":
				check(stage.output.Chmod(0644))
			case "hardlink":
				check(os.Link(filepath.Join(root, stage.name), filepath.Join(root, "alias")))
			case "symlink-target":
				check(os.Symlink("missing", filepath.Join(root, target)))
			case "replaced-stage":
				check(os.Remove(filepath.Join(root, stage.name)))
				check(os.WriteFile(filepath.Join(root, stage.name), []byte("foreign"), 0600))
			case "cancelled":
				var cancel context.CancelFunc
				ctx, cancel = context.WithCancel(ctx)
				cancel()
			}
			err = stage.publish(ctx)
			if (err == nil) != (kind == "valid" || kind == "existing") {
				t.Fatalf("publication: %v", err)
			}
			if err == nil {
				data, err := os.ReadFile(filepath.Join(root, target))
				check(err)
				if string(data) != pngSignature+"data" {
					t.Fatal("published wrong bytes")
				}
			}
			if kind == "replaced-stage" {
				stage.close()
				data, err := os.ReadFile(filepath.Join(root, stage.name))
				check(err)
				if string(data) != "foreign" {
					t.Fatal("cleanup removed replacement")
				}
			}
		})
	}
}

func TestProcessParentAndMountOrder(t *testing.T) {
	parent, err := processParent([]byte("123 (worker ) spaced) S 42 0 0"))
	if err != nil || parent != 42 {
		t.Fatalf("parent: %d %v", parent, err)
	}
	for _, record := range []string{"", "123 missing", "123 (x) S", "123 (x) SS 42", "123 (x) S bad"} {
		if _, err := processParent([]byte(record)); err == nil {
			t.Fatal("accepted malformed proc stat")
		}
	}
	args := sandboxArguments("/runtime-fixture", true, []string{"transform", "png"})
	joined := strings.Join(args, " ")
	for _, required := range []string{"--as=1610612736:1610612736", "--cpu=10:10", "--fsize=67108864:67108864", "--nofile=128:128", "--unshare-all", "--disable-userns", "--size 134217728 --tmpfs /tmp", "--ro-bind-data 3 /input/source", "--bind-fd 4 /output/frame", "/runtime/image-worker transform png"} {
		if !strings.Contains(joined, required) {
			t.Fatalf("missing sandbox contract: %s", required)
		}
	}
	if strings.Index(joined, "--bind-fd") > strings.Index(joined, "--proc") {
		t.Fatal("descriptor mount occurs after proc replacement")
	}
	for _, mount := range []string{
		"--ro-bind-try /var/cache/fontconfig /var/cache/fontconfig",
		"--ro-bind /etc/fonts /etc/fonts",
		"--ro-bind /runtime-fixture /runtime",
		"--ro-bind /runtime-fixture/policy.xml /policy/policy.xml",
	} {
		if !strings.Contains(joined, mount) {
			t.Fatalf("missing cache or required runtime mount: %s", mount)
		}
	}
	if strings.Count(joined, "--ro-bind-try ") != 1 || strings.Contains(joined, "--ro-bind /var/cache/fontconfig ") {
		t.Fatal("only the generated font cache may be an optional read-only mount")
	}
}
