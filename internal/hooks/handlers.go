package hooks

import (
	"encoding/json"
	"fmt"
	"io"
	"log"
	"net/http"
	"path/filepath"
	"ccmc/internal/daemon"
	"ccmc/pkg/ccmc"
)

// readAndDecode reads the full request body and routes it through DecodeEvent.
// Returns the parsed HookEvent or writes a 400 and returns nil.
func readAndDecode(w http.ResponseWriter, r *http.Request) (HookEvent, json.RawMessage) {
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, fmt.Sprintf("hooks: failed to read body: %v", err), http.StatusBadRequest)
		return nil, nil
	}
	raw := json.RawMessage(body)
	ev, err := DecodeEvent(raw)
	if err != nil {
		http.Error(w, fmt.Sprintf("hooks: %v", err), http.StatusBadRequest)
		return nil, nil
	}
	return ev, raw
}

// HandleSessionStart processes a SessionStart event: upserts a new Session
// into the registry marked active. If the session already exists it is replaced
// (CC can replay SessionStart on reconnect).
func HandleSessionStart(reg *daemon.Registry) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ev, _ := readAndDecode(w, r)
		if ev == nil {
			return
		}
		typed, ok := ev.(*SessionStartEvent)
		if !ok {
			http.Error(w, "hooks: wrong event type for SessionStart handler", http.StatusBadRequest)
			return
		}

		s := ccmc.Session{
			ID:              typed.SessionID,
			ProjectPath:     typed.ProjectPath,
			ProjectName:     filepath.Base(typed.ProjectPath),
			Status:          ccmc.SessionActive,
			LastActivity:    typed.Timestamp,
			StartedAt:       typed.Timestamp,
			ActiveSubagents: []string{},
		}
		reg.Add(s)
		w.WriteHeader(http.StatusNoContent)
	}
}

// HandleSessionEnd processes a SessionEnd event: marks the session dead so it
// remains inspectable but is excluded from active counts. Does not Remove —
// dead sessions are still queryable.
func HandleSessionEnd(reg *daemon.Registry) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ev, _ := readAndDecode(w, r)
		if ev == nil {
			return
		}
		typed, ok := ev.(*SessionEndEvent)
		if !ok {
			http.Error(w, "hooks: wrong event type for SessionEnd handler", http.StatusBadRequest)
			return
		}

		s, found := reg.Get(typed.SessionID)
		if !found {
			log.Printf("hooks: SessionEnd for unknown session %q — ignoring", typed.SessionID)
			w.WriteHeader(http.StatusNoContent)
			return
		}

		s.Status = ccmc.SessionDead
		s.LastActivity = typed.Timestamp
		if !reg.Update(s) {
			log.Printf("hooks: SessionEnd Update race for session %q", typed.SessionID)
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

// HandlePostToolUse processes a PostToolUse event: bumps LastActivity on the
// session. Does not invent fields beyond what Session holds.
func HandlePostToolUse(reg *daemon.Registry) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ev, _ := readAndDecode(w, r)
		if ev == nil {
			return
		}
		typed, ok := ev.(*PostToolUseEvent)
		if !ok {
			http.Error(w, "hooks: wrong event type for PostToolUse handler", http.StatusBadRequest)
			return
		}

		s, found := reg.Get(typed.SessionID)
		if !found {
			log.Printf("hooks: PostToolUse for unknown session %q — ignoring", typed.SessionID)
			w.WriteHeader(http.StatusNoContent)
			return
		}

		s.LastActivity = typed.Timestamp
		if !reg.Update(s) {
			log.Printf("hooks: PostToolUse Update race for session %q", typed.SessionID)
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

// HandleSubagentStart processes a SubagentStart event: appends the agent ID to
// the session's ActiveSubagents slice and bumps LastActivity.
func HandleSubagentStart(reg *daemon.Registry) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ev, _ := readAndDecode(w, r)
		if ev == nil {
			return
		}
		typed, ok := ev.(*SubagentStartEvent)
		if !ok {
			http.Error(w, "hooks: wrong event type for SubagentStart handler", http.StatusBadRequest)
			return
		}

		s, found := reg.Get(typed.SessionID)
		if !found {
			log.Printf("hooks: SubagentStart for unknown session %q — ignoring", typed.SessionID)
			w.WriteHeader(http.StatusNoContent)
			return
		}

		s.ActiveSubagents = append(s.ActiveSubagents, typed.AgentID)
		s.LastActivity = typed.Timestamp
		if !reg.Update(s) {
			log.Printf("hooks: SubagentStart Update race for session %q", typed.SessionID)
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

// HandleSubagentStop processes a SubagentStop event: removes the agent ID from
// ActiveSubagents and bumps LastActivity.
func HandleSubagentStop(reg *daemon.Registry) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ev, _ := readAndDecode(w, r)
		if ev == nil {
			return
		}
		typed, ok := ev.(*SubagentStopEvent)
		if !ok {
			http.Error(w, "hooks: wrong event type for SubagentStop handler", http.StatusBadRequest)
			return
		}

		s, found := reg.Get(typed.SessionID)
		if !found {
			log.Printf("hooks: SubagentStop for unknown session %q — ignoring", typed.SessionID)
			w.WriteHeader(http.StatusNoContent)
			return
		}

		s.ActiveSubagents = removeString(s.ActiveSubagents, typed.AgentID)
		s.LastActivity = typed.Timestamp
		if !reg.Update(s) {
			log.Printf("hooks: SubagentStop Update race for session %q", typed.SessionID)
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

// HandleStop processes a Stop event: bumps LastActivity and records the
// response summary as the session's TaskSummary.
func HandleStop(reg *daemon.Registry) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ev, _ := readAndDecode(w, r)
		if ev == nil {
			return
		}
		typed, ok := ev.(*StopEvent)
		if !ok {
			http.Error(w, "hooks: wrong event type for Stop handler", http.StatusBadRequest)
			return
		}

		s, found := reg.Get(typed.SessionID)
		if !found {
			log.Printf("hooks: Stop for unknown session %q — ignoring", typed.SessionID)
			w.WriteHeader(http.StatusNoContent)
			return
		}

		s.LastActivity = typed.Timestamp
		if typed.ResponseSummary != "" {
			s.TaskSummary = typed.ResponseSummary
		}
		if !reg.Update(s) {
			log.Printf("hooks: Stop Update race for session %q", typed.SessionID)
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

// HandleNotification processes a Notification event: bumps LastActivity only.
// No session creation — Notification requires an existing session context.
func HandleNotification(reg *daemon.Registry) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		ev, _ := readAndDecode(w, r)
		if ev == nil {
			return
		}
		typed, ok := ev.(*NotificationEvent)
		if !ok {
			http.Error(w, "hooks: wrong event type for Notification handler", http.StatusBadRequest)
			return
		}

		s, found := reg.Get(typed.SessionID)
		if !found {
			log.Printf("hooks: Notification for unknown session %q — ignoring", typed.SessionID)
			w.WriteHeader(http.StatusNoContent)
			return
		}

		s.LastActivity = typed.Timestamp
		if !reg.Update(s) {
			log.Printf("hooks: Notification Update race for session %q", typed.SessionID)
		}
		w.WriteHeader(http.StatusNoContent)
	}
}

// removeString returns a new slice with the first occurrence of target removed.
// Preserves order. Returns the original slice if target is not found.
func removeString(ss []string, target string) []string {
	for i, s := range ss {
		if s == target {
			out := make([]string, 0, len(ss)-1)
			out = append(out, ss[:i]...)
			out = append(out, ss[i+1:]...)
			return out
		}
	}
	return ss
}

