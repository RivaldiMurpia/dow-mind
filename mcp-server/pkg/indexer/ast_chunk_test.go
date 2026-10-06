package indexer

import (
	"strings"
	"testing"
)

const astTestGo = `package demo

import "fmt"

// Greeter says hello.
type Greeter struct {
	name string
}

func NewGreeter(name string) *Greeter {
	return &Greeter{name: name}
}

func (g *Greeter) Hello() string {
	return "hello " + g.name
}

func main() {
	g := NewGreeter("world")
	fmt.Println(g.Hello())
}
`

const astTestTS = `import { useState } from "react";

export interface Project {
  id: string;
}

const API = "http://localhost:7777";

export function useProjects() {
  const [p, setP] = useState<Project[]>([]);
  return { p, setP };
}

export class Watcher {
  start() { console.log(API); }
}
`

const astTestPy = `import os

CONSTANT = 42

def helper(x):
    return x * 2

class Worker:
    def run(self):
        return helper(CONSTANT)

@decorator
def decorated():
    pass
`

func TestChunkASTGo(t *testing.T) {
	chunks := chunkAST("demo.go", []byte(astTestGo), 1000)
	if chunks == nil {
		t.Fatal("expected AST chunks, got nil (fallback)")
	}
	// Every declaration should be its own chunk: type + 2 funcs + method + main
	// = 4 chunks (package clause, imports and doc comment attach to the
	// first declaration).
	if len(chunks) != 4 {
		t.Fatalf("expected 4 chunks, got %d: %q", len(chunks), chunks)
	}
	for _, want := range []string{"type Greeter struct", "func NewGreeter", "func (g *Greeter) Hello", "func main"} {
		found := false
		for _, c := range chunks {
			if strings.Contains(c, want) {
				found = true
				break
			}
		}
		if !found {
			t.Errorf("no chunk contains %q", want)
		}
	}
	// Import block should be attached to the first chunk, not orphaned.
	if !strings.Contains(chunks[0], `import "fmt"`) {
		t.Errorf("expected imports attached to first chunk, got %q", chunks[0])
	}
}

func TestChunkASTTypeScript(t *testing.T) {
	chunks := chunkAST("demo.ts", []byte(astTestTS), 1000)
	if chunks == nil {
		t.Fatal("expected AST chunks, got nil (fallback)")
	}
	joined := strings.Join(chunks, "\n---\n")
	for _, want := range []string{"interface Project", "function useProjects", "class Watcher"} {
		if !strings.Contains(joined, want) {
			t.Errorf("no chunk contains %q\n%s", want, joined)
		}
	}
}

func TestChunkASTPython(t *testing.T) {
	chunks := chunkAST("demo.py", []byte(astTestPy), 1000)
	if chunks == nil {
		t.Fatal("expected AST chunks, got nil (fallback)")
	}
	joined := strings.Join(chunks, "\n---\n")
	for _, want := range []string{"def helper", "class Worker", "@decorator"} {
		if !strings.Contains(joined, want) {
			t.Errorf("no chunk contains %q\n%s", want, joined)
		}
	}
	// The decorator must stay attached to its function.
	for _, c := range chunks {
		if strings.Contains(c, "@decorator") && !strings.Contains(c, "def decorated") {
			t.Errorf("decorator separated from its function: %q", c)
		}
	}
}

func TestChunkASTFallbacks(t *testing.T) {
	// Unsupported language -> nil (caller uses heuristic chunker).
	if got := chunkAST("demo.rs", []byte("fn main() {}"), 1000); got != nil {
		t.Errorf("expected nil for unsupported extension, got %v", got)
	}
	// Empty source -> nil.
	if got := chunkAST("demo.go", nil, 1000); got != nil {
		t.Errorf("expected nil for empty source, got %v", got)
	}
	// Top-level syntax error -> nil (don't chunk on a broken tree).
	broken := "package demo\n\nfunc broken( {\n  this is not go\n"
	if got := chunkAST("broken.go", []byte(broken), 1000); got != nil {
		t.Errorf("expected nil for broken syntax, got %d chunks", len(got))
	}
}

func TestChunkASTOversizedDecl(t *testing.T) {
	// One giant function must be split heuristically, never emitted whole.
	body := "package demo\n\nfunc giant() {\n" + strings.Repeat("\tx()\n", 500) + "}\n"
	chunks := chunkAST("giant.go", []byte(body), 1000)
	if chunks == nil {
		t.Fatal("expected chunks, got nil")
	}
	if len(chunks) < 2 {
		t.Fatalf("expected oversized decl to split, got %d chunk(s)", len(chunks))
	}
	for i, c := range chunks {
		if len(c) > 1000 {
			t.Errorf("chunk %d exceeds maxChars: %d", i, len(c))
		}
	}
}

func TestChunkASTAllChunksBounded(t *testing.T) {
	for name, tc := range map[string]struct {
		file string
		src  string
	}{
		"go":         {"a.go", astTestGo},
		"typescript": {"a.ts", astTestTS},
		"python":     {"a.py", astTestPy},
	} {
		chunks := chunkAST(tc.file, []byte(tc.src), 1000)
		if chunks == nil {
			t.Fatalf("%s: got nil", name)
		}
		for i, c := range chunks {
			if len(c) > 1000 {
				t.Errorf("%s chunk %d exceeds maxChars: %d", name, i, len(c))
			}
			if strings.TrimSpace(c) == "" {
				t.Errorf("%s chunk %d is empty", name, i)
			}
		}
	}
}
