//go:build cgo

package compressors

import (
	"bytes"
	"strings"
	"testing"
)

func elides(t *testing.T, src, mustKeep string) []byte {
	t.Helper()
	out, ok := newCode().Compress([]byte(src))
	if !ok {
		t.Fatalf("expected compression of:\n%s", src)
	}
	if !bytes.Contains(out, []byte("caveman: body elided")) {
		t.Errorf("expected a body elision in:\n%s", out)
	}
	if mustKeep != "" && !bytes.Contains(out, []byte(mustKeep)) {
		t.Errorf("signature %q must survive, got:\n%s", mustKeep, out)
	}
	if len(out) >= len(src) {
		t.Errorf("expected smaller output, got %d >= %d", len(out), len(src))
	}
	return out
}

func TestCodeRust(t *testing.T) {
	elides(t, `use std::collections::HashMap;

pub fn process(items: Vec<String>) -> usize {
    let mut count = 0;
    for it in items {
        if it.is_empty() {
            continue;
        }
        count += 1;
    }
    count
}
`, "pub fn process(items: Vec<String>) -> usize")
}

func TestCodeJava(t *testing.T) {
	elides(t, `package com.example;

import java.util.List;

public class Processor {
    public int process(List<String> items) {
        int count = 0;
        for (String it : items) {
            if (it.isEmpty()) continue;
            count++;
        }
        return count;
    }
}
`, "public int process(List<String> items)")
}

func TestCodeC(t *testing.T) {
	elides(t, `#include <stdio.h>

int process(int n) {
    int sum = 0;
    for (int i = 0; i < n; i++) {
        sum += i;
    }
    return sum;
}
`, "int process(int n)")
}

func TestCodeCpp(t *testing.T) {
	elides(t, `#include <vector>
#include <string>

int Process(const std::vector<std::string>& items) {
    int count = 0;
    for (const auto& it : items) {
        if (it.empty()) continue;
        count++;
    }
    return count;
}
`, "int Process(const std::vector<std::string>& items)")
}

func TestCodeCommentRemove(t *testing.T) {
	src := `package main

// PackageDoc explains the package in a long comment that we want gone.
// Second line of the comment.

func Run() int {
	return 1
}
`
	// Default keeps the comment.
	def, ok := newCode().Compress([]byte(src))
	if !ok {
		t.Fatal("expected compression (default)")
	}
	if !bytes.Contains(def, []byte("PackageDoc explains")) {
		t.Error("default mode must keep top-level comments")
	}
	// CommentRemove drops it but keeps the signature and re-parses.
	rm, ok := newCodeWith(CodeOptions{Comments: CommentRemove}).Compress([]byte(src))
	if !ok {
		t.Fatal("expected compression (CommentRemove)")
	}
	if bytes.Contains(rm, []byte("PackageDoc explains")) {
		t.Errorf("CommentRemove must drop the comment, got:\n%s", rm)
	}
	if !bytes.Contains(rm, []byte("func Run()")) {
		t.Error("signature must survive comment removal")
	}
}

func TestCodePythonDocstringRemove(t *testing.T) {
	src := `"""Module docstring that should be removable."""
import os


class Worker:
    """Class docstring to drop."""

    def run(self):
        return os.getpid()
`
	rm, ok := newCodeWith(CodeOptions{Docstrings: CommentRemove}).Compress([]byte(src))
	if !ok {
		t.Fatal("expected compression")
	}
	if bytes.Contains(rm, []byte("Module docstring")) || bytes.Contains(rm, []byte("Class docstring")) {
		t.Errorf("docstrings should be removed, got:\n%s", rm)
	}
	if !bytes.Contains(rm, []byte("class Worker")) || !bytes.Contains(rm, []byte("import os")) {
		t.Error("structure must survive docstring removal")
	}
}

func TestCodeCommentRemovePreservesDirectives(t *testing.T) {
	// Directive comments carry build/embed/linkage semantics that survive a
	// re-parse — CommentRemove must keep them while still dropping prose.
	src := `//go:build linux && amd64
// +build linux,amd64

// PackageDoc is ordinary prose that may be removed.
package main

//go:embed banner.txt
var banner string

//go:generate stringer -type=T
func F() int { return 1 }
`
	out, ok := newCodeWith(CodeOptions{Comments: CommentRemove}).Compress([]byte(src))
	if !ok {
		t.Fatal("expected compression")
	}
	for _, must := range []string{"//go:build linux", "// +build linux,amd64", "//go:embed banner.txt", "//go:generate"} {
		if !bytes.Contains(out, []byte(must)) {
			t.Errorf("directive %q must NOT be removed, got:\n%s", must, out)
		}
	}
	if bytes.Contains(out, []byte("ordinary prose")) {
		t.Error("prose comment should still be removed")
	}
}

func TestCodePythonDocstringRejectsFStringAndBytes(t *testing.T) {
	// A leading f-string (may have side effects) or bytes literal is NOT a
	// docstring and must not be removed.
	src := `f"""evaluated {1 + 1} expression, not a docstring"""
import os


def run():
    return os.getpid()
`
	out, ok := newCodeWith(CodeOptions{Docstrings: CommentRemove}).Compress([]byte(src))
	// Compression may or may not trigger (the f-string isn't removable); if it
	// does, the f-string must survive.
	if ok && !bytes.Contains(out, []byte("evaluated")) {
		t.Errorf("leading f-string must not be removed as a docstring, got:\n%s", out)
	}
}

func TestCodePythonDocstringOnlyModulePreserved(t *testing.T) {
	// A module whose ONLY statement is a docstring must not be emptied (would be
	// invalid). Removing it is guarded; the compressor should pass through.
	src := strings.Repeat(`"""Only a docstring lives here, nothing else at all in this module file."""
`, 1)
	out, ok := newCodeWith(CodeOptions{Docstrings: CommentRemove}).Compress([]byte(src))
	if ok && !bytes.Contains(out, []byte("Only a docstring")) {
		t.Error("a sole-docstring module must not have its docstring removed")
	}
}
