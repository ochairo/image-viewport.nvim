//go:build linux && (arm64 || amd64)

// Package imageprocessor owns the fixed, isolated Neovim image decoder runtime.
package imageprocessor

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"math"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
	"time"
)

const magick = "/usr/bin/magick-im7.q16"
const ghostscript = "/usr/bin/gs"
const maximumInput = 128 << 20
const maximumOutput = 32 << 20
const maximumPixels = 16 << 20
const pngSignature = "\x89PNG\r\n\x1a\n"

var formats = map[string]string{"png": "PNG", "jpeg": "JPEG", "webp": "WEBP", "gif": "GIF", "bmp": "BMP", "heic": "HEIC", "xpm": "XPM", "ico": "ICO", "avif": "AVIF", "svg": "MSVG", "xml": "MSVG", "pdf": "PDF"}
var integer = regexp.MustCompile(`^(0|[1-9][0-9]{0,5})$`)
var dimensions = regexp.MustCompile(`^[1-9][0-9]{0,5} [1-9][0-9]{0,5}$`)
var xmlSVG = regexp.MustCompile(`(?s)\?>\s*(?:<!--.*?-->\s*)*<svg(?:\s|>)`)
var decoderEnvironment = []string{"HOME=/tmp/home", "LANG=C.UTF-8", "LC_ALL=C.UTF-8", "MAGICK_CONFIGURE_PATH=/policy", "MAGICK_TEMPORARY_PATH=/tmp", "PATH=/usr/bin:/bin", "TMPDIR=/tmp"}

type boundedBuffer struct {
	bytes.Buffer
	limit int
}

func (b *boundedBuffer) Write(data []byte) (int, error) {
	if len(data) > b.limit-b.Len() {
		return 0, fmt.Errorf("decoder output exceeds limit")
	}
	return b.Buffer.Write(data)
}

func decoder(ctx context.Context, args []string, capture bool) (string, error) {
	ctx, cancel := context.WithTimeout(ctx, 9*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, args[0], args[1:]...)
	cmd.Env = decoderEnvironment
	cmd.WaitDelay = 250 * time.Millisecond
	out, diagnostics := &boundedBuffer{limit: 64 << 10}, &boundedBuffer{limit: 64 << 10}
	cmd.Stdout, cmd.Stderr = io.Discard, diagnostics
	if capture {
		cmd.Stdout = out
	}
	if err := cmd.Run(); err != nil {
		return "", fmt.Errorf("sandboxed decoder rejected the image: %w", err)
	}
	return out.String(), nil
}

type worker struct {
	input, output, normalized string
	run                       func(context.Context, []string, bool) (string, error)
}

func boundedInteger(value string, minimum, maximum int) (int, error) {
	if !integer.MatchString(value) {
		return 0, fmt.Errorf("invalid numeric argument")
	}
	n, err := strconv.Atoi(value)
	if err != nil || n < minimum || n > maximum {
		return 0, fmt.Errorf("numeric argument is outside the reviewed range")
	}
	return n, nil
}

func declarations(source io.Reader) error {
	buffer := make([]byte, 1<<20)
	var tail []byte
	for {
		n, err := source.Read(buffer)
		if n > 0 {
			inspected := bytes.ToUpper(append(tail, buffer[:n]...))
			if bytes.Contains(inspected, []byte("<!DOCTYPE")) || bytes.Contains(inspected, []byte("<!ENTITY")) {
				return fmt.Errorf("SVG declarations are not permitted")
			}
			start := len(inspected) - 16
			if start < 0 {
				start = 0
			}
			tail = append([]byte{}, inspected[start:]...)
		}
		if err == io.EOF {
			return nil
		}
		if err != nil {
			return err
		}
	}
}

func (w worker) detect() (string, error) {
	source, err := os.Open(w.input)
	if err != nil {
		return "", err
	}
	defer source.Close()
	header := make([]byte, 8192)
	n, err := source.Read(header)
	if err != nil && err != io.EOF {
		return "", err
	}
	header = header[:n]
	var tail [2]byte
	info, err := source.Stat()
	if err != nil {
		return "", err
	}
	if info.Size() >= 2 {
		if _, err := source.ReadAt(tail[:], info.Size()-2); err != nil {
			return "", err
		}
	}
	starts := func(value string) bool { return bytes.HasPrefix(header, []byte(value)) }
	brand := ""
	if len(header) >= 12 {
		brand = string(header[4:12])
	}
	format := ""
	switch {
	case starts(pngSignature):
		format = "png"
	case starts("\xff\xd8\xff") && string(tail[:]) == "\xff\xd9":
		format = "jpeg"
	case starts("RIFF") && len(header) >= 12 && string(header[8:12]) == "WEBP":
		format = "webp"
	case starts("GIF87a") || starts("GIF89a"):
		format = "gif"
	case starts("BM"):
		format = "bmp"
	case brand == "ftypheic" || brand == "ftypheix" || brand == "ftyphevc" || brand == "ftyphevx" || brand == "ftypmif1":
		format = "heic"
	case brand == "ftypavif" || brand == "ftypavis":
		format = "avif"
	case starts("/* XPM */"):
		format = "xpm"
	case starts("\x00\x00\x01\x00"):
		format = "ico"
	case starts("%PDF"):
		format = "pdf"
	default:
		text := strings.ToValidUTF8(string(header), "")
		upper := strings.ToUpper(text)
		if strings.Contains(upper, "<!DOCTYPE") || strings.Contains(upper, "<!ENTITY") {
			return "", fmt.Errorf("SVG declarations are not permitted")
		}
		compact := strings.TrimLeft(text, "\ufeff\x00\t\r\n ")
		if strings.HasPrefix(compact, "<svg") {
			format = "svg"
		}
		if strings.HasPrefix(compact, "<?xml") && xmlSVG.MatchString(compact) {
			format = "xml"
		}
	}
	if format == "" {
		return "", fmt.Errorf("input magic is unsupported")
	}
	if format == "svg" || format == "xml" {
		if _, err := source.Seek(0, io.SeekStart); err != nil {
			return "", err
		}
		if err := declarations(source); err != nil {
			return "", err
		}
	}
	return format, nil
}

func (w worker) inputSpec(ctx context.Context, format string) (string, error) {
	actual, err := w.detect()
	if err != nil {
		return "", err
	}
	if actual != format || formats[format] == "" {
		return "", fmt.Errorf("input magic changed")
	}
	if format == "pdf" {
		_, err := w.run(ctx, []string{ghostscript, "-q", "-dSAFER", "-dBATCH", "-dNOPAUSE", "-dNOPROMPT", "-dFirstPage=1", "-dLastPage=1", "-sDEVICE=pngalpha", "-r144", "-sOutputFile=" + w.normalized, w.input}, false)
		if err != nil {
			return "", err
		}
		return "PNG:" + w.normalized + "[0]", nil
	}
	return formats[format] + ":" + w.input + "[0]", nil
}

func (w worker) identify(ctx context.Context, format string) (string, error) {
	source, err := w.inputSpec(ctx, format)
	if err != nil {
		return "", err
	}
	result, err := w.run(ctx, []string{magick, "identify", "-format", "%w %h", source}, true)
	if err != nil {
		return "", err
	}
	if !dimensions.MatchString(result) {
		return "", fmt.Errorf("decoder returned invalid dimensions")
	}
	parts := strings.Split(result, " ")
	width, err := boundedInteger(parts[0], 1, 16384)
	if err != nil {
		return "", err
	}
	height, err := boundedInteger(parts[1], 1, 16384)
	if err != nil {
		return "", err
	}
	if width*height > maximumPixels {
		return "", fmt.Errorf("source dimensions exceed the reviewed limit")
	}
	return result + "\n", nil
}

func (w worker) transform(ctx context.Context, args []string) error {
	if len(args) != 11 {
		return fmt.Errorf("invalid transform request")
	}
	format, cropStage := args[0], args[1]
	if cropStage != "none" && cropStage != "before" && cropStage != "after" {
		return fmt.Errorf("invalid crop stage")
	}
	numbers := make([]int, 6)
	for i, value := range args[2:8] {
		minimum := 0
		if i < 2 {
			minimum = 1
		}
		n, err := boundedInteger(value, minimum, 16384)
		if err != nil {
			return err
		}
		numbers[i] = n
	}
	width, height := numbers[0], numbers[1]
	if width*height > maximumPixels {
		return fmt.Errorf("output dimensions exceed the reviewed limit")
	}
	effect, value := args[8], args[9]
	if args[10] != "png" || effect != "none" && effect != "brightness" && effect != "saturation" && effect != "hue" {
		return fmt.Errorf("invalid transform mode")
	}
	numeric := float64(0)
	if effect == "none" {
		if value != "0" {
			return fmt.Errorf("invalid effect value")
		}
	} else {
		var err error
		numeric, err = strconv.ParseFloat(value, 64)
		if err != nil || math.IsNaN(numeric) || numeric < -200 || numeric > 200 {
			return fmt.Errorf("effect value is outside the reviewed range")
		}
	}
	source, err := w.inputSpec(ctx, format)
	if err != nil {
		return err
	}
	command := []string{magick, source}
	geometry := fmt.Sprintf("%dx%d+%d+%d", numbers[4], numbers[5], numbers[2], numbers[3])
	if cropStage == "before" {
		command = append(command, "-crop", geometry, "+repage")
	}
	command = append(command, "-resize", fmt.Sprintf("%dx%d!", width, height))
	if cropStage == "after" {
		command = append(command, "-crop", geometry, "+repage")
	}
	modulation := strconv.FormatFloat(numeric, 'f', -1, 64)
	switch effect {
	case "brightness":
		command = append(command, "-modulate", modulation+",100,100")
	case "saturation":
		command = append(command, "-modulate", "100,"+modulation+",100")
	case "hue":
		command = append(command, "-modulate", "100,100,"+modulation)
	}
	_, err = w.run(ctx, append(command, "PNG:"+w.output), false)
	return err
}
