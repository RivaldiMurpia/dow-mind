package indexer

// ast_engine.go — tree-sitter parsing engine driven from pure Go.
//
// The official tree-sitter C runtime plus our grammar set (Go, TypeScript, TSX,
// JavaScript, Python) are compiled into ONE standalone wasm32-wasi module
// (ts-core.wasm, built via `zig cc`; see docs/ast-wasm.md) and executed by
// wazero, a pure-Go WebAssembly runtime. No cgo: the dow-mind binary stays a
// fully static single binary.
//
// A custom guest export, ts_dump_tree, walks the parsed tree ENTIRELY inside
// the wasm guest and writes a flat pre-order array of fixed-size records into
// linear memory. The host then does a single Memory.Read per parse instead of
// ~3 wazero calls per node.
//
// Crash isolation: a guest trap (pathological input) is recovered into a Go
// error — the host process stays alive, where cgo would SIGSEGV it. Callers
// treat any engine error as "AST unavailable" and fall back to heuristic
// chunking.

import (
	"context"
	_ "embed"
	"encoding/binary"
	"fmt"
	"log"
	"sync"

	"github.com/tetratelabs/wazero"
	"github.com/tetratelabs/wazero/api"
	"github.com/tetratelabs/wazero/imports/wasi_snapshot_preview1"
)

//go:embed ts-core.wasm
var tsCoreWasm []byte

// astRecSize must match sizeof(NodeRec) in the guest's host_extra.c
// (9 x uint32). Asserted against ts_dump_rec_size() at init.
const astRecSize = 36

// astMemLimitPages caps guest linear memory (64 MiB). A runaway parse traps
// and surfaces as a Go error instead of growing unbounded on a small VPS.
// The module itself needs a 17 MiB minimum, so this must stay well above that.
const astMemLimitPages = 1024

// astNode is one decoded tree-sitter node from the batched dump.
type astNode struct {
	kindID                       uint32
	kind                         string // resolved from kindID via the per-language symbol table
	startByte, endByte           uint32
	startRow, startCol           uint32
	endRow, endCol               uint32
	depth                        uint32
	named, isError, missing, extra bool
}

// tsEngine is a single wazero instance hosting the tree-sitter runtime.
// ParseNodes is safe for concurrent use (mutex-guarded); the underlying
// wazero module instance is not goroutine-safe.
type tsEngine struct {
	ctx context.Context
	rt  wazero.Runtime
	mod api.Module
	mem api.Memory
	mu  sync.Mutex

	malloc, free                         api.Function
	parserNew, parserDelete, parserReset  api.Function
	setLang, parse, treeDelete            api.Function
	dumpTree                              api.Function
	langSymCount, langSymName             api.Function

	langPtr map[string]uint32            // grammar export -> TSLanguage*
	symName map[string]map[uint32]string // grammar export -> (symbol id -> kind name)
}

var (
	astEngineOnce sync.Once
	astEngine     *tsEngine
	astEngineErr  error
)

// getASTEngine lazily builds the singleton engine. Any failure is remembered
// and returned on every call so chunking degrades to the heuristic path
// instead of retrying a broken init on every file.
func getASTEngine() (*tsEngine, error) {
	astEngineOnce.Do(func() {
		astEngine, astEngineErr = newTSEngine(context.Background())
		if astEngineErr != nil {
			// Visible in journalctl: without this, a broken engine fails
			// silently into heuristic chunking and looks like "nothing changed".
			log.Printf("dow-mind: ast: tree-sitter engine unavailable, using heuristic chunking: %v", astEngineErr)
		}
	})
	return astEngine, astEngineErr
}

func newTSEngine(ctx context.Context) (*tsEngine, error) {
	// Try the optimizing compiler backend first; fall back to the
	// interpreter when the host can't run wazero-compiled code (some
	// VPS kernels restrict executable memory mappings). Parsing stays
	// ms-scale either way next to embedding API calls.
	eng, backend, err := instantiateEngine(ctx, wazero.NewRuntimeConfigCompiler(), "compiler")
	if err != nil {
		log.Printf("dow-mind: ast: compiler backend failed (%v), trying interpreter", err)
		eng, backend, err = instantiateEngine(ctx, wazero.NewRuntimeConfigInterpreter(), "interpreter")
	}
	if err != nil {
		return nil, fmt.Errorf("ast: no usable wasm backend: %w", err)
	}
	log.Printf("dow-mind: ast: tree-sitter engine ready (%s backend)", backend)
	return eng, nil
}

func instantiateEngine(ctx context.Context, cfg wazero.RuntimeConfig, backend string) (*tsEngine, string, error) {
	cfg = cfg.WithMemoryLimitPages(astMemLimitPages)
	rt := wazero.NewRuntimeWithConfig(ctx, cfg)
	wasi_snapshot_preview1.MustInstantiate(ctx, rt)
	mod, err := rt.InstantiateWithConfig(ctx, tsCoreWasm,
		wazero.NewModuleConfig().WithName("ts").WithStartFunctions("_initialize"))
	if err != nil {
		rt.Close(ctx)
		return nil, "", fmt.Errorf("ast: instantiate wasm: %w", err)
	}
	e := &tsEngine{
		ctx: ctx, rt: rt, mod: mod, mem: mod.Memory(),
		malloc:       mod.ExportedFunction("malloc"),
		free:         mod.ExportedFunction("free"),
		parserNew:    mod.ExportedFunction("ts_parser_new"),
		parserDelete: mod.ExportedFunction("ts_parser_delete"),
		parserReset:  mod.ExportedFunction("ts_parser_reset"),
		setLang:      mod.ExportedFunction("ts_parser_set_language"),
		parse:        mod.ExportedFunction("ts_parser_parse_string"),
		treeDelete:   mod.ExportedFunction("ts_tree_delete"),
		dumpTree:     mod.ExportedFunction("ts_dump_tree"),
		langSymCount: mod.ExportedFunction("ts_language_symbol_count"),
		langSymName:  mod.ExportedFunction("ts_language_symbol_name"),
		langPtr:      map[string]uint32{},
		symName:      map[string]map[uint32]string{},
	}
	if rs := e.call(mod.ExportedFunction("ts_dump_rec_size")); rs != astRecSize {
		rt.Close(ctx)
		return nil, "", fmt.Errorf("ast: NodeRec size mismatch: guest=%d host=%d", rs, astRecSize)
	}
	return e, backend, nil
}

func (e *tsEngine) close() { e.rt.Close(e.ctx) }

// call invokes a wasm export. A nil function or a guest trap panics so the
// caller's recover in ParseNodes can contain it as a regular error.
func (e *tsEngine) call(f api.Function, args ...uint64) uint64 {
	if f == nil {
		panic("ast: missing wasm export")
	}
	r, err := f.Call(e.ctx, args...)
	if err != nil {
		panic(err)
	}
	if len(r) == 0 {
		return 0
	}
	return r[0]
}

// language resolves (and caches) the TSLanguage* for a grammar export such as
// "tree_sitter_go".
func (e *tsEngine) language(grammarExport string) uint32 {
	if p, ok := e.langPtr[grammarExport]; ok {
		return p
	}
	p := uint32(e.call(e.mod.ExportedFunction(grammarExport)))
	e.langPtr[grammarExport] = p
	return p
}

// symbolNames builds (once per language) the symbol-id -> kind-name table so
// per-node kind resolution happens in pure Go, never across the wazero boundary.
func (e *tsEngine) symbolNames(grammarExport string, lang uint32) map[uint32]string {
	if m, ok := e.symName[grammarExport]; ok {
		return m
	}
	count := uint32(e.call(e.langSymCount, uint64(lang)))
	m := make(map[uint32]string, count)
	for id := uint32(0); id < count; id++ {
		ptr := uint32(e.call(e.langSymName, uint64(lang), uint64(id)))
		m[id] = e.readCStr(ptr)
	}
	e.symName[grammarExport] = m
	return m
}

// parseNodes parses src with the given grammar and returns the whole syntax
// tree as a flat pre-order slice. A guest-side trap is returned as an error;
// the engine and the host process stay alive.
func (e *tsEngine) parseNodes(grammarExport string, src []byte) (nodes []astNode, err error) {
	e.mu.Lock()
	defer e.mu.Unlock()
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("ast: wasm trap (contained): %v", r)
		}
	}()

	lang := e.language(grammarExport)
	parser := e.call(e.parserNew)
	defer e.call(e.parserDelete, parser)
	e.call(e.setLang, parser, uint64(lang))

	sp := uint32(e.call(e.malloc, uint64(len(src)+1)))
	e.mem.Write(sp, src)
	e.mem.WriteByte(sp+uint32(len(src)), 0)
	defer e.call(e.free, uint64(sp))

	tree := e.call(e.parse, parser, 0, uint64(sp), uint64(len(src)))
	if tree == 0 {
		return nil, fmt.Errorf("ast: parse returned null tree")
	}
	defer e.call(e.treeDelete, tree)

	// Pass 1: count nodes (no writes). Pass 2: dump into an exact-size buffer.
	n := uint32(e.call(e.dumpTree, tree, 0, 0))
	if n == 0 {
		return nil, nil
	}
	buf := uint32(e.call(e.malloc, uint64(n)*astRecSize))
	defer e.call(e.free, uint64(buf))
	got := uint32(e.call(e.dumpTree, tree, uint64(buf), uint64(n)))
	if got != n {
		return nil, fmt.Errorf("ast: dump count changed between passes: %d vs %d", n, got)
	}

	raw, ok := e.mem.Read(buf, n*astRecSize)
	if !ok {
		return nil, fmt.Errorf("ast: read dump buffer failed (ptr=%d len=%d)", buf, n*astRecSize)
	}
	names := e.symbolNames(grammarExport, lang)

	nodes = make([]astNode, n)
	for i := range nodes {
		o := uint32(i) * astRecSize
		kindID := binary.LittleEndian.Uint32(raw[o:])
		flags := binary.LittleEndian.Uint32(raw[o+32:])
		nodes[i] = astNode{
			kindID:    kindID,
			kind:      names[kindID],
			startByte: binary.LittleEndian.Uint32(raw[o+4:]),
			endByte:   binary.LittleEndian.Uint32(raw[o+8:]),
			startRow:  binary.LittleEndian.Uint32(raw[o+12:]),
			startCol:  binary.LittleEndian.Uint32(raw[o+16:]),
			endRow:    binary.LittleEndian.Uint32(raw[o+20:]),
			endCol:    binary.LittleEndian.Uint32(raw[o+24:]),
			depth:     binary.LittleEndian.Uint32(raw[o+28:]),
			named:     flags&1 != 0,
			isError:   flags&2 != 0,
			missing:   flags&4 != 0,
			extra:     flags&8 != 0,
		}
	}
	return nodes, nil
}

func (e *tsEngine) readCStr(ptr uint32) string {
	if ptr == 0 {
		return ""
	}
	var b []byte
	for off := ptr; ; off++ {
		c, ok := e.mem.ReadByte(off)
		if !ok || c == 0 {
			break
		}
		b = append(b, c)
	}
	return string(b)
}
