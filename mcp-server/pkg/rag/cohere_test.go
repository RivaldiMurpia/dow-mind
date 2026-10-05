package rag

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

// TestCohereRerankerImplementsReranker verifies at compile time that
// CohereReranker implements the Reranker interface.
func TestCohereRerankerImplementsReranker(t *testing.T) {
	var _ Reranker = (*CohereReranker)(nil)
	var r Reranker = NewCohereReranker("key", "")
	if r == nil {
		t.Fatal("NewCohereReranker returned nil")
	}
}

// TestNewCohereRerankerDefaults verifies that the model defaults to
// rerank-v3.5 when empty.
func TestNewCohereRerankerDefaults(t *testing.T) {
	r := NewCohereReranker("key", "")
	if r.Model != DefaultCohereRerankModel {
		t.Errorf("Model = %q, want %q", r.Model, DefaultCohereRerankModel)
	}
	if r.APIKey != "key" {
		t.Errorf("APIKey = %q, want %q", r.APIKey, "key")
	}
	r2 := NewCohereReranker("key", "rerank-english-v3.0")
	if r2.Model != "rerank-english-v3.0" {
		t.Errorf("Model = %q, want %q", r2.Model, "rerank-english-v3.0")
	}
}

// TestCohereRerankerCallsCorrectEndpoint verifies that Rerank sends POST to
// the correct URL path, with the correct model, top_n, and auth header, and
// that the v2 response is mapped to RerankHit correctly.
func TestCohereRerankerCallsCorrectEndpoint(t *testing.T) {
	var capturedMethod string
	var capturedPath string
	var capturedAuth string
	var capturedBody map[string]any

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		capturedMethod = r.Method
		capturedPath = r.URL.Path
		capturedAuth = r.Header.Get("Authorization")
		body, _ := io.ReadAll(r.Body)
		_ = json.Unmarshal(body, &capturedBody)

		resp := cohereRerankResponse{
			Results: []cohereRerankResult{
				{Index: 1, RelevanceScore: 0.87},
				{Index: 0, RelevanceScore: 0.42},
			},
		}
		resp.Meta.BilledUnits.SearchUnits = 2
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	r := NewCohereReranker("test-key", "rerank-v3.5")
	r.baseURL = server.URL

	hits, err := r.Rerank(context.Background(), "how does auth work", []string{"doc0", "doc1"}, 2)
	if err != nil {
		t.Fatalf("Rerank: %v", err)
	}

	if capturedMethod != http.MethodPost {
		t.Errorf("method = %q, want POST", capturedMethod)
	}
	if capturedPath != "/" {
		t.Errorf("path = %q, want /", capturedPath)
	}
	if capturedAuth != "Bearer test-key" {
		t.Errorf("auth = %q, want %q", capturedAuth, "Bearer test-key")
	}
	if capturedBody["model"] != "rerank-v3.5" {
		t.Errorf("model = %v, want rerank-v3.5", capturedBody["model"])
	}
	if capturedBody["query"] != "how does auth work" {
		t.Errorf("query = %v", capturedBody["query"])
	}
	if n, ok := capturedBody["top_n"].(float64); !ok || int(n) != 2 {
		t.Errorf("top_n = %v, want 2", capturedBody["top_n"])
	}
	docs, ok := capturedBody["documents"].([]any)
	if !ok || len(docs) != 2 {
		t.Errorf("documents = %v, want 2 docs", capturedBody["documents"])
	}

	if len(hits) != 2 {
		t.Fatalf("hits = %d, want 2", len(hits))
	}
	if hits[0].Index != 1 || hits[0].Score != 0.87 {
		t.Errorf("hits[0] = %+v, want {Index:1 Score:0.87}", hits[0])
	}
	if got := r.TokensUsed(); got != 2 {
		t.Errorf("TokensUsed = %d, want 2 (search units)", got)
	}
}

// TestCohereRerankerEmptyDocs verifies Rerank short-circuits on empty input.
func TestCohereRerankerEmptyDocs(t *testing.T) {
	r := NewCohereReranker("key", "")
	hits, err := r.Rerank(context.Background(), "q", nil, 5)
	if err != nil {
		t.Fatalf("Rerank: %v", err)
	}
	if hits != nil {
		t.Errorf("hits = %v, want nil", hits)
	}
}

// TestCohereRerankerHTTPError verifies non-200 responses surface an error.
func TestCohereRerankerHTTPError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"message":"invalid api key"}`))
	}))
	defer server.Close()

	r := NewCohereReranker("bad-key", "")
	r.baseURL = server.URL
	if _, err := r.Rerank(context.Background(), "q", []string{"d"}, 1); err == nil {
		t.Error("expected error for 401 response, got nil")
	}
}
