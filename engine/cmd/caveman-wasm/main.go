//go:build js && wasm

// Command caveman-wasm is the engine compiled for the browser. It registers
// JS-callable globals — cavemanCompress, cavemanDetect, cavemanRetrieve — backed
// by the same compressors as the native engine, with an in-memory CCR store.
// Build it with `make engine-wasm`. Token figures are local estimates.
package main

import (
	"syscall/js"

	"github.com/JuliusBrussee/caveman/engine"
	"github.com/JuliusBrussee/caveman/engine/ccr"
)

func main() {
	store, err := ccr.OpenMemory()
	if err != nil {
		panic(err) // an in-memory store cannot fail to open; if it does, fail loudly
	}
	eng := engine.New(store, nil)

	js.Global().Set("cavemanCompress", js.FuncOf(func(_ js.Value, args []js.Value) any {
		if len(args) < 1 || args[0].Type() != js.TypeString {
			return passThrough("")
		}
		return compress(eng, args[0].String())
	}))

	js.Global().Set("cavemanDetect", js.FuncOf(func(_ js.Value, args []js.Value) any {
		if len(args) < 1 || args[0].Type() != js.TypeString {
			return engine.TypeText
		}
		return eng.Detect([]byte(args[0].String()))
	}))

	js.Global().Set("cavemanRetrieve", js.FuncOf(func(_ js.Value, args []js.Value) any {
		if len(args) < 1 || args[0].Type() != js.TypeString {
			return map[string]any{"ok": false, "error": "cave_invalid_arguments"}
		}
		original, err := eng.Retrieve(args[0].String())
		if err != nil {
			return map[string]any{"ok": false, "error": "cave_unknown_handle"}
		}
		return map[string]any{"ok": true, "original": string(original)}
	}))

	// Signal readiness to the host page if it provided a callback, then block so
	// the registered functions stay callable for the page's lifetime.
	if ready := js.Global().Get("cavemanReady"); ready.Type() == js.TypeFunction {
		ready.Invoke()
	}
	select {}
}

// compress runs the engine and returns a plain JS object. It is fail-closed: on
// any engine error it returns the original unchanged with ratio 0 — never throws.
func compress(eng *engine.Engine, input string) any {
	res, err := eng.Compress([]byte(input), engine.Options{Mode: engine.ModeCompress})
	if err != nil {
		return passThrough(input)
	}
	var handle any
	if res.RecoveryHandle != "" {
		handle = res.RecoveryHandle
	}
	return map[string]any{
		"output":            string(res.Output),
		"ratio":             res.Ratio,
		"tokens_before":     res.TokensBefore,
		"tokens_after":      res.TokensAfter,
		"token_count_basis": res.TokenCountBasis,
		"basis":             res.Basis,
		"content_type":      res.ContentType,
		"recovery_handle":   handle,
	}
}

func passThrough(input string) any {
	return map[string]any{
		"output":            input,
		"ratio":             0.0,
		"tokens_before":     0,
		"tokens_after":      0,
		"token_count_basis": "unavailable",
		"basis":             engine.BasisInferred,
		"content_type":      engine.TypeText,
		"recovery_handle":   nil,
	}
}
