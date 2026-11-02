package source

import (
	_ "embed"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/Madian2112/nexo-ai/internal/model"
	"github.com/Madian2112/nexo-ai/internal/plan"
)

//go:embed defaults/soul/core.md
var defaultCore []byte

// InitPlan seeds only a missing core. Existing source content, even an empty
// core, belongs to the user. As with adapter plans, the caller applies writes.
func InitPlan(root string) (plan.Plan, error) {
	p := plan.Plan{Agent: "init"}
	abs, err := filepath.Abs(root)
	if err != nil {
		return p, err
	}
	// Refuse ambiguous entries rather than replacing them or following soul links.
	for _, entry := range []struct {
		path      string
		directory bool
	}{
		{abs, true}, {filepath.Join(abs, "soul"), true}, {filepath.Join(abs, "soul", "core.md"), false},
	} {
		info, err := os.Lstat(entry.path)
		if os.IsNotExist(err) {
			continue
		}
		if err != nil {
			return p, err
		}
		if (entry.directory && !info.IsDir()) || (!entry.directory && !info.Mode().IsRegular()) {
			return p, fmt.Errorf("cannot initialize source: conflicting entry %s", entry.path)
		}
	}
	if _, err := os.Stat(abs); err == nil {
		if _, err := Load(abs); err != nil {
			return p, err
		}
	} else if !os.IsNotExist(err) {
		return p, err
	}
	core := filepath.Join(abs, "soul", "core.md")
	if _, err := os.Lstat(core); os.IsNotExist(err) {
		p.File(core, append([]byte(nil), defaultCore...))
	} else if err != nil {
		return p, err
	}
	return p, nil
}

// Default returns the source that InitPlan seeds into an empty root, as Load would
// read it back, without touching the disk. It lets install plan a first run, dry
// runs included, before the source exists.
func Default(root string) (*model.Source, error) {
	abs, err := filepath.Abs(root)
	if err != nil {
		return nil, err
	}
	src := &model.Source{Root: abs}
	src.Settings.KiroToolSearch = true
	src.Soul.Core = strings.ReplaceAll(string(defaultCore), "\r\n", "\n")
	return src, nil
}
