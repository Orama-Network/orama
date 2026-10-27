package hostfunctions

// A minimal WebAssembly binary encoder for tests that need a real module
// calling real host functions, without a TinyGo toolchain. Only what the tests
// use is encoded: function imports from env, one memory, i32 globals, function
// and memory exports, and active data segments at a constant offset.

const (
	wasmI32 = 0x7f
	wasmI64 = 0x7e

	opEnd       = 0x0b
	opCall      = 0x10
	opDrop      = 0x1a
	opLocalGet  = 0x20
	opGlobalGet = 0x23
	opGlobalSet = 0x24
	opI32Const  = 0x41
	opI32Add    = 0x6a

	exportFunc   = 0x00
	exportMemory = 0x02
)

type wasmImport struct {
	name string // imported from module env
	typ  byte
}

type wasmFunc struct {
	typ  byte
	body []byte // instructions, without the local declarations, ending in opEnd
}

type wasmExport struct {
	name string
	kind byte
	idx  byte
}

type wasmData struct {
	offset int32
	bytes  []byte
}

type wasmModule struct {
	types   [][]byte // each a full functype encoding
	imports []wasmImport
	funcs   []wasmFunc
	globals []int32 // mutable i32 globals with these initial values
	exports []wasmExport
	data    []wasmData
}

func uleb(v uint64) []byte {
	var out []byte
	for {
		b := byte(v & 0x7f)
		v >>= 7
		if v != 0 {
			out = append(out, b|0x80)
			continue
		}
		return append(out, b)
	}
}

func sleb32(v int32) []byte {
	var out []byte
	for {
		b := byte(v & 0x7f)
		v >>= 7
		if (v == 0 && b&0x40 == 0) || (v == -1 && b&0x40 != 0) {
			return append(out, b)
		}
		out = append(out, b|0x80)
	}
}

func wasmName(s string) []byte { return append(uleb(uint64(len(s))), s...) }

func wasmSection(id byte, items [][]byte) []byte {
	body := uleb(uint64(len(items)))
	for _, it := range items {
		body = append(body, it...)
	}
	return append(append([]byte{id}, uleb(uint64(len(body)))...), body...)
}

// functype encodes a function type of i32/i64 params and results.
func functype(params, results []byte) []byte {
	out := append([]byte{0x60}, uleb(uint64(len(params)))...)
	out = append(out, params...)
	out = append(out, uleb(uint64(len(results)))...)
	return append(out, results...)
}

func (m wasmModule) encode() []byte {
	out := []byte{0x00, 0x61, 0x73, 0x6d, 0x01, 0x00, 0x00, 0x00}
	out = append(out, wasmSection(1, m.types)...)
	var imports [][]byte
	for _, im := range m.imports {
		imports = append(imports, append(append(append(wasmName("env"), wasmName(im.name)...), exportFunc), im.typ))
	}
	out = append(out, wasmSection(2, imports)...)
	var funcs [][]byte
	for _, f := range m.funcs {
		funcs = append(funcs, []byte{f.typ})
	}
	out = append(out, wasmSection(3, funcs)...)
	out = append(out, wasmSection(5, [][]byte{{0x00, 0x01}})...) // one memory, min 1 page
	var globals [][]byte
	for _, g := range m.globals {
		globals = append(globals, append(append([]byte{wasmI32, 0x01, opI32Const}, sleb32(g)...), opEnd))
	}
	if len(globals) > 0 {
		out = append(out, wasmSection(6, globals)...)
	}
	var exports [][]byte
	for _, e := range m.exports {
		exports = append(exports, append(wasmName(e.name), e.kind, e.idx))
	}
	out = append(out, wasmSection(7, exports)...)
	var code [][]byte
	for _, f := range m.funcs {
		body := append([]byte{0x00}, f.body...) // no locals
		code = append(code, append(uleb(uint64(len(body))), body...))
	}
	out = append(out, wasmSection(10, code)...)
	var data [][]byte
	for _, d := range m.data {
		seg := concat([]byte{0x00}, i32c(d.offset), []byte{opEnd})
		data = append(data, append(append(seg, uleb(uint64(len(d.bytes)))...), d.bytes...))
	}
	if len(data) > 0 {
		out = append(out, wasmSection(11, data)...)
	}
	return out
}

// bumpAlloc is orama_alloc(size) -> ptr over global 0: it returns the global
// and advances it by size. Nothing is ever freed; the tests' modules live for
// a few calls.
var bumpAlloc = []byte{
	opGlobalGet, 0x00,
	opGlobalGet, 0x00, opLocalGet, 0x00, opI32Add, opGlobalSet, 0x00,
	opEnd,
}

// i32c is an i32.const instruction.
func i32c(v int32) []byte { return append([]byte{opI32Const}, sleb32(v)...) }

func concat(parts ...[]byte) []byte {
	var out []byte
	for _, p := range parts {
		out = append(out, p...)
	}
	return out
}
