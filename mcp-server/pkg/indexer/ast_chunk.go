package indexer

// ast_chunk.go — tree-sitter AST-aware code chunking.
//
// Instead of cutting code at arbitrary line boundaries, chunkAST splits a file
// along its real syntactic declarations (functions, methods, classes, ...):
// each top-level declaration becomes its own chunk. This keeps semantically
// related code together, which measurably improves retrieval quality versus
// the heuristic splitter in chunkCode.
//
// Languages without a bundled grammar, parse failures, and trees with
// top-level syntax errors all return nil so the caller falls back to the
// heuristic chunker — AST chunking is strictly an upgrade path, never a
// hard requirement.

import (
	"log"
	"path/filepath"
	"strings"
	"sync"
)

// astLang describes one bundled tree-sitter grammar: the wasm export that
// yields its TSLanguage*, plus the node kinds treated as chunk boundaries.
type astLang struct {
	grammarExport string
	declKinds     map[string]bool
}

var tsDeclKinds = map[string]bool{
	"function_declaration":           true,
	"generator_function_declaration": true,
	"class_declaration":              true,
	"abstract_class_declaration":     true,
	"interface_declaration":          true,
	"type_alias_declaration":         true,
	"enum_declaration":               true,
	"export_statement":               true,
}

// astLanguages maps file extensions to bundled grammars. Keep in sync with
// the grammars compiled into ts-core.wasm (see docs/ast-wasm.md).
var astLanguages = map[string]*astLang{
	".go":  {grammarExport: "tree_sitter_go", declKinds: map[string]bool{
		"function_declaration": true,
		"method_declaration":   true,
		"type_declaration":     true,
	}},
	".ts":  {grammarExport: "tree_sitter_typescript", declKinds: tsDeclKinds},
	".tsx": {grammarExport: "tree_sitter_tsx", declKinds: tsDeclKinds},
	".js":  {grammarExport: "tree_sitter_javascript", declKinds: tsDeclKinds},
	".jsx": {grammarExport: "tree_sitter_javascript", declKinds: tsDeclKinds},
	".mjs": {grammarExport: "tree_sitter_javascript", declKinds: tsDeclKinds},
	".cjs": {grammarExport: "tree_sitter_javascript", declKinds: tsDeclKinds},
	".py": {grammarExport: "tree_sitter_python", declKinds: map[string]bool{
		"function_definition":  true,
		"class_definition":     true,
		"decorated_definition": true,
	}},
}

// astFallbackOnce ensures we log only the first fallback reason per process:
// when every file falls back the same way, one line names the cause.
var astFallbackOnce sync.Once

func noteASTFallback(reason string) {
	astFallbackOnce.Do(func() {
		log.Printf("dow-mind: ast: chunkAST fallback (%s), using heuristic chunking", reason)
	})
}

func errString(err error) string {
	if err == nil {
		return "empty tree"
	}
	return err.Error()
}

// chunkAST splits src into AST-aligned chunks of at most maxChars.
// It returns nil when the file's language has no bundled grammar, the engine
// fails to initialize, parsing fails, or the tree has top-level syntax
// errors — in all those cases the caller must use the heuristic chunker.
func chunkAST(filename string, src []byte, maxChars int) []string {
	if maxChars <= 0 || len(src) == 0 {
		return nil
	}
	lang := astLanguages[strings.ToLower(filepath.Ext(filename))]
	if lang == nil {
		return nil // no bundled grammar: heuristic chunking is the normal path
	}
	eng, err := getASTEngine()
	if err != nil {
		noteASTFallback("engine: " + err.Error())
		return nil
	}
	nodes, err := eng.parseNodes(lang.grammarExport, src)
	if err != nil || len(nodes) == 0 {
		noteASTFallback("parse " + lang.grammarExport + ": " + errString(err))
		return nil
	}
	// A syntax error at the top level means the declaration structure is
	// unreliable — don't build chunks on a broken tree.
	for _, n := range nodes {
		if n.depth <= 1 && n.isError {
			noteASTFallback("top-level syntax error")
			return nil
		}
	}

	var chunks []string
	var pending []byte // non-declaration source (imports, comments, consts)

	flushPending := func() {
		if len(pending) == 0 {
			return
		}
		chunks = append(chunks, chunkText(string(pending), maxChars)...)
		pending = pending[:0]
	}

	for _, n := range nodes {
		if n.depth != 1 || !n.named {
			continue
		}
		if !lang.declKinds[n.kind] {
			pending = append(pending, src[n.startByte:n.endByte]...)
			pending = append(pending, '\n')
			continue
		}
		body := src[n.startByte:n.endByte]
		// Attach leading context (usually doc comments / imports) to the
		// declaration when it fits — it belongs with the code semantically.
		if len(pending) > 0 && len(pending)+1+len(body) <= maxChars {
			merged := make([]byte, 0, len(pending)+1+len(body))
			merged = append(merged, pending...)
			merged = append(merged, '\n')
			merged = append(merged, body...)
			body = merged
			pending = pending[:0]
		} else {
			flushPending()
		}
		if len(body) <= maxChars {
			chunks = append(chunks, string(body))
		} else {
			// Oversized declaration (rare): split it heuristically rather
			// than emitting a chunk the embedder would truncate.
			chunks = append(chunks, chunkText(string(body), maxChars)...)
		}
	}
	flushPending()

	if len(chunks) == 0 {
		return nil
	}
	return chunks
}
