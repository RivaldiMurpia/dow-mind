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

// TestChunkTextWithOverlap verifies that consecutive chunks share trailing
// context and that edge cases degrade gracefully.
func TestChunkTextWithOverlap(t *testing.T) {
	// Build ~30 lines of ~40 chars => ~1200 chars total, chunkSize 500.
	var sb strings.Builder
	for i := 0; i < 30; i++ {
		sb.WriteString(strings.Repeat("x", 38) + "\n")
	}
	text := sb.String()

	chunks := chunkTextWithOverlap(text, 500, 200)
	if len(chunks) < 2 {
		t.Fatalf("expected multiple chunks, got %d", len(chunks))
	}
	// Chunk 2 must start with the tail of chunk 1 (overlap on line boundary).
	prev := chunks[0]
	tail := prev
	if len(tail) > 200 {
		tail = tail[len(tail)-200:]
		if nl := strings.Index(tail, "\n"); nl >= 0 {
			tail = tail[nl+1:]
		}
	}
	if !strings.HasPrefix(chunks[1], tail) {
		t.Errorf("chunk[1] does not start with overlap tail of chunk[0]")
	}

	// Single-chunk input is returned unchanged.
	single := chunkTextWithOverlap("short", 500, 200)
	if len(single) != 1 || single[0] != "short" {
		t.Errorf("single-chunk input altered: %q", single)
	}

	// Zero overlap behaves exactly like chunkText.
	plain := chunkText(text, 500)
	zero := chunkTextWithOverlap(text, 500, 0)
	if len(plain) != len(zero) {
		t.Fatalf("overlap=0 changed chunk count: %d vs %d", len(zero), len(plain))
	}
	for i := range plain {
		if plain[i] != zero[i] {
			t.Fatalf("overlap=0 changed chunk %d", i)
		}
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
