package main

import (
	"flag"
	"fmt"
	"path/filepath"

	"github.com/Madian2112/nexo-ai/internal/install"
	"github.com/Madian2112/nexo-ai/internal/source"
)

func (c cli) initCmd(args []string) int {
	fs := flag.NewFlagSet("init", flag.ContinueOnError)
	fs.SetOutput(c.stderr)
	dir := fs.String("source", "", "source directory (default $NEXO_HOME/source)")
	dryRun := fs.Bool("dry-run", false, "show what would change without writing")
	if err := fs.Parse(args); err != nil {
		fmt.Fprintln(c.stderr, "error:", err)
		return 2
	}
	if fs.NArg() != 0 {
		fmt.Fprintln(c.stderr, "error: init takes no positional arguments")
		return 2
	}
	if *dir == "" {
		*dir = filepath.Join(c.nexoHome, "source")
	}
	p, err := source.InitPlan(*dir)
	if err != nil {
		fmt.Fprintln(c.stderr, "error:", err)
		return 1
	}
	r, err := install.Apply(p, install.Options{Home: c.home, DryRun: *dryRun})
	c.printReport(r, *dryRun)
	if err != nil {
		fmt.Fprintln(c.stderr, "error:", err)
		return 1
	}
	if len(r.Entries) == 0 {
		fmt.Fprintln(c.stdout, "  source already initialized; existing files preserved")
	}
	if !*dryRun {
		fmt.Fprintln(c.stdout, "Next: nexo-ai install claude --source", fmt.Sprintf("%q", *dir))
	}
	return 0
}
