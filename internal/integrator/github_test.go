package integrator

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"
)

// b64 encodes s as base64 with embedded newlines, matching GitHub's wire format.
func b64(s string) string {
	raw := base64.StdEncoding.EncodeToString([]byte(s))
	// GitHub inserts a newline every 60 characters.
	var sb strings.Builder
	for i, ch := range raw {
		if i > 0 && i%60 == 0 {
			sb.WriteByte('\n')
		}
		sb.WriteRune(ch)
	}
	sb.WriteByte('\n')
	return sb.String()
}

// contentsJSON builds a GitHub contents API JSON response for a file.
func contentsJSON(content string) string {
	cr := map[string]string{
		"type":     "file",
		"encoding": "base64",
		"content":  b64(content),
	}
	out, _ := json.Marshal(cr)
	return string(out)
}

// repoMetaJSON builds a minimal GitHub repo metadata JSON response.
func repoMetaJSON(description, defaultBranch string, topics []string) string {
	m := map[string]any{
		"description":    description,
		"default_branch": defaultBranch,
		"topics":         topics,
	}
	out, _ := json.Marshal(m)
	return string(out)
}

// TestParseURL_AllForms validates all accepted and rejected input forms.
func TestParseURL_AllForms(t *testing.T) {
	cases := []struct {
		input     string
		wantOwner string
		wantRepo  string
		wantErr   bool
	}{
		{"owner/repo", "owner", "repo", false},
		{"https://github.com/owner/repo", "owner", "repo", false},
		{"https://github.com/owner/repo/", "owner", "repo", false},
		{"github.com/owner/repo/tree/main", "owner", "repo", false},
		{"http://github.com/owner/repo", "owner", "repo", false},
		{"github.com/owner/repo.git", "owner", "repo", false},
		// Malformed
		{"notaurl", "", "", true},
		{"github.com/onlyowner", "", "", true},
		{"github.com//repo", "", "", true},
		{"", "", "", true},
	}

	for _, tc := range cases {
		t.Run(tc.input, func(t *testing.T) {
			owner, repo, err := ParseURL(tc.input)
			if tc.wantErr {
				if err == nil {
					t.Fatalf("ParseURL(%q) = (%q, %q, nil), want error", tc.input, owner, repo)
				}
				if !errors.Is(err, ErrInvalidURL) {
					t.Fatalf("ParseURL(%q) error = %v, want ErrInvalidURL", tc.input, err)
				}
				return
			}
			if err != nil {
				t.Fatalf("ParseURL(%q) unexpected error: %v", tc.input, err)
			}
			if owner != tc.wantOwner || repo != tc.wantRepo {
				t.Fatalf("ParseURL(%q) = (%q, %q), want (%q, %q)",
					tc.input, owner, repo, tc.wantOwner, tc.wantRepo)
			}
		})
	}
}

// stubMux builds an http.ServeMux that simulates the GitHub API for owner "test" / repo "proj".
// handlers is a map from URL path suffix to handler func; paths not in the map return 404.
func stubMux(paths map[string]http.HandlerFunc) http.Handler {
	mux := http.NewServeMux()
	for path, h := range paths {
		mux.HandleFunc(path, h)
	}
	// Fallback: 404
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})
	return mux
}

func TestFetch_HappyPath(t *testing.T) {
	const (
		readmeText  = "# My Tool\nThis is the README."
		pkgJSON     = `{"name":"my-tool","version":"1.0.0"}`
		description = "A great tool"
		branch      = "main"
	)
	topics := []string{"claude", "mcp"}

	mux := stubMux(map[string]http.HandlerFunc{
		"/repos/test/proj": func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(repoMetaJSON(description, branch, topics)))
		},
		"/repos/test/proj/contents/README.md": func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(contentsJSON(readmeText)))
		},
		"/repos/test/proj/contents/package.json": func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(contentsJSON(pkgJSON)))
		},
	})

	srv := httptest.NewServer(mux)
	defer srv.Close()

	c := NewClient(WithBaseURL(srv.URL))
	ec, err := c.Fetch(context.Background(), "test", "proj")
	if err != nil {
		t.Fatalf("Fetch: unexpected error: %v", err)
	}
	if ec.Owner != "test" {
		t.Errorf("Owner = %q, want %q", ec.Owner, "test")
	}
	if ec.Repo != "proj" {
		t.Errorf("Repo = %q, want %q", ec.Repo, "proj")
	}
	if ec.Description != description {
		t.Errorf("Description = %q, want %q", ec.Description, description)
	}
	if ec.DefaultBranch != branch {
		t.Errorf("DefaultBranch = %q, want %q", ec.DefaultBranch, branch)
	}
	if len(ec.Topics) != 2 || ec.Topics[0] != "claude" {
		t.Errorf("Topics = %v, want [claude mcp]", ec.Topics)
	}
	if ec.ReadmeMarkdown != readmeText {
		t.Errorf("ReadmeMarkdown = %q, want %q", ec.ReadmeMarkdown, readmeText)
	}
	if ec.PackageJSON != pkgJSON {
		t.Errorf("PackageJSON = %q, want %q", ec.PackageJSON, pkgJSON)
	}
	if ec.PyprojectTOML != "" {
		t.Errorf("PyprojectTOML should be empty, got %q", ec.PyprojectTOML)
	}
}

func TestFetch_NoReadme(t *testing.T) {
	mux := stubMux(map[string]http.HandlerFunc{
		"/repos/test/proj": func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(repoMetaJSON("desc", "main", nil)))
		},
		// All README paths return 404 via the fallback handler.
	})

	srv := httptest.NewServer(mux)
	defer srv.Close()

	c := NewClient(WithBaseURL(srv.URL))
	ec, err := c.Fetch(context.Background(), "test", "proj")
	if err != nil {
		t.Fatalf("Fetch: unexpected error: %v", err)
	}
	if ec.ReadmeMarkdown != "" {
		t.Errorf("ReadmeMarkdown should be empty when no README exists, got %q", ec.ReadmeMarkdown)
	}
}

func TestFetch_NoPackageJSON(t *testing.T) {
	mux := stubMux(map[string]http.HandlerFunc{
		"/repos/test/proj": func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(repoMetaJSON("desc", "main", nil)))
		},
		"/repos/test/proj/contents/README.md": func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(contentsJSON("# readme")))
		},
		// package.json returns 404 via fallback.
	})

	srv := httptest.NewServer(mux)
	defer srv.Close()

	c := NewClient(WithBaseURL(srv.URL))
	ec, err := c.Fetch(context.Background(), "test", "proj")
	if err != nil {
		t.Fatalf("Fetch: unexpected error: %v", err)
	}
	if ec.PackageJSON != "" {
		t.Errorf("PackageJSON should be empty, got %q", ec.PackageJSON)
	}
}

func TestFetch_RateLimit(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("X-RateLimit-Remaining", "0")
		w.WriteHeader(http.StatusForbidden)
	}))
	defer srv.Close()

	c := NewClient(WithBaseURL(srv.URL))
	_, err := c.Fetch(context.Background(), "test", "proj")
	if !errors.Is(err, ErrRateLimit) {
		t.Fatalf("Fetch: error = %v, want ErrRateLimit", err)
	}
}

func TestFetch_NotFound(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	}))
	defer srv.Close()

	c := NewClient(WithBaseURL(srv.URL))
	_, err := c.Fetch(context.Background(), "test", "proj")
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("Fetch: error = %v, want ErrNotFound", err)
	}
}

func TestFetch_UserAgentSet(t *testing.T) {
	var mu sync.Mutex
	seen := make([]string, 0)

	mux := stubMux(map[string]http.HandlerFunc{
		"/repos/test/proj": func(w http.ResponseWriter, r *http.Request) {
			mu.Lock()
			seen = append(seen, r.Header.Get("User-Agent"))
			mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(repoMetaJSON("desc", "main", nil)))
		},
		"/repos/test/proj/contents/README.md": func(w http.ResponseWriter, r *http.Request) {
			mu.Lock()
			seen = append(seen, r.Header.Get("User-Agent"))
			mu.Unlock()
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(contentsJSON("# readme")))
		},
	})

	srv := httptest.NewServer(mux)
	defer srv.Close()

	c := NewClient(WithBaseURL(srv.URL))
	_, err := c.Fetch(context.Background(), "test", "proj")
	if err != nil {
		t.Fatalf("Fetch: unexpected error: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(seen) == 0 {
		t.Fatal("no requests observed")
	}
	for _, ua := range seen {
		if ua != userAgent {
			t.Errorf("User-Agent = %q, want %q", ua, userAgent)
		}
	}
}

func TestFetch_Timeout(t *testing.T) {
	// done is closed when the test exits so the handler goroutine can unblock.
	done := make(chan struct{})
	t.Cleanup(func() { close(done) })

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Block until the client gives up or the test exits.
		select {
		case <-r.Context().Done():
		case <-done:
		}
	}))
	defer srv.Close()

	c := NewClient(WithBaseURL(srv.URL), WithTimeout(50*time.Millisecond))
	_, err := c.Fetch(context.Background(), "test", "proj")
	if err == nil {
		t.Fatal("Fetch: expected timeout error, got nil")
	}
	// The error should indicate a deadline/timeout — not ErrNotFound or ErrRateLimit.
	if errors.Is(err, ErrNotFound) || errors.Is(err, ErrRateLimit) {
		t.Fatalf("Fetch: expected timeout error, got sentinel %v", err)
	}
}

func TestFetch_README_TriedInOrder(t *testing.T) {
	var mu sync.Mutex
	requestedPaths := make([]string, 0)

	mux := http.NewServeMux()
	mux.HandleFunc("/repos/test/proj", func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(repoMetaJSON("desc", "main", nil)))
	})
	// Only "README" (no extension) succeeds; the others should 404.
	mux.HandleFunc("/repos/test/proj/contents/README.md", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		requestedPaths = append(requestedPaths, r.URL.Path)
		mu.Unlock()
		w.WriteHeader(http.StatusNotFound)
	})
	mux.HandleFunc("/repos/test/proj/contents/README", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		requestedPaths = append(requestedPaths, r.URL.Path)
		mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		w.Write([]byte(contentsJSON("# readme no ext")))
	})
	mux.HandleFunc("/repos/test/proj/contents/readme.md", func(w http.ResponseWriter, r *http.Request) {
		mu.Lock()
		requestedPaths = append(requestedPaths, r.URL.Path)
		mu.Unlock()
		// Should never be reached because README succeeded.
		w.WriteHeader(http.StatusNotFound)
	})
	// Fallback 404 for all other paths (supplementary files).
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusNotFound)
	})

	srv := httptest.NewServer(mux)
	defer srv.Close()

	c := NewClient(WithBaseURL(srv.URL))
	ec, err := c.Fetch(context.Background(), "test", "proj")
	if err != nil {
		t.Fatalf("Fetch: unexpected error: %v", err)
	}

	mu.Lock()
	paths := requestedPaths
	mu.Unlock()

	// Verify README.md was tried before README.
	if len(paths) < 2 {
		t.Fatalf("expected at least 2 README path attempts, got %d: %v", len(paths), paths)
	}
	if !strings.HasSuffix(paths[0], "/README.md") {
		t.Errorf("first README attempt = %q, want .../README.md", paths[0])
	}
	if !strings.HasSuffix(paths[1], "/README") {
		t.Errorf("second README attempt = %q, want .../README", paths[1])
	}
	// readme.md should NOT have been tried since README succeeded.
	for _, p := range paths {
		if strings.HasSuffix(p, "/readme.md") {
			t.Errorf("readme.md was fetched even though README succeeded")
		}
	}
	if ec.ReadmeMarkdown != "# readme no ext" {
		t.Errorf("ReadmeMarkdown = %q, want %q", ec.ReadmeMarkdown, "# readme no ext")
	}
}
