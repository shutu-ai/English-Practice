package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestRealUseReportMarksSmallDataInsufficient(t *testing.T) {
	s := testServer(t)
	report, err := s.realUseCalibrationReport("all", "")
	if err != nil {
		t.Fatal(err)
	}
	if report["total_attempts"] != 0 {
		t.Fatalf("empty report attempts=%v", report["total_attempts"])
	}
	accuracy := report["accuracy"].(map[string]any)["overall"].(map[string]any)
	if accuracy["status"] != "insufficient_evidence" {
		t.Fatalf("empty report accuracy=%v", accuracy)
	}
	gate := report["readiness_gate"].(map[string]any)
	if gate["status"] != "NOT_ENOUGH_DATA" {
		t.Fatalf("empty report readiness=%v", gate)
	}
	patterns, err := s.calibrationPatternDiagnostics("all")
	if err != nil || len(patterns) != len(patternCatalog()) {
		t.Fatalf("pattern diagnostics err=%v count=%d", err, len(patterns))
	}
}

func TestRealUseSessionFeedbackAndSnapshot(t *testing.T) {
	s := testServer(t)
	provider := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(fmt.Sprintf(`{"choices":[{"message":{"content":%q}}]}`, validEvaluationJSON())))
	}))
	defer provider.Close()
	s.llm.configs["mock"] = ProviderConfig{ID: "mock", Type: "openai-compatible", BaseURL: provider.URL, Model: "mock", Enabled: true, Timeout: 2}
	exercise, err := s.generateExercise(context.Background(), 3, "adaptive", "")
	if err != nil {
		t.Fatal(err)
	}
	result, err := s.submitAttempt(context.Background(), "real-session", exercise["exercise_id"].(string), "I didn't go because something came up.")
	if err != nil || result["evaluation_status"] != "validated" {
		t.Fatalf("submit err=%v result=%#v", err, result)
	}
	if err := s.recordFeedback(FeedbackRequest{ID: "feedback-test", ExerciseID: exercise["exercise_id"].(string), AttemptID: result["attempt_id"].(string), SessionID: "real-session", FeedbackType: "unnatural"}); err != nil {
		t.Fatal(err)
	}
	session, err := s.calibrationSessionReport("real-session")
	if err != nil {
		t.Fatal(err)
	}
	if session["attempt_count"] != 1 || len(session["questions"].([]map[string]any)) != 1 {
		t.Fatalf("session report=%#v", session)
	}
	question := session["questions"].([]map[string]any)[0]
	if question["mastery_before"] == nil || question["difficulty_after"] == nil {
		t.Fatalf("missing before/after trace=%#v", question)
	}
	snapshot, err := s.calibrationSnapshot("all", "real-session", false)
	if err != nil {
		t.Fatal(err)
	}
	raw, _ := json.Marshal(snapshot)
	if string(raw) == "" || string(raw) == "{}" || containsAny(string(raw), "api_key", "Authorization", "user_answer") {
		t.Fatalf("unsafe or empty snapshot=%s", raw)
	}
}

func TestRepresentativeLegacyDatabaseMigrationPreservesHistoryAndProviders(t *testing.T) {
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	defer db.Close()
	legacy := []string{
		`CREATE TABLE schema_meta(version INTEGER NOT NULL)`, `INSERT INTO schema_meta(version) VALUES(1)`,
		`CREATE TABLE scenes(id TEXT PRIMARY KEY,name TEXT NOT NULL,description TEXT NOT NULL DEFAULT '')`, `INSERT INTO scenes VALUES('daily','Daily','')`,
		`CREATE TABLE communication_intents(id TEXT PRIMARY KEY,name TEXT NOT NULL)`, `INSERT INTO communication_intents VALUES('plans','Plans')`,
		`CREATE TABLE sentence_patterns(id TEXT PRIMARY KEY,pattern TEXT NOT NULL,intent_id TEXT NOT NULL,difficulty REAL NOT NULL,metadata_json TEXT NOT NULL DEFAULT '{}')`, `INSERT INTO sentence_patterns VALUES('going-to','was going to','plans',2.2,'{}')`,
		`CREATE TABLE pattern_mastery(pattern_id TEXT PRIMARY KEY,attempts INTEGER NOT NULL,correct INTEGER NOT NULL,recent_accuracy REAL NOT NULL,long_term_accuracy REAL NOT NULL,consecutive_correct INTEGER NOT NULL,last_practiced TEXT,mastery REAL NOT NULL)`, `INSERT INTO pattern_mastery VALUES('going-to',12,10,.8,.8,2,NULL,.82)`,
		`CREATE TABLE user_profile(id TEXT PRIMARY KEY,global_difficulty REAL NOT NULL,assessment_complete INTEGER NOT NULL,assessment_progress TEXT NOT NULL,updated_at TEXT NOT NULL)`, `INSERT INTO user_profile VALUES('default',4,0,'[]','2026-01-01T00:00:00Z')`,
		`CREATE TABLE llm_providers(id TEXT PRIMARY KEY,name TEXT NOT NULL,type TEXT NOT NULL,base_url TEXT NOT NULL,api_key TEXT NOT NULL,model TEXT NOT NULL,timeout INTEGER NOT NULL,temperature REAL NOT NULL,max_tokens INTEGER NOT NULL,enabled INTEGER NOT NULL)`, `INSERT INTO llm_providers VALUES('legacy','Legacy','openai-compatible','http://local','keep-me','model',2,.2,100,1)`,
	}
	for _, query := range legacy {
		if _, err := db.Exec(query); err != nil {
			t.Fatal(err)
		}
	}
	if err := migrate(db); err != nil {
		t.Fatal(err)
	}
	if err := seed(db); err != nil {
		t.Fatal(err)
	}
	var attempts, evidence int
	var state string
	if err := db.QueryRow(`SELECT attempt_count,evidence_count,state FROM learner_skill_state WHERE pattern_id='going-to'`).Scan(&attempts, &evidence, &state); err != nil {
		t.Fatal(err)
	}
	if attempts != 12 || evidence != 12 || state == stateUnknown {
		t.Fatalf("legacy history changed attempts=%d evidence=%d state=%s", attempts, evidence, state)
	}
	var key string
	if err := db.QueryRow(`SELECT api_key FROM llm_providers WHERE id='legacy'`).Scan(&key); err != nil || key != "keep-me" {
		t.Fatalf("provider config changed err=%v key=%q", err, key)
	}
	var newAttempts int
	if err := db.QueryRow(`SELECT attempt_count FROM learner_skill_state WHERE pattern_id='formal-opinion'`).Scan(&newAttempts); err != nil {
		t.Fatal(err)
	}
	if newAttempts != 0 {
		t.Fatalf("new pattern inherited history: %d", newAttempts)
	}
}

func containsAny(raw string, values ...string) bool {
	for _, value := range values {
		if strings.Contains(raw, value) {
			return true
		}
	}
	return false
}
