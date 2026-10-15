// Package core provides the service layer shared by MCP stdio and HTTP API.
// It wraps a rag.Provider, an optional rag.Reranker, and an *indexer.Indexer
// behind a single Service struct with methods that both transport layers call.
package core

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"

	"github.com/google/uuid"

	"github.com/RivaldiMurpia/dow-mind/pkg/indexer"
	"github.com/RivaldiMurpia/dow-mind/pkg/rag"
)

// DefaultK is the default number of final results returned by Search.
const DefaultK = 5

// DefaultRecall is the default number of candidates retrieved before rerank.
const DefaultRecall = 40

// SearchOpts controls the behaviour of Service.Search.
type SearchOpts struct {
	K        int  // final top-K results (default 5)
	Recall   int  // retrieval recall before rerank (default 40)
	Hybrid   bool // use dense+lexical RRF (provider must support it)
	Rerank   bool // use reranker if configured
	Compress bool // drop near-duplicate results (same content_hash / identical content)
	// MinScore is a relevance floor: results scoring below it are dropped
	// after rerank/ranking but BEFORE the top-K cutoff, so K still means
	// "up to K good results". 0 (default) disables the floor.
	MinScore float64
	// MaxChars is the character budget for RetrieveContext's concatenated
	// context: chunks are appended best-first until the budget is exhausted.
	// 0 = default (DefaultMaxContextChars), negative = no cap. Ignored by
	// Search.
	MaxChars int
	// IncludeSuperseded disables the default skip of points whose metadata
	// carries superseded_by (i.e. documents replaced by a newer write).
	// Default false: superseded memories stay out of retrieval because a
	// superseding document is, by definition, the current truth.
	IncludeSuperseded bool
}

// ProjectStat holds per-project statistics returned by ListProjects.
type ProjectStat struct {
	ProjectID  string `json:"project_id"`
	ChunkCount int    `json:"chunk_count"`
}

// Event is a single SSE event published by the EventBus.
type Event struct {
	Type      string    `json:"type"` // e.g. "index_started", "query_executed"
	Timestamp time.Time `json:"timestamp"`
	Data      any       `json:"data,omitempty"`
}

// EventBus is a simple pub/sub mechanism for SSE events. Subscribers receive
// events on a buffered channel; the bus is safe for concurrent use.
type EventBus struct {
	mu          sync.Mutex
	subscribers map[chan Event]struct{}
}

// NewEventBus creates a new EventBus.
func NewEventBus() *EventBus {
	return &EventBus{
		subscribers: make(map[chan Event]struct{}),
	}
}

// Subscribe returns a buffered channel on which events are delivered.
// The caller must call Unsubscribe with the returned channel to stop
// receiving and allow the bus to clean up.
func (b *EventBus) Subscribe() chan Event {
	ch := make(chan Event, 64)
	b.mu.Lock()
	b.subscribers[ch] = struct{}{}
	b.mu.Unlock()
	return ch
}

// Unsubscribe removes a subscriber channel and closes it.
func (b *EventBus) Unsubscribe(ch chan Event) {
	b.mu.Lock()
	delete(b.subscribers, ch)
	b.mu.Unlock()
	// Close the channel so any range-loop consumer exits.
	close(ch)
}

// Publish sends an event to all current subscribers. If a subscriber's
// channel buffer is full the event is dropped for that subscriber (non-blocking).
func (b *EventBus) Publish(ev Event) {
	b.mu.Lock()
	subs := make([]chan Event, 0, len(b.subscribers))
	for ch := range b.subscribers {
		subs = append(subs, ch)
	}
	b.mu.Unlock()

	if ev.Timestamp.IsZero() {
		ev.Timestamp = time.Now()
	}
	for _, ch := range subs {
		select {
		case ch <- ev:
		default: // drop if buffer full
		}
	}
}

// Service wraps a provider, an optional reranker, and an indexer behind a
// single API used by both the MCP stdio handlers and the HTTP API layer.
type Service struct {
	provider     rag.Provider
	reranker     rag.Reranker // may be nil
	indexer      *indexer.Indexer
	events       *EventBus
	embedModel   string // optional: set by main.go for stats endpoint
	metrics      *Metrics
	metricsStore MetricsStore // optional durable metrics; nil = in-memory only
	backend      string       // vector store name (e.g. "qdrant"), set by main.go
	// watches holds active directory watches (projectID -> handle).
	// The registry is persisted to ~/.dow-mind/watches.json and restored
	// on server start (see RestoreWatches).
	watchMu sync.Mutex
	watches map[string]*watchHandle
	// watchesPath overrides the persisted-watch location; empty means the
	// default (config.WatchesPath()). Tests set this to a temp dir.
	watchesPath string
}

// MetricsStore is an optional durable sink for query metrics, injected into
// Service (not a provider method — that would create a rag→core import cycle).
// When set, Service persists each query and reads durable aggregates so the
// dashboard survives restarts; when nil, metrics are in-memory only and the
// snapshot reports Persistent=false. SQLiteMetricsStore is the default
// implementation and works with any vector-store backend.
type MetricsStore interface {
	// PersistQueryMetric durably records one query's latency and composition.
	PersistQueryMetric(ctx context.Context, latencyMs float64, comp QueryComposition) error
	// Summary returns durable aggregates across all persisted queries.
	Summary(ctx context.Context) (MetricsSummary, error)
}

// MetricsSummary holds durable metric aggregates read back from a MetricsStore.
type MetricsSummary struct {
	QueryCount   int64
	AvgLatencyMs float64
	P50LatencyMs float64
	P95LatencyMs float64
}

// MetricsSnapshot is the JSON-serializable metrics response returned by
// Service.MetricsSnapshot and the GET /api/metrics endpoint.
type MetricsSnapshot struct {
	QueryCount   int64             `json:"query_count"`
	AvgLatencyMs float64           `json:"avg_latency_ms"`
	P50LatencyMs float64           `json:"p50_latency_ms"`
	P95LatencyMs float64           `json:"p95_latency_ms"`
	TokensTotal  int64             `json:"tokens_total"`
	TokensEmbed  int64             `json:"tokens_embed"`
	TokensRerank int64             `json:"tokens_rerank"`
	Persistent   bool              `json:"persistent"` // true iff backend implements MetricsStore
	Backend      string            `json:"backend"`
	LastQuery    *QueryComposition `json:"last_query,omitempty"` // nil until first query
}

// NewService creates a Service from the given components. reranker may be nil.
// indexer may be nil; if so, IndexProject will create one with default chunk size.
func NewService(provider rag.Provider, reranker rag.Reranker, idx *indexer.Indexer) *Service {
	svc := &Service{
		provider: provider,
		reranker: reranker,
		indexer:  idx,
		events:   NewEventBus(),
		metrics:  NewMetrics(),
	}
	return svc
}

// Events returns the EventBus for SSE publishing/subscribing.
func (s *Service) Events() *EventBus {
	return s.events
}

// Provider returns the underlying rag.Provider. This is intended for
// advanced use cases where the caller needs direct provider access.
func (s *Service) Provider() rag.Provider {
	return s.provider
}

// SetEmbedModel sets the embedding model name used by the service. This is
// used by the HTTP API stats endpoint to report the active embedding model.
func (s *Service) SetEmbedModel(model string) {
	s.embedModel = model
}

// EmbedModel returns the embedding model name, or "unknown" if not set.
func (s *Service) EmbedModel() string {
	if s.embedModel != "" {
		return s.embedModel
	}
	return "unknown"
}

// SetBackend records the active vector store name (e.g. "qdrant", "pgvector")
// so the metrics snapshot can report it honestly.
func (s *Service) SetBackend(name string) {
	s.backend = name
}

// SetMetricsStore injects a durable metrics store. When set, query metrics are
// persisted and the snapshot reports Persistent=true with durable aggregates.
func (s *Service) SetMetricsStore(store MetricsStore) {
	s.metricsStore = store
}

// MetricsSnapshot returns a point-in-time view of query metrics: in-memory
// latency aggregates and query count, plus token usage read from the provider
// and reranker when they implement rag.TokenCounter. Persistent reflects
// whether the backend can store metrics durably (implements MetricsStore).
func (s *Service) MetricsSnapshot(ctx context.Context) MetricsSnapshot {
	avg, p50, p95, count, comp, haveComp := s.metrics.snapshot()

	var embedTok, rerankTok int64
	if tc, ok := s.provider.(rag.TokenCounter); ok {
		embedTok = tc.TokensUsed()
	}
	if tc, ok := s.reranker.(rag.TokenCounter); ok {
		rerankTok = tc.TokensUsed()
	}
	// When a durable store is configured, prefer its aggregates so counts and
	// latencies survive restarts. Fall back to in-memory on read error.
	persistent := s.metricsStore != nil
	if persistent {
		if sum, err := s.metricsStore.Summary(ctx); err == nil {
			count = sum.QueryCount
			avg = sum.AvgLatencyMs
			p50 = sum.P50LatencyMs
			p95 = sum.P95LatencyMs
		}
	}

	snap := MetricsSnapshot{
		QueryCount:   count,
		AvgLatencyMs: avg,
		P50LatencyMs: p50,
		P95LatencyMs: p95,
		TokensEmbed:  embedTok,
		TokensRerank: rerankTok,
		TokensTotal:  embedTok + rerankTok,
		Persistent:   persistent,
		Backend:      s.backend,
	}
	if haveComp {
		c := comp
		snap.LastQuery = &c
	}
	return snap
}

// retrieveCandidates fetches recall candidates from the provider. When hybrid
// is true and the provider implements HybridSearcher, it uses the hybrid
// search path (dense + lexical RRF). Otherwise it falls back to dense-only
// SemanticSearch.
func (s *Service) retrieveCandidates(ctx context.Context, projectID, query string, recall int, hybrid bool) ([]rag.Result, error) {
	if hybrid {
		if hs, ok := s.provider.(rag.HybridSearcher); ok {
			return hs.SemanticSearchHybrid(ctx, projectID, query, recall)
		}
	}
	return s.provider.SemanticSearch(ctx, projectID, query, recall)
}

// Search performs a semantic search with optional reranking.
//
// Flow:
//  1. Retrieve `recall` candidates via provider.SemanticSearch.
//  2. If rerank is requested and a reranker is configured, rerank candidates
//     and return the top-K results with updated scores.
//  3. If the reranker fails, fall back to semantic order truncated to K.
//  4. If no rerank, truncate to K.
//
// Defaults: K=5, Recall=40.
func (s *Service) Search(ctx context.Context, projectID, query string, opts SearchOpts) ([]rag.Result, error) {
	k := opts.K
	if k <= 0 {
		k = DefaultK
	}
	recall := opts.Recall
	if recall <= 0 {
		recall = DefaultRecall
	}

	// Measure retrieval latency around the provider call only (excludes rerank,
	// which is measured separately by the reranker's own metrics if needed).
	t0 := time.Now()
	cands, err := s.retrieveCandidates(ctx, projectID, query, recall, opts.Hybrid)
	latencyMs := float64(time.Since(t0).Microseconds()) / 1000.0
	if err != nil {
		return nil, err
	}
	// Superseded points are skipped by default (before reranking and the
	// top-K cutoff, so K still counts only live results). This is where the
	// episodic-memory protocol stays honest: a replaced memory must not
	// resurface just because its embedding is still close to the query.
	if !opts.IncludeSuperseded {
		cands = dropSuperseded(cands)
	}

	hybridUsed := opts.Hybrid && s.providerSupportsHybrid()
	comp := QueryComposition{
		Hybrid:     hybridUsed,
		Candidates: len(cands),
	}
	// Compute dense/lexical composition from per-result origin when the hybrid
	// path populated it (pgvector). Non-hybrid or backends that don't project
	// origin leave these at zero — an honest fallback.
	for _, c := range cands {
		if c.InDense {
			comp.DenseCount++
		}
		if c.InLexical {
			comp.LexicalCount++
		}
	}

	// Record metrics once, at return, regardless of which branch produced out.
	out := cands
	defer func() {
		comp.Results = len(out)
		s.metrics.RecordQuery(latencyMs, comp)
		if s.metricsStore != nil {
			// Persist asynchronously so query latency is unaffected. A copy of
			// comp is captured; failures are non-fatal.
			c := comp
			lat := latencyMs
			go func() { _ = s.metricsStore.PersistQueryMetric(context.WithoutCancel(context.Background()), lat, c) }()
		}
	}()

	// Publish a query event for SSE listeners.
	s.events.Publish(Event{
		Type:      "query_executed",
		Timestamp: time.Now(),
		Data: map[string]any{
			"project_id": projectID,
			"query":      query,
			"candidates": len(cands),
		},
	})

	if opts.Rerank && s.reranker != nil && len(cands) > 0 {
		docs := make([]string, len(cands))
		for i, c := range cands {
			docs[i] = c.Content
		}
		hits, rerr := s.reranker.Rerank(ctx, query, docs, k)
		if rerr == nil && len(hits) > 0 {
			reranked := make([]rag.Result, 0, len(hits))
			for _, h := range hits {
				if h.Index < 0 || h.Index >= len(cands) {
					continue
				}
				r := cands[h.Index]
				r.Score = h.Score
				reranked = append(reranked, r)
			}
			// Relevance floor (after rerank scoring, before the top-K cutoff
			// so K still means "up to K good results"), then a defensive
			// clamp: don't rely on the reranker API honoring top_k.
			reranked = applyMinScore(reranked, opts.MinScore)
			if len(reranked) > k {
				reranked = reranked[:k]
			}
			comp.Reranked = true
			comp.RerankMoved = rerankMoved(cands, reranked)
			out = maybeCompress(reranked, opts.Compress)
			stampAge(out)
			return out, nil
		}
		// Reranker failed or returned empty: fall back to semantic order.
	}

	cands = applyMinScore(cands, opts.MinScore)
	if len(cands) > k {
		cands = cands[:k]
	}
	out = maybeCompress(cands, opts.Compress)
	stampAge(out)
	return out, nil
}

// applyMinScore drops results scoring below the relevance floor. min<=0 is a
// no-op (fully backward compatible default). Input must already be ranked;
// the filter preserves order.
func applyMinScore(results []rag.Result, min float64) []rag.Result {
	if min <= 0 {
		return results
	}
	out := results[:0:0] // new backing array; don't mutate caller's slice
	for _, r := range results {
		if r.Score >= min {
			out = append(out, r)
		}
	}
	return out
}

// providerSupportsHybrid reports whether the provider implements the hybrid
// search path, mirroring the type assertion in retrieveCandidates.
func (s *Service) providerSupportsHybrid() bool {
	_, ok := s.provider.(rag.HybridSearcher)
	return ok
}

// rerankMoved counts how many of the reranked results changed position relative
// to their original order in cands (by result ID).
func rerankMoved(cands, reranked []rag.Result) int {
	origPos := make(map[string]int, len(cands))
	for i, c := range cands {
		origPos[c.ID] = i
	}
	moved := 0
	for newPos, r := range reranked {
		if old, ok := origPos[r.ID]; ok && old != newPos {
			moved++
		}
	}
	return moved
}

// maybeCompress removes near-duplicate results when compress is true, preserving
// input order (which is already score-sorted). Two results are considered
// duplicates when they share a non-empty content_hash or have identical content.
func maybeCompress(results []rag.Result, compress bool) []rag.Result {
	if !compress || len(results) < 2 {
		return results
	}
	seenHash := make(map[string]struct{}, len(results))
	seenContent := make(map[string]struct{}, len(results))
	out := results[:0:0] // new backing array; don't mutate caller's slice
	for _, r := range results {
		hash := r.Meta["content_hash"]
		if hash != "" {
			if _, dup := seenHash[hash]; dup {
				continue
			}
			seenHash[hash] = struct{}{}
		}
		if _, dup := seenContent[r.Content]; dup {
			continue
		}
		seenContent[r.Content] = struct{}{}
		out = append(out, r)
	}
	return out
}

// IndexProject scans the given directory and indexes all code/text files
// into the project collection. Delegates to the indexer.
func (s *Service) IndexProject(ctx context.Context, projectID, dir string) (*indexer.SyncResult, error) {
	s.events.Publish(Event{
		Type:      "index_started",
		Timestamp: time.Now(),
		Data:      map[string]any{"project_id": projectID, "directory": dir},
	})

	// Ensure the collection exists before indexing. The MCP flow calls
	// CreateProject first, but the HTTP reindex endpoint indexes a project
	// directly, so a first-time index would otherwise fail with a missing
	// collection. CreateCollection is idempotent across providers.
	if err := s.provider.CreateCollection(ctx, projectID); err != nil {
		s.events.Publish(Event{
			Type:      "index_failed",
			Timestamp: time.Now(),
			Data:      map[string]any{"project_id": projectID, "error": err.Error()},
		})
		return nil, err
	}

	idx := s.indexer
	if idx == nil {
		idx = indexer.NewIndexer(s.provider, 1500)
	}

	result, err := idx.IndexProject(ctx, projectID, dir)
	if err != nil {
		s.events.Publish(Event{
			Type:      "index_failed",
			Timestamp: time.Now(),
			Data:      map[string]any{"project_id": projectID, "error": err.Error()},
		})
		return nil, err
	}

	s.events.Publish(Event{
		Type:      "index_completed",
		Timestamp: time.Now(),
		Data: map[string]any{
			"project_id":    projectID,
			"indexed":       result.Indexed,
			"deleted":       result.Deleted,
			"files_scanned": result.FilesScanned,
			"skipped":       result.Skipped,
		},
	})

	return result, nil
}

// ListProjects returns statistics for all known projects. It queries the
// provider's ListPoints for each project ID it can discover.
// Since the Provider interface doesn't have a "list collections" method,
// this method uses a best-effort approach by checking known project IDs
// via the provider. The caller may pass project IDs to check.
func (s *Service) ListProjects(ctx context.Context) ([]ProjectStat, error) {
	// The Provider interface does not expose a "list collections" method.
	// We rely on a helper that may be implemented by specific providers.
	// For now, we use a type assertion to check if the provider supports
	// listing project IDs.
	lister, ok := s.provider.(ProjectLister)
	if ok {
		ids, err := lister.ListProjectIDs(ctx)
		if err != nil {
			return nil, err
		}
		// Prefer an efficient count when the provider supports it. Otherwise
		// fall back to counting via ListPoints (which scrolls every point with
		// its payload — correct but slow for large projects).
		counter, hasCounter := s.provider.(ProjectCounter)
		stats := make([]ProjectStat, 0, len(ids))
		for _, id := range ids {
			var count int
			if hasCounter {
				c, err := counter.CountPoints(ctx, id)
				if err != nil {
					continue
				}
				count = c
			} else {
				points, err := s.provider.ListPoints(ctx, id, nil)
				if err != nil {
					continue
				}
				count = len(points)
			}
			stats = append(stats, ProjectStat{
				ProjectID:  id,
				ChunkCount: count,
			})
		}
		return stats, nil
	}
	return []ProjectStat{}, nil
}

// ProjectCounter is an optional interface for providers that can count points
// in a project cheaply (e.g. Qdrant points/count, pgvector COUNT(*)), avoiding
// a full ListPoints scroll just to size a project.
type ProjectCounter interface {
	CountPoints(ctx context.Context, projectID string) (int, error)
}

// ProjectLister is an optional interface that providers may implement to
// support listing all project IDs. This allows ListProjects to enumerate
// collections without prior knowledge.
type ProjectLister interface {
	ListProjectIDs(ctx context.Context) ([]string, error)
}

// CreateProject creates a new RAG collection for the given project.
func (s *Service) CreateProject(ctx context.Context, projectID string) error {
	if err := s.provider.CreateCollection(ctx, projectID); err != nil {
		return err
	}
	s.events.Publish(Event{
		Type:      "project_created",
		Timestamp: time.Now(),
		Data:      map[string]any{"project_id": projectID},
	})
	return nil
}

// DeleteProject deletes the project collection and all its indexed memory.
func (s *Service) DeleteProject(ctx context.Context, projectID string) error {
	if err := s.provider.DeleteCollection(ctx, projectID); err != nil {
		return err
	}
	s.events.Publish(Event{
		Type:      "project_deleted",
		Timestamp: time.Now(),
		Data:      map[string]any{"project_id": projectID},
	})
	return nil
}

// List-points pagination controls for the mind_list_points MCP tool and the
// paged listing API. Pages keep a single tool call from pulling an entire
// large collection into the transcript.
const (
	// DefaultListPointsLimit is the page size used when the caller does not
	// specify a limit.
	DefaultListPointsLimit = 50
	// MaxListPointsLimit caps the page size; values above it are clamped, not
	// rejected, so an over-eager caller still cannot pull the whole collection.
	MaxListPointsLimit = 500
	// PointPreviewMaxChars bounds the content preview in list output. Full
	// content stays available via search / retrieve / export.
	PointPreviewMaxChars = 300
)

// ListPointsPaged returns one page of points (ID + metadata + content
// preview) plus the TOTAL number of points matching the filter, so callers
// can page with offset without a separate count call. limit<=0 uses
// DefaultListPointsLimit; limit>MaxListPointsLimit is clamped. Providers that
// implement rag.PointPager are paged server-side; others fall back to a full
// ListPoints sliced in memory (correct, but O(n) per page).
func (s *Service) ListPointsPaged(ctx context.Context, projectID string, metaFilter map[string]string, limit, offset int) ([]rag.PointInfo, int, error) {
	if limit <= 0 {
		limit = DefaultListPointsLimit
	}
	if limit > MaxListPointsLimit {
		limit = MaxListPointsLimit
	}
	if offset < 0 {
		offset = 0
	}

	if pager, ok := s.provider.(rag.PointPager); ok {
		points, total, err := pager.ListPointsPage(ctx, projectID, metaFilter, limit, offset)
		if err != nil {
			return nil, 0, err
		}
		clampPointPreviews(points)
		return points, total, nil
	}

	// Fallback for providers without page support: scroll everything, then
	// slice the requested page in memory.
	all, err := s.provider.ListPoints(ctx, projectID, metaFilter)
	if err != nil {
		return nil, 0, err
	}
	total := len(all)
	if offset >= total {
		return []rag.PointInfo{}, total, nil
	}
	end := offset + limit
	if end > total {
		end = total
	}
	page := all[offset:end]
	clampPointPreviews(page)
	return page, total, nil
}

// clampPointPreviews truncates each point's Content in place to
// PointPreviewMaxChars so list output stays bounded regardless of backend.
func clampPointPreviews(points []rag.PointInfo) {
	for i := range points {
		if len(points[i].Content) > PointPreviewMaxChars {
			points[i].Content = points[i].Content[:PointPreviewMaxChars]
		}
	}
}

// ListPoints returns all points (ID + metadata) in the project collection,
// optionally filtered by metadata. Prefer ListPointsPaged for user-facing
// listing: it bounds both page size and content preview.
func (s *Service) ListPoints(ctx context.Context, projectID string, metaFilter map[string]string) ([]rag.PointInfo, error) {
	return s.provider.ListPoints(ctx, projectID, metaFilter)
}

// ExportProject returns every point of a project with full content and metadata,
// for migration / re-embedding. Requires the provider to implement rag.Exporter
// (all built-in providers do); returns an error otherwise.
func (s *Service) ExportProject(ctx context.Context, projectID string) ([]rag.Document, error) {
	exporter, ok := s.provider.(rag.Exporter)
	if !ok {
		return nil, fmt.Errorf("this vector store does not support export")
	}
	return exporter.ExportPoints(ctx, projectID)
}

// ProjectExists checks whether a project with the given ID has any indexed
// data. It first tries the ProjectLister interface (if the provider supports
// it) for an O(1) set lookup; otherwise it falls back to ListPoints which
// works for all providers. Returns false if the project does not exist or
// an error occurs during the check.
func (s *Service) ProjectExists(ctx context.Context, projectID string) bool {
	// Fast path: if the provider can list project IDs, check membership.
	if lister, ok := s.provider.(ProjectLister); ok {
		ids, err := lister.ListProjectIDs(ctx)
		if err != nil {
			return false
		}
		for _, id := range ids {
			if id == projectID {
				return true
			}
		}
		return false
	}
	// Fallback: query ListPoints for the project. If it returns nil or an
	// error, the project doesn't exist.
	points, err := s.provider.ListPoints(ctx, projectID, nil)
	if err != nil {
		return false
	}
	return points != nil
}

// DeletePoints removes specific points by ID from the project collection.
func (s *Service) DeletePoints(ctx context.Context, projectID string, pointIDs []string) error {
	if err := s.provider.DeletePoints(ctx, projectID, pointIDs); err != nil {
		return err
	}
	s.events.Publish(Event{
		Type:      "points_deleted",
		Timestamp: time.Now(),
		Data:      map[string]any{"project_id": projectID, "count": len(pointIDs)},
	})
	return nil
}

// DefaultDuplicateThreshold is the similarity score above which a new
// document is considered a duplicate of an existing point and is NOT
// inserted unless the caller forces it (meta["force"]=true). Score scales
// are provider-specific but all current backends are cosine-based (0..1).
const DefaultDuplicateThreshold = 0.9

// DuplicateHit reports a document that was NOT indexed because it is too
// similar to an existing point. duplicate_of is the existing point's ID.
type DuplicateHit struct {
	DocID       string  `json:"doc_id"`
	DuplicateOf string  `json:"duplicate_of"`
	Score       float64 `json:"score"`
}

// IndexReport is the outcome of a batch IndexDocuments call.
type IndexReport struct {
	Indexed    int            `json:"indexed"`    // documents actually stored
	Duplicates []DuplicateHit `json:"duplicates"` // rejected as near-duplicates
}

// IndexDocuments indexes a batch of documents into the project collection
// directly (without scanning a directory). This is used by the mind_index MCP
// tool. Documents larger than the chunk size are split with the SAME chunker
// the directory scan flow uses (indexer.ChunkDocument); each resulting chunk
// keeps the original document's metadata and gains doc_id / chunk_index /
// content_hash so chunks remain attributable to their source document.
//
// Before writing, each document is checked against the project's existing
// points: when the best match scores >= DefaultDuplicateThreshold the
// document is NOT inserted and is reported as a duplicate. Set
// meta["force"]="true" to insert anyway (superseding documents are exempt:
// replacing a memory with near-identical text is the point of supersedes).
func (s *Service) IndexDocuments(ctx context.Context, projectID string, docs []rag.Document) (IndexReport, error) {
	var report IndexReport
	chunked := make([]rag.Document, 0, len(docs))
	chunkSize := indexer.DefaultChunkSize
	if s.indexer != nil {
		chunkSize = s.indexer.ChunkSize()
	}

	for _, d := range docs {
		// Dedup guard: compare against what is ALREADY stored. A superseding
		// document intentionally re-states the old one, so it skips the check.
		if d.Meta["force"] != "true" && d.Meta["supersedes"] == "" {
			hits, err := s.provider.SemanticSearch(ctx, projectID, d.Content, 1)
			if err != nil {
				return report, fmt.Errorf("dedup check %q: %w", d.ID, err)
			}
			if len(hits) > 0 && hits[0].Score >= DefaultDuplicateThreshold {
				report.Duplicates = append(report.Duplicates, DuplicateHit{
					DocID:       d.ID,
					DuplicateOf: hits[0].ID,
					Score:       hits[0].Score,
				})
				continue
			}
		}
		// force is write-time control input, not memory content: strip it so
		// it does not leak into the stored payload.
		if d.Meta["force"] != "" {
			meta := make(map[string]string, len(d.Meta))
			for k, v := range d.Meta {
				meta[k] = v
			}
			delete(meta, "force")
			d.Meta = meta
		}
		if len(d.Content) <= chunkSize {
			chunked = append(chunked, d)
			continue
		}
		// A missing doc ID would make every chunk collide on the same
		// deterministic provider point ID ("<empty>#chunk0"), so generate one.
		base := d.ID
		if base == "" {
			base = uuid.NewString()
		}
		// Source-file metadata (when present) lets AST chunking pick the
		// right tree-sitter grammar; otherwise the heuristic splitter runs.
		filename := ""
		if d.Meta != nil {
			filename = d.Meta["source_file"]
		}
		chunks := indexer.ChunkDocument(filename, d.Content, chunkSize)
		if len(chunks) <= 1 {
			// Nothing splittable — index the document unchanged.
			chunked = append(chunked, d)
			continue
		}
		for i, chunk := range chunks {
			meta := make(map[string]string, len(d.Meta)+3)
			for k, v := range d.Meta {
				meta[k] = v
			}
			if d.ID != "" {
				meta["doc_id"] = d.ID
			}
			meta["chunk_index"] = strconv.Itoa(i)
			meta["content_hash"] = indexer.ContentHash(chunk)
			chunked = append(chunked, rag.Document{
				ID:      fmt.Sprintf("%s#chunk%d", base, i),
				Content: chunk,
				Meta:    meta,
			})
		}
	}

	report.Indexed = len(chunked)
	if err := s.provider.Index(ctx, projectID, chunked); err != nil {
		return report, fmt.Errorf("index documents: %w", err)
	}
	// Supersede bookkeeping: a document whose metadata carries supersedes=<old
	// doc id> tags every stored point of the OLD document with
	// superseded_by=<new doc id>, so retrieval skips it by default. Done after
	// the new points are durably stored so a failure here can never leave the
	// corpus with BOTH copies hidden.
	if err := s.applySupersedes(ctx, projectID, docs); err != nil {
		return report, err
	}
	s.events.Publish(Event{
		Type:      "documents_indexed",
		Timestamp: time.Now(),
		Data:      map[string]any{"project_id": projectID, "count": len(docs), "duplicates": len(report.Duplicates)},
	})
	return report, nil
}

// DefaultMaxContextChars is the default character budget for
// RetrieveContext's concatenated context string (~2k tokens at 4 chars/token).
const DefaultMaxContextChars = 8000

// RetrieveContext performs a semantic search and returns a concatenated
// context string suitable for feeding into an LLM, along with the kept raw
// results. opts.MaxChars caps the context AFTER ranking: chunks are appended
// best-first until the budget is exhausted, and when any chunk is cut the
// string ends with a "[truncated: N chunks omitted]" marker (the omitted
// chunks are also absent from the returned results). MaxChars semantics:
// 0 = default budget (DefaultMaxContextChars), negative = no cap.
func (s *Service) RetrieveContext(ctx context.Context, projectID, query string, opts SearchOpts) (string, []rag.Result, error) {
	if opts.K <= 0 {
		opts.K = DefaultK
	}
	results, err := s.Search(ctx, projectID, query, opts)
	if err != nil {
		return "", nil, err
	}

	budget := opts.MaxChars
	if budget == 0 {
		budget = DefaultMaxContextChars
	}

	var b strings.Builder
	kept := make([]rag.Result, 0, len(results))
	used := 0
	omitted := 0
	for i, r := range results {
		part := fmt.Sprintf("[score %.3f] %s", r.Score, r.Content)
		sepLen := 0
		if used > 0 {
			sepLen = 2 // "\n\n" between chunks
		}
		if budget > 0 && used+sepLen+len(part) > budget {
			if i == 0 {
				// Even the best chunk alone exceeds the budget: include it
				// truncated rather than returning an empty context.
				cut := part[:budget]
				for len(cut) > 0 && !utf8.ValidString(cut) {
					cut = cut[:len(cut)-1]
				}
				b.WriteString(cut)
				used = budget
				kept = append(kept, r)
			}
			omitted = len(results) - len(kept)
			break
		}
		if used > 0 {
			b.WriteString("\n\n")
		}
		b.WriteString(part)
		used += sepLen + len(part)
		kept = append(kept, r)
	}
	if omitted > 0 {
		fmt.Fprintf(&b, "\n\n[truncated: %d chunks omitted]", omitted)
	}
	return b.String(), kept, nil
}

// dropSuperseded removes results whose metadata marks them as replaced by a
// newer document (superseded_by). The filter returns a new slice so the
// caller's candidate slice is never mutated.
func dropSuperseded(results []rag.Result) []rag.Result {
	out := results[:0:0]
	for _, r := range results {
		if r.Meta["superseded_by"] != "" {
			continue
		}
		out = append(out, r)
	}
	return out
}

// stampAge annotates each result's metadata with a human-readable age derived
// from its created_at timestamp (e.g. "5m", "3h", "12d"), so a consumer can
// decide how much to trust a memory before verifying it against live state.
// Results without a parseable created_at pass through untouched.
func stampAge(results []rag.Result) {
	now := time.Now()
	for i := range results {
		ts := results[i].Meta["created_at"]
		if ts == "" {
			continue
		}
		t, err := time.Parse(time.RFC3339, ts)
		if err != nil {
			continue
		}
		age := now.Sub(t)
		if age < 0 {
			age = 0
		}
		results[i].Meta["age"] = humanizeAge(age)
	}
}

// humanizeAge renders a duration as a compact human string: seconds under a
// minute, minutes under an hour, hours under two days, then days.
func humanizeAge(d time.Duration) string {
	switch {
	case d < time.Minute:
		return fmt.Sprintf("%ds", int(d.Seconds()))
	case d < time.Hour:
		return fmt.Sprintf("%dm", int(d.Minutes()))
	case d < 48*time.Hour:
		return fmt.Sprintf("%dh", int(d.Hours()))
	default:
		return fmt.Sprintf("%dd", int(d.Hours()/24))
	}
}

// applySupersedes performs the write side of the supersede mechanism. The new
// points are already durably stored when this runs; tagging the old ones
// afterwards means a failure can hide at most the OLD copy, never both.
func (s *Service) applySupersedes(ctx context.Context, projectID string, docs []rag.Document) error {
	requested := false
	for _, d := range docs {
		if d.Meta["supersedes"] != "" {
			requested = true
			break
		}
	}
	if !requested {
		return nil
	}
	updater, ok := s.provider.(rag.PointPayloadUpdater)
	if !ok {
		return fmt.Errorf("supersedes: backend %s does not support payload updates", s.backend)
	}
	for _, d := range docs {
		old := d.Meta["supersedes"]
		if old == "" {
			continue
		}
		if d.ID == "" {
			return fmt.Errorf("supersedes %q: new document must have an explicit ID", old)
		}
		// The OLD points are tagged with the ID of the replacement document
		// (its base ID; chunked documents keep it under each chunk's doc_id).
		if err := updater.SetPayloadByDocID(ctx, projectID, old, map[string]any{"superseded_by": d.ID}); err != nil {
			return fmt.Errorf("supersedes %q: %w", old, err)
		}
	}
	return nil
}
