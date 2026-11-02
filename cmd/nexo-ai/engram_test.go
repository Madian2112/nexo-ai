package main

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
	"time"
)

func TestInstallEngramOffer(t *testing.T) {
	for _, tc := range []struct {
		name, input            string
		interactive, dry, fail bool
		wantCalls              int
	}{
		{name: "accept", input: "yes\n", interactive: true, wantCalls: 2},
		{name: "decline", input: "n\n", interactive: true},
		{name: "default no", input: "\n", interactive: true},
		{name: "EOF", interactive: true},
		{name: "invalid answer", input: "maybe\n", interactive: true},
		{name: "partial answer without enter", input: "yes", interactive: true},
		{name: "noninteractive", input: "yes\n"},
		{name: "dry run", input: "yes\n", interactive: true, dry: true},
		{name: "download failure", input: "y\n", interactive: true, fail: true, wantCalls: 1},
	} {
		t.Run(tc.name, func(t *testing.T) {
			home := setup(t)
			t.Setenv("PATH", t.TempDir())
			var out, errOut bytes.Buffer
			var calls [][]string
			opts := installInteraction{input: strings.NewReader(tc.input), interactive: tc.interactive,
				execute: func(name string, args []string, env []string, stdin io.Reader, stdout, stderr io.Writer) error {
					calls = append(calls, append([]string{name}, args...))
					if name == "go" {
						if !strings.Contains(strings.Join(env, "\n"), "GOBIN="+filepath.Join(home, ".nexo-ai", "bin")) {
							t.Error("missing isolated GOBIN")
						}
					}
					if tc.fail {
						return errors.New("offline")
					}
					return nil
				},
			}
			args := []string{"install", "claude", "--source", example(t)}
			if tc.dry {
				args = append(args, "--dry-run")
			}
			code := runCLI(args, &out, &errOut, time.Now, opts)
			if code != 0 {
				t.Fatalf("optional offer changed install result: %d %s", code, errOut.String())
			}
			if len(calls) != tc.wantCalls {
				t.Fatalf("calls: %v", calls)
			}
			prompt := strings.Contains(out.String(), "Install Engram")
			if prompt != (tc.interactive && !tc.dry) {
				t.Fatalf("unexpected prompt: %s", out.String())
			}
			if prompt && (!strings.Contains(out.String(), "Alan Buscaglia") || !strings.Contains(out.String(), "Gentleman Programming")) {
				t.Fatal("missing attribution")
			}
			if tc.wantCalls == 2 {
				if !reflect.DeepEqual(calls[0], []string{"go", "install", engramModule}) {
					t.Fatalf("install: %v", calls)
				}
				if !reflect.DeepEqual(calls[1], []string{engramExecutable(filepath.Join(home, ".nexo-ai")), "setup", "claude-code"}) {
					t.Fatalf("setup: %v", calls)
				}
			}
			if tc.fail && !strings.Contains(errOut.String(), "warning:") {
				t.Fatal("failure not reported")
			}
			if tc.dry {
				if _, err := os.Stat(filepath.Join(home, ".nexo-ai")); !os.IsNotExist(err) {
					t.Fatalf("dry-run wrote: %v", err)
				}
			}
		})
	}
}

func TestEngramOfferAllAndExistingBinary(t *testing.T) {
	home := setup(t)
	bin := engramExecutable(filepath.Join(home, ".nexo-ai"))
	if err := os.MkdirAll(filepath.Dir(bin), 0755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(bin, []byte("existing"), 0755); err != nil {
		t.Fatal(err)
	}
	var calls [][]string
	var out bytes.Buffer
	c := cli{stdout: &out, stderr: &out, home: home, nexoHome: filepath.Join(home, ".nexo-ai"), interaction: installInteraction{
		input: strings.NewReader("y\n"), interactive: true,
		execute: func(name string, args []string, _ []string, _ io.Reader, _, _ io.Writer) error {
			calls = append(calls, append([]string{name}, args...))
			return nil
		},
	}}
	c.offerEngram([]string{"claude", "codex", "kiro", "opencode"})
	want := [][]string{{bin, "setup", "claude-code"}, {bin, "setup", "codex"}, {bin, "setup", "kiro"}, {bin, "setup", "opencode"}}
	if !reflect.DeepEqual(calls, want) {
		t.Fatalf("calls: %v", calls)
	}
}

// Keep stdin open after the answer, like a terminal. The child exits without
// reading it; a buffered stdin wrapper must not keep os/exec.Wait blocked.
func TestEngramSubprocessStdin(t *testing.T) {
	if os.Getenv("NEXO_TEST_ENGRAM_CHILD") == "1" {
		return
	}
	if os.Getenv("NEXO_TEST_ENGRAM_PARENT") == "1" {
		home := setup(t)
		input, writer, err := os.Pipe()
		if err != nil {
			t.Fatal(err)
		}
		defer input.Close()
		defer writer.Close()
		if _, err := writer.WriteString("yes\n"); err != nil {
			t.Fatal(err)
		}
		interaction := terminalInteraction()
		execute := interaction.execute
		interaction.input, interaction.interactive = input, true
		interaction.execute = func(_ string, _ []string, env []string, stdin io.Reader, out, errOut io.Writer) error {
			return execute(os.Args[0], []string{"-test.run=^TestEngramSubprocessStdin$"}, append(env, "NEXO_TEST_ENGRAM_CHILD=1"), stdin, out, errOut)
		}
		var out bytes.Buffer
		c := cli{stdout: &out, stderr: &out, home: home, nexoHome: filepath.Join(home, ".nexo-ai"), interaction: interaction}
		c.offerEngram([]string{"claude"})
		if !strings.Contains(out.String(), "Engram setup completed") {
			t.Fatal(out.String())
		}
		return
	}
	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, os.Args[0], "-test.run=^TestEngramSubprocessStdin$")
	cmd.Env = append(os.Environ(), "NEXO_TEST_ENGRAM_PARENT=1")
	output, err := cmd.CombinedOutput()
	if err != nil {
		t.Fatalf("subprocess did not finish: %v\n%s", err, output)
	}
}
