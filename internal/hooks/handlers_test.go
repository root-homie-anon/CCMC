package hooks

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"ccmc/internal/daemon"
	"ccmc/pkg/ccmc"
)

// freshReg returns a new in-memory registry with no snapshot path (empty string
// skips disk I/O, but config.CcmcRegistryPath() would use ~/.ccmc). We pass a
// non-empty sentinel to avoid touching the real filesystem in tests.
func freshReg(t *testing.T) *daemon.Registry {
	t.Helper()
	return daemon.NewRegistry(t.TempDir() + "/registry.json")
}

// seedSession inserts a session directly via Add so individual handler tests
// can start with a known state.
func seedSession(reg *daemon.Registry, id, project string) ccmc.Session {
	s := ccmc.Session{
		ID:              id,
		ProjectPath:     project,
		ProjectName:     "test-project",
		Status:          ccmc.SessionActive,
		LastActivity:    time.Date(2026, 4, 1, 12, 0, 0, 0, time.UTC),
		StartedAt:       time.Date(2026, 4, 1, 12, 0, 0, 0, time.UTC),
		ActiveSubagents: []string{},
	}
	reg.Add(s)
	return s
}

// post builds a POST request with the given JSON body.
func post(t *testing.T, body string) *http.Request {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, "/hooks", bytes.NewBufferString(body))
	if err != nil {
		t.Fatalf("http.NewRequest: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	return req
}

// ─── SessionStart ────────────────────────────────────────────────────────────

func TestHandleSessionStart(t *testing.T) {
	ts := "2026-04-25T10:00:00Z"

	tests := []struct {
		name       string
		body       string
		wantStatus int
		wantInReg  bool
		wantActive bool
	}{
		{
			name: "happy path — new session",
			body: `{"type":"SessionStart","session_id":"sess-1","project_path":"/projects/foo","timestamp":"` + ts + `"}`,
			wantStatus: http.StatusNoContent,
			wantInReg:  true,
			wantActive: true,
		},
		{
			name: "upsert — existing session replaced",
			body: `{"type":"SessionStart","session_id":"sess-1","project_path":"/projects/bar","timestamp":"` + ts + `"}`,
			wantStatus: http.StatusNoContent,
			wantInReg:  true,
			wantActive: true,
		},
		{
			name:       "malformed JSON",
			body:       `{not valid`,
			wantStatus: http.StatusBadRequest,
			wantInReg:  false,
		},
		{
			name:       "missing session_id",
			body:       `{"type":"SessionStart","project_path":"/projects/foo","timestamp":"` + ts + `"}`,
			wantStatus: http.StatusNoContent, // Decodes fine, session_id is empty string
			wantInReg:  true,
			wantActive: true,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			reg := freshReg(t)
			h := HandleSessionStart(reg)
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, post(t, tc.body))

			if rec.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d; body: %s", rec.Code, tc.wantStatus, rec.Body.String())
			}

			if tc.wantInReg {
				sessions := reg.List()
				if len(sessions) == 0 {
					t.Fatal("expected session in registry, got none")
				}
				if tc.wantActive && sessions[0].Status != ccmc.SessionActive {
					t.Fatalf("status = %q, want %q", sessions[0].Status, ccmc.SessionActive)
				}
			}
		})
	}
}

// ─── SessionEnd ──────────────────────────────────────────────────────────────

func TestHandleSessionEnd(t *testing.T) {
	ts := "2026-04-25T11:00:00Z"

	tests := []struct {
		name       string
		body       string
		seed       bool
		wantStatus int
		wantDead   bool
	}{
		{
			name:       "happy path — marks session dead",
			body:       `{"type":"SessionEnd","session_id":"sess-2","project_path":"/p","timestamp":"` + ts + `","duration_seconds":120}`,
			seed:       true,
			wantStatus: http.StatusNoContent,
			wantDead:   true,
		},
		{
			name:       "unknown session — 204 best-effort",
			body:       `{"type":"SessionEnd","session_id":"unknown","project_path":"/p","timestamp":"` + ts + `","duration_seconds":0}`,
			seed:       false,
			wantStatus: http.StatusNoContent,
			wantDead:   false,
		},
		{
			name:       "malformed JSON",
			body:       `bad`,
			seed:       false,
			wantStatus: http.StatusBadRequest,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			reg := freshReg(t)
			if tc.seed {
				seedSession(reg, "sess-2", "/p")
			}
			h := HandleSessionEnd(reg)
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, post(t, tc.body))

			if rec.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d; body: %s", rec.Code, tc.wantStatus, rec.Body.String())
			}

			if tc.wantDead {
				s, ok := reg.Get("sess-2")
				if !ok {
					t.Fatal("session not found after SessionEnd")
				}
				if s.Status != ccmc.SessionDead {
					t.Fatalf("status = %q, want %q", s.Status, ccmc.SessionDead)
				}
			}
		})
	}
}

// ─── PostToolUse ─────────────────────────────────────────────────────────────

func TestHandlePostToolUse(t *testing.T) {
	newTS := "2026-04-25T12:00:00Z"
	expectedTime := time.Date(2026, 4, 25, 12, 0, 0, 0, time.UTC)

	tests := []struct {
		name       string
		body       string
		seed       bool
		wantStatus int
		checkTime  bool
	}{
		{
			name:       "happy path — LastActivity bumped",
			body:       `{"type":"PostToolUse","session_id":"sess-3","project_path":"/p","tool_name":"Bash","tool_input":{},"tool_output":{},"timestamp":"` + newTS + `"}`,
			seed:       true,
			wantStatus: http.StatusNoContent,
			checkTime:  true,
		},
		{
			name:       "unknown session — 204 best-effort",
			body:       `{"type":"PostToolUse","session_id":"nope","project_path":"/p","tool_name":"Read","tool_input":{},"tool_output":{},"timestamp":"` + newTS + `"}`,
			seed:       false,
			wantStatus: http.StatusNoContent,
		},
		{
			name:       "malformed JSON",
			body:       `{oops`,
			seed:       false,
			wantStatus: http.StatusBadRequest,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			reg := freshReg(t)
			if tc.seed {
				seedSession(reg, "sess-3", "/p")
			}
			h := HandlePostToolUse(reg)
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, post(t, tc.body))

			if rec.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d; body: %s", rec.Code, tc.wantStatus, rec.Body.String())
			}

			if tc.checkTime {
				s, ok := reg.Get("sess-3")
				if !ok {
					t.Fatal("session not found")
				}
				if !s.LastActivity.Equal(expectedTime) {
					t.Fatalf("LastActivity = %v, want %v", s.LastActivity, expectedTime)
				}
			}
		})
	}
}

// ─── SubagentStart ───────────────────────────────────────────────────────────

func TestHandleSubagentStart(t *testing.T) {
	ts := "2026-04-25T13:00:00Z"

	tests := []struct {
		name         string
		body         string
		seed         bool
		wantStatus   int
		wantAgentIDs []string
	}{
		{
			name:         "happy path — agent appended",
			body:         `{"type":"SubagentStart","session_id":"sess-4","project_path":"/p","agent_id":"agt-1","agent_name":"tony","task_description":"design","timestamp":"` + ts + `"}`,
			seed:         true,
			wantStatus:   http.StatusNoContent,
			wantAgentIDs: []string{"agt-1"},
		},
		{
			name:       "unknown session — 204 best-effort",
			body:       `{"type":"SubagentStart","session_id":"ghost","project_path":"/p","agent_id":"agt-x","agent_name":"x","task_description":"","timestamp":"` + ts + `"}`,
			seed:       false,
			wantStatus: http.StatusNoContent,
		},
		{
			name:       "malformed JSON",
			body:       `broken`,
			seed:       false,
			wantStatus: http.StatusBadRequest,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			reg := freshReg(t)
			if tc.seed {
				seedSession(reg, "sess-4", "/p")
			}
			h := HandleSubagentStart(reg)
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, post(t, tc.body))

			if rec.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d; body: %s", rec.Code, tc.wantStatus, rec.Body.String())
			}

			if tc.wantAgentIDs != nil {
				s, ok := reg.Get("sess-4")
				if !ok {
					t.Fatal("session not found")
				}
				if len(s.ActiveSubagents) != len(tc.wantAgentIDs) {
					t.Fatalf("ActiveSubagents = %v, want %v", s.ActiveSubagents, tc.wantAgentIDs)
				}
				for i, id := range tc.wantAgentIDs {
					if s.ActiveSubagents[i] != id {
						t.Fatalf("ActiveSubagents[%d] = %q, want %q", i, s.ActiveSubagents[i], id)
					}
				}
			}
		})
	}
}

// ─── SubagentStop ────────────────────────────────────────────────────────────

func TestHandleSubagentStop(t *testing.T) {
	ts := "2026-04-25T14:00:00Z"

	tests := []struct {
		name           string
		preAgents      []string // agent IDs to seed before the stop event
		body           string
		seed           bool
		wantStatus     int
		wantAgentCount int
	}{
		{
			name:           "happy path — agent removed",
			preAgents:      []string{"agt-1", "agt-2"},
			body:           `{"type":"SubagentStop","session_id":"sess-5","project_path":"/p","agent_id":"agt-1","agent_name":"tony","result":"done","success":true,"timestamp":"` + ts + `"}`,
			seed:           true,
			wantStatus:     http.StatusNoContent,
			wantAgentCount: 1,
		},
		{
			name:       "agent not in list — no-op, still 204",
			preAgents:  []string{"agt-2"},
			body:       `{"type":"SubagentStop","session_id":"sess-5","project_path":"/p","agent_id":"agt-99","agent_name":"x","result":"","success":false,"timestamp":"` + ts + `"}`,
			seed:       true,
			wantStatus: http.StatusNoContent,
			// agt-2 remains, agt-99 was never there
			wantAgentCount: 1,
		},
		{
			name:       "unknown session — 204 best-effort",
			body:       `{"type":"SubagentStop","session_id":"ghost","project_path":"/p","agent_id":"agt-1","agent_name":"x","result":"","success":true,"timestamp":"` + ts + `"}`,
			seed:       false,
			wantStatus: http.StatusNoContent,
		},
		{
			name:       "malformed JSON",
			body:       `{`,
			seed:       false,
			wantStatus: http.StatusBadRequest,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			reg := freshReg(t)
			if tc.seed {
				s := seedSession(reg, "sess-5", "/p")
				if len(tc.preAgents) > 0 {
					s.ActiveSubagents = tc.preAgents
					reg.Update(s)
				}
			}
			h := HandleSubagentStop(reg)
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, post(t, tc.body))

			if rec.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d; body: %s", rec.Code, tc.wantStatus, rec.Body.String())
			}

			if tc.seed && tc.wantStatus == http.StatusNoContent {
				s, ok := reg.Get("sess-5")
				if !ok {
					t.Fatal("session not found")
				}
				if len(s.ActiveSubagents) != tc.wantAgentCount {
					t.Fatalf("ActiveSubagents = %v (len %d), want len %d",
						s.ActiveSubagents, len(s.ActiveSubagents), tc.wantAgentCount)
				}
			}
		})
	}
}

// ─── Stop ────────────────────────────────────────────────────────────────────

func TestHandleStop(t *testing.T) {
	ts := "2026-04-25T15:00:00Z"

	tests := []struct {
		name           string
		body           string
		seed           bool
		wantStatus     int
		wantSummary    string
	}{
		{
			name:        "happy path — summary updated",
			body:        `{"type":"Stop","session_id":"sess-6","project_path":"/p","stop_reason":"end_turn","response_summary":"Did the thing","tool_calls":["Bash"],"timestamp":"` + ts + `"}`,
			seed:        true,
			wantStatus:  http.StatusNoContent,
			wantSummary: "Did the thing",
		},
		{
			name:        "empty summary — existing summary preserved",
			body:        `{"type":"Stop","session_id":"sess-6","project_path":"/p","stop_reason":"end_turn","response_summary":"","tool_calls":[],"timestamp":"` + ts + `"}`,
			seed:        true,
			wantStatus:  http.StatusNoContent,
			wantSummary: "", // seed has no summary, empty stays empty
		},
		{
			name:       "unknown session — 204 best-effort",
			body:       `{"type":"Stop","session_id":"ghost","project_path":"/p","stop_reason":"end_turn","response_summary":"x","tool_calls":[],"timestamp":"` + ts + `"}`,
			seed:       false,
			wantStatus: http.StatusNoContent,
		},
		{
			name:       "malformed JSON",
			body:       `notjson`,
			seed:       false,
			wantStatus: http.StatusBadRequest,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			reg := freshReg(t)
			if tc.seed {
				seedSession(reg, "sess-6", "/p")
			}
			h := HandleStop(reg)
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, post(t, tc.body))

			if rec.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d; body: %s", rec.Code, tc.wantStatus, rec.Body.String())
			}

			if tc.seed && rec.Code == http.StatusNoContent {
				s, ok := reg.Get("sess-6")
				if !ok {
					t.Fatal("session not found")
				}
				if s.TaskSummary != tc.wantSummary {
					t.Fatalf("TaskSummary = %q, want %q", s.TaskSummary, tc.wantSummary)
				}
			}
		})
	}
}

// ─── Notification ────────────────────────────────────────────────────────────

func TestHandleNotification(t *testing.T) {
	ts := "2026-04-25T16:00:00Z"
	expectedTime := time.Date(2026, 4, 25, 16, 0, 0, 0, time.UTC)

	tests := []struct {
		name       string
		body       string
		seed       bool
		wantStatus int
		checkTime  bool
	}{
		{
			name:       "happy path — LastActivity bumped",
			body:       `{"type":"Notification","session_id":"sess-7","project_path":"/p","notification_type":"attention","message":"hey","timestamp":"` + ts + `"}`,
			seed:       true,
			wantStatus: http.StatusNoContent,
			checkTime:  true,
		},
		{
			name:       "unknown session — 204 best-effort",
			body:       `{"type":"Notification","session_id":"ghost","project_path":"/p","notification_type":"x","message":"","timestamp":"` + ts + `"}`,
			seed:       false,
			wantStatus: http.StatusNoContent,
		},
		{
			name:       "malformed JSON",
			body:       `{}bad`,
			seed:       false,
			wantStatus: http.StatusBadRequest,
		},
	}

	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			reg := freshReg(t)
			if tc.seed {
				seedSession(reg, "sess-7", "/p")
			}
			h := HandleNotification(reg)
			rec := httptest.NewRecorder()
			h.ServeHTTP(rec, post(t, tc.body))

			if rec.Code != tc.wantStatus {
				t.Fatalf("status = %d, want %d; body: %s", rec.Code, tc.wantStatus, rec.Body.String())
			}

			if tc.checkTime {
				s, ok := reg.Get("sess-7")
				if !ok {
					t.Fatal("session not found")
				}
				if !s.LastActivity.Equal(expectedTime) {
					t.Fatalf("LastActivity = %v, want %v", s.LastActivity, expectedTime)
				}
			}
		})
	}
}

// ─── removeString helper ─────────────────────────────────────────────────────

func TestRemoveString(t *testing.T) {
	tests := []struct {
		name   string
		input  []string
		target string
		want   []string
	}{
		{"removes first occurrence", []string{"a", "b", "c"}, "b", []string{"a", "c"}},
		{"removes first of duplicates", []string{"a", "a", "b"}, "a", []string{"a", "b"}},
		{"target not found — original returned", []string{"a", "b"}, "x", []string{"a", "b"}},
		{"empty slice", []string{}, "a", []string{}},
		{"single element removed", []string{"a"}, "a", []string{}},
	}
	for _, tc := range tests {
		t.Run(tc.name, func(t *testing.T) {
			got := removeString(tc.input, tc.target)
			if len(got) != len(tc.want) {
				t.Fatalf("got %v, want %v", got, tc.want)
			}
			for i := range got {
				if got[i] != tc.want[i] {
					t.Fatalf("got[%d] = %q, want %q", i, got[i], tc.want[i])
				}
			}
		})
	}
}
