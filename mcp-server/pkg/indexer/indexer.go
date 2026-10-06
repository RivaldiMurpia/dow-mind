package indexer

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	gitignore "github.com/sabhiram/go-gitignore"

	"github.com/RivaldiMurpia/dow-mind/pkg/rag"
)

// Indexer scans a project directory and syncs its contents to RAG.
type Indexer struct {
	provider  rag.Provider
	chunkSize int
}

// NewIndexer creates an indexer with the given provider.
// chunkSize is the max characters per chunk (default 1000). The default is
// chosen so a chunk plus its "File: <path>" prefix stays under the TEI
// embedding model's 512-token input limit (code averages ~3 chars/token).
func NewIndexer(provider rag.Provider, chunkSize int) *Indexer {
	if chunkSize <= 0 {
		chunkSize = 1000
	}
	return &Indexer{provider: provider, chunkSize: chunkSize}
}

// Default ignore directories.
var defaultIgnores = map[string]bool{
	"node_modules": true,
	".git":         true,
	"vendor":       true,
	"dist":         true,
	"build":        true,
	".next":        true,
	".nuxt":        true,
	"__pycache__":  true,
	".cache":       true,
	"target":       true,
	".idea":        true,
	".vscode":      true,
	".DS_Store":    true,
	"coverage":     true,
	".pytest_cache": true,
}

// IndexProject scans the directory and indexes all code/text files.
// It handles insertions (new/changed files), and deletions (files removed since last index).
// Chunks whose content_hash matches an existing point are skipped (no re-embedding).
func (idx *Indexer) IndexProject(ctx context.Context, projectID, rootDir string) (*SyncResult, error) {
	rootDir, err := filepath.Abs(rootDir)
	if err != nil {
		return nil, err
	}

	// Use the directory base name as a source_dir tag so stale-point
	// reconciliation is scoped per-directory. Without this, indexing a second
	// directory into the same project would delete all points from the first
	// directory because their source_file values don't appear in the second
	// directory's file list.
	sourceDir := filepath.Base(rootDir)

	// Fetch existing points for this source_dir so we can compare content hashes
	// and skip re-embedding unchanged chunks.
	existingPoints, err := idx.provider.ListPoints(ctx, projectID, map[string]string{"source_dir": sourceDir})
	if err != nil {
		// If we can't list existing points, proceed without skip optimization
		existingPoints = nil
	}
	existingHashes := make(map[string]string) // docID -> content_hash
	for _, pt := range existingPoints {
		key := pt.DocID
		if key == "" {
			key = pt.ID
		}
		if pt.ContentHash != "" {
			existingHashes[key] = pt.ContentHash
		}
	}

	// Walk the directory and collect files.
	var docs []rag.Document
	var currentFiles []string
	skipped := 0
	// fileChunkCounts tracks how many chunks each file produced in THIS run.
	// A chunking change (e.g. heuristic -> AST) can shrink a file's chunk
	// count; without this, the orphaned high-index points from the previous
	// run would never be recognized as stale.
	fileChunkCounts := make(map[string]int)

	// Compile the project's .gitignore (if any) so indexed files respect it.
	// defaultIgnores still apply on top as a baseline.
	var ignorer *gitignore.GitIgnore
	if data, err := os.ReadFile(filepath.Join(rootDir, ".gitignore")); err == nil {
		ignorer = gitignore.CompileIgnoreLines(strings.Split(string(data), "\n")...)
	}

	err = filepath.WalkDir(rootDir, func(path string, d fs.DirEntry, err error) error {
		if err != nil {
			return nil // skip errors
		}
		relPath, relErr := filepath.Rel(rootDir, path)
		if relErr != nil {
			return nil
		}
		relPath = filepath.ToSlash(relPath)

		if d.IsDir() {
			name := d.Name()
			if defaultIgnores[name] {
				return filepath.SkipDir
			}
			// Respect .gitignore for directories too (the trailing slash
			// helps the matcher hit `dir/` patterns). Never skip root itself.
			if ignorer != nil && relPath != "." && ignorer.MatchesPath(relPath+"/") {
				return filepath.SkipDir
			}
			return nil
		}
		if ignorer != nil && ignorer.MatchesPath(relPath) {
			return nil
		}
		// Skip binary/irrelevant files. Secret files are never indexed —
		// this is checked before isIndexable so a permissive extension
		// allowlist can never pull credentials in.
		if isSecretFile(d.Name()) {
			return nil
		}
		if !isIndexable(d.Name()) {
			return nil
		}

		currentFiles = append(currentFiles, relPath)

		content, err := os.ReadFile(path)
		if err != nil {
			return nil
		}
		// Skip empty or very large files (>500KB)
		if len(content) == 0 || len(content) > 500*1024 {
			return nil
		}

		// AST-aware chunking first (tree-sitter, pure Go via wazero): each
		// top-level declaration becomes its own chunk. Falls back to the
		// heuristic splitter for languages without a bundled grammar or on
		// any parse failure — AST chunking is an upgrade path, never a
		// hard requirement.
		chunks := chunkAST(relPath, content, idx.chunkSize)
		if chunks == nil {
			chunks = chunkCode(string(content), idx.chunkSize)
		}
		chunks = applyOverlap(chunks, defaultOverlapChars)
		fileChunkCounts[relPath] = len(chunks)
		for i, chunk := range chunks {
			docID := fmt.Sprintf("%s/%s#chunk%d", sourceDir, relPath, i)
			contentHash := computeContentHash(chunk)
			// Skip re-embedding if the content_hash matches an existing point
			if existingHash, ok := existingHashes[docID]; ok && existingHash == contentHash {
				skipped++
				continue
			}
			docs = append(docs, rag.Document{
				ID:      docID,
				Content: fmt.Sprintf("File: %s\n\n%s", relPath, chunk),
				Meta: map[string]string{
					"source_file":   relPath,
					"source_dir":    sourceDir,
					"chunk_index":   fmt.Sprintf("%d", i),
					"content_hash":  contentHash,
					"chunk_version": "v4",
				},
			})
		}
		return nil
	})
	if err != nil {
		return nil, err
	}

	// Index new/changed documents.
	if len(docs) > 0 {
		// Batch index 100 docs per provider call to bound request size. The TEI
		// embedding client sub-batches these further to respect its own limit.
		for i := 0; i < len(docs); i += 100 {
			end := i + 100
			if end > len(docs) {
				end = len(docs)
			}
			if err := idx.provider.Index(ctx, projectID, docs[i:end]); err != nil {
				return nil, fmt.Errorf("index batch %d: %w", i/100, err)
			}
		}
	}

	// Find and delete stale points (files that no longer exist in THIS directory).
	staleIDs, err := idx.findStalePoints(ctx, projectID, sourceDir, currentFiles, fileChunkCounts)
	if err != nil {
		return &SyncResult{Indexed: len(docs), Deleted: 0, Skipped: skipped, StaleError: err.Error()}, nil
	}
	if len(staleIDs) > 0 {
		if err := idx.provider.DeletePoints(ctx, projectID, staleIDs); err != nil {
			return &SyncResult{Indexed: len(docs), Deleted: 0, Skipped: skipped, StaleError: err.Error()}, nil
		}
	}

	return &SyncResult{
		Indexed:      len(docs),
		Deleted:      len(staleIDs),
		FilesScanned: len(currentFiles),
		Skipped:      skipped,
	}, nil
}

// SyncResult holds stats from a project sync.
type SyncResult struct {
	Indexed      int    `json:"indexed"`
	Deleted      int    `json:"deleted"`
	FilesScanned int    `json:"files_scanned"`
	Skipped      int    `json:"skipped"`
	StaleError   string `json:"stale_error,omitempty"`
}

func (idx *Indexer) findStalePoints(ctx context.Context, projectID, sourceDir string, currentFiles []string, fileChunkCounts map[string]int) ([]string, error) {
	currentSet := make(map[string]bool, len(currentFiles))
	for _, f := range currentFiles {
		currentSet[f] = true
	}

	// List only points belonging to this source_dir so that indexing a
	// different directory into the same project doesn't wipe the first.
	points, err := idx.provider.ListPoints(ctx, projectID, map[string]string{"source_dir": sourceDir})
	if err != nil {
		return nil, err
	}

	var stale []string
	for _, pt := range points {
		if pt.SourceFile == "" {
			continue
		}
		if !currentSet[pt.SourceFile] {
			stale = append(stale, pt.ID)
			continue
		}
		// Orphaned chunk slot: the file still exists but now produces fewer
		// chunks than the point's index (e.g. after a chunking-version
		// change). Without this, old-version chunks linger forever.
		if n, ok := fileChunkCounts[pt.SourceFile]; ok {
			if ci, err := strconv.Atoi(pt.ChunkIndex); err == nil && ci >= n {
				stale = append(stale, pt.ID)
			}
		}
	}
	return stale, nil
}

// defaultOverlapChars is how many trailing characters of the previous chunk
// are prepended to the next one. Retrieval quality drops when a concept is
// split exactly at a chunk boundary with zero shared context; a small overlap
// keeps both sides of the boundary searchable.
const defaultOverlapChars = 200

// splitCodeBlocks splits text into blocks separated by blank lines.
// Functions, classes, and paragraphs are usually separated by blank lines,
// so block boundaries are much better chunk cut points than arbitrary lines.
func splitCodeBlocks(text string) []string {
	var blocks []string
	var cur strings.Builder
	flush := func() {
		if cur.Len() > 0 {
			blocks = append(blocks, cur.String())
			cur.Reset()
		}
	}
	for _, line := range strings.Split(text, "\n") {
		if strings.TrimSpace(line) == "" {
			flush()
			continue
		}
		cur.WriteString(line)
		cur.WriteString("\n")
	}
	flush()
	return blocks
}

// chunkCode splits text into chunks of approximately maxChars, preferring cut
// points at blank-line boundaries (which usually separate functions, classes,
// or paragraphs in code). Blocks larger than maxChars fall back to line-based
// splitting. This is the heuristic fallback used when AST chunking (chunkAST)
// is unavailable for a file's language or its parse fails.
func chunkCode(text string, maxChars int) []string {
	if len(text) <= maxChars {
		return []string{text}
	}
	var chunks []string
	var cur strings.Builder
	flush := func() {
		if cur.Len() > 0 {
			chunks = append(chunks, cur.String())
			cur.Reset()
		}
	}
	for _, block := range splitCodeBlocks(text) {
		block = strings.TrimSuffix(block, "\n")
		units := []string{block}
		if len(block) > maxChars {
			units = chunkText(block, maxChars)
		}
		for _, u := range units {
			u = strings.TrimSuffix(u, "\n")
			if cur.Len() > 0 && cur.Len()+1+len(u) > maxChars {
				flush()
			}
			if cur.Len() > 0 {
				cur.WriteString("\n")
			}
			cur.WriteString(u)
		}
	}
	flush()
	return chunks
}

// applyOverlap prepends up to overlapChars of trailing context from the
// previous chunk (cut on a line boundary) to every chunk after the first.
func applyOverlap(chunks []string, overlapChars int) []string {
	if overlapChars <= 0 || len(chunks) < 2 {
		return chunks
	}
	out := make([]string, 0, len(chunks))
	for i, c := range chunks {
		if i > 0 {
			prev := chunks[i-1]
			tail := prev
			if len(tail) > overlapChars {
				tail = tail[len(tail)-overlapChars:]
				if nl := strings.Index(tail, "\n"); nl >= 0 {
					tail = tail[nl+1:]
				}
			}
			if tail != "" {
				c = tail + "\n" + c
			}
		}
		out = append(out, c)
	}
	return out
}

// chunkText splits text into chunks of approximately maxChars.
// It tries to split on newline boundaries.
func chunkText(text string, maxChars int) []string {
	if len(text) <= maxChars {
		return []string{text}
	}

	var chunks []string
	lines := strings.Split(text, "\n")
	var current strings.Builder

	flush := func() {
		if current.Len() > 0 {
			chunks = append(chunks, current.String())
			current.Reset()
		}
	}

	for _, line := range lines {
		// A single line longer than maxChars (e.g. minified/generated code)
		// cannot fit in any chunk on its own, so hard-split it on byte
		// boundaries. Without this the line would be emitted whole and exceed
		// the embedding model's token limit.
		for len(line) > maxChars {
			flush()
			chunks = append(chunks, line[:maxChars])
			line = line[maxChars:]
		}
		if current.Len()+len(line)+1 > maxChars && current.Len() > 0 {
			flush()
		}
		current.WriteString(line)
		current.WriteString("\n")
	}
	flush()
	return chunks
}

// computeContentHash returns the first 8 bytes of SHA-256 of the given content
// as a 16-character lowercase hex string. This is used for incremental sync:
// chunks whose hash matches an existing point are skipped (no re-embedding).
func computeContentHash(content string) string {
	h := sha256.Sum256([]byte(content))
	return hex.EncodeToString(h[:8])
}

// isSecretFile reports whether a file conventionally holds credentials and
// must never be indexed, even if its extension looks indexable.
// Template files (.env.example) are safe and explicitly allowed.
func isSecretFile(name string) bool {
	nameLower := strings.ToLower(name)

	// .env and .env.<name> hold real secrets; only templates are allowed.
	if nameLower == ".env.example" || nameLower == ".env.sample" || nameLower == ".env.template" {
		return false
	}
	if nameLower == ".env" || strings.HasPrefix(nameLower, ".env.") {
		return true
	}

	// Key/certificate stores and encrypted blobs.
	switch strings.ToLower(filepath.Ext(name)) {
	case ".pem", ".key", ".p12", ".pfx", ".jks", ".keystore", ".asc", ".gpg", ".age":
		return true
	}

	// Basenames that are exactly (or start/end with) a secret token are
	// skipped: the file is probably the secret itself. This is intentionally
	// conservative — e.g. "credential_manager.go" is skipped too. Rename the
	// file if you need it indexed.
	base := strings.TrimSuffix(nameLower, strings.ToLower(filepath.Ext(nameLower)))
	for _, s := range []string{"id_rsa", "id_dsa", "id_ecdsa", "id_ed25519", "credentials", "credential", "secrets", "secret"} {
		for _, sep := range []string{".", "_", "-"} {
			if base == s || strings.HasPrefix(base, s+sep) || strings.HasSuffix(base, sep+s) {
				return true
			}
		}
	}

	// Dotfiles that conventionally carry tokens.
	switch nameLower {
	case ".npmrc", ".pypirc", ".netrc", "_netrc":
		return true
	}
	return false
}

// isIndexable returns true for code and text files.
func isIndexable(name string) bool {
	ext := strings.ToLower(filepath.Ext(name))
	codeExts := map[string]bool{
		".go": true, ".ts": true, ".tsx": true, ".js": true, ".jsx": true,
		".py": true, ".rs": true, ".java": true, ".kt": true, ".swift": true,
		".c": true, ".cpp": true, ".h": true, ".hpp": true, ".cs": true,
		".rb": true, ".php": true, ".vue": true, ".svelte": true,
		".sql": true, ".sh": true, ".bash": true, ".zsh": true,
		".yml": true, ".yaml": true, ".toml": true, ".json": true,
		".xml": true, ".html": true, ".css": true, ".scss": true,
		".md": true, ".txt": true, ".cfg": true,
		".ini": true, ".conf": true, ".dockerfile": true,
		".proto": true, ".graphql": true, ".gql": true,
	}
	if codeExts[ext] {
		return true
	}
	// Also index files without extension but with known names
	nameLower := strings.ToLower(name)
	if nameLower == "dockerfile" || nameLower == "makefile" || nameLower == "license" || nameLower == ".gitignore" ||
		nameLower == ".env.example" || nameLower == ".env.sample" || nameLower == ".env.template" {
		return true
	}
	return false
}
