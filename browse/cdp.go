package browse

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"math"
	"os"
	"path/filepath"
	"time"

	"github.com/chromedp/cdproto/accessibility"
	cdpbrowser "github.com/chromedp/cdproto/browser"
	"github.com/chromedp/cdproto/cdp"
	"github.com/chromedp/cdproto/dom"
	"github.com/chromedp/cdproto/input"
	"github.com/chromedp/cdproto/runtime"
	cdptarget "github.com/chromedp/cdproto/target"
	"github.com/chromedp/chromedp"
)

type CDPOptions struct {
	Endpoint    string
	UserDataDir string
	BrowserPath string
	Headless    bool
	TargetID    string
}

type CDPDriver struct {
	allocCtx    context.Context
	allocCancel context.CancelFunc
	ctx         context.Context
	cancel      context.CancelFunc
	tmpProfile  string
}

func NewCDPDriver(parent context.Context, opts CDPOptions) (*CDPDriver, error) {
	if parent == nil {
		parent = context.Background()
	}
	var allocCtx context.Context
	var allocCancel context.CancelFunc
	var tmpProfile string
	if opts.Endpoint != "" {
		allocCtx, allocCancel = chromedp.NewRemoteAllocator(parent, opts.Endpoint)
	} else {
		userDataDir := opts.UserDataDir
		if userDataDir == "" {
			dir, err := os.MkdirTemp("", "caveman-browse-*")
			if err != nil {
				return nil, err
			}
			tmpProfile = dir
			userDataDir = dir
		}
		execOpts := append([]chromedp.ExecAllocatorOption{}, chromedp.DefaultExecAllocatorOptions[:]...)
		// Keep Chrome's default Site Isolation (site-per-process) intact. It was
		// previously disabled to paper over cross-origin iframes whose child
		// document is absent from a single frame's getFullAXTree response, but
		// the axtree compressor now treats that boundary as a leaf (issue #140),
		// so we no longer trade away a security boundary for it.
		execOpts = append(execOpts,
			chromedp.UserDataDir(userDataDir),
		)
		if opts.BrowserPath != "" {
			execOpts = append(execOpts, chromedp.ExecPath(opts.BrowserPath))
		}
		if !opts.Headless {
			execOpts = append(execOpts, chromedp.Flag("headless", false))
		}
		allocCtx, allocCancel = chromedp.NewExecAllocator(parent, execOpts...)
	}
	var ctx context.Context
	var cancel context.CancelFunc
	if opts.TargetID != "" {
		ctx, cancel = chromedp.NewContext(allocCtx, chromedp.WithTargetID(cdptarget.ID(opts.TargetID)))
	} else {
		ctx, cancel = chromedp.NewContext(allocCtx)
	}
	if err := chromedp.Run(ctx); err != nil {
		cancel()
		allocCancel()
		if tmpProfile != "" {
			_ = os.RemoveAll(tmpProfile)
		}
		return nil, fmt.Errorf("start browser: %w", err)
	}
	return &CDPDriver{allocCtx: allocCtx, allocCancel: allocCancel, ctx: ctx, cancel: cancel, tmpProfile: tmpProfile}, nil
}

func (d *CDPDriver) Close() error {
	if d.cancel != nil {
		d.cancel()
	}
	if d.allocCancel != nil {
		d.allocCancel()
	}
	if d.tmpProfile != "" {
		return os.RemoveAll(d.tmpProfile)
	}
	return nil
}

func (d *CDPDriver) TargetID() string {
	c := chromedp.FromContext(d.ctx)
	if c == nil || c.Target == nil {
		return ""
	}
	return string(c.Target.TargetID)
}

func (d *CDPDriver) requestContext(parent context.Context) (context.Context, context.CancelFunc) {
	if parent == nil {
		parent = context.Background()
	}
	var runCtx context.Context
	var cancel context.CancelFunc
	if deadline, ok := parent.Deadline(); ok {
		runCtx, cancel = context.WithDeadline(d.ctx, deadline)
	} else {
		runCtx, cancel = context.WithCancel(d.ctx)
	}
	go func() {
		select {
		case <-parent.Done():
			cancel()
		case <-runCtx.Done():
		}
	}()
	return runCtx, cancel
}

func (d *CDPDriver) Snapshot(ctx context.Context, url string, wait time.Duration) ([]byte, error) {
	if wait < 0 {
		wait = 0
	}
	runCtx, cancel := d.requestContext(ctx)
	defer cancel()
	var nodes []*accessibility.Node
	actions := []chromedp.Action{dom.Enable()}
	if url != "" {
		actions = append(actions, chromedp.Navigate(url))
	}
	if wait > 0 {
		actions = append(actions, chromedp.Sleep(wait))
	}
	actions = append(actions, chromedp.ActionFunc(func(actionCtx context.Context) error {
		var err error
		nodes, err = accessibility.GetFullAXTree().Do(actionCtx)
		return err
	}))
	if err := chromedp.Run(runCtx, actions...); err != nil {
		return nil, err
	}
	return json.Marshal(nodes)
}

func (d *CDPDriver) Act(ctx context.Context, req ActionRequest, target Target) (ActionResult, error) {
	runCtx, cancel := d.requestContext(ctx)
	defer cancel()
	switch req.Action {
	case "click":
		if err := d.scrollIntoView(runCtx, target); err != nil {
			return ActionResult{OK: false, Settled: false}, err
		}
		x, y, err := d.waitActionable(runCtx, target)
		if err != nil {
			return ActionResult{OK: false, Settled: false}, err
		}
		if err := d.click(runCtx, x, y); err != nil {
			return ActionResult{OK: false, Settled: false}, err
		}
		return dispatchedAction(), nil
	case "type":
		if err := d.scrollIntoView(runCtx, target); err != nil {
			return ActionResult{OK: false, Settled: false}, err
		}
		x, y, err := d.waitActionable(runCtx, target)
		if err != nil {
			return ActionResult{OK: false, Settled: false}, err
		}
		if err := d.click(runCtx, x, y); err != nil {
			return ActionResult{OK: false, Settled: false}, err
		}
		if err := chromedp.Run(runCtx, chromedp.ActionFunc(func(actionCtx context.Context) error {
			return input.InsertText(req.Text).Do(actionCtx)
		})); err != nil {
			return ActionResult{OK: false, Settled: false}, err
		}
		return dispatchedAction(), nil
	case "select":
		if err := d.scrollIntoView(runCtx, target); err != nil {
			return ActionResult{OK: false, Settled: false}, err
		}
		if _, _, err := d.waitActionable(runCtx, target); err != nil {
			return ActionResult{OK: false, Settled: false}, err
		}
		option := req.Option
		if option == "" {
			option = req.Text
		}
		if option == "" {
			return ActionResult{OK: false, Settled: false}, errors.New("select: missing option")
		}
		if err := d.callOnNode(runCtx, target, fmt.Sprintf(`function(){ const wanted = %s; const options = Array.from(this.options || []); const match = options.find(o => o.value === wanted || o.label === wanted || o.textContent.trim() === wanted); if (!match) { throw new Error("option not found"); } this.value = match.value; this.dispatchEvent(new Event("input", {bubbles:true})); this.dispatchEvent(new Event("change", {bubbles:true})); return true; }`, jsString(option))); err != nil {
			return ActionResult{OK: false, Settled: false}, err
		}
		return dispatchedAction(), nil
	case "scroll":
		if err := d.scrollIntoView(runCtx, target); err != nil {
			return ActionResult{OK: false, Settled: false}, err
		}
		if _, _, err := d.waitActionable(runCtx, target); err != nil {
			return ActionResult{OK: false, Settled: false}, err
		}
		return dispatchedAction(), nil
	default:
		return ActionResult{OK: false, Settled: false}, fmt.Errorf("unknown action: %s", req.Action)
	}
}

func dispatchedAction() ActionResult {
	// CDP acknowledged dispatch, but asynchronous application state may still be
	// changing. Claiming settled=true here made agents trust an unverified result.
	// A focused browser_snapshot is the cheap, evidence-bearing verification step.
	return ActionResult{OK: true, Settled: false, Note: "dispatched; resnapshot to verify"}
}

func (d *CDPDriver) scrollIntoView(ctx context.Context, target Target) error {
	return d.callOnNode(ctx, target, `function(){ this.scrollIntoView({block:"center", inline:"center", behavior:"instant"}); return true; }`)
}

// Shutdown closes the attached browser process. Ordinary Close only releases
// this driver context; direct CLI mode deliberately leaves its detached Chrome
// alive between snapshot and act commands until `browse close` calls Shutdown.
func (d *CDPDriver) Shutdown(ctx context.Context) error {
	runCtx, cancel := d.requestContext(ctx)
	defer cancel()
	return chromedp.Run(runCtx, chromedp.ActionFunc(func(actionCtx context.Context) error {
		return cdpbrowser.Close().Do(actionCtx)
	}))
}

func (d *CDPDriver) Eval(ctx context.Context, expression string) (any, error) {
	runCtx, cancel := d.requestContext(ctx)
	defer cancel()
	var result any
	if err := chromedp.Run(runCtx, chromedp.Evaluate(expression, &result)); err != nil {
		return nil, err
	}
	return result, nil
}

func (d *CDPDriver) waitActionable(ctx context.Context, target Target) (float64, float64, error) {
	deadline := time.Now().Add(5 * time.Second)
	var last boxPoint
	for {
		x, y, w, h, err := d.box(ctx, target)
		if err == nil && w > 0 && h > 0 {
			select {
			case <-ctx.Done():
				return 0, 0, ctx.Err()
			case <-time.After(32 * time.Millisecond):
			}
			x2, y2, w2, h2, err2 := d.box(ctx, target)
			stable := err2 == nil && math.Abs(x-x2) < 0.5 && math.Abs(y-y2) < 0.5 && math.Abs(w-w2) < 0.5 && math.Abs(h-h2) < 0.5
			if stable {
				visible, err := d.visible(ctx, target)
				if err == nil && visible {
					enabled, err := d.enabled(ctx, target)
					if err == nil && enabled {
						ok, err := d.receivesEvents(ctx, target, x2, y2)
						if err == nil && ok {
							return x2, y2, nil
						}
					}
				}
			}
			last = boxPoint{x: x2, y: y2, w: w2, h: h2}
		}
		if time.Now().After(deadline) {
			return 0, 0, fmt.Errorf("element not actionable: last box=%+v", last)
		}
		select {
		case <-ctx.Done():
			return 0, 0, ctx.Err()
		case <-time.After(100 * time.Millisecond):
		}
	}
}

type boxPoint struct {
	x float64
	y float64
	w float64
	h float64
}

func (d *CDPDriver) box(ctx context.Context, target Target) (float64, float64, float64, float64, error) {
	if target.BackendDOMNodeID <= 0 {
		return 0, 0, 0, 0, errors.New("missing backend DOM node id")
	}
	var model *dom.BoxModel
	if err := chromedp.Run(ctx, chromedp.ActionFunc(func(actionCtx context.Context) error {
		var err error
		model, err = dom.GetBoxModel().WithBackendNodeID(cdp.BackendNodeID(target.BackendDOMNodeID)).Do(actionCtx)
		return err
	})); err != nil {
		return 0, 0, 0, 0, err
	}
	quad := model.Content
	if len(quad) < 8 {
		quad = model.Border
	}
	if len(quad) < 8 {
		return 0, 0, 0, 0, errors.New("box model has no quad")
	}
	minX, maxX := quad[0], quad[0]
	minY, maxY := quad[1], quad[1]
	for i := 0; i+1 < len(quad); i += 2 {
		minX = math.Min(minX, quad[i])
		maxX = math.Max(maxX, quad[i])
		minY = math.Min(minY, quad[i+1])
		maxY = math.Max(maxY, quad[i+1])
	}
	return (minX + maxX) / 2, (minY + maxY) / 2, maxX - minX, maxY - minY, nil
}

func (d *CDPDriver) visible(ctx context.Context, target Target) (bool, error) {
	return d.callBoolOnNode(ctx, target, `function(){ const s = window.getComputedStyle(this); const r = this.getBoundingClientRect(); return !!s && s.display !== "none" && s.visibility !== "hidden" && s.pointerEvents !== "none" && Number(s.opacity || "1") > 0 && r.width > 0 && r.height > 0; }`)
}

func (d *CDPDriver) enabled(ctx context.Context, target Target) (bool, error) {
	return d.callBoolOnNode(ctx, target, `function(){ return !(this.disabled === true || this.getAttribute("disabled") !== null || this.getAttribute("aria-disabled") === "true" || this.closest("[inert]")); }`)
}

func (d *CDPDriver) receivesEvents(ctx context.Context, target Target, x, y float64) (bool, error) {
	var backend cdp.BackendNodeID
	err := chromedp.Run(ctx, chromedp.ActionFunc(func(actionCtx context.Context) error {
		var err error
		backend, _, _, err = dom.GetNodeForLocation(int64(math.Round(x)), int64(math.Round(y))).WithIgnorePointerEventsNone(false).Do(actionCtx)
		return err
	}))
	if err == nil && int(backend) == target.BackendDOMNodeID {
		return true, nil
	}
	fn := fmt.Sprintf(`function(){ const el = document.elementFromPoint(%f, %f); return !!el && (el === this || this.contains(el)); }`, x, y)
	return d.callBoolOnNode(ctx, target, fn)
}

func (d *CDPDriver) click(ctx context.Context, x, y float64) error {
	return chromedp.Run(ctx,
		chromedp.ActionFunc(func(actionCtx context.Context) error {
			if err := input.DispatchMouseEvent(input.MouseMoved, x, y).Do(actionCtx); err != nil {
				return err
			}
			if err := input.DispatchMouseEvent(input.MousePressed, x, y).WithButton(input.Left).WithClickCount(1).Do(actionCtx); err != nil {
				return err
			}
			return input.DispatchMouseEvent(input.MouseReleased, x, y).WithButton(input.Left).WithClickCount(1).Do(actionCtx)
		}),
	)
}

func (d *CDPDriver) callOnNode(ctx context.Context, target Target, fn string) error {
	_, err := d.callOnNodeRaw(ctx, target, fn)
	return err
}

func (d *CDPDriver) callBoolOnNode(ctx context.Context, target Target, fn string) (bool, error) {
	res, err := d.callOnNodeRaw(ctx, target, fn)
	if err != nil {
		return false, err
	}
	return decodeBoolObject(res)
}

// decodeBoolObject reads a boolean out of a CallFunctionOn result, failing
// closed on a nil object. CallFunctionOn can return a nil RemoteObject without
// an error; dereferencing res.Value there would nil-panic and, through the MCP
// panic hole, kill the whole process (issue #140).
func decodeBoolObject(res *runtime.RemoteObject) (bool, error) {
	if res == nil {
		return false, errors.New("browser returned no result object")
	}
	var out bool
	if err := json.Unmarshal(res.Value, &out); err != nil {
		return false, err
	}
	return out, nil
}

func (d *CDPDriver) callOnNodeRaw(ctx context.Context, target Target, fn string) (*runtime.RemoteObject, error) {
	if target.BackendDOMNodeID <= 0 {
		return nil, errors.New("missing backend DOM node id")
	}
	var res *runtime.RemoteObject
	var exception *runtime.ExceptionDetails
	if err := chromedp.Run(ctx, chromedp.ActionFunc(func(actionCtx context.Context) error {
		obj, err := dom.ResolveNode().WithBackendNodeID(cdp.BackendNodeID(target.BackendDOMNodeID)).Do(actionCtx)
		if err != nil {
			return err
		}
		if obj == nil {
			return errors.New("resolve node returned no object")
		}
		res, exception, err = runtime.CallFunctionOn(fn).WithObjectID(obj.ObjectID).Do(actionCtx)
		return err
	})); err != nil {
		return nil, err
	}
	if exception != nil {
		return nil, errors.New("browser function threw")
	}
	if res == nil {
		return nil, errors.New("browser function returned no result object")
	}
	return res, nil
}

func jsString(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}

func DefaultUserDataDir(home string) string {
	if home == "" {
		if h, err := os.UserHomeDir(); err == nil {
			home = filepath.Join(h, ".caveman")
		}
	}
	if home == "" {
		return ""
	}
	return filepath.Join(home, "browse-profile")
}
