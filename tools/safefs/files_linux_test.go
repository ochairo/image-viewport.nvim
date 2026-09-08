//go:build linux

package safefs

import (
	"os"
	"path/filepath"
	"syscall"
	"testing"
)

func TestReadRejectsLinksSpecialFilesAndTraversal(t *testing.T) {
	root := t.TempDir()
	source := filepath.Join(root,"source")
	if err := os.WriteFile(source, []byte("canonical"), 0600); err != nil { t.Fatal(err) }
	if _, err := Read(root,"source",100); err != nil { t.Fatal(err) }
	if err := os.Symlink(source,filepath.Join(root,"alias")); err != nil { t.Fatal(err) }
	if _, err := Read(root,"alias",100); err == nil { t.Fatal("symlink accepted") }
	if err := os.Link(source,filepath.Join(root,"hardlink")); err != nil { t.Fatal(err) }
	if _, err := Read(root,"source",100); err == nil { t.Fatal("hardlink accepted") }
	if err := syscall.Mkfifo(filepath.Join(root,"fifo"),0600); err != nil { t.Fatal(err) }
	for _, name := range []string{"fifo","../escape","/absolute","./source"} {
		if _, err := Read(root,name,100); err == nil { t.Fatalf("accepted %s",name) }
	}
}

func TestDestinationSubstitutionIsRejected(t *testing.T) {
	parent := t.TempDir()
	foreign := filepath.Join(parent,"foreign")
	if err := os.Mkdir(foreign,0700); err != nil { t.Fatal(err) }
	path := filepath.Join(parent,"output")
	_,err := newDestination(path,func() {
		if err := os.Remove(path); err != nil { t.Fatal(err) }
		if err := os.Symlink("foreign",path); err != nil { t.Fatal(err) }
	})
	if err == nil { t.Fatal("substituted output symlink admitted") }
	entries,err := os.ReadDir(foreign)
	if err != nil || len(entries) != 0 { t.Fatal("foreign output modified") }
	if err := os.Chmod(parent,0777); err != nil { t.Fatal(err) }
	if _,err := NewDestination(filepath.Join(parent,"other")); err == nil { t.Fatal("shared writable parent admitted") }
}

func TestSourceRootSymlinkAncestryIsRejected(t *testing.T) {
	parent := t.TempDir()
	real := filepath.Join(parent,"real")
	if err := os.MkdirAll(filepath.Join(real,"source"),0700); err != nil { t.Fatal(err) }
	if err := os.WriteFile(filepath.Join(real,"source/file"),[]byte("source"),0600); err != nil { t.Fatal(err) }
	if err := os.Symlink(real,filepath.Join(parent,"alias")); err != nil { t.Fatal(err) }
	if _,err := Read(filepath.Join(parent,"alias/source"),"file",100); err == nil { t.Fatal("source ancestor symlink admitted") }
}
