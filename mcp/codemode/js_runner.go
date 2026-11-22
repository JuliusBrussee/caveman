package codemode

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"time"

	"modernc.org/quickjs"

	"github.com/JuliusBrussee/caveman/mcp"
)

const (
	defaultJSTimeout     = 30 * time.Second
	maxJSTimeout         = 60 * time.Second
	defaultJSMemoryLimit = 32 * 1024 * 1024 // 32 MiB QuickJS heap limit
	defaultJSMaxStack    = 1024             // QuickJS call-stack slot limit
	maxJSToolCalls       = 64
	maxJSCodeBytes       = 64 * 1024
	maxJSLogEntries      = 100
	maxJSLogLineBytes    = 4096
)

// qjsBootstrap initializes a locked-down QuickJS ES2023 realm with:
//   - zero OS / filesystem / network / env capabilities (no std/os/fs/process/require/fetch)
//   - ONLY `mcp.<server>.<tool>(args)` async RPC proxy + frozen captured `console`
//   - private closure-scoped host bridge functions deleted from globalThis before user code runs
//   - defense-in-depth lockdown of eval and Function constructors
const qjsBootstrap = `(function(__dispatch, __log, __finish) {
  'use strict';
  delete globalThis.__caveman_dispatch;
  delete globalThis.__caveman_log;
  delete globalThis.__caveman_finish;

  const _Map = Map;
  const _MapGet = Map.prototype.get;
  const _MapSet = Map.prototype.set;
  const _MapDelete = Map.prototype.delete;
  const _Promise = Promise;
  const _PromiseThen = Promise.prototype.then;
  const _FnCall = Function.prototype.call;
  const _ArrayIsArray = Array.isArray;
  const _ArrayMap = Array.prototype.map;
  const _ArrayJoin = Array.prototype.join;
  const _String = String;
  const _Error = Error;
  const _Proxy = Proxy;
  const _Object = Object;
  const _FunctionProto = Function.prototype;
  const _JSONParse = JSON.parse;
  const _JSONStringify = JSON.stringify;

  const pending = new _Map();
  let nextId = 1;

  function formatError(err) {
    if (err && typeof err === 'object' && typeof err.message === 'string') {
      return err.message;
    }
    try {
      return _String(err);
    } catch (_) {
      return 'unknown error';
    }
  }

  function safeSerialize(v) {
    try {
      const s = _JSONStringify(v);
      return s === undefined ? 'undefined' : s;
    } catch (_) {
      try { return _String(v); } catch (_2) { return '[unserializable]'; }
    }
  }

  function callTool(tool, args) {
    return new _Promise((resolve, reject) => {
      const id = nextId++;
      _FnCall.call(_MapSet, pending, id, { resolve, reject });
      let argsJSON = '{}';
      if (args !== undefined && args !== null) {
        if (typeof args !== 'object' || _ArrayIsArray(args)) {
          _FnCall.call(_MapDelete, pending, id);
          reject(new _Error('tool arguments must be a JSON object'));
          return;
        }
        try {
          argsJSON = _JSONStringify(args);
        } catch (err) {
          _FnCall.call(_MapDelete, pending, id);
          reject(new _Error('tool arguments are not JSON-serializable: ' + formatError(err)));
          return;
        }
      }
      try {
        __dispatch(id, tool, argsJSON);
      } catch (err) {
        _FnCall.call(_MapDelete, pending, id);
        reject(new _Error(formatError(err)));
      }
    });
  }

  const mcp = new _Proxy(_Object.create(null), {
    get(_t, server) {
      if (typeof server !== 'string') return undefined;
      return new _Proxy(_Object.create(null), {
        get(_t2, tool) {
          if (typeof tool !== 'string') return undefined;
          return (args) => callTool(server + '.' + tool, args);
        },
      });
    },
  });

  const formatLogArgs = (a) => _FnCall.call(_ArrayJoin, _FnCall.call(_ArrayMap, a, (v) => typeof v === 'string' ? v : safeSerialize(v)), ' ');
  const safeConsole = _Object.freeze({
    log: (...a) => __log(formatLogArgs(a)),
    warn: (...a) => __log(formatLogArgs(a)),
    error: (...a) => __log(formatLogArgs(a)),
  });

  _Object.defineProperty(globalThis, 'mcp', {
    value: mcp,
    writable: false,
    enumerable: true,
    configurable: false,
  });
  _Object.defineProperty(globalThis, 'console', {
    value: safeConsole,
    writable: false,
    enumerable: true,
    configurable: false,
  });

  const blockedCodeGen = function() {
    throw new _Error('dynamic code generation is disabled in codemode QuickJS sandbox');
  };
  _Object.defineProperty(globalThis, 'eval', {
    value: blockedCodeGen,
    writable: false,
    configurable: false,
  });
  _Object.defineProperty(globalThis, 'Function', {
    value: blockedCodeGen,
    writable: false,
    configurable: false,
  });
  _Object.defineProperty(_FunctionProto, 'constructor', {
    value: blockedCodeGen,
    writable: false,
    configurable: false,
  });
  for (const proto of [
    _Object.getPrototypeOf(async function() {}),
    _Object.getPrototypeOf(function*() {}),
    _Object.getPrototypeOf(async function*() {}),
  ]) {
    if (proto) {
      try {
        _Object.defineProperty(proto, 'constructor', {
          value: blockedCodeGen,
          writable: false,
          configurable: false,
        });
      } catch (_) {}
    }
  }

  function settle(id, ok, payload) {
    const p = _FnCall.call(_MapGet, pending, id);
    if (!p) return;
    _FnCall.call(_MapDelete, pending, id);
    if (ok) {
      try {
        p.resolve(_JSONParse(payload));
      } catch (err) {
        p.reject(new _Error('failed to decode tool response: ' + formatError(err)));
      }
    } else {
      p.reject(new _Error(payload));
    }
  }

  let finishedOnce = false;
  function attach(promise) {
    _FnCall.call(
      _PromiseThen,
      promise,
      (val) => {
        if (finishedOnce) return;
        finishedOnce = true;
        let encoded;
        try {
          encoded = _JSONStringify(val === undefined ? null : val);
          if (encoded === undefined) encoded = 'null';
        } catch (err) {
          __finish(false, 'result is not JSON-serializable: ' + formatError(err));
          return;
        }
        __finish(true, encoded);
      },
      (err) => {
        if (finishedOnce) return;
        finishedOnce = true;
        __finish(false, formatError(err));
      }
    );
  }

  return { settle, attach };
})(globalThis.__caveman_dispatch, globalThis.__caveman_log, globalThis.__caveman_finish);
`

type jsCallCompletion struct {
	id         int
	ok         bool
	payload    string
	ccrHandles map[string]string
}

func (g *Gateway) handleExecuteJS(args executeArgs, maxOut int, allowWrites bool) mcp.ToolResult {
	g.mu.RLock()
	enabled := g.jsEnabled
	memLimit := g.jsMemoryLimit
	g.mu.RUnlock()

	if !enabled {
		return mcp.ToolError("cave_js_disabled", "embedded QuickJS sandbox is disabled; use steps pipeline instead")
	}
	if len(args.Code) > maxJSCodeBytes {
		return mcp.ToolError("cave_limit_exceeded", fmt.Sprintf("code exceeds %d byte limit", maxJSCodeBytes))
	}
	if memLimit == 0 {
		memLimit = defaultJSMemoryLimit
	}

	timeout := defaultJSTimeout
	if args.TimeoutMS > 0 {
		timeout = time.Duration(args.TimeoutMS) * time.Millisecond
		if timeout > maxJSTimeout {
			timeout = maxJSTimeout
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	vm, err := quickjs.NewVM()
	if err != nil {
		return mcp.ToolError("cave_js_error", fmt.Sprintf("init QuickJS VM: %v", err))
	}
	// Synchronize context-cancellation interrupts against vm.Close() so an
	// AfterFunc goroutine that already started running never calls vm.Interrupt()
	// concurrently with or after vm.Close().
	var vmCloseMu sync.Mutex
	vmClosed := false
	defer func() {
		vmCloseMu.Lock()
		vmClosed = true
		vm.Close()
		vmCloseMu.Unlock()
	}()

	vm.SetMemoryLimit(memLimit)
	vm.SetMaxStackSize(defaultJSMaxStack)
	if err := vm.SetEvalTimeout(timeout); err != nil {
		return mcp.ToolError("cave_js_error", fmt.Sprintf("configure QuickJS timeout: %v", err))
	}
	stopInterrupt := context.AfterFunc(ctx, func() {
		vmCloseMu.Lock()
		if !vmClosed {
			vm.Interrupt()
		}
		vmCloseMu.Unlock()
	})
	defer stopInterrupt()

	var (
		logs          []string
		ccrHandles    = map[string]string{}
		toolCalls     = 0
		inFlight      = 0
		finished      = false
		finishOK      = false
		finishPayload = ""
		doneCh        = make(chan jsCallCompletion, maxJSToolCalls+4)
	)

	if err := vm.RegisterHostFunc("__caveman_dispatch", func(hArgs []any) (any, error) {
		if len(hArgs) < 3 {
			return nil, errors.New("invalid dispatch arguments")
		}
		id, ok := jsToInt(hArgs[0])
		if !ok {
			return nil, errors.New("invalid call id")
		}
		toolName, _ := hArgs[1].(string)
		argsJSON, _ := hArgs[2].(string)

		toolCalls++
		if toolCalls > maxJSToolCalls {
			return nil, fmt.Errorf("exceeded max %d tool calls per execution", maxJSToolCalls)
		}

		var callArgs map[string]any
		if strings.TrimSpace(argsJSON) != "" {
			dec := json.NewDecoder(strings.NewReader(argsJSON))
			dec.UseNumber()
			if err := dec.Decode(&callArgs); err != nil {
				return nil, fmt.Errorf("decode tool args: %w", err)
			}
		}
		if callArgs == nil {
			callArgs = map[string]any{}
		}

		stepID := fmt.Sprintf("call_%d", toolCalls)
		inFlight++
		go func(reqID int, tool string, a map[string]any, sid string) {
			localCCR := map[string]string{}
			val, cerr := g.invokeUpstream(ctx, tool, a, sid, localCCR, allowWrites)
			if cerr != nil {
				doneCh <- jsCallCompletion{id: reqID, ok: false, payload: cerr.Error()}
				return
			}
			encoded, merr := json.Marshal(val)
			if merr != nil {
				doneCh <- jsCallCompletion{id: reqID, ok: false, payload: fmt.Sprintf("encode tool result: %v", merr)}
				return
			}
			doneCh <- jsCallCompletion{id: reqID, ok: true, payload: string(encoded), ccrHandles: localCCR}
		}(id, toolName, callArgs, stepID)
		return nil, nil
	}); err != nil {
		return mcp.ToolError("cave_js_error", fmt.Sprintf("register QuickJS dispatch: %v", err))
	}

	if err := vm.RegisterHostFunc("__caveman_log", func(hArgs []any) (any, error) {
		if len(logs) >= maxJSLogEntries || len(hArgs) == 0 {
			return nil, nil
		}
		line := fmt.Sprint(hArgs[0])
		if len(line) > maxJSLogLineBytes {
			line = line[:maxJSLogLineBytes]
		}
		logs = append(logs, line)
		return nil, nil
	}); err != nil {
		return mcp.ToolError("cave_js_error", fmt.Sprintf("register QuickJS console: %v", err))
	}

	if err := vm.RegisterHostFunc("__caveman_finish", func(hArgs []any) (any, error) {
		if len(hArgs) < 2 {
			return nil, nil
		}
		finished = true
		finishOK, _ = hArgs[0].(bool)
		finishPayload, _ = hArgs[1].(string)
		return nil, nil
	}); err != nil {
		return mcp.ToolError("cave_js_error", fmt.Sprintf("register QuickJS finish: %v", err))
	}

	bridgeVal, err := vm.EvalValue(qjsBootstrap, quickjs.EvalGlobal)
	if err != nil {
		return classifyJSError(ctx, timeout, err)
	}
	defer bridgeVal.Free()

	settleAtom, err := vm.NewAtom("settle")
	if err != nil {
		return mcp.ToolError("cave_js_error", fmt.Sprintf("init settle atom: %v", err))
	}
	attachAtom, err := vm.NewAtom("attach")
	if err != nil {
		return mcp.ToolError("cave_js_error", fmt.Sprintf("init attach atom: %v", err))
	}
	settleFn, err := bridgeVal.GetPropertyValue(settleAtom)
	if err != nil {
		return mcp.ToolError("cave_js_error", fmt.Sprintf("lookup settle bridge: %v", err))
	}
	defer settleFn.Free()
	attachFn, err := bridgeVal.GetPropertyValue(attachAtom)
	if err != nil {
		return mcp.ToolError("cave_js_error", fmt.Sprintf("lookup attach bridge: %v", err))
	}
	defer attachFn.Free()

	wrapped := "(async () => {\n" + args.Code + "\n})()"
	promiseVal, err := vm.EvalValue(wrapped, quickjs.EvalGlobal)
	if err != nil {
		return classifyJSError(ctx, timeout, err)
	}
	defer promiseVal.Free()

	if _, err := attachFn.Call(quickjs.UndefinedValue, promiseVal); err != nil {
		return classifyJSError(ctx, timeout, err)
	}

	deadline, _ := ctx.Deadline()
	resetRemainingTimeout := func() error {
		if ctx.Err() != nil {
			return ctx.Err()
		}
		rem := time.Until(deadline)
		if rem <= 0 {
			return context.DeadlineExceeded
		}
		return vm.SetEvalTimeout(rem)
	}

	if err := resetRemainingTimeout(); err != nil {
		return mcp.ToolError("cave_js_timeout", fmt.Sprintf("codemode QuickJS execution timed out after %s", timeout))
	}
	if _, err := vm.ExecutePendingJobs(); err != nil {
		return classifyJSError(ctx, timeout, err)
	}

	for !finished {
		if inFlight == 0 {
			if ctx.Err() != nil {
				return mcp.ToolError("cave_js_timeout", fmt.Sprintf("codemode QuickJS execution timed out after %s", timeout))
			}
			return mcp.ToolError("cave_js_error", "async script returned an unsettled promise with no pending tool calls")
		}
		select {
		case <-ctx.Done():
			vmCloseMu.Lock()
			if !vmClosed {
				vm.Interrupt()
			}
			vmCloseMu.Unlock()
			return mcp.ToolError("cave_js_timeout", fmt.Sprintf("codemode QuickJS execution timed out after %s", timeout))
		case comp := <-doneCh:
			inFlight--
			for k, v := range comp.ccrHandles {
				ccrHandles[k] = v
			}
			// modernc.org/quickjs enter() resets interrupt and restarts timeout;
			// check ctx and shrink EvalTimeout to remaining budget before every re-entry.
			if err := resetRemainingTimeout(); err != nil {
				return mcp.ToolError("cave_js_timeout", fmt.Sprintf("codemode QuickJS execution timed out after %s", timeout))
			}
			if _, err := settleFn.Call(quickjs.UndefinedValue, comp.id, comp.ok, comp.payload); err != nil {
				return classifyJSError(ctx, timeout, err)
			}
			if finished {
				break
			}
			if err := resetRemainingTimeout(); err != nil {
				return mcp.ToolError("cave_js_timeout", fmt.Sprintf("codemode QuickJS execution timed out after %s", timeout))
			}
			if _, err := vm.ExecutePendingJobs(); err != nil {
				return classifyJSError(ctx, timeout, err)
			}
		}
	}

	if !finishOK {
		if errors.Is(ctx.Err(), context.DeadlineExceeded) || isInterruptError(finishPayload) {
			return mcp.ToolError("cave_js_timeout", fmt.Sprintf("codemode QuickJS execution timed out after %s", timeout))
		}
		if isOOMError(finishPayload) {
			return mcp.ToolError("cave_js_oom", fmt.Sprintf("codemode QuickJS execution exceeded memory limit (%d bytes)", memLimit))
		}
		if strings.Contains(finishPayload, "call codemode_mutate instead") {
			return mcp.ToolError("cave_write_not_allowed", finishPayload)
		}
		return mcp.ToolError("cave_js_error", finishPayload)
	}

	const maxFinishPayloadBytes = 8 * 1024 * 1024
	if len(finishPayload) > maxFinishPayloadBytes {
		return mcp.ToolError("cave_limit_exceeded", fmt.Sprintf("QuickJS result exceeds %d byte cap; project/filter inside code before returning", maxFinishPayloadBytes))
	}

	var decodedVal any
	if strings.TrimSpace(finishPayload) != "" {
		dec := json.NewDecoder(strings.NewReader(finishPayload))
		dec.UseNumber()
		if err := dec.Decode(&decodedVal); err != nil {
			return mcp.ToolError("cave_marshal_failed", fmt.Sprintf("decode QuickJS result: %v", err))
		}
	}

	out := map[string]any{
		"result":     decodedVal,
		"tool_calls": toolCalls,
	}
	if len(logs) > 0 {
		out["logs"] = logs
	}
	if len(ccrHandles) > 0 {
		out["ccr_handles"] = ccrHandles
	}
	encoded, merr := json.Marshal(out)
	if merr != nil {
		return mcp.ToolError("cave_marshal_failed", "could not encode QuickJS execution result")
	}
	return g.finalizeExecutionOutput(encoded, maxOut)
}

func classifyJSError(ctx context.Context, timeout time.Duration, err error) mcp.ToolResult {
	msg := err.Error()
	if errors.Is(ctx.Err(), context.DeadlineExceeded) || isInterruptError(msg) {
		return mcp.ToolError("cave_js_timeout", fmt.Sprintf("codemode QuickJS execution timed out after %s", timeout))
	}
	if isOOMError(msg) {
		return mcp.ToolError("cave_js_oom", msg)
	}
	if strings.Contains(msg, "call codemode_mutate instead") {
		return mcp.ToolError("cave_write_not_allowed", msg)
	}
	return mcp.ToolError("cave_js_error", msg)
}

func isInterruptError(msg string) bool {
	lower := strings.ToLower(msg)
	return strings.Contains(lower, "interrupted")
}

func isOOMError(msg string) bool {
	lower := strings.ToLower(msg)
	return strings.Contains(lower, "out of memory") || strings.Contains(lower, "memory limit")
}

func jsToInt(v any) (int, bool) {
	switch n := v.(type) {
	case int:
		return n, true
	case int32:
		return int(n), true
	case int64:
		return int(n), true
	case float64:
		return int(n), true
	default:
		return 0, false
	}
}
