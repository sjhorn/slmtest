//go:build browserdriver

// Package browserdriver is the "browser" driver.Driver: a real Chromium
// instance driven via Playwright-Go. It exists to prove the driver
// abstraction against a genuinely different UI paradigm from the
// terminal — snapshot-based observation (a fresh accessibility-tree-
// style listing every call, not a byte diff) and pointer clicks reusing
// the shared PrimitiveClick/PrimitiveTypeText primitives, plus a bespoke
// navigate action for loading a URL.
//
// This package is gated behind the "browserdriver" build tag so the
// default `slmtest` build has no Playwright/browser-binary dependency —
// only `go build -tags browserdriver` (or a binary that blank-imports
// this package under that tag, see cmd/slmtest) pulls it in. A test spec
// selecting `driver: browser` against a default build gets a clear
// "unknown driver" error, not a missing-binary crash.
package browserdriver

import (
	"context"
	"encoding/json"
	"fmt"
	"net/url"
	"strconv"
	"strings"
	"time"

	"github.com/mxschmitt/playwright-go"

	"github.com/sjhorn/slmtest/internal/driver"
)

func init() {
	driver.Register("browser", New)
}

// ActionNavigate is this driver's one bespoke action: no shared
// primitive fits "load a different URL, replacing the current page."
const ActionNavigate driver.ActionType = "navigate"

// NavigateParams is ActionNavigate's Dispatch param shape.
type NavigateParams struct {
	URL string `json:"url"`
}

// Driver drives one Chromium page for the lifetime of a test run.
type Driver struct {
	pw      *playwright.Playwright
	browser playwright.Browser
	page    playwright.Page
}

// New is this driver's driver.Factory, registered under "browser".
// Recognized cfg.Options (all optional): "headless" ("false" to show a
// real window, anything else/absent means headless), "url" (navigated
// to once at startup, before the first step).
func New(ctx context.Context, cfg driver.Config) (driver.Driver, error) {
	pw, err := playwright.Run()
	if err != nil {
		return nil, fmt.Errorf("starting playwright: %w", err)
	}
	headless := true
	if v, ok := cfg.Options["headless"]; ok {
		if b, perr := strconv.ParseBool(v); perr == nil {
			headless = b
		}
	}
	browser, err := pw.Chromium.Launch(playwright.BrowserTypeLaunchOptions{Headless: playwright.Bool(headless)})
	if err != nil {
		_ = pw.Stop()
		return nil, fmt.Errorf("launching chromium: %w", err)
	}
	page, err := browser.NewPage()
	if err != nil {
		_ = browser.Close()
		_ = pw.Stop()
		return nil, fmt.Errorf("opening page: %w", err)
	}
	d := &Driver{pw: pw, browser: browser, page: page}
	if url := cfg.Options["url"]; url != "" {
		if _, err := page.Goto(url); err != nil {
			_ = d.Close()
			return nil, fmt.Errorf("navigating to %s: %w", url, err)
		}
	}
	return d, nil
}

var _ driver.Driver = (*Driver)(nil)

func (d *Driver) Name() string { return "browser" }

// Actions offers the shared click/type_text/press_key/mouse primitives
// plus this driver's own bespoke navigate.
func (d *Driver) Actions() []driver.ActionSpec {
	return []driver.ActionSpec{
		driver.PrimitiveClick,
		driver.PrimitiveTypeText,
		driver.PrimitivePressKey,
		driver.PrimitiveDoubleClick,
		driver.PrimitiveRightClick,
		driver.PrimitiveMouseMove,
		driver.PrimitiveScroll,
		driver.PrimitiveDrag,
		{
			Type:        ActionNavigate,
			Description: "Navigate the page to a URL, replacing whatever is currently loaded. \"url\" may be absolute or relative to the current page (relative resolves the same way a link's href would).",
			ParamSchema: json.RawMessage(`{
				"type": "object",
				"properties": {"url": {"type": "string"}},
				"required": ["url"]
			}`),
		},
	}
}

func (d *Driver) PromptFragment() string {
	return `- click: "target" is one of the CSS selectors shown in the "Interactive elements" list below each observation (e.g. "#submit", "button:nth-of-type(2)") — use the selector shown, not the visible label. "x"/"y" coordinates work as an alternative to "target".
- type_text: types into whatever element currently has keyboard focus. Click a text field first if nothing is focused yet.
- press_key: sends a keyboard key/chord to the page (e.g. "enter", "tab", or "a" with modifiers ["ctrl"]) rather than clicking.
- double_click, right_click, mouse_move: same "target" (or "x"/"y") contract as click — a double-click, a right-click, and a hover with no click, respectively.
- scroll: scrolls "target" into view, or the viewport by "delta_x"/"delta_y" when target is omitted.
- drag: presses on "from", drags to "to", and releases — both selectors from the "Interactive elements" list.
- navigate: loads a different URL, replacing the current page entirely. "url" may be relative to the current page (e.g. "other-page.html") — it resolves the same way a link's href would.
After every action you'll be shown a fresh snapshot of the page: its title/URL, a list of visible interactive elements with the selector to click each one, and the page's visible text — not raw HTML, and not a diff of what changed.`
}

func (d *Driver) Dispatch(ctx context.Context, action driver.ActionType, params json.RawMessage) (driver.Observation, error) {
	switch action {
	case driver.ActionClick:
		var p driver.ClickParams
		if err := json.Unmarshal(params, &p); err != nil {
			return driver.Observation{}, fmt.Errorf("click: bad params: %w", err)
		}
		if p.Target == "" {
			return driver.Observation{}, driver.NewBadParamsError(d.Name(), action, `"target" is required and must be non-empty`)
		}
		if err := d.page.Locator(p.Target).Click(); err != nil {
			return driver.Observation{}, fmt.Errorf("click %q: %w", p.Target, err)
		}

	case driver.ActionTypeText:
		var p driver.TypeTextParams
		if err := json.Unmarshal(params, &p); err != nil {
			return driver.Observation{}, fmt.Errorf("type_text: bad params: %w", err)
		}
		if err := d.page.Keyboard().Type(p.Text); err != nil {
			return driver.Observation{}, fmt.Errorf("type_text: %w", err)
		}

	case driver.ActionPressKey:
		var p driver.PressKeyParams
		if err := json.Unmarshal(params, &p); err != nil {
			return driver.Observation{}, fmt.Errorf("press_key: bad params: %w", err)
		}
		if p.Key == "" {
			return driver.Observation{}, driver.NewBadParamsError(d.Name(), action, `"key" is required and must be non-empty`)
		}
		if err := d.page.Keyboard().Press(playwrightKeyChord(p.Key, p.Modifiers)); err != nil {
			return driver.Observation{}, fmt.Errorf("press_key %q: %w", p.Key, err)
		}

	case driver.ActionDoubleClick:
		p, err := requireClickTarget(action, d.Name(), params)
		if err != nil {
			return driver.Observation{}, err
		}
		if err := d.locatorFor(p).Dblclick(); err != nil {
			return driver.Observation{}, fmt.Errorf("double_click %q: %w", p.Target, err)
		}

	case driver.ActionRightClick:
		p, err := requireClickTarget(action, d.Name(), params)
		if err != nil {
			return driver.Observation{}, err
		}
		if err := d.locatorFor(p).Click(playwright.LocatorClickOptions{Button: playwright.MouseButtonRight}); err != nil {
			return driver.Observation{}, fmt.Errorf("right_click %q: %w", p.Target, err)
		}

	case driver.ActionMouseMove:
		var p driver.ClickParams
		if err := json.Unmarshal(params, &p); err != nil {
			return driver.Observation{}, fmt.Errorf("mouse_move: bad params: %w", err)
		}
		if p.Target != "" {
			if err := d.page.Locator(p.Target).Hover(); err != nil {
				return driver.Observation{}, fmt.Errorf("mouse_move %q: %w", p.Target, err)
			}
		} else if p.X != 0 || p.Y != 0 {
			if err := d.page.Mouse().Move(float64(p.X), float64(p.Y)); err != nil {
				return driver.Observation{}, fmt.Errorf("mouse_move (%d,%d): %w", p.X, p.Y, err)
			}
		} else {
			return driver.Observation{}, driver.NewBadParamsError(d.Name(), action, `either "target" or non-zero "x"/"y" is required`)
		}

	case driver.ActionScroll:
		var p driver.ScrollParams
		if err := json.Unmarshal(params, &p); err != nil {
			return driver.Observation{}, fmt.Errorf("scroll: bad params: %w", err)
		}
		if p.Target != "" {
			if err := d.page.Locator(p.Target).ScrollIntoViewIfNeeded(); err != nil {
				return driver.Observation{}, fmt.Errorf("scroll %q: %w", p.Target, err)
			}
		} else {
			if err := d.page.Mouse().Wheel(float64(p.DeltaX), float64(p.DeltaY)); err != nil {
				return driver.Observation{}, fmt.Errorf("scroll: %w", err)
			}
		}

	case driver.ActionDrag:
		var p driver.DragParams
		if err := json.Unmarshal(params, &p); err != nil {
			return driver.Observation{}, fmt.Errorf("drag: bad params: %w", err)
		}
		if p.From == "" || p.To == "" {
			return driver.Observation{}, driver.NewBadParamsError(d.Name(), action, `"from" and "to" are both required and must be non-empty`)
		}
		if err := d.page.Locator(p.From).DragTo(d.page.Locator(p.To)); err != nil {
			return driver.Observation{}, fmt.Errorf("drag %q -> %q: %w", p.From, p.To, err)
		}

	case ActionNavigate:
		var p NavigateParams
		if err := json.Unmarshal(params, &p); err != nil {
			return driver.Observation{}, fmt.Errorf("navigate: bad params: %w", err)
		}
		if p.URL == "" {
			// Without this check, an empty URL silently resolves to the
			// current page (see resolveURL) — a no-op that produces no
			// error, leaving the model with no signal that its params
			// were dropped. Found live: a model sent a flat top-level
			// "url" field instead of nesting it under "params" as the
			// system prompt specifies; agent.Action has no such field,
			// so it was silently ignored. See driver.BadParamsError's
			// doc comment for the full story.
			return driver.Observation{}, driver.NewBadParamsError(d.Name(), action, `"url" is required and must be non-empty`)
		}
		target, err := d.resolveURL(p.URL)
		if err != nil {
			return driver.Observation{}, fmt.Errorf("navigate to %q: %w", p.URL, err)
		}
		if _, err := d.page.Goto(target); err != nil {
			return driver.Observation{}, fmt.Errorf("navigate to %q: %w", p.URL, err)
		}

	default:
		return driver.Observation{}, driver.NewUnsupportedActionError(d.Name(), action)
	}
	return d.snapshot()
}

// Observe is the core "wait" action: settle for the given duration, then
// report a fresh snapshot — no separate "diff since last call" concept,
// unlike ptydriver's Observe. This is the deliberate proof point that
// Observation is genuinely opaque to the runner: a full-replace snapshot
// and a byte-diff are both legitimate under the same interface.
func (d *Driver) Observe(ctx context.Context, wait time.Duration) (driver.Observation, error) {
	if wait > 0 {
		select {
		case <-time.After(wait):
		case <-ctx.Done():
			return driver.Observation{}, ctx.Err()
		}
	}
	return d.snapshot()
}

// requireClickTarget unmarshals ClickParams and rejects an empty Target —
// double_click/right_click only support selector targets today (unlike
// mouse_move/scroll, which fall back to a coordinate or a wheel delta),
// so an empty Target here is always a mistake worth surfacing loudly
// rather than a silent no-op. See driver.BadParamsError's doc comment for
// why this class of mistake must be loud.
func requireClickTarget(action driver.ActionType, driverName string, params json.RawMessage) (driver.ClickParams, error) {
	var p driver.ClickParams
	if err := json.Unmarshal(params, &p); err != nil {
		return p, fmt.Errorf("%s: bad params: %w", action, err)
	}
	if p.Target == "" {
		return p, driver.NewBadParamsError(driverName, action, `"target" is required and must be non-empty`)
	}
	return p, nil
}

func (d *Driver) locatorFor(p driver.ClickParams) playwright.Locator {
	return d.page.Locator(p.Target)
}

// playwrightKeyChord translates a logical key name plus modifiers into
// Playwright's own "Control+C"-style chord syntax for Keyboard().Press.
// Playwright expects the base key capitalized for named keys (e.g.
// "Enter", "ArrowUp") but accepts a bare lowercase character as-is.
func playwrightKeyChord(key string, modifiers []string) string {
	named := map[string]string{
		"enter": "Enter", "return": "Enter",
		"escape": "Escape", "esc": "Escape",
		"tab": "Tab", "backspace": "Backspace", "delete": "Delete",
		"insert": "Insert", "home": "Home", "end": "End",
		"pageup": "PageUp", "pagedown": "PageDown", "space": "Space",
		"up": "ArrowUp", "down": "ArrowDown", "left": "ArrowLeft", "right": "ArrowRight",
		"back": "Escape", "select": "Enter",
	}
	base, ok := named[strings.ToLower(strings.TrimSpace(key))]
	if !ok {
		base = key
	}
	modNames := map[string]string{"ctrl": "Control", "control": "Control", "alt": "Alt", "option": "Alt", "shift": "Shift", "meta": "Meta", "cmd": "Meta", "command": "Meta"}
	var chord string
	for _, m := range modifiers {
		if name, ok := modNames[strings.ToLower(strings.TrimSpace(m))]; ok {
			chord += name + "+"
		}
	}
	return chord + base
}

func (d *Driver) Alive() bool {
	return d.page != nil && !d.page.IsClosed()
}

func (d *Driver) Close() error {
	var firstErr error
	if d.page != nil {
		if err := d.page.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	if d.browser != nil {
		if err := d.browser.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	if d.pw != nil {
		if err := d.pw.Stop(); err != nil && firstErr == nil {
			firstErr = err
		}
	}
	return firstErr
}

// snapshotScript runs in the page and returns a compact, LLM-readable
// description of the page: title/URL, every visible interactive
// element with a selector that Dispatch's click action can use
// directly (so a model never has to invent its own selector), and the
// page's visible text. This is the "accessibility-tree-as-text"
// snapshot the driver-abstraction plan calls for — approximated via a
// direct DOM query rather than the browser's native accessibility tree,
// which keeps this driver's one non-Go dependency (this script) simple
// and auditable.
const snapshotScript = `() => {
  function visible(el) {
    const r = el.getBoundingClientRect();
    const s = getComputedStyle(el);
    return r.width > 0 && r.height > 0 && s.visibility !== 'hidden' && s.display !== 'none';
  }
  function label(el) {
    const l = el.getAttribute('aria-label') || el.innerText || el.value || el.placeholder || '';
    return l.trim().replace(/\s+/g, ' ').slice(0, 80);
  }
  function selectorFor(el) {
    if (el.id) return '#' + el.id;
    if (el.name) return el.tagName.toLowerCase() + '[name="' + el.name + '"]';
    const tag = el.tagName.toLowerCase();
    const parent = el.parentElement;
    if (!parent) return tag;
    const siblings = Array.from(parent.children).filter(c => c.tagName === el.tagName);
    if (siblings.length <= 1) return tag;
    return tag + ':nth-of-type(' + (siblings.indexOf(el) + 1) + ')';
  }
  const els = Array.from(document.querySelectorAll(
    'a, button, input, textarea, select, [role="button"], [role="link"]'
  ));
  const elements = els.filter(visible).slice(0, 200).map(el => {
    return '[' + el.tagName.toLowerCase() + '] "' + label(el) + '" -> ' + selectorFor(el);
  });
  return {
    title: document.title,
    url: location.href,
    elements: elements,
    text: document.body ? document.body.innerText.slice(0, 4000) : ''
  };
}`

// resolveURL resolves target against the current page's URL, the way a
// browser resolves a relative <a href>. Playwright's own Page.Goto does
// NOT do this — it treats a non-absolute string as a literal (and
// invalid) URL — so without this, "navigate" could only ever be used
// with a full absolute URL, which is awkward for a spec navigating
// between sibling local files (see examples/browser-form-test.md, whose
// confirmation-page step uses a bare relative filename rather than an
// absolute path baked into the spec, which would vary per checkout). An
// already-absolute target round-trips through this unchanged.
func (d *Driver) resolveURL(target string) (string, error) {
	base, err := url.Parse(d.page.URL())
	if err != nil {
		return "", fmt.Errorf("parsing current page URL: %w", err)
	}
	ref, err := url.Parse(target)
	if err != nil {
		return "", fmt.Errorf("parsing target URL: %w", err)
	}
	return base.ResolveReference(ref).String(), nil
}

func (d *Driver) snapshot() (driver.Observation, error) {
	raw, err := d.page.Evaluate(snapshotScript)
	if err != nil {
		return driver.Observation{}, fmt.Errorf("snapshotting page: %w", err)
	}
	m, ok := raw.(map[string]interface{})
	if !ok {
		return driver.Observation{}, fmt.Errorf("snapshotting page: unexpected result shape %T", raw)
	}

	title, _ := m["title"].(string)
	url, _ := m["url"].(string)
	text, _ := m["text"].(string)
	var elements []string
	if raw, ok := m["elements"].([]interface{}); ok {
		for _, e := range raw {
			if s, ok := e.(string); ok {
				elements = append(elements, s)
			}
		}
	}

	out := fmt.Sprintf("Page: %s (%s)\n\nInteractive elements:\n", title, url)
	if len(elements) == 0 {
		out += "(none)\n"
	} else {
		for _, e := range elements {
			out += e + "\n"
		}
	}
	out += "\nVisible text:\n" + text

	return driver.Observation{Text: out}, nil
}
