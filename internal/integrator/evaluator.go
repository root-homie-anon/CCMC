package integrator

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"os"
	"os/exec"
	"strings"
	"time"
	"unicode/utf8"

	"ccmc/pkg/ccmc"
)

// Sentinel errors returned by Evaluator.Evaluate.
var (
	// ErrNoAPIKey is returned when no Anthropic API key can be resolved from
	// the environment or KeyMaster.
	ErrNoAPIKey = errors.New("evaluator: no Anthropic API key found")

	// ErrAnthropicAPI is returned when the Anthropic API responds with a 4xx or
	// 5xx status code. The error message includes the status code and any body text.
	ErrAnthropicAPI = errors.New("evaluator: Anthropic API error")

	// ErrParseResult is returned when the API returns a successful response but
	// the content cannot be parsed into a structured EvalResult. The raw response
	// text is included in the wrapped error message.
	ErrParseResult = errors.New("evaluator: failed to parse EvalResult from API response")
)

const (
	anthropicBaseURL    = "https://api.anthropic.com"
	anthropicVersion    = "2023-06-01"
	defaultEvalModel    = "claude-sonnet-4-6"
	defaultMaxTokens    = 4096
	defaultEvalTimeout  = 30 * time.Second
)

// evaluatorSystemPrompt is the verbatim system prompt from the CCMC spec (CLAUDE(4).md §6).
// It is stable across calls, making it a prompt-cache candidate. The cache_control
// field is injected in the request body — see buildRequest.
const evaluatorSystemPrompt = `You are a CC tool evaluator. Given a GitHub repo's README and the user's current CC setup, assess whether this tool would be useful. Consider: what capability it adds, whether it overlaps with existing tools, which of the user's projects would benefit, any risks or dependencies, and how to configure it.

Respond with a JSON object only — no markdown fences, no prose outside the object. The object must match this schema exactly:
{
  "toolName":        "<name of the tool, typically the repo name>",
  "repoUrl":         "<the GitHub URL>",
  "capability":      "<one-line description of what it does>",
  "gapFilled":       "<capability gap it fills, or overlap it creates with existing tools>",
  "projectsBenefit": ["<project name or path>", ...],
  "dependencies":    ["<required dep>", ...],
  "riskAssessment":  "<permissions it needs, data it accesses, trust level>",
  "recommendation":  "<one of: install | skip | investigate>"
}`

// Evaluator calls the Anthropic API to assess a candidate Claude Code tool repo.
// Construct with NewEvaluator; customize via functional options.
type Evaluator struct {
	httpClient *http.Client
	baseURL    string
	model      string
	apiKey     string // resolved at construction time; may be empty if no key found
	timeout    time.Duration
}

// EvalOption configures an Evaluator.
type EvalOption func(*Evaluator)

// WithEvalAPIKey sets the Anthropic API key directly, bypassing env/KeyMaster lookup.
func WithEvalAPIKey(key string) EvalOption {
	return func(e *Evaluator) {
		e.apiKey = key
	}
}

// WithEvalModel overrides the default model (claude-sonnet-4-6).
func WithEvalModel(model string) EvalOption {
	return func(e *Evaluator) {
		e.model = model
	}
}

// WithEvalBaseURL overrides the Anthropic API base URL. Used in tests to point
// at an httptest.Server instead of the real endpoint.
//
// TEST-ONLY: production code MUST NOT call this — see x-api-key guard in
// Evaluate which refuses to send the key to any non-anthropic base URL.
func WithEvalBaseURL(url string) EvalOption {
	return func(e *Evaluator) {
		e.baseURL = strings.TrimRight(url, "/")
	}
}

// WithEvalHTTPClient replaces the underlying http.Client entirely. The caller is
// responsible for the client's timeout when using this option.
func WithEvalHTTPClient(c *http.Client) EvalOption {
	return func(e *Evaluator) {
		e.httpClient = c
	}
}

// WithEvalTimeout sets the per-call timeout. Defaults to 30 seconds.
func WithEvalTimeout(d time.Duration) EvalOption {
	return func(e *Evaluator) {
		e.timeout = d
	}
}

// NewEvaluator constructs an Evaluator.
//
// API key resolution order (first non-empty value wins):
//  1. WithEvalAPIKey option — caller-supplied key, highest priority.
//  2. ANTHROPIC_API_KEY environment variable.
//  3. KeyMaster lookup: `keymaster get CCMC ANTHROPIC_API_KEY` — used only when
//     the keymaster binary is on PATH (exec.LookPath succeeds).
//
// If no key is resolved, the Evaluator is constructed successfully but Evaluate
// will return ErrNoAPIKey. This allows callers to inspect the struct before use.
func NewEvaluator(opts ...EvalOption) *Evaluator {
	e := &Evaluator{
		httpClient: &http.Client{Timeout: defaultEvalTimeout},
		baseURL:    anthropicBaseURL,
		model:      defaultEvalModel,
		timeout:    defaultEvalTimeout,
	}
	for _, o := range opts {
		o(e)
	}

	// Resolve API key if not set by WithEvalAPIKey.
	if e.apiKey == "" {
		e.apiKey = resolveAPIKey()
	}

	return e
}

// resolveAPIKey attempts to find an Anthropic API key from the environment or
// KeyMaster. Returns empty string if neither source yields a key.
func resolveAPIKey() string {
	// 1. Environment variable — cheapest, most common path.
	if v := os.Getenv("ANTHROPIC_API_KEY"); v != "" {
		return v
	}

	// 2. KeyMaster binary — only attempted when binary is on PATH to avoid a
	//    subprocess penalty on every evaluator construction in environments
	//    that rely on the env var.
	if path, err := exec.LookPath("keymaster"); err == nil {
		out, err := exec.Command(path, "get", "CCMC", "ANTHROPIC_API_KEY").Output()
		if err == nil {
			if key := strings.TrimSpace(string(out)); key != "" {
				return key
			}
		}
	}

	return ""
}

// anthropicRequest is the JSON body for POST /v1/messages.
// The system field is a slice of content blocks so we can attach cache_control
// to the stable system prompt per the anthropic-api rules (prompt caching).
type anthropicRequest struct {
	Model     string             `json:"model"`
	MaxTokens int                `json:"max_tokens"`
	System    []systemBlock      `json:"system"`
	Messages  []anthropicMessage `json:"messages"`
}

// systemBlock is one element in the system content array. Using a slice (rather
// than a bare string) lets us attach cache_control without altering the user
// message, keeping the cache breakpoint at the system prompt boundary.
type systemBlock struct {
	Type         string        `json:"type"`
	Text         string        `json:"text"`
	CacheControl *cacheControl `json:"cache_control,omitempty"`
}

type cacheControl struct {
	Type string `json:"type"` // "ephemeral"
}

type anthropicMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

// anthropicResponse is the subset of the Anthropic /v1/messages response we use.
type anthropicResponse struct {
	ID      string `json:"id"`
	Content []struct {
		Type string `json:"type"`
		Text string `json:"text"`
	} `json:"content"`
	Error *struct {
		Type    string `json:"type"`
		Message string `json:"message"`
	} `json:"error"`
	Usage struct {
		InputTokens              int `json:"input_tokens"`
		OutputTokens             int `json:"output_tokens"`
		CacheCreationInputTokens int `json:"cache_creation_input_tokens"`
		CacheReadInputTokens     int `json:"cache_read_input_tokens"`
	} `json:"usage"`
}

// Evaluate sends the EvalContext and currentInventory to the Anthropic API and
// returns a structured EvalResult.
//
// Returns ErrNoAPIKey if no key was resolved at construction time.
// Returns ErrAnthropicAPI (wrapping status code and body) on 4xx/5xx responses.
// Returns ErrParseResult (wrapping raw response text) when the API returns text
// that cannot be decoded into the EvalResult JSON schema.
func (e *Evaluator) Evaluate(ctx context.Context, repo ccmc.EvalContext, currentInventory string) (ccmc.EvalResult, error) {
	if e.apiKey == "" {
		return ccmc.EvalResult{}, ErrNoAPIKey
	}

	// Apply per-call timeout by wrapping the caller's context.
	callCtx, cancel := context.WithTimeout(ctx, e.timeout)
	defer cancel()

	body, err := e.buildRequest(repo, currentInventory)
	if err != nil {
		return ccmc.EvalResult{}, fmt.Errorf("evaluator: build request: %w", err)
	}

	req, err := http.NewRequestWithContext(callCtx, http.MethodPost,
		e.baseURL+"/v1/messages", bytes.NewReader(body))
	if err != nil {
		return ccmc.EvalResult{}, fmt.Errorf("evaluator: create request: %w", err)
	}
	// M-3: only send the API key to the canonical Anthropic base URL.
	// WithEvalBaseURL is a test seam — if someone misconfigures it in production,
	// we must not leak the key to an arbitrary server.
	if e.baseURL == anthropicBaseURL {
		req.Header.Set("x-api-key", e.apiKey)
	}
	req.Header.Set("anthropic-version", anthropicVersion)
	req.Header.Set("content-type", "application/json")

	resp, err := e.httpClient.Do(req)
	if err != nil {
		return ccmc.EvalResult{}, fmt.Errorf("evaluator: http: %w", err)
	}
	defer resp.Body.Close()

	var apiResp anthropicResponse
	if err := json.NewDecoder(resp.Body).Decode(&apiResp); err != nil {
		return ccmc.EvalResult{}, fmt.Errorf("evaluator: decode response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		msg := fmt.Sprintf("status %d", resp.StatusCode)
		if apiResp.Error != nil {
			msg = fmt.Sprintf("status %d: %s: %s", resp.StatusCode, apiResp.Error.Type, apiResp.Error.Message)
		}
		return ccmc.EvalResult{}, fmt.Errorf("%w: %s", ErrAnthropicAPI, msg)
	}

	if len(apiResp.Content) == 0 {
		return ccmc.EvalResult{}, fmt.Errorf("%w: empty content array in response", ErrParseResult)
	}

	rawText := apiResp.Content[0].Text

	var result ccmc.EvalResult
	if err := json.Unmarshal([]byte(rawText), &result); err != nil {
		// L-3: strip non-printable bytes before including API response in errors
		// to prevent terminal control-sequence injection via malicious API responses.
		return ccmc.EvalResult{}, fmt.Errorf("%w: %s — raw: %s", ErrParseResult, err.Error(), sanitizePrintable(rawText))
	}

	return result, nil
}

// buildRequest serialises the anthropicRequest body. The system prompt receives
// cache_control so repeated eval calls for different repos share the cached
// system-prompt tokens (5-minute TTL on "ephemeral" cache blocks).
func (e *Evaluator) buildRequest(repo ccmc.EvalContext, currentInventory string) ([]byte, error) {
	userContent := buildUserMessage(repo, currentInventory)

	reqBody := anthropicRequest{
		Model:     e.model,
		MaxTokens: defaultMaxTokens,
		System: []systemBlock{
			{
				Type: "text",
				Text: evaluatorSystemPrompt,
				// Prompt caching: system prompt is stable across all eval calls.
				// Cache creation costs slightly more on first call but all
				// subsequent calls within the 5-minute window pay cache_read price
				// (~10x cheaper). This matters for batch eval workflows.
				CacheControl: &cacheControl{Type: "ephemeral"},
			},
		},
		Messages: []anthropicMessage{
			{Role: "user", Content: userContent},
		},
	}

	return json.Marshal(reqBody)
}

// buildUserMessage assembles the structured evaluation bundle from the repo context
// and the caller-supplied inventory summary string.
func buildUserMessage(repo ccmc.EvalContext, currentInventory string) string {
	var sb strings.Builder

	sb.WriteString("## Repository\n")
	sb.WriteString(fmt.Sprintf("Owner: %s\nRepo: %s\n", repo.Owner, repo.Repo))
	if repo.Description != "" {
		sb.WriteString(fmt.Sprintf("Description: %s\n", repo.Description))
	}
	if len(repo.Topics) > 0 {
		sb.WriteString(fmt.Sprintf("Topics: %s\n", strings.Join(repo.Topics, ", ")))
	}

	if repo.ReadmeMarkdown != "" {
		sb.WriteString("\n## README\n")
		sb.WriteString(repo.ReadmeMarkdown)
		sb.WriteString("\n")
	}

	if repo.PackageJSON != "" {
		sb.WriteString("\n## package.json\n```json\n")
		sb.WriteString(repo.PackageJSON)
		sb.WriteString("\n```\n")
	}

	if repo.PyprojectTOML != "" {
		sb.WriteString("\n## pyproject.toml\n```toml\n")
		sb.WriteString(repo.PyprojectTOML)
		sb.WriteString("\n```\n")
	}

	if repo.ExampleSettings != "" {
		sb.WriteString("\n## Example settings.json\n```json\n")
		sb.WriteString(repo.ExampleSettings)
		sb.WriteString("\n```\n")
	}

	if currentInventory != "" {
		sb.WriteString("\n## Current Inventory\n")
		sb.WriteString(currentInventory)
		sb.WriteString("\n")
	}

	return sb.String()
}

// sanitizePrintable strips non-printable runes from s before it is included in
// error messages or written to stderr. This prevents terminal control-sequence
// injection when untrusted API response bytes are echoed to a terminal (L-3).
//
// Allowed: printable ASCII (0x20–0x7E), TAB (0x09), LF (0x0A).
// Stripped: everything else including DEL (0x7F), C1 control codes (0x80–0x9F),
// and any non-ASCII bytes that are not valid printable Unicode.
func sanitizePrintable(s string) string {
	if s == "" {
		return s
	}
	b := make([]byte, 0, len(s))
	for i := 0; i < len(s); {
		r, size := utf8.DecodeRuneInString(s[i:])
		i += size
		if r == utf8.RuneError && size == 1 {
			// Invalid UTF-8 byte — drop it.
			continue
		}
		if r == '\t' || r == '\n' {
			b = append(b, byte(r))
			continue
		}
		if r >= 0x20 && r <= 0x7E {
			b = append(b, byte(r))
			continue
		}
		// Drop everything else (control codes, C1, surrogates, etc.).
	}
	return string(b)
}
