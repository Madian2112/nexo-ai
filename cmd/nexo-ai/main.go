// Command nexo-ai compiles one canonical source directory into the native
// configuration of Claude Code, Kiro CLI and Codex.
package main

import (
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"

	"github.com/Madian2112/nexo-ai/internal/adapters"
	"github.com/Madian2112/nexo-ai/internal/hookrun"
	"github.com/Madian2112/nexo-ai/internal/importer"
	"github.com/Madian2112/nexo-ai/internal/install"
	"github.com/Madian2112/nexo-ai/internal/measure"
	"github.com/Madian2112/nexo-ai/internal/model"
	"github.com/Madian2112/nexo-ai/internal/plan"
	"github.com/Madian2112/nexo-ai/internal/source"
)

// version is set at release time with -ldflags "-X main.version=...".
var version = "0.3.0-preview.1"

const usage = `nexo-ai: one source of skills, MCP, hooks, permissions, rules, soul and agents for every coding agent.

Usage:
  nexo-ai init [--source DIR] [--dry-run]
  nexo-ai import <agent|all> [--source DIR] [--dry-run]
  nexo-ai install <agent|all> [--source DIR] [--dry-run] [--copy-skills]
  nexo-ai measure [agent|all] [--source DIR] [--json]
  nexo-ai hook opencode --hooks FILE   (called by the generated OpenCode plugin)
  nexo-ai version

Agents: %s

After a successful interactive install, you can optionally install/configure Engram.
Dry runs and noninteractive installs never prompt or install Engram.

Environment:
  NEXO_HOME  nexo-ai home (default ~/.nexo-ai). The source defaults to $NEXO_HOME/source,
             install state lives in $NEXO_HOME/state and backups in $NEXO_HOME/backups.
`

func main() {
	os.Exit(runCLI(os.Args[1:], os.Stdout, os.Stderr, time.Now, terminalInteraction()))
}

type cli struct {
	stdout, stderr io.Writer
	home, nexoHome string
	interaction    installInteraction
}

func run(args []string, stdout, stderr io.Writer, now func() time.Time) int {
	return runCLI(args, stdout, stderr, now, installInteraction{})
}

func runCLI(args []string, stdout, stderr io.Writer, now func() time.Time, interaction installInteraction) int {
	if len(args) == 0 || args[0] == "help" || args[0] == "-h" || args[0] == "--help" {
		fmt.Fprintf(stdout, usage, strings.Join(adapters.Names(), ", "))
		if len(args) == 0 {
			return 2
		}
		return 0
	}
	home, err := os.UserHomeDir()
	if err != nil {
		fmt.Fprintln(stderr, "error: cannot resolve home directory:", err)
		return 1
	}
	c := cli{stdout: stdout, stderr: stderr, home: home, nexoHome: os.Getenv("NEXO_HOME"), interaction: interaction}
	if c.nexoHome == "" {
		c.nexoHome = filepath.Join(home, ".nexo-ai")
	}
	switch args[0] {
	case "init":
		return c.initCmd(args[1:])
	case "import":
		return c.importCmd(args[1:], now)
	case "install":
		return c.install(args[1:], now)
	case "measure":
		return c.measure(args[1:])
	case "hook":
		return c.hook(args[1:], os.Stdin)
	case "version":
		fmt.Fprintln(stdout, "nexo-ai", version)
		return 0
	default:
		fmt.Fprintf(stderr, "error: unknown command %q\n", args[0])
		fmt.Fprintf(stderr, usage, strings.Join(adapters.Names(), ", "))
		return 2
	}
}

// parse accepts flags before or after the single positional argument.
func parse(fs *flag.FlagSet, args []string) (string, error) {
	if err := fs.Parse(args); err != nil {
		return "", err
	}
	rest := fs.Args()
	if len(rest) == 0 {
		return "", nil
	}
	if err := fs.Parse(rest[1:]); err != nil {
		return "", err
	}
	if extra := fs.Args(); len(extra) > 0 {
		return "", fmt.Errorf("unexpected arguments: %s", strings.Join(extra, " "))
	}
	return rest[0], nil
}

func (c cli) agents(arg string) ([]string, error) {
	if arg == "" || arg == "all" {
		return adapters.Names(), nil
	}
	if _, ok := adapters.Get(arg); !ok {
		return nil, fmt.Errorf("unknown agent %q (want %s or all)", arg, strings.Join(adapters.Names(), ", "))
	}
	return []string{arg}, nil
}

func (c cli) load(dir string) (*model.Source, error) {
	if dir == "" {
		dir = filepath.Join(c.nexoHome, "source")
	}
	src, err := source.Load(dir)
	if err != nil {
		return nil, fmt.Errorf("%w\nhint: pass --source DIR, or run nexo-ai init to create %s", err, filepath.Join(c.nexoHome, "source"))
	}
	return src, nil
}

// installSource loads the source for install. Only the default source is seeded
// automatically, and only when nothing exists at its path: an explicit --source is
// the user's choice, and an existing but broken source is an error, never a reset.
// The returned init plan is nil when nothing needs seeding; agents are planned from
// the in-memory default, so a dry run writes nothing.
func (c cli) installSource(dir string) (*model.Source, *plan.Plan, error) {
	if dir != "" {
		src, err := c.load(dir)
		return src, nil, err
	}
	root := filepath.Join(c.nexoHome, "source")
	if _, err := os.Lstat(root); !os.IsNotExist(err) {
		src, err := c.load("")
		return src, nil, err
	}
	p, err := source.InitPlan(root)
	if err != nil {
		return nil, nil, err
	}
	p.Agent = "init " + c.tilde(root)
	src, err := source.Default(root)
	if err != nil {
		return nil, nil, err
	}
	return src, &p, nil
}

func (c cli) install(args []string, now func() time.Time) int {
	fs := flag.NewFlagSet("install", flag.ContinueOnError)
	fs.SetOutput(c.stderr)
	sourceDir := fs.String("source", "", "source directory")
	dryRun := fs.Bool("dry-run", false, "show what would change without writing")
	copySkills := fs.Bool("copy-skills", false, "copy skill directories instead of symlinking them")
	target, err := parse(fs, args)
	if err != nil {
		fmt.Fprintln(c.stderr, "error:", err)
		return 2
	}
	if target == "" {
		fmt.Fprintln(c.stderr, "error: install needs an agent: "+strings.Join(adapters.Names(), ", ")+" or all")
		return 2
	}
	names, err := c.agents(target)
	if err != nil {
		fmt.Fprintln(c.stderr, "error:", err)
		return 2
	}
	src, initPlan, err := c.installSource(*sourceDir)
	if err != nil {
		fmt.Fprintln(c.stderr, "error:", err)
		return 1
	}

	// Plan every agent first, so an unreadable config aborts before anything is written.
	stateDir := filepath.Join(c.nexoHome, "state")
	plans := make([]plan.Plan, 0, len(names)+1)
	if initPlan != nil {
		// Seeding the source is applied first, without install state, like `nexo-ai init`.
		plans = append(plans, *initPlan)
	}
	for _, n := range names {
		env := plan.OSEnv(c.home)
		env.Executable = executable()
		env.OwnedKeys, err = install.OwnedKeys(stateDir, n)
		if err != nil {
			fmt.Fprintf(c.stderr, "error: %s: %v\n", n, err)
			return 1
		}
		f, _ := adapters.Get(n)
		p, err := f(src, env)
		if err == nil {
			err = p.Validate()
		}
		if err != nil {
			fmt.Fprintf(c.stderr, "error: %s: %v\n", n, err)
			return 1
		}
		plans = append(plans, p)
	}

	opts := install.Options{
		Home:      c.home,
		StateDir:  stateDir,
		BackupDir: filepath.Join(c.nexoHome, "backups", now().Format("20060102-150405")),
		DryRun:    *dryRun,
		CopyLinks: *copySkills,
	}
	backedUp := false
	for i, p := range plans {
		popts := opts
		if initPlan != nil && i == 0 {
			popts = install.Options{Home: c.home, DryRun: *dryRun}
		}
		r, err := install.Apply(p, popts)
		c.printReport(r, *dryRun)
		if err != nil {
			fmt.Fprintf(c.stderr, "error: %s: %v\n", p.Agent, err)
			return 1
		}
		for _, e := range r.Entries {
			backedUp = backedUp || e.BackedUp
		}
	}
	if backedUp && !*dryRun {
		fmt.Fprintln(c.stdout, "backups:", c.tilde(opts.BackupDir))
	}
	if !*dryRun {
		c.offerEngram(names)
	}
	return 0
}

// importCmd folds the agents' current config into the source. Existing source entries
// always win; the source is created when it does not exist yet.
func (c cli) importCmd(args []string, now func() time.Time) int {
	fs := flag.NewFlagSet("import", flag.ContinueOnError)
	fs.SetOutput(c.stderr)
	sourceDir := fs.String("source", "", "source directory")
	dryRun := fs.Bool("dry-run", false, "show what would change without writing")
	target, err := parse(fs, args)
	if err != nil {
		fmt.Fprintln(c.stderr, "error:", err)
		return 2
	}
	if target == "" {
		fmt.Fprintln(c.stderr, "error: import needs an agent: "+strings.Join(adapters.Names(), ", ")+" or all")
		return 2
	}
	names, err := c.agents(target)
	if err != nil {
		fmt.Fprintln(c.stderr, "error:", err)
		return 2
	}
	dir := *sourceDir
	if dir == "" {
		dir = filepath.Join(c.nexoHome, "source")
	}
	if dir, err = filepath.Abs(dir); err != nil {
		fmt.Fprintln(c.stderr, "error:", err)
		return 1
	}
	base := &model.Source{Root: dir}
	if _, err := os.Stat(dir); err == nil {
		if base, err = source.Load(dir); err != nil {
			fmt.Fprintln(c.stderr, "error:", err)
			return 1
		}
	}

	merger := importer.NewMerger(base, importer.Options{Root: dir, Home: c.home})
	var warnings []string
	for _, n := range names {
		read, _ := adapters.GetImport(n)
		imported, w, err := read(plan.OSEnv(c.home))
		if err != nil {
			fmt.Fprintf(c.stderr, "error: %s: %v\n", n, err)
			return 1
		}
		warnings = append(warnings, prefix(n, w)...)
		warnings = append(warnings, merger.Add(imported, n)...)
	}
	warnings = append(warnings, merger.Summary()...)
	p, err := importer.Render(base, merger.Result(), dir)
	if err != nil {
		fmt.Fprintln(c.stderr, "error:", err)
		return 1
	}
	p.Agent = "import into " + c.tilde(dir)
	p.Warnings = warnings
	r, err := install.Apply(p, install.Options{
		Home:      c.home,
		BackupDir: filepath.Join(c.nexoHome, "backups", now().Format("20060102-150405")),
		DryRun:    *dryRun,
		CopyLinks: true,
	})
	c.printReport(r, *dryRun)
	if err != nil {
		fmt.Fprintln(c.stderr, "error:", err)
		return 1
	}
	if len(r.Entries) == 0 {
		fmt.Fprintln(c.stdout, "  nothing new to import")
	}
	if !*dryRun && len(r.Entries) > 0 {
		if _, err := source.Load(dir); err != nil {
			fmt.Fprintln(c.stderr, "error: the imported source does not load, fix it before installing:", err)
			return 1
		}
	}
	return 0
}

func prefix(agent string, warnings []string) []string {
	out := make([]string, len(warnings))
	for i, w := range warnings {
		out[i] = agent + ": " + w
	}
	return out
}

func (c cli) printReport(r install.Report, dryRun bool) {
	title := r.Agent
	if dryRun {
		title += " (dry run, nothing written)"
	}
	fmt.Fprintln(c.stdout, title)
	for _, e := range r.Entries {
		suffix := ""
		if e.BackedUp {
			suffix = "  (backup)"
		}
		fmt.Fprintf(c.stdout, "  %-9s %s%s\n", e.Action, c.tilde(e.Path), suffix)
	}
	for _, w := range r.Warnings {
		fmt.Fprintln(c.stdout, "  warning:", w)
	}
}

func (c cli) tilde(path string) string {
	if rel, err := filepath.Rel(c.home, path); err == nil && !strings.HasPrefix(rel, "..") {
		return filepath.ToSlash(filepath.Join("~", rel))
	}
	return path
}

func (c cli) measure(args []string) int {
	fs := flag.NewFlagSet("measure", flag.ContinueOnError)
	fs.SetOutput(c.stderr)
	sourceDir := fs.String("source", "", "source directory")
	asJSON := fs.Bool("json", false, "print JSON")
	target, err := parse(fs, args)
	if err != nil {
		fmt.Fprintln(c.stderr, "error:", err)
		return 2
	}
	names, err := c.agents(target)
	if err != nil {
		fmt.Fprintln(c.stderr, "error:", err)
		return 2
	}
	src, err := c.load(*sourceDir)
	if err != nil {
		fmt.Fprintln(c.stderr, "error:", err)
		return 1
	}
	reports := []measure.Report{}
	for _, n := range names {
		r, err := measure.For(n, src)
		if err != nil {
			fmt.Fprintf(c.stderr, "error: %s: %v\n", n, err)
			return 1
		}
		reports = append(reports, r)
	}
	if *asJSON {
		enc := json.NewEncoder(c.stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(reports); err != nil {
			fmt.Fprintln(c.stderr, "error:", err)
			return 1
		}
		return 0
	}
	fmt.Fprintln(c.stdout, "Estimated tokens (~3.8 chars/token, offline):")
	for _, r := range reports {
		fmt.Fprintf(c.stdout, "\n%s  always ~%d  on demand ~%d\n", r.Agent, r.Always, r.OnDemand)
		for _, l := range r.Lines {
			fmt.Fprintf(c.stdout, "  %-13s %6d  %s\n", l.Load, l.Tokens, l.Item)
		}
		for _, n := range r.Notes {
			fmt.Fprintln(c.stdout, "  note:", n)
		}
	}
	return 0
}

// executable is this binary's resolved path, written into generated files that call it.
func executable() string {
	exe, err := os.Executable()
	if err != nil {
		return ""
	}
	if real, err := filepath.EvalSymlinks(exe); err == nil {
		return real
	}
	return exe
}

// hook runs the hooks for one event of an agent without command hooks. It reads the
// event as JSON on stdin and prints {"block": "reason"} or {}. It fails open: any error
// is reported on stderr and answered with {}, so a broken setup never stops the agent.
func (c cli) hook(args []string, stdin io.Reader) int {
	// Read the whole event first: a caller writing into a pipe nobody reads fails, and
	// every path below, failures included, must leave the pipe drained.
	data, readErr := io.ReadAll(stdin)
	fs := flag.NewFlagSet("hook", flag.ContinueOnError)
	fs.SetOutput(c.stderr)
	hooksFile := fs.String("hooks", "", "hooks file written by install")
	agent, err := parse(fs, args)
	if err == nil && agent != "opencode" {
		err = fmt.Errorf("hook supports only opencode, got %q", agent)
	}
	if err == nil && *hooksFile == "" {
		err = fmt.Errorf("hook needs --hooks FILE")
	}
	if err != nil {
		fmt.Fprintln(c.stderr, "error:", err)
		return 2
	}
	result := hookrun.Result{}
	var req hookrun.Request
	hooks, err := hookrun.LoadHooks(*hooksFile)
	if err == nil {
		err = readErr
	}
	if err == nil {
		err = json.Unmarshal(data, &req)
	}
	if err != nil {
		fmt.Fprintln(c.stderr, "nexo-ai hook:", err)
	} else {
		result = hookrun.OpenCode(req, hooks, hookrun.Exec)
	}
	_ = json.NewEncoder(c.stdout).Encode(result)
	return 0
}
