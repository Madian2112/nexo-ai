package source

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"gopkg.in/yaml.v3"

	"github.com/Madian2112/nexo-ai/internal/model"
)

// ReadMarkdown returns the body of a Markdown file, without leading blank lines, and decodes its frontmatter into meta.
func ReadMarkdown(path string, meta any) (string, error) {
	b, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	m, body, err := SplitFrontmatter(b)
	if err != nil {
		return "", err
	}
	if m != "" && meta != nil {
		if err := yaml.Unmarshal([]byte(m), meta); err != nil {
			return "", fmt.Errorf("frontmatter: %w", err)
		}
	}
	return strings.TrimLeft(body, "\n"), nil
}

// ScanSkills is the lenient counterpart of Load for directories nexo-ai does not own:
// it returns every valid skill under dir/*/SKILL.md and one warning per skipped entry.
// Skill directories are resolved through symlinks.
func ScanSkills(dir string) ([]model.Skill, []string) {
	files, err := sortedGlob(filepath.Join(dir, "*", "SKILL.md"))
	if err != nil {
		return nil, []string{err.Error()}
	}
	var skills []model.Skill
	var warnings []string
	seen := map[string]string{}
	for _, p := range files {
		var meta struct {
			Name        string `yaml:"name"`
			Description string `yaml:"description"`
		}
		if _, err := ReadMarkdown(p, &meta); err != nil {
			warnings = append(warnings, fmt.Sprintf("skill %s skipped: %v", p, err))
			continue
		}
		if meta.Name == "" || meta.Description == "" {
			warnings = append(warnings, fmt.Sprintf("skill %s skipped: SKILL.md frontmatter requires name and description", p))
			continue
		}
		if err := checkName(meta.Name); err != nil {
			warnings = append(warnings, fmt.Sprintf("skill %s skipped: %v", p, err))
			continue
		}
		if first, dup := seen[meta.Name]; dup {
			warnings = append(warnings, fmt.Sprintf("skill %s skipped: name %q is already used by %s", p, meta.Name, first))
			continue
		}
		seen[meta.Name] = p
		real, err := filepath.EvalSymlinks(filepath.Dir(p))
		if err != nil {
			warnings = append(warnings, fmt.Sprintf("skill %s skipped: %v", p, err))
			continue
		}
		skills = append(skills, model.Skill{Name: meta.Name, Description: meta.Description, Dir: real})
	}
	return skills, warnings
}

// ValidName reports whether name can be used as a single file or directory name.
func ValidName(name string) bool { return checkName(name) == nil }
