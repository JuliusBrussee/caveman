//go:build cgo

package compressors

import (
	"bytes"
	"testing"
)

// `#ifdef`, `#ifndef` and `typedef` all contain "def ", which the Python sniff
// matched, so a guarded C or C++ file was parsed as Python and passed through.
func TestSniffIfdefIsNotPython(t *testing.T) {
	src := `#ifndef _WIN32
#include <unistd.h>
#endif
#include <string>

int count(const std::string &s) {
	int n = 0;
	for (char c : s) {
		if (c == ':') n++;
	}
	return n;
}
`
	l := sniffLanguage([]byte(src))
	if l == nil || l.name == "python" {
		t.Fatalf("sniffed %v, want C or C++", l)
	}
	elides(t, src, "int count(const std::string &s)")
}

// Godot-style C++: a class-body macro without a trailing semicolon and a typed
// anonymous enum. tree-sitter recovers from both by inserting a token (a
// MISSING node), which used to fail the whole file. Real ERROR nodes still do.
func TestCodeCppToleratesRecoveredTokens(t *testing.T) {
	src := `#include <godot_cpp/classes/object.hpp>

using namespace godot;

enum : int64_t {
	PASS_THROUGH = 0,
	PASS_REGULAR = 1,
};

class AvaNativeKernel : public Object {
	GDCLASS(AvaNativeKernel, Object)

protected:
	static void _bind_methods() {
		ClassDB::bind_method(D_METHOD("deform_pass"), &AvaNativeKernel::deform_pass);
	}

public:
	int64_t deform_pass(int64_t mode) {
		if (mode == PASS_THROUGH) {
			return 0;
		}
		return mode * 2;
	}
};
`
	out := elides(t, src, "int64_t deform_pass(int64_t mode)")
	for _, keep := range []string{"GDCLASS(AvaNativeKernel, Object)", "enum : int64_t {", "PASS_REGULAR = 1,", "static void _bind_methods()"} {
		if !bytes.Contains(out, []byte(keep)) {
			t.Errorf("%q must survive, got:\n%s", keep, out)
		}
	}
	if bytes.Contains(out, []byte("ClassDB::bind_method")) {
		t.Errorf("method bodies must be elided, got:\n%s", out)
	}
}

// A macro in declaration position is a real ERROR node, not a recovered token,
// and the file still passes through untouched.
func TestCodeCppRealErrorsStillPassThrough(t *testing.T) {
	src := `#include <godot_cpp/godot.hpp>

extern "C" {
GDExtensionBool GDE_EXPORT ava_init(GDExtensionInterfaceGetProcAddress p_get_proc_address) {
	return true;
}
}
`
	if out, ok := newCode().Compress([]byte(src)); ok {
		t.Fatalf("expected pass-through for code with an ERROR node, got:\n%s", out)
	}
}
