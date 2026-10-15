package rag

import (
	"strings"
	"context"
	"encoding/json"
	"math"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestQdrantSemanticSearchUsesEmbedQuery verifies that when the embedder
// implements QueryEmbedder, Qdrant's SemanticSearch calls EmbedQuery (not Embed).
func TestQdrantSemanticSearchUsesEmbedQuery(t *testing.T) {
	embedder := &mockQueryEmbedder{}

	// Mock Qdrant server
	var searchBody map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/healthz" {
			w.WriteHeader(http.StatusOK)
			return
		}
		if r.Method == http.MethodPost && r.URL.Path != "" {
			// Capture search request body
			if r.URL.Path != "/healthz" {
				body := make(map[string]any)
				_ = json.NewDecoder(r.Body).Decode(&body)
				searchBody = body
			}
		}
		// Return empty search results
		resp := map[string]any{
			"result": []any{},
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	p, err := NewQdrantProvider(context.Background(), srv.URL, "", embedder)
	if err != nil {
		t.Fatalf("NewQdrantProvider: %v", err)
	}
	defer p.Close()

	_, err = p.SemanticSearch(context.Background(), "testproj", "hello world", 5)
	if err != nil {
		t.Fatalf("SemanticSearch: %v", err)
	}

	if embedder.getQueryCalls() == 0 {
		t.Error("EmbedQuery should have been called")
	}
	if embedder.getEmbedCalls() != 0 {
		t.Error("Embed should NOT have been called when QueryEmbedder is implemented")
	}

	// Verify the search request used the query vector (0.4, 0.5, 0.6)
	if searchBody == nil {
		t.Fatal("search request body was not captured")
	}
	vec, ok := searchBody["vector"].([]any)
	if !ok {
		t.Fatal("search request should contain a vector")
	}
	if len(vec) != 3 {
		t.Fatalf("expected 3-dimensional vector, got %d", len(vec))
	}
	// First element should be 0.4 (from EmbedQuery), not 0.1 (from Embed)
	// float32(0.4) becomes 0.4000000059604645 when converted to float64 by JSON
	firstElem, ok := vec[0].(float64)
	if !ok {
		t.Fatalf("expected vector[0] to be float64, got %T", vec[0])
	}
	if math.Abs(firstElem-float64(float32(0.4))) > 1e-6 {
		t.Errorf("expected vector[0]≈0.4 (from EmbedQuery), got %v", vec[0])
	}
}

// TestQdrantSemanticSearchFallsBackToEmbed verifies that when the embedder
// does NOT implement QueryEmbedder, Qdrant's SemanticSearch falls back to Embed.
func TestQdrantSemanticSearchFallsBackToEmbed(t *testing.T) {
	embedder := &mockPlainEmbedder{}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/healthz" {
			w.WriteHeader(http.StatusOK)
			return
		}
		resp := map[string]any{
			"result": []any{},
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	p, err := NewQdrantProvider(context.Background(), srv.URL, "", embedder)
	if err != nil {
		t.Fatalf("NewQdrantProvider: %v", err)
	}
	defer p.Close()

	_, err = p.SemanticSearch(context.Background(), "testproj", "hello world", 5)
	if err != nil {
		t.Fatalf("SemanticSearch: %v", err)
	}

	if embedder.getEmbedCalls() == 0 {
		t.Error("Embed should have been called as fallback")
	}
}

// TestQdrantSemanticSearchEmbedQueryError verifies that if EmbedQuery returns
// an error, SemanticSearch propagates it.
func TestQdrantSemanticSearchEmbedQueryError(t *testing.T) {
	embedder := &mockQueryEmbedder{
		queryErr: context.DeadlineExceeded,
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/healthz" {
			w.WriteHeader(http.StatusOK)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]any{"result": []any{}})
	}))
	defer srv.Close()

	p, err := NewQdrantProvider(context.Background(), srv.URL, "", embedder)
	if err != nil {
		t.Fatalf("NewQdrantProvider: %v", err)
	}
	defer p.Close()

	_, err = p.SemanticSearch(context.Background(), "testproj", "hello world", 5)
	if err == nil {
		t.Fatal("expected error from EmbedQuery failure")
	}
}

// TestQdrantListProjectIDs verifies that ListProjectIDs lists collections and
// strips the "project_" prefix, implementing core.ProjectLister for Qdrant.
func TestQdrantListProjectIDs(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/healthz" {
			w.WriteHeader(http.StatusOK)
			return
		}
		if r.Method == http.MethodGet && r.URL.Path == "/collections" {
			w.Header().Set("Content-Type", "application/json")
			w.Write([]byte(`{"result":{"collections":[{"name":"project_dow-mind"},{"name":"project_demo"},{"name":"other_thing"}]}}`))
			return
		}
		w.WriteHeader(http.StatusOK)
	}))
	defer srv.Close()

	p, err := NewQdrantProvider(context.Background(), srv.URL, "", &mockQueryEmbedder{})
	if err != nil {
		t.Fatalf("NewQdrantProvider: %v", err)
	}
	defer p.Close()

	ids, err := p.ListProjectIDs(context.Background())
	if err != nil {
		t.Fatalf("ListProjectIDs: %v", err)
	}
	// Only project_-prefixed collections, prefix stripped; "other_thing" excluded.
	if len(ids) != 2 {
		t.Fatalf("expected 2 project IDs, got %d: %v", len(ids), ids)
	}
	want := map[string]bool{"dow-mind": true, "demo": true}
	for _, id := range ids {
		if !want[id] {
			t.Errorf("unexpected project ID %q", id)
		}
	}
}

// TestQdrantExportPoints verifies export returns FULL content (not truncated to
// 200 chars) and preserves doc_id + metadata.
func TestQdrantExportPoints(t *testing.T) {
	longContent := ""
	for i := 0; i < 500; i++ {
		longContent += "x"
	}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path == "/healthz" {
			w.WriteHeader(http.StatusOK)
			return
		}
		w.Header().Set("Content-Type", "application/json")
		resp := map[string]any{"result": map[string]any{
			"points": []map[string]any{
				{"id": "uuid-1", "payload": map[string]any{"content": longContent, "doc_id": "file.go#chunk0", "source_file": "file.go", "content_hash": "h1"}},
			},
			"next_page_offset": nil,
		}}
		json.NewEncoder(w).Encode(resp)
	}))
	defer srv.Close()

	p, err := NewQdrantProvider(context.Background(), srv.URL, "", &mockQueryEmbedder{})
	if err != nil {
		t.Fatalf("NewQdrantProvider: %v", err)
	}
	docs, err := p.ExportPoints(context.Background(), "proj")
	if err != nil {
		t.Fatalf("ExportPoints: %v", err)
	}
	if len(docs) != 1 {
		t.Fatalf("expected 1 doc, got %d", len(docs))
	}
	if len(docs[0].Content) != 500 {
		t.Errorf("content truncated: got %d chars, want 500 (export must not truncate)", len(docs[0].Content))
	}
	if docs[0].ID != "file.go#chunk0" {
		t.Errorf("ID = %q, want doc_id preserved", docs[0].ID)
	}
	if docs[0].Meta["content_hash"] != "h1" {
		t.Error("metadata not preserved")
	}
}

// TestQdrantListPointsPage verifies that ListPointsPage fetches the total via
// points/count (with the same filter) and exactly one scroll page via the
// integer offset — never a full-collection scroll.
func TestQdrantListPointsPage(t *testing.T) {
	type req struct {
		path string
		body map[string]any
	}
	var reqs []req

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		_ = json.NewDecoder(r.Body).Decode(&body)
		reqs = append(reqs, req{path: r.URL.Path, body: body})

		w.Header().Set("Content-Type", "application/json")
		switch {
		case strings.HasSuffix(r.URL.Path, "/points/count"):
			json.NewEncoder(w).Encode(map[string]any{
				"result": map[string]any{"count": 42},
			})
		case strings.HasSuffix(r.URL.Path, "/points/scroll"):
			json.NewEncoder(w).Encode(map[string]any{
				"result": map[string]any{
					"points": []any{
						map[string]any{
							"id": "uuid-1",
							"payload": map[string]any{
								"content":     strings.Repeat("c", 500),
								"doc_id":      "doc-1",
								"source_file": "a.go",
							},
						},
					},
				},
			})
		default:
			json.NewEncoder(w).Encode(map[string]any{"result": map[string]any{}})
		}
	}))
	defer srv.Close()

	p, err := NewQdrantProvider(context.Background(), srv.URL, "", &mockQueryEmbedder{})
	if err != nil {
		t.Fatalf("NewQdrantProvider: %v", err)
	}
	defer p.Close()

	points, total, err := p.ListPointsPage(context.Background(), "proj", map[string]string{"source_file": "a.go"}, 50, 10)
	if err != nil {
		t.Fatalf("ListPointsPage: %v", err)
	}
	if total != 42 {
		t.Errorf("expected total 42, got %d", total)
	}
	if len(points) != 1 || points[0].ID != "uuid-1" || points[0].DocID != "doc-1" {
		t.Fatalf("unexpected page: %+v", points)
	}
	if len(points[0].Content) > 200 {
		t.Errorf("expected content preview <= 200 chars, got %d", len(points[0].Content))
	}

	// Verify request shapes: count carries the filter; scroll carries
	// limit+offset (not next_page_offset chaining).
	var sawCount, sawScroll bool
	for _, r := range reqs {
		switch {
		case strings.HasSuffix(r.path, "/points/count"):
			sawCount = true
			f, ok := r.body["filter"].(map[string]any)
			if !ok {
				t.Fatalf("count request should carry the meta filter, got %v", r.body)
			}
			must, _ := f["must"].([]any)
			if len(must) != 1 {
				t.Errorf("expected 1 must clause, got %v", f)
			}
		case strings.HasSuffix(r.path, "/points/scroll"):
			sawScroll = true
			if r.body["limit"].(float64) != 50 || r.body["offset"].(float64) != 10 {
				t.Errorf("scroll body should carry limit=50 offset=10, got %v", r.body)
			}
		}
	}
	if !sawCount || !sawScroll {
		t.Errorf("expected one count and one scroll request, got %d request(s)", len(reqs))
	}
}
