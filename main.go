package main

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log"
	"math"
	"math/rand"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	_ "modernc.org/sqlite"
)

const schemaVersion = 11

type Server struct {
	db     *sql.DB
	llm    *LLMRegistry
	mu     sync.RWMutex
	seeded bool
}

type LLMClient interface {
	Chat(context.Context, ChatRequest) (*ChatResponse, error)
}

type ChatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}
type ChatRequest struct {
	Messages    []ChatMessage `json:"messages"`
	Temperature float64       `json:"temperature,omitempty"`
	MaxTokens   int           `json:"max_tokens,omitempty"`
	JSONMode    bool          `json:"json_mode,omitempty"`
	// ReasoningMode and ReasoningEffort are role-specific provider options.
	// "inherit"/empty leaves the provider default unchanged. The wire shape is
	// the documented OpenAI-compatible thinking/reasoning_effort shape and is
	// only sent when a caller explicitly selects a mode.
	ReasoningMode   string `json:"-"`
	ReasoningEffort string `json:"-"`
	AllowEmpty      bool   `json:"-"`
	RequestID       string `json:"-"`
}

// ProviderRequestOptions are role-specific overrides. Empty values mean
// inherit the provider default, preserving existing provider records.
type ProviderRequestOptions struct {
	ReasoningMode   string
	ReasoningEffort string
	MaxTokens       int
	Temperature     *float64
}
type ChatResponse struct {
	Content          string        `json:"content"`
	Provider         string        `json:"provider,omitempty"`
	Model            string        `json:"model,omitempty"`
	ContentSource    string        `json:"-"`
	FinishReason     string        `json:"-"`
	ReasoningPresent bool          `json:"-"`
	ResponseBytes    int           `json:"-"`
	Latency          time.Duration `json:"-"`
	HTTPStatus       int           `json:"-"`
	ResponseShape    string        `json:"-"`
}

type ProviderError struct {
	Stage            string
	Category         string
	HTTPStatus       int
	Latency          time.Duration
	ResponseShape    string
	ContentSource    string
	FinishReason     string
	ReasoningPresent bool
	ResponseBytes    int
	Err              error
}

func (e *ProviderError) Error() string {
	if e.Err == nil {
		return e.Category
	}
	return e.Err.Error()
}
func (e *ProviderError) Unwrap() error { return e.Err }

type ProviderConfig struct {
	ID                       string  `json:"id"`
	Name                     string  `json:"name"`
	Type                     string  `json:"type"`
	BaseURL                  string  `json:"base_url"`
	APIKey                   string  `json:"api_key,omitempty"`
	Model                    string  `json:"model"`
	Timeout                  int     `json:"timeout"`
	Temperature              float64 `json:"temperature"`
	MaxTokens                int     `json:"max_tokens"`
	Enabled                  bool    `json:"enabled"`
	SupportsReasoningControl bool    `json:"supports_reasoning_control,omitempty"`
	SupportsJSONMode         bool    `json:"supports_json_mode,omitempty"`
	ExposesReasoningContent  bool    `json:"exposes_reasoning_content,omitempty"`
}
type LLMRegistry struct {
	mu      sync.RWMutex
	configs map[string]ProviderConfig
}

func (r *LLMRegistry) Client(c ProviderConfig) LLMClient {
	switch strings.ToLower(strings.ReplaceAll(c.Type, "_", "-")) {
	case "ollama":
		return HTTPChatClient{cfg: c, ollama: true}
	case "openai-compatible", "compatible":
		return HTTPChatClient{cfg: c}
	case "openai":
		return HTTPChatClient{cfg: c}
	}
	return HTTPChatClient{cfg: c}
}

type HTTPChatClient struct {
	cfg    ProviderConfig
	ollama bool
}

func normalizeReasoningMode(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "enabled", "enable", "on", "thinking":
		return "enabled"
	case "disabled", "disable", "off", "non-thinking", "non_thinking", "none":
		return "disabled"
	case "inherit", "default", "":
		return "inherit"
	default:
		return ""
	}
}

func normalizeReasoningEffort(value string) string {
	switch strings.ToLower(strings.TrimSpace(value)) {
	case "none", "low", "high", "max":
		return strings.ToLower(strings.TrimSpace(value))
	default:
		return ""
	}
}

func (c HTTPChatClient) Chat(ctx context.Context, req ChatRequest) (*ChatResponse, error) {
	start := time.Now()
	timeout := time.Duration(c.cfg.Timeout) * time.Second
	if timeout <= 0 {
		timeout = 45 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	payload := map[string]any{"model": c.cfg.Model, "messages": req.Messages, "temperature": req.Temperature, "max_tokens": req.MaxTokens}
	if mode := normalizeReasoningMode(req.ReasoningMode); mode != "" && mode != "inherit" {
		payload["thinking"] = map[string]string{"type": mode}
	}
	if effort := normalizeReasoningEffort(req.ReasoningEffort); effort != "" {
		payload["reasoning_effort"] = effort
	}
	if req.JSONMode {
		payload["response_format"] = map[string]string{"type": "json_object"}
	}
	b, _ := json.Marshal(payload)
	base := strings.TrimRight(c.cfg.BaseURL, "/")
	if base == "" {
		if c.ollama {
			base = "http://127.0.0.1:11434"
		} else {
			base = "https://api.openai.com/v1"
		}
	}
	url := base + "/chat/completions"
	if c.ollama {
		url = base + "/v1/chat/completions"
	}
	request, err := http.NewRequestWithContext(ctx, http.MethodPost, url, strings.NewReader(string(b)))
	if err != nil {
		return nil, err
	}
	request.Header.Set("Content-Type", "application/json")
	if req.RequestID != "" {
		request.Header.Set("X-Request-ID", req.RequestID)
	}
	if c.cfg.APIKey != "" {
		request.Header.Set("Authorization", "Bearer "+c.cfg.APIKey)
	}
	// Context deadlines cover request cancellation, but a client-level timeout
	// is also required for providers that keep an HTTP/2 response body open
	// while no final content is emitted. Without it, io.ReadAll below can hold
	// a role call beyond the simulation guard.
	httpClient := &http.Client{Transport: http.DefaultTransport, Timeout: timeout}
	resp, err := httpClient.Do(request)
	if err != nil {
		category := "provider_error"
		if errors.Is(err, context.DeadlineExceeded) || strings.Contains(strings.ToLower(err.Error()), "deadline exceeded") || strings.Contains(strings.ToLower(err.Error()), "client.timeout") {
			category = "timeout"
		}
		return nil, &ProviderError{Stage: "provider_http", Category: category, Latency: time.Since(start), Err: err}
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		category := "provider_5xx"
		if resp.StatusCode >= 400 && resp.StatusCode < 500 {
			category = "provider_4xx"
		}
		return nil, &ProviderError{Stage: "provider_http", Category: category, HTTPStatus: resp.StatusCode, Latency: time.Since(start), ResponseShape: responseShape(body), Err: fmt.Errorf("provider returned %s", resp.Status)}
	}
	content, meta, err := extractAssistantContentDetailed(body)
	if err != nil {
		category := "invalid_provider_envelope"
		var extractionErr *AssistantContentError
		if errors.As(err, &extractionErr) && extractionErr.Category != "" {
			category = extractionErr.Category
		}
		if req.AllowEmpty && (category == "provider_empty" || category == "reasoning_only") {
			return &ChatResponse{Provider: c.cfg.ID, Model: c.cfg.Model, Latency: time.Since(start), HTTPStatus: resp.StatusCode, ResponseShape: meta.ResponseShape, ContentSource: meta.ContentSource, FinishReason: meta.FinishReason, ReasoningPresent: meta.ReasoningPresent, ResponseBytes: len(body)}, nil
		}
		return nil, &ProviderError{Stage: "content_extraction", Category: category, HTTPStatus: resp.StatusCode, Latency: time.Since(start), ResponseShape: meta.ResponseShape, ContentSource: meta.ContentSource, FinishReason: meta.FinishReason, ReasoningPresent: meta.ReasoningPresent, ResponseBytes: len(body), Err: err}
	}
	return &ChatResponse{Content: content, Provider: c.cfg.ID, Model: c.cfg.Model, ContentSource: meta.ContentSource, FinishReason: meta.FinishReason, ReasoningPresent: meta.ReasoningPresent, ResponseBytes: len(body), Latency: time.Since(start), HTTPStatus: resp.StatusCode, ResponseShape: meta.ResponseShape}, nil
}

func responseShape(body []byte) string {
	var v any
	if json.Unmarshal(body, &v) != nil {
		return "invalid_json"
	}
	switch x := v.(type) {
	case map[string]any:
		keys := make([]string, 0, len(x))
		for k := range x {
			keys = append(keys, k)
		}
		shape := strings.Join(keys, ",")
		if choices, ok := x["choices"].([]any); ok && len(choices) > 0 {
			if choice, ok := choices[0].(map[string]any); ok {
				choiceKeys := make([]string, 0, len(choice))
				for k := range choice {
					choiceKeys = append(choiceKeys, k)
				}
				shape += "|choice:" + strings.Join(choiceKeys, ",")
				if finish, exists := choice["finish_reason"]; exists {
					shape += "|finish_reason:" + fmt.Sprint(finish)
				}
				if message, ok := choice["message"].(map[string]any); ok {
					messageKeys := make([]string, 0, len(message))
					for k := range message {
						messageKeys = append(messageKeys, k)
					}
					shape += "|message:" + strings.Join(messageKeys, ",")
					if content, exists := message["content"]; exists {
						shape += "|content_type:" + fmt.Sprintf("%T", content)
						if text, ok := content.(string); ok {
							shape += fmt.Sprintf("|content_length:%d", len(text))
						}
					}
				}
			}
		}
		return shape
	default:
		return fmt.Sprintf("%T", x)
	}
}

type AssistantContentMeta struct {
	ResponseShape    string
	ContentSource    string
	FinishReason     string
	ReasoningPresent bool
}

type AssistantContentError struct {
	Category string
	Err      error
}

func (e *AssistantContentError) Error() string {
	if e == nil || e.Err == nil {
		return "assistant content extraction failed"
	}
	return e.Err.Error()
}

func (e *AssistantContentError) Unwrap() error {
	if e == nil {
		return nil
	}
	return e.Err
}

// extractAssistantContent keeps the historical helper contract for callers and
// tests. The HTTP adapter uses the detailed form so diagnostics can distinguish
// provider-empty, reasoning-only, and extraction failures without persisting raw
// provider content.
func extractAssistantContent(body []byte) (string, string, error) {
	content, meta, err := extractAssistantContentDetailed(body)
	return content, meta.ResponseShape, err
}

func extractAssistantContentDetailed(body []byte) (string, AssistantContentMeta, error) {
	meta := AssistantContentMeta{ResponseShape: responseShape(body)}
	var root map[string]any
	if err := json.Unmarshal(body, &root); err != nil {
		meta.ResponseShape = "invalid_json"
		return "", meta, &AssistantContentError{Category: "invalid_provider_envelope", Err: fmt.Errorf("invalid provider envelope JSON: %w", err)}
	}
	if v, ok := root["response"].(string); ok && strings.TrimSpace(v) != "" {
		meta.ContentSource = "response"
		return v, meta, nil
	}
	if v, ok := root["output_text"].(string); ok && strings.TrimSpace(v) != "" {
		meta.ContentSource = "output_text"
		return v, meta, nil
	}
	if message, ok := root["message"].(map[string]any); ok {
		meta.ReasoningPresent = nonEmptyString(message["reasoning_content"])
		if content, ok := contentString(message["content"]); ok {
			meta.ContentSource = "message.content"
			return content, meta, nil
		}
	}
	if v, ok := root["content"].(string); ok && strings.TrimSpace(v) != "" {
		meta.ContentSource = "content"
		return v, meta, nil
	}
	if choices, ok := root["choices"].([]any); ok && len(choices) > 0 {
		if choice, ok := choices[0].(map[string]any); ok {
			if finish, ok := choice["finish_reason"].(string); ok {
				meta.FinishReason = finish
			}
			if msg, ok := choice["message"].(map[string]any); ok {
				meta.ReasoningPresent = nonEmptyString(msg["reasoning_content"])
				if content, ok := contentString(msg["content"]); ok {
					meta.ContentSource = "choices.message.content"
					return content, meta, nil
				}
				if rawContent, exists := msg["content"]; exists && !recognizedEmptyContent(rawContent) {
					return "", meta, &AssistantContentError{Category: "adapter_extraction_empty", Err: errors.New("assistant message content exists but has no extractable text")}
				}
				for key, value := range msg {
					if key != "role" && key != "content" && key != "reasoning_content" && key != "tool_calls" && nonEmptyString(value) {
						return "", meta, &AssistantContentError{Category: "valid_content_wrong_field", Err: fmt.Errorf("assistant text was returned in unsupported field %q", key)}
					}
				}
			}
			if content, ok := contentString(choice["text"]); ok {
				meta.ContentSource = "choices.text"
				return content, meta, nil
			}
		}
	}
	category := "provider_empty"
	message := "provider response has no final assistant content"
	if meta.ReasoningPresent {
		category = "reasoning_only"
		message = "provider response contains reasoning but no final assistant content"
	}
	return "", meta, &AssistantContentError{Category: category, Err: errors.New(message)}
}

func nonEmptyString(v any) bool {
	s, ok := v.(string)
	return ok && strings.TrimSpace(s) != ""
}

func recognizedEmptyContent(v any) bool {
	if v == nil {
		return true
	}
	if s, ok := v.(string); ok {
		return strings.TrimSpace(s) == ""
	}
	if parts, ok := v.([]any); ok {
		return len(parts) == 0
	}
	return false
}

func contentString(v any) (string, bool) {
	if s, ok := v.(string); ok && strings.TrimSpace(s) != "" {
		return s, true
	}
	parts, ok := v.([]any)
	if !ok {
		return "", false
	}
	var out strings.Builder
	for _, part := range parts {
		if m, ok := part.(map[string]any); ok {
			if text, ok := m["text"].(string); ok {
				out.WriteString(text)
			}
			if text, ok := m["content"].(string); ok {
				out.WriteString(text)
			}
		}
	}
	return out.String(), strings.TrimSpace(out.String()) != ""
}

func main() {
	if len(os.Args) > 1 && os.Args[1] == "simulate" {
		if err := runSimulationCLI(os.Args[2:]); err != nil {
			log.Fatal(err)
		}
		return
	}
	if len(os.Args) > 1 && os.Args[1] == "benchmark" {
		if err := runBenchmarkCLI(os.Args[2:]); err != nil {
			log.Fatal(err)
		}
		return
	}
	if len(os.Args) > 1 && os.Args[1] == "v25-live" {
		if err := runV25LiveCLI(os.Args[2:]); err != nil {
			log.Fatal(err)
		}
		return
	}
	if len(os.Args) > 1 && os.Args[1] == "v251-live" {
		if err := runV251LiveCLI(os.Args[2:]); err != nil {
			log.Fatal(err)
		}
		return
	}
	if len(os.Args) > 1 && os.Args[1] == "v26-live" {
		if err := runV26LiveCLI(os.Args[2:]); err != nil {
			log.Fatal(err)
		}
		return
	}
	if len(os.Args) > 1 && os.Args[1] == "curriculum-live" {
		if err := runCurriculumLiveCLI(os.Args[2:]); err != nil {
			log.Fatal(err)
		}
		return
	}
	if len(os.Args) > 1 && os.Args[1] == "curriculum-map" {
		if err := runCurriculumMapCLI(os.Args[2:]); err != nil {
			log.Fatal(err)
		}
		return
	}
	if len(os.Args) > 1 && os.Args[1] == "calibration-report" {
		if err := runCalibrationReportCLI(); err != nil {
			log.Fatal(err)
		}
		return
	}
	dataDir := os.Getenv("ENGLISH_PRACTICE_DATA")
	if dataDir == "" {
		dataDir = "data"
	}
	_ = os.MkdirAll(dataDir, 0755)
	db, err := sql.Open("sqlite", filepath.Join(dataDir, "english-practice.db"))
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close()
	db.SetMaxOpenConns(1)
	db.SetMaxIdleConns(1)
	if _, err = db.Exec(`PRAGMA busy_timeout=5000`); err != nil {
		log.Fatal(err)
	}
	if err = migrate(db); err != nil {
		log.Fatal(err)
	}
	if err = seed(db); err != nil {
		log.Fatal(err)
	}
	s := &Server{db: db, llm: &LLMRegistry{configs: map[string]ProviderConfig{}}}
	if err = loadProviders(s); err != nil {
		log.Fatal(err)
	}
	static := http.FileServer(http.Dir("web/dist"))
	mux := http.NewServeMux()
	registerRoutes(mux, s, static)
	port := os.Getenv("PORT")
	if port == "" {
		port = "8081"
	}
	log.Printf("English Practice listening on http://localhost:%s", port)
	log.Fatal(http.ListenAndServe(":"+port, logging(mux)))
}

func migrate(db *sql.DB) error {
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS schema_meta (version INTEGER NOT NULL)`,
		`CREATE TABLE IF NOT EXISTS scenes (id TEXT PRIMARY KEY, name TEXT NOT NULL, description TEXT NOT NULL DEFAULT '')`,
		`CREATE TABLE IF NOT EXISTS communication_intents (id TEXT PRIMARY KEY, name TEXT NOT NULL)`,
		`CREATE TABLE IF NOT EXISTS sentence_patterns (id TEXT PRIMARY KEY, pattern TEXT NOT NULL, intent_id TEXT NOT NULL, difficulty REAL NOT NULL, metadata_json TEXT NOT NULL DEFAULT '{}', catalog_difficulty REAL NOT NULL DEFAULT 0, FOREIGN KEY(intent_id) REFERENCES communication_intents(id))`,
		`CREATE TABLE IF NOT EXISTS exercises (id TEXT PRIMARY KEY, chinese_prompt TEXT NOT NULL, pattern_id TEXT NOT NULL, scene_id TEXT NOT NULL, intent_id TEXT NOT NULL, difficulty REAL NOT NULL, metadata_json TEXT NOT NULL DEFAULT '{}', created_at TEXT NOT NULL, FOREIGN KEY(pattern_id) REFERENCES sentence_patterns(id), FOREIGN KEY(scene_id) REFERENCES scenes(id))`,
		`CREATE TABLE IF NOT EXISTS sessions (id TEXT PRIMARY KEY, mode TEXT NOT NULL, started_at TEXT NOT NULL, ended_at TEXT, attempt_count INTEGER NOT NULL DEFAULT 0, start_global_difficulty REAL NOT NULL DEFAULT 0, end_global_difficulty REAL NOT NULL DEFAULT 0, patterns_seen TEXT NOT NULL DEFAULT '{}', skills_seen TEXT NOT NULL DEFAULT '{}', reviews_served INTEGER NOT NULL DEFAULT 0, probes_served INTEGER NOT NULL DEFAULT 0, new_skills_served INTEGER NOT NULL DEFAULT 0, difficulty_mode TEXT NOT NULL DEFAULT 'adaptive', fixed_difficulty REAL NOT NULL DEFAULT 0, training_focus TEXT NOT NULL DEFAULT 'pattern')`,
		`CREATE TABLE IF NOT EXISTS attempts (id TEXT PRIMARY KEY, session_id TEXT NOT NULL, exercise_id TEXT NOT NULL, user_answer TEXT NOT NULL, submitted_at TEXT NOT NULL, provider TEXT NOT NULL DEFAULT '', model TEXT NOT NULL DEFAULT '', prompt_version TEXT NOT NULL DEFAULT '', evaluation_status TEXT NOT NULL, difficulty_mode TEXT NOT NULL DEFAULT 'adaptive', fixed_difficulty REAL NOT NULL DEFAULT 0, training_focus TEXT NOT NULL DEFAULT 'pattern', target_pattern_present INTEGER NOT NULL DEFAULT 1, FOREIGN KEY(session_id) REFERENCES sessions(id), FOREIGN KEY(exercise_id) REFERENCES exercises(id))`,
		`CREATE TABLE IF NOT EXISTS evaluations (id TEXT PRIMARY KEY, attempt_id TEXT NOT NULL UNIQUE, verdict TEXT NOT NULL, meaning_score REAL NOT NULL, grammar_score REAL NOT NULL, naturalness_score REAL NOT NULL, pattern_score REAL NOT NULL, target_pattern_match TEXT NOT NULL DEFAULT 'unknown', target_pattern_score REAL NOT NULL DEFAULT 0, errors_json TEXT NOT NULL, suggested_answer TEXT NOT NULL, more_natural TEXT NOT NULL DEFAULT '', more_natural_needed INTEGER NOT NULL DEFAULT 0, alternative TEXT NOT NULL DEFAULT '', explanation_zh TEXT NOT NULL, validated INTEGER NOT NULL, created_at TEXT NOT NULL, FOREIGN KEY(attempt_id) REFERENCES attempts(id))`,
		`CREATE TABLE IF NOT EXISTS pattern_mastery (pattern_id TEXT PRIMARY KEY, attempts INTEGER NOT NULL, correct INTEGER NOT NULL, recent_accuracy REAL NOT NULL, long_term_accuracy REAL NOT NULL, consecutive_correct INTEGER NOT NULL, last_practiced TEXT, mastery REAL NOT NULL, FOREIGN KEY(pattern_id) REFERENCES sentence_patterns(id))`,
		`CREATE TABLE IF NOT EXISTS scene_mastery (scene_id TEXT PRIMARY KEY, attempts INTEGER NOT NULL, correct INTEGER NOT NULL, mastery REAL NOT NULL, last_practiced TEXT, FOREIGN KEY(scene_id) REFERENCES scenes(id))`,
		`CREATE TABLE IF NOT EXISTS error_stats (error_type TEXT PRIMARY KEY, count INTEGER NOT NULL, severity_sum REAL NOT NULL, last_seen TEXT NOT NULL)`,
		`CREATE TABLE IF NOT EXISTS review_schedule (id TEXT PRIMARY KEY, pattern_id TEXT NOT NULL, due_at TEXT NOT NULL, priority REAL NOT NULL, reason TEXT NOT NULL, last_practiced TEXT, FOREIGN KEY(pattern_id) REFERENCES sentence_patterns(id))`,
		`CREATE TABLE IF NOT EXISTS user_profile (id TEXT PRIMARY KEY, global_difficulty REAL NOT NULL, assessment_complete INTEGER NOT NULL, assessment_progress TEXT NOT NULL, updated_at TEXT NOT NULL)`,
		`CREATE TABLE IF NOT EXISTS llm_providers (id TEXT PRIMARY KEY, name TEXT NOT NULL, type TEXT NOT NULL, base_url TEXT NOT NULL, api_key TEXT NOT NULL DEFAULT '', model TEXT NOT NULL, timeout INTEGER NOT NULL, temperature REAL NOT NULL, max_tokens INTEGER NOT NULL, enabled INTEGER NOT NULL)`,
		`CREATE TABLE IF NOT EXISTS llm_task_configs (task_type TEXT PRIMARY KEY, provider_id TEXT NOT NULL, model TEXT NOT NULL, temperature REAL NOT NULL, max_tokens INTEGER NOT NULL, FOREIGN KEY(provider_id) REFERENCES llm_providers(id))`,
		`CREATE TABLE IF NOT EXISTS skills (id TEXT PRIMARY KEY, name TEXT NOT NULL, description TEXT NOT NULL DEFAULT '', level INTEGER NOT NULL DEFAULT 1, metadata_json TEXT NOT NULL DEFAULT '{}')`,
		`CREATE TABLE IF NOT EXISTS skill_edges (id TEXT PRIMARY KEY, from_skill_id TEXT NOT NULL, to_skill_id TEXT NOT NULL, relation TEXT NOT NULL, weight REAL NOT NULL DEFAULT 1, metadata_json TEXT NOT NULL DEFAULT '{}', UNIQUE(from_skill_id,to_skill_id,relation), FOREIGN KEY(from_skill_id) REFERENCES skills(id), FOREIGN KEY(to_skill_id) REFERENCES skills(id))`,
		`CREATE TABLE IF NOT EXISTS pattern_skills (pattern_id TEXT NOT NULL, skill_id TEXT NOT NULL, weight REAL NOT NULL DEFAULT 1, PRIMARY KEY(pattern_id,skill_id), FOREIGN KEY(pattern_id) REFERENCES sentence_patterns(id), FOREIGN KEY(skill_id) REFERENCES skills(id))`,
		`CREATE TABLE IF NOT EXISTS learner_skill_state (user_id TEXT NOT NULL, skill_id TEXT NOT NULL DEFAULT '', pattern_id TEXT NOT NULL, mastery REAL NOT NULL DEFAULT 0.25, acquisition REAL NOT NULL DEFAULT 0, retention REAL NOT NULL DEFAULT 0, transfer REAL NOT NULL DEFAULT 0, attempt_count INTEGER NOT NULL DEFAULT 0, success_count INTEGER NOT NULL DEFAULT 0, failure_count INTEGER NOT NULL DEFAULT 0, recent_accuracy REAL NOT NULL DEFAULT 0, long_term_accuracy REAL NOT NULL DEFAULT 0, current_difficulty REAL NOT NULL DEFAULT 1, max_success_difficulty REAL NOT NULL DEFAULT 1, consecutive_success INTEGER NOT NULL DEFAULT 0, consecutive_failure INTEGER NOT NULL DEFAULT 0, last_seen_at TEXT, last_success_at TEXT, last_failure_at TEXT, next_review_at TEXT, scene_coverage TEXT NOT NULL DEFAULT '{}', intent_coverage TEXT NOT NULL DEFAULT '{}', context_diversity REAL NOT NULL DEFAULT 0, memory_strength REAL NOT NULL DEFAULT 0, stability REAL NOT NULL DEFAULT 0, state_version INTEGER NOT NULL DEFAULT 1, updated_at TEXT NOT NULL, PRIMARY KEY(user_id,pattern_id))`,
		`CREATE TABLE IF NOT EXISTS difficulty_state (scope TEXT NOT NULL, entity_id TEXT NOT NULL, difficulty REAL NOT NULL, success_rate REAL NOT NULL DEFAULT 0.5, attempts INTEGER NOT NULL DEFAULT 0, empirical_difficulty REAL NOT NULL DEFAULT 0, confidence REAL NOT NULL DEFAULT 0, updated_at TEXT NOT NULL, PRIMARY KEY(scope,entity_id))`,
		`CREATE TABLE IF NOT EXISTS adaptive_config (key TEXT PRIMARY KEY, value TEXT NOT NULL)`,
		`CREATE TABLE IF NOT EXISTS feedback (id TEXT PRIMARY KEY, exercise_id TEXT NOT NULL DEFAULT '', attempt_id TEXT NOT NULL DEFAULT '', session_id TEXT NOT NULL DEFAULT '', feedback_type TEXT NOT NULL, details_json TEXT NOT NULL DEFAULT '{}', created_at TEXT NOT NULL)`,
		`CREATE TABLE IF NOT EXISTS skill_unlock_events (id TEXT PRIMARY KEY, skill_id TEXT NOT NULL, session_id TEXT NOT NULL DEFAULT '', unlocked_at TEXT NOT NULL, prerequisite_evidence TEXT NOT NULL DEFAULT '{}', prerequisite_mastery REAL NOT NULL DEFAULT 0, retention REAL NOT NULL DEFAULT 0, probe_evidence TEXT NOT NULL DEFAULT '{}', unlock_reason TEXT NOT NULL DEFAULT '')`,
		`CREATE TABLE IF NOT EXISTS difficulty_validation_events (id TEXT PRIMARY KEY, exercise_id TEXT NOT NULL DEFAULT '', target_difficulty REAL NOT NULL DEFAULT 0, realized_difficulty REAL NOT NULL DEFAULT 0, difficulty_delta REAL NOT NULL DEFAULT 0, status TEXT NOT NULL, reason TEXT NOT NULL DEFAULT '', retry_count INTEGER NOT NULL DEFAULT 0, created_at TEXT NOT NULL)`,
		`CREATE TABLE IF NOT EXISTS scene_pattern_map (scene_id TEXT NOT NULL, pattern_id TEXT NOT NULL, weight REAL NOT NULL DEFAULT 1, PRIMARY KEY(scene_id,pattern_id), FOREIGN KEY(scene_id) REFERENCES scenes(id), FOREIGN KEY(pattern_id) REFERENCES sentence_patterns(id))`,
		`CREATE TABLE IF NOT EXISTS scene_intent_map (scene_id TEXT NOT NULL, intent_id TEXT NOT NULL, weight REAL NOT NULL DEFAULT 1, PRIMARY KEY(scene_id,intent_id), FOREIGN KEY(scene_id) REFERENCES scenes(id), FOREIGN KEY(intent_id) REFERENCES communication_intents(id))`,
		`CREATE TABLE IF NOT EXISTS scene_skill_map (scene_id TEXT NOT NULL, skill_id TEXT NOT NULL, weight REAL NOT NULL DEFAULT 1, PRIMARY KEY(scene_id,skill_id), FOREIGN KEY(scene_id) REFERENCES scenes(id), FOREIGN KEY(skill_id) REFERENCES skills(id))`,
		`CREATE TABLE IF NOT EXISTS scene_transfer_events (id TEXT PRIMARY KEY, pattern_id TEXT NOT NULL, previous_scene_count INTEGER NOT NULL, new_scene_id TEXT NOT NULL, transfer_before REAL NOT NULL, transfer_after REAL NOT NULL, intent_count INTEGER NOT NULL, created_at TEXT NOT NULL)`,
		`CREATE TABLE IF NOT EXISTS curriculum_skills (skill_id TEXT PRIMARY KEY, curriculum_version TEXT NOT NULL, app_level INTEGER NOT NULL, cefr_anchor TEXT NOT NULL, metadata_json TEXT NOT NULL DEFAULT '{}')`,
		`CREATE TABLE IF NOT EXISTS curriculum_patterns (pattern_id TEXT PRIMARY KEY, curriculum_version TEXT NOT NULL, display_name TEXT NOT NULL, app_level_min INTEGER NOT NULL, cefr_anchor TEXT NOT NULL, grammar_family TEXT NOT NULL, communication_functions_json TEXT NOT NULL DEFAULT '[]', prerequisites_json TEXT NOT NULL DEFAULT '[]', productive_complexity REAL NOT NULL, typical_contexts_json TEXT NOT NULL DEFAULT '[]', rationale TEXT NOT NULL DEFAULT '', confidence TEXT NOT NULL DEFAULT 'medium', instruction_complexity INTEGER NOT NULL DEFAULT 1, allowed_grammar_json TEXT NOT NULL DEFAULT '[]', not_yet_targetable_json TEXT NOT NULL DEFAULT '[]', metadata_json TEXT NOT NULL DEFAULT '{}')`,
		`CREATE TABLE IF NOT EXISTS level_completions (level INTEGER NOT NULL, curriculum_version TEXT NOT NULL, completed_at TEXT NOT NULL, PRIMARY KEY(level,curriculum_version))`,
	}
	for _, q := range stmts {
		if _, err := db.Exec(q); err != nil {
			return err
		}
	}
	if err := ensureColumn(db, "attempts", "evaluation_diagnostics_json", "TEXT NOT NULL DEFAULT '{}'"); err != nil {
		return err
	}
	for _, c := range []struct{ name, definition string }{
		{"intent_id", "TEXT NOT NULL DEFAULT ''"},
		{"exercise_difficulty", "REAL NOT NULL DEFAULT 0"},
		{"practice_mode", "TEXT NOT NULL DEFAULT 'adaptive'"},
		{"selection_reason", "TEXT NOT NULL DEFAULT ''"},
		{"is_review", "INTEGER NOT NULL DEFAULT 0"},
		{"is_probe", "INTEGER NOT NULL DEFAULT 0"},
		{"is_new_skill", "INTEGER NOT NULL DEFAULT 0"},
		{"evaluation_scores_json", "TEXT NOT NULL DEFAULT '{}'"},
		{"error_types_json", "TEXT NOT NULL DEFAULT '[]'"},
		{"error_severity", "TEXT NOT NULL DEFAULT ''"},
		{"generated_by", "TEXT NOT NULL DEFAULT 'fallback'"},
		{"normalized_chinese_hash", "TEXT NOT NULL DEFAULT ''"},
		{"mastery_before", "REAL NOT NULL DEFAULT 0"},
		{"mastery_after", "REAL NOT NULL DEFAULT 0"},
		{"difficulty_before", "REAL NOT NULL DEFAULT 0"},
		{"difficulty_after", "REAL NOT NULL DEFAULT 0"},
		{"position", "INTEGER NOT NULL DEFAULT 0"},
		{"review_timing", "TEXT NOT NULL DEFAULT ''"},
		{"learner_ability", "REAL NOT NULL DEFAULT 0"},
		{"session_center", "REAL NOT NULL DEFAULT 0"},
		{"pattern_ability", "REAL NOT NULL DEFAULT 0"},
		{"target_difficulty", "REAL NOT NULL DEFAULT 0"},
		{"realized_difficulty", "REAL NOT NULL DEFAULT 0"},
		{"difficulty_delta", "REAL NOT NULL DEFAULT 0"},
		{"difficulty_validation_status", "TEXT NOT NULL DEFAULT 'unknown'"},
		{"difficulty_validation_reason", "TEXT NOT NULL DEFAULT ''"},
		{"difficulty_policy_version", "TEXT NOT NULL DEFAULT ''"},
		{"perceived_difficulty_signal", "TEXT NOT NULL DEFAULT ''"},
	} {
		if err := ensureColumn(db, "attempts", c.name, c.definition); err != nil {
			return err
		}
	}
	for _, c := range []struct{ table, name, definition string }{
		{"llm_providers", "supports_reasoning_control", "INTEGER NOT NULL DEFAULT 0"},
		{"llm_providers", "supports_json_mode", "INTEGER NOT NULL DEFAULT 1"},
		{"llm_providers", "exposes_reasoning_content", "INTEGER NOT NULL DEFAULT 1"},
		{"llm_task_configs", "reasoning_mode", "TEXT NOT NULL DEFAULT 'inherit'"},
		{"llm_task_configs", "reasoning_effort", "TEXT NOT NULL DEFAULT ''"},
		{"evaluations", "more_natural", "TEXT NOT NULL DEFAULT ''"},
		{"evaluations", "more_natural_needed", "INTEGER NOT NULL DEFAULT 0"},
		{"evaluations", "alternative", "TEXT NOT NULL DEFAULT ''"},
		{"evaluations", "target_pattern_match", "TEXT NOT NULL DEFAULT 'unknown'"},
		{"evaluations", "target_pattern_score", "REAL NOT NULL DEFAULT 0"},
		{"sentence_patterns", "catalog_difficulty", "REAL NOT NULL DEFAULT 0"},
		{"learner_skill_state", "evidence_count", "INTEGER NOT NULL DEFAULT 0"},
		{"learner_skill_state", "state_confidence", "REAL NOT NULL DEFAULT 0"},
		{"learner_skill_state", "state", "TEXT NOT NULL DEFAULT 'UNKNOWN'"},
		{"difficulty_state", "empirical_difficulty", "REAL NOT NULL DEFAULT 0"},
		{"difficulty_state", "confidence", "REAL NOT NULL DEFAULT 0"},
		{"sessions", "attempt_count", "INTEGER NOT NULL DEFAULT 0"},
		{"sessions", "start_global_difficulty", "REAL NOT NULL DEFAULT 0"},
		{"sessions", "end_global_difficulty", "REAL NOT NULL DEFAULT 0"},
		{"sessions", "patterns_seen", "TEXT NOT NULL DEFAULT '{}'"},
		{"sessions", "skills_seen", "TEXT NOT NULL DEFAULT '{}'"},
		{"sessions", "reviews_served", "INTEGER NOT NULL DEFAULT 0"},
		{"sessions", "probes_served", "INTEGER NOT NULL DEFAULT 0"},
		{"sessions", "new_skills_served", "INTEGER NOT NULL DEFAULT 0"},
		{"sessions", "session_difficulty_center", "REAL NOT NULL DEFAULT 0"},
		{"sessions", "session_band_lower", "REAL NOT NULL DEFAULT 0"},
		{"sessions", "session_band_upper", "REAL NOT NULL DEFAULT 0"},
		{"sessions", "session_center_confidence", "REAL NOT NULL DEFAULT 0"},
		{"sessions", "session_evidence_count", "INTEGER NOT NULL DEFAULT 0"},
		{"sessions", "scene_id", "TEXT NOT NULL DEFAULT ''"},
		{"sessions", "subscene_id", "TEXT NOT NULL DEFAULT ''"},
		{"sessions", "practice_scope", "TEXT NOT NULL DEFAULT 'global'"},
		{"sessions", "difficulty_mode", "TEXT NOT NULL DEFAULT 'adaptive'"},
		{"sessions", "fixed_difficulty", "REAL NOT NULL DEFAULT 0"},
		{"sessions", "training_focus", "TEXT NOT NULL DEFAULT 'pattern'"},
		{"sessions", "curriculum_version", "TEXT NOT NULL DEFAULT '" + curriculumVersion + "'"},
		{"attempts", "difficulty_mode", "TEXT NOT NULL DEFAULT 'adaptive'"},
		{"attempts", "fixed_difficulty", "REAL NOT NULL DEFAULT 0"},
		{"attempts", "training_focus", "TEXT NOT NULL DEFAULT 'pattern'"},
		{"attempts", "target_pattern_present", "INTEGER NOT NULL DEFAULT 1"},
		{"scenes", "parent_id", "TEXT NOT NULL DEFAULT ''"},
		{"scenes", "category", "TEXT NOT NULL DEFAULT ''"},
		{"scenes", "difficulty_min", "REAL NOT NULL DEFAULT 1"},
		{"scenes", "difficulty_max", "REAL NOT NULL DEFAULT 8"},
		{"scenes", "enabled", "INTEGER NOT NULL DEFAULT 1"},
		{"scenes", "sort_order", "INTEGER NOT NULL DEFAULT 0"},
		{"scene_mastery", "recent_accuracy", "REAL NOT NULL DEFAULT 0"},
		{"scene_mastery", "effective_difficulty", "REAL NOT NULL DEFAULT 0"},
		{"scene_mastery", "evidence_count", "INTEGER NOT NULL DEFAULT 0"},
		{"scene_mastery", "confidence", "REAL NOT NULL DEFAULT 0"},
		{"scene_mastery", "pattern_coverage", "REAL NOT NULL DEFAULT 0"},
		{"scene_mastery", "intent_coverage", "REAL NOT NULL DEFAULT 0"},
		{"scene_mastery", "transfer", "REAL NOT NULL DEFAULT 0"},
		{"scene_mastery", "state", "TEXT NOT NULL DEFAULT 'UNKNOWN'"},
		{"sentence_patterns", "curriculum_version", "TEXT NOT NULL DEFAULT ''"},
		{"sentence_patterns", "curriculum_level", "INTEGER NOT NULL DEFAULT 0"},
		{"exercises", "curriculum_version", "TEXT NOT NULL DEFAULT ''"},
		{"exercises", "curriculum_level", "INTEGER NOT NULL DEFAULT 0"},
		{"attempts", "curriculum_version", "TEXT NOT NULL DEFAULT ''"},
		{"attempts", "curriculum_level", "INTEGER NOT NULL DEFAULT 0"},
	} {
		if err := ensureColumn(db, c.table, c.name, c.definition); err != nil {
			return err
		}
	}
	if _, err := db.Exec(`UPDATE sentence_patterns SET catalog_difficulty=difficulty WHERE catalog_difficulty=0`); err != nil {
		return err
	}
	for _, c := range []struct{ name, definition string }{{"normalized_chinese_hash", "TEXT NOT NULL DEFAULT ''"}, {"generated_by", "TEXT NOT NULL DEFAULT 'fallback'"}} {
		if err := ensureColumn(db, "exercises", c.name, c.definition); err != nil {
			return err
		}
	}
	if err := ensureColumn(db, "exercises", "subscene_id", "TEXT NOT NULL DEFAULT ''"); err != nil {
		return err
	}
	if err := ensureColumn(db, "exercises", "decision_trace_json", "TEXT NOT NULL DEFAULT '{}' "); err != nil {
		return err
	}
	for _, c := range []struct{ name, definition string }{
		{"training_focus", "TEXT NOT NULL DEFAULT 'pattern'"},
		{"difficulty_mode", "TEXT NOT NULL DEFAULT 'adaptive'"},
		{"fixed_difficulty", "REAL NOT NULL DEFAULT 0"},
		{"target_pattern_present", "INTEGER NOT NULL DEFAULT 1"},
	} {
		if err := ensureColumn(db, "exercises", c.name, c.definition); err != nil {
			return err
		}
	}
	for _, c := range []struct{ name, definition string }{
		{"target_difficulty", "REAL NOT NULL DEFAULT 0"},
		{"realized_difficulty", "REAL NOT NULL DEFAULT 0"},
		{"difficulty_delta", "REAL NOT NULL DEFAULT 0"},
		{"difficulty_validation_status", "TEXT NOT NULL DEFAULT 'unknown'"},
		{"difficulty_validation_reason", "TEXT NOT NULL DEFAULT ''"},
		{"difficulty_policy_version", "TEXT NOT NULL DEFAULT ''"},
	} {
		if err := ensureColumn(db, "exercises", c.name, c.definition); err != nil {
			return err
		}
	}
	for _, q := range []string{
		`CREATE INDEX IF NOT EXISTS idx_attempts_submitted ON attempts(submitted_at DESC)`,
		`CREATE INDEX IF NOT EXISTS idx_attempts_pattern ON attempts(exercise_id,submitted_at DESC)`,
		`CREATE INDEX IF NOT EXISTS idx_learner_review ON learner_skill_state(next_review_at)`,
		`CREATE INDEX IF NOT EXISTS idx_exercises_pattern_created ON exercises(pattern_id,created_at DESC)`,
		`CREATE INDEX IF NOT EXISTS idx_feedback_session ON feedback(session_id,created_at DESC)`,
		`CREATE INDEX IF NOT EXISTS idx_feedback_pattern ON feedback(exercise_id,created_at DESC)`,
		`CREATE INDEX IF NOT EXISTS idx_unlock_events_skill ON skill_unlock_events(skill_id,unlocked_at DESC)`,
		`CREATE INDEX IF NOT EXISTS idx_scene_pattern_map_pattern ON scene_pattern_map(pattern_id,scene_id)`,
		`CREATE INDEX IF NOT EXISTS idx_scene_intent_map_intent ON scene_intent_map(intent_id,scene_id)`,
		`CREATE INDEX IF NOT EXISTS idx_scenes_parent ON scenes(parent_id,sort_order)`,
		`CREATE INDEX IF NOT EXISTS idx_scene_transfer_pattern ON scene_transfer_events(pattern_id,created_at DESC)`,
	} {
		if _, err := db.Exec(q); err != nil {
			return err
		}
	}
	var v int
	if err := db.QueryRow("SELECT COALESCE(MAX(version),0) FROM schema_meta").Scan(&v); err != nil {
		return err
	}
	if v < schemaVersion {
		_, err := db.Exec("INSERT INTO schema_meta(version) VALUES(?)", schemaVersion)
		return err
	}
	return nil
}

func ensureColumn(db *sql.DB, table, column, definition string) error {
	rows, err := db.Query("PRAGMA table_info(" + table + ")")
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var cid int
		var name, typ string
		var notNull, pk int
		var defaultValue sql.NullString
		if err := rows.Scan(&cid, &name, &typ, &notNull, &defaultValue, &pk); err != nil {
			return err
		}
		if name == column {
			return nil
		}
	}
	if err := rows.Err(); err != nil {
		return err
	}
	_, err = db.Exec("ALTER TABLE " + table + " ADD COLUMN " + column + " " + definition)
	return err
}

func seed(db *sql.DB) error {
	var count int
	if err := db.QueryRow("SELECT COUNT(*) FROM scenes").Scan(&count); err != nil {
		return err
	}
	if count > 0 {
		return seedAdaptiveData(db)
	}
	for _, s := range []struct{ id, n, d string }{{"daily", "Daily Conversation", "Everyday exchanges"}, {"friends", "Friends", "Casual conversations"}, {"work", "Work Meeting", "Workplace communication"}, {"restaurant", "Restaurant", "Ordering and service"}, {"travel", "Travel", "Travel situations"}, {"phone", "Phone Call", "Phone conversations"}, {"problem", "Problem Explanation", "Explaining issues"}, {"refusal", "Polite Refusal", "Declining respectfully"}} {
		if _, err := db.Exec("INSERT INTO scenes(id,name,description) VALUES(?,?,?)", s.id, s.n, s.d); err != nil {
			return err
		}
	}
	for _, i := range []struct{ id, n string }{{"plans", "Plans and intentions"}, {"possibility", "Possibility"}, {"condition", "Condition"}, {"request", "Request"}, {"refusal", "Polite refusal"}, {"explanation", "Explanation"}} {
		if _, err := db.Exec("INSERT INTO communication_intents(id,name) VALUES(?,?)", i.id, i.n); err != nil {
			return err
		}
	}
	patterns := []struct {
		id, p, i string
		d        float64
	}{{"going-to", "was/were going to", "plans", 2.2}, {"modal-possibility", "might / may", "possibility", 3.0}, {"conditional", "if ... would", "condition", 4.5}, {"polite-request", "Could you ...?", "request", 3.2}, {"polite-refusal", "I don't think I'll be able to ...", "refusal", 4.1}, {"because", "because / so", "explanation", 2.8}, {"past-perfect", "had already ...", "plans", 5.2}, {"wish-past", "I wish I had ...", "explanation", 5.8}}
	for _, p := range patterns {
		if _, err := db.Exec("INSERT INTO sentence_patterns(id,pattern,intent_id,difficulty) VALUES(?,?,?,?)", p.id, p.p, p.i, p.d); err != nil {
			return err
		}
		if _, err := db.Exec("INSERT INTO pattern_mastery(pattern_id,attempts,correct,recent_accuracy,long_term_accuracy,consecutive_correct,mastery) VALUES(?,0,0,0,0,0,0.25)", p.id); err != nil {
			return err
		}
	}
	_, err := db.Exec("INSERT INTO user_profile(id,global_difficulty,assessment_complete,assessment_progress,updated_at) VALUES('default',3.0,0,'[]',?)", time.Now().UTC().Format(time.RFC3339))
	if err != nil {
		return err
	}
	return seedAdaptiveData(db)
}

func loadProviders(s *Server) error {
	rows, err := s.db.Query(`SELECT id,name,type,base_url,api_key,model,timeout,temperature,max_tokens,enabled,supports_reasoning_control,supports_json_mode,exposes_reasoning_content FROM llm_providers`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var c ProviderConfig
		var en, reasoningControl, jsonMode, reasoningContent int
		if err := rows.Scan(&c.ID, &c.Name, &c.Type, &c.BaseURL, &c.APIKey, &c.Model, &c.Timeout, &c.Temperature, &c.MaxTokens, &en, &reasoningControl, &jsonMode, &reasoningContent); err != nil {
			return err
		}
		c.Enabled = en == 1
		c.SupportsReasoningControl = reasoningControl == 1
		c.SupportsJSONMode = jsonMode == 1
		c.ExposesReasoningContent = reasoningContent == 1
		s.llm.configs[c.ID] = c
	}
	return rows.Err()
}

func logging(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		start := time.Now()
		next.ServeHTTP(w, r)
		log.Printf("%s %s %s", r.Method, r.URL.Path, time.Since(start))
	})
}

func jsonResp(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
func decode(r *http.Request, v any) error {
	return json.NewDecoder(io.LimitReader(r.Body, 2<<20)).Decode(v)
}
func id(prefix string) string {
	return fmt.Sprintf("%s_%d_%04d", prefix, time.Now().UnixNano(), rand.Intn(10000))
}

func registerRoutes(mux *http.ServeMux, s *Server, static http.Handler) {
	mux.HandleFunc("/api/health", func(w http.ResponseWriter, r *http.Request) {
		jsonResp(w, 200, map[string]any{"status": "ok", "schema_version": schemaVersion})
	})
	mux.HandleFunc("/api/scenes", func(w http.ResponseWriter, r *http.Request) {
		rows, err := s.db.Query("SELECT id FROM scenes WHERE parent_id='' AND enabled=1 ORDER BY sort_order,name")
		if err != nil {
			jsonResp(w, 500, map[string]string{"error": err.Error()})
			return
		}
		var sceneIDs []string
		for rows.Next() {
			var sceneID string
			if rows.Scan(&sceneID) == nil {
				sceneIDs = append(sceneIDs, sceneID)
			}
		}
		rows.Close()
		out := []map[string]any{}
		for _, sceneID := range sceneIDs {
			if detail, detailErr := s.sceneDetail(sceneID); detailErr == nil {
				out = append(out, detail)
			}
		}
		jsonResp(w, 200, out)
	})
	mux.HandleFunc("/api/scenes/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			jsonResp(w, 405, nil)
			return
		}
		parts := strings.Split(strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/scenes/"), "/"), "/")
		if len(parts) == 0 || parts[0] == "" {
			jsonResp(w, 400, map[string]string{"error": "scene id is required"})
			return
		}
		detail, err := s.sceneDetail(parts[0])
		if err != nil {
			jsonResp(w, 404, map[string]string{"error": "scene not found"})
			return
		}
		if len(parts) == 2 && parts[1] == "progress" {
			jsonResp(w, 200, map[string]any{"scene_id": parts[0], "progress": detail["mastery"], "subscenes": detail["subscenes"]})
			return
		}
		jsonResp(w, 200, detail)
	})
	mux.HandleFunc("/api/patterns", func(w http.ResponseWriter, r *http.Request) {
		rows, err := s.db.Query(`SELECT p.id,p.pattern,COALESCE(p.catalog_difficulty,p.difficulty),COALESCE(ls.mastery,COALESCE(m.mastery,.25)),COALESCE(ls.attempt_count,COALESCE(m.attempts,0)),COALESCE(ls.state,'UNKNOWN') FROM sentence_patterns p LEFT JOIN pattern_mastery m ON p.id=m.pattern_id LEFT JOIN learner_skill_state ls ON ls.pattern_id=p.id AND ls.user_id='default' ORDER BY p.difficulty`)
		if err != nil {
			jsonResp(w, 500, map[string]string{"error": err.Error()})
			return
		}
		defer rows.Close()
		out := []map[string]any{}
		for rows.Next() {
			var id, p string
			var d, ma float64
			var a int
			var state string
			_ = rows.Scan(&id, &p, &d, &ma, &a, &state)
			out = append(out, map[string]any{"id": id, "pattern": p, "difficulty": d, "catalog_difficulty": d, "mastery": ma, "attempts": a, "state": state})
		}
		jsonResp(w, 200, out)
	})
	mux.HandleFunc("/api/profile", func(w http.ResponseWriter, r *http.Request) {
		var d float64
		var complete int
		var prog string
		_ = s.db.QueryRow("SELECT global_difficulty,assessment_complete,assessment_progress FROM user_profile WHERE id='default'").Scan(&d, &complete, &prog)
		var p any
		_ = json.Unmarshal([]byte(prog), &p)
		jsonResp(w, 200, map[string]any{"global_difficulty": d, "assessment_complete": complete == 1, "assessment_progress": p})
	})
	mux.HandleFunc("/api/assessment/start", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			jsonResp(w, 405, nil)
			return
		}
		var prof float64
		_ = s.db.QueryRow("SELECT global_difficulty FROM user_profile WHERE id='default'").Scan(&prof)
		ex, err := s.generateExercise(r.Context(), prof, "assessment", "")
		if err != nil {
			jsonResp(w, 500, map[string]string{"error": err.Error()})
			return
		}
		jsonResp(w, 200, map[string]any{"assessment_id": id("assessment"), "total": 12, "completed": 0, "exercise": ex})
	})
	mux.HandleFunc("/api/practice/next", func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Mode            string  `json:"mode"`
			SceneID         string  `json:"scene_id"`
			SubsceneID      string  `json:"subscene_id"`
			SessionID       string  `json:"session_id"`
			DifficultyMode  string  `json:"difficulty_mode"`
			FixedDifficulty float64 `json:"fixed_difficulty"`
			TrainingFocus   string  `json:"training_focus"`
		}
		if r.Method == http.MethodPost {
			_ = decode(r, &req)
		}
		prefs, prefsErr := practicePreferencesFromSession(s.db, req.SessionID)
		if prefsErr != nil && req.SessionID != "" {
			jsonResp(w, 400, map[string]string{"error": "session not found"})
			return
		}
		if req.DifficultyMode != "" || req.TrainingFocus != "" || req.FixedDifficulty != 0 {
			requested := prefs
			if req.DifficultyMode != "" {
				requested.DifficultyMode = req.DifficultyMode
			}
			if req.TrainingFocus != "" {
				requested.TrainingFocus = req.TrainingFocus
			}
			if req.FixedDifficulty != 0 {
				requested.FixedDifficulty = req.FixedDifficulty
			}
			var err error
			prefs, err = normalizePracticePreferences(requested)
			if err != nil {
				jsonResp(w, 400, map[string]string{"error": err.Error()})
				return
			}
			// A first practice request can carry explicit preferences before the
			// client has a session. Persist those preferences in a new session so
			// generation and exercise snapshots use the requested mode.
			if req.SessionID == "" && (req.DifficultyMode != "" || req.TrainingFocus != "" || req.FixedDifficulty != 0) {
				var globalDifficulty float64
				_ = s.db.QueryRow("SELECT global_difficulty FROM user_profile WHERE id='default'").Scan(&globalDifficulty)
				center := globalDifficulty
				if center <= 0 {
					center = 3
				}
				if prefs.DifficultyMode == DifficultyModeFixed {
					center = prefs.FixedDifficulty
				}
				lower, upper := sessionBand(center, difficultyConfig(s.adaptiveConfig()))
				if prefs.DifficultyMode == DifficultyModeFixed {
					lower, upper = fixedDifficultyBand(center, s.adaptiveConfig())
				}
				sessionID := id("session")
				requestMode := req.Mode
				if requestMode == "" {
					requestMode = DifficultyModeAdaptive
				}
				practiceScope := "global"
				if req.SubsceneID != "" {
					practiceScope = "subscene"
				} else if req.SceneID != "" {
					practiceScope = "scene"
				}
				_, err := s.db.Exec(`INSERT INTO sessions(id,mode,started_at,start_global_difficulty,end_global_difficulty,session_difficulty_center,session_band_lower,session_band_upper,session_center_confidence,session_evidence_count,scene_id,subscene_id,practice_scope,difficulty_mode,fixed_difficulty,training_focus,curriculum_version) VALUES(?,?,?,?,?,?,?,?,?,0,?,?,?,?,?,?,?)`, sessionID, requestMode, time.Now().UTC().Format(time.RFC3339), globalDifficulty, globalDifficulty, center, lower, upper, 0, req.SceneID, req.SubsceneID, practiceScope, prefs.DifficultyMode, prefs.FixedDifficulty, prefs.TrainingFocus, curriculumVersion)
				if err != nil {
					jsonResp(w, 500, map[string]string{"error": err.Error()})
					return
				}
				req.SessionID = sessionID
			}
			if req.SessionID != "" {
				if err := writePracticePreferences(s.db, req.SessionID, prefs); err != nil {
					jsonResp(w, 500, map[string]string{"error": err.Error()})
					return
				}
			}
		}
		var diff float64
		_ = s.db.QueryRow("SELECT global_difficulty FROM user_profile WHERE id='default'").Scan(&diff)
		if req.SessionID != "" {
			if state, sessionErr := loadSessionDifficulty(s.db, req.SessionID); sessionErr == nil && state.Center > 0 {
				diff = state.Center
			}
		}
		ex, err := s.generateExerciseForScene(r.Context(), diff, req.Mode, req.SceneID, req.SubsceneID, req.SessionID)
		if err != nil {
			jsonResp(w, 500, map[string]string{"error": err.Error()})
			return
		}
		ex["difficulty_mode"] = prefs.DifficultyMode
		ex["fixed_difficulty"] = prefs.FixedDifficulty
		ex["training_focus"] = prefs.TrainingFocus
		ex["target_pattern_enabled"] = prefs.TargetPatternEnabled
		if req.SessionID != "" {
			ex["session_id"] = req.SessionID
		}
		jsonResp(w, 200, ex)
	})
	mux.HandleFunc("/api/sessions", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			var req struct {
				Mode            string  `json:"mode"`
				SceneID         string  `json:"scene_id"`
				SubsceneID      string  `json:"subscene_id"`
				PracticeScope   string  `json:"practice_scope"`
				DifficultyMode  string  `json:"difficulty_mode"`
				FixedDifficulty float64 `json:"fixed_difficulty"`
				TrainingFocus   string  `json:"training_focus"`
			}
			_ = decode(r, &req)
			if req.Mode == "" {
				req.Mode = "adaptive"
			}
			if req.PracticeScope == "" {
				req.PracticeScope = "global"
				if req.SubsceneID != "" {
					req.PracticeScope = "subscene"
				} else if req.SceneID != "" {
					req.PracticeScope = "scene"
				}
			}
			prefs, prefsErr := normalizePracticePreferences(PracticePreferences{DifficultyMode: req.DifficultyMode, FixedDifficulty: req.FixedDifficulty, TrainingFocus: req.TrainingFocus})
			if prefsErr != nil {
				jsonResp(w, 400, map[string]string{"error": prefsErr.Error()})
				return
			}
			session := id("session")
			var difficulty float64
			_ = s.db.QueryRow("SELECT global_difficulty FROM user_profile WHERE id='default'").Scan(&difficulty)
			cfg := difficultyConfig(s.adaptiveConfig())
			center := difficulty
			lower, upper := sessionBand(difficulty, cfg)
			if prefs.DifficultyMode == DifficultyModeFixed {
				center = prefs.FixedDifficulty
				lower, upper = fixedDifficultyBand(center, cfg)
			}
			_, err := s.db.Exec("INSERT INTO sessions(id,mode,started_at,end_global_difficulty,start_global_difficulty,session_difficulty_center,session_band_lower,session_band_upper,session_center_confidence,session_evidence_count,scene_id,subscene_id,practice_scope,difficulty_mode,fixed_difficulty,training_focus,curriculum_version) VALUES(?,?,?,?,?,?,?,?,?,0,?,?,?,?,?,?,?)", session, req.Mode, time.Now().UTC().Format(time.RFC3339), difficulty, difficulty, center, lower, upper, 0, req.SceneID, req.SubsceneID, req.PracticeScope, prefs.DifficultyMode, prefs.FixedDifficulty, prefs.TrainingFocus, curriculumVersion)
			if err != nil {
				jsonResp(w, 500, map[string]string{"error": err.Error()})
				return
			}
			jsonResp(w, 201, map[string]any{"session_id": session, "mode": req.Mode, "scene_id": req.SceneID, "subscene_id": req.SubsceneID, "practice_scope": req.PracticeScope, "difficulty_mode": prefs.DifficultyMode, "fixed_difficulty": prefs.FixedDifficulty, "training_focus": prefs.TrainingFocus, "target_pattern_enabled": prefs.TargetPatternEnabled, "curriculum_version": curriculumVersion, "session_center": center, "session_band_lower": lower, "session_band_upper": upper})
			return
		}
		rows, err := s.db.Query("SELECT id,mode,started_at,ended_at,attempt_count,start_global_difficulty,end_global_difficulty,patterns_seen,skills_seen,reviews_served,probes_served,new_skills_served,session_difficulty_center,session_band_lower,session_band_upper,session_center_confidence,session_evidence_count,scene_id,subscene_id,practice_scope,difficulty_mode,fixed_difficulty,training_focus FROM sessions ORDER BY started_at DESC LIMIT 50")
		if err != nil {
			jsonResp(w, 500, map[string]string{"error": err.Error()})
			return
		}
		defer rows.Close()
		out := []map[string]any{}
		for rows.Next() {
			var idv, mode, start, patterns, skills, sceneID, subsceneID, practiceScope, difficultyMode, trainingFocus string
			var end sql.NullString
			var count, reviews, probes, newSkills int
			var startDifficulty, endDifficulty, center, lower, upper, confidence, fixedDifficulty float64
			var evidence int
			_ = rows.Scan(&idv, &mode, &start, &end, &count, &startDifficulty, &endDifficulty, &patterns, &skills, &reviews, &probes, &newSkills, &center, &lower, &upper, &confidence, &evidence, &sceneID, &subsceneID, &practiceScope, &difficultyMode, &fixedDifficulty, &trainingFocus)
			out = append(out, map[string]any{"session_id": idv, "mode": mode, "started_at": start, "ended_at": end.String, "attempt_count": count, "start_global_difficulty": startDifficulty, "end_global_difficulty": endDifficulty, "session_difficulty_center": center, "session_band_lower": lower, "session_band_upper": upper, "session_center_confidence": confidence, "session_evidence_count": evidence, "scene_id": sceneID, "subscene_id": subsceneID, "practice_scope": practiceScope, "difficulty_mode": difficultyMode, "fixed_difficulty": fixedDifficulty, "training_focus": trainingFocus, "target_pattern_enabled": trainingFocus == TrainingFocusPattern, "patterns_seen": decodeJSONMap(patterns), "skills_seen": decodeJSONMap(skills), "reviews_served": reviews, "probes_served": probes, "new_skills_served": newSkills})
		}
		jsonResp(w, 200, out)
	})
	mux.HandleFunc("/api/attempts", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			jsonResp(w, 405, nil)
			return
		}
		var req struct {
			SessionID  string `json:"session_id"`
			ExerciseID string `json:"exercise_id"`
			Answer     string `json:"answer"`
			Mode       string `json:"mode"`
		}
		if err := decode(r, &req); err != nil {
			jsonResp(w, 400, map[string]string{"error": "invalid request"})
			return
		}
		result, err := s.submitAttempt(r.Context(), req.SessionID, req.ExerciseID, req.Answer)
		if err != nil {
			jsonResp(w, 500, map[string]string{"error": err.Error()})
			return
		}
		jsonResp(w, 200, result)
	})
	mux.HandleFunc("/api/attempts/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost || !strings.HasSuffix(r.URL.Path, "/reevaluate") {
			jsonResp(w, 404, map[string]string{"error": "not found"})
			return
		}
		attemptID := strings.TrimSuffix(strings.TrimPrefix(strings.TrimPrefix(r.URL.Path, "/api/attempts/"), "/"), "/reevaluate")
		if attemptID == "" {
			jsonResp(w, 400, map[string]string{"error": "attempt id is required"})
			return
		}
		result, err := s.reevaluateAttempt(r.Context(), attemptID)
		if err != nil {
			if errors.Is(err, sql.ErrNoRows) {
				jsonResp(w, 404, map[string]string{"error": "attempt not found"})
			} else {
				jsonResp(w, 500, map[string]string{"error": err.Error()})
			}
			return
		}
		jsonResp(w, 200, result)
	})
	mux.HandleFunc("/api/assessment/submit", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			jsonResp(w, 405, nil)
			return
		}
		var req struct {
			ExerciseID string `json:"exercise_id"`
			Answer     string `json:"answer"`
		}
		if err := decode(r, &req); err != nil {
			jsonResp(w, 400, map[string]string{"error": "invalid request"})
			return
		}
		result, err := s.submitAttempt(r.Context(), "assessment", req.ExerciseID, req.Answer)
		if err != nil {
			jsonResp(w, 500, map[string]string{"error": err.Error()})
			return
		}
		if ev, ok := result["evaluation"].(Eval); ok {
			_ = s.updateAssessment(ev)
		}
		jsonResp(w, 200, result)
	})
	mux.HandleFunc("/api/history", func(w http.ResponseWriter, r *http.Request) {
		query := `SELECT a.id,a.submitted_at,e.chinese_prompt,a.user_answer,COALESCE(v.verdict,''),COALESCE(v.suggested_answer,''),COALESCE(v.errors_json,'[]'),e.pattern_id,e.scene_id,COALESCE(e.subscene_id,''),a.intent_id,a.exercise_difficulty,COALESCE(NULLIF(a.target_difficulty,0),NULLIF(e.target_difficulty,0),e.difficulty),COALESCE(NULLIF(a.realized_difficulty,0),NULLIF(e.realized_difficulty,0),e.difficulty),COALESCE(NULLIF(a.difficulty_delta,0),ABS(COALESCE(NULLIF(e.realized_difficulty,0),e.difficulty)-COALESCE(NULLIF(e.target_difficulty,0),e.difficulty))),a.selection_reason,a.is_review,a.is_probe,a.generated_by,COALESCE(NULLIF(a.difficulty_validation_status,'unknown'),NULLIF(e.difficulty_validation_status,'unknown'),'unknown'),COALESCE(a.difficulty_mode,e.difficulty_mode,'adaptive'),COALESCE(a.fixed_difficulty,e.fixed_difficulty,0),COALESCE(a.training_focus,e.training_focus,'pattern'),COALESCE(a.target_pattern_present,e.target_pattern_present,1) FROM attempts a JOIN exercises e ON e.id=a.exercise_id LEFT JOIN evaluations v ON v.attempt_id=a.id`
		args := []any{}
		if sceneID := r.URL.Query().Get("scene_id"); sceneID != "" {
			query += " WHERE e.scene_id=?"
			args = append(args, sceneID)
		}
		if subsceneID := r.URL.Query().Get("subscene_id"); subsceneID != "" {
			if len(args) == 0 {
				query += " WHERE "
			} else {
				query += " AND "
			}
			query += "e.subscene_id=?"
			args = append(args, subsceneID)
		}
		query += " ORDER BY a.submitted_at DESC LIMIT 100"
		rows, err := s.db.Query(query, args...)
		if err != nil {
			jsonResp(w, 500, map[string]string{"error": err.Error()})
			return
		}
		defer rows.Close()
		out := []map[string]any{}
		for rows.Next() {
			var a, b, c, d, e, f, g, h, i, subscene, intent, reason, generated, validationStatus, difficultyMode, trainingFocus string
			var difficulty, targetDifficulty, realizedDifficulty, difficultyDelta, fixedDifficulty float64
			var review, probe, targetPatternPresent int
			_ = rows.Scan(&a, &b, &c, &d, &e, &f, &g, &h, &i, &subscene, &intent, &difficulty, &targetDifficulty, &realizedDifficulty, &difficultyDelta, &reason, &review, &probe, &generated, &validationStatus, &difficultyMode, &fixedDifficulty, &trainingFocus, &targetPatternPresent)
			var er any
			_ = json.Unmarshal([]byte(g), &er)
			out = append(out, map[string]any{"id": a, "submitted_at": b, "prompt": c, "answer": d, "verdict": e, "suggested_answer": f, "errors": er, "pattern_id": h, "scene_id": i, "subscene_id": subscene, "intent_id": intent, "difficulty": difficulty, "target_difficulty": targetDifficulty, "realized_difficulty": realizedDifficulty, "difficulty_delta": difficultyDelta, "difficulty_validation_status": validationStatus, "selection_reason": reason, "is_review": review == 1, "is_probe": probe == 1, "generated_by": generated, "difficulty_mode": difficultyMode, "fixed_difficulty": fixedDifficulty, "training_focus": trainingFocus, "target_pattern_present": targetPatternPresent == 1})
		}
		jsonResp(w, 200, out)
	})
	mux.HandleFunc("/api/reviews", func(w http.ResponseWriter, r *http.Request) {
		rows, err := s.db.Query(`SELECT r.id,r.pattern_id,p.pattern,r.due_at,r.priority,r.reason FROM review_schedule r JOIN sentence_patterns p ON p.id=r.pattern_id ORDER BY r.due_at LIMIT 50`)
		if err != nil {
			jsonResp(w, 500, map[string]string{"error": err.Error()})
			return
		}
		defer rows.Close()
		out := []map[string]any{}
		for rows.Next() {
			var idv, pid, pat, due, reason string
			var priority float64
			_ = rows.Scan(&idv, &pid, &pat, &due, &priority, &reason)
			out = append(out, map[string]any{"id": idv, "pattern_id": pid, "pattern": pat, "due_at": due, "priority": priority, "reason": reason})
		}
		jsonResp(w, 200, out)
	})
	mux.HandleFunc("/api/progress", func(w http.ResponseWriter, r *http.Request) { jsonResp(w, 200, s.progress()) })
	mux.HandleFunc("/api/progress/difficulty/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			jsonResp(w, 405, nil)
			return
		}
		value := strings.Trim(strings.TrimPrefix(r.URL.Path, "/api/progress/difficulty/"), "/")
		level, err := strconv.ParseFloat(value, 64)
		if err != nil || level < 1 || level > 8 {
			jsonResp(w, 400, map[string]string{"error": "difficulty level must be between 1 and 8"})
			return
		}
		jsonResp(w, 200, s.fixedDifficultyMastery(level))
	})
	mux.HandleFunc("/api/learner-state", func(w http.ResponseWriter, r *http.Request) {
		rows, err := s.db.Query(`SELECT skill_id,pattern_id,mastery,acquisition,retention,transfer,attempt_count,success_count,failure_count,evidence_count,state_confidence,state,current_difficulty,max_success_difficulty,consecutive_success,consecutive_failure,next_review_at,context_diversity,memory_strength,stability,updated_at FROM learner_skill_state WHERE user_id='default' ORDER BY mastery`)
		if err != nil {
			jsonResp(w, 500, map[string]string{"error": err.Error()})
			return
		}
		defer rows.Close()
		out := []map[string]any{}
		for rows.Next() {
			var skill, pattern, updated string
			var next sql.NullString
			var mastery, acq, ret, tr, confidence, cur, maxd, div, mem, stab float64
			var attempts, success, failure, evidence, css, cf int
			var state string
			if rows.Scan(&skill, &pattern, &mastery, &acq, &ret, &tr, &attempts, &success, &failure, &evidence, &confidence, &state, &cur, &maxd, &css, &cf, &next, &div, &mem, &stab, &updated) != nil {
				continue
			}
			out = append(out, map[string]any{"skill_id": skill, "pattern_id": pattern, "mastery": mastery, "acquisition": acq, "retention": ret, "transfer": tr, "attempt_count": attempts, "success_count": success, "failure_count": failure, "evidence_count": evidence, "state_confidence": confidence, "state": stateFromRow(attempts, success, mastery, state, s.adaptiveConfig()), "current_difficulty": cur, "max_success_difficulty": maxd, "consecutive_success": css, "consecutive_failure": cf, "next_review_at": next.String, "context_diversity": div, "memory_strength": mem, "stability": stab, "updated_at": updated})
		}
		jsonResp(w, 200, out)
	})
	mux.HandleFunc("/api/skill-graph", func(w http.ResponseWriter, r *http.Request) {
		cfg := s.adaptiveConfig()
		rows, err := s.db.Query(`SELECT s.id,s.name,s.description,s.level,COALESCE((SELECT AVG(mastery) FROM learner_skill_state l JOIN pattern_skills ps ON ps.pattern_id=l.pattern_id WHERE ps.skill_id=s.id AND l.user_id='default'),.25) FROM skills s ORDER BY s.level,s.id`)
		if err != nil {
			jsonResp(w, 500, map[string]string{"error": err.Error()})
			return
		}
		defer rows.Close()
		out := []map[string]any{}
		for rows.Next() {
			var id, name, desc string
			var level int
			var mastery float64
			if rows.Scan(&id, &name, &desc, &level, &mastery) != nil {
				continue
			}
			out = append(out, map[string]any{"id": id, "name": name, "description": desc, "level": level, "mastery": mastery, "eligible": mastery >= cfg.MasteryThreshold || level <= 1})
		}
		jsonResp(w, 200, out)
	})
	mux.HandleFunc("/api/curriculum", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			jsonResp(w, 405, nil)
			return
		}
		guides := make([]CurriculumLevelGuide, 0, 8)
		for level := 1; level <= 8; level++ {
			guides = append(guides, curriculumLevelGuide(level))
		}
		patterns := make([]map[string]any, 0, len(curriculumPatternMap()))
		for _, pattern := range curriculumPatternsSorted() {
			patterns = append(patterns, curriculumPatternJSON(pattern))
		}
		skills := make([]CurriculumSkill, 0, len(curriculumSkillCatalog()))
		for _, skill := range curriculumSkillCatalog() {
			skills = append(skills, skill)
		}
		sort.Slice(skills, func(i, j int) bool {
			if skills[i].AppLevel == skills[j].AppLevel {
				return skills[i].ID < skills[j].ID
			}
			return skills[i].AppLevel < skills[j].AppLevel
		})
		jsonResp(w, 200, map[string]any{"curriculum_version": curriculumVersion, "internal_scale": "D1-D8 productive difficulty", "cefr_note": "CEFR anchors are alignment hypotheses, not official equivalences or certification.", "levels": guides, "skills": skills, "patterns": patterns, "graph": curriculumGraphDiagnostics()})
	})
	mux.HandleFunc("/api/curriculum/patterns", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			jsonResp(w, 405, nil)
			return
		}
		out := make([]map[string]any, 0, len(curriculumPatternMap()))
		for _, pattern := range curriculumPatternsSorted() {
			out = append(out, curriculumPatternJSON(pattern))
		}
		jsonResp(w, 200, map[string]any{"curriculum_version": curriculumVersion, "patterns": out})
	})
	mux.HandleFunc("/api/calibration/catalog", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			jsonResp(w, 405, nil)
			return
		}
		report, err := s.curriculumCalibrationReport()
		if err != nil {
			jsonResp(w, 500, map[string]string{"error": err.Error()})
			return
		}
		jsonResp(w, 200, report)
	})
	mux.HandleFunc("/api/calibration/report", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			jsonResp(w, 405, nil)
			return
		}
		report, err := s.realUseCalibrationReport(r.URL.Query().Get("window"), r.URL.Query().Get("session_id"))
		if err != nil {
			jsonResp(w, 500, map[string]string{"error": err.Error()})
			return
		}
		report["scene_coverage"] = s.sceneCalibrationReport()
		if scenes, sceneErr := s.sceneDiagnostics(); sceneErr == nil {
			report["scene_mastery_distribution"] = scenes
		}
		jsonResp(w, 200, report)
	})
	mux.HandleFunc("/api/calibration/scenes", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			jsonResp(w, 405, nil)
			return
		}
		out, err := s.sceneDiagnostics()
		if err != nil {
			jsonResp(w, 500, map[string]string{"error": err.Error()})
			return
		}
		jsonResp(w, 200, out)
	})
	mux.HandleFunc("/api/calibration/difficulty-trace", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			jsonResp(w, 405, nil)
			return
		}
		limit := 50
		if r.URL.Query().Get("limit") == "100" {
			limit = 100
		}
		trace, err := s.difficultyTrace(r.URL.Query().Get("window"), r.URL.Query().Get("session_id"), limit)
		if err != nil {
			jsonResp(w, 500, map[string]string{"error": err.Error()})
			return
		}
		jsonResp(w, 200, trace)
	})
	mux.HandleFunc("/api/calibration/patterns", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			jsonResp(w, 405, nil)
			return
		}
		patterns, err := s.calibrationPatternDiagnostics(r.URL.Query().Get("window"))
		if err != nil {
			jsonResp(w, 500, map[string]string{"error": err.Error()})
			return
		}
		jsonResp(w, 200, patterns)
	})
	mux.HandleFunc("/api/calibration/sessions/", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			jsonResp(w, 405, nil)
			return
		}
		sessionID := strings.TrimPrefix(r.URL.Path, "/api/calibration/sessions/")
		if sessionID == "" {
			jsonResp(w, 400, map[string]string{"error": "session id is required"})
			return
		}
		report, err := s.calibrationSessionReport(sessionID)
		if err != nil {
			status := 500
			if errors.Is(err, sql.ErrNoRows) {
				status = 404
			}
			jsonResp(w, status, map[string]string{"error": err.Error()})
			return
		}
		jsonResp(w, 200, report)
	})
	mux.HandleFunc("/api/calibration/snapshot", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			jsonResp(w, 405, nil)
			return
		}
		snapshot, err := s.calibrationSnapshot(r.URL.Query().Get("window"), r.URL.Query().Get("session_id"), r.URL.Query().Get("debug") == "true")
		if err != nil {
			jsonResp(w, 500, map[string]string{"error": err.Error()})
			return
		}
		jsonResp(w, 200, snapshot)
	})
	mux.HandleFunc("/api/calibration/replay", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			jsonResp(w, 405, nil)
			return
		}
		replay, err := s.calibrationReplay(r.URL.Query().Get("window"), r.URL.Query().Get("candidate"))
		if err != nil {
			jsonResp(w, 500, map[string]string{"error": err.Error()})
			return
		}
		jsonResp(w, 200, replay)
	})
	mux.HandleFunc("/api/feedback", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			jsonResp(w, 405, nil)
			return
		}
		var req FeedbackRequest
		if err := decode(r, &req); err != nil {
			jsonResp(w, 400, map[string]string{"error": "invalid request"})
			return
		}
		if req.ID == "" {
			req.ID = id("feedback")
		}
		if err := s.recordFeedback(req); err != nil {
			jsonResp(w, 400, map[string]string{"error": err.Error()})
			return
		}
		jsonResp(w, 201, map[string]any{"ok": true, "feedback_id": req.ID})
	})
	mux.HandleFunc("/api/adaptive-config", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			jsonResp(w, 200, s.adaptiveConfig())
			return
		}
		if r.Method != http.MethodPut && r.Method != http.MethodPost {
			jsonResp(w, 405, nil)
			return
		}
		var vals map[string]any
		if decode(r, &vals) != nil {
			jsonResp(w, 400, map[string]string{"error": "invalid config"})
			return
		}
		for k, v := range vals {
			if k == "recent_pattern_window" || k == "max_pattern_repeats" {
				if _, ok := v.(float64); !ok {
					continue
				}
			}
			b, _ := json.Marshal(v)
			if _, err := s.db.Exec(`INSERT INTO adaptive_config(key,value) VALUES(?,?) ON CONFLICT(key) DO UPDATE SET value=excluded.value`, k, string(b)); err != nil {
				jsonResp(w, 500, map[string]string{"error": err.Error()})
				return
			}
		}
		jsonResp(w, 200, s.adaptiveConfig())
	})
	mux.HandleFunc("/api/rebuild-learning-state", func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			jsonResp(w, 405, nil)
			return
		}
		if err := s.rebuildLearnerState(); err != nil {
			jsonResp(w, 500, map[string]string{"error": err.Error()})
			return
		}
		jsonResp(w, 200, map[string]any{"ok": true, "rebuilt": true})
	})
	mux.HandleFunc("/api/providers", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			jsonResp(w, 200, s.providers())
			return
		}
		var c ProviderConfig
		if err := decode(r, &c); err != nil {
			jsonResp(w, 400, map[string]string{"error": "invalid request"})
			return
		}
		if c.ID == "" {
			c.ID = id("provider")
		}
		if c.Timeout == 0 {
			c.Timeout = 45
		}
		if c.MaxTokens == 0 {
			c.MaxTokens = 800
		}
		if c.Temperature == 0 {
			c.Temperature = .2
		}
		// The API never returns a stored key. An empty key on an update means
		// "keep the existing secret", so preserve it in the runtime registry too.
		if c.APIKey == "" {
			s.llm.mu.RLock()
			if existing, ok := s.llm.configs[c.ID]; ok {
				c.APIKey = existing.APIKey
			}
			s.llm.mu.RUnlock()
		}
		_, err := s.db.Exec(`INSERT INTO llm_providers(id,name,type,base_url,api_key,model,timeout,temperature,max_tokens,enabled,supports_reasoning_control,supports_json_mode,exposes_reasoning_content) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?) ON CONFLICT(id) DO UPDATE SET name=excluded.name,type=excluded.type,base_url=excluded.base_url,api_key=CASE WHEN excluded.api_key='' THEN llm_providers.api_key ELSE excluded.api_key END,model=excluded.model,timeout=excluded.timeout,temperature=excluded.temperature,max_tokens=excluded.max_tokens,enabled=excluded.enabled,supports_reasoning_control=excluded.supports_reasoning_control,supports_json_mode=excluded.supports_json_mode,exposes_reasoning_content=excluded.exposes_reasoning_content`, c.ID, c.Name, c.Type, c.BaseURL, c.APIKey, c.Model, c.Timeout, c.Temperature, c.MaxTokens, boolInt(c.Enabled), boolInt(c.SupportsReasoningControl), boolInt(c.SupportsJSONMode), boolInt(c.ExposesReasoningContent))
		if err != nil {
			jsonResp(w, 500, map[string]string{"error": err.Error()})
			return
		}
		s.llm.mu.Lock()
		s.llm.configs[c.ID] = c
		s.llm.mu.Unlock()
		response := c
		response.APIKey = ""
		jsonResp(w, 200, response)
	})
	mux.HandleFunc("/api/providers/test", func(w http.ResponseWriter, r *http.Request) {
		var c ProviderConfig
		if err := decode(r, &c); err != nil {
			jsonResp(w, 400, map[string]string{"error": "invalid request"})
			return
		}
		if c.APIKey == "" && c.ID != "" {
			s.llm.mu.RLock()
			if existing, ok := s.llm.configs[c.ID]; ok {
				c.APIKey = existing.APIKey
			}
			s.llm.mu.RUnlock()
		}
		start := time.Now()
		_, err := s.llm.Client(c).Chat(r.Context(), ChatRequest{Messages: []ChatMessage{{Role: "system", Content: "Connection check. Reply briefly if possible; no reasoning is needed."}, {Role: "user", Content: "Reply with OK"}}, MaxTokens: 128, AllowEmpty: true})
		if err != nil {
			jsonResp(w, 200, map[string]any{"ok": false, "error": err.Error(), "latency_ms": time.Since(start).Milliseconds()})
			return
		}
		jsonResp(w, 200, map[string]any{"ok": true, "latency_ms": time.Since(start).Milliseconds()})
	})
	mux.HandleFunc("/api/task-configs", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			rows, err := s.db.Query("SELECT task_type,provider_id,model,temperature,max_tokens,reasoning_mode,reasoning_effort FROM llm_task_configs ORDER BY task_type")
			if err != nil {
				jsonResp(w, 500, map[string]string{"error": err.Error()})
				return
			}
			defer rows.Close()
			out := []map[string]any{}
			for rows.Next() {
				var task, provider, model, reasoningMode, reasoningEffort string
				var temp float64
				var max int
				_ = rows.Scan(&task, &provider, &model, &temp, &max, &reasoningMode, &reasoningEffort)
				out = append(out, map[string]any{"task_type": task, "provider_id": provider, "model": model, "temperature": temp, "max_tokens": max, "reasoning_mode": reasoningMode, "reasoning_effort": reasoningEffort})
			}
			jsonResp(w, 200, out)
			return
		}
		var cfg struct {
			TaskType        string  `json:"task_type"`
			ProviderID      string  `json:"provider_id"`
			Model           string  `json:"model"`
			Temperature     float64 `json:"temperature"`
			MaxTokens       int     `json:"max_tokens"`
			ReasoningMode   string  `json:"reasoning_mode"`
			ReasoningEffort string  `json:"reasoning_effort"`
		}
		if err := decode(r, &cfg); err != nil || cfg.TaskType == "" || cfg.ProviderID == "" {
			jsonResp(w, 400, map[string]string{"error": "task_type and provider_id are required"})
			return
		}
		if cfg.ReasoningMode == "" {
			cfg.ReasoningMode = "inherit"
		}
		_, err := s.db.Exec(`INSERT INTO llm_task_configs(task_type,provider_id,model,temperature,max_tokens,reasoning_mode,reasoning_effort) VALUES(?,?,?,?,?,?,?) ON CONFLICT(task_type) DO UPDATE SET provider_id=excluded.provider_id,model=excluded.model,temperature=excluded.temperature,max_tokens=excluded.max_tokens,reasoning_mode=excluded.reasoning_mode,reasoning_effort=excluded.reasoning_effort`, cfg.TaskType, cfg.ProviderID, cfg.Model, cfg.Temperature, cfg.MaxTokens, cfg.ReasoningMode, cfg.ReasoningEffort)
		if err != nil {
			jsonResp(w, 500, map[string]string{"error": err.Error()})
			return
		}
		jsonResp(w, 200, cfg)
	})
	mux.HandleFunc("/favicon.ico", func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(http.StatusNoContent) })
	mux.Handle("/", static)
}

func boolInt(v bool) int {
	if v {
		return 1
	}
	return 0
}
func (s *Server) providers() []ProviderConfig {
	s.llm.mu.RLock()
	defer s.llm.mu.RUnlock()
	out := []ProviderConfig{}
	for _, c := range s.llm.configs {
		c.APIKey = ""
		out = append(out, c)
	}
	return out
}

func chooseScene(s string) string {
	if s != "" {
		return s
	}
	return "daily"
}

type Eval struct {
	Verdict            string           `json:"verdict"`
	MeaningScore       float64          `json:"meaning_score"`
	GrammarScore       float64          `json:"grammar_score"`
	NaturalnessScore   float64          `json:"naturalness_score"`
	PatternScore       float64          `json:"pattern_score"`
	TargetPatternMatch string           `json:"target_pattern_match"`
	TargetPatternScore float64          `json:"target_pattern_score"`
	Errors             []map[string]any `json:"errors"`
	SuggestedAnswer    string           `json:"suggested_answer"`
	MoreNatural        string           `json:"more_natural,omitempty"`
	MoreNaturalNeeded  bool             `json:"more_natural_needed,omitempty"`
	Alternative        string           `json:"alternative,omitempty"`
	ExplanationZH      string           `json:"explanation_zh"`
}

type EvaluationSpec struct {
	TargetPatternMode string // required or none
	TargetPattern     string
	Intent            string
}

const (
	TargetPatternExact              = "exact"
	TargetPatternSemanticEquivalent = "semantic_equivalent"
	TargetPatternPartial            = "partial"
	TargetPatternNotMatched         = "not_matched"
	TargetPatternUnknown            = "unknown"
	TargetPatternNotApplicable      = TargetPatternNA
)

type EvaluationDiagnostics struct {
	RequestID        string `json:"request_id"`
	Provider         string `json:"provider"`
	ProviderType     string `json:"provider_type"`
	Model            string `json:"model"`
	HTTPStatus       int    `json:"http_status,omitempty"`
	LatencyMS        int64  `json:"latency_ms,omitempty"`
	FailureStage     string `json:"failure_stage,omitempty"`
	ErrorCategory    string `json:"error_category,omitempty"`
	SchemaError      string `json:"schema_error,omitempty"`
	ResponseShape    string `json:"response_shape,omitempty"`
	ContentSource    string `json:"content_source,omitempty"`
	FinishReason     string `json:"finish_reason,omitempty"`
	ReasoningPresent bool   `json:"reasoning_present,omitempty"`
	ResponseBytes    int    `json:"response_bytes,omitempty"`
	ContentBytes     int    `json:"content_bytes,omitempty"`
	PromptBytes      int    `json:"prompt_bytes,omitempty"`
	RetryCount       int    `json:"retry_count"`
	ProviderCalls    int    `json:"provider_calls"`
	RepairAttempted  bool   `json:"repair_attempted"`
	FreshRetry       bool   `json:"fresh_retry"`
	Success          bool   `json:"success"`
}

var evaluationErrorTypes = map[string]string{
	"meaning": "meaning", "tense": "tense", "article": "article", "preposition": "preposition", "word_order": "word_order", "modal": "modal", "condition": "condition", "agreement": "agreement", "word_choice": "word_choice", "missing_information": "missing_information", "extra_information": "extra_information", "unnatural_expression": "unnatural_expression", "target_pattern_missing": "target_pattern_missing", "register": "register", "other": "other",
}

func normalizeEnum(value string) string {
	v := strings.ToLower(strings.TrimSpace(value))
	v = strings.ReplaceAll(v, "-", "_")
	v = strings.Join(strings.Fields(v), "_")
	return v
}

func normalizeVerdict(value string) (string, error) {
	v := normalizeEnum(value)
	switch v {
	case "correct", "good", "excellent", "great", "perfect", "right", "pass", "passed", "fully_correct", "fullycorrect":
		return "correct", nil
	case "mostly_correct", "mostlycorrect", "partially_correct", "partiallycorrect", "almost_correct", "almostcorrect", "acceptable", "okay", "ok", "fair":
		return "mostly_correct", nil
	case "needs_improvement", "needsimprovement", "needs_work", "needswork", "weak", "poor":
		return "needs_improvement", nil
	case "incorrect", "wrong", "fail", "failed", "bad":
		return "incorrect", nil
	}
	return "", fmt.Errorf("unknown verdict %q", value)
}

func normalizeErrorType(value string) (string, error) {
	v := normalizeEnum(value)
	switch v {
	case "meaning_mismatch", "meaning_error":
		v = "meaning"
	case "task_mismatch":
		v = "meaning"
	case "tense_error", "verb_tense":
		v = "tense"
	case "article_error":
		v = "article"
	case "preposition_error":
		v = "preposition"
	case "word_order_error":
		v = "word_order"
	case "modal_error":
		v = "modal"
	case "condition_error", "conditional_error":
		v = "condition"
	case "agreement_error", "subject_verb_agreement":
		v = "agreement"
	case "word_choice_error":
		v = "word_choice"
	case "spelling", "spelling_error", "vocabulary", "vocabulary_error":
		// Keep provider-specific surface labels in the stable persisted taxonomy.
		v = "word_choice"
	case "grammar", "grammar_error", "punctuation", "punctuation_error", "capitalization", "capitalization_error":
		v = "other"
	case "fluency", "fluency_error", "naturalness_error":
		v = "unnatural_expression"
	case "unnatural", "unnatural_error":
		v = "unnatural_expression"
	case "targetpatternmissing", "target_pattern_error":
		v = "target_pattern_missing"
	}
	if canonical, ok := evaluationErrorTypes[v]; ok {
		return canonical, nil
	}
	return "", fmt.Errorf("unknown error type %q", value)
}

func normalizeSeverity(value string) (string, error) {
	v := normalizeEnum(value)
	switch v {
	case "low":
		v = "minor"
	case "medium", "mid":
		v = "moderate"
	case "high":
		v = "major"
	}
	if v == "minor" || v == "moderate" || v == "major" {
		return v, nil
	}
	return "", fmt.Errorf("unknown severity %q", value)
}

func normalizeTargetPatternMatch(value string) (string, error) {
	v := normalizeEnum(value)
	switch v {
	case TargetPatternExact, TargetPatternSemanticEquivalent, TargetPatternPartial, TargetPatternNotMatched, TargetPatternUnknown, TargetPatternNotApplicable:
		return v, nil
	case "semantic", "equivalent", "semantic_match":
		return TargetPatternSemanticEquivalent, nil
	case "notmatch", "no_match", "mismatch":
		return TargetPatternNotMatched, nil
	}
	return "", fmt.Errorf("unknown target pattern match %q", value)
}

func normalizePatternText(value string) string {
	value = strings.ToLower(strings.TrimSpace(value))
	value = strings.NewReplacer("’", "'", "‘", "'", "“", "\"", "”", "\"", "…", "...", "–", "-", "—", "-").Replace(value)
	value = strings.NewReplacer(".", " ", ",", " ", ";", " ", ":", " ", "!", " ", "?", " ", "(", " ", ")", " ").Replace(value)
	return strings.Join(strings.Fields(value), " ")
}

func hasPatternPhrase(answer, phrase string) bool {
	return strings.Contains(" "+normalizePatternText(answer)+" ", " "+normalizePatternText(phrase)+" ")
}

// classifyTargetPattern implements the small, versioned pattern-family model
// used to reconcile provider judgments. It is deliberately narrower than a
// general semantic parser: it recognizes only documented equivalents and
// otherwise leaves the provider result alone.
func classifyTargetPattern(pattern, targetPattern, answer string) (string, float64, bool) {
	text := normalizePatternText(answer)
	if text == "" {
		return TargetPatternNotMatched, 0, true
	}
	id := strings.ToLower(strings.TrimSpace(pattern))
	target := normalizePatternText(targetPattern)
	if id == "" {
		id = target
	}
	exact := func(phrases ...string) bool {
		for _, phrase := range phrases {
			if hasPatternPhrase(text, phrase) {
				return true
			}
		}
		return false
	}
	semantic := func(phrases ...string) bool {
		for _, phrase := range phrases {
			if hasPatternPhrase(text, phrase) {
				return true
			}
		}
		return false
	}

	switch id {
	case "having-said-that", "having said that":
		if exact("having said that") {
			return TargetPatternExact, 1, true
		}
		if semantic("that said", "even so") {
			return TargetPatternSemanticEquivalent, .9, true
		}
		if semantic("on the other hand", "nevertheless") {
			return TargetPatternPartial, .6, true
		}
		return TargetPatternNotMatched, .15, true
	case "clarification", "what i mean is":
		if exact("what i mean is") {
			return TargetPatternExact, 1, true
		}
		if semantic("what i'm trying to say is", "what i am trying to say is", "what i mean to say is") {
			return TargetPatternSemanticEquivalent, .9, true
		}
		if semantic("in other words", "to clarify") {
			return TargetPatternPartial, .6, true
		}
		return TargetPatternNotMatched, .15, true
	case "professional-suggestion", "i'd like to suggest":
		if exact("i'd like to suggest", "i would like to suggest") {
			return TargetPatternExact, 1, true
		}
		if semantic("i suggest", "i recommend") {
			return TargetPatternSemanticEquivalent, .9, true
		}
		return TargetPatternNotMatched, .15, true
	case "disagreement", "i see your point, but":
		if exact("i see your point but") {
			return TargetPatternExact, 1, true
		}
		if semantic("i understand your point but", "i understand you but", "i get your point but") {
			return TargetPatternSemanticEquivalent, .9, true
		}
		return TargetPatternNotMatched, .15, true
	case "formal-opinion", "i'm not entirely convinced that":
		if exact("i'm not entirely convinced that", "i am not entirely convinced that") {
			return TargetPatternExact, 1, true
		}
		if semantic("i'm not completely convinced that", "i am not completely convinced that", "i'm not fully convinced that", "i'm not totally convinced that", "i am not fully convinced that", "i am not totally convinced that") {
			return TargetPatternSemanticEquivalent, .9, true
		}
		return TargetPatternNotMatched, .15, true
	case "mixed-conditional", "if i had i would":
		if hasPatternPhrase(text, "if i had") && strings.Contains(text, " would ") {
			return TargetPatternExact, 1, true
		}
		if hasPatternPhrase(text, "had i") && strings.Contains(text, " would ") {
			return TargetPatternSemanticEquivalent, .9, true
		}
		return TargetPatternNotMatched, .15, true
	}
	return TargetPatternUnknown, 0, false
}

func removeTargetPatternErrors(errorsList []map[string]any) []map[string]any {
	out := make([]map[string]any, 0, len(errorsList))
	for _, item := range errorsList {
		typ, _ := item["type"].(string)
		if typ != "target_pattern_missing" {
			out = append(out, item)
		}
	}
	return out
}

func reconcileTargetPattern(e *Eval, pattern, targetPattern, answer string) {
	if strings.TrimSpace(pattern) == "" && strings.TrimSpace(targetPattern) == "" {
		e.TargetPatternMatch = TargetPatternNotApplicable
		e.TargetPatternScore = 0
		e.Errors = removeTargetPatternErrors(e.Errors)
		return
	}
	match, score, supported := classifyTargetPattern(pattern, targetPattern, answer)
	if !supported {
		if e.TargetPatternMatch == "" {
			e.TargetPatternMatch = TargetPatternUnknown
		}
		if e.TargetPatternScore <= 0 {
			e.TargetPatternScore = e.PatternScore
		}
		return
	}
	e.TargetPatternMatch, e.TargetPatternScore = match, score
	switch match {
	case TargetPatternExact, TargetPatternSemanticEquivalent:
		e.Errors = removeTargetPatternErrors(e.Errors)
		if e.PatternScore < score {
			e.PatternScore = score
		}
		quality := math.Min(e.MeaningScore, math.Min(e.GrammarScore, e.NaturalnessScore))
		if quality >= .65 {
			e.Verdict = "correct"
		}
	case TargetPatternPartial:
		if e.PatternScore > score {
			e.PatternScore = score
		}
		if e.Verdict == "correct" {
			e.Verdict = "mostly_correct"
		}
	case TargetPatternNotMatched:
		e.PatternScore = math.Min(e.PatternScore, score)
		found := false
		for _, item := range e.Errors {
			if typ, _ := item["type"].(string); typ == "target_pattern_missing" {
				found = true
				break
			}
		}
		if !found {
			e.Errors = append(e.Errors, map[string]any{"type": "target_pattern_missing", "severity": "major", "explanation": "The answer may communicate a related meaning, but it does not demonstrate the target grammatical or discourse function."})
		}
		if e.Verdict == "correct" || e.Verdict == "mostly_correct" {
			e.Verdict = "needs_improvement"
		}
	}
}

func lookupField(fields map[string]json.RawMessage, names ...string) (json.RawMessage, bool) {
	for _, name := range names {
		if value, ok := fields[name]; ok {
			return value, true
		}
	}
	return nil, false
}

func parseRequiredString(fields map[string]json.RawMessage, names ...string) (string, error) {
	raw, ok := lookupField(fields, names...)
	if !ok {
		return "", fmt.Errorf("missing required field %s", names[0])
	}
	var value string
	if err := json.Unmarshal(raw, &value); err != nil || strings.TrimSpace(value) == "" {
		return "", fmt.Errorf("field %s must be a non-empty string", names[0])
	}
	return strings.TrimSpace(value), nil
}

func parseScore(raw json.RawMessage, name string) (float64, error) {
	if string(raw) == "null" {
		return 0, fmt.Errorf("score %s is null", name)
	}
	var number float64
	if err := json.Unmarshal(raw, &number); err == nil {
		return normalizeScore(number, name)
	}
	var text string
	if err := json.Unmarshal(raw, &text); err != nil {
		return 0, fmt.Errorf("score %s must be numeric", name)
	}
	text = strings.TrimSpace(strings.TrimSuffix(text, "%"))
	number, err := strconv.ParseFloat(text, 64)
	if err != nil {
		return 0, fmt.Errorf("score %s is not numeric", name)
	}
	return normalizeScore(number, name)
}

func normalizeScore(number float64, name string) (float64, error) {
	if math.IsNaN(number) || math.IsInf(number, 0) || number < 0 || number > 100 {
		return 0, fmt.Errorf("score %s is out of range", name)
	}
	if number > 1 {
		if number < 2 {
			return 0, fmt.Errorf("score %s is ambiguous", name)
		}
		number /= 100
	}
	return number, nil
}

// stripJSONFence extracts one complete JSON object from a provider response.
// Providers frequently add a short lead-in, a markdown fence, or a trailing
// sentence even when they were asked for JSON mode. We tolerate those wrappers
// but still require exactly one balanced JSON object before schema validation.
func stripJSONFence(raw string) (string, error) {
	text := strings.TrimSpace(raw)
	if text == "" {
		return "", errors.New("empty provider content")
	}
	// A few OpenAI-compatible gateways double-encode assistant content as a
	// JSON string. Decode one such layer, then apply the same strict extraction.
	if strings.HasPrefix(text, "\"") {
		var decoded string
		if err := json.Unmarshal([]byte(text), &decoded); err == nil && strings.TrimSpace(decoded) != text {
			text = strings.TrimSpace(decoded)
		}
	}
	if fenceStart := strings.Index(text, "```"); fenceStart >= 0 {
		fenceEndRel := strings.Index(text[fenceStart+3:], "```")
		if fenceEndRel < 0 {
			return "", errors.New("incomplete markdown JSON fence")
		}
		fenceEnd := fenceStart + 3 + fenceEndRel
		text = strings.TrimSpace(text[fenceStart+3 : fenceEnd])
		// Drop an optional language tag such as ```json. The opening marker was
		// removed above, so the tag is now the first line of the fenced body.
		if newline := strings.IndexByte(text, '\n'); newline >= 0 {
			first := strings.TrimSpace(text[:newline])
			if first == "json" || first == "JSON" {
				text = strings.TrimSpace(text[newline+1:])
			}
		}
	}
	return extractSingleJSONObject(text)
}

func extractSingleJSONObject(text string) (string, error) {
	var candidates []string
	for start := 0; start < len(text); start++ {
		if text[start] != '{' {
			continue
		}
		end, ok := balancedJSONObjectEnd(text, start)
		if !ok {
			continue
		}
		candidate := strings.TrimSpace(text[start : end+1])
		var object map[string]json.RawMessage
		if json.Unmarshal([]byte(candidate), &object) == nil && object != nil {
			candidates = append(candidates, candidate)
			start = end
		}
	}
	if len(candidates) == 0 {
		return "", errors.New("no complete JSON object found")
	}
	if len(candidates) > 1 {
		return "", errors.New("multiple JSON objects found")
	}
	return candidates[0], nil
}

func balancedJSONObjectEnd(text string, start int) (int, bool) {
	depth := 0
	inString := false
	escaped := false
	for i := start; i < len(text); i++ {
		ch := text[i]
		if inString {
			if escaped {
				escaped = false
				continue
			}
			if ch == '\\' {
				escaped = true
				continue
			}
			if ch == '"' {
				inString = false
			}
			continue
		}
		switch ch {
		case '"':
			inString = true
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				return i, true
			}
			if depth < 0 {
				return 0, false
			}
		}
	}
	return 0, false
}

func normalizeEvalContent(raw string) (Eval, error) {
	clean, err := stripJSONFence(raw)
	if err != nil {
		return Eval{}, err
	}
	var fields map[string]json.RawMessage
	if err := json.Unmarshal([]byte(clean), &fields); err != nil {
		return Eval{}, fmt.Errorf("invalid JSON: %w", err)
	}
	// Some providers wrap the requested object in {"evaluation": {...}} or
	// {"result": {...}}. Unwrap only when the outer object has no verdict, so
	// an ambiguous response cannot silently override a valid top-level schema.
	if _, hasVerdict := lookupField(fields, "verdict"); !hasVerdict {
		for _, wrapper := range []string{"evaluation", "result", "assessment"} {
			if nestedRaw, ok := fields[wrapper]; ok {
				var nested map[string]json.RawMessage
				if json.Unmarshal(nestedRaw, &nested) == nil && nested != nil {
					fields = nested
					break
				}
			}
		}
	}
	var out Eval
	verdictRaw, ok := lookupField(fields, "verdict")
	if !ok {
		return out, errors.New("missing required field verdict")
	}
	var verdict string
	if err := json.Unmarshal(verdictRaw, &verdict); err != nil {
		return out, errors.New("verdict must be a string")
	}
	out.Verdict, err = normalizeVerdict(verdict)
	if err != nil {
		return out, err
	}
	if raw, ok := lookupField(fields, "meaning_score", "meaningScore", "meaning"); ok {
		out.MeaningScore, err = parseScore(raw, "meaning_score")
	} else {
		err = errors.New("missing required field meaning_score")
	}
	if err != nil {
		return out, err
	}
	if raw, ok := lookupField(fields, "grammar_score", "grammarScore", "grammar"); ok {
		out.GrammarScore, err = parseScore(raw, "grammar_score")
	} else {
		err = errors.New("missing required field grammar_score")
	}
	if err != nil {
		return out, err
	}
	if raw, ok := lookupField(fields, "naturalness_score", "naturalnessScore", "naturalness"); ok {
		out.NaturalnessScore, err = parseScore(raw, "naturalness_score")
	} else {
		err = errors.New("missing required field naturalness_score")
	}
	if err != nil {
		return out, err
	}
	if raw, ok := lookupField(fields, "pattern_score", "patternScore", "pattern"); ok {
		out.PatternScore, err = parseScore(raw, "pattern_score")
	} else {
		err = errors.New("missing required field pattern_score")
	}
	if err != nil {
		return out, err
	}
	out.TargetPatternMatch = TargetPatternUnknown
	if raw, ok := lookupField(fields, "target_pattern_match", "targetPatternMatch"); ok {
		var value string
		if err := json.Unmarshal(raw, &value); err != nil {
			return out, errors.New("target_pattern_match must be a string")
		}
		out.TargetPatternMatch, err = normalizeTargetPatternMatch(value)
		if err != nil {
			return out, err
		}
	}
	if raw, ok := lookupField(fields, "target_pattern_score", "targetPatternScore"); ok && string(raw) != "null" {
		out.TargetPatternScore, err = parseScore(raw, "target_pattern_score")
		if err != nil {
			return out, err
		}
	}
	out.SuggestedAnswer, err = parseRequiredString(fields, "suggested_answer", "suggestedAnswer")
	if err != nil {
		return out, err
	}
	if raw, ok := lookupField(fields, "more_natural", "moreNatural"); ok && string(raw) != "null" {
		var value string
		if json.Unmarshal(raw, &value) == nil {
			out.MoreNatural = strings.TrimSpace(value)
		}
	}
	if raw, ok := lookupField(fields, "more_natural_needed", "moreNaturalNeeded"); ok {
		_ = json.Unmarshal(raw, &out.MoreNaturalNeeded)
	}
	if out.MoreNatural != "" && !out.MoreNaturalNeeded {
		out.MoreNaturalNeeded = true
	}
	if raw, ok := lookupField(fields, "alternative", "alternative_answer", "alternativeAnswer"); ok && string(raw) != "null" {
		var value string
		if json.Unmarshal(raw, &value) == nil {
			out.Alternative = strings.TrimSpace(value)
		}
	}
	out.ExplanationZH, err = parseRequiredString(fields, "explanation_zh", "explanationZh", "explanation")
	if err != nil {
		return out, err
	}
	errorsRaw, ok := lookupField(fields, "errors")
	if !ok {
		return out, errors.New("missing required field errors")
	}
	if string(errorsRaw) == "null" {
		out.Errors = []map[string]any{}
	} else {
		var items []map[string]json.RawMessage
		if err := json.Unmarshal(errorsRaw, &items); err != nil {
			return out, errors.New("errors must be an array or null")
		}
		out.Errors = make([]map[string]any, 0, len(items))
		for _, item := range items {
			typRaw, ok := lookupField(item, "type", "error_type", "errorType")
			if !ok {
				return out, errors.New("error is missing type")
			}
			var typ string
			if err := json.Unmarshal(typRaw, &typ); err != nil {
				return out, errors.New("error type must be a string")
			}
			canonicalType, err := normalizeErrorType(typ)
			if err != nil {
				return out, err
			}
			sevRaw, ok := lookupField(item, "severity", "level")
			if !ok {
				return out, errors.New("error is missing severity")
			}
			var sev string
			if err := json.Unmarshal(sevRaw, &sev); err != nil {
				return out, errors.New("severity must be a string")
			}
			canonicalSeverity, err := normalizeSeverity(sev)
			if err != nil {
				return out, err
			}
			explanation, err := parseRequiredString(item, "explanation", "detail", "description", "message", "reason")
			if err != nil {
				return out, err
			}
			out.Errors = append(out.Errors, map[string]any{"type": canonicalType, "severity": canonicalSeverity, "explanation": explanation})
		}
	}
	return out, validateEval(&out)
}

func (s *Server) submitAttempt(ctx context.Context, session, exercise, answer string) (map[string]any, error) {
	if session == "" {
		session = id("session")
	}
	var sessionDifficulty float64
	_ = s.db.QueryRow("SELECT global_difficulty FROM user_profile WHERE id='default'").Scan(&sessionDifficulty)
	if state, err := ensureSessionDifficulty(s.db, session, "adaptive"); err == nil && state.Center > 0 {
		sessionDifficulty = state.Center
	}
	var prompt, pattern, scene, intent string
	var difficulty, target, realized float64
	var validationStatus, validationReason, policyVersion string
	if err := s.db.QueryRow("SELECT chinese_prompt,pattern_id,scene_id,intent_id,difficulty,COALESCE(NULLIF(target_difficulty,0),difficulty),COALESCE(NULLIF(realized_difficulty,0),0),COALESCE(difficulty_validation_status,'unknown'),COALESCE(difficulty_validation_reason,''),COALESCE(difficulty_policy_version,'') FROM exercises WHERE id=?", exercise).Scan(&prompt, &pattern, &scene, &intent, &difficulty, &target, &realized, &validationStatus, &validationReason, &policyVersion); err != nil {
		return nil, err
	}
	if target <= 0 {
		target = difficulty
	}
	attempt := id("attempt")
	if _, err := s.db.Exec(`INSERT INTO attempts(id,session_id,exercise_id,user_answer,submitted_at,evaluation_status,evaluation_diagnostics_json,learner_ability,session_center,target_difficulty,realized_difficulty,difficulty_delta,difficulty_validation_status,difficulty_validation_reason,difficulty_policy_version) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?)`, attempt, session, exercise, answer, time.Now().UTC().Format(time.RFC3339), "pending", "{}", learnerAbility(s.db), sessionDifficulty, target, realized, math.Abs(realized-target), validationStatus, validationReason, policyVersion); err != nil {
		return nil, err
	}
	_ = s.populateAttemptMetadata(attempt)
	spec := EvaluationSpec{TargetPatternMode: "required", TargetPattern: pattern, Intent: intent}
	var focus string
	var targetPresent int
	_ = s.db.QueryRow(`SELECT COALESCE(training_focus,'pattern'),COALESCE(target_pattern_present,1) FROM attempts WHERE id=?`, attempt).Scan(&focus, &targetPresent)
	if focus == TrainingFocusFree || targetPresent == 0 || pattern == "" {
		spec.TargetPatternMode = "none"
		spec.TargetPattern = ""
	}
	return s.evaluateAndPersistWithSpec(ctx, attempt, prompt, pattern, scene, difficulty, answer, spec)
}

func failureUserMessage(d EvaluationDiagnostics) string {
	switch d.ErrorCategory {
	case "timeout":
		return "AI evaluation timed out; please try again."
	case "provider_4xx":
		return "The provider rejected the request; check its settings."
	case "provider_5xx", "provider_error":
		return "The provider is temporarily unavailable; please try again."
	case "invalid_provider_envelope":
		return "The provider response format could not be recognized."
	case "invalid_structured_output", "schema_validation":
		return "The AI evaluation format was invalid; please retry."
	default:
		return "AI evaluation failed; please try again."
	}
}

func (s *Server) evaluateAndPersist(ctx context.Context, attempt, prompt, pattern, scene string, difficulty float64, answer string) (map[string]any, error) {
	return s.evaluateAndPersistWithSpec(ctx, attempt, prompt, pattern, scene, difficulty, answer, EvaluationSpec{TargetPatternMode: "required", TargetPattern: pattern})
}

func (s *Server) evaluateAndPersistWithSpec(ctx context.Context, attempt, prompt, pattern, scene string, difficulty float64, answer string, spec EvaluationSpec) (map[string]any, error) {
	eval, provider, model, diagnostics, err := s.evaluateWithProviderOptionsSpec(ctx, prompt, pattern, answer, "", ProviderRequestOptions{}, spec)
	if err != nil {
		diagnostics.Success = false
		diagJSON, _ := json.Marshal(diagnostics)
		_, _ = s.db.Exec("UPDATE attempts SET provider=?,model=?,evaluation_status='failed',evaluation_diagnostics_json=? WHERE id=?", provider, model, string(diagJSON), attempt)
		return map[string]any{"attempt_id": attempt, "evaluation_status": "failed", "error": failureUserMessage(diagnostics), "error_category": diagnostics.ErrorCategory, "diagnostics": diagnostics, "retryable": diagnostics.RetryCount > 0 || diagnostics.ErrorCategory == "invalid_structured_output"}, nil
	}
	diagnostics.Success = true
	return s.saveValidatedAttempt(attempt, prompt, pattern, scene, difficulty, eval, provider, model, diagnostics)
}

func (s *Server) saveValidatedAttempt(attempt, prompt, pattern, scene string, difficulty float64, eval Eval, provider, model string, diagnostics EvaluationDiagnostics) (map[string]any, error) {
	diagJSON, _ := json.Marshal(diagnostics)
	errorsJSON, _ := json.Marshal(eval.Errors)
	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	var status, session string
	if err := tx.QueryRow("SELECT evaluation_status,session_id FROM attempts WHERE id=?", attempt).Scan(&status, &session); err != nil {
		return nil, err
	}
	var existing int
	if err := tx.QueryRow("SELECT COUNT(*) FROM evaluations WHERE attempt_id=?", attempt).Scan(&existing); err != nil {
		return nil, err
	}
	if status == "validated" || existing > 0 {
		tx.Rollback()
		return s.loadAttemptResult(attempt)
	}
	if _, err = tx.Exec(`UPDATE attempts SET provider=?,model=?,prompt_version='v1',evaluation_status='validated',evaluation_diagnostics_json=? WHERE id=?`, provider, model, string(diagJSON), attempt); err != nil {
		return nil, err
	}
	if _, err = tx.Exec(`INSERT INTO evaluations(id,attempt_id,verdict,meaning_score,grammar_score,naturalness_score,pattern_score,target_pattern_match,target_pattern_score,errors_json,suggested_answer,more_natural,more_natural_needed,alternative,explanation_zh,validated,created_at) VALUES(?,?,?,?,?,?,?,?,?,?,?,?,?,?,?,1,?)`, id("evaluation"), attempt, eval.Verdict, eval.MeaningScore, eval.GrammarScore, eval.NaturalnessScore, eval.PatternScore, eval.TargetPatternMatch, eval.TargetPatternScore, string(errorsJSON), eval.SuggestedAnswer, eval.MoreNatural, boolInt(eval.MoreNaturalNeeded), eval.Alternative, eval.ExplanationZH, time.Now().UTC().Format(time.RFC3339)); err != nil {
		return nil, err
	}
	var masteryBefore, difficultyBefore, patternAbilityBefore, targetDifficulty, realizedDifficulty float64
	_ = tx.QueryRow(`SELECT COALESCE(mastery,.25) FROM learner_skill_state WHERE user_id='default' AND pattern_id=?`, pattern).Scan(&masteryBefore)
	_ = tx.QueryRow(`SELECT COALESCE(current_difficulty,0) FROM learner_skill_state WHERE user_id='default' AND pattern_id=?`, pattern).Scan(&patternAbilityBefore)
	_ = tx.QueryRow(`SELECT global_difficulty FROM user_profile WHERE id='default'`).Scan(&difficultyBefore)
	mode, selectionReason, _, isProbe, _, _, _ := adaptiveAttemptMetadata(tx, attempt)
	_ = tx.QueryRow(`SELECT COALESCE(NULLIF(target_difficulty,0),difficulty),COALESCE(NULLIF(realized_difficulty,0),0) FROM exercises WHERE id=(SELECT exercise_id FROM attempts WHERE id=?)`, attempt).Scan(&targetDifficulty, &realizedDifficulty)
	var trainingFocus string
	var targetPatternPresent int
	_ = tx.QueryRow(`SELECT COALESCE(training_focus,'pattern'),COALESCE(target_pattern_present,1) FROM attempts WHERE id=?`, attempt).Scan(&trainingFocus, &targetPatternPresent)
	if pattern != "" && trainingFocus != TrainingFocusFree && targetPatternPresent != 0 {
		if err = updateMasteryTxMode(tx, pattern, scene, difficulty, eval, isProbe); err != nil {
			return nil, err
		}
		if err = updateAdaptiveStateTx(tx, attempt, pattern, scene, difficulty, eval); err != nil {
			return nil, err
		}
	} else if err = updateFreeExpressionStateTx(tx, attempt, scene, difficulty, eval); err != nil {
		return nil, err
	}
	if err = updateProfileTx(tx, eval, difficulty); err != nil {
		return nil, err
	}
	cfg := difficultyConfig(defaultAdaptiveConfig())
	if rows, configErr := tx.Query(`SELECT key,value FROM adaptive_config`); configErr == nil {
		// Keep the transactional controller aligned with runtime-configured limits.
		for rows.Next() {
			var key, value string
			if rows.Scan(&key, &value) != nil {
				continue
			}
			var n float64
			if json.Unmarshal([]byte(value), &n) == nil {
				switch key {
				case "deadband_low":
					cfg.DeadbandLow = n
				case "deadband_high":
					cfg.DeadbandHigh = n
				case "max_session_center_step":
					cfg.MaxSessionCenterStep = n
				case "recent_window_size":
					cfg.RecentWindowSize = int(n)
				case "ewma_alpha":
					cfg.EWMAAlpha = n
				case "session_band_lower":
					cfg.SessionBandLower = n
				case "session_band_upper":
					cfg.SessionBandUpper = n
				}
			}
		}
		rows.Close()
	}
	sessionBefore, _ := loadSessionDifficultyTx(tx, session)
	updatedSession, sessionReason, err := updateSessionCenterTx(tx, session, cfg)
	if err != nil {
		return nil, err
	}
	var masteryAfter, difficultyAfter float64
	_ = tx.QueryRow(`SELECT COALESCE(mastery,.25) FROM learner_skill_state WHERE user_id='default' AND pattern_id=?`, pattern).Scan(&masteryAfter)
	_ = tx.QueryRow(`SELECT global_difficulty FROM user_profile WHERE id='default'`).Scan(&difficultyAfter)
	var position int
	_ = tx.QueryRow(`SELECT COUNT(*) FROM attempts WHERE session_id=? AND evaluation_status='validated'`, session).Scan(&position)
	if err = updateSessionObservationTx(tx, session, pattern, eval, isProbe, masteryBefore, masteryAfter, difficultyBefore, difficultyAfter, position); err != nil {
		return nil, err
	}
	if targetDifficulty <= 0 {
		targetDifficulty = difficulty
	}
	adjustment := sessionReason
	if adjustment == "insufficient_rolling_evidence" {
		adjustment = "session_center_waiting_for_rolling_evidence"
	}
	traceRealized := realizedDifficulty
	traceDelta := 0.0
	if traceRealized > 0 {
		traceDelta = math.Abs(traceRealized - targetDifficulty)
	}
	trace := difficultyPolicyTrace(difficultyBefore, updatedSession, patternAbilityBefore, targetDifficulty, traceRealized, selectionReason, adjustment, isProbe, cfg)
	trace["difficulty_delta"] = traceDelta
	trace["mode"] = mode
	trace["session_center_before"] = sessionBefore.Center
	trace["session_center_after"] = updatedSession.Center
	trace["learner_ability_after"] = difficultyAfter
	trace["target_pattern_match"] = eval.TargetPatternMatch
	trace["target_pattern_score"] = eval.TargetPatternScore
	trace["difficulty_validation_status"] = validationStatusForAttempt(tx, attempt)
	traceJSON, _ := json.Marshal(trace)
	if _, err = tx.Exec(`UPDATE attempts SET mastery_before=?,mastery_after=?,difficulty_before=?,difficulty_after=?,learner_ability=?,session_center=?,pattern_ability=?,target_difficulty=?,realized_difficulty=?,difficulty_delta=?,difficulty_validation_status=?,difficulty_policy_version=?,position=? WHERE id=?`, masteryBefore, masteryAfter, difficultyBefore, difficultyAfter, difficultyAfter, updatedSession.Center, patternAbilityBefore, targetDifficulty, realizedDifficulty, traceDelta, validationStatusForAttempt(tx, attempt), cfg.DifficultyPolicyVersion, position, attempt); err != nil {
		return nil, err
	}
	if _, err = tx.Exec(`UPDATE exercises SET decision_trace_json=?,metadata_json=json_set(metadata_json,'$.decision_trace',json(?),'$.realized_difficulty',?,'$.difficulty_delta',?) WHERE id=(SELECT exercise_id FROM attempts WHERE id=?)`, string(traceJSON), string(traceJSON), realizedDifficulty, math.Abs(realizedDifficulty-targetDifficulty), attempt); err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return map[string]any{"attempt_id": attempt, "evaluation_status": "validated", "evaluation": eval, "session_id": session}, nil
}

func (s *Server) loadAttemptResult(attempt string) (map[string]any, error) {
	var status, session string
	if err := s.db.QueryRow("SELECT evaluation_status,session_id FROM attempts WHERE id=?", attempt).Scan(&status, &session); err != nil {
		return nil, err
	}
	eval, err := s.loadEvaluation(attempt)
	if err != nil {
		return nil, err
	}
	return map[string]any{"attempt_id": attempt, "evaluation_status": status, "evaluation": eval, "session_id": session}, nil
}

func (s *Server) loadEvaluation(attempt string) (Eval, error) {
	var e Eval
	var errorsJSON string
	var moreNaturalNeeded int
	if err := s.db.QueryRow("SELECT verdict,meaning_score,grammar_score,naturalness_score,pattern_score,COALESCE(target_pattern_match,'unknown'),COALESCE(target_pattern_score,0),errors_json,suggested_answer,more_natural,more_natural_needed,alternative,explanation_zh FROM evaluations WHERE attempt_id=?", attempt).Scan(&e.Verdict, &e.MeaningScore, &e.GrammarScore, &e.NaturalnessScore, &e.PatternScore, &e.TargetPatternMatch, &e.TargetPatternScore, &errorsJSON, &e.SuggestedAnswer, &e.MoreNatural, &moreNaturalNeeded, &e.Alternative, &e.ExplanationZH); err != nil {
		return e, err
	}
	e.MoreNaturalNeeded = moreNaturalNeeded == 1
	if err := json.Unmarshal([]byte(errorsJSON), &e.Errors); err != nil {
		return e, err
	}
	if e.Errors == nil {
		e.Errors = []map[string]any{}
	}
	return e, nil
}

func (s *Server) reevaluateAttempt(ctx context.Context, attempt string) (map[string]any, error) {
	var prompt, pattern, scene, answer, status, intent, focus string
	var difficulty float64
	var targetPresent int
	if err := s.db.QueryRow(`SELECT e.chinese_prompt,e.pattern_id,e.scene_id,e.intent_id,e.difficulty,a.user_answer,a.evaluation_status,COALESCE(a.training_focus,'pattern'),COALESCE(a.target_pattern_present,1) FROM attempts a JOIN exercises e ON e.id=a.exercise_id WHERE a.id=?`, attempt).Scan(&prompt, &pattern, &scene, &intent, &difficulty, &answer, &status, &focus, &targetPresent); err != nil {
		return nil, err
	}
	if status == "validated" {
		return s.loadAttemptResult(attempt)
	}
	spec := EvaluationSpec{TargetPatternMode: "required", TargetPattern: pattern, Intent: intent}
	if focus == TrainingFocusFree || targetPresent == 0 || pattern == "" {
		spec.TargetPatternMode, spec.TargetPattern = "none", ""
	}
	return s.evaluateAndPersistWithSpec(ctx, attempt, prompt, pattern, scene, difficulty, answer, spec)
}

func (s *Server) evaluate(ctx context.Context, prompt, pattern, answer string) (Eval, string, string, EvaluationDiagnostics, error) {
	return s.evaluateWithProviderOptions(ctx, prompt, pattern, answer, "", ProviderRequestOptions{})
}

func (s *Server) evaluateWithProvider(ctx context.Context, prompt, pattern, answer, providerID string) (Eval, string, string, EvaluationDiagnostics, error) {
	return s.evaluateWithProviderOptions(ctx, prompt, pattern, answer, providerID, ProviderRequestOptions{})
}

func (s *Server) evaluateWithProviderOptions(ctx context.Context, prompt, pattern, answer, providerID string, options ProviderRequestOptions) (Eval, string, string, EvaluationDiagnostics, error) {
	return s.evaluateWithProviderOptionsSpec(ctx, prompt, pattern, answer, providerID, options, EvaluationSpec{TargetPatternMode: "required", TargetPattern: pattern})
}

func (s *Server) evaluateWithProviderOptionsSpec(ctx context.Context, prompt, pattern, answer, providerID string, options ProviderRequestOptions, spec EvaluationSpec) (Eval, string, string, EvaluationDiagnostics, error) {
	if spec.TargetPatternMode == "" {
		spec.TargetPatternMode = "required"
	}
	s.llm.mu.RLock()
	var c ProviderConfig
	for _, x := range s.llm.configs {
		if x.Enabled && (providerID == "" || x.ID == providerID || strings.EqualFold(x.Name, providerID)) {
			c = x
			break
		}
	}
	s.llm.mu.RUnlock()
	diagnostics := EvaluationDiagnostics{RequestID: id("evaluation_request"), Provider: c.ID, ProviderType: c.Type, Model: c.Model}
	if c.ID == "" {
		diagnostics.Provider = "local"
		diagnostics.ProviderType = "local"
		diagnostics.Model = "heuristic"
		diagnostics.Success = true
		e := heuristicEval(prompt, pattern, answer)
		if spec.TargetPatternMode == "none" {
			reconcileTargetPattern(&e, "", "", answer)
		} else {
			reconcileTargetPattern(&e, pattern, pattern, answer)
		}
		return e, "local", "heuristic", diagnostics, nil
	}
	targetPattern := pattern
	if spec.TargetPattern != "" {
		targetPattern = spec.TargetPattern
	}
	if s.db != nil {
		var label string
		if s.db.QueryRow("SELECT pattern FROM sentence_patterns WHERE id=?", pattern).Scan(&label) == nil && strings.TrimSpace(label) != "" {
			targetPattern = label
		}
	}
	systemPrompt := "Evaluate an English learner answer. You may reason internally, but after reasoning always return the final evaluation as one complete JSON object in the assistant's final content. Never leave final content empty and never return reasoning as the evaluation. Do not include prose or markdown outside JSON. The object must have verdict, meaning_score, grammar_score, naturalness_score, pattern_score, target_pattern_match, target_pattern_score, errors, suggested_answer, and explanation_zh. verdict must be exactly one of: correct, mostly_correct, needs_improvement, incorrect. Scores must be numbers from 0 to 1. Use errors:[] when there are no errors. Every errors item must contain type, severity, and explanation. Do not rewrite an already correct and natural answer merely to produce a different sentence."
	userPrompt := fmt.Sprintf("Original evaluation context\nPrompt: %s\nLearner answer: %s", prompt, answer)
	if spec.TargetPatternMode == "none" {
		systemPrompt += " There is no required target sentence pattern. Evaluate meaning accuracy, communication intent fulfillment, grammar, naturalness, and appropriateness. Do not penalize a valid answer for choosing a different structure. Set target_pattern_match to not_applicable and target_pattern_score to 0. Do not add target_pattern_missing errors."
		userPrompt = fmt.Sprintf("Original evaluation context\nPrompt: %s\nCommunication intent: %s\nLearner answer: %s", prompt, spec.Intent, answer)
	} else {
		systemPrompt += " Evaluate the target sentence pattern after meaning, intent, grammar, and naturalness. Do not require literal use of the target phrase; accept genuine semantic equivalents. target_pattern_match must be one of exact, semantic_equivalent, partial, not_matched. The suggested_answer should demonstrate the target pattern."
		userPrompt = fmt.Sprintf("Original evaluation context\nPrompt: %s\nTarget pattern expression: %s\nTarget pattern ID: %s\nLearner answer: %s", prompt, targetPattern, pattern, answer)
	}
	baseMessages := []ChatMessage{{Role: "system", Content: systemPrompt}, {Role: "user", Content: userPrompt}}
	jsonMode := true
	const maxEvaluationAttempts = 3
	lastInvalidResponse := ""
	for attempt := 0; attempt < maxEvaluationAttempts; attempt++ {
		messages := baseMessages
		if attempt == 1 && lastInvalidResponse != "" {
			diagnostics.RepairAttempted = true
			messages = append(append([]ChatMessage{}, baseMessages...), ChatMessage{Role: "system", Content: "Repair instruction: Return only one valid JSON object matching the required schema. No markdown and no explanation outside JSON. Do not omit any required field. Every errors item must include type, severity, and explanation; use errors:[] if there are no errors."})
			invalid := lastInvalidResponse
			if len(invalid) > 4000 {
				invalid = invalid[:4000]
			}
			messages = append(messages, ChatMessage{Role: "user", Content: "Previous response violated the output contract. Do not preserve its verdict or scores. Repair only the format/schema. Invalid response:\n" + invalid})
		} else if attempt > 0 {
			diagnostics.FreshRetry = true
			messages = append(append([]ChatMessage{}, baseMessages...), ChatMessage{Role: "system", Content: "Fresh retry: the previous provider call failed the output contract. Re-evaluate the original exercise and learner answer independently. Do not assume the previous verdict was correct. Return exactly one complete JSON object."})
		}
		if !jsonMode {
			messages = append(messages, ChatMessage{Role: "system", Content: "This provider does not support native JSON response mode. Return only the JSON object in the assistant content."})
		}
		diagnostics.PromptBytes = chatPromptBytes(messages)
		maxTokens := c.MaxTokens
		if options.MaxTokens > 0 {
			maxTokens = options.MaxTokens
		}
		if maxTokens < 1200 {
			maxTokens = 1200
		}
		if attempt > 0 {
			maxTokens *= 2
		}
		if attempt > 1 {
			maxTokens *= 2
		}
		temperature := c.Temperature
		if options.Temperature != nil {
			temperature = *options.Temperature
		}
		resp, err := s.llm.Client(c).Chat(ctx, ChatRequest{Messages: messages, Temperature: temperature, MaxTokens: maxTokens, JSONMode: jsonMode, ReasoningMode: options.ReasoningMode, ReasoningEffort: options.ReasoningEffort, RequestID: diagnostics.RequestID})
		diagnostics.ProviderCalls++
		diagnostics.RetryCount = attempt
		if err != nil {
			var pe *ProviderError
			if errors.As(err, &pe) {
				diagnostics.FailureStage = pe.Stage
				diagnostics.ErrorCategory = pe.Category
				diagnostics.HTTPStatus = pe.HTTPStatus
				diagnostics.LatencyMS = pe.Latency.Milliseconds()
				diagnostics.ResponseShape = pe.ResponseShape
				fallbackJSONMode := attempt == 0 && (pe.Category == "invalid_provider_envelope" || (jsonMode && (pe.HTTPStatus == http.StatusBadRequest || pe.HTTPStatus == http.StatusUnprocessableEntity)))
				if fallbackJSONMode {
					jsonMode = false
				}
				if (fallbackJSONMode || retryableEvaluatorProviderCategory(pe.Category)) && attempt+1 < maxEvaluationAttempts {
					continue
				}
				return Eval{}, c.ID, c.Model, diagnostics, err
			} else {
				diagnostics.FailureStage = "provider_http"
				diagnostics.ErrorCategory = "provider_error"
				if attempt+1 < maxEvaluationAttempts {
					continue
				}
			}
			return Eval{}, c.ID, c.Model, diagnostics, err
		}
		diagnostics.HTTPStatus = resp.HTTPStatus
		diagnostics.LatencyMS = resp.Latency.Milliseconds()
		diagnostics.ResponseShape = resp.ResponseShape
		diagnostics.ContentSource = resp.ContentSource
		diagnostics.FinishReason = resp.FinishReason
		diagnostics.ReasoningPresent = resp.ReasoningPresent
		diagnostics.ResponseBytes = resp.ResponseBytes
		diagnostics.ContentBytes = len(resp.Content)
		ev, parseErr := normalizeEvalContent(resp.Content)
		if parseErr == nil {
			if spec.TargetPatternMode == "none" {
				reconcileTargetPattern(&ev, "", "", answer)
			} else {
				reconcileTargetPattern(&ev, pattern, targetPattern, answer)
			}
			diagnostics.Success = true
			return ev, c.ID, c.Model, diagnostics, nil
		}
		diagnostics.FailureStage = "schema_validation"
		diagnostics.ErrorCategory = "invalid_structured_output"
		diagnostics.SchemaError = parseErr.Error()
		lastInvalidResponse = resp.Content
		if attempt+1 >= maxEvaluationAttempts {
			return Eval{}, c.ID, c.Model, diagnostics, parseErr
		}
	}
	return Eval{}, c.ID, c.Model, diagnostics, errors.New("evaluation failed")
}

func validateEval(e *Eval) error {
	if e.Verdict != "correct" && e.Verdict != "mostly_correct" && e.Verdict != "needs_improvement" && e.Verdict != "incorrect" {
		return errors.New("invalid verdict")
	}
	if e.TargetPatternMatch == "" {
		e.TargetPatternMatch = TargetPatternUnknown
	}
	if _, err := normalizeTargetPatternMatch(e.TargetPatternMatch); err != nil {
		return err
	}
	for _, v := range []float64{e.MeaningScore, e.GrammarScore, e.NaturalnessScore, e.PatternScore} {
		if v < 0 || v > 1 {
			return errors.New("score out of range")
		}
	}
	if e.TargetPatternScore < 0 || e.TargetPatternScore > 1 {
		return errors.New("target pattern score out of range")
	}
	if strings.TrimSpace(e.SuggestedAnswer) == "" || strings.TrimSpace(e.ExplanationZH) == "" {
		return errors.New("missing required evaluation fields")
	}
	for _, item := range e.Errors {
		typ, _ := item["type"].(string)
		sev, _ := item["severity"].(string)
		if typ == "" || (sev != "minor" && sev != "moderate" && sev != "major") {
			return errors.New("invalid error taxonomy")
		}
	}
	return nil
}
func heuristicEval(prompt, pattern, answer string) Eval {
	a := strings.TrimSpace(answer)
	lower := strings.ToLower(a)
	e := Eval{Verdict: "needs_improvement", MeaningScore: .45, GrammarScore: .45, NaturalnessScore: .45, PatternScore: .4, Errors: []map[string]any{}, SuggestedAnswer: a, ExplanationZH: "Keep practicing this sentence pattern."}
	switch pattern {
	case "going-to", "was/were going to":
		e.SuggestedAnswer = "I was going to call you yesterday."
		if strings.Contains(lower, "going to") || strings.Contains(lower, "wanted to") {
			e.Verdict = "mostly_correct"
			e.MeaningScore = .82
			e.GrammarScore = .75
			e.NaturalnessScore = .72
			e.PatternScore = .65
		} else {
			e.Errors = append(e.Errors, map[string]any{"type": "target_pattern_missing", "severity": "moderate", "explanation": "The answer does not clearly express the target pattern."})
		}
	case "modal-possibility", "might / may":
		e.SuggestedAnswer = "I might be a little late today."
		if strings.Contains(lower, "might") || strings.Contains(lower, "may") {
			e.Verdict = "correct"
			e.MeaningScore = .95
			e.GrammarScore = .9
			e.NaturalnessScore = .86
			e.PatternScore = .9
		} else {
			e.Errors = append(e.Errors, map[string]any{"type": "modal", "severity": "major", "explanation": "Use might or may to express possibility."})
		}
	default:
		if len(a) > 8 {
			e.Verdict = "mostly_correct"
			e.MeaningScore = .75
			e.GrammarScore = .7
			e.NaturalnessScore = .7
			e.PatternScore = .6
		} else {
			e.Errors = append(e.Errors, map[string]any{"type": "missing_information", "severity": "major", "explanation": "The answer does not contain enough information."})
		}
	}
	return e
}

func updateMasteryTx(tx *sql.Tx, pattern, scene string, difficulty float64, e Eval) error {
	return updateMasteryTxMode(tx, pattern, scene, difficulty, e, false)
}

func updateMasteryTxMode(tx *sql.Tx, pattern, scene string, difficulty float64, e Eval, probe bool) error {
	var attempts, correct, consec int
	var recent, long, mastery float64
	_ = tx.QueryRow("SELECT attempts,correct,recent_accuracy,long_term_accuracy,consecutive_correct,mastery FROM pattern_mastery WHERE pattern_id=?", pattern).Scan(&attempts, &correct, &recent, &long, &consec, &mastery)
	attempts++
	ok := e.Verdict == "correct" || e.Verdict == "mostly_correct"
	if ok {
		correct++
		consec++
	} else {
		consec = 0
	}
	acc := float64(correct) / float64(attempts)
	recent = .65*recent + .35*e.PatternScore
	if attempts == 1 {
		recent = e.PatternScore
	}
	long = .85*long + .15*acc
	if attempts == 1 {
		long = acc
	}
	severity := 0.0
	for _, er := range e.Errors {
		if v, ok := er["severity"].(string); ok {
			if v == "major" {
				severity += .2
			} else if v == "moderate" {
				severity += .1
			} else {
				severity += .03
			}
		}
	}
	if probe && !ok {
		severity *= .25
	}
	mastery = clamp(.35*recent+.35*long+.15*math.Min(1, float64(consec)/5)+.15*e.PatternScore-severity, 0, 1)
	now := time.Now().UTC().Format(time.RFC3339)
	if _, err := tx.Exec(`INSERT INTO pattern_mastery(pattern_id,attempts,correct,recent_accuracy,long_term_accuracy,consecutive_correct,last_practiced,mastery) VALUES(?,?,?,?,?,?,?,?) ON CONFLICT(pattern_id) DO UPDATE SET attempts=excluded.attempts,correct=excluded.correct,recent_accuracy=excluded.recent_accuracy,long_term_accuracy=excluded.long_term_accuracy,consecutive_correct=excluded.consecutive_correct,last_practiced=excluded.last_practiced,mastery=excluded.mastery`, pattern, attempts, correct, recent, long, consec, now, mastery); err != nil {
		return err
	}
	// Scene mastery is maintained by the adaptive scene updater below. Keeping
	// this legacy pattern updater scene-neutral avoids counting one attempt twice.
	for _, er := range e.Errors {
		typ, _ := er["type"].(string)
		sev, _ := er["severity"].(string)
		weight := 1.0
		if sev == "major" {
			weight = 3
		} else if sev == "moderate" {
			weight = 2
		}
		_, _ = tx.Exec(`INSERT INTO error_stats(error_type,count,severity_sum,last_seen) VALUES(?,?,?,?) ON CONFLICT(error_type) DO UPDATE SET count=count+excluded.count,severity_sum=severity_sum+excluded.severity_sum,last_seen=excluded.last_seen`, typ, 1, weight, now)
	}
	due := time.Now().UTC().Add(reviewInterval(mastery, ok)).Format(time.RFC3339)
	_, err := tx.Exec(`INSERT INTO review_schedule(id,pattern_id,due_at,priority,reason,last_practiced) VALUES(?,?,?,?,?,?) ON CONFLICT(id) DO UPDATE SET due_at=excluded.due_at,priority=excluded.priority,reason=excluded.reason,last_practiced=excluded.last_practiced`, "review_"+pattern, pattern, due, 1-mastery, "weakness or retention", now)
	return err
}
func reviewInterval(m float64, ok bool) time.Duration {
	if !ok {
		return 6 * time.Hour
	}
	days := math.Max(1, math.Min(30, 1+m*29))
	return time.Duration(days*24) * time.Hour
}
func clamp(v, a, b float64) float64 {
	if v < a {
		return a
	}
	if v > b {
		return b
	}
	return v
}

// updateProfileTx keeps policy decisions in application logic. Provider output
// supplies scores, while difficulty is adjusted conservatively from outcomes.
func updateProfileTx(tx *sql.Tx, e Eval, exerciseDifficulty float64) error {
	// Long-term ability is updated from rolling non-probe evidence. A single
	// success/failure therefore cannot move the learner state.
	_, _, err := updateLearnerAbilityTx(tx, difficultyConfig(defaultAdaptiveConfig()))
	return err
}

func (s *Server) updateAssessment(e Eval) error {
	var current float64
	var complete int
	var raw string
	if err := s.db.QueryRow("SELECT global_difficulty,assessment_complete,assessment_progress FROM user_profile WHERE id='default'").Scan(&current, &complete, &raw); err != nil {
		return err
	}
	if e.Verdict == "correct" {
		current += .35
	} else if e.Verdict == "mostly_correct" {
		current += .12
	} else {
		current -= .22
	}
	current = clamp(current, 1, 8)
	var progress []map[string]any
	_ = json.Unmarshal([]byte(raw), &progress)
	progress = append(progress, map[string]any{"verdict": e.Verdict, "pattern_score": e.PatternScore})
	if len(progress) > 12 {
		progress = progress[len(progress)-12:]
	}
	complete = boolInt(len(progress) >= 12)
	data, _ := json.Marshal(progress)
	_, err := s.db.Exec("UPDATE user_profile SET global_difficulty=?, assessment_complete=?, assessment_progress=?, updated_at=? WHERE id='default'", current, complete, string(data), time.Now().UTC().Format(time.RFC3339))
	return err
}

func (s *Server) progress() map[string]any {
	var d float64
	_ = s.db.QueryRow("SELECT global_difficulty FROM user_profile WHERE id='default'").Scan(&d)
	rows, _ := s.db.Query(`SELECT p.pattern,COALESCE(ls.mastery,m.mastery,.25),COALESCE(ls.retention,0),COALESCE(ls.transfer,0),COALESCE(ls.next_review_at,''),COALESCE(ls.attempt_count,m.attempts,0),COALESCE(ls.state,'') FROM sentence_patterns p LEFT JOIN pattern_mastery m ON m.pattern_id=p.id LEFT JOIN learner_skill_state ls ON ls.pattern_id=p.id AND ls.user_id='default' WHERE COALESCE(ls.attempt_count,m.attempts,0)>0 ORDER BY COALESCE(ls.mastery,m.mastery,.25) LIMIT 10`)
	weak := []map[string]any{}
	if rows != nil {
		for rows.Next() {
			var p, next string
			var m, ret, tr float64
			var a int
			var state string
			_ = rows.Scan(&p, &m, &ret, &tr, &next, &a, &state)
			if stateFromRow(a, 0, m, state, s.adaptiveConfig()) == stateWeak {
				weak = append(weak, map[string]any{"pattern": p, "mastery": m, "retention": ret, "transfer": tr, "next_review_at": next, "attempts": a, "state": stateWeak})
			}
		}
		rows.Close()
	}
	var count int
	var correct int
	_ = s.db.QueryRow("SELECT COUNT(*) FROM attempts WHERE evaluation_status='validated'").Scan(&count)
	_ = s.db.QueryRow("SELECT COUNT(*) FROM evaluations WHERE verdict IN ('correct','mostly_correct')").Scan(&correct)
	rate := 0.0
	if count > 0 {
		rate = float64(correct) / float64(count)
	}
	var due, probes, repeated int
	_ = s.db.QueryRow(`SELECT COUNT(*) FROM learner_skill_state WHERE next_review_at<=?`, time.Now().UTC().Format(time.RFC3339)).Scan(&due)
	_ = s.db.QueryRow(`SELECT COUNT(*) FROM attempts WHERE is_probe=1`).Scan(&probes)
	_ = s.db.QueryRow(`SELECT COUNT(*) FROM attempts a JOIN exercises e ON e.id=a.exercise_id WHERE a.evaluation_status='validated' AND a.normalized_chinese_hash IN (SELECT normalized_chinese_hash FROM attempts WHERE normalized_chinese_hash<>'' GROUP BY normalized_chinese_hash HAVING COUNT(*)>1)`).Scan(&repeated)
	unknown, weakCount := s.unknownAndWeakCounts()
	scenes, _ := s.sceneDiagnostics()
	return map[string]any{"recent_attempts": count, "recent_success_rate": rate, "global_difficulty": d, "weak_patterns": weak, "weak_pattern_count": weakCount, "unknown_pattern_count": unknown, "global_difficulty_trend": d, "due_reviews": due, "probe_count": probes, "exact_repeats": repeated, "scene_progress": scenes}
}
