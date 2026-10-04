package compressors

import (
	"bytes"
	"strings"
	"testing"
)

// A class-level GDScript file with the shapes that matter: annotations, a
// signal, typed and static functions, a one-line function, a multi-line
// parameter list, an inner class with methods, and a triple-quoted string that
// contains a fake function header.
const gdSource = `@tool
class_name HistoryLayer
extends Control

## Shows the dialog history.

signal entry_added(text: String)

const MAX_ENTRIES := 200
@export var scroll_to_bottom: bool = true
@onready var list: VBoxContainer = $List

var _entries: Array[String] = []


func _ready() -> void:
	list.clear()
	for e in _entries:
		_append(e)


func add_entry(text: String) -> void:
	if text.is_empty():
		return
	_entries.append(text)
	entry_added.emit(text)


static func make(text: String) -> HistoryLayer:
	var layer := HistoryLayer.new()
	layer.add_entry(text)
	return layer


@rpc("any_peer") func ping(from: int) -> void:
	print(from)


func count() -> int: return _entries.size()


func describe(
		prefix: String,
		suffix: String = "",
) -> String:
	return prefix + str(_entries.size()) + suffix


class Entry:
	var text: String

	func _init(t: String) -> void:
		text = t

	func shout() -> String:
		return text.to_upper()


const HELP := """
func fake_header():
	not code
"""
`

const gdElided = `@tool
class_name HistoryLayer
extends Control

## Shows the dialog history.

signal entry_added(text: String)

const MAX_ENTRIES := 200
@export var scroll_to_bottom: bool = true
@onready var list: VBoxContainer = $List

var _entries: Array[String] = []


func _ready() -> void:
	pass # caveman: body elided


func add_entry(text: String) -> void:
	pass # caveman: body elided


static func make(text: String) -> HistoryLayer:
	pass # caveman: body elided


@rpc("any_peer") func ping(from: int) -> void:
	pass # caveman: body elided


func count() -> int: return _entries.size()


func describe(
		prefix: String,
		suffix: String = "",
) -> String:
	pass # caveman: body elided


class Entry:
	var text: String

	func _init(t: String) -> void:
		pass # caveman: body elided

	func shout() -> String:
		pass # caveman: body elided


const HELP := """
func fake_header():
	not code
"""
`

func TestGDScriptElidesBodiesKeepsDeclarations(t *testing.T) {
	out, ok := compressGDScript([]byte(gdSource))
	if !ok {
		t.Fatal("expected the GDScript sample to compress")
	}
	if string(out) != gdElided {
		t.Fatalf("unexpected output:\n%s\n--- want ---\n%s", out, gdElided)
	}
}

func TestGDScriptIdempotent(t *testing.T) {
	once, ok := compressGDScript([]byte(gdSource))
	if !ok {
		t.Fatal("first pass must compress")
	}
	if twice, ok := compressGDScript(once); ok && !bytes.Equal(twice, once) {
		t.Fatalf("second pass changed the output:\n%s", twice)
	}
}

func TestGDScriptSpaceIndentation(t *testing.T) {
	src := "extends Node\n\n\nfunc _ready() -> void:\n    var x = 1\n    print(x)\n\n\nfunc helper(a, b):\n    return a + b\n"
	want := "extends Node\n\n\nfunc _ready() -> void:\n    pass # caveman: body elided\n\n\nfunc helper(a, b):\n    pass # caveman: body elided\n"
	out, ok := compressGDScript([]byte(src))
	if !ok {
		t.Fatal("expected compression")
	}
	if string(out) != want {
		t.Fatalf("unexpected output:\n%q\nwant:\n%q", out, want)
	}
}

func TestGDScriptBodyEndsAtDedentNotAtBlankLine(t *testing.T) {
	// A blank line inside a body does not end it; trailing blank lines after a
	// body are kept as spacing rather than swallowed into the elision.
	src := "extends Node\n\nfunc a():\n\tvar x = 1\n\n\tprint(x)\n\nvar after := 2\n"
	want := "extends Node\n\nfunc a():\n\tpass # caveman: body elided\n\nvar after := 2\n"
	out, ok := compressGDScript([]byte(src))
	if !ok {
		t.Fatal("expected compression")
	}
	if string(out) != want {
		t.Fatalf("unexpected output:\n%q\nwant:\n%q", out, want)
	}
}

func TestGDScriptNothingToElidePassesThrough(t *testing.T) {
	for name, src := range map[string]string{
		"declarations only": "extends Node\n\n@export var speed := 1.0\nsignal done\n",
		"one-line bodies":   "extends Node\n\nfunc a(): return 1\nfunc b(): pass\n",
		"already elided":    "extends Node\n\nfunc a():\n\tpass # caveman: body elided\n",
	} {
		if out, ok := compressGDScript([]byte(src)); ok {
			t.Errorf("%s: expected pass-through, got:\n%s", name, out)
		}
	}
}

func TestIsGDScriptRejectsOtherLanguages(t *testing.T) {
	for name, src := range map[string]string{
		"python": "import os\n\ndef main():\n    return os.getcwd()\n",
		"go":     "package main\n\nfunc main() {\n\tprintln(1)\n}\n",
		"ts":     "export function f(a: number): number {\n  return a + 1;\n}\n",
		"prose":  "The func keyword: a reserved word in several languages.\n",
	} {
		if isGDScript([]byte(src)) {
			t.Errorf("%s misdetected as GDScript", name)
		}
	}
	if !isGDScript([]byte(gdSource)) {
		t.Error("the GDScript sample must be detected")
	}
}

// The code compressor must route GDScript to the line-based elider in every
// build: the cgo build has no GDScript grammar and the pure-Go build has no
// grammar at all.
func TestCodeCompressorHandlesGDScriptInEveryBuild(t *testing.T) {
	out, ok := newCode().Compress([]byte(gdSource))
	if !ok {
		t.Fatal("the code compressor must accept GDScript")
	}
	if string(out) != gdElided {
		t.Fatalf("unexpected output:\n%s", out)
	}
	if strings.Count(string(out), "caveman: body elided") != 7 {
		t.Errorf("expected seven elided bodies, got %d", strings.Count(string(out), "caveman: body elided"))
	}
}
