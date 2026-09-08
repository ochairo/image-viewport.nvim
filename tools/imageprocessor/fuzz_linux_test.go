//go:build linux && (arm64 || amd64)

package imageprocessor

import (
	"bytes"
	"io"
	"strconv"
	"testing"
)

type fragmentedSVG struct{ io.Reader }

func (r fragmentedSVG) Read(data []byte) (int, error) {
	if len(data) > 7 {
		data = data[:7]
	}
	return r.Reader.Read(data)
}

func FuzzSVGDeclarations(f *testing.F) {
	for _, seed := range []string{"<svg/>", "<!DOCTYPE svg>", "<!entity x 'x'>", "123456<!DoCtYpE svg>"} {
		f.Add([]byte(seed))
	}
	f.Fuzz(func(t *testing.T, data []byte) {
		if len(data) > 65536 {
			return
		}
		upper := append([]byte(nil), data...)
		for i, b := range upper {
			if b >= 'a' && b <= 'z' {
				upper[i] = b - 'a' + 'A'
			}
		}
		forbidden := bytes.Contains(upper, []byte("<!DOCTYPE")) || bytes.Contains(upper, []byte("<!ENTITY"))
		if err := declarations(fragmentedSVG{bytes.NewReader(data)}); forbidden && err == nil {
			t.Fatal("declaration crossed an unchecked read boundary")
		}
	})
}

func FuzzImageDimensions(f *testing.F) {
	for _, seed := range []string{"1", "16384", "16385", "-1", "NaN", "1e9"} {
		f.Add(seed)
	}
	f.Fuzz(func(t *testing.T, value string) {
		n, err := boundedInteger(value, 1, 16384)
		if err != nil {
			return
		}
		parsed, parseErr := strconv.Atoi(value)
		if parseErr != nil || parsed != n || n < 1 || n > 16384 {
			t.Fatal("unbounded dimension admitted")
		}
	})
}
