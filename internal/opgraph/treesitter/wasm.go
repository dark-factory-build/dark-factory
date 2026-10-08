package treesitter

import (
	"bytes"
	"encoding/binary"
	"errors"
)

// The few WebAssembly binary details linking needs: the dylink.0 section
// Emscripten side modules carry, import module renames, and modules that
// only define a memory, a table and globals for others to import.

var errBinary = errors.New("treesitter: malformed WebAssembly binary")

type reader struct {
	data []byte
	at   int
	err  error
}

func (r *reader) byte() byte {
	if r.at >= len(r.data) {
		r.err = errBinary
		return 0
	}
	r.at++
	return r.data[r.at-1]
}

func (r *reader) uleb() uint32 {
	var value uint32
	for shift := 0; shift < 35; shift += 7 {
		b := r.byte()
		value |= uint32(b&0x7f) << shift
		if b&0x80 == 0 {
			return value
		}
	}
	r.err = errBinary
	return 0
}

func (r *reader) bytes(n uint32) []byte {
	if r.err != nil || uint64(r.at)+uint64(n) > uint64(len(r.data)) {
		r.err = errBinary
		return nil
	}
	r.at += int(n)
	return r.data[r.at-int(n) : r.at]
}

func (r *reader) limits() {
	if r.byte()&1 != 0 {
		r.uleb()
	}
	r.uleb()
}

// sections calls visit with each section's id and content.
func sections(module []byte, visit func(id byte, content *reader)) error {
	if len(module) < 8 || !bytes.Equal(module[:8], []byte("\x00asm\x01\x00\x00\x00")) {
		return errBinary
	}
	r := &reader{data: module, at: 8}
	for r.at < len(module) && r.err == nil {
		id := r.byte()
		size := r.uleb()
		start := r.at
		content := r.bytes(size)
		if r.err == nil {
			section := &reader{data: module[:start+len(content)], at: start}
			visit(id, section)
			if section.err != nil {
				return section.err
			}
		}
	}
	return r.err
}

// dylink is a side module's memory and table needs.
type dylink struct{ memory, memoryAlign, table uint32 }

func readDylink(module []byte) (dylink, error) {
	var info dylink
	found := false
	err := sections(module, func(id byte, r *reader) {
		if id != 0 || string(r.bytes(r.uleb())) != "dylink.0" {
			return
		}
		for r.at < len(r.data) && r.err == nil {
			kind, size := r.byte(), r.uleb()
			end := r.at + int(size)
			if kind == 1 { // WASM_DYLINK_MEM_INFO
				info = dylink{memory: r.uleb(), memoryAlign: r.uleb(), table: r.uleb()}
				found = true
			}
			r.at = end
		}
	})
	if err == nil && !found {
		err = errBinary
	}
	return info, err
}

// renameImports rewrites each import's module name in place. A name must
// keep its length, so no section size changes.
func renameImports(module []byte, rename func(module, name string, kind byte) string) error {
	out := module
	return sections(module, func(id byte, r *reader) {
		if id != 2 {
			return
		}
		for count := r.uleb(); count > 0 && r.err == nil; count-- {
			length := r.uleb()
			at := r.at
			from := string(r.bytes(length))
			name := string(r.bytes(r.uleb()))
			kind := r.byte()
			switch kind {
			case 0: // function
				r.uleb()
			case 1: // table
				r.byte()
				r.limits()
			case 2: // memory
				r.limits()
			case 3: // global
				r.byte()
				r.byte()
			default:
				r.err = errBinary
			}
			if to := rename(from, name, kind); r.err == nil && to != from {
				if len(to) != len(from) {
					r.err = errBinary
					return
				}
				copy(out[at:], to)
			}
		}
	})
}

type global struct {
	name    string
	mutable bool
	value   uint32
}

// definitions encodes a module that defines, and exports as "memory" and
// "__indirect_function_table", an optional memory and table, plus i32 globals.
func definitions(memoryMin, memoryMax, table uint32, globals []global) []byte {
	module := []byte("\x00asm\x01\x00\x00\x00")
	section := func(id byte, body []byte) {
		module = append(module, id)
		module = binary.AppendUvarint(module, uint64(len(body)))
		module = append(module, body...)
	}
	name := func(body []byte, text string) []byte {
		return append(binary.AppendUvarint(body, uint64(len(text))), text...)
	}
	var exports []byte
	count := 0
	if table > 0 {
		section(4, binary.AppendUvarint([]byte{1, 0x70, 0}, uint64(table)))
		exports = append(name(exports, "__indirect_function_table"), 1, 0)
		count++
	}
	if memoryMax > 0 {
		body := binary.AppendUvarint([]byte{1, 1}, uint64(memoryMin))
		section(5, binary.AppendUvarint(body, uint64(memoryMax)))
		exports = append(name(exports, "memory"), 2, 0)
		count++
	}
	if len(globals) > 0 {
		body := binary.AppendUvarint(nil, uint64(len(globals)))
		for index, item := range globals {
			mutable := byte(0)
			if item.mutable {
				mutable = 1
			}
			body = append(body, 0x7f, mutable, 0x41)
			body = sleb(body, int32(item.value))
			body = append(body, 0x0b)
			exports = binary.AppendUvarint(append(name(exports, item.name), 3), uint64(index))
			count++
		}
		section(6, body)
	}
	section(7, append(binary.AppendUvarint(nil, uint64(count)), exports...))
	return module
}

// sleb appends a signed LEB128 value, as i32.const takes.
func sleb(out []byte, value int32) []byte {
	for {
		b := byte(value & 0x7f)
		value >>= 7
		if value == 0 && b&0x40 == 0 || value == -1 && b&0x40 != 0 {
			return append(out, b)
		}
		out = append(out, b|0x80)
	}
}
