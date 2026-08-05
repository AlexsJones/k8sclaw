package main

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"testing"

	_ "modernc.org/sqlite"
)

func openTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db, err := sql.Open("sqlite", ":memory:")
	if err != nil {
		t.Fatalf("open test db: %v", err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

func seedTestDB(t *testing.T, db *sql.DB) {
	t.Helper()
	if err := initSchema(db); err != nil {
		t.Fatalf("initSchema: %v", err)
	}
	if err := migrateMembraneColumns(db); err != nil {
		t.Fatalf("migrateMembraneColumns: %v", err)
	}
	if err := migrateEvidenceColumn(db); err != nil {
		t.Fatalf("migrateEvidenceColumn: %v", err)
	}
}

// setupTestDB is an alias that includes all migrations (used by evidence tests).
func setupTestDB(t *testing.T) *sql.DB {
	t.Helper()
	db := openTestDB(t)
	seedTestDB(t, db)
	return db
}

// helper to store with defaults for backward-compat tests.
func storeDefault(t *testing.T, db *sql.DB, content string, tags []string) int64 {
	t.Helper()
	id, _, _, err := storeMemory(db, content, tags, "public", "", 0, nil)
	if err != nil {
		t.Fatalf("storeMemory: %v", err)
	}
	return id
}

// ── initSchema tests ─────────────────────────────────────────────────────────

func TestInitSchema(t *testing.T) {
	db := openTestDB(t)
	if err := initSchema(db); err != nil {
		t.Fatalf("initSchema: %v", err)
	}

	// Verify the memories table exists.
	var tableName string
	err := db.QueryRow(`SELECT name FROM sqlite_master WHERE type='table' AND name='memories'`).Scan(&tableName)
	if err != nil {
		t.Fatalf("memories table not found: %v", err)
	}

	// Verify the FTS5 virtual table exists.
	err = db.QueryRow(`SELECT name FROM sqlite_master WHERE type='table' AND name='memories_fts'`).Scan(&tableName)
	if err != nil {
		t.Fatalf("memories_fts virtual table not found: %v", err)
	}

	// Verify idempotency -- calling again should not fail.
	if err := initSchema(db); err != nil {
		t.Fatalf("initSchema (idempotent): %v", err)
	}
}

// ── Membrane migration tests ────────────────────────────────────────────────

func TestMigrateMembraneColumns(t *testing.T) {
	db := openTestDB(t)
	if err := initSchema(db); err != nil {
		t.Fatalf("initSchema: %v", err)
	}

	if err := migrateMembraneColumns(db); err != nil {
		t.Fatalf("first migration: %v", err)
	}

	// Verify columns exist.
	for _, col := range []string{"visibility", "source_agent", "parent_id", "seq"} {
		var count int
		err := db.QueryRow(`SELECT COUNT(*) FROM pragma_table_info('memories') WHERE name=?`, col).Scan(&count)
		if err != nil || count != 1 {
			t.Errorf("column %q missing after migration", col)
		}
	}
}

func TestMigrateMembraneColumns_Idempotent(t *testing.T) {
	db := openTestDB(t)
	if err := initSchema(db); err != nil {
		t.Fatalf("initSchema: %v", err)
	}
	if err := migrateMembraneColumns(db); err != nil {
		t.Fatalf("first migration: %v", err)
	}
	if err := migrateMembraneColumns(db); err != nil {
		t.Fatalf("second migration should be idempotent: %v", err)
	}
}

// ── Evidence migration tests ─────────────────────────────────────────────────

func TestMigrateEvidenceColumn_Idempotent(t *testing.T) {
	db := setupTestDB(t)
	// Second call should be no-op
	if err := migrateEvidenceColumn(db); err != nil {
		t.Fatalf("second migration: %v", err)
	}
}

// ── Core database function tests ─────────────────────────────────────────────

func TestStoreMemory(t *testing.T) {
	db := setupTestDB(t)

	id, seq, storedAt, err := storeMemory(db, "Kafka consumer lag detected", []string{"kafka", "payments"}, "public", "researcher", 0, nil)
	if err != nil {
		t.Fatalf("storeMemory: %v", err)
	}
	if id <= 0 {
		t.Errorf("expected positive id, got %d", id)
	}
	if seq <= 0 {
		t.Errorf("expected positive seq, got %d", seq)
	}
	if storedAt == "" {
		t.Error("expected non-empty stored_at")
	}
}

func TestStoreMemory_WithVisibility(t *testing.T) {
	db := setupTestDB(t)

	for _, vis := range []string{"public", "trusted", "private"} {
		id, _, _, err := storeMemory(db, "entry-"+vis, nil, vis, "agent-a", 0, nil)
		if err != nil {
			t.Fatalf("storeMemory(%s): %v", vis, err)
		}

		var got string
		err = db.QueryRow(`SELECT visibility FROM memories WHERE id = ?`, id).Scan(&got)
		if err != nil {
			t.Fatalf("query visibility: %v", err)
		}
		if got != vis {
			t.Errorf("visibility = %q, want %q", got, vis)
		}
	}
}

func TestStoreMemory_SequenceNumbers(t *testing.T) {
	db := setupTestDB(t)

	_, seq1, _, _ := storeMemory(db, "first", nil, "public", "", 0, nil)
	_, seq2, _, _ := storeMemory(db, "second", nil, "public", "", 0, nil)
	_, seq3, _, _ := storeMemory(db, "third", nil, "public", "", 0, nil)

	if seq2 != seq1+1 || seq3 != seq2+1 {
		t.Errorf("expected monotonic seq: %d, %d, %d", seq1, seq2, seq3)
	}
}

func TestStoreMemory_WithEvidence(t *testing.T) {
	db := setupTestDB(t)
	ev := &EvidenceTrace{
		Kind:       "tool_result",
		ToolCall:   "web_search(query='kubernetes')",
		RawResult:  "Found 10 results",
		Source:     "https://k8s.io",
		Confidence: 0.9,
	}
	id, seq, _, err := storeMemory(db, "k8s findings", []string{"k8s"}, "public", "researcher", 0, ev)
	if err != nil {
		t.Fatalf("store: %v", err)
	}
	if id <= 0 || seq <= 0 {
		t.Fatalf("expected positive id/seq, got id=%d seq=%d", id, seq)
	}

	// Verify evidence was stored
	chain, err := getProvenanceChain(db, id)
	if err != nil {
		t.Fatalf("provenance: %v", err)
	}
	if len(chain) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(chain))
	}
	if chain[0].Evidence == nil {
		t.Fatal("expected evidence to be set")
	}
	if chain[0].Evidence.Kind != "tool_result" {
		t.Errorf("kind = %q, want tool_result", chain[0].Evidence.Kind)
	}
	if chain[0].Evidence.Confidence != 0.9 {
		t.Errorf("confidence = %f, want 0.9", chain[0].Evidence.Confidence)
	}
}

func TestStoreMemory_WithoutEvidence(t *testing.T) {
	db := setupTestDB(t)
	id, _, _, err := storeMemory(db, "no evidence entry", nil, "public", "agent", 0, nil)
	if err != nil {
		t.Fatalf("store: %v", err)
	}

	chain, _ := getProvenanceChain(db, id)
	if len(chain) != 1 {
		t.Fatalf("expected 1 entry, got %d", len(chain))
	}
	if chain[0].Evidence != nil {
		t.Error("expected nil evidence for entry stored without evidence")
	}
}

func TestStoreMemory_EvidenceDerivedFrom(t *testing.T) {
	db := setupTestDB(t)
	// Store parent
	parentID, _, _, _ := storeMemory(db, "parent finding", nil, "public", "a", 0, &EvidenceTrace{Kind: "tool_result"})
	// Store child with derived_from
	childID, _, _, _ := storeMemory(db, "derived finding", nil, "public", "b", parentID, &EvidenceTrace{
		Kind:        "llm_interpretation",
		DerivedFrom: []int64{parentID},
		Confidence:  0.6,
	})

	chain, _ := getProvenanceChain(db, childID)
	if len(chain) != 2 {
		t.Fatalf("expected 2 entries in provenance chain, got %d", len(chain))
	}
	// Root first
	if chain[0].ID != parentID {
		t.Errorf("first in chain should be parent, got id=%d", chain[0].ID)
	}
	if chain[1].Evidence.DerivedFrom[0] != parentID {
		t.Errorf("derived_from should reference parent")
	}
}

// ── buildMinKindFilter tests ─────────────────────────────────────────────────

func TestBuildMinKindFilter_NoFilter(t *testing.T) {
	sql, args := buildMinKindFilter("")
	if sql != "" || args != nil {
		t.Errorf("empty minKind should produce no filter, got sql=%q args=%v", sql, args)
	}
}

func TestBuildMinKindFilter_InvalidKind(t *testing.T) {
	sql, args := buildMinKindFilter("invalid")
	if sql != "" || args != nil {
		t.Errorf("invalid minKind should produce no filter")
	}
}

func TestBuildMinKindFilter_ToolResult(t *testing.T) {
	sql, args := buildMinKindFilter("tool_result")
	if sql == "" {
		t.Fatal("expected filter for tool_result")
	}
	// Only tool_result should pass (rank 4, only rank >= 4)
	if len(args) != 1 {
		t.Errorf("expected 1 arg for tool_result filter, got %d", len(args))
	}
}

func TestBuildMinKindFilter_ExternalSource(t *testing.T) {
	_, args := buildMinKindFilter("external_source")
	// external_source (3) and tool_result (4) should pass
	if len(args) != 2 {
		t.Errorf("expected 2 args for external_source filter, got %d", len(args))
	}
}

func TestBuildMinKindFilter_AgentOpinion(t *testing.T) {
	_, args := buildMinKindFilter("agent_opinion")
	// All 4 kinds should pass
	if len(args) != 4 {
		t.Errorf("expected 4 args for agent_opinion filter, got %d", len(args))
	}
}

// ── Search with min_kind filter ──────────────────────────────────────────────

func TestSearchMemories(t *testing.T) {
	db := setupTestDB(t)

	storeDefault(t, db, "Kafka consumer lag detected in payments namespace", []string{"kafka"})
	storeDefault(t, db, "OOM crash in checkout service", []string{"oom"})
	storeDefault(t, db, "Deployment rollback completed for auth service", nil)

	results, err := searchMemories(db, "kafka consumer", 5, "", nil, nil, "", "")
	if err != nil {
		t.Fatalf("searchMemories: %v", err)
	}
	if len(results) == 0 {
		t.Fatal("expected at least 1 search result")
	}
	if results[0].Content != "Kafka consumer lag detected in payments namespace" {
		t.Errorf("first result content = %q", results[0].Content)
	}
}

func TestSearchMemories_VisibilityFilter(t *testing.T) {
	db := setupTestDB(t)

	storeMemory(db, "kafka public finding", nil, "public", "agent-a", 0, nil)
	storeMemory(db, "kafka trusted secret", nil, "trusted", "agent-a", 0, nil)
	storeMemory(db, "kafka private note", nil, "private", "agent-a", 0, nil)

	// agent-b with no trust relationship should only see public
	results, err := searchMemories(db, "kafka", 10, "agent-b", nil, nil, "", "")
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result (public only), got %d", len(results))
	}
	if results[0].Content != "kafka public finding" {
		t.Errorf("expected kafka public finding, got %q", results[0].Content)
	}
}

func TestSearchMemories_TrustPeers(t *testing.T) {
	db := setupTestDB(t)

	storeMemory(db, "kafka public finding", nil, "public", "agent-a", 0, nil)
	storeMemory(db, "kafka trusted secret", nil, "trusted", "agent-a", 0, nil)
	storeMemory(db, "kafka private note", nil, "private", "agent-a", 0, nil)

	// agent-b trusts agent-a -> should see public + trusted
	results, err := searchMemories(db, "kafka", 10, "agent-b", []string{"agent-a"}, nil, "", "")
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(results) != 2 {
		t.Fatalf("expected 2 results (public + trusted), got %d", len(results))
	}
}

func TestSearchMemories_CallerSeesOwnPrivate(t *testing.T) {
	db := setupTestDB(t)

	storeMemory(db, "kafka private note", nil, "private", "agent-a", 0, nil)

	// agent-a should see their own private entries
	results, err := searchMemories(db, "kafka", 10, "agent-a", nil, nil, "", "")
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result (own private), got %d", len(results))
	}
}

func TestSearchMemories_TimeDecay(t *testing.T) {
	db := setupTestDB(t)

	// Store an entry with a very old timestamp.
	_, err := db.Exec(`
		INSERT INTO memories (content, tags, visibility, source_agent, parent_id, seq, created_at, updated_at, evidence)
		VALUES (?, '', 'public', '', 0, 1, '2020-01-01T00:00:00Z', '2020-01-01T00:00:00Z', '')
	`, "ancient entry")
	if err != nil {
		t.Fatalf("insert old entry: %v", err)
	}
	// FTS trigger fires on INSERT, so the entry is indexed.

	storeMemory(db, "recent entry", nil, "public", "", 0, nil)

	// With max_age=24h, only the recent entry should appear.
	results, err := searchMemories(db, "entry", 10, "", nil, nil, "24h", "")
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result (recent only), got %d", len(results))
	}
	if results[0].Content != "recent entry" {
		t.Errorf("expected recent entry, got %q", results[0].Content)
	}
}

func TestSearchMemories_Fallback(t *testing.T) {
	db := setupTestDB(t)

	storeDefault(t, db, "the payments service had an OOM kill event", nil)

	results, err := searchMemories(db, "OOM kill", 5, "", nil, nil, "", "")
	if err != nil {
		t.Fatalf("searchMemories fallback: %v", err)
	}
	if len(results) == 0 {
		t.Fatal("expected LIKE fallback to find the entry")
	}
}

func TestSearchMemories_MinKindFilter(t *testing.T) {
	db := setupTestDB(t)
	// Store entries with different evidence kinds
	storeMemory(db, "tool result finding about kubernetes", []string{"k8s"}, "public", "a", 0, &EvidenceTrace{Kind: "tool_result", Confidence: 0.9})
	storeMemory(db, "agent opinion about kubernetes", []string{"k8s"}, "public", "a", 0, &EvidenceTrace{Kind: "agent_opinion", Confidence: 0.3})
	storeMemory(db, "no evidence kubernetes entry", []string{"k8s"}, "public", "a", 0, nil)

	// Search without filter - should get all 3
	results, err := searchMemories(db, "kubernetes", 10, "", nil, nil, "", "")
	if err != nil {
		t.Fatalf("search: %v", err)
	}
	if len(results) != 3 {
		t.Errorf("no filter: expected 3 results, got %d", len(results))
	}

	// Search with min_kind=tool_result - should get tool_result + no-evidence (backward compat)
	results, err = searchMemories(db, "kubernetes", 10, "", nil, nil, "", "tool_result")
	if err != nil {
		t.Fatalf("search with min_kind: %v", err)
	}
	if len(results) != 2 {
		t.Errorf("min_kind=tool_result: expected 2 results (tool_result + no-evidence), got %d", len(results))
	}
}

// ── List tests ───────────────────────────────────────────────────────────────

func TestListMemories(t *testing.T) {
	db := setupTestDB(t)

	storeDefault(t, db, "first entry", nil)
	storeDefault(t, db, "second entry", nil)
	storeDefault(t, db, "third entry", nil)

	results, err := listMemories(db, "", 20, "", nil, "", "", "")
	if err != nil {
		t.Fatalf("listMemories: %v", err)
	}
	if len(results) != 3 {
		t.Fatalf("expected 3 entries, got %d", len(results))
	}
}

func TestListMemories_WithTags(t *testing.T) {
	db := setupTestDB(t)

	storeDefault(t, db, "kafka issue", []string{"kafka", "infra"})
	storeDefault(t, db, "redis issue", []string{"redis", "infra"})
	storeDefault(t, db, "code review notes", []string{"review"})

	results, err := listMemories(db, "kafka", 20, "", nil, "", "", "")
	if err != nil {
		t.Fatalf("listMemories with tags: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 entry with kafka tag, got %d", len(results))
	}
	if results[0].Content != "kafka issue" {
		t.Errorf("entry content = %q", results[0].Content)
	}
}

func TestListMemories_SourceAgentFilter(t *testing.T) {
	db := setupTestDB(t)
	storeMemory(db, "from alpha", nil, "public", "alpha", 0, nil)
	storeMemory(db, "from beta", nil, "public", "beta", 0, nil)

	results, err := listMemories(db, "", 10, "", nil, "", "", "alpha")
	if err != nil {
		t.Fatalf("list: %v", err)
	}
	if len(results) != 1 {
		t.Fatalf("expected 1 result for alpha, got %d", len(results))
	}
	if results[0].SourceAgent != "alpha" {
		t.Errorf("source_agent = %q, want alpha", results[0].SourceAgent)
	}
}

func TestListMemories_MinKindFilter(t *testing.T) {
	db := setupTestDB(t)
	storeMemory(db, "external source", nil, "public", "a", 0, &EvidenceTrace{Kind: "external_source"})
	storeMemory(db, "opinion", nil, "public", "a", 0, &EvidenceTrace{Kind: "agent_opinion"})

	results, _ := listMemories(db, "", 10, "", nil, "", "external_source", "")
	// Should get external_source but not agent_opinion
	if len(results) != 1 {
		t.Errorf("min_kind=external_source: expected 1 result, got %d", len(results))
	}
}

// ── Provenance tests ────────────────────────────────────────────────────────

func TestProvenanceChain(t *testing.T) {
	db := setupTestDB(t)

	// Create a chain: root -> child -> grandchild
	rootID, _, _, _ := storeMemory(db, "root finding", nil, "public", "agent-a", 0, nil)
	childID, _, _, _ := storeMemory(db, "derived insight", nil, "public", "agent-b", rootID, nil)
	grandchildID, _, _, _ := storeMemory(db, "final conclusion", nil, "public", "agent-c", childID, nil)

	chain, err := getProvenanceChain(db, grandchildID)
	if err != nil {
		t.Fatalf("getProvenanceChain: %v", err)
	}
	if len(chain) != 3 {
		t.Fatalf("expected chain of 3, got %d", len(chain))
	}
	// Should be root-first order.
	if chain[0].ID != rootID {
		t.Errorf("chain[0].ID = %d, want root %d", chain[0].ID, rootID)
	}
	if chain[1].ID != childID {
		t.Errorf("chain[1].ID = %d, want child %d", chain[1].ID, childID)
	}
	if chain[2].ID != grandchildID {
		t.Errorf("chain[2].ID = %d, want grandchild %d", chain[2].ID, grandchildID)
	}
}

// ── fts5Query tests ──────────────────────────────────────────────────────────

func TestFts5Query(t *testing.T) {
	tests := []struct {
		input string
		want  string
	}{
		{"kafka consumer", "kafka* AND consumer*"},
		{"single", "single*"},
		{"", ""},
		{`special "chars" and (parens)`, "special* AND chars* AND and* AND parens*"},
		{"***", "***"}, // all chars stripped -> empty terms -> returns original
	}

	for _, tt := range tests {
		got := fts5Query(tt.input)
		if got != tt.want {
			t.Errorf("fts5Query(%q) = %q, want %q", tt.input, got, tt.want)
		}
	}
}

// ── HTTP handler tests ───────────────────────────────────────────────────────

func TestHealthHandler(t *testing.T) {
	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/health", nil)

	handler := func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusOK)
		w.Write([]byte("ok"))
	}
	handler(w, r)

	if w.Code != http.StatusOK {
		t.Errorf("status = %d, want 200", w.Code)
	}
	if w.Body.String() != "ok" {
		t.Errorf("body = %q, want ok", w.Body.String())
	}
}

func TestStoreHandler(t *testing.T) {
	db := setupTestDB(t)

	body := `{"content":"Kafka lag in payments","tags":["kafka","payments"]}`
	w := httptest.NewRecorder()
	r := httptest.NewRequest("POST", "/store", bytes.NewBufferString(body))
	r.Header.Set("Content-Type", "application/json")

	storeHandler(db)(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}

	var resp apiResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("parse response: %v", err)
	}
	if !resp.Success {
		t.Fatalf("expected success, got error: %s", resp.Error)
	}

	// Content should contain id, seq, and stored_at.
	contentBytes, _ := json.Marshal(resp.Content)
	var stored map[string]any
	if err := json.Unmarshal(contentBytes, &stored); err != nil {
		t.Fatalf("parse content: %v", err)
	}
	if id, ok := stored["id"].(float64); !ok || id <= 0 {
		t.Errorf("expected positive id, got %v", stored["id"])
	}
	if seq, ok := stored["seq"].(float64); !ok || seq <= 0 {
		t.Errorf("expected positive seq, got %v", stored["seq"])
	}
}

func TestStoreHandler_WithVisibility(t *testing.T) {
	db := setupTestDB(t)

	body := `{"content":"trusted finding","tags":["test"],"visibility":"trusted","source_agent":"researcher"}`
	w := httptest.NewRecorder()
	r := httptest.NewRequest("POST", "/store", bytes.NewBufferString(body))
	r.Header.Set("Content-Type", "application/json")

	storeHandler(db)(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}

	// Verify the stored entry has correct visibility.
	var vis, src string
	err := db.QueryRow(`SELECT visibility, source_agent FROM memories WHERE id = 1`).Scan(&vis, &src)
	if err != nil {
		t.Fatalf("query: %v", err)
	}
	if vis != "trusted" {
		t.Errorf("visibility = %q, want trusted", vis)
	}
	if src != "researcher" {
		t.Errorf("source_agent = %q, want researcher", src)
	}
}

func TestStoreHandler_WithEvidence(t *testing.T) {
	db := setupTestDB(t)
	mux := http.NewServeMux()
	mux.HandleFunc("POST /store", storeHandler(db))

	body, _ := json.Marshal(map[string]any{
		"content":      "test finding",
		"tags":         []string{"test"},
		"visibility":   "public",
		"source_agent": "tester",
		"evidence": map[string]any{
			"kind":       "tool_result",
			"tool_call":  "web_search(q='test')",
			"raw_result": "result data",
			"confidence": 0.85,
		},
	})

	req := httptest.NewRequest("POST", "/store", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("store returned %d: %s", rec.Code, rec.Body.String())
	}

	var resp apiResponse
	json.NewDecoder(rec.Body).Decode(&resp)
	if !resp.Success {
		t.Fatalf("store not successful: %s", resp.Error)
	}
}

func TestStoreHandler_MissingContent(t *testing.T) {
	db := setupTestDB(t)

	body := `{}`
	w := httptest.NewRecorder()
	r := httptest.NewRequest("POST", "/store", bytes.NewBufferString(body))
	r.Header.Set("Content-Type", "application/json")

	storeHandler(db)(w, r)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.Code)
	}
}

func TestStoreHandler_InvalidJSON(t *testing.T) {
	db := setupTestDB(t)

	w := httptest.NewRecorder()
	r := httptest.NewRequest("POST", "/store", bytes.NewBufferString("not json"))
	r.Header.Set("Content-Type", "application/json")

	storeHandler(db)(w, r)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.Code)
	}
}

func TestSearchHandler(t *testing.T) {
	db := setupTestDB(t)

	storeDefault(t, db, "Kafka consumer lag detected in payments namespace", []string{"kafka"})
	storeDefault(t, db, "OOM crash in checkout service", []string{"oom"})

	body := `{"query":"kafka consumer","top_k":5}`
	w := httptest.NewRecorder()
	r := httptest.NewRequest("POST", "/search", bytes.NewBufferString(body))
	r.Header.Set("Content-Type", "application/json")

	searchHandler(db)(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}

	var resp apiResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("parse response: %v", err)
	}
	if !resp.Success {
		t.Fatalf("expected success, got error: %s", resp.Error)
	}

	contentBytes, _ := json.Marshal(resp.Content)
	var entries []memoryEntry
	if err := json.Unmarshal(contentBytes, &entries); err != nil {
		t.Fatalf("parse content: %v", err)
	}
	if len(entries) == 0 {
		t.Fatal("expected at least 1 search result")
	}
	if entries[0].Content != "Kafka consumer lag detected in payments namespace" {
		t.Errorf("first result content = %q", entries[0].Content)
	}
}

func TestSearchHandler_MissingQuery(t *testing.T) {
	db := setupTestDB(t)

	body := `{}`
	w := httptest.NewRecorder()
	r := httptest.NewRequest("POST", "/search", bytes.NewBufferString(body))
	r.Header.Set("Content-Type", "application/json")

	searchHandler(db)(w, r)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.Code)
	}
}

func TestSearchHandler_DefaultTopK(t *testing.T) {
	db := setupTestDB(t)

	storeDefault(t, db, "entry one", nil)

	body := `{"query":"entry"}`
	w := httptest.NewRecorder()
	r := httptest.NewRequest("POST", "/search", bytes.NewBufferString(body))
	r.Header.Set("Content-Type", "application/json")

	searchHandler(db)(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}

	var resp apiResponse
	json.Unmarshal(w.Body.Bytes(), &resp)
	if !resp.Success {
		t.Fatalf("expected success, got error: %s", resp.Error)
	}
}

func TestSearchHandler_WithMinKind(t *testing.T) {
	db := setupTestDB(t)
	// Seed data
	storeMemory(db, "kubernetes tool result", nil, "public", "a", 0, &EvidenceTrace{Kind: "tool_result"})
	storeMemory(db, "kubernetes opinion", nil, "public", "a", 0, &EvidenceTrace{Kind: "agent_opinion"})

	mux := http.NewServeMux()
	mux.HandleFunc("POST /search", searchHandler(db))

	body, _ := json.Marshal(map[string]any{
		"query":    "kubernetes",
		"top_k":    10,
		"min_kind": "tool_result",
	})

	req := httptest.NewRequest("POST", "/search", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("search returned %d: %s", rec.Code, rec.Body.String())
	}

	var resp apiResponse
	json.NewDecoder(rec.Body).Decode(&resp)
	if !resp.Success {
		t.Fatalf("search not successful: %s", resp.Error)
	}

	var entries []memoryEntry
	raw, _ := json.Marshal(resp.Content)
	json.Unmarshal(raw, &entries)

	// Should only get tool_result (opinion filtered out)
	for _, e := range entries {
		if e.Evidence != nil && e.Evidence.Kind == "agent_opinion" {
			t.Error("agent_opinion should be filtered out with min_kind=tool_result")
		}
	}
}

func TestListHandler(t *testing.T) {
	db := setupTestDB(t)

	storeDefault(t, db, "first entry", nil)
	storeDefault(t, db, "second entry", nil)

	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/list", nil)

	listHandler(db)(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}

	var resp apiResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("parse response: %v", err)
	}
	if !resp.Success {
		t.Fatalf("expected success, got error: %s", resp.Error)
	}

	contentBytes, _ := json.Marshal(resp.Content)
	var entries []memoryEntry
	if err := json.Unmarshal(contentBytes, &entries); err != nil {
		t.Fatalf("parse content: %v", err)
	}
	if len(entries) != 2 {
		t.Fatalf("expected 2 entries, got %d", len(entries))
	}
}

func TestListHandler_WithTags(t *testing.T) {
	db := setupTestDB(t)

	storeDefault(t, db, "kafka issue", []string{"kafka", "infra"})
	storeDefault(t, db, "redis issue", []string{"redis", "infra"})

	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/list?tags=kafka", nil)

	listHandler(db)(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}

	var resp apiResponse
	json.Unmarshal(w.Body.Bytes(), &resp)
	contentBytes, _ := json.Marshal(resp.Content)
	var entries []memoryEntry
	json.Unmarshal(contentBytes, &entries)

	if len(entries) != 1 {
		t.Fatalf("expected 1 entry with kafka tag, got %d", len(entries))
	}
	if entries[0].Content != "kafka issue" {
		t.Errorf("entry content = %q", entries[0].Content)
	}
}

func TestListHandler_WithLimit(t *testing.T) {
	db := setupTestDB(t)

	storeDefault(t, db, "entry 1", nil)
	storeDefault(t, db, "entry 2", nil)
	storeDefault(t, db, "entry 3", nil)

	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/list?limit=2", nil)

	listHandler(db)(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}

	var resp apiResponse
	json.Unmarshal(w.Body.Bytes(), &resp)
	contentBytes, _ := json.Marshal(resp.Content)
	var entries []memoryEntry
	json.Unmarshal(contentBytes, &entries)

	if len(entries) != 2 {
		t.Fatalf("expected 2 entries (limit=2), got %d", len(entries))
	}
}

func TestListHandler_SourceAgentParam(t *testing.T) {
	db := setupTestDB(t)
	storeMemory(db, "from alpha", nil, "public", "alpha", 0, nil)
	storeMemory(db, "from beta", nil, "public", "beta", 0, nil)

	mux := http.NewServeMux()
	mux.HandleFunc("GET /list", listHandler(db))

	req := httptest.NewRequest("GET", "/list?source_agent=alpha", nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("list returned %d: %s", rec.Code, rec.Body.String())
	}

	var resp apiResponse
	json.NewDecoder(rec.Body).Decode(&resp)
	var entries []memoryEntry
	raw, _ := json.Marshal(resp.Content)
	json.Unmarshal(raw, &entries)

	if len(entries) != 1 {
		t.Errorf("expected 1 entry for alpha, got %d", len(entries))
	}
}

// ── Stats handler tests ─────────────────────────────────────────────────────

func TestStatsHandler(t *testing.T) {
	db := setupTestDB(t)

	storeMemory(db, "public entry", nil, "public", "agent-a", 0, nil)
	storeMemory(db, "trusted entry", nil, "trusted", "agent-a", 0, nil)
	storeMemory(db, "private entry", nil, "private", "agent-b", 0, nil)

	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/stats", nil)
	statsHandler(db)(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}

	var resp apiResponse
	json.Unmarshal(w.Body.Bytes(), &resp)
	if !resp.Success {
		t.Fatalf("expected success, got error: %s", resp.Error)
	}

	contentBytes, _ := json.Marshal(resp.Content)
	var stats map[string]any
	json.Unmarshal(contentBytes, &stats)

	maxSeq, ok := stats["max_seq"].(float64)
	if !ok || maxSeq < 3 {
		t.Errorf("expected max_seq >= 3, got %v", stats["max_seq"])
	}
}

// ── Provenance handler tests ─────────────────────────────────────────────────

func TestProvenanceHandler(t *testing.T) {
	db := setupTestDB(t)

	rootID, _, _, _ := storeMemory(db, "root", nil, "public", "a", 0, nil)
	childID, _, _, _ := storeMemory(db, "child", nil, "public", "b", rootID, nil)

	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/provenance?id="+itoa(childID), nil)
	provenanceHandler(db)(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", w.Code)
	}

	var resp apiResponse
	json.Unmarshal(w.Body.Bytes(), &resp)
	if !resp.Success {
		t.Fatalf("expected success: %s", resp.Error)
	}

	contentBytes, _ := json.Marshal(resp.Content)
	var chain []memoryEntry
	json.Unmarshal(contentBytes, &chain)

	if len(chain) != 2 {
		t.Fatalf("expected chain of 2, got %d", len(chain))
	}
	if chain[0].Content != "root" || chain[1].Content != "child" {
		t.Errorf("chain = %v", chain)
	}
}

func TestProvenanceHandler_MissingID(t *testing.T) {
	db := setupTestDB(t)

	w := httptest.NewRecorder()
	r := httptest.NewRequest("GET", "/provenance", nil)
	provenanceHandler(db)(w, r)

	if w.Code != http.StatusBadRequest {
		t.Errorf("status = %d, want 400", w.Code)
	}
}

func TestProvenanceHandler_IncludesEvidence(t *testing.T) {
	db := setupTestDB(t)
	parentID, _, _, _ := storeMemory(db, "root", nil, "public", "a", 0, &EvidenceTrace{Kind: "tool_result"})
	childID, _, _, _ := storeMemory(db, "child", nil, "public", "b", parentID, &EvidenceTrace{Kind: "llm_interpretation"})

	mux := http.NewServeMux()
	mux.HandleFunc("GET /provenance", provenanceHandler(db))

	req := httptest.NewRequest("GET", fmt.Sprintf("/provenance?id=%d", childID), nil)
	rec := httptest.NewRecorder()
	mux.ServeHTTP(rec, req)

	if rec.Code != http.StatusOK {
		t.Fatalf("provenance returned %d: %s", rec.Code, rec.Body.String())
	}

	var resp apiResponse
	json.NewDecoder(rec.Body).Decode(&resp)
	var chain []memoryEntry
	raw, _ := json.Marshal(resp.Content)
	json.Unmarshal(raw, &chain)

	if len(chain) != 2 {
		t.Fatalf("expected 2 entries in chain, got %d", len(chain))
	}
	if chain[0].Evidence == nil || chain[0].Evidence.Kind != "tool_result" {
		t.Error("root should have tool_result evidence")
	}
	if chain[1].Evidence == nil || chain[1].Evidence.Kind != "llm_interpretation" {
		t.Error("child should have llm_interpretation evidence")
	}
}

func itoa(n int64) string {
	return fmt.Sprintf("%d", n)
}

// ── deleteHandler tests ──────────────────────────────────────────────────────

func TestDeleteHandler_DisabledWithoutToken(t *testing.T) {
	db := setupTestDB(t)
	id := storeDefault(t, db, "corrupted entry", nil)

	w := httptest.NewRecorder()
	r := httptest.NewRequest("DELETE", "/delete?id="+itoa(id), nil)
	deleteHandler(db, "")(w, r) // empty admin token → endpoint disabled

	if w.Code != http.StatusForbidden {
		t.Fatalf("status = %d, want 403", w.Code)
	}
	// The entry must remain untouched.
	if _, found, _ := getMemoryByID(db, id); !found {
		t.Error("entry should not have been deleted while endpoint is disabled")
	}
}

func TestDeleteHandler_Unauthorized(t *testing.T) {
	db := setupTestDB(t)
	id := storeDefault(t, db, "sensitive entry", nil)

	cases := map[string]string{
		"missing header": "",
		"wrong token":    "Bearer nope",
		"not bearer":     "secret-token",
	}
	for name, authHeader := range cases {
		t.Run(name, func(t *testing.T) {
			w := httptest.NewRecorder()
			r := httptest.NewRequest("DELETE", "/delete?id="+itoa(id), nil)
			if authHeader != "" {
				r.Header.Set("Authorization", authHeader)
			}
			deleteHandler(db, "secret-token")(w, r)

			if w.Code != http.StatusUnauthorized {
				t.Fatalf("status = %d, want 401", w.Code)
			}
		})
	}
	if _, found, _ := getMemoryByID(db, id); !found {
		t.Error("entry should survive unauthorized delete attempts")
	}
}

func TestDeleteHandler_Success(t *testing.T) {
	db := setupTestDB(t)
	id := storeDefault(t, db, "delete me: kafka lag in payments", []string{"kafka"})

	w := httptest.NewRecorder()
	r := httptest.NewRequest("DELETE", "/delete?id="+itoa(id), nil)
	r.Header.Set("Authorization", "Bearer secret-token")
	deleteHandler(db, "secret-token")(w, r)

	if w.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200 (body: %s)", w.Code, w.Body.String())
	}
	var resp apiResponse
	if err := json.Unmarshal(w.Body.Bytes(), &resp); err != nil {
		t.Fatalf("parse response: %v", err)
	}
	if !resp.Success {
		t.Fatalf("expected success, got error: %s", resp.Error)
	}

	// Row is gone from the base table.
	if _, found, err := getMemoryByID(db, id); err != nil || found {
		t.Fatalf("entry still present after delete (found=%v err=%v)", found, err)
	}
	// Gone from list.
	if list, _ := listMemories(db, "", 20, "", nil, "", "", ""); len(list) != 0 {
		t.Errorf("list returned %d entries after delete, want 0", len(list))
	}
	// Gone from FTS search too — proves the memories_ad trigger fired.
	if hits, _ := searchMemories(db, "kafka", 5, "", nil, nil, "", ""); len(hits) != 0 {
		t.Errorf("search returned %d hits after delete, want 0", len(hits))
	}
}

func TestDeleteHandler_NotFound(t *testing.T) {
	db := setupTestDB(t)

	w := httptest.NewRecorder()
	r := httptest.NewRequest("DELETE", "/delete?id=9999", nil)
	r.Header.Set("Authorization", "Bearer secret-token")
	deleteHandler(db, "secret-token")(w, r)

	if w.Code != http.StatusNotFound {
		t.Fatalf("status = %d, want 404", w.Code)
	}
}

func TestDeleteHandler_BadID(t *testing.T) {
	db := setupTestDB(t)

	for _, q := range []string{"/delete", "/delete?id=abc"} {
		w := httptest.NewRecorder()
		r := httptest.NewRequest("DELETE", q, nil)
		r.Header.Set("Authorization", "Bearer secret-token")
		deleteHandler(db, "secret-token")(w, r)

		if w.Code != http.StatusBadRequest {
			t.Errorf("%s: status = %d, want 400", q, w.Code)
		}
	}
}
