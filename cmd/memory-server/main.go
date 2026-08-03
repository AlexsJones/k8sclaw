// memory-server runs as a standalone Deployment per Agent.
// It provides persistent memory for Sympozium agents via an HTTP API,
// backed by SQLite with FTS5 for full-text search.
//
// The SQLite database lives on a PersistentVolume so data survives across
// ephemeral agent pod runs. Agent pods call this server over HTTP via a
// ClusterIP Service.
//
// Membrane extensions add visibility-based permeability (public/trusted/private),
// provenance tracking (source_agent, parent_id), event sequencing, and time decay.
package main

import (
	"context"
	"crypto/subtle"
	"database/sql"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"time"

	"github.com/sympozium-ai/sympozium/pkg/telemetry"
	"go.opentelemetry.io/contrib/instrumentation/net/http/otelhttp"
	"go.opentelemetry.io/otel"
	"go.opentelemetry.io/otel/attribute"
	"go.opentelemetry.io/otel/metric"
	oteltrace "go.opentelemetry.io/otel/trace"
	_ "modernc.org/sqlite"
)

const defaultDBPath = "/data/memory.db"

// memObservability holds the OTel instruments for the memory sidecar. Agents
// in an Ensemble read and write a shared memory store, but that interaction was
// previously unmeasurable (ISI-1406 gap 6). These counters expose read/write
// volume per agent and operation so memory traffic is visible in Dynatrace.
type memObservability struct {
	enabled  bool
	shutdown func(context.Context) error
	reads    metric.Int64Counter
	writes   metric.Int64Counter
}

var memObs = &memObservability{shutdown: func(context.Context) error { return nil }}

// agentName is the owning agent's name, stamped on every memory metric so a
// shared-memory Ensemble can be broken down per agent.
var agentName = envOr("MEMORY_AGENT", "")

// initMemObservability bootstraps OTel via the shared pkg/telemetry path when an
// OTLP endpoint is configured. It is a no-op (counters stay nil) otherwise, so
// the sidecar runs unchanged in environments without observability.
func initMemObservability(ctx context.Context) {
	endpoint := firstNonEmptyEnv("OTEL_EXPORTER_OTLP_ENDPOINT", "SYMPOZIUM_OTEL_OTLP_ENDPOINT")
	if endpoint == "" {
		return
	}
	serviceName := firstNonEmptyEnv("OTEL_SERVICE_NAME", "SYMPOZIUM_OTEL_SERVICE_NAME")
	if serviceName == "" {
		serviceName = "sympozium-memory-server"
	}
	tel, err := telemetry.Init(ctx, telemetry.Config{
		ServiceName:     serviceName,
		BatchTimeout:    1 * time.Second,
		ShutdownTimeout: 3 * time.Second,
	})
	if err != nil {
		log.Printf("[memory-server] OTel init failed, metrics disabled: %v", err)
		return
	}
	meter := otel.Meter("sympozium.ai/memory-server")
	reads, err := meter.Int64Counter("sympozium.memory.read",
		metric.WithUnit("{op}"), metric.WithDescription("Memory read operations (search/list/provenance)"))
	if err != nil {
		log.Printf("[memory-server] failed creating sympozium.memory.read: %v", err)
	}
	writes, err := meter.Int64Counter("sympozium.memory.write",
		metric.WithUnit("{op}"), metric.WithDescription("Memory write operations (store)"))
	if err != nil {
		log.Printf("[memory-server] failed creating sympozium.memory.write: %v", err)
	}
	memObs = &memObservability{enabled: true, shutdown: tel.Shutdown, reads: reads, writes: writes}
	log.Printf("[memory-server] OTel metrics enabled, endpoint=%s service=%s", endpoint, serviceName)
}

// recordRead increments the read counter. op is the read kind (search/list/...),
// status is "ok" or "error". caller is the requesting agent when provided.
func (o *memObservability) recordRead(ctx context.Context, op, status, caller string) {
	if o == nil || !o.enabled || o.reads == nil {
		return
	}
	o.reads.Add(ctx, 1, metric.WithAttributes(
		attribute.String("op", op),
		attribute.String("status", status),
		attribute.String("agent", agentName),
	))
	// caller_agent is caller-controlled (read from the request body/query on a
	// plain HTTP endpoint), so it stays off the bounded counter and goes on the
	// span instead, where high cardinality is acceptable.
	if caller != "" {
		oteltrace.SpanFromContext(ctx).SetAttributes(attribute.String("caller_agent", caller))
	}
}

// recordWrite increments the write counter. status is "ok" or "error", source
// is the agent that produced the entry when provided.
func (o *memObservability) recordWrite(ctx context.Context, status, source string) {
	if o == nil || !o.enabled || o.writes == nil {
		return
	}
	o.writes.Add(ctx, 1, metric.WithAttributes(
		attribute.String("status", status),
		attribute.String("agent", agentName),
	))
	// source_agent is caller-controlled; keep it on the span, not the counter.
	if source != "" {
		oteltrace.SpanFromContext(ctx).SetAttributes(attribute.String("source_agent", source))
	}
}

func firstNonEmptyEnv(keys ...string) string {
	for _, k := range keys {
		if v := os.Getenv(k); v != "" {
			return v
		}
	}
	return ""
}

// apiResponse is the standard JSON response format.
type apiResponse struct {
	Success bool   `json:"success"`
	Content any    `json:"content,omitempty"`
	Error   string `json:"error,omitempty"`
}

// memoryEntry represents a stored memory.
type memoryEntry struct {
	ID          int64          `json:"id"`
	Content     string         `json:"content"`
	Tags        []string       `json:"tags,omitempty"`
	Visibility  string         `json:"visibility,omitempty"`
	SourceAgent string         `json:"source_agent,omitempty"`
	ParentID    int64          `json:"parent_id,omitempty"`
	Seq         int64          `json:"seq,omitempty"`
	CreatedAt   string         `json:"created_at"`
	UpdatedAt   string         `json:"updated_at"`
	Evidence    *EvidenceTrace `json:"evidence,omitempty"`
}

// EvidenceTrace captures how a memory entry was derived, enabling
// quality-based filtering and provenance auditing.
type EvidenceTrace struct {
	Kind        string  `json:"kind"`                   // tool_result, external_source, llm_interpretation, agent_opinion
	ToolCall    string  `json:"tool_call,omitempty"`    // tool name + args that produced this
	RawResult   string  `json:"raw_result,omitempty"`   // unmodified tool output (truncated)
	Source      string  `json:"source,omitempty"`       // URL, doc ref, or upstream entry ID
	Confidence  float64 `json:"confidence,omitempty"`   // 0.0-1.0, set by producing agent
	DerivedFrom []int64 `json:"derived_from,omitempty"` // entry IDs this was derived from
}

func main() {
	dbPath := envOr("MEMORY_DB_PATH", defaultDBPath)
	port := envOr("MEMORY_PORT", "8080")

	// Admin-only delete is gated on a bearer token supplied via a Secret and
	// injected only into this pod's env (never into agent pods). When unset,
	// DELETE /delete is disabled, so existing deployments are unaffected.
	adminToken := os.Getenv("MEMORY_ADMIN_TOKEN")

	// Ensure database directory exists.
	if err := os.MkdirAll(filepath.Dir(dbPath), 0o755); err != nil {
		log.Fatalf("failed to create db directory: %v", err)
	}

	// Open SQLite database and initialize schema.
	db, err := sql.Open("sqlite", dbPath+"?_pragma=journal_mode(wal)&_pragma=busy_timeout(5000)")
	if err != nil {
		log.Fatalf("failed to open database: %v", err)
	}
	defer db.Close()

	if err := initSchema(db); err != nil {
		log.Fatalf("failed to initialize schema: %v", err)
	}
	if err := migrateMembraneColumns(db); err != nil {
		log.Fatalf("failed to run membrane migration: %v", err)
	}
	if err := migrateEvidenceColumn(db); err != nil {
		log.Fatalf("failed to run evidence migration: %v", err)
	}

	// Bootstrap OTel (ISI-1406 gap 6). No-op when no OTLP endpoint is set.
	initMemObservability(context.Background())
	defer func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = memObs.shutdown(shutdownCtx)
	}()

	mux := http.NewServeMux()
	mux.HandleFunc("POST /search", searchHandler(db))
	mux.HandleFunc("POST /store", storeHandler(db))
	mux.HandleFunc("GET /list", listHandler(db))
	mux.HandleFunc("GET /stats", statsHandler(db))
	mux.HandleFunc("GET /provenance", provenanceHandler(db))
	mux.HandleFunc("DELETE /delete", deleteHandler(db, adminToken))
	mux.HandleFunc("GET /health", func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, "ok")
	})

	// Wrap the router with otelhttp so each memory operation emits a server
	// span (ISI-1406 gap 6 — "spans on the memory part"). The handler extracts
	// the W3C traceparent from the incoming request (the agent-runner injects it
	// via its otelhttp client), so memory reads/writes nest under the agent's
	// run trace and the full BMAD chain instead of appearing as orphans. Spans
	// are named by route (e.g. "memory POST /store"); /health is left
	// uninstrumented to avoid probe noise. No-op when OTel is disabled — the
	// global TracerProvider is then a noop and spans are dropped cheaply.
	// Cap request bodies so a single caller cannot exhaust the backing
	// PersistentVolume (or the process's memory) with an oversized /store
	// payload. 4 MiB comfortably exceeds any legitimate memory entry.
	const maxBodyBytes = 4 << 20
	bodyLimited := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Body != nil {
			r.Body = http.MaxBytesReader(w, r.Body, maxBodyBytes)
		}
		mux.ServeHTTP(w, r)
	})

	handler := otelhttp.NewHandler(bodyLimited, "memory-server",
		otelhttp.WithSpanNameFormatter(func(_ string, r *http.Request) string {
			return "memory " + r.Method + " " + r.URL.Path
		}),
		otelhttp.WithFilter(func(r *http.Request) bool {
			return r.URL.Path != "/health"
		}),
	)

	addr := ":" + port
	log.Printf("[memory-server] listening on %s, db=%s", addr, dbPath)
	if err := http.ListenAndServe(addr, handler); err != nil {
		log.Fatalf("server error: %v", err)
	}
}

func searchHandler(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Query       string   `json:"query"`
			TopK        int      `json:"top_k"`
			CallerAgent string   `json:"caller_agent"`
			TrustPeers  []string `json:"trust_peers"`
			AcceptTags  []string `json:"accept_tags"`
			MaxAge      string   `json:"max_age"`
			MinKind     string   `json:"min_kind"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			log.Printf("[search] bad request: %v", err)
			writeJSON(w, http.StatusBadRequest, apiResponse{Error: "invalid JSON body"})
			return
		}
		if req.Query == "" {
			log.Printf("[search] rejected: empty query")
			writeJSON(w, http.StatusBadRequest, apiResponse{Error: "'query' is required"})
			return
		}
		if req.TopK <= 0 {
			req.TopK = 5
		}

		log.Printf("[search] query=%q top_k=%d caller=%s", truncateLog(req.Query, 120), req.TopK, req.CallerAgent)
		results, err := searchMemories(db, req.Query, req.TopK, req.CallerAgent, req.TrustPeers, req.AcceptTags, req.MaxAge, req.MinKind)
		if err != nil {
			log.Printf("[search] error: %v", err)
			memObs.recordRead(r.Context(), "search", "error", req.CallerAgent)
			writeJSON(w, http.StatusInternalServerError, apiResponse{Error: err.Error()})
			return
		}
		log.Printf("[search] returned %d result(s)", len(results))
		memObs.recordRead(r.Context(), "search", "ok", req.CallerAgent)
		writeJSON(w, http.StatusOK, apiResponse{Success: true, Content: results})
	}
}

func storeHandler(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Content     string         `json:"content"`
			Tags        []string       `json:"tags"`
			Visibility  string         `json:"visibility"`
			SourceAgent string         `json:"source_agent"`
			ParentID    int64          `json:"parent_id"`
			Evidence    *EvidenceTrace `json:"evidence"`
		}
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			log.Printf("[store] bad request: %v", err)
			writeJSON(w, http.StatusBadRequest, apiResponse{Error: "invalid JSON body"})
			return
		}
		if req.Content == "" {
			log.Printf("[store] rejected: empty content")
			writeJSON(w, http.StatusBadRequest, apiResponse{Error: "'content' is required"})
			return
		}
		if req.Visibility == "" {
			req.Visibility = "public"
		}

		log.Printf("[store] content=%d bytes tags=%v visibility=%s source=%s parent=%d",
			len(req.Content), req.Tags, req.Visibility, req.SourceAgent, req.ParentID)
		id, seq, storedAt, err := storeMemory(db, req.Content, req.Tags, req.Visibility, req.SourceAgent, req.ParentID, req.Evidence)
		if err != nil {
			log.Printf("[store] error: %v", err)
			memObs.recordWrite(r.Context(), "error", req.SourceAgent)
			writeJSON(w, http.StatusInternalServerError, apiResponse{Error: err.Error()})
			return
		}
		log.Printf("[store] saved id=%d seq=%d at=%s", id, seq, storedAt)
		memObs.recordWrite(r.Context(), "ok", req.SourceAgent)
		writeJSON(w, http.StatusOK, apiResponse{
			Success: true,
			Content: map[string]any{"id": id, "seq": seq, "stored_at": storedAt},
		})
	}
}

func listHandler(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		tags := r.URL.Query().Get("tags")
		callerAgent := r.URL.Query().Get("caller_agent")
		trustPeersStr := r.URL.Query().Get("trust_peers")
		maxAge := r.URL.Query().Get("max_age")
		minKind := r.URL.Query().Get("min_kind")
		sourceAgent := r.URL.Query().Get("source_agent")
		limit := 20
		if l, err := strconv.Atoi(r.URL.Query().Get("limit")); err == nil && l > 0 {
			limit = l
		}

		var trustPeers []string
		if trustPeersStr != "" {
			trustPeers = strings.Split(trustPeersStr, ",")
		}

		log.Printf("[list] tags=%q caller=%s limit=%d", tags, callerAgent, limit)
		results, err := listMemories(db, tags, limit, callerAgent, trustPeers, maxAge, minKind, sourceAgent)
		if err != nil {
			log.Printf("[list] error: %v", err)
			memObs.recordRead(r.Context(), "list", "error", callerAgent)
			writeJSON(w, http.StatusInternalServerError, apiResponse{Error: err.Error()})
			return
		}
		log.Printf("[list] returned %d entry/entries", len(results))
		memObs.recordRead(r.Context(), "list", "ok", callerAgent)
		writeJSON(w, http.StatusOK, apiResponse{Success: true, Content: results})
	}
}

func statsHandler(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		type agentStats struct {
			SourceAgent string `json:"source_agent"`
			Visibility  string `json:"visibility"`
			Count       int    `json:"count"`
		}
		rows, err := db.Query(`
			SELECT COALESCE(source_agent, '') as source_agent,
			       COALESCE(visibility, 'public') as visibility,
			       COUNT(*) as count
			FROM memories
			GROUP BY source_agent, visibility
			ORDER BY count DESC
		`)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, apiResponse{Error: err.Error()})
			return
		}
		defer rows.Close()

		var stats []agentStats
		for rows.Next() {
			var s agentStats
			if err := rows.Scan(&s.SourceAgent, &s.Visibility, &s.Count); err != nil {
				continue
			}
			stats = append(stats, s)
		}
		if stats == nil {
			stats = []agentStats{}
		}

		var maxSeq int64
		_ = db.QueryRow(`SELECT COALESCE(MAX(seq), 0) FROM memories`).Scan(&maxSeq)

		writeJSON(w, http.StatusOK, apiResponse{
			Success: true,
			Content: map[string]any{
				"by_agent_visibility": stats,
				"max_seq":             maxSeq,
			},
		})
	}
}

func provenanceHandler(db *sql.DB) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		idStr := r.URL.Query().Get("id")
		if idStr == "" {
			writeJSON(w, http.StatusBadRequest, apiResponse{Error: "'id' query parameter is required"})
			return
		}
		id, err := strconv.ParseInt(idStr, 10, 64)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, apiResponse{Error: "invalid 'id' parameter"})
			return
		}

		chain, err := getProvenanceChain(db, id)
		if err != nil {
			memObs.recordRead(r.Context(), "provenance", "error", r.URL.Query().Get("caller_agent"))
			writeJSON(w, http.StatusInternalServerError, apiResponse{Error: err.Error()})
			return
		}
		memObs.recordRead(r.Context(), "provenance", "ok", r.URL.Query().Get("caller_agent"))
		writeJSON(w, http.StatusOK, apiResponse{Success: true, Content: chain})
	}
}

// deleteHandler removes a single entry by its exact id. It is a manual admin
// backstop (corrupted or sensitive entries) — not wired to agents or skills.
// Access is gated on a bearer token injected only into this pod's env; when the
// token is unset the endpoint is disabled, so existing deployments are
// unaffected. The delete is a hard row removal; the memories_ad FTS trigger
// keeps memories_fts in sync automatically.
func deleteHandler(db *sql.DB, adminToken string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if adminToken == "" {
			log.Printf("[delete] rejected: MEMORY_ADMIN_TOKEN not configured")
			writeJSON(w, http.StatusForbidden, apiResponse{Error: "delete is disabled: MEMORY_ADMIN_TOKEN is not configured"})
			return
		}
		token := ""
		if auth := r.Header.Get("Authorization"); strings.HasPrefix(auth, "Bearer ") {
			token = strings.TrimPrefix(auth, "Bearer ")
		}
		if subtle.ConstantTimeCompare([]byte(token), []byte(adminToken)) != 1 {
			log.Printf("[delete] rejected: invalid or missing bearer token")
			writeJSON(w, http.StatusUnauthorized, apiResponse{Error: "unauthorized"})
			return
		}

		idStr := r.URL.Query().Get("id")
		if idStr == "" {
			writeJSON(w, http.StatusBadRequest, apiResponse{Error: "'id' query parameter is required"})
			return
		}
		id, err := strconv.ParseInt(idStr, 10, 64)
		if err != nil {
			writeJSON(w, http.StatusBadRequest, apiResponse{Error: "invalid 'id' parameter"})
			return
		}

		entry, found, err := getMemoryByID(db, id)
		if err != nil {
			log.Printf("[delete] lookup error: %v", err)
			memObs.recordWrite(r.Context(), "error", "")
			writeJSON(w, http.StatusInternalServerError, apiResponse{Error: err.Error()})
			return
		}
		if !found {
			writeJSON(w, http.StatusNotFound, apiResponse{Error: fmt.Sprintf("no memory entry with id %d", id)})
			return
		}

		if err := deleteMemory(db, id); err != nil {
			log.Printf("[delete] error: %v", err)
			memObs.recordWrite(r.Context(), "error", entry.SourceAgent)
			writeJSON(w, http.StatusInternalServerError, apiResponse{Error: err.Error()})
			return
		}
		log.Printf("[delete] removed id=%d seq=%d source=%s", entry.ID, entry.Seq, entry.SourceAgent)
		memObs.recordWrite(r.Context(), "delete", entry.SourceAgent)
		writeJSON(w, http.StatusOK, apiResponse{Success: true, Content: map[string]any{"deleted": entry}})
	}
}

// --- Core database operations ---

func searchMemories(db *sql.DB, query string, topK int, callerAgent string, trustPeers, acceptTags []string, maxAge, minKind string) ([]memoryEntry, error) {
	// Build visibility filter.
	visFilter, visArgs := buildVisibilityFilter(callerAgent, trustPeers)

	// Build time decay filter.
	ageFilter, ageArgs := buildTimeDecayFilter(maxAge)

	// Build accept tags filter.
	tagFilter, tagArgs := buildAcceptTagsFilter(acceptTags)

	// Build min kind filter.
	kindFilter, kindArgs := buildMinKindFilter(minKind)

	// FTS5 search with ranking + membrane filters.
	allArgs := []any{fts5Query(query)}
	allArgs = append(allArgs, visArgs...)
	allArgs = append(allArgs, ageArgs...)
	allArgs = append(allArgs, tagArgs...)
	allArgs = append(allArgs, kindArgs...)
	allArgs = append(allArgs, topK)

	q := fmt.Sprintf(`
		SELECT m.id, m.content, m.tags, m.visibility, m.source_agent, m.parent_id, m.seq, m.created_at, m.updated_at, m.evidence
		FROM memories_fts fts
		JOIN memories m ON m.id = fts.rowid
		WHERE memories_fts MATCH ?
		%s %s %s %s
		ORDER BY rank
		LIMIT ?
	`, visFilter, ageFilter, tagFilter, kindFilter)

	rows, err := db.Query(q, allArgs...)
	if err != nil {
		// Fallback to LIKE search with same membrane filters.
		allArgs = []any{"%" + query + "%"}
		allArgs = append(allArgs, visArgs...)
		allArgs = append(allArgs, ageArgs...)
		allArgs = append(allArgs, tagArgs...)
		allArgs = append(allArgs, kindArgs...)
		allArgs = append(allArgs, topK)

		q = fmt.Sprintf(`
			SELECT id, content, tags, visibility, source_agent, parent_id, seq, created_at, updated_at, evidence
			FROM memories
			WHERE content LIKE ?
			%s %s %s %s
			ORDER BY updated_at DESC
			LIMIT ?
		`, visFilter, ageFilter, tagFilter, kindFilter)

		rows, err = db.Query(q, allArgs...)
		if err != nil {
			return nil, fmt.Errorf("search failed: %w", err)
		}
	}
	defer rows.Close()
	return scanEntries(rows)
}

func storeMemory(db *sql.DB, content string, tags []string, visibility, sourceAgent string, parentID int64, evidence *EvidenceTrace) (int64, int64, string, error) {
	tagsStr := strings.Join(tags, ",")
	now := time.Now().UTC().Format(time.RFC3339)

	var evidenceStr string
	if evidence != nil {
		b, err := json.Marshal(evidence)
		if err != nil {
			return 0, 0, "", fmt.Errorf("marshal evidence: %w", err)
		}
		evidenceStr = string(b)
	}

	tx, err := db.Begin()
	if err != nil {
		return 0, 0, "", fmt.Errorf("store begin tx: %w", err)
	}
	defer tx.Rollback()

	// Get next monotonic sequence number.
	var nextSeq int64
	_ = tx.QueryRow(`SELECT COALESCE(MAX(seq), 0) + 1 FROM memories`).Scan(&nextSeq)

	result, err := tx.Exec(`
		INSERT INTO memories (content, tags, visibility, source_agent, parent_id, seq, created_at, updated_at, evidence)
		VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, content, tagsStr, visibility, sourceAgent, parentID, nextSeq, now, now, evidenceStr)
	if err != nil {
		return 0, 0, "", fmt.Errorf("store failed: %w", err)
	}
	id, _ := result.LastInsertId()

	if err := tx.Commit(); err != nil {
		return 0, 0, "", fmt.Errorf("store commit: %w", err)
	}
	return id, nextSeq, now, nil
}

func listMemories(db *sql.DB, tags string, limit int, callerAgent string, trustPeers []string, maxAge, minKind, sourceAgent string) ([]memoryEntry, error) {
	visFilter, visArgs := buildVisibilityFilter(callerAgent, trustPeers)
	ageFilter, ageArgs := buildTimeDecayFilter(maxAge)
	kindFilter, kindArgs := buildMinKindFilter(minKind)

	var conditions []string
	var args []any

	if tags != "" {
		conditions = append(conditions, "tags LIKE ?")
		args = append(args, "%"+tags+"%")
	}
	if sourceAgent != "" {
		conditions = append(conditions, "source_agent = ?")
		args = append(args, sourceAgent)
	}

	args = append(args, visArgs...)
	args = append(args, ageArgs...)
	args = append(args, kindArgs...)

	where := ""
	if len(conditions) > 0 {
		where = "WHERE " + strings.Join(conditions, " AND ")
	}
	// visFilter, ageFilter, and kindFilter already include "AND" prefix when non-empty.
	if where == "" && (visFilter != "" || ageFilter != "" || kindFilter != "") {
		where = "WHERE 1=1"
	}

	args = append(args, limit)

	q := fmt.Sprintf(`
		SELECT id, content, tags, visibility, source_agent, parent_id, seq, created_at, updated_at, evidence
		FROM memories
		%s %s %s %s
		ORDER BY updated_at DESC
		LIMIT ?
	`, where, visFilter, ageFilter, kindFilter)

	rows, err := db.Query(q, args...)
	if err != nil {
		return nil, fmt.Errorf("list failed: %w", err)
	}
	defer rows.Close()
	return scanEntries(rows)
}

func getProvenanceChain(db *sql.DB, id int64) ([]memoryEntry, error) {
	var chain []memoryEntry
	seen := map[int64]bool{}
	current := id

	for current > 0 && !seen[current] {
		seen[current] = true
		row := db.QueryRow(`
			SELECT id, content, tags, visibility, source_agent, parent_id, seq, created_at, updated_at, evidence
			FROM memories WHERE id = ?
		`, current)

		var e memoryEntry
		var tags string
		var evidenceStr string
		err := row.Scan(&e.ID, &e.Content, &tags, &e.Visibility, &e.SourceAgent, &e.ParentID, &e.Seq, &e.CreatedAt, &e.UpdatedAt, &evidenceStr)
		if err != nil {
			break
		}
		if tags != "" {
			e.Tags = strings.Split(tags, ",")
		}
		if evidenceStr != "" {
			var ev EvidenceTrace
			if err := json.Unmarshal([]byte(evidenceStr), &ev); err == nil {
				e.Evidence = &ev
			}
		}
		chain = append(chain, e)
		current = e.ParentID
	}

	// Reverse so root is first.
	for i, j := 0, len(chain)-1; i < j; i, j = i+1, j-1 {
		chain[i], chain[j] = chain[j], chain[i]
	}
	if chain == nil {
		chain = []memoryEntry{}
	}
	return chain, nil
}

// getMemoryByID fetches a single entry by its exact primary key. found is false
// (with a nil error) when no such row exists.
func getMemoryByID(db *sql.DB, id int64) (memoryEntry, bool, error) {
	row := db.QueryRow(`
		SELECT id, content, tags, visibility, source_agent, parent_id, seq, created_at, updated_at, evidence
		FROM memories WHERE id = ?
	`, id)

	var e memoryEntry
	var tags string
	var evidenceStr string
	err := row.Scan(&e.ID, &e.Content, &tags, &e.Visibility, &e.SourceAgent, &e.ParentID, &e.Seq, &e.CreatedAt, &e.UpdatedAt, &evidenceStr)
	if err == sql.ErrNoRows {
		return memoryEntry{}, false, nil
	}
	if err != nil {
		return memoryEntry{}, false, fmt.Errorf("lookup by id: %w", err)
	}
	if tags != "" {
		e.Tags = strings.Split(tags, ",")
	}
	if evidenceStr != "" {
		var ev EvidenceTrace
		if err := json.Unmarshal([]byte(evidenceStr), &ev); err == nil {
			e.Evidence = &ev
		}
	}
	return e, true, nil
}

// deleteMemory hard-deletes the row with the given id. The memories_ad FTS
// trigger removes the matching FTS entry, so search/list stop returning it.
func deleteMemory(db *sql.DB, id int64) error {
	if _, err := db.Exec(`DELETE FROM memories WHERE id = ?`, id); err != nil {
		return fmt.Errorf("delete failed: %w", err)
	}
	return nil
}

func scanEntries(rows *sql.Rows) ([]memoryEntry, error) {
	var results []memoryEntry
	for rows.Next() {
		var e memoryEntry
		var tags string
		var evidenceStr string
		if err := rows.Scan(&e.ID, &e.Content, &tags, &e.Visibility, &e.SourceAgent, &e.ParentID, &e.Seq, &e.CreatedAt, &e.UpdatedAt, &evidenceStr); err != nil {
			continue
		}
		if tags != "" {
			e.Tags = strings.Split(tags, ",")
		}
		if evidenceStr != "" {
			var ev EvidenceTrace
			if err := json.Unmarshal([]byte(evidenceStr), &ev); err == nil {
				e.Evidence = &ev
			}
		}
		results = append(results, e)
	}
	if results == nil {
		results = []memoryEntry{}
	}
	return results, nil
}

// --- Membrane filter builders ---

// buildVisibilityFilter returns an SQL fragment and args that enforce
// three-tier permeability. If callerAgent is empty, no filtering is applied
// (backward-compatible with pre-membrane deployments).
func buildVisibilityFilter(callerAgent string, trustPeers []string) (string, []any) {
	if callerAgent == "" {
		return "", nil
	}

	// Caller can always see:
	// 1. Public entries
	// 2. Trusted entries from their trust peers
	// 3. Their own entries (any visibility)
	peers := append(trustPeers, callerAgent)
	placeholders := make([]string, len(peers))
	args := make([]any, 0, len(peers)+1)
	for i, p := range peers {
		placeholders[i] = "?"
		args = append(args, p)
	}
	args = append(args, callerAgent)

	filter := fmt.Sprintf(
		`AND (visibility = 'public' OR (visibility = 'trusted' AND source_agent IN (%s)) OR source_agent = ?)`,
		strings.Join(placeholders, ","),
	)
	return filter, args
}

// buildTimeDecayFilter returns an SQL fragment that excludes entries older
// than maxAge. maxAge should be a Go duration string (e.g., "24h", "168h").
func buildTimeDecayFilter(maxAge string) (string, []any) {
	if maxAge == "" {
		return "", nil
	}
	d, err := time.ParseDuration(maxAge)
	if err != nil {
		return "", nil
	}
	cutoff := time.Now().UTC().Add(-d).Format(time.RFC3339)
	return "AND created_at > ?", []any{cutoff}
}

// buildAcceptTagsFilter returns an SQL fragment that filters entries to only
// those with at least one matching tag from acceptTags.
func buildAcceptTagsFilter(acceptTags []string) (string, []any) {
	if len(acceptTags) == 0 {
		return "", nil
	}
	conditions := make([]string, len(acceptTags))
	args := make([]any, len(acceptTags))
	for i, tag := range acceptTags {
		conditions[i] = "tags LIKE ?"
		args[i] = "%" + tag + "%"
	}
	return "AND (" + strings.Join(conditions, " OR ") + ")", args
}

// --- Schema ---

// initSchema creates the memories table and FTS5 virtual table.
func initSchema(db *sql.DB) error {
	_, err := db.Exec(`
		CREATE TABLE IF NOT EXISTS memories (
			id         INTEGER PRIMARY KEY AUTOINCREMENT,
			content    TEXT NOT NULL,
			tags       TEXT DEFAULT '',
			created_at TEXT NOT NULL,
			updated_at TEXT NOT NULL
		);

		CREATE VIRTUAL TABLE IF NOT EXISTS memories_fts USING fts5(
			content,
			content=memories,
			content_rowid=id,
			tokenize='porter unicode61'
		);

		-- Triggers to keep FTS index in sync.
		CREATE TRIGGER IF NOT EXISTS memories_ai AFTER INSERT ON memories BEGIN
			INSERT INTO memories_fts(rowid, content) VALUES (new.id, new.content);
		END;

		CREATE TRIGGER IF NOT EXISTS memories_ad AFTER DELETE ON memories BEGIN
			INSERT INTO memories_fts(memories_fts, rowid, content) VALUES('delete', old.id, old.content);
		END;

		CREATE TRIGGER IF NOT EXISTS memories_au AFTER UPDATE ON memories BEGIN
			INSERT INTO memories_fts(memories_fts, rowid, content) VALUES('delete', old.id, old.content);
			INSERT INTO memories_fts(rowid, content) VALUES (new.id, new.content);
		END;

		CREATE INDEX IF NOT EXISTS idx_memories_updated ON memories(updated_at DESC);
		CREATE INDEX IF NOT EXISTS idx_memories_tags ON memories(tags);
	`)
	return err
}

// migrateMembraneColumns adds membrane columns to an existing database.
// Safe to call on fresh databases (columns won't exist yet) and idempotent
// on already-migrated databases.
func migrateMembraneColumns(db *sql.DB) error {
	var count int
	err := db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('memories') WHERE name='visibility'`).Scan(&count)
	if err != nil {
		return fmt.Errorf("membrane migration check: %w", err)
	}
	if count > 0 {
		return nil // already migrated
	}

	log.Printf("[memory-server] running membrane schema migration")
	for _, stmt := range []string{
		`ALTER TABLE memories ADD COLUMN visibility TEXT DEFAULT 'public'`,
		`ALTER TABLE memories ADD COLUMN source_agent TEXT DEFAULT ''`,
		`ALTER TABLE memories ADD COLUMN parent_id INTEGER DEFAULT 0`,
		`ALTER TABLE memories ADD COLUMN seq INTEGER DEFAULT 0`,
	} {
		if _, err := db.Exec(stmt); err != nil {
			return fmt.Errorf("membrane migration: %w", err)
		}
	}

	// Create indexes for membrane columns.
	for _, stmt := range []string{
		`CREATE INDEX IF NOT EXISTS idx_memories_visibility ON memories(visibility)`,
		`CREATE INDEX IF NOT EXISTS idx_memories_source_agent ON memories(source_agent)`,
		`CREATE INDEX IF NOT EXISTS idx_memories_seq ON memories(seq DESC)`,
	} {
		if _, err := db.Exec(stmt); err != nil {
			log.Printf("[memory-server] warning: index creation: %v", err)
		}
	}
	log.Printf("[memory-server] membrane migration complete")
	return nil
}

// migrateEvidenceColumn adds the evidence column to an existing database.
// Idempotent: safe to call on fresh or already-migrated databases.
func migrateEvidenceColumn(db *sql.DB) error {
	var count int
	err := db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('memories') WHERE name='evidence'`).Scan(&count)
	if err != nil {
		return fmt.Errorf("evidence migration check: %w", err)
	}
	if count > 0 {
		return nil // already migrated
	}

	log.Printf("[memory-server] running evidence schema migration")
	if _, err := db.Exec(`ALTER TABLE memories ADD COLUMN evidence TEXT DEFAULT ''`); err != nil {
		return fmt.Errorf("evidence migration: %w", err)
	}
	log.Printf("[memory-server] evidence migration complete")
	return nil
}

// evidenceKindRank maps evidence kinds to quality ranks for filtering.
var evidenceKindRank = map[string]int{
	"tool_result":        4,
	"external_source":    3,
	"llm_interpretation": 2,
	"agent_opinion":      1,
}

func buildMinKindFilter(minKind string) (string, []any) {
	if minKind == "" {
		return "", nil
	}
	rank, ok := evidenceKindRank[minKind]
	if !ok {
		return "", nil
	}
	// Build SQL that checks the JSON 'kind' field in the evidence column.
	// Entries with no evidence pass through (backward compatible).
	var kinds []string
	for k, r := range evidenceKindRank {
		if r >= rank {
			kinds = append(kinds, k)
		}
	}
	conditions := make([]string, len(kinds))
	args := make([]any, len(kinds))
	for i, k := range kinds {
		conditions[i] = "json_extract(evidence, '$.kind') = ?"
		args[i] = k
	}
	return fmt.Sprintf("AND (evidence = '' OR %s)", strings.Join(conditions, " OR ")), args
}

// fts5Query converts a natural language query into an FTS5 query.
// Each word becomes a prefix search term joined with AND.
func fts5Query(query string) string {
	words := strings.Fields(query)
	if len(words) == 0 {
		return query
	}
	terms := make([]string, 0, len(words))
	for _, w := range words {
		// Strip special FTS5 characters to prevent syntax errors.
		w = strings.Map(func(r rune) rune {
			if r == '"' || r == '*' || r == '+' || r == '-' || r == '(' || r == ')' || r == ':' || r == '^' {
				return -1
			}
			return r
		}, w)
		if w != "" {
			terms = append(terms, w+"*")
		}
	}
	if len(terms) == 0 {
		return query
	}
	return strings.Join(terms, " AND ")
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	json.NewEncoder(w).Encode(v)
}

func truncateLog(s string, n int) string {
	if len(s) <= n {
		return s
	}
	return s[:n] + "..."
}

func envOr(key, fallback string) string {
	if v := os.Getenv(key); v != "" {
		return v
	}
	return fallback
}
