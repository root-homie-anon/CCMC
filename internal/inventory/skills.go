package inventory

import (
	"bufio"
	"bytes"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"ccmc/pkg/ccmc"

	"gopkg.in/yaml.v3"
)

// ParseSkills reads SKILL.md frontmatter for every skill path in raw and returns
// a slice of SkillEntry values. Sort order: global entries first (alphabetical by
// Name), then each project scope sorted ascending by ProjectPath, entries within
// each project alphabetical by Name.
//
// Files that lack frontmatter or have malformed YAML are skipped with a warning;
// the overall scan succeeds.
func ParseSkills(raw ccmc.InventoryRaw) ([]ccmc.SkillEntry, error) {
	var out []ccmc.SkillEntry

	// Global scope.
	global := collectSkillEntries(raw.Global.Skills, "global")
	sortSkillsByName(global)
	out = append(out, global...)

	// Project scopes — already sorted by ProjectPath from scanner.
	for _, scopeFiles := range raw.Projects {
		entries := collectSkillEntries(scopeFiles.Skills, scopeFiles.ProjectPath)
		sortSkillsByName(entries)
		out = append(out, entries...)
	}

	return out, nil
}

// ParseCommands reads frontmatter from every commands/*.md path in raw and returns
// a slice of CommandEntry values. Sort order matches ParseSkills: global first,
// then projects sorted by ProjectPath, entries within each scope alphabetical by Name.
//
// Files that lack frontmatter or have malformed YAML are skipped with a warning.
func ParseCommands(raw ccmc.InventoryRaw) ([]ccmc.CommandEntry, error) {
	var out []ccmc.CommandEntry

	global := collectCommandEntries(raw.Global.Commands, "global")
	sortCommandsByName(global)
	out = append(out, global...)

	for _, scopeFiles := range raw.Projects {
		entries := collectCommandEntries(scopeFiles.Commands, scopeFiles.ProjectPath)
		sortCommandsByName(entries)
		out = append(out, entries...)
	}

	return out, nil
}

// collectSkillEntries processes a slice of SKILL.md absolute paths for one scope.
func collectSkillEntries(paths []string, scope string) []ccmc.SkillEntry {
	var entries []ccmc.SkillEntry
	for _, p := range paths {
		fm, ok := parseFrontmatter(p)
		if !ok {
			continue
		}

		name := stringField(fm, "name")
		if name == "" {
			// Fall back to parent directory basename: skills/<name>/SKILL.md.
			name = filepath.Base(filepath.Dir(p))
		}

		entries = append(entries, ccmc.SkillEntry{
			Name:                   name,
			Path:                   p,
			Scope:                  scope,
			Description:            stringField(fm, "description"),
			UserInvocable:          boolField(fm, "user-invocable"),
			DisableModelInvocation: boolField(fm, "disable-model-invocation"),
		})
	}
	return entries
}

// collectCommandEntries processes a slice of command .md absolute paths for one scope.
func collectCommandEntries(paths []string, scope string) []ccmc.CommandEntry {
	var entries []ccmc.CommandEntry
	for _, p := range paths {
		fm, ok := parseFrontmatter(p)
		if !ok {
			continue
		}

		name := stringField(fm, "name")
		if name == "" {
			// Fall back to filename without extension.
			base := filepath.Base(p)
			name = strings.TrimSuffix(base, filepath.Ext(base))
		}

		entries = append(entries, ccmc.CommandEntry{
			Name:        name,
			Path:        p,
			Scope:       scope,
			Description: stringField(fm, "description"),
		})
	}
	return entries
}

// parseFrontmatter reads the file at path, extracts the YAML block between the
// opening and closing "---" delimiters, and decodes it into a map. Returns
// (nil, false) — after logging a warning — when:
//   - the file cannot be read
//   - the file does not begin with "---"
//   - the YAML inside the block is malformed
//
// Returns (nil, false) without warning when frontmatter is absent (file does not
// start with "---"), treating that as a normal markdown file.
//
// Callers in this package (agents.go, skills.go) should call parseFrontmatter
// rather than implementing their own frontmatter extractor.
func parseFrontmatter(path string) (map[string]any, bool) {
	data, err := os.ReadFile(path)
	if err != nil {
		warnf("inventory: cannot read %s: %v", path, err)
		return nil, false
	}

	const delimiter = "---"

	scanner := bufio.NewScanner(bytes.NewReader(data))

	// First non-empty line must be exactly "---".
	if !scanner.Scan() {
		return nil, false
	}
	if strings.TrimRight(scanner.Text(), "\r") != delimiter {
		warnf("inventory: no frontmatter in %s — skipping", path)
		return nil, false
	}

	// Collect lines until the closing "---".
	var buf strings.Builder
	for scanner.Scan() {
		line := strings.TrimRight(scanner.Text(), "\r")
		if line == delimiter {
			// Closing delimiter found — parse accumulated YAML.
			var fm map[string]any
			if err := yaml.Unmarshal([]byte(buf.String()), &fm); err != nil {
				warnf("inventory: malformed YAML frontmatter in %s: %v — skipping", path, err)
				return nil, false
			}
			if fm == nil {
				warnf("inventory: empty frontmatter in %s — skipping", path)
				return nil, false
			}
			return fm, true
		}
		buf.WriteString(line)
		buf.WriteByte('\n')
	}

	// Reached EOF without a closing delimiter — treat as no frontmatter.
	warnf("inventory: unclosed frontmatter in %s — skipping", path)
	return nil, false
}

// stringField extracts a string value from the frontmatter map. Returns "" when the
// key is absent or the value is not a string.
func stringField(fm map[string]any, key string) string {
	v, ok := fm[key]
	if !ok {
		return ""
	}
	s, _ := v.(string)
	return s
}

// boolField extracts a bool value from the frontmatter map. Returns false when the
// key is absent or the value is not a bool.
func boolField(fm map[string]any, key string) bool {
	v, ok := fm[key]
	if !ok {
		return false
	}
	b, _ := v.(bool)
	return b
}

func sortSkillsByName(s []ccmc.SkillEntry) {
	sort.Slice(s, func(i, j int) bool { return s[i].Name < s[j].Name })
}

func sortCommandsByName(s []ccmc.CommandEntry) {
	sort.Slice(s, func(i, j int) bool { return s[i].Name < s[j].Name })
}
