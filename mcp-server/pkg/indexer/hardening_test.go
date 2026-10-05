package indexer

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestIsSecretFile verifies the secret-file denylist: credential files are
// never indexed, while templates and ordinary code are allowed through.
func TestIsSecretFile(t *testing.T) {
	secret := []string{
		".env", ".env.local", ".env.production", ".ENV",
		"id_rsa", "id_ed25519", "deploy.pem", "cert.p12", "store.jks",
		"credentials.json", "secrets.yaml", "my-secrets.toml",
		".npmrc", ".pypirc", ".netrc",
		// Conservative: code *about* secrets is skipped too (documented).
		"credential_manager.go",
	}
	for _, name := range secret {
		if !isSecretFile(name) {
			t.Errorf("isSecretFile(%q) = false, want true", name)
		}
	}

	safe := []string{
		".env.example", ".env.sample", ".env.template",
		"main.go", "config.yaml", "README.md", "Dockerfile",
		"secretariat.txt", // "secret" inside a larger word is fine
	}
	for _, name := range safe {
		if isSecretFile(name) {
			t.Errorf("isSecretFile(%q) = true, want false", name)
		}
	}
}

// TestChunkCode_PrefersBlankLineBoundaries verifies chunks are cut at blank
// lines (function boundaries) rather than mid-function when possible.
func TestChunkCode_PrefersBlankLineBoundaries(t *testing.T) {
	fn1 := "func alpha() {\n\tline1\n\tline2\n}"
	fn2 := "func beta() {\n\tline3\n\tline4\n}"
	text := fn1 + "\n\n" + fn2

	// chunkSize fits one function but not both.
	chunks := chunkCode(text, len(fn1)+10)
	if len(chunks) != 2 {
		t.Fatalf("expected 2 chunks, got %d: %q", len(chunks), chunks)
	}
	if chunks[0] != fn1 {
		t.Errorf("chunk[0] = %q, want function alpha intact", chunks[0])
	}
	if chunks[1] != fn2 {
		t.Errorf("chunk[1] = %q, want function beta intact", chunks[1])
	}
}

// TestChunkCode_FallsBackToLines verifies oversized blocks (no blank lines)
// degrade to the old line-based splitting.
func TestChunkCode_FallsBackToLines(t *testing.T) {
	var sb strings.Builder
	for i := 0; i < 30; i++ {
		sb.WriteString(strings.Repeat("x", 38) + "\n")
	}
	chunks := chunkCode(sb.String(), 500)
	if len(chunks) < 2 {
		t.Fatalf("expected multiple chunks, got %d", len(chunks))
	}
	for i, c := range chunks {
		if len(c) > 500 {
			t.Errorf("chunk %d len %d exceeds maxChars", i, len(c))
		}
	}
}

// TestApplyOverlap verifies that consecutive chunks share trailing context
// and that edge cases degrade gracefully.
func TestApplyOverlap(t *testing.T) {
	chunks := applyOverlap([]string{"aaa\nbbb", "ccc\nddd"}, 200)
	if len(chunks) != 2 {
		t.Fatalf("expected 2 chunks, got %d", len(chunks))
	}
	if chunks[1] != "aaa\nbbb\nccc\nddd" {
		t.Errorf("chunk[1] = %q, want overlap-prefixed", chunks[1])
	}

	// Single-chunk input is returned unchanged.
	single := applyOverlap([]string{"short"}, 200)
	if len(single) != 1 || single[0] != "short" {
		t.Errorf("single-chunk input altered: %q", single)
	}

	// Zero overlap returns input unchanged.
	in := []string{"a", "b"}
	zero := applyOverlap(in, 0)
	if len(zero) != 2 || zero[0] != "a" || zero[1] != "b" {
		t.Errorf("overlap=0 altered input: %q", zero)
	}
}

// indexedSourceFiles returns the set of source_file values in indexed docs.
func indexedSourceFiles(t *testing.T, p *mockProvider) map[string]bool {
	t.Helper()
	out := map[string]bool{}
	for _, d := range p.getIndexedDocs() {
		out[d.Meta["source_file"]] = true
	}
	return out
}

// TestIndexProject_SkipsSecretFiles verifies .env / keys never reach the provider.
func TestIndexProject_SkipsSecretFiles(t *testing.T) {
	dir := t.TempDir()
	files := map[string]string{
		"app.go":        "package main\n",
		".env":          "DOWMIND_VOYAGE_API_KEY=super-secret\n",
		".env.local":    "TOKEN=abc\n",
		".env.example":  "DOWMIND_VOYAGE_API_KEY=\n",
		"id_rsa":        "-----BEGIN OPENSSH PRIVATE KEY-----\n",
		"deploy.pem":    "-----BEGIN CERTIFICATE-----\n",
		"notes.txt":     "hello\n",
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
	}

	provider := &mockProvider{}
	idx := NewIndexer(provider, 1000)
	if _, err := idx.IndexProject(context.Background(), "testproj", dir); err != nil {
		t.Fatalf("IndexProject: %v", err)
	}

	got := indexedSourceFiles(t, provider)
	for _, want := range []string{"app.go", "notes.txt", ".env.example"} {
		if !got[want] {
			t.Errorf("expected %q to be indexed", want)
		}
	}
	for _, notWant := range []string{".env", ".env.local", "id_rsa", "deploy.pem"} {
		if got[notWant] {
			t.Errorf("secret file %q was indexed", notWant)
		}
	}
}

// TestIndexProject_RespectsGitignore verifies the root .gitignore is honored.
func TestIndexProject_RespectsGitignore(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, ".gitignore"), []byte("ignored/\nscratch.txt\n"), 0644); err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Join(dir, "ignored"), 0755); err != nil {
		t.Fatal(err)
	}
	files := map[string]string{
		"keep.go":           "package main\n",
		"ignored/skip.go":   "package ignored\n",
		"scratch.txt":       "tmp\n",
		"notes.txt":         "keep me\n",
	}
	for name, content := range files {
		if err := os.WriteFile(filepath.Join(dir, name), []byte(content), 0644); err != nil {
			t.Fatal(err)
		}
	}

	provider := &mockProvider{}
	idx := NewIndexer(provider, 1000)
	if _, err := idx.IndexProject(context.Background(), "testproj", dir); err != nil {
		t.Fatalf("IndexProject: %v", err)
	}

	got := indexedSourceFiles(t, provider)
	for _, want := range []string{"keep.go", "notes.txt", ".gitignore"} {
		if !got[want] {
			t.Errorf("expected %q to be indexed", want)
		}
	}
	for _, notWant := range []string{"ignored/skip.go", "scratch.txt"} {
		if got[notWant] {
			t.Errorf("gitignored file %q was indexed", notWant)
		}
	}
}

// TestIsIndexable_EnvRemoved ensures .env is no longer in the allowlist.
func TestIsIndexable_EnvRemoved(t *testing.T) {
	if isIndexable(".env") {
		t.Error(`isIndexable(".env") = true, want false`)
	}
	if !isIndexable(".env.example") {
		t.Error(`isIndexable(".env.example") = false, want true`)
	}
}
