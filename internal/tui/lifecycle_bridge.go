package tui

// lifecycle_bridge.go — thin wrappers that connect the lifecycle package to App.
// Kept in a separate file so the seam variables in app.go can reference these
// functions without cluttering the main model file with package imports.

import (
	"ccmc/internal/lifecycle"
	"ccmc/pkg/ccmc"
)

// lifecycleKill delegates to lifecycle.Kill. Called by the production killFunc seam.
func lifecycleKill(client *ccmc.Client, id string) error {
	return lifecycle.Kill(client, id)
}

// lifecycleLaunch delegates to lifecycle.Launch. Called by the production launchFunc seam.
func lifecycleLaunch(client *ccmc.Client, dir string) (string, error) {
	return lifecycle.Launch(client, dir)
}

// lifecycleOpenInITerm delegates to lifecycle.OpenInITerm. Called by the production
// openInITermFunc seam.
func lifecycleOpenInITerm(dir string) error {
	return lifecycle.OpenInITerm(dir)
}
