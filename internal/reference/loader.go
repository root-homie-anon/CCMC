package reference

import (
	"embed"
	"fmt"
	"path"
	"strings"

	"ccmc/pkg/ccmc"

	"gopkg.in/yaml.v3"
)

//go:embed data/*.yaml
var embeddedData embed.FS

// LoadAll parses every embedded YAML file in data/ and returns a flat slice
// of RefEntry values. Entries that do not carry a Category in the YAML are
// tagged with the category derived from the filename (e.g. "hooks.yaml" →
// RefCategory("hooks")). Returns a non-nil error only if a file cannot be
// read or cannot be decoded as a []RefEntry.
func LoadAll() ([]ccmc.RefEntry, error) {
	entries, err := embeddedData.ReadDir("data")
	if err != nil {
		return nil, fmt.Errorf("reference loader: read embedded dir: %w", err)
	}

	var all []ccmc.RefEntry

	for _, entry := range entries {
		if entry.IsDir() {
			continue
		}
		name := entry.Name()
		if !strings.HasSuffix(name, ".yaml") {
			continue
		}

		filePath := path.Join("data", name)
		raw, err := embeddedData.ReadFile(filePath)
		if err != nil {
			return nil, fmt.Errorf("reference loader: read %s: %w", filePath, err)
		}

		var refs []ccmc.RefEntry
		if err := yaml.Unmarshal(raw, &refs); err != nil {
			return nil, fmt.Errorf("reference loader: parse %s: %w", filePath, err)
		}

		// Derive a fallback category from the filename (strip ".yaml" suffix).
		fileCategory := ccmc.RefCategory(strings.TrimSuffix(name, ".yaml"))

		for i := range refs {
			if refs[i].Category == "" {
				refs[i].Category = fileCategory
			}
		}

		all = append(all, refs...)
	}

	return all, nil
}
