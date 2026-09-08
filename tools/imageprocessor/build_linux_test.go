//go:build linux && (arm64 || amd64)

package imageprocessor

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"

)

func writeFixture(t *testing.T, path, text string, mode os.FileMode) {
	t.Helper()
	if err := os.WriteFile(path, []byte(text), mode); err != nil { t.Fatal(err) }
}

func sourceFixture(t *testing.T) string {
	t.Helper()
	root := filepath.Join(t.TempDir(), "plugin with spaces")
	for _, name := range []string{"tools", "scripts", "runtime"} {
		if err := os.MkdirAll(filepath.Join(root,name),0700); err != nil { t.Fatal(err) }
	}
	writeFixture(t,filepath.Join(root,"tools/go.mod"),"module fixture\n\ngo 1.25\n",0600)
	writeFixture(t,filepath.Join(root,"tools/source.go"),"package fixture\n",0600)
	for _, name := range []string{"build","tool","go-supervise.sh"} { writeFixture(t,filepath.Join(root,"scripts",name),"#!/bin/sh\nexit 0\n",0700) }
	writeFixture(t,filepath.Join(root,"runtime/policy.xml"),"policy fixture",0600)
	return root
}

func releaseFixture(t *testing.T, root string) string {
	t.Helper()
	source, err := SourceDigest(root)
	if err != nil { t.Fatal(err) }
	stage, err := os.MkdirTemp(filepath.Join(root,"runtime"),".fixture-")
	if err != nil { t.Fatal(err) }
	for _, name := range []string{"image-launch","image-worker"} { writeFixture(t,filepath.Join(stage,name),"synthetic executable, never run",0700) }
	writeFixture(t,filepath.Join(stage,"policy.xml"),"policy fixture",0600)
	writeFixture(t,filepath.Join(stage,"source.sha256"),source+"\n",0600)
	digest, err := Fingerprint(stage)
	if err != nil { t.Fatal(err) }
	writeFixture(t,filepath.Join(stage,runtimeMarker),"image-processor:"+digest+"\n",0600)
	name := "image-processor-"+digest
	target := filepath.Join(root,"runtime",name)
	if err := os.Rename(stage,target); err != nil { t.Fatal(err) }
	if err := os.Symlink(name,filepath.Join(root,"runtime/current")); err != nil { t.Fatal(err) }
	return target
}

func TestStandaloneRuntimeAdmission(t *testing.T) {
	for _, defect := range []string{"none","source-edit","source-add","source-delete","source-link","tamper","missing-worker","marker","absolute-selector","outside-selector","missing-selector","writable","relocated"} {
		t.Run(defect,func(t *testing.T) {
			root := sourceFixture(t)
			release := releaseFixture(t,root)
			check := func(err error) { t.Helper(); if err != nil { t.Fatal(err) } }
			switch defect {
			case "source-edit": writeFixture(t,filepath.Join(root,"tools/source.go"),"package changed\n",0600)
			case "source-add": writeFixture(t,filepath.Join(root,"tools/new.go"),"package fixture\n",0600)
			case "source-delete": check(os.Remove(filepath.Join(root,"tools/source.go")))
			case "source-link": check(os.Symlink("source.go",filepath.Join(root,"tools/alias.go")))
			case "tamper": writeFixture(t,filepath.Join(release,"policy.xml"),"changed",0600)
			case "missing-worker": check(os.Remove(filepath.Join(release,"image-worker")))
			case "marker": writeFixture(t,filepath.Join(release,runtimeMarker),"foreign\n",0600)
			case "absolute-selector", "outside-selector", "missing-selector":
				check(os.Remove(filepath.Join(root,"runtime/current")))
				if defect == "absolute-selector" { check(os.Symlink(release,filepath.Join(root,"runtime/current"))) }
				if defect == "outside-selector" { check(os.Symlink("../elsewhere",filepath.Join(root,"runtime/current"))) }
			case "writable": check(os.Chmod(release,0770))
			case "relocated":
				renamed := filepath.Join(filepath.Dir(root),"moved plugin with spaces")
				check(os.Rename(root,renamed)); root = renamed; release = filepath.Join(root,"runtime",filepath.Base(release))
			}
			_, err := admitRuntime(filepath.Join(release,"image-launch"))
			if (err == nil) != (defect == "none" || defect == "relocated") { t.Fatalf("admission: %v",err) }
		})
	}
}

func TestBuildFailuresPreserveSelection(t *testing.T) {
	for _, defect := range []string{"compile-failure","concurrent","foreign-selector"} {
		t.Run(defect,func(t *testing.T) {
			root := sourceFixture(t)
			release := releaseFixture(t,root)
			if defect == "foreign-selector" {
				if err := os.Remove(filepath.Join(root,"runtime/current")); err != nil { t.Fatal(err) }
				writeFixture(t,filepath.Join(root,"runtime/current"),"keep foreign",0600)
			}
			if defect == "concurrent" {
				lock, err := os.OpenFile(filepath.Join(root,"runtime/.build-lock"),os.O_RDWR|os.O_CREATE,0600)
				if err != nil { t.Fatal(err) }; defer lock.Close()
				if err := syscall.Flock(int(lock.Fd()),syscall.LOCK_EX|syscall.LOCK_NB); err != nil { t.Fatal(err) }
			}
			if err := Build(context.Background(),root); err == nil { t.Fatal("invalid build succeeded") }
			if defect == "foreign-selector" {
				data,err := os.ReadFile(filepath.Join(root,"runtime/current"))
				if err != nil || string(data) != "keep foreign" { t.Fatal("foreign selector changed") }
			} else {
				name,err := os.Readlink(filepath.Join(root,"runtime/current"))
				if err != nil || name != filepath.Base(release) { t.Fatal("previous selection lost") }
			}
			entries,err := os.ReadDir(filepath.Join(root,"runtime"))
			if err != nil { t.Fatal(err) }
			for _,entry := range entries { if strings.HasPrefix(entry.Name(),".build-") && entry.IsDir() { t.Fatal("owned staging leaked") } }
		})
	}
}

func TestBuildRebuildAndRelocation(t *testing.T) {
	original, err := filepath.Abs("../..")
	if err != nil { t.Fatal(err) }
	root := filepath.Join(t.TempDir(),"plugin build with spaces")
	if err := os.Mkdir(root,0700); err != nil { t.Fatal(err) }
	if err := os.CopyFS(filepath.Join(root,"tools"),os.DirFS(filepath.Join(original,"tools"))); err != nil { t.Fatal(err) }
	for _, name := range []string{"scripts","runtime"} {
		if err := os.Mkdir(filepath.Join(root,name),0700); err != nil { t.Fatal(err) }
	}
	for _, name := range []string{"scripts/build","scripts/tool","scripts/go-supervise.sh","runtime/policy.xml"} {
		data,err := os.ReadFile(filepath.Join(original,name))
		if err != nil { t.Fatal(err) }
		writeFixture(t,filepath.Join(root,name),string(data),0600)
	}
	if err := Build(context.Background(),root); err != nil { t.Fatal(err) }
	name,err := selection(filepath.Join(root,"runtime"))
	if err != nil { t.Fatal(err) }
	launcher := filepath.Join(root,"runtime",name,"image-launch")
	if _,err := admitRuntime(launcher); err != nil { t.Fatal(err) }
	writeFixture(t,filepath.Join(root,"tools/imageprocessor/new_source.go"),"package imageprocessor\n// changed source inventory\n",0600)
	if _,err := admitRuntime(launcher); err == nil { t.Fatal("stale build admitted") }
	if err := Build(context.Background(),root); err != nil { t.Fatal(err) }
	renamed := filepath.Join(filepath.Dir(root),"relocated plugin")
	if err := os.Rename(root,renamed); err != nil { t.Fatal(err) }
	name,err = selection(filepath.Join(renamed,"runtime"))
	if err != nil { t.Fatal(err) }
	if _,err := admitRuntime(filepath.Join(renamed,"runtime",name,"image-launch")); err != nil { t.Fatal(err) }
}

func TestForeignOutputDirectoryPreserved(t *testing.T) {
	root := filepath.Join(t.TempDir(),"foreign")
	if err := os.Mkdir(root,0755); err != nil { t.Fatal(err) }
	writeFixture(t,filepath.Join(root,"keep"),"foreign",0600)
	stage,err := newStaging(filepath.Join(root,strings.Repeat("a",64)+".png"))
	if err == nil { stage.close(); t.Fatal("non-private output directory admitted") }
	data,err := os.ReadFile(filepath.Join(root,"keep"))
	if err != nil || string(data) != "foreign" { t.Fatal("foreign output changed") }
}
