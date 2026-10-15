package rag

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"
)

// --- ModelNamer tests ---

// TestVoyageModelName verifies that VoyageEmbeddingClient implements ModelNamer.
func TestVoyageModelName(t *testing.T) {
	c := NewVoyageEmbeddingClient("key", "voyage-4", 1024)
	mn, ok := any(c).(ModelNamer)
	if !ok {
		t.Fatal("VoyageEmbeddingClient should implement ModelNamer")
	}
	if mn.ModelName() != "voyage-4" {
		t.Errorf("ModelName() = %q, want %q", mn.ModelName(), "voyage-4")
	}
}

// TestTEIDoesNotImplementModelNamer verifies that TEIEmbeddingClient does NOT
// implement ModelNamer (it has no ModelName method).
func TestTEIDoesNotImplementModelNamer(t *testing.T) {
	embedder := NewTEIEmbeddingClient("http://localhost:8081")
	_, ok := any(embedder).(ModelNamer)
	if ok {
		t.Fatal("TEIEmbeddingClient should NOT implement ModelNamer")
	}
}

// mockModelNamerEmbedder implements EmbeddingClient + ModelNamer.
type mockModelNamerEmbedder struct {
	mockPlainEmbedder
	modelName string
}

func (m *mockModelNamerEmbedder) ModelName() string {
	return m.modelName
}

// --- Qdrant embed_model/embed_dim injection tests ---

// TestQdrantIndexInjectsEmbedMetadata verifies that Qdrant's Index method
// injects embed_model and embed_dim into the point payload.
func TestQdrantIndexInjectsEmbedMetadata(t *testing.T) {
	var capturedBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/healthz" {
			w.WriteHeader(http.StatusOK)
			return
		}
		body := make(map[string]any)
		_ = json.NewDecoder(r.Body).Decode(&body)
		capturedBody = body
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"result": map[string]any{"operation_id": 0, "status": "completed"}})
	}))
	defer srv.Close()

	embedder := &mockModelNamerEmbedder{
		mockPlainEmbedder: mockPlainEmbedder{vectorSize: 3},
		modelName:         "voyage-4",
	}

	p, err := NewQdrantProvider(context.Background(), srv.URL, "", embedder)
	if err != nil {
		t.Fatalf("NewQdrantProvider: %v", err)
	}
	defer p.Close()

	docs := []Document{
		{ID: "doc1", Content: "hello", Meta: map[string]string{"source_file": "test.go"}},
	}
	if err := p.Index(context.Background(), "testproj", docs); err != nil {
		t.Fatalf("Index: %v", err)
	}

	points, ok := capturedBody["points"].([]any)
	if !ok || len(points) == 0 {
		t.Fatal("expected at least 1 point in request body")
	}
	firstPoint, ok := points[0].(map[string]any)
	if !ok {
		t.Fatal("expected point to be a map")
	}
	payload, ok := firstPoint["payload"].(map[string]any)
	if !ok {
		t.Fatal("expected payload in point")
	}

	if payload["embed_model"] != "voyage-4" {
		t.Errorf("embed_model = %v, want %q", payload["embed_model"], "voyage-4")
	}
	// embed_dim is stored as int but JSON round-trips as float64
	if embedDimVal, ok := payload["embed_dim"].(float64); !ok || embedDimVal != 3 {
		t.Errorf("embed_dim = %v, want 3", payload["embed_dim"])
	}
}

// TestQdrantIndexInjectsEmbedMetadataNoModelNamer verifies that when the embedder
// does NOT implement ModelNamer, embed_model defaults to "unknown".
func TestQdrantIndexInjectsEmbedMetadataNoModelNamer(t *testing.T) {
	var capturedBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/healthz" {
			w.WriteHeader(http.StatusOK)
			return
		}
		body := make(map[string]any)
		_ = json.NewDecoder(r.Body).Decode(&body)
		capturedBody = body
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"result": map[string]any{"operation_id": 0, "status": "completed"}})
	}))
	defer srv.Close()

	// mockPlainEmbedder does NOT implement ModelNamer
	embedder := &mockPlainEmbedder{vectorSize: 3}

	p, err := NewQdrantProvider(context.Background(), srv.URL, "", embedder)
	if err != nil {
		t.Fatalf("NewQdrantProvider: %v", err)
	}
	defer p.Close()

	docs := []Document{
		{ID: "doc1", Content: "hello", Meta: map[string]string{}},
	}
	if err := p.Index(context.Background(), "testproj", docs); err != nil {
		t.Fatalf("Index: %v", err)
	}

	points, _ := capturedBody["points"].([]any)
	firstPoint, _ := points[0].(map[string]any)
	payload, _ := firstPoint["payload"].(map[string]any)

	if payload["embed_model"] != "unknown" {
		t.Errorf("embed_model = %v, want %q", payload["embed_model"], "unknown")
	}
}

// --- Chroma embed_model/embed_dim injection tests ---

// TestChromaIndexInjectsEmbedMetadata verifies that Chroma's Index method
// injects embed_model and embed_dim into the document metadata.
func TestChromaIndexInjectsEmbedMetadata(t *testing.T) {
	var capturedBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		body := make(map[string]any)
		_ = json.NewDecoder(r.Body).Decode(&body)
		capturedBody = body
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{})
	}))
	defer srv.Close()

	embedder := &mockModelNamerEmbedder{
		mockPlainEmbedder: mockPlainEmbedder{vectorSize: 3},
		modelName:         "voyage-4",
	}

	p := NewChromaProvider(srv.URL, embedder)

	docs := []Document{
		{ID: "doc1", Content: "hello", Meta: map[string]string{"source_file": "test.go"}},
	}
	if err := p.Index(context.Background(), "testproj", docs); err != nil {
		t.Fatalf("Index: %v", err)
	}

	metas, ok := capturedBody["metadatas"].([]any)
	if !ok || len(metas) == 0 {
		t.Fatal("expected at least 1 metadata entry")
	}
	firstMeta, ok := metas[0].(map[string]any)
	if !ok {
		t.Fatal("expected metadata to be a map")
	}

	if firstMeta["embed_model"] != "voyage-4" {
		t.Errorf("embed_model = %v, want %q", firstMeta["embed_model"], "voyage-4")
	}
	// embed_dim is stored as int but JSON round-trips as float64
	if embedDimVal, ok := firstMeta["embed_dim"].(float64); !ok || embedDimVal != 3 {
		t.Errorf("embed_dim = %v, want 3", firstMeta["embed_dim"])
	}
}

// --- pgvector embed_model/embed_dim injection test (integration) ---

// TestPGVectorIndexInjectsEmbedMetadata verifies that pgvector's Index method
// injects embed_model and embed_dim into the stored metadata.
func TestPGVectorIndexInjectsEmbedMetadata(t *testing.T) {
	skipIfNoPostgres(t)

	embedder := &mockModelNamerEmbedder{
		mockPlainEmbedder: mockPlainEmbedder{vectorSize: 3},
		modelName:         "voyage-4",
	}

	p, err := NewPGVectorProvider(context.Background(), pgvectorDSN, embedder, "project_memory_test")
	if err != nil {
		t.Fatalf("NewPGVectorProvider: %v", err)
	}
	defer p.Close()

	projectID := "test_embed_meta_inject"
	cleanupTestTable(t, projectID)
	defer cleanupTestTable(t, projectID)

	docs := []Document{
		{ID: uuid.NewString(), Content: "hello world", Meta: map[string]string{"source_file": "test.go"}},
	}
	if err := p.Index(context.Background(), projectID, docs); err != nil {
		t.Fatalf("Index: %v", err)
	}

	// Query the stored metadata directly
	var meta map[string]string
	err = p.pool.QueryRow(context.Background(),
		fmt.Sprintf("SELECT metadata FROM %s WHERE project_id = $1 AND id = $2", "project_memory_test"),
		projectID, docs[0].ID,
	).Scan(&meta)
	if err != nil {
		t.Fatalf("query metadata: %v", err)
	}

	if meta["embed_model"] != "voyage-4" {
		t.Errorf("embed_model = %q, want %q", meta["embed_model"], "voyage-4")
	}
	if meta["embed_dim"] != "3" {
		t.Errorf("embed_dim = %q, want %q", meta["embed_dim"], "3")
	}
}

// namedMockEmbedder is an EmbeddingClient + ModelNamer used to verify that
// Index injects CURRENT embed identity even when document metadata carries
// stale values from a previous backend (migration scenario).
type namedMockEmbedder struct{}

func (namedMockEmbedder) Embed(ctx context.Context, texts []string) ([][]float32, error) {
	out := make([][]float32, len(texts))
	for i := range out {
		out[i] = []float32{0.1, 0.2, 0.3}
	}
	return out, nil
}
func (namedMockEmbedder) VectorSize() int   { return 1024 }
func (namedMockEmbedder) ModelName() string { return "current-model" }

// TestQdrantIndexInjectsCurrentEmbedModel verifies the injection order in
// QdrantProvider.Index: document metadata (e.g. exported from a previous
// embedder) must NOT override the embed_model/embed_dim describing the
// embedding actually stored.
func TestQdrantIndexInjectsCurrentEmbedModel(t *testing.T) {
	var payload map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/points") && r.Method == http.MethodPut {
			var body struct {
				Points []struct {
					Payload map[string]any `json:"payload"`
				} `json:"points"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			payload = body.Points[0].Payload
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"result": map[string]any{}})
	}))
	defer srv.Close()

	p, err := NewQdrantProvider(context.Background(), srv.URL, "", namedMockEmbedder{})
	if err != nil {
		t.Fatalf("NewQdrantProvider: %v", err)
	}
	defer p.Close()

	err = p.Index(context.Background(), "proj", []Document{{
		ID: "d1", Content: "hello",
		Meta: map[string]string{"embed_model": "stale-old", "embed_dim": "384"},
	}})
	if err != nil {
		t.Fatalf("Index: %v", err)
	}
	if payload["embed_model"] != "current-model" {
		t.Errorf("embed_model = %v, want current-model (stale metadata must not win)", payload["embed_model"])
	}
	if payload["embed_dim"] != float64(1024) {
		t.Errorf("embed_dim = %v, want 1024", payload["embed_dim"])
	}
	// Non-identity metadata still flows through.
	if payload["doc_id"] != "d1" {
		t.Errorf("doc_id = %v, want d1", payload["doc_id"])
	}
}

// TestQdrantIndexInjectsCreatedAtAndAuthor verifies provenance injection:
// created_at (RFC3339) is stamped when absent, author falls back to
// DOWMIND_DEFAULT_AUTHOR, and values already present in metadata are kept.
func TestQdrantIndexInjectsCreatedAtAndAuthor(t *testing.T) {
	var payloads []map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/points") && r.Method == http.MethodPut {
			var body struct {
				Points []struct {
					Payload map[string]any `json:"payload"`
				} `json:"points"`
			}
			_ = json.NewDecoder(r.Body).Decode(&body)
			for _, p := range body.Points {
				payloads = append(payloads, p.Payload)
			}
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"result": map[string]any{}})
	}))
	defer srv.Close()

	t.Setenv("DOWMIND_DEFAULT_AUTHOR", "dowarch")
	p, err := NewQdrantProvider(context.Background(), srv.URL, "", namedMockEmbedder{})
	if err != nil {
		t.Fatalf("NewQdrantProvider: %v", err)
	}
	defer p.Close()

	err = p.Index(context.Background(), "proj", []Document{
		{ID: "d1", Content: "no provenance in meta"},
		{ID: "d2", Content: "with provenance", Meta: map[string]string{
			"created_at": "2026-01-01T00:00:00Z", "author": "ipol",
		}},
	})
	if err != nil {
		t.Fatalf("Index: %v", err)
	}
	if len(payloads) != 2 {
		t.Fatalf("want 2 payloads, got %d", len(payloads))
	}
	// d1: injected.
	ts, ok := payloads[0]["created_at"].(string)
	if !ok {
		t.Fatalf("d1 missing created_at: %v", payloads[0])
	}
	if _, err := time.Parse(time.RFC3339, ts); err != nil {
		t.Errorf("d1 created_at %q is not RFC3339: %v", ts, err)
	}
	if payloads[0]["author"] != "dowarch" {
		t.Errorf("d1 author = %v, want dowarch (env fallback)", payloads[0]["author"])
	}
	// d2: caller-supplied values preserved.
	if payloads[1]["created_at"] != "2026-01-01T00:00:00Z" {
		t.Errorf("d2 created_at overwritten: %v", payloads[1]["created_at"])
	}
	if payloads[1]["author"] != "ipol" {
		t.Errorf("d2 author overwritten: %v", payloads[1]["author"])
	}
}

// TestQdrantSetPayloadByDocID verifies the supersede write path targets the
// points/payload endpoint with a doc_id filter and the expected payload.
func TestQdrantSetPayloadByDocID(t *testing.T) {
	var body map[string]any
	var path string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if strings.HasSuffix(r.URL.Path, "/points/payload") {
			path = r.URL.Path
			_ = json.NewDecoder(r.Body).Decode(&body)
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"result": map[string]any{}})
	}))
	defer srv.Close()

	p, err := NewQdrantProvider(context.Background(), srv.URL, "", namedMockEmbedder{})
	if err != nil {
		t.Fatalf("NewQdrantProvider: %v", err)
	}
	defer p.Close()

	err = p.SetPayloadByDocID(context.Background(), "proj", "lesson-v1",
		map[string]any{"superseded_by": "lesson-v2"})
	if err != nil {
		t.Fatalf("SetPayloadByDocID: %v", err)
	}
	if !strings.Contains(fmt.Sprintf("%v", body["payload"]), "lesson-v2") {
		t.Errorf("payload = %v, want superseded_by lesson-v2", body["payload"])
	}
	filterDump := fmt.Sprintf("%v", body["filter"])
	if !strings.Contains(filterDump, "doc_id") || !strings.Contains(filterDump, "lesson-v1") {
		t.Errorf("filter = %v, want doc_id match lesson-v1", body["filter"])
	}
	if !strings.Contains(path, "/collections/") {
		t.Errorf("unexpected path %q", path)
	}
}

// TestTruncateUTF8 verifies byte-budget truncation never splits a rune.
func TestTruncateUTF8(t *testing.T) {
	s := "héllo世界" // multibyte runes (é=2B, 世=3B, 界=3B)
	got := TruncateUTF8(s, 4)
	if !utf8.ValidString(got) {
		t.Errorf("result not valid UTF-8: %q", got)
	}
	if !strings.HasPrefix(s, got) {
		t.Errorf("truncated %q is not a prefix of %q", got, s)
	}
	if len(got) > 4 {
		t.Errorf("length = %d, want <= 4", len(got))
	}
	if TruncateUTF8(s, len(s)+10) != s {
		t.Error("no-op truncation changed the string")
	}
	// Cut exactly between runes keeps the full preceding rune.
	if got := TruncateUTF8("ab世", 5); got != "ab世" {
		t.Errorf("boundary cut = %q, want ab世", got)
	}
}
