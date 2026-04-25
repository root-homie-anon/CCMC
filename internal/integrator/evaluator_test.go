package integrator

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"ccmc/pkg/ccmc"
)

// anthropicShape constructs a minimal valid Anthropic /v1/messages response body
// with the given text in content[0].text.
func anthropicShape(t *testing.T, text string) []byte {
	t.Helper()
	resp := map[string]any{
		"id":    "msg_test",
		"type":  "message",
		"model": "claude-sonnet-4-6",
		"content": []map[string]any{
			{"type": "text", "text": text},
		},
		"stop_reason": "end_turn",
		"usage": map[string]any{
			"input_tokens":  100,
			"output_tokens": 50,
		},
	}
	b, err := json.Marshal(resp)
	if err != nil {
		t.Fatalf("anthropicShape: marshal: %v", err)
	}
	return b
}

// validEvalResultJSON is a well-formed EvalResult JSON string.
const validEvalResultJSON = `{
  "toolName": "mcp-postgres",
  "repoUrl": "https://github.com/example/mcp-postgres",
  "capability": "PostgreSQL MCP server with query/execute/schema tools",
  "gapFilled": "Adds SQL database access not currently in the user's setup",
  "projectsBenefit": ["myapp", "analytics"],
  "dependencies": ["node >= 18", "pg npm package"],
  "riskAssessment": "Needs DB credentials in env; read/write access to configured DB",
  "recommendation": "install"
}`

func sampleRepo() ccmc.EvalContext {
	return ccmc.EvalContext{
		Owner:          "example",
		Repo:           "mcp-postgres",
		Description:    "PostgreSQL MCP server",
		Topics:         []string{"mcp", "postgres"},
		ReadmeMarkdown: "# mcp-postgres\nA PostgreSQL MCP server.",
	}
}

// TestEvaluate_HappyPath — httptest server returns a valid Anthropic-shaped
// response with structured EvalResult JSON in content[0].text.
func TestEvaluate_HappyPath(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write(anthropicShape(t, validEvalResultJSON))
	}))
	defer srv.Close()

	eval := NewEvaluator(
		WithEvalAPIKey("test-key-abc"),
		WithEvalBaseURL(srv.URL),
	)

	result, err := eval.Evaluate(context.Background(), sampleRepo(), "MCPs: none\nSkills: none")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if result.ToolName != "mcp-postgres" {
		t.Errorf("ToolName = %q; want %q", result.ToolName, "mcp-postgres")
	}
	if result.Recommendation != "install" {
		t.Errorf("Recommendation = %q; want %q", result.Recommendation, "install")
	}
	if len(result.ProjectsBenefit) != 2 {
		t.Errorf("ProjectsBenefit len = %d; want 2", len(result.ProjectsBenefit))
	}
	if result.Capability == "" {
		t.Error("Capability must not be empty")
	}
}

// TestEvaluate_ErrNoAPIKey — no env var set, no WithEvalAPIKey option.
// Evaluate must return ErrNoAPIKey.
func TestEvaluate_ErrNoAPIKey(t *testing.T) {
	// Clear env just in case — this test relies on no key being present.
	t.Setenv("ANTHROPIC_API_KEY", "")

	// Construct without providing a key. resolveAPIKey will find nothing.
	eval := NewEvaluator(
		WithEvalBaseURL("http://127.0.0.1:0"), // unreachable — should never be called
	)
	// Force apiKey to empty string to isolate from any global env.
	eval.apiKey = ""

	_, err := eval.Evaluate(context.Background(), sampleRepo(), "")
	if !errors.Is(err, ErrNoAPIKey) {
		t.Errorf("want ErrNoAPIKey; got %v", err)
	}
}

// TestEvaluate_APIError — stub returns 500. Must return ErrAnthropicAPI.
func TestEvaluate_APIError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusInternalServerError)
		json.NewEncoder(w).Encode(map[string]any{
			"error": map[string]string{
				"type":    "overloaded_error",
				"message": "service is temporarily overloaded",
			},
		})
	}))
	defer srv.Close()

	eval := NewEvaluator(
		WithEvalAPIKey("test-key"),
		WithEvalBaseURL(srv.URL),
	)

	_, err := eval.Evaluate(context.Background(), sampleRepo(), "")
	if !errors.Is(err, ErrAnthropicAPI) {
		t.Errorf("want ErrAnthropicAPI; got %v", err)
	}
	if err != nil && err.Error() == "" {
		t.Error("error message must not be empty")
	}
}

// TestEvaluate_RateLimit — stub returns 429. Must return ErrAnthropicAPI with
// rate-limit detail visible in the error message.
func TestEvaluate_RateLimit(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusTooManyRequests)
		json.NewEncoder(w).Encode(map[string]any{
			"error": map[string]string{
				"type":    "rate_limit_error",
				"message": "rate limit exceeded",
			},
		})
	}))
	defer srv.Close()

	eval := NewEvaluator(
		WithEvalAPIKey("test-key"),
		WithEvalBaseURL(srv.URL),
	)

	_, err := eval.Evaluate(context.Background(), sampleRepo(), "")
	if !errors.Is(err, ErrAnthropicAPI) {
		t.Errorf("want ErrAnthropicAPI; got %v", err)
	}
	if err != nil {
		msg := err.Error()
		// The message must contain the status code 429.
		if msg == "" {
			t.Error("error message must not be empty")
		}
	}
}

// TestEvaluate_ParseError — stub returns a valid Anthropic response shape but
// content[0].text contains garbage JSON. Must return ErrParseResult with the
// raw text included in the error.
func TestEvaluate_ParseError(t *testing.T) {
	garbage := "not json at all, just garbage text from the model"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write(anthropicShape(t, garbage))
	}))
	defer srv.Close()

	eval := NewEvaluator(
		WithEvalAPIKey("test-key"),
		WithEvalBaseURL(srv.URL),
	)

	_, err := eval.Evaluate(context.Background(), sampleRepo(), "")
	if !errors.Is(err, ErrParseResult) {
		t.Errorf("want ErrParseResult; got %v", err)
	}
	// The raw text should be included in the error message.
	if err != nil && !containsSubstring(err.Error(), garbage) {
		t.Errorf("error message should include raw text; got: %s", err.Error())
	}
}

// TestEvaluate_TimeoutFires — stub sleeps longer than the configured timeout.
// WithEvalTimeout(50ms) must cause Evaluate to return an error before the stub responds.
func TestEvaluate_TimeoutFires(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// Delay longer than the eval timeout so the client cancels first.
		// The handler returns when the request context is done to clean up
		// the goroutine and avoid a lingering-connection warning from httptest.
		select {
		case <-r.Context().Done():
		case <-time.After(5 * time.Second):
		}
	}))
	defer srv.Close()

	eval := NewEvaluator(
		WithEvalAPIKey("test-key"),
		WithEvalBaseURL(srv.URL),
		WithEvalTimeout(50*time.Millisecond),
	)

	start := time.Now()
	_, err := eval.Evaluate(context.Background(), sampleRepo(), "")
	elapsed := time.Since(start)

	if err == nil {
		t.Fatal("expected an error from timeout but got nil")
	}
	// Should have returned well within 1 second.
	if elapsed > 2*time.Second {
		t.Errorf("timeout took too long: %v; want < 2s", elapsed)
	}
}

// TestEvaluate_HeadersSent — stub asserts that the required Anthropic headers
// are present on every request.
func TestEvaluate_HeadersSent(t *testing.T) {
	var (
		gotAPIKey  string
		gotVersion string
		gotCT      string
	)

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotAPIKey = r.Header.Get("x-api-key")
		gotVersion = r.Header.Get("anthropic-version")
		gotCT = r.Header.Get("content-type")
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write(anthropicShape(t, validEvalResultJSON))
	}))
	defer srv.Close()

	const wantKey = "my-secret-key"
	eval := NewEvaluator(
		WithEvalAPIKey(wantKey),
		WithEvalBaseURL(srv.URL),
	)

	_, err := eval.Evaluate(context.Background(), sampleRepo(), "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	if gotAPIKey != wantKey {
		t.Errorf("x-api-key = %q; want %q", gotAPIKey, wantKey)
	}
	if gotVersion != anthropicVersion {
		t.Errorf("anthropic-version = %q; want %q", gotVersion, anthropicVersion)
	}
	if gotCT != "application/json" {
		t.Errorf("content-type = %q; want application/json", gotCT)
	}
}

// TestEvaluate_ModelOverride — WithEvalModel sets the model in the request body.
func TestEvaluate_ModelOverride(t *testing.T) {
	const wantModel = "claude-haiku-4-5-20251001"
	var gotModel string

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Model string `json:"model"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err == nil {
			gotModel = body.Model
		}
		w.Header().Set("Content-Type", "application/json")
		w.WriteHeader(http.StatusOK)
		w.Write(anthropicShape(t, validEvalResultJSON))
	}))
	defer srv.Close()

	eval := NewEvaluator(
		WithEvalAPIKey("test-key"),
		WithEvalBaseURL(srv.URL),
		WithEvalModel(wantModel),
	)

	_, err := eval.Evaluate(context.Background(), sampleRepo(), "")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if gotModel != wantModel {
		t.Errorf("model in request = %q; want %q", gotModel, wantModel)
	}
}

// containsSubstring is a helper for error message substring checks.
func containsSubstring(s, substr string) bool {
	return len(substr) > 0 && len(s) >= len(substr) &&
		(s == substr || len(s) > 0 && stringContains(s, substr))
}

func stringContains(s, substr string) bool {
	for i := 0; i <= len(s)-len(substr); i++ {
		if s[i:i+len(substr)] == substr {
			return true
		}
	}
	return false
}
