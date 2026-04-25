package integrator

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"ccmc/pkg/ccmc"
)

// Sentinel errors returned by Client methods.
var (
	// ErrRateLimit is returned when GitHub responds 403 with X-RateLimit-Remaining: 0.
	ErrRateLimit = errors.New("github: rate limit exceeded")
	// ErrNotFound is returned when the repo itself returns 404.
	ErrNotFound = errors.New("github: repository not found")
	// ErrInvalidURL is returned by ParseURL when the input cannot be parsed.
	ErrInvalidURL = errors.New("github: invalid URL or owner/repo string")
)

const (
	defaultBaseURL = "https://api.github.com"
	defaultTimeout = 10 * time.Second
	userAgent      = "ccmc/1.0"
)

// Client fetches GitHub repo metadata and file contents via the GitHub REST API v3.
// Unauthenticated — suitable for public repos within GitHub's anonymous rate limits.
type Client struct {
	http    *http.Client
	baseURL string // Without trailing slash. Override via WithBaseURL for tests.
}

// Option configures a Client.
type Option func(*Client)

// WithBaseURL overrides the GitHub API base URL. Used to point at an httptest server.
func WithBaseURL(u string) Option {
	return func(c *Client) {
		c.baseURL = strings.TrimRight(u, "/")
	}
}

// WithHTTPClient replaces the underlying http.Client entirely.
func WithHTTPClient(hc *http.Client) Option {
	return func(c *Client) {
		c.http = hc
	}
}

// WithTimeout sets the per-request timeout on the default HTTP client.
// Has no effect if WithHTTPClient was also provided (caller owns that client's timeout).
func WithTimeout(d time.Duration) Option {
	return func(c *Client) {
		c.http.Timeout = d
	}
}

// NewClient constructs a Client with a 10-second default timeout and the production
// GitHub API base URL.
func NewClient(opts ...Option) *Client {
	c := &Client{
		http:    &http.Client{Timeout: defaultTimeout},
		baseURL: defaultBaseURL,
	}
	for _, o := range opts {
		o(c)
	}
	return c
}

// repoMeta is the subset of the GitHub repo JSON we care about.
type repoMeta struct {
	Description   string   `json:"description"`
	Topics        []string `json:"topics"`
	DefaultBranch string   `json:"default_branch"`
}

// contentsResponse is the GitHub contents API response for a single file.
type contentsResponse struct {
	Type     string `json:"type"`     // "file" | "dir" | "symlink"
	Encoding string `json:"encoding"` // "base64"
	Content  string `json:"content"`  // Base64-encoded file content, with possible newlines
}

// Fetch retrieves repo metadata and key files for the given owner/repo.
//
// Returns ErrNotFound if the repo does not exist.
// Returns ErrRateLimit if GitHub signals the anonymous rate limit has been hit.
// Network errors on supplementary files (README, package.json, etc.) are tolerated:
// the corresponding EvalContext field is left empty and Fetch continues. Only a
// failure on the canonical repo metadata endpoint propagates as an error.
func (c *Client) Fetch(ctx context.Context, owner, repo string) (ccmc.EvalContext, error) {
	meta, err := c.fetchRepoMeta(ctx, owner, repo)
	if err != nil {
		return ccmc.EvalContext{}, err
	}

	ec := ccmc.EvalContext{
		Owner:         owner,
		Repo:          repo,
		Description:   meta.Description,
		Topics:        meta.Topics,
		DefaultBranch: meta.DefaultBranch,
	}

	// README: try candidates in order; first 200 response wins.
	// Order matters — the spec requires README.md before README before readme.md.
	for _, name := range []string{"README.md", "README", "readme.md"} {
		text, ok := c.fetchFileOptional(ctx, owner, repo, name)
		if ok {
			ec.ReadmeMarkdown = text
			break
		}
	}

	// Supplementary files at repo root. Missing or errored → leave field empty.
	ec.PackageJSON, _ = c.fetchFileOptional(ctx, owner, repo, "package.json")
	ec.PyprojectTOML, _ = c.fetchFileOptional(ctx, owner, repo, "pyproject.toml")

	// settings.json: try root first, then examples/.
	if text, ok := c.fetchFileOptional(ctx, owner, repo, "settings.json"); ok {
		ec.ExampleSettings = text
	} else if text, ok := c.fetchFileOptional(ctx, owner, repo, "examples/settings.json"); ok {
		ec.ExampleSettings = text
	}

	return ec, nil
}

// fetchRepoMeta calls GET /repos/{owner}/{repo} and returns parsed metadata.
// Canonical errors (ErrNotFound, ErrRateLimit) propagate; all others wrap.
func (c *Client) fetchRepoMeta(ctx context.Context, owner, repo string) (repoMeta, error) {
	url := fmt.Sprintf("%s/repos/%s/%s", c.baseURL, owner, repo)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return repoMeta{}, fmt.Errorf("github: build request: %w", err)
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept", "application/vnd.github.v3+json")

	resp, err := c.http.Do(req)
	if err != nil {
		return repoMeta{}, fmt.Errorf("github: fetch repo metadata: %w", err)
	}
	defer resp.Body.Close()

	if err := checkStatus(resp); err != nil {
		return repoMeta{}, err
	}

	var meta repoMeta
	if err := json.NewDecoder(resp.Body).Decode(&meta); err != nil {
		return repoMeta{}, fmt.Errorf("github: decode repo metadata: %w", err)
	}
	return meta, nil
}

// fetchFileOptional calls the GitHub Contents API for a single file path.
// Returns (content, true) on success, ("", false) on 404 or any network/decode error.
// The contents API returns JSON with base64-encoded file content — we decode it here.
// This keeps all file fetches on the same base URL, making httptest injection trivial.
func (c *Client) fetchFileOptional(ctx context.Context, owner, repo, path string) (string, bool) {
	url := fmt.Sprintf("%s/repos/%s/%s/contents/%s", c.baseURL, owner, repo, path)
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return "", false
	}
	req.Header.Set("User-Agent", userAgent)
	req.Header.Set("Accept", "application/vnd.github.v3+json")

	resp, err := c.http.Do(req)
	if err != nil {
		return "", false
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", false
	}

	var cr contentsResponse
	if err := json.NewDecoder(resp.Body).Decode(&cr); err != nil {
		return "", false
	}
	if cr.Type != "file" || cr.Encoding != "base64" {
		return "", false
	}

	// GitHub wraps base64 content in newlines; strip them before decoding.
	raw := strings.ReplaceAll(cr.Content, "\n", "")
	decoded, err := base64.StdEncoding.DecodeString(raw)
	if err != nil {
		return "", false
	}
	return string(decoded), true
}

// checkStatus maps HTTP status codes to sentinel errors.
func checkStatus(resp *http.Response) error {
	switch resp.StatusCode {
	case http.StatusOK:
		return nil
	case http.StatusNotFound:
		return ErrNotFound
	case http.StatusForbidden:
		if resp.Header.Get("X-RateLimit-Remaining") == "0" {
			return ErrRateLimit
		}
		// Drain body before returning so the connection can be reused.
		io.Copy(io.Discard, resp.Body) //nolint:errcheck
		return fmt.Errorf("github: forbidden (status 403)")
	default:
		io.Copy(io.Discard, resp.Body) //nolint:errcheck
		return fmt.Errorf("github: unexpected status %d", resp.StatusCode)
	}
}

// ParseURL parses a GitHub repo reference in any of these forms:
//
//	"owner/repo"
//	"https://github.com/owner/repo"
//	"https://github.com/owner/repo/"
//	"github.com/owner/repo/tree/main"
//
// Returns ErrInvalidURL when the input cannot be resolved to an owner and repo.
func ParseURL(input string) (owner, repo string, err error) {
	s := strings.TrimSpace(input)

	// Strip scheme.
	for _, prefix := range []string{"https://", "http://"} {
		if strings.HasPrefix(s, prefix) {
			s = s[len(prefix):]
			break
		}
	}

	// Strip github.com host if present.
	if strings.HasPrefix(s, "github.com/") {
		s = s[len("github.com/"):]
	}

	// s is now "owner/repo[/anything...]"
	parts := strings.SplitN(s, "/", 3)
	if len(parts) < 2 || parts[0] == "" || parts[1] == "" {
		return "", "", ErrInvalidURL
	}

	owner = parts[0]
	repo = strings.TrimSuffix(parts[1], ".git")
	if repo == "" {
		return "", "", ErrInvalidURL
	}
	return owner, repo, nil
}
