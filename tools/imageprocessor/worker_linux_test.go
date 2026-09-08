//go:build linux && (arm64 || amd64)

package imageprocessor

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func fixtureWorker(t *testing.T, data []byte) worker {
	t.Helper()
	root := t.TempDir()
	input := filepath.Join(root, "input")
	if err := os.WriteFile(input, data, 0600); err != nil {
		t.Fatal(err)
	}
	return worker{input: input, output: filepath.Join(root, "output"), normalized: filepath.Join(root, "normalized"), run: func(context.Context, []string, bool) (string, error) {
		t.Fatal("unexpected decoder invocation")
		return "", nil
	}}
}

func TestDetectMagic(t *testing.T) {
	for format, data := range map[string]string{"png": pngSignature, "jpeg": "\xff\xd8\xffdata\xff\xd9", "webp": "RIFF0000WEBP", "gif": "GIF89a", "bmp": "BM", "heic": "0000ftypheic", "avif": "0000ftypavif", "xpm": "/* XPM */", "ico": "\x00\x00\x01\x00", "pdf": "%PDF", "svg": "\ufeff <svg></svg>", "xml": "<?xml version='1.0'?>\n<!-- hi --><svg></svg>"} {
		t.Run(format, func(t *testing.T) {
			actual, err := fixtureWorker(t, []byte(data)).detect()
			if err != nil || actual != format {
				t.Fatalf("detect: %q, %v", actual, err)
			}
		})
	}
	for _, data := range []string{"", "\xff\xd8\xfftruncated", "RIFF", "0000ftyp", "<!DOCTYPE svg><svg/>", "<?xml?><not-svg/>", "<svg>" + strings.Repeat(" ", 1<<20) + "<!ENTITY bad 'x'>"} {
		if format, err := fixtureWorker(t, []byte(data)).detect(); err == nil {
			t.Fatalf("accepted hostile/unknown magic as %s", format)
		}
	}
}

func TestDeclarationChunkBoundary(t *testing.T) {
	for _, token := range []string{"<!DOCTYPE", "<!ENTITY", "<!doctype", "<!entity"} {
		for split := 1; split < len(token); split++ {
			data := strings.Repeat(" ", (1<<20)-split) + token
			if err := declarations(strings.NewReader(data)); err == nil {
				t.Fatalf("accepted declaration split at %d", split)
			}
		}
	}
}

func TestFixedPDFAndTransformArguments(t *testing.T) {
	w := fixtureWorker(t, []byte("%PDF"))
	var commands [][]string
	w.run = func(_ context.Context, args []string, capture bool) (string, error) {
		commands = append(commands, append([]string{}, args...))
		return "", nil
	}
	args := []string{"pdf", "before", "20", "30", "1", "2", "10", "15", "brightness", "50", "png"}
	if err := w.transform(context.Background(), args); err != nil {
		t.Fatal(err)
	}
	expected := [][]string{
		{ghostscript, "-q", "-dSAFER", "-dBATCH", "-dNOPAUSE", "-dNOPROMPT", "-dFirstPage=1", "-dLastPage=1", "-sDEVICE=pngalpha", "-r144", "-sOutputFile=" + w.normalized, w.input},
		{magick, "PNG:" + w.normalized + "[0]", "-crop", "10x15+1+2", "+repage", "-resize", "20x30!", "-modulate", "50,100,100", "PNG:" + w.output},
	}
	if !reflect.DeepEqual(commands, expected) {
		t.Fatalf("commands: %#v", commands)
	}
}

func TestTransformRejectsBeforeDecoder(t *testing.T) {
	base := []string{"png", "none", "20", "30", "0", "0", "0", "0", "none", "0", "png"}
	for _, change := range []struct {
		index int
		value string
	}{{0, "jpeg"}, {1, "else"}, {2, "0"}, {2, "01"}, {2, "16385"}, {3, "-1"}, {4, "1;exit"}, {8, "command"}, {9, "1"}, {10, "jpeg"}} {
		t.Run(fmt.Sprint(change), func(t *testing.T) {
			args := append([]string{}, base...)
			args[change.index] = change.value
			if err := fixtureWorker(t, []byte(pngSignature)).transform(context.Background(), args); err == nil {
				t.Fatal("accepted invalid transform")
			}
		})
	}
	for _, value := range []string{"NaN", "Inf", "-Inf", "201", "-201", "not-a-number"} {
		args := append([]string{}, base...)
		args[8], args[9] = "hue", value
		if err := fixtureWorker(t, []byte(pngSignature)).transform(context.Background(), args); err == nil {
			t.Fatal("accepted invalid effect")
		}
	}
	args := append([]string{}, base...)
	args[2], args[3] = "16384", "16384"
	if err := fixtureWorker(t, []byte(pngSignature)).transform(context.Background(), args); err == nil {
		t.Fatal("accepted oversized output")
	}
}

func TestIdentifyDimensions(t *testing.T) {
	for _, value := range []string{"20 30", "0 2", "01 2", "20 30\n", "20000 1", "16384 16384", "1 x"} {
		w := fixtureWorker(t, []byte(pngSignature))
		w.run = func(_ context.Context, args []string, capture bool) (string, error) {
			if !capture || !reflect.DeepEqual(args, []string{magick, "identify", "-format", "%w %h", "PNG:" + w.input + "[0]"}) {
				t.Fatalf("identify command: %v", args)
			}
			return value, nil
		}
		result, err := w.identify(context.Background(), "png")
		if (err == nil) != (value == "20 30") {
			t.Fatalf("dimensions %q: %v", value, err)
		}
		if err == nil && result != "20 30\n" {
			t.Fatal(result)
		}
	}
}

func TestExactPolicyRules(t *testing.T) {
	policy := "Path: /policy/policy.xml\n"
	for _, rule := range []policyRule{
		{"Delegate", map[string]string{"rights": "None", "pattern": "*"}},
		{"Filter", map[string]string{"rights": "None", "pattern": "*"}},
		{"Module", map[string]string{"rights": "None", "pattern": "*"}},
		{"Module", map[string]string{"rights": "Read Write", "pattern": "{PNG,JPEG,WEBP,GIF,BMP,HEIC,XPM,ICON,SVG,MVG}"}},
		{"Coder", map[string]string{"rights": "None", "pattern": "{HTTP,HTTPS,URL,MSL,TEXT,LABEL,CAPTION,EPHEMERAL,INLINE}"}},
	} {
		policy += "  Policy: " + rule.Domain + "\n    rights: " + rule.Attributes["rights"] + "\n    pattern: " + rule.Attributes["pattern"] + "\n"
	}
	policy += "  Policy: Resource\n    name: list-length\n    value: 8\n  Policy: Resource\n    name: width\n    value: 16KP\n  Policy: Resource\n    name: height\n    value: 16KP\n"
	if err := verifyPolicy(policy); err != nil {
		t.Fatal(err)
	}
	for _, bad := range []string{strings.Replace(policy, "rights: None", "rights: Read Write", 1), policy + "  Policy: Delegate\n    rights: Read Write\n    pattern: *\n", strings.Replace(policy, "value: 8", "value: 99", 1), strings.Replace(policy, "/policy/policy.xml", "/etc/ImageMagick/policy.xml", 1)} {
		if err := verifyPolicy(bad); err == nil {
			t.Fatal("accepted altered policy")
		}
	}
	buffer := &boundedBuffer{limit: 8}
	if _, err := buffer.Write(bytes.Repeat([]byte("x"), 9)); err == nil {
		t.Fatal("accepted oversized decoder output")
	}
}
