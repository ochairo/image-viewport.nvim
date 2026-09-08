//go:build linux && (arm64 || amd64)

package imageprocessor

import (
	"context"
	"fmt"
	"io"
	"net"
	"os"
	"os/exec"
	"reflect"
	"regexp"
	"strings"
	"time"
)

type policyRule struct {
	Domain     string
	Attributes map[string]string
}

func verifyPolicy(policy string) error {
	if !strings.Contains(policy, "Path: /policy/policy.xml") || strings.Contains(policy, "/etc/ImageMagick") {
		return fmt.Errorf("unexpected ImageMagick policy source")
	}
	managed := strings.SplitN(strings.SplitN(policy, "Path: /policy/policy.xml", 2)[1], "\nPath:", 2)[0]
	var rules []policyRule
	current := policyRule{}
	for _, line := range append(strings.Split(managed, "\n"), "  Policy: End") {
		if strings.HasPrefix(line, "  Policy: ") {
			switch current.Domain {
			case "Delegate", "Filter", "Module", "Coder":
				rules = append(rules, current)
			}
			current = policyRule{Domain: strings.TrimSpace(strings.TrimPrefix(line, "  Policy: ")), Attributes: map[string]string{}}
		} else if strings.HasPrefix(line, "    ") {
			name, value, found := strings.Cut(strings.TrimSpace(line), ":")
			if found && current.Attributes != nil {
				current.Attributes[name] = strings.Join(strings.Fields(value), " ")
			}
		}
	}
	expected := []policyRule{
		{"Delegate", map[string]string{"rights": "None", "pattern": "*"}},
		{"Filter", map[string]string{"rights": "None", "pattern": "*"}},
		{"Module", map[string]string{"rights": "None", "pattern": "*"}},
		{"Module", map[string]string{"rights": "Read Write", "pattern": "{PNG,JPEG,WEBP,GIF,BMP,HEIC,XPM,ICON,SVG,MVG}"}},
		{"Coder", map[string]string{"rights": "None", "pattern": "{HTTP,HTTPS,URL,MSL,TEXT,LABEL,CAPTION,EPHEMERAL,INLINE}"}},
	}
	if !reflect.DeepEqual(rules, expected) {
		return fmt.Errorf("ImageMagick security policy rules are not exact")
	}
	for _, pattern := range []string{`name: list-length\s+value: 8`, `name: width\s+value: 16KP`, `name: height\s+value: 16KP`} {
		if !regexp.MustCompile(pattern).MatchString(policy) {
			return fmt.Errorf("ImageMagick policy is incomplete")
		}
	}
	return nil
}

func workerProbe(ctx context.Context) error {
	policy, err := decoder(ctx, []string{magick, "identify", "-list", "policy"}, true)
	if err != nil {
		return err
	}
	if err := verifyPolicy(policy); err != nil {
		return err
	}
	for _, args := range [][]string{{magick, "identify", "@/input/source"}, {"/usr/bin/bwrap", "--unshare-user", "--", "/usr/bin/true"}} {
		bounded, cancel := context.WithTimeout(ctx, 2*time.Second)
		cmd := exec.CommandContext(bounded, args[0], args[1:]...)
		cmd.Env = decoderEnvironment
		cmd.WaitDelay = 100 * time.Millisecond
		err := cmd.Run()
		deadline := bounded.Err()
		cancel()
		if deadline != nil {
			return fmt.Errorf("sandbox negative probe timed out")
		}
		if err == nil {
			return fmt.Errorf("sandbox policy negative probe succeeded")
		}
		if _, ok := err.(*exec.ExitError); !ok {
			return fmt.Errorf("sandbox negative probe could not execute: %w", err)
		}
	}
	if _, err := os.Stat("/etc/passwd"); !os.IsNotExist(err) {
		return fmt.Errorf("sandbox exposes an unreviewed host path")
	}
	connection, err := net.DialTimeout("tcp4", "1.1.1.1:53", 250*time.Millisecond)
	if err == nil {
		connection.Close()
		return fmt.Errorf("sandbox network namespace is ineffective")
	}
	return nil
}

func processLimitProbe(ctx context.Context) error {
	if err := os.WriteFile("/proc/self/comm", []byte("worker ) spaced"), 0600); err != nil {
		return err
	}
	self, err := os.Executable()
	if err != nil {
		return err
	}
	var children []*exec.Cmd
	defer func() {
		for _, child := range children {
			_ = child.Process.Kill()
		}
		for _, child := range children {
			_ = child.Wait()
		}
	}()
	for i := 0; i < 40; i++ {
		if err := ctx.Err(); err != nil {
			return err
		}
		child := exec.Command(self, "probe-child")
		child.Env = decoderEnvironment
		if err := child.Start(); err != nil {
			return err
		}
		children = append(children, child)
	}
	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-time.After(3 * time.Second):
	}
	return fmt.Errorf("supervisor accepted too many sandbox descendants")
}

func Worker(ctx context.Context, args []string, output io.Writer) error {
	w := worker{input: "/input/source", output: "/output/frame", normalized: "/tmp/source.png", run: decoder}
	if len(args) == 1 {
		switch args[0] {
		case "version":
			_, err := fmt.Fprintln(output, "dotfiles-image-worker-v1")
			return err
		case "probe-child":
			select {
			case <-ctx.Done():
				return ctx.Err()
			case <-time.After(3 * time.Second):
				return nil
			}
		case "probe-process-limit":
			return processLimitProbe(ctx)
		case "probe":
			if err := workerProbe(ctx); err != nil {
				return err
			}
			_, err := fmt.Fprintln(output, "dotfiles-image-processor-probe-v1")
			return err
		case "detect":
			format, err := w.detect()
			if err != nil {
				return err
			}
			_, err = fmt.Fprintln(output, format)
			return err
		}
	}
	if len(args) == 2 && args[0] == "identify" {
		result, err := w.identify(ctx, args[1])
		if err != nil {
			return err
		}
		_, err = io.WriteString(output, result)
		return err
	}
	if len(args) >= 2 && args[0] == "transform" {
		return w.transform(ctx, args[1:])
	}
	return fmt.Errorf("invalid image worker request")
}
