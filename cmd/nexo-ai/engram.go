package main

import (
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

const engramModule = "github.com/Gentleman-Programming/engram/v3/cmd/engram@latest"
const engramURL = "https://github.com/Gentleman-Programming/engram"

// installInteraction keeps terminal input and external processes out of planning
// and makes the optional, explicitly consented integration testable offline.
type installInteraction struct {
	input       io.Reader
	interactive bool
	execute     func(name string, args []string, env []string, stdin io.Reader, stdout, stderr io.Writer) error
}

func terminalInteraction() installInteraction {
	terminal := func(f *os.File) bool {
		info, err := f.Stat()
		if err != nil || info.Mode()&os.ModeCharDevice == 0 {
			return false
		}
		// /dev/null is also a character device, but it is not an interactive input.
		null, err := os.Stat(os.DevNull)
		return err != nil || !os.SameFile(info, null)
	}
	return installInteraction{input: os.Stdin, interactive: terminal(os.Stdin) && terminal(os.Stdout), execute: func(name string, args []string, env []string, stdin io.Reader, stdout, stderr io.Writer) error {
		cmd := exec.Command(name, args...)
		cmd.Env, cmd.Stdin, cmd.Stdout, cmd.Stderr = env, stdin, stdout, stderr
		return cmd.Run()
	}}
}

func engramExecutable(nexoHome string) string {
	name := "engram"
	if runtime.GOOS == "windows" {
		name += ".exe"
	}
	return filepath.Join(nexoHome, "bin", name)
}

func (c cli) offerEngram(names []string) {
	i := c.interaction
	if !i.interactive || i.input == nil || i.execute == nil {
		return
	}
	binary, err := filepath.Abs(engramExecutable(c.nexoHome))
	if err != nil {
		fmt.Fprintln(c.stderr, "warning: cannot resolve Engram path:", err)
		return
	}
	// Reuse our existing installation or one explicitly available on PATH; never
	// upgrade an existing Engram binary as a side effect of installing an agent.
	existing := false
	if info, err := os.Stat(binary); err == nil && info.Mode().IsRegular() {
		existing = true
	} else if found, err := exec.LookPath("engram"); err == nil {
		binary = found
		existing = true
	}
	fmt.Fprintln(c.stdout, "\nEngram™ provides persistent memory for AI coding agents, stored locally by default.")
	fmt.Fprintln(c.stdout, "Created by Alan Buscaglia (Gentleman Programming). Independent project:", engramURL)
	if existing {
		fmt.Fprintln(c.stdout, "Engram is already installed. This will configure its integration for:", strings.Join(names, ", "))
	} else {
		fmt.Fprintln(c.stdout, "Requires Go on PATH. Downloads and builds the latest released v3 using:")
		fmt.Fprintln(c.stdout, "  go install", engramModule)
		fmt.Fprintln(c.stdout, "Binary destination:", binary)
		fmt.Fprintln(c.stdout, "Then runs Engram setup for:", strings.Join(names, ", "))
	}
	fmt.Fprintln(c.stdout, "Setup may modify the selected agents' MCP/plugin configuration; no cloud enrollment is requested.")
	fmt.Fprint(c.stdout, "Install Engram and configure the selected agents? [y/N] ")
	if !engramConsent(i.input) {
		fmt.Fprintln(c.stdout, "Engram skipped.")
		return
	}
	env := os.Environ()
	if !existing {
		filtered := make([]string, 0, len(env)+1)
		for _, entry := range env {
			key, _, _ := strings.Cut(entry, "=")
			if !strings.EqualFold(key, "GOBIN") {
				filtered = append(filtered, entry)
			}
		}
		env = append(filtered, "GOBIN="+filepath.Dir(binary))
		if err := i.execute("go", []string{"install", engramModule}, env, i.input, c.stdout, c.stderr); err != nil {
			fmt.Fprintln(c.stderr, "warning: agent installation succeeded, but Engram installation failed:", err)
			fmt.Fprintln(c.stderr, "Install Engram manually:", engramURL)
			return
		}
	}
	for _, name := range names {
		target := name
		if target == "claude" {
			target = "claude-code"
		}
		if err := i.execute(binary, []string{"setup", target}, env, i.input, c.stdout, c.stderr); err != nil {
			fmt.Fprintf(c.stderr, "warning: agent installation succeeded, but Engram setup for %s failed: %v\n", name, err)
			fmt.Fprintf(c.stderr, "Retry manually: %q setup %s\n", binary, target)
			return
		}
	}
	fmt.Fprintln(c.stdout, "Engram setup completed. Restart the selected agents to load the integration.")
}

// Read exactly one line without buffering ahead. Keep the original terminal
// *os.File for subprocess stdin: wrapping it makes os/exec copy from the terminal
// in a goroutine which can remain blocked after the subprocess has exited.
func engramConsent(input io.Reader) bool {
	var line strings.Builder
	for range 1024 {
		var b [1]byte
		if _, err := io.ReadFull(input, b[:]); err != nil {
			return false
		}
		if b[0] == '\n' {
			answer := strings.ToLower(strings.TrimSpace(line.String()))
			return answer == "y" || answer == "yes"
		}
		line.WriteByte(b[0])
	}
	return false
}
