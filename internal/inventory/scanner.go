package inventory

import (
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"sort"

	"ccmc/pkg/ccmc"
)

// Scanner walks a Claude config directory and collects raw file paths by category.
// Construct via NewScanner; call Scan to produce an InventoryRaw.
type Scanner struct {
	claudeDir string
}

// NewScanner returns a Scanner rooted at claudeDir (typically the result of config.ClaudeDir()).
// Inject an alternate path in tests via t.TempDir().
func NewScanner(claudeDir string) *Scanner {
	return &Scanner{claudeDir: claudeDir}
}

// Scan walks the Claude directory and returns the structured inventory of raw paths.
// Returns a non-nil error only on catastrophic failure (e.g. claudeDir does not exist).
// A directory that exists but is empty yields an empty InventoryRaw — not an error.
func (s *Scanner) Scan() (ccmc.InventoryRaw, error) {
	if _, err := os.Stat(s.claudeDir); err != nil {
		return ccmc.InventoryRaw{}, fmt.Errorf("inventory scanner: claude dir inaccessible: %w", err)
	}

	raw := ccmc.InventoryRaw{}
	raw.Global = s.scanScope(s.claudeDir, "")

	projectsDir := filepath.Join(s.claudeDir, "projects")
	entries, err := os.ReadDir(projectsDir)
	if err != nil {
		if os.IsNotExist(err) {
			// No projects dir is normal on a fresh install.
			return raw, nil
		}
		// Unreadable projects dir is non-catastrophic; warn and return what we have.
		warnf("inventory scanner: cannot read projects dir %s: %v", projectsDir, err)
		return raw, nil
	}

	for _, e := range entries {
		if !e.IsDir() {
			continue
		}
		name := e.Name()
		if isDotfile(name) {
			continue
		}
		projectRoot := filepath.Join(projectsDir, name)
		scope := s.scanScope(projectRoot, name)
		raw.Projects = append(raw.Projects, scope)
	}

	// os.ReadDir returns entries in sorted order, but make the contract explicit.
	sort.Slice(raw.Projects, func(i, j int) bool {
		return raw.Projects[i].ProjectPath < raw.Projects[j].ProjectPath
	})

	return raw, nil
}

// scanScope collects all inventory file paths within a single scope root directory.
// projectPath is empty for the global scope.
func (s *Scanner) scanScope(root, projectPath string) ccmc.ScopeFiles {
	scope := ccmc.ScopeFiles{ProjectPath: projectPath}

	// settings.json — optional single file.
	settingsPath := filepath.Join(root, "settings.json")
	if fileReadable(settingsPath) {
		scope.SettingsPath = settingsPath
	}

	// CLAUDE.md — optional single file.
	claudeMDPath := filepath.Join(root, "CLAUDE.md")
	if fileReadable(claudeMDPath) {
		scope.ClaudeMDPath = claudeMDPath
	}

	// commands/*.md
	scope.Commands = collectGlob(root, "commands", func(entry fs.DirEntry, path string) bool {
		return !entry.IsDir() && filepath.Ext(entry.Name()) == ".md"
	})

	// skills/*/SKILL.md — skills are directories that contain a SKILL.md file.
	scope.Skills = collectSkills(root)

	// agents/*.md
	scope.Agents = collectGlob(root, "agents", func(entry fs.DirEntry, path string) bool {
		return !entry.IsDir() && filepath.Ext(entry.Name()) == ".md"
	})

	// plugins/* — any top-level entry (file or dir) inside plugins/.
	scope.Plugins = collectGlob(root, "plugins", func(entry fs.DirEntry, path string) bool {
		return !isDotfile(entry.Name())
	})

	return scope
}

// collectGlob reads the immediate children of root/subdir and calls keep(entry, absPath) to
// filter. Returns the absolute paths of kept entries, sorted. Unreadable entries are warned
// and skipped; an absent subdir is silently ignored.
func collectGlob(root, subdir string, keep func(fs.DirEntry, string) bool) []string {
	dir := filepath.Join(root, subdir)
	entries, err := os.ReadDir(dir)
	if err != nil {
		if !os.IsNotExist(err) {
			warnf("inventory scanner: cannot read %s: %v", dir, err)
		}
		return nil
	}

	var paths []string
	for _, e := range entries {
		if isDotfile(e.Name()) {
			continue
		}
		absPath := filepath.Join(dir, e.Name())

		// Do not follow symlinks — record as found.
		if e.Type()&fs.ModeSymlink != 0 {
			if keep(e, absPath) {
				paths = append(paths, absPath)
			}
			continue
		}

		// Stat to confirm readability (catches mode-0 files).
		if _, err := os.Lstat(absPath); err != nil {
			warnf("inventory scanner: skipping unreadable path %s: %v", absPath, err)
			continue
		}

		if keep(e, absPath) {
			paths = append(paths, absPath)
		}
	}

	return paths
}

// collectSkills walks root/skills/ looking for subdirectories that contain a SKILL.md file.
// Returns the absolute paths of those SKILL.md files.
func collectSkills(root string) []string {
	skillsDir := filepath.Join(root, "skills")
	entries, err := os.ReadDir(skillsDir)
	if err != nil {
		if !os.IsNotExist(err) {
			warnf("inventory scanner: cannot read %s: %v", skillsDir, err)
		}
		return nil
	}

	var paths []string
	for _, e := range entries {
		if isDotfile(e.Name()) {
			continue
		}

		// Symlinks in skills/ are recorded as-is; we don't traverse them.
		if e.Type()&fs.ModeSymlink != 0 {
			// A symlink at this level is recorded only if it resolves to a SKILL.md path.
			// Since we don't follow symlinks, skip traversal; record the symlink itself.
			// (Per contract: record the path as found, don't traverse.)
			skillMD := filepath.Join(skillsDir, e.Name(), "SKILL.md")
			paths = append(paths, skillMD)
			continue
		}

		if !e.IsDir() {
			continue
		}

		skillMD := filepath.Join(skillsDir, e.Name(), "SKILL.md")
		if fileReadable(skillMD) {
			paths = append(paths, skillMD)
		}
		// Dirs without SKILL.md are intentionally skipped — not a warning.
	}

	return paths
}

// fileReadable returns true if the path exists and is a regular file (or symlink) that can be stat'd.
func fileReadable(path string) bool {
	info, err := os.Lstat(path)
	if err != nil {
		return false
	}
	// Accept regular files and symlinks; reject directories.
	return info.Mode().IsRegular() || info.Mode()&fs.ModeSymlink != 0
}

// isDotfile returns true if the name starts with '.'.
func isDotfile(name string) bool {
	return len(name) > 0 && name[0] == '.'
}

// warnf writes a one-line warning to stderr. Exported only within the package for testability.
func warnf(format string, args ...any) {
	fmt.Fprintf(os.Stderr, "warning: "+format+"\n", args...)
}
