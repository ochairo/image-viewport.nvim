//go:build linux && (arm64 || amd64)

package imageprocessor

import (
	"context"
	"fmt"
	"io"
	"strings"
)

func Launch(ctx context.Context, args []string, output io.Writer) (int, error) {
	if len(args) == 2 && args[0] == "fingerprint" {
		digest, err := Fingerprint(args[1])
		if err != nil {
			return 70, err
		}
		_, err = fmt.Fprintln(output, digest)
		return 0, err
	}
	runtime, err := trustedRuntime()
	if err != nil {
		return 70, err
	}
	if len(args) == 1 && args[0] == "probe" {
		input, err := memfd()
		if err != nil {
			return 70, err
		}
		defer input.Close()
		if _, err := input.WriteString(pngSignature); err != nil {
			return 70, err
		}
		if err := seal(input); err != nil {
			return 70, err
		}
		code, result, err := runSandbox(ctx, runtime, input, nil, []string{"probe"})
		if err != nil {
			return code, err
		}
		if code != 0 || result != "dotfiles-image-processor-probe-v1\n" {
			return 70, fmt.Errorf("sandbox probe failed")
		}
		limit, _, err := runSandbox(ctx, runtime, input, nil, []string{"probe-process-limit"})
		if err != nil {
			return limit, err
		}
		if limit != 75 {
			return 70, fmt.Errorf("sandbox process-tree limit is ineffective")
		}
		_, err = io.WriteString(output, result)
		return 0, err
	}
	if len(args) == 2 && args[0] == "detect" {
		input, err := sealedSnapshot(ctx, args[1])
		if err != nil {
			return 70, err
		}
		defer input.Close()
		code, result, err := runSandbox(ctx, runtime, input, nil, []string{"detect"})
		if err != nil {
			return code, err
		}
		format := strings.TrimSpace(result)
		if code != 0 || formats[format] == "" {
			if code == 0 {
				code = 70
			}
			return code, fmt.Errorf("sandboxed format detection failed")
		}
		_, err = fmt.Fprintln(output, format)
		return 0, err
	}
	if len(args) < 3 || (args[0] != "identify" && args[0] != "transform") || formats[args[1]] == "" {
		return 64, fmt.Errorf("invalid image request")
	}
	if args[0] == "identify" && len(args) != 3 || args[0] == "transform" && len(args) != 14 {
		return 64, fmt.Errorf("invalid image operation arguments")
	}
	input, err := sealedSnapshot(ctx, args[2])
	if err != nil {
		return 70, err
	}
	defer input.Close()
	if args[0] == "identify" {
		code, result, err := runSandbox(ctx, runtime, input, nil, []string{"identify", args[1]})
		if err != nil {
			return code, err
		}
		if code != 0 {
			return code, fmt.Errorf("sandboxed identify failed")
		}
		_, err = io.WriteString(output, result)
		return 0, err
	}
	stage, err := newStaging(args[3])
	if err != nil {
		return 70, err
	}
	defer stage.close()
	arguments := append([]string{"transform", args[1]}, args[4:]...)
	code, _, err := runSandbox(ctx, runtime, input, stage.output, arguments)
	if err != nil {
		return code, err
	}
	if code != 0 {
		return code, fmt.Errorf("sandboxed transform failed")
	}
	if err := stage.publish(ctx); err != nil {
		return 70, err
	}
	_, err = fmt.Fprintln(output, args[3])
	return 0, err
}
