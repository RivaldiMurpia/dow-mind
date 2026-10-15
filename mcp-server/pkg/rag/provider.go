// Package rag abstracts vector stores and embedding backends used by the MCP server.
// The Provider interface is intentionally small: one project = one collection/index.
package rag

import (
	"context"
	"unicode/utf8"
)

// Document is a unit of content stored in a RAG collection.
type Document struct {
	ID      string
	Content string
	Meta    map[string]string
}

// Result is a retrieved chunk with its similarity score.
//
// InDense/InLexical are optional origin flags populated only by the hybrid
// pgvector path (SemanticSearchHybrid); they indicate whether the result
// appeared in the dense and/or lexical ranking before RRF fusion. Other
// backends and the dense-only path leave them false. They are additive and
// omitempty, so existing consumers are unaffected.
type Result struct {
	ID        string            `json:"id"`
	Content   string            `json:"content"`
	Score     float64           `json:"score"`
	Meta      map[string]string `json:"meta,omitempty"`
	InDense   bool              `json:"in_dense,omitempty"`
	InLexical bool              `json:"in_lexical,omitempty"`
}

// Provider describes a RAG backend capable of per-project collections.
type Provider interface {
	// CreateCollection creates the project collection if it doesn't exist.
	CreateCollection(ctx context.Context, projectID string) error

	// DeleteCollection removes the project collection.
	DeleteCollection(ctx context.Context, projectID string) error

	// Index inserts documents into the project collection, embedding them.
	Index(ctx context.Context, projectID string, docs []Document) error

	// SemanticSearch returns the k most relevant chunks for a query.
	SemanticSearch(ctx context.Context, projectID, query string, limit int) ([]Result, error)

	// Embed returns the vector for a single text (useful for debugging).
	Embed(ctx context.Context, text string) ([]float32, error)

	// DeletePoints removes specific points by ID from the project collection.
	DeletePoints(ctx context.Context, projectID string, pointIDs []string) error

	// ListPointIDs returns all point IDs in the project collection, optionally filtered by metadata.
	ListPointIDs(ctx context.Context, projectID string, metaFilter map[string]string) ([]string, error)

	// ListPoints returns all points (ID + source_file payload) in the project
	// collection, optionally filtered by metadata.
	ListPoints(ctx context.Context, projectID string, metaFilter map[string]string) ([]PointInfo, error)

	// Close releases resources.
	Close() error
}

// EmbeddingClient abstracts text embedding models (TEI, OpenAI, local, etc.).
type EmbeddingClient interface {
	Embed(ctx context.Context, texts []string) ([][]float32, error)
	VectorSize() int
}

// QueryEmbedder is an optional interface for embedders that distinguish
// query vs document inputs (e.g. Voyage AI input_type=query). Providers check
// for this via type assertion in SemanticSearch and prefer EmbedQuery when
// available, falling back to Embed otherwise.
type QueryEmbedder interface {
	EmbedQuery(ctx context.Context, text string) ([]float32, error)
}

// ModelNamer is an optional interface for embedders that expose their model
// name. Providers use this in Index() to inject embed_model into document
// metadata before persisting.
type ModelNamer interface {
	ModelName() string
}

// TokenCounter is an optional interface implemented by embedding clients and
// rerankers that track cumulative token usage reported by their upstream API
// (e.g. Voyage AI returns usage.total_tokens). core.Service type-asserts the
// provider and reranker for this interface to report token metrics; backends
// that cannot report tokens (e.g. TEI) simply do not implement it, and the
// metrics layer reports zero honestly. Providers may forward to their
// embedder's counter when the embedder implements this interface.
type TokenCounter interface {
	// TokensUsed returns the total tokens consumed since process start.
	TokensUsed() int64
}

// HybridSearcher is an optional interface for providers that support hybrid
// search combining dense vector similarity with lexical full-text search
// using Reciprocal Rank Fusion (RRF). Providers that implement this
// interface are used by core.Service.Search when opts.Hybrid is true.
// Providers that do not implement it fall back to dense-only SemanticSearch.
type HybridSearcher interface {
	SemanticSearchHybrid(ctx context.Context, projectID, query string, limit int) ([]Result, error)
}

// RerankHit is a single reranked result: the Index of the original document
// in the input slice and its relevance Score from the reranker.
type RerankHit struct {
	Index int     `json:"index"`
	Score float64 `json:"relevance_score"`
}

// Reranker is an optional interface for reranking retrieved documents.
// Implementations (e.g. VoyageReranker) call a rerank API to re-order
// candidates by relevance to the query.
type Reranker interface {
	Rerank(ctx context.Context, query string, docs []string, topK int) ([]RerankHit, error)
}

// Exporter is an optional interface for providers that can export every stored
// point of a project with its FULL content and metadata (unlike ListPoints,
// which truncates content for previews). This is the read side of migration /
// re-embedding: the returned Documents can be re-embedded by a destination
// provider. ID is the original doc_id and Meta carries the full stored
// metadata (source_file, content_hash, chunk_version, embed_model, etc.) so a
// migration can preserve identity for incremental sync.
type Exporter interface {
	ExportPoints(ctx context.Context, projectID string) ([]Document, error)
}

// PointPager is an optional extension interface for providers that support
// page-wise listing of points without scrolling the whole collection. The
// first page call should pass offset=0; the returned total lets callers page
// deterministically (offset += limit until offset >= total). Providers that
// do not implement it fall back to a full ListPoints + in-memory slice in
// core.Service, which is correct but O(n) per page.
type PointPager interface {
	// ListPointsPage returns up to limit points starting at offset (skipping
	// that many matching points), plus the TOTAL number of points matching
	// metaFilter. limit and offset arrive already validated/clamped by the
	// service layer.
	ListPointsPage(ctx context.Context, projectID string, metaFilter map[string]string, limit, offset int) ([]PointInfo, int, error)
}

// PointPayloadUpdater is an optional provider extension for setting payload
// fields on EXISTING points identified by their ORIGINAL doc_id. All stored
// points sharing that doc_id (e.g. every chunk of a split document) are
// updated. Used for supersede bookkeeping: when a new document supersedes an
// old one, the old points are tagged superseded_by=<new doc id> so the
// service can skip them in retrieval by default.
type PointPayloadUpdater interface {
	SetPayloadByDocID(ctx context.Context, projectID, docID string, payload map[string]any) error
}

// PointPreviewMaxChars bounds the content preview attached to point listings
// across providers (kept identical to the core service clamp so every layer
// truncates to the same length).
const PointPreviewMaxChars = 300

// TruncateUTF8 cuts s to at most max BYTES without splitting a rune: trailing
// partial runes are trimmed so the result is always valid UTF-8. Preview
// clamps that cut raw bytes can otherwise end mid-rune (e.g. bug-bounty
// writeups full of multibyte characters) and produce garbage in output.
func TruncateUTF8(s string, max int) string {
	if len(s) <= max {
		return s
	}
	cut := s[:max]
	for len(cut) > 0 && !utf8.ValidString(cut) {
		cut = cut[:len(cut)-1]
	}
	return cut
}

// PointInfo is a stored point's ID together with the payload fields needed to
// reconcile it against the current file set during incremental sync. Content
// and ChunkIndex are populated by ListPoints when the UI needs to display
// chunk previews (e.g. the Chunks page).
type PointInfo struct {
	ID           string `json:"id"`
	SourceFile   string `json:"source_file,omitempty"`
	ContentHash  string `json:"content_hash,omitempty"`
	ChunkVersion string `json:"chunk_version,omitempty"`
	DocID        string `json:"doc_id,omitempty"` // original document ID (Qdrant stores UUID, doc_id preserves the original)
	Content      string `json:"content,omitempty"`
	ChunkIndex   string `json:"chunk_index,omitempty"`
}
