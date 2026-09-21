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
	"strings"
	"sync"
	"time"

	_ "modernc.org/sqlite"
)

const schemaVersion = 1

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
}
type ChatResponse struct {
	Content  string        `json:"content"`
	Provider string        `json:"provider,omitempty"`
	Model    string        `json:"model,omitempty"`
	Latency  time.Duration `json:"-"`
}

type ProviderConfig struct {
	ID          string  `json:"id"`
	Name        string  `json:"name"`
	Type        string  `json:"type"`
	BaseURL     string  `json:"base_url"`
	APIKey      string  `json:"api_key,omitempty"`
	Model       string  `json:"model"`
	Timeout     int     `json:"timeout"`
	Temperature float64 `json:"temperature"`
	MaxTokens   int     `json:"max_tokens"`
	Enabled     bool    `json:"enabled"`
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

func (c HTTPChatClient) Chat(ctx context.Context, req ChatRequest) (*ChatResponse, error) {
	start := time.Now()
	timeout := time.Duration(c.cfg.Timeout) * time.Second
	if timeout <= 0 {
		timeout = 45 * time.Second
	}
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	payload := map[string]any{"model": c.cfg.Model, "messages": req.Messages, "temperature": req.Temperature, "max_tokens": req.MaxTokens}
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
	if c.cfg.APIKey != "" {
		request.Header.Set("Authorization", "Bearer "+c.cfg.APIKey)
	}
	resp, err := http.DefaultClient.Do(request)
	if err != nil {
		return nil, err
	}
	defer resp.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(resp.Body, 4<<20))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("provider returned %s", resp.Status)
	}
	var out struct {
		Choices []struct {
			Message struct {
				Content string `json:"content"`
			} `json:"message"`
		} `json:"choices"`
	}
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, fmt.Errorf("invalid provider response: %w", err)
	}
	if len(out.Choices) == 0 {
		return nil, errors.New("provider returned no choices")
	}
	return &ChatResponse{Content: out.Choices[0].Message.Content, Provider: c.cfg.ID, Model: c.cfg.Model, Latency: time.Since(start)}, nil
}

func main() {
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
		port = "8080"
	}
	log.Printf("English Practice listening on http://localhost:%s", port)
	log.Fatal(http.ListenAndServe(":"+port, logging(mux)))
}

func migrate(db *sql.DB) error {
	stmts := []string{
		`CREATE TABLE IF NOT EXISTS schema_meta (version INTEGER NOT NULL)`,
		`CREATE TABLE IF NOT EXISTS scenes (id TEXT PRIMARY KEY, name TEXT NOT NULL, description TEXT NOT NULL DEFAULT '')`,
		`CREATE TABLE IF NOT EXISTS communication_intents (id TEXT PRIMARY KEY, name TEXT NOT NULL)`,
		`CREATE TABLE IF NOT EXISTS sentence_patterns (id TEXT PRIMARY KEY, pattern TEXT NOT NULL, intent_id TEXT NOT NULL, difficulty REAL NOT NULL, metadata_json TEXT NOT NULL DEFAULT '{}', FOREIGN KEY(intent_id) REFERENCES communication_intents(id))`,
		`CREATE TABLE IF NOT EXISTS exercises (id TEXT PRIMARY KEY, chinese_prompt TEXT NOT NULL, pattern_id TEXT NOT NULL, scene_id TEXT NOT NULL, intent_id TEXT NOT NULL, difficulty REAL NOT NULL, metadata_json TEXT NOT NULL DEFAULT '{}', created_at TEXT NOT NULL, FOREIGN KEY(pattern_id) REFERENCES sentence_patterns(id), FOREIGN KEY(scene_id) REFERENCES scenes(id))`,
		`CREATE TABLE IF NOT EXISTS sessions (id TEXT PRIMARY KEY, mode TEXT NOT NULL, started_at TEXT NOT NULL, ended_at TEXT)`,
		`CREATE TABLE IF NOT EXISTS attempts (id TEXT PRIMARY KEY, session_id TEXT NOT NULL, exercise_id TEXT NOT NULL, user_answer TEXT NOT NULL, submitted_at TEXT NOT NULL, provider TEXT NOT NULL DEFAULT '', model TEXT NOT NULL DEFAULT '', prompt_version TEXT NOT NULL DEFAULT '', evaluation_status TEXT NOT NULL, FOREIGN KEY(session_id) REFERENCES sessions(id), FOREIGN KEY(exercise_id) REFERENCES exercises(id))`,
		`CREATE TABLE IF NOT EXISTS evaluations (id TEXT PRIMARY KEY, attempt_id TEXT NOT NULL UNIQUE, verdict TEXT NOT NULL, meaning_score REAL NOT NULL, grammar_score REAL NOT NULL, naturalness_score REAL NOT NULL, pattern_score REAL NOT NULL, errors_json TEXT NOT NULL, suggested_answer TEXT NOT NULL, explanation_zh TEXT NOT NULL, validated INTEGER NOT NULL, created_at TEXT NOT NULL, FOREIGN KEY(attempt_id) REFERENCES attempts(id))`,
		`CREATE TABLE IF NOT EXISTS pattern_mastery (pattern_id TEXT PRIMARY KEY, attempts INTEGER NOT NULL, correct INTEGER NOT NULL, recent_accuracy REAL NOT NULL, long_term_accuracy REAL NOT NULL, consecutive_correct INTEGER NOT NULL, last_practiced TEXT, mastery REAL NOT NULL, FOREIGN KEY(pattern_id) REFERENCES sentence_patterns(id))`,
		`CREATE TABLE IF NOT EXISTS scene_mastery (scene_id TEXT PRIMARY KEY, attempts INTEGER NOT NULL, correct INTEGER NOT NULL, mastery REAL NOT NULL, last_practiced TEXT, FOREIGN KEY(scene_id) REFERENCES scenes(id))`,
		`CREATE TABLE IF NOT EXISTS error_stats (error_type TEXT PRIMARY KEY, count INTEGER NOT NULL, severity_sum REAL NOT NULL, last_seen TEXT NOT NULL)`,
		`CREATE TABLE IF NOT EXISTS review_schedule (id TEXT PRIMARY KEY, pattern_id TEXT NOT NULL, due_at TEXT NOT NULL, priority REAL NOT NULL, reason TEXT NOT NULL, last_practiced TEXT, FOREIGN KEY(pattern_id) REFERENCES sentence_patterns(id))`,
		`CREATE TABLE IF NOT EXISTS user_profile (id TEXT PRIMARY KEY, global_difficulty REAL NOT NULL, assessment_complete INTEGER NOT NULL, assessment_progress TEXT NOT NULL, updated_at TEXT NOT NULL)`,
		`CREATE TABLE IF NOT EXISTS llm_providers (id TEXT PRIMARY KEY, name TEXT NOT NULL, type TEXT NOT NULL, base_url TEXT NOT NULL, api_key TEXT NOT NULL DEFAULT '', model TEXT NOT NULL, timeout INTEGER NOT NULL, temperature REAL NOT NULL, max_tokens INTEGER NOT NULL, enabled INTEGER NOT NULL)`,
		`CREATE TABLE IF NOT EXISTS llm_task_configs (task_type TEXT PRIMARY KEY, provider_id TEXT NOT NULL, model TEXT NOT NULL, temperature REAL NOT NULL, max_tokens INTEGER NOT NULL, FOREIGN KEY(provider_id) REFERENCES llm_providers(id))`,
	}
	for _, q := range stmts {
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

func seed(db *sql.DB) error {
	var count int
	if err := db.QueryRow("SELECT COUNT(*) FROM scenes").Scan(&count); err != nil {
		return err
	}
	if count > 0 {
		return nil
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
	return err
}

func loadProviders(s *Server) error {
	rows, err := s.db.Query(`SELECT id,name,type,base_url,api_key,model,timeout,temperature,max_tokens,enabled FROM llm_providers`)
	if err != nil {
		return err
	}
	defer rows.Close()
	for rows.Next() {
		var c ProviderConfig
		var en int
		if err := rows.Scan(&c.ID, &c.Name, &c.Type, &c.BaseURL, &c.APIKey, &c.Model, &c.Timeout, &c.Temperature, &c.MaxTokens, &en); err != nil {
			return err
		}
		c.Enabled = en == 1
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
		rows, err := s.db.Query("SELECT id,name,description FROM scenes ORDER BY name")
		if err != nil {
			jsonResp(w, 500, map[string]string{"error": err.Error()})
			return
		}
		defer rows.Close()
		out := []map[string]string{}
		for rows.Next() {
			var a, b, c string
			_ = rows.Scan(&a, &b, &c)
			out = append(out, map[string]string{"id": a, "name": b, "description": c})
		}
		jsonResp(w, 200, out)
	})
	mux.HandleFunc("/api/patterns", func(w http.ResponseWriter, r *http.Request) {
		rows, err := s.db.Query(`SELECT p.id,p.pattern,p.difficulty,COALESCE(m.mastery,.25),COALESCE(m.attempts,0) FROM sentence_patterns p LEFT JOIN pattern_mastery m ON p.id=m.pattern_id ORDER BY p.difficulty`)
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
			_ = rows.Scan(&id, &p, &d, &ma, &a)
			out = append(out, map[string]any{"id": id, "pattern": p, "difficulty": d, "mastery": ma, "attempts": a})
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
			Mode    string `json:"mode"`
			SceneID string `json:"scene_id"`
		}
		if r.Method == http.MethodPost {
			_ = decode(r, &req)
		}
		var diff float64
		_ = s.db.QueryRow("SELECT global_difficulty FROM user_profile WHERE id='default'").Scan(&diff)
		ex, err := s.generateExercise(r.Context(), diff, req.Mode, req.SceneID)
		if err != nil {
			jsonResp(w, 500, map[string]string{"error": err.Error()})
			return
		}
		jsonResp(w, 200, ex)
	})
	mux.HandleFunc("/api/sessions", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodPost {
			var req struct {
				Mode string `json:"mode"`
			}
			_ = decode(r, &req)
			if req.Mode == "" {
				req.Mode = "adaptive"
			}
			session := id("session")
			_, err := s.db.Exec("INSERT INTO sessions(id,mode,started_at) VALUES(?,?,?)", session, req.Mode, time.Now().UTC().Format(time.RFC3339))
			if err != nil {
				jsonResp(w, 500, map[string]string{"error": err.Error()})
				return
			}
			jsonResp(w, 201, map[string]any{"session_id": session, "mode": req.Mode})
			return
		}
		rows, err := s.db.Query("SELECT id,mode,started_at,ended_at FROM sessions ORDER BY started_at DESC LIMIT 50")
		if err != nil {
			jsonResp(w, 500, map[string]string{"error": err.Error()})
			return
		}
		defer rows.Close()
		out := []map[string]any{}
		for rows.Next() {
			var idv, mode, start string
			var end sql.NullString
			_ = rows.Scan(&idv, &mode, &start, &end)
			out = append(out, map[string]any{"session_id": idv, "mode": mode, "started_at": start, "ended_at": end.String})
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
		rows, err := s.db.Query(`SELECT a.id,a.submitted_at,e.chinese_prompt,a.user_answer,v.verdict,v.suggested_answer,v.errors_json,e.pattern_id,e.scene_id FROM attempts a JOIN exercises e ON e.id=a.exercise_id LEFT JOIN evaluations v ON v.attempt_id=a.id ORDER BY a.submitted_at DESC LIMIT 100`)
		if err != nil {
			jsonResp(w, 500, map[string]string{"error": err.Error()})
			return
		}
		defer rows.Close()
		out := []map[string]any{}
		for rows.Next() {
			var a, b, c, d, e, f, g, h, i string
			_ = rows.Scan(&a, &b, &c, &d, &e, &f, &g, &h, &i)
			var er any
			_ = json.Unmarshal([]byte(g), &er)
			out = append(out, map[string]any{"id": a, "submitted_at": b, "prompt": c, "answer": d, "verdict": e, "suggested_answer": f, "errors": er, "pattern_id": h, "scene_id": i})
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
		_, err := s.db.Exec(`INSERT INTO llm_providers(id,name,type,base_url,api_key,model,timeout,temperature,max_tokens,enabled) VALUES(?,?,?,?,?,?,?,?,?,?) ON CONFLICT(id) DO UPDATE SET name=excluded.name,type=excluded.type,base_url=excluded.base_url,api_key=CASE WHEN excluded.api_key='' THEN llm_providers.api_key ELSE excluded.api_key END,model=excluded.model,timeout=excluded.timeout,temperature=excluded.temperature,max_tokens=excluded.max_tokens,enabled=excluded.enabled`, c.ID, c.Name, c.Type, c.BaseURL, c.APIKey, c.Model, c.Timeout, c.Temperature, c.MaxTokens, boolInt(c.Enabled))
		if err != nil {
			jsonResp(w, 500, map[string]string{"error": err.Error()})
			return
		}
		s.llm.mu.Lock()
		s.llm.configs[c.ID] = c
		s.llm.mu.Unlock()
		c.APIKey = ""
		jsonResp(w, 200, c)
	})
	mux.HandleFunc("/api/providers/test", func(w http.ResponseWriter, r *http.Request) {
		var c ProviderConfig
		if err := decode(r, &c); err != nil {
			jsonResp(w, 400, map[string]string{"error": "invalid request"})
			return
		}
		start := time.Now()
		_, err := s.llm.Client(c).Chat(r.Context(), ChatRequest{Messages: []ChatMessage{{Role: "user", Content: "Reply with OK"}}, MaxTokens: 8})
		if err != nil {
			jsonResp(w, 200, map[string]any{"ok": false, "error": err.Error(), "latency_ms": time.Since(start).Milliseconds()})
			return
		}
		jsonResp(w, 200, map[string]any{"ok": true, "latency_ms": time.Since(start).Milliseconds()})
	})
	mux.HandleFunc("/api/task-configs", func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			rows, err := s.db.Query("SELECT task_type,provider_id,model,temperature,max_tokens FROM llm_task_configs ORDER BY task_type")
			if err != nil {
				jsonResp(w, 500, map[string]string{"error": err.Error()})
				return
			}
			defer rows.Close()
			out := []map[string]any{}
			for rows.Next() {
				var task, provider, model string
				var temp float64
				var max int
				_ = rows.Scan(&task, &provider, &model, &temp, &max)
				out = append(out, map[string]any{"task_type": task, "provider_id": provider, "model": model, "temperature": temp, "max_tokens": max})
			}
			jsonResp(w, 200, out)
			return
		}
		var cfg struct {
			TaskType    string  `json:"task_type"`
			ProviderID  string  `json:"provider_id"`
			Model       string  `json:"model"`
			Temperature float64 `json:"temperature"`
			MaxTokens   int     `json:"max_tokens"`
		}
		if err := decode(r, &cfg); err != nil || cfg.TaskType == "" || cfg.ProviderID == "" {
			jsonResp(w, 400, map[string]string{"error": "task_type and provider_id are required"})
			return
		}
		_, err := s.db.Exec(`INSERT INTO llm_task_configs(task_type,provider_id,model,temperature,max_tokens) VALUES(?,?,?,?,?) ON CONFLICT(task_type) DO UPDATE SET provider_id=excluded.provider_id,model=excluded.model,temperature=excluded.temperature,max_tokens=excluded.max_tokens`, cfg.TaskType, cfg.ProviderID, cfg.Model, cfg.Temperature, cfg.MaxTokens)
		if err != nil {
			jsonResp(w, 500, map[string]string{"error": err.Error()})
			return
		}
		jsonResp(w, 200, cfg)
	})
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

func (s *Server) generateExercise(ctx context.Context, diff float64, mode, scene string) (map[string]any, error) {
	var patternID, pat, intent string
	var pd float64
	query := `SELECT p.id,p.pattern,i.name,p.difficulty FROM sentence_patterns p JOIN communication_intents i ON i.id=p.intent_id WHERE p.difficulty BETWEEN ? AND ? AND NOT EXISTS (SELECT 1 FROM exercises recent WHERE recent.pattern_id=p.id AND recent.created_at >= ?) ORDER BY ABS(p.difficulty-?) LIMIT 1`
	args := []any{math.Max(1, diff-1.4), math.Min(8, diff+1.4), time.Now().UTC().Add(-2 * time.Hour).Format(time.RFC3339), diff}
	if mode == "weak" {
		query = `SELECT p.id,p.pattern,i.name,p.difficulty FROM sentence_patterns p JOIN communication_intents i ON i.id=p.intent_id LEFT JOIN pattern_mastery m ON m.pattern_id=p.id ORDER BY COALESCE(m.mastery,0.25), ABS(p.difficulty-?) LIMIT 1`
		args = []any{diff}
	}
	if mode == "review" {
		query = `SELECT p.id,p.pattern,i.name,p.difficulty FROM review_schedule r JOIN sentence_patterns p ON p.id=r.pattern_id JOIN communication_intents i ON i.id=p.intent_id ORDER BY r.due_at LIMIT 1`
		args = nil
	}
	err := s.db.QueryRow(query, args...).Scan(&patternID, &pat, &intent, &pd)
	if err != nil { // The recent-exercise guard is best effort when the range is exhausted.
		query = `SELECT p.id,p.pattern,i.name,p.difficulty FROM sentence_patterns p JOIN communication_intents i ON i.id=p.intent_id WHERE p.difficulty BETWEEN ? AND ? ORDER BY ABS(p.difficulty-?) LIMIT 1`
		err = s.db.QueryRow(query, math.Max(1, diff-1.4), math.Min(8, diff+1.4), diff).Scan(&patternID, &pat, &intent, &pd)
	}
	if err != nil {
		return nil, err
	}
	prompts := map[string]string{"going-to": "我本来打算昨天给你打电话的。", "modal-possibility": "我今天可能会晚一点到。", "conditional": "如果明天下雨，我们就不去了。", "polite-request": "你能把会议移到下午吗？", "polite-refusal": "恐怕我这周抽不出时间。", "because": "我没有去，因为我感觉不舒服。", "past-perfect": "她到达时，我已经吃过饭了。", "wish-past": "我真希望我当时听了你的建议。"}
	ch := prompts[patternID]
	if ch == "" {
		ch = "请用自然英语表达这句话。"
	}
	exID := id("exercise")
	meta, _ := json.Marshal(map[string]any{"mode": mode, "generated": "seed"})
	_, err = s.db.Exec(`INSERT INTO exercises(id,chinese_prompt,pattern_id,scene_id,intent_id,difficulty,metadata_json,created_at) VALUES(?,?,?,?,?,?,?,?)`, exID, ch, patternID, chooseScene(scene), intent, pd, string(meta), time.Now().UTC().Format(time.RFC3339))
	if err != nil {
		return nil, err
	}
	return map[string]any{"exercise_id": exID, "chinese_prompt": ch, "target_pattern": pat, "pattern_id": patternID, "scene_id": chooseScene(scene), "communication_intent": intent, "difficulty": pd}, nil
}
func chooseScene(s string) string {
	if s != "" {
		return s
	}
	return "daily"
}

type Eval struct {
	Verdict          string           `json:"verdict"`
	MeaningScore     float64          `json:"meaning_score"`
	GrammarScore     float64          `json:"grammar_score"`
	NaturalnessScore float64          `json:"naturalness_score"`
	PatternScore     float64          `json:"pattern_score"`
	Errors           []map[string]any `json:"errors"`
	SuggestedAnswer  string           `json:"suggested_answer"`
	ExplanationZH    string           `json:"explanation_zh"`
}

func (s *Server) submitAttempt(ctx context.Context, session, exercise, answer string) (map[string]any, error) {
	if session == "" {
		session = id("session")
	}
	_, _ = s.db.Exec("INSERT OR IGNORE INTO sessions(id,mode,started_at) VALUES(?,?,?)", session, "adaptive", time.Now().UTC().Format(time.RFC3339))
	var prompt, pattern, scene string
	var difficulty float64
	if err := s.db.QueryRow("SELECT chinese_prompt,pattern_id,scene_id,difficulty FROM exercises WHERE id=?", exercise).Scan(&prompt, &pattern, &scene, &difficulty); err != nil {
		return nil, err
	}
	attempt := id("attempt")
	_, err := s.db.Exec(`INSERT INTO attempts(id,session_id,exercise_id,user_answer,submitted_at,evaluation_status) VALUES(?,?,?,?,?,?)`, attempt, session, exercise, answer, time.Now().UTC().Format(time.RFC3339), "pending")
	if err != nil {
		return nil, err
	}
	eval, provider, model, err := s.evaluate(ctx, prompt, pattern, answer)
	if err != nil {
		_, _ = s.db.Exec("UPDATE attempts SET evaluation_status='failed' WHERE id=?", attempt)
		return map[string]any{"attempt_id": attempt, "evaluation_status": "failed", "error": "AI 评估失败，本次不会更新掌握度。", "retryable": true}, nil
	}
	errorsJSON, _ := json.Marshal(eval.Errors)
	tx, err := s.db.Begin()
	if err != nil {
		return nil, err
	}
	defer tx.Rollback()
	_, err = tx.Exec(`UPDATE attempts SET provider=?,model=?,prompt_version='v1',evaluation_status='validated' WHERE id=?`, provider, model, attempt)
	if err != nil {
		return nil, err
	}
	_, err = tx.Exec(`INSERT INTO evaluations(id,attempt_id,verdict,meaning_score,grammar_score,naturalness_score,pattern_score,errors_json,suggested_answer,explanation_zh,validated,created_at) VALUES(?,?,?,?,?,?,?,?,?,?,1,?)`, id("evaluation"), attempt, eval.Verdict, eval.MeaningScore, eval.GrammarScore, eval.NaturalnessScore, eval.PatternScore, string(errorsJSON), eval.SuggestedAnswer, eval.ExplanationZH, time.Now().UTC().Format(time.RFC3339))
	if err != nil {
		return nil, err
	}
	if err = updateMasteryTx(tx, pattern, scene, difficulty, eval); err != nil {
		return nil, err
	}
	if err = updateProfileTx(tx, eval, difficulty); err != nil {
		return nil, err
	}
	if err = tx.Commit(); err != nil {
		return nil, err
	}
	return map[string]any{"attempt_id": attempt, "evaluation_status": "validated", "evaluation": eval, "session_id": session}, nil
}

func (s *Server) evaluate(ctx context.Context, prompt, pattern, answer string) (Eval, string, string, error) {
	s.llm.mu.RLock()
	var c ProviderConfig
	for _, x := range s.llm.configs {
		if x.Enabled {
			c = x
			break
		}
	}
	s.llm.mu.RUnlock()
	if c.ID != "" {
		req := ChatRequest{Messages: []ChatMessage{{Role: "system", Content: "Evaluate an English learner answer. Return JSON with verdict, meaning_score, grammar_score, naturalness_score, pattern_score, errors, suggested_answer, explanation_zh."}, {Role: "user", Content: fmt.Sprintf("Prompt: %s\nTarget pattern: %s\nAnswer: %s", prompt, pattern, answer)}}, Temperature: c.Temperature, MaxTokens: c.MaxTokens, JSONMode: true}
		var lastErr error
		for attempt := 0; attempt < 2; attempt++ {
			resp, err := s.llm.Client(c).Chat(ctx, req)
			if err != nil {
				lastErr = err
				continue
			}
			var ev Eval
			if err := json.Unmarshal([]byte(resp.Content), &ev); err != nil {
				lastErr = fmt.Errorf("invalid JSON from provider: %w", err)
				continue
			}
			if err := validateEval(&ev); err != nil {
				lastErr = fmt.Errorf("evaluation schema validation failed: %w", err)
				continue
			}
			return ev, c.ID, c.Model, nil
		}
		return Eval{}, c.ID, c.Model, lastErr
	}
	return heuristicEval(prompt, pattern, answer), "local", "heuristic", nil
}
func validateEval(e *Eval) error {
	if e.Verdict != "correct" && e.Verdict != "mostly_correct" && e.Verdict != "needs_improvement" && e.Verdict != "incorrect" {
		return errors.New("invalid verdict")
	}
	for _, v := range []float64{e.MeaningScore, e.GrammarScore, e.NaturalnessScore, e.PatternScore} {
		if v < 0 || v > 1 {
			return errors.New("score out of range")
		}
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
	e := Eval{Verdict: "needs_improvement", MeaningScore: .45, GrammarScore: .45, NaturalnessScore: .45, PatternScore: .4, Errors: []map[string]any{}, SuggestedAnswer: a, ExplanationZH: "你的表达可以理解，继续练习这个句型。"}
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
			e.Errors = append(e.Errors, map[string]any{"type": "target_pattern_missing", "severity": "moderate", "explanation": "没有清楚表达‘本来打算’。"})
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
			e.Errors = append(e.Errors, map[string]any{"type": "modal", "severity": "major", "explanation": "需要使用 might 或 may 表达可能性。"})
		}
	default:
		if len(a) > 8 {
			e.Verdict = "mostly_correct"
			e.MeaningScore = .75
			e.GrammarScore = .7
			e.NaturalnessScore = .7
			e.PatternScore = .6
		} else {
			e.Errors = append(e.Errors, map[string]any{"type": "missing_information", "severity": "major", "explanation": "答案信息不足。"})
		}
	}
	return e
}

func updateMasteryTx(tx *sql.Tx, pattern, scene string, difficulty float64, e Eval) error {
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
	mastery = clamp(.35*recent+.35*long+.15*math.Min(1, float64(consec)/5)+.15*e.PatternScore-severity, 0, 1)
	now := time.Now().UTC().Format(time.RFC3339)
	if _, err := tx.Exec(`INSERT INTO pattern_mastery(pattern_id,attempts,correct,recent_accuracy,long_term_accuracy,consecutive_correct,last_practiced,mastery) VALUES(?,?,?,?,?,?,?,?) ON CONFLICT(pattern_id) DO UPDATE SET attempts=excluded.attempts,correct=excluded.correct,recent_accuracy=excluded.recent_accuracy,long_term_accuracy=excluded.long_term_accuracy,consecutive_correct=excluded.consecutive_correct,last_practiced=excluded.last_practiced,mastery=excluded.mastery`, pattern, attempts, correct, recent, long, consec, now, mastery); err != nil {
		return err
	}
	var sa, sc int
	var sm float64
	_ = tx.QueryRow("SELECT attempts,correct,mastery FROM scene_mastery WHERE scene_id=?", scene).Scan(&sa, &sc, &sm)
	sa++
	if ok {
		sc++
	}
	sm = .7*sm + .3*e.MeaningScore
	if sa == 1 {
		sm = e.MeaningScore
	}
	if _, err := tx.Exec(`INSERT INTO scene_mastery(scene_id,attempts,correct,mastery,last_practiced) VALUES(?,?,?,?,?) ON CONFLICT(scene_id) DO UPDATE SET attempts=excluded.attempts,correct=excluded.correct,mastery=excluded.mastery,last_practiced=excluded.last_practiced`, scene, sa, sc, sm, now); err != nil {
		return err
	}
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
	var current float64
	if err := tx.QueryRow("SELECT global_difficulty FROM user_profile WHERE id='default'").Scan(&current); err != nil {
		return err
	}
	success := e.Verdict == "correct" || e.Verdict == "mostly_correct"
	if success && e.PatternScore >= .75 {
		current += .12
	} else if !success {
		current -= .10
	}
	current = clamp(current, 1, 8)
	_, err := tx.Exec("UPDATE user_profile SET global_difficulty=?, updated_at=? WHERE id='default'", current, time.Now().UTC().Format(time.RFC3339))
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
	rows, _ := s.db.Query(`SELECT p.pattern,m.mastery,m.attempts FROM sentence_patterns p JOIN pattern_mastery m ON m.pattern_id=p.id ORDER BY m.mastery LIMIT 10`)
	defer func() {
		if rows != nil {
			rows.Close()
		}
	}()
	weak := []map[string]any{}
	if rows != nil {
		for rows.Next() {
			var p string
			var m float64
			var a int
			_ = rows.Scan(&p, &m, &a)
			weak = append(weak, map[string]any{"pattern": p, "mastery": m, "attempts": a})
		}
	}
	var count int
	var correct int
	_ = s.db.QueryRow("SELECT COUNT(*) FROM attempts WHERE evaluation_status='validated'").Scan(&count)
	_ = s.db.QueryRow("SELECT COUNT(*) FROM evaluations WHERE verdict IN ('correct','mostly_correct')").Scan(&correct)
	rate := 0.0
	if count > 0 {
		rate = float64(correct) / float64(count)
	}
	return map[string]any{"recent_attempts": count, "recent_success_rate": rate, "global_difficulty": d, "weak_patterns": weak}
}
