# AST chunking — tree-sitter WASM module

DOW Mind does AST-aware code chunking with tree-sitter, driven from pure Go
(no cgo) via [wazero](https://wazero.io). This keeps `dow-mind` a fully static
single binary.

## What's bundled

`mcp-server/pkg/indexer/ts-core.wasm` is ONE standalone `wasm32-wasi` reactor
module containing:

- the official tree-sitter C runtime (`tree-sitter/tree-sitter` v0.25.10, `lib/src/lib.c`)
- 5 grammars: Go (v0.25.0), TypeScript (v0.23.2), TSX (v0.23.2),
  JavaScript (v0.25.0), Python (v0.25.0)
- `host_extra.c`: a `ts_dump_tree` export that walks the parsed tree ENTIRELY
  inside the guest and writes a flat pre-order array of fixed-size records
  into linear memory — one wazero call per parse instead of ~3 per node.

The Go side (`pkg/indexer/ast_engine.go`) instantiates the module once (lazy
singleton), parses each file with its grammar, and `pkg/indexer/ast_chunk.go`
turns top-level declarations (functions, methods, classes, interfaces, ...)
into chunks. Files whose language has no grammar, failed parses, and trees
with top-level syntax errors fall back to the heuristic splitter in
`chunkCode` — AST chunking is an upgrade path, never a hard requirement.

## Rebuilding

Requires: `zig` (any recent 0.14+), `git`. No emscripten, no Docker, no C
toolchain on the target machine — the `.wasm` is committed to the repo.

```bash
TS=tree-sitter@v0.25.10   # git clone --depth 1 --branch v0.25.10 https://github.com/tree-sitter/tree-sitter
# ... clone each grammar repo at its pinned tag (see table above) ...

zig cc --target=wasm32-wasi-musl -mexec-model=reactor \
  -I $TS/lib/include -I $TS/lib/src \
  -I <grammar-src-dirs...> \
  $TS/lib/src/lib.c host_extra.c \
  <each grammar>/src/parser.c [<each grammar>/src/scanner.c] \
  -o ts-core.wasm -Oz -fPIC -Wl,--no-entry -Wl,--strip-debug \
  -Wl,--export=malloc -Wl,--export=free \
  -Wl,--export=ts_parser_new -Wl,--export=ts_parser_delete \
  -Wl,--export=ts_parser_set_language -Wl,--export=ts_parser_parse_string \
  -Wl,--export=ts_parser_reset \
  -Wl,--export=ts_tree_delete \
  -Wl,--export=ts_dump_tree -Wl,--export=ts_dump_rec_size \
  -Wl,--export=ts_language_symbol_count -Wl,--export=ts_language_symbol_name \
  -Wl,--export=tree_sitter_go -Wl,--export=tree_sitter_typescript \
  -Wl,--export=tree_sitter_tsx -Wl,--export=tree_sitter_javascript \
  -Wl,--export=tree_sitter_python
```

Notes:

- Compile each grammar **in place from a full clone** so relative includes
  (e.g. TypeScript's `../../common/scanner.h`) resolve. For TypeScript/TSX
  compile `<repo>/typescript/src/scanner.c` (it pulls in `common/scanner.h`),
  not `common/` directly.
- `host_extra.c` lives in the original spike at
  `~/workspace/ast-spike/poc/csrc/host_extra.c` (build machine only; the
  compiled `.wasm` is what ships).
- Export one `tree_sitter_<lang>` per grammar and add the matching entry to
  `astLanguages` in `pkg/indexer/ast_chunk.go`.
- If you change `NodeRec` in `host_extra.c`, `astRecSize` in
  `pkg/indexer/ast_engine.go` must match — it's asserted at engine init
  (`ts_dump_rec_size`).

## Design constraints

- **No cgo, ever.** The whole point of the WASM route is keeping
  `CGO_ENABLED=0` static builds. Do not "simplify" by switching to a cgo
  tree-sitter binding.
- **Crash isolation.** Adversarial inputs trap inside the guest and surface
  as Go errors via `recover()` in `parseNodes`. Never let a parse panic
  propagate — the indexer must always degrade to heuristic chunking.
- **Engine init is lazy** (~135 ms one-time wazero compile). A failed init is
  remembered; every file then uses the heuristic path.
