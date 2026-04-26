package main

// integrator.go wires ccmc eval, ccmc install, and ccmc tools ls|rm|update.
//
// Seam strategy: three package-level function variables (ghFetchFunc, evalFunc,
// installFunc) allow tests to inject stubs without subprocess or HTTP overhead.
// managerFactory is similarly replaceable so tools subcommand tests can use a
// fake manager without touching the filesystem.
//
// Prompt reader: runEval and runToolsRm receive an io.Reader for stdin so tests
// can supply a bytes.Buffer rather than os.Stdin, preventing interactive prompts
// from blocking during go test.

import (
	"bufio"
	"context"
	"fmt"
	"io"
	"os"
	"strings"
	"text/tabwriter"

	"ccmc/internal/config"
	"ccmc/internal/integrator"
	"ccmc/internal/inventory"
	"ccmc/pkg/ccmc"
)

// runInstallStdin is a package-level seam that tests replace to supply canned
// stdin input to runInstall (mirrors the pattern used by runEval and runToolsRm).
// When nil at runtime, os.Stdin is used.
var runInstallStdin io.Reader

// ── package-level seams ────────────────────────────────────────────────────────

// ghFetchFunc fetches repo context from GitHub. Tests replace this to avoid real
// HTTP calls.
var ghFetchFunc = func(ctx context.Context, owner, repo string) (ccmc.EvalContext, error) {
	client := integrator.NewClient()
	return client.Fetch(ctx, owner, repo)
}

// evalFunc calls the Anthropic evaluator. Tests replace this to avoid real API
// calls and to exercise the ErrNoAPIKey path.
var evalFunc = func(ctx context.Context, model string, ec ccmc.EvalContext, inv string) (ccmc.EvalResult, error) {
	e := integrator.NewEvaluator(integrator.WithEvalModel(model))
	return e.Evaluate(ctx, ec, inv)
}

// installFunc performs the install. Tests replace this to avoid filesystem and
// git operations.
var installFunc = func(ctx context.Context, cfg config.Config, src integrator.InstallSource) (ccmc.InstallResult, error) {
	inst := integrator.NewInstaller(cfg)
	return inst.Install(ctx, src)
}

// managerIface is the subset of integrator.Manager used by the tools subcommands.
// Defined as an interface so tests can supply a lightweight fake.
type managerIface interface {
	List() ([]ccmc.ToolRegistryEntry, error)
	Get(name string) (ccmc.ToolRegistryEntry, error)
	Remove(name string, deleteClone bool) error
	Update(name string) error
}

// managerFactory constructs the manager for the given registry path. Tests replace
// this to supply a fake manager.
var managerFactory = func(registryPath string) managerIface {
	return integrator.NewManager(registryPath)
}

// ── eval ───────────────────────────────────────────────────────────────────────

// runEval handles "ccmc eval <github-url> [flags]".
// stdin is injected so tests can supply canned input for the install prompt.
func runEval(args []string, stdout, stderr io.Writer, stdin io.Reader) int {
	// Flag parsing.
	var (
		noPrompt bool
		scope    string
		force    bool
	)
	rest := args
	for len(rest) > 0 {
		switch rest[0] {
		case "--no-prompt":
			noPrompt = true
			rest = rest[1:]
			continue
		case "--force":
			force = true
			rest = rest[1:]
			continue
		}
		if strings.HasPrefix(rest[0], "--scope=") {
			scope = strings.TrimPrefix(rest[0], "--scope=")
			rest = rest[1:]
			continue
		}
		if rest[0] == "--scope" && len(rest) >= 2 {
			scope = rest[1]
			rest = rest[2:]
			continue
		}
		// Not a flag — must be the positional URL.
		break
	}

	if len(rest) == 0 {
		fmt.Fprintln(stderr, "ccmc eval: missing github-url\nUsage: ccmc eval <github-url> [--no-prompt] [--scope global|<path>] [--force]")
		return 2
	}
	rawURL := rest[0]

	owner, repo, err := integrator.ParseURL(rawURL)
	if err != nil {
		fmt.Fprintf(stderr, "ccmc eval: invalid URL %q: %v\n", rawURL, err)
		return 1
	}

	cfg, err := config.Load(config.CcmcConfigPath())
	if err != nil {
		fmt.Fprintf(stderr, "ccmc eval: load config: %v\n", err)
		return 1
	}

	ctx := context.Background()

	// Fetch GitHub context.
	ec, err := ghFetchFunc(ctx, owner, repo)
	if err != nil {
		fmt.Fprintf(stderr, "ccmc eval: fetch repo: %v\n", err)
		return 1
	}

	// Build inventory summary for the evaluator's user message.
	invSummary := buildInventorySummary()

	// Evaluate.
	result, err := evalFunc(ctx, cfg.Integrator.Model, ec, invSummary)
	if err != nil {
		if strings.Contains(err.Error(), integrator.ErrNoAPIKey.Error()) {
			fmt.Fprintln(stderr, "ccmc eval: no Anthropic API key found")
			fmt.Fprintln(stderr, "  Set ANTHROPIC_API_KEY env var, or run: keymaster require CCMC ANTHROPIC_API_KEY")
			return 1
		}
		fmt.Fprintf(stderr, "ccmc eval: evaluate: %v\n", err)
		return 1
	}

	printEvalResult(stdout, result)

	if noPrompt {
		return 0
	}

	// Prompt the user.
	fmt.Fprintf(stdout, "\nInstall? [y/N] ")
	reader := bufio.NewReader(stdin)
	answer, _ := reader.ReadString('\n')
	answer = strings.TrimSpace(answer)

	if strings.EqualFold(answer, "y") {
		src := integrator.InstallSource{
			URL:     rawURL,
			EvalCtx: ec,
			EvalRes: result,
			Scope:   scope,
			Force:   force,
		}
		return doInstall(ctx, cfg, src, stdout, stderr)
	}

	return 0
}

// printEvalResult writes the structured EvalResult fields to w.
func printEvalResult(w io.Writer, r ccmc.EvalResult) {
	fmt.Fprintf(w, "Tool:           %s\n", r.ToolName)
	fmt.Fprintf(w, "Repo:           %s\n", r.RepoURL)
	fmt.Fprintf(w, "Capability:     %s\n", r.Capability)
	fmt.Fprintf(w, "Gap filled:     %s\n", r.GapFilled)
	if len(r.ProjectsBenefit) > 0 {
		fmt.Fprintf(w, "Projects:       %s\n", strings.Join(r.ProjectsBenefit, ", "))
	}
	if len(r.Dependencies) > 0 {
		fmt.Fprintf(w, "Dependencies:   %s\n", strings.Join(r.Dependencies, ", "))
	}
	fmt.Fprintf(w, "Risk:           %s\n", r.RiskAssessment)
	fmt.Fprintf(w, "Recommendation: %s\n", r.Recommendation)
}

// buildInventorySummary scans the global ~/.claude/ scope and returns a compact
// text summary suitable for the evaluator's user message. Errors are non-fatal —
// a partial or empty summary is better than blocking eval.
func buildInventorySummary() string {
	raw, err := inventory.NewScanner(config.ClaudeDir()).Scan()
	if err != nil {
		return ""
	}

	var sb strings.Builder

	mcps, _ := inventory.ParseMCPs(raw)
	if len(mcps) > 0 {
		sb.WriteString("MCPs: ")
		names := make([]string, 0, len(mcps))
		for _, m := range mcps {
			names = append(names, fmt.Sprintf("%s(%s)", m.Name, m.Type))
		}
		sb.WriteString(strings.Join(names, ", "))
		sb.WriteString("\n")
	}

	skills, _ := inventory.ParseSkills(raw)
	if len(skills) > 0 {
		sb.WriteString("Skills: ")
		names := make([]string, 0, len(skills))
		for _, s := range skills {
			names = append(names, s.Name)
		}
		sb.WriteString(strings.Join(names, ", "))
		sb.WriteString("\n")
	}

	agents, _ := inventory.ParseAgents(raw)
	if len(agents) > 0 {
		sb.WriteString("Agents: ")
		names := make([]string, 0, len(agents))
		for _, a := range agents {
			names = append(names, a.Name)
		}
		sb.WriteString(strings.Join(names, ", "))
		sb.WriteString("\n")
	}

	return sb.String()
}

// ── install ────────────────────────────────────────────────────────────────────

// runInstall handles "ccmc install <github-url> [flags]".
// stdin is the io.Reader for confirmation prompts (H-2). Callers that want to
// suppress the prompt pass --no-prompt or --force. The package-level
// runInstallStdin is used when the caller doesn't supply one directly.
func runInstall(args []string, stdout, stderr io.Writer) int {
	return runInstallWithReader(args, stdout, stderr, runInstallStdin)
}

func runInstallWithReader(args []string, stdout, stderr io.Writer, stdin io.Reader) int {
	var (
		scope    string
		force    bool
		noPrompt bool
		toolType string
	)
	rest := args
	for len(rest) > 0 {
		switch {
		case rest[0] == "--force":
			force = true
			rest = rest[1:]
			continue
		case rest[0] == "--no-prompt":
			noPrompt = true
			rest = rest[1:]
			continue
		case strings.HasPrefix(rest[0], "--scope="):
			scope = strings.TrimPrefix(rest[0], "--scope=")
			rest = rest[1:]
			continue
		case rest[0] == "--scope" && len(rest) >= 2:
			scope = rest[1]
			rest = rest[2:]
			continue
		case strings.HasPrefix(rest[0], "--type="):
			toolType = strings.TrimPrefix(rest[0], "--type=")
			rest = rest[1:]
			continue
		case rest[0] == "--type" && len(rest) >= 2:
			toolType = rest[1]
			rest = rest[2:]
			continue
		}
		break
	}

	if len(rest) == 0 {
		fmt.Fprintln(stderr, "ccmc install: missing github-url\nUsage: ccmc install <github-url> [--scope global|<path>] [--force] [--no-prompt] [--type stdio|sse|skill|agent|plugin]")
		return 2
	}
	rawURL := rest[0]

	// C-2: validate and canonicalize URL via ParseURL before it reaches git clone.
	// This rejects file://, ssh://, and flag-injection forms like --upload-pack=...
	owner, repo, err := integrator.ParseURL(rawURL)
	if err != nil {
		fmt.Fprintf(stderr, "ccmc install: invalid URL %q: %v\n", rawURL, err)
		return 1
	}
	canonicalURL := "https://github.com/" + owner + "/" + repo

	cfg, err := config.Load(config.CcmcConfigPath())
	if err != nil {
		fmt.Fprintf(stderr, "ccmc install: load config: %v\n", err)
		return 1
	}

	// H-2: confirmation gate before settings.json is mutated.
	// Skip when --force or --no-prompt is set (the eval flow uses doInstall
	// after its own prior prompt, so it bypasses this gate via doInstall directly).
	if !force && !noPrompt {
		effectiveStdin := stdin
		if effectiveStdin == nil {
			effectiveStdin = os.Stdin
		}
		fmt.Fprintf(stdout, "Install %s and write mcpServers entry to settings.json? [y/N] ", canonicalURL)
		reader := bufio.NewReader(effectiveStdin)
		answer, _ := reader.ReadString('\n')
		if !strings.EqualFold(strings.TrimSpace(answer), "y") {
			fmt.Fprintln(stdout, "aborted")
			return 0
		}
	}

	src := integrator.InstallSource{
		URL:      canonicalURL,
		Scope:    scope,
		ToolType: toolType,
		Force:    force,
	}

	return doInstall(context.Background(), cfg, src, stdout, stderr)
}

// doInstall is the shared install execution used by both runInstall and the
// eval-then-install flow in runEval (when the user answers 'y' to the prompt).
func doInstall(ctx context.Context, cfg config.Config, src integrator.InstallSource, stdout, stderr io.Writer) int {
	result, err := installFunc(ctx, cfg, src)
	if err != nil {
		if strings.Contains(err.Error(), integrator.ErrToolAlreadyInstalled.Error()) {
			fmt.Fprintf(stderr, "ccmc install: %v\n", err)
			fmt.Fprintln(stderr, "  Re-run with --force to overwrite the existing installation.")
			return 1
		}
		fmt.Fprintf(stderr, "ccmc install: %v\n", err)
		return 1
	}

	printInstallResult(stdout, result)
	return 0
}

// printInstallResult writes the structured InstallResult to w.
func printInstallResult(w io.Writer, r ccmc.InstallResult) {
	fmt.Fprintf(w, "Installed: %s\n", r.Name)
	fmt.Fprintf(w, "Type:      %s\n", r.Type)
	fmt.Fprintf(w, "Scope:     %s\n", r.Scope)
	if r.ClonePath != "" {
		fmt.Fprintf(w, "Clone:     %s\n", r.ClonePath)
	}
	if r.ConfigPath != "" {
		fmt.Fprintf(w, "Config:    %s\n", r.ConfigPath)
	}
}

// ── tools ──────────────────────────────────────────────────────────────────────

// runTools dispatches "ccmc tools <ls|rm|update>" subcommands.
func runTools(args []string, stdout, stderr io.Writer, stdin io.Reader) int {
	if len(args) == 0 {
		fmt.Fprintln(stderr, "ccmc tools: missing subcommand (ls|rm|update)")
		return 2
	}
	sub := args[0]
	rest := args[1:]

	registryPath := config.CcmcRegistryPath()
	mgr := managerFactory(registryPath)

	switch sub {
	case "ls":
		return runToolsLs(mgr, stdout, stderr)
	case "rm":
		return runToolsRm(mgr, rest, stdout, stderr, stdin)
	case "update":
		return runToolsUpdate(mgr, rest, stdout, stderr)
	default:
		fmt.Fprintf(stderr, "ccmc tools: unknown subcommand %q (ls|rm|update)\n", sub)
		return 2
	}
}

// runToolsLs prints the registered tool inventory as a tab-aligned table.
func runToolsLs(mgr managerIface, stdout, stderr io.Writer) int {
	entries, err := mgr.List()
	if err != nil {
		fmt.Fprintf(stderr, "ccmc tools ls: %v\n", err)
		return 1
	}
	if len(entries) == 0 {
		fmt.Fprintln(stdout, "no tools installed")
		return 0
	}

	w := tabwriter.NewWriter(stdout, 0, 0, 2, ' ', 0)
	fmt.Fprintln(w, "NAME\tTYPE\tSCOPE\tINSTALLED_AT")
	fmt.Fprintln(w, "----\t----\t-----\t------------")
	for _, e := range entries {
		fmt.Fprintf(w, "%s\t%s\t%s\t%s\n", e.Name, e.Type, e.Scope, e.InstalledAt)
	}
	w.Flush()
	return 0
}

// runToolsRm handles "ccmc tools rm <name> [--no-prompt] [--keep-clone]".
// stdin is injected so tests can supply canned input without blocking.
func runToolsRm(mgr managerIface, args []string, stdout, stderr io.Writer, stdin io.Reader) int {
	var (
		noPrompt  bool
		keepClone bool
	)
	rest := args
	for len(rest) > 0 {
		switch rest[0] {
		case "--no-prompt":
			noPrompt = true
			rest = rest[1:]
			continue
		case "--keep-clone":
			keepClone = true
			rest = rest[1:]
			continue
		}
		break
	}

	if len(rest) == 0 {
		fmt.Fprintln(stderr, "ccmc tools rm: missing tool name\nUsage: ccmc tools rm <name> [--no-prompt] [--keep-clone]")
		return 2
	}
	name := rest[0]

	entry, err := mgr.Get(name)
	if err != nil {
		fmt.Fprintf(stderr, "ccmc tools rm: %v\n", err)
		return 1
	}

	deleteClone := !keepClone

	if !noPrompt {
		if entry.ClonePath != "" {
			fmt.Fprintf(stdout, "remove tool %q (clone dir at %q) [y/N] ", name, entry.ClonePath)
		} else {
			fmt.Fprintf(stdout, "remove tool %q [y/N] ", name)
		}

		reader := bufio.NewReader(stdin)
		answer, _ := reader.ReadString('\n')
		answer = strings.TrimSpace(answer)
		if !strings.EqualFold(answer, "y") {
			fmt.Fprintln(stdout, "aborted")
			return 0
		}
	}

	if err := mgr.Remove(name, deleteClone); err != nil {
		fmt.Fprintf(stderr, "ccmc tools rm: %v\n", err)
		return 1
	}

	fmt.Fprintf(stdout, "removed %s\n", name)
	return 0
}

// runToolsUpdate handles "ccmc tools update [name]".
// With a name, updates only that tool. Without a name, updates all tools that
// have a clone_path. Per-tool errors are printed but do not abort the loop.
func runToolsUpdate(mgr managerIface, args []string, stdout, stderr io.Writer) int {
	if len(args) > 0 {
		name := args[0]
		if err := mgr.Update(name); err != nil {
			fmt.Fprintf(stderr, "ccmc tools update: %s: %v\n", name, err)
			return 1
		}
		fmt.Fprintf(stdout, "updated %s\n", name)
		return 0
	}

	// Update all tools that have a clone path.
	entries, err := mgr.List()
	if err != nil {
		fmt.Fprintf(stderr, "ccmc tools update: list tools: %v\n", err)
		return 1
	}
	if len(entries) == 0 {
		fmt.Fprintln(stdout, "no tools installed")
		return 0
	}

	exitCode := 0
	for _, e := range entries {
		if e.ClonePath == "" {
			continue
		}
		if err := mgr.Update(e.Name); err != nil {
			fmt.Fprintf(stderr, "ccmc tools update: %s: %v\n", e.Name, err)
			exitCode = 1
		} else {
			fmt.Fprintf(stdout, "updated %s\n", e.Name)
		}
	}
	return exitCode
}
