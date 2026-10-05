package rag

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"sync/atomic"
	"time"
)

const (
	// cohereRerankURL is the default endpoint for the Cohere rerank API (v2).
	cohereRerankURL = "https://api.cohere.com/v2/rerank"
	// DefaultCohereRerankModel is the default Cohere rerank model.
	DefaultCohereRerankModel = "rerank-v3.5"
)

// CohereReranker implements the Reranker interface by calling the Cohere
// rerank API v2 (https://docs.cohere.com/v2/docs/rerank).
type CohereReranker struct {
	APIKey  string
	Model   string // defaults to "rerank-v3.5"
	client  *http.Client
	baseURL string // override for testing; defaults to cohereRerankURL
	units   atomic.Int64 // cumulative billed search_units reported by the API
}

// NewCohereReranker creates a CohereReranker with the given API key.
// If model is empty, it defaults to "rerank-v3.5".
func NewCohereReranker(apiKey, model string) *CohereReranker {
	if model == "" {
		model = DefaultCohereRerankModel
	}
	return &CohereReranker{
		APIKey: apiKey,
		Model:  model,
		client: &http.Client{Timeout: 120 * time.Second},
	}
}

// cohereRerankRequest is the JSON body sent to the Cohere rerank API v2.
type cohereRerankRequest struct {
	Model     string   `json:"model"`
	Query     string   `json:"query"`
	Documents []string `json:"documents"`
	TopN      int      `json:"top_n"`
}

// cohereRerankResult is one entry of the Cohere v2 results array.
type cohereRerankResult struct {
	Index          int     `json:"index"`
	RelevanceScore float64 `json:"relevance_score"`
}

// cohereRerankResponse is the JSON response from the Cohere rerank API v2.
type cohereRerankResponse struct {
	Results []cohereRerankResult `json:"results"`
	Meta    struct {
		BilledUnits struct {
			SearchUnits int64 `json:"search_units"`
		} `json:"billed_units"`
	} `json:"meta"`
}

// Rerank sends the query and documents to the Cohere rerank API and returns
// a slice of RerankHit containing the index and relevance score for each
// reranked document, truncated to topK.
func (r *CohereReranker) Rerank(ctx context.Context, query string, docs []string, topK int) ([]RerankHit, error) {
	if len(docs) == 0 {
		return nil, nil
	}

	body, err := json.Marshal(cohereRerankRequest{
		Model:     r.Model,
		Query:     query,
		Documents: docs,
		TopN:      topK,
	})
	if err != nil {
		return nil, fmt.Errorf("marshal rerank request: %w", err)
	}

	url := r.baseURL
	if url == "" {
		url = cohereRerankURL
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("create rerank request: %w", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+r.APIKey)

	resp, err := r.client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("cohere rerank request failed: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		b, _ := io.ReadAll(resp.Body)
		return nil, fmt.Errorf("cohere rerank returned %d: %s", resp.StatusCode, string(b))
	}

	var result cohereRerankResponse
	if err := json.NewDecoder(resp.Body).Decode(&result); err != nil {
		return nil, fmt.Errorf("decode cohere rerank response: %w", err)
	}
	r.units.Add(result.Meta.BilledUnits.SearchUnits)

	hits := make([]RerankHit, 0, len(result.Results))
	for _, res := range result.Results {
		hits = append(hits, RerankHit{Index: res.Index, Score: res.RelevanceScore})
	}
	return hits, nil
}

// TokensUsed returns the cumulative billed search_units reported by the
// Cohere rerank API since this reranker was created. Cohere bills rerank by
// search units rather than tokens; this is the closest usage analog and is
// reported under the same usage counters. Implements TokenCounter.
func (r *CohereReranker) TokensUsed() int64 {
	return r.units.Load()
}

// Compile-time assertions.
var _ Reranker = (*CohereReranker)(nil)
var _ TokenCounter = (*CohereReranker)(nil)
