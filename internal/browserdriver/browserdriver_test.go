//go:build browserdriver

package browserdriver

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/sjhorn/slmtest/internal/driver"
)

// These tests drive a real headless Chromium against a real local static
// HTML page served over HTTP — matching this project's real-integration-
// test philosophy (see internal/ptydriver's tests, which drive a real
// shell in a real PTY rather than mocking the terminal). A mocked DOM
// would leave the only interesting behavior — does a click primitive
// actually reach a real page, does the snapshot actually reflect it —
// untested.

func startTestPage(t *testing.T) string {
	t.Helper()
	srv := httptest.NewServer(http.FileServer(http.Dir("testdata")))
	t.Cleanup(srv.Close)
	return srv.URL + "/page.html"
}

func startTestDriver(t *testing.T, url string) *Driver {
	t.Helper()
	d, err := New(context.Background(), driver.Config{Options: map[string]string{
		"headless": "true",
		"url":      url,
	}})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Cleanup(func() { _ = d.Close() })
	return d.(*Driver)
}

func TestImplementsDriverInterface(t *testing.T) {
	var _ driver.Driver = (*Driver)(nil)
}

func TestFactoryRegistered(t *testing.T) {
	if _, ok := driver.Get("browser"); !ok {
		t.Fatal("browser driver not registered")
	}
}

func TestNewNavigatesToStartURL(t *testing.T) {
	d := startTestDriver(t, startTestPage(t))
	obs, err := d.Observe(context.Background(), 0)
	if err != nil {
		t.Fatalf("Observe: %v", err)
	}
	if !strings.Contains(obs.Text, "browserdriver test page") {
		t.Fatalf("snapshot = %q, want it to mention the page title", obs.Text)
	}
	if !strings.Contains(obs.Text, "not clicked") {
		t.Fatalf("snapshot = %q, want the initial status text", obs.Text)
	}
}

func TestSnapshotListsInteractiveElements(t *testing.T) {
	d := startTestDriver(t, startTestPage(t))
	obs, err := d.Observe(context.Background(), 0)
	if err != nil {
		t.Fatalf("Observe: %v", err)
	}
	if !strings.Contains(obs.Text, "#reveal") {
		t.Fatalf("snapshot = %q, want the #reveal button's selector listed", obs.Text)
	}
	// The name field starts hidden (display:none), so a well-formed
	// snapshot must not list it as an interactive element yet — this is
	// what proves the snapshot reflects real page state, not just a
	// static DOM query.
	if strings.Contains(obs.Text, "#name-field") {
		t.Fatalf("snapshot = %q, should not list the hidden name field", obs.Text)
	}
}

func TestDispatchClickActuallyClicksTheRealPage(t *testing.T) {
	d := startTestDriver(t, startTestPage(t))
	params, _ := json.Marshal(driver.ClickParams{Target: "#reveal"})
	obs, err := d.Dispatch(context.Background(), driver.ActionClick, params)
	if err != nil {
		t.Fatalf("Dispatch(click): %v", err)
	}
	if !strings.Contains(obs.Text, "clicked") {
		t.Fatalf("post-click snapshot = %q, want status text to have changed", obs.Text)
	}
	if !strings.Contains(obs.Text, "#name-field") {
		t.Fatalf("post-click snapshot = %q, want the now-visible name field listed", obs.Text)
	}
}

func TestDispatchTypeTextEntersRealInput(t *testing.T) {
	d := startTestDriver(t, startTestPage(t))
	clickParams, _ := json.Marshal(driver.ClickParams{Target: "#reveal"})
	if _, err := d.Dispatch(context.Background(), driver.ActionClick, clickParams); err != nil {
		t.Fatalf("Dispatch(click #reveal): %v", err)
	}
	focusParams, _ := json.Marshal(driver.ClickParams{Target: "#name-field"})
	if _, err := d.Dispatch(context.Background(), driver.ActionClick, focusParams); err != nil {
		t.Fatalf("Dispatch(click #name-field): %v", err)
	}
	typeParams, _ := json.Marshal(driver.TypeTextParams{Text: "Ada"})
	obs, err := d.Dispatch(context.Background(), driver.ActionTypeText, typeParams)
	if err != nil {
		t.Fatalf("Dispatch(type_text): %v", err)
	}
	if !strings.Contains(obs.Text, "you typed: Ada") {
		t.Fatalf("post-type snapshot = %q, want the real DOM echo of what was typed", obs.Text)
	}
}

func startMouseTestDriver(t *testing.T) *Driver {
	t.Helper()
	srv := httptest.NewServer(http.FileServer(http.Dir("testdata")))
	t.Cleanup(srv.Close)
	return startTestDriver(t, srv.URL+"/mouse.html")
}

func TestDispatchPressKeySendsRealKeystroke(t *testing.T) {
	d := startMouseTestDriver(t)
	// Focus the hover target first so the keystroke has somewhere to go;
	// pressing Tab from a blank page is enough to prove press_key reaches
	// the real page without erroring — the more interesting assertion is
	// the modifier-chord case below.
	params, _ := json.Marshal(driver.PressKeyParams{Key: "tab"})
	if _, err := d.Dispatch(context.Background(), driver.ActionPressKey, params); err != nil {
		t.Fatalf("Dispatch(press_key tab): %v", err)
	}
}

func TestDispatchPressKeyEmptyKeyIsBadParams(t *testing.T) {
	d := startMouseTestDriver(t)
	params, _ := json.Marshal(driver.PressKeyParams{Key: ""})
	_, err := d.Dispatch(context.Background(), driver.ActionPressKey, params)
	if err == nil {
		t.Fatal("expected an error for an empty press_key key")
	}
	var bpe *driver.BadParamsError
	if !errors.As(err, &bpe) {
		t.Fatalf("error = %T (%v), want *driver.BadParamsError", err, err)
	}
}

func TestDispatchDoubleClickActuallyDoubleClicksTheRealPage(t *testing.T) {
	d := startMouseTestDriver(t)
	params, _ := json.Marshal(driver.ClickParams{Target: "#dblclick-target"})
	obs, err := d.Dispatch(context.Background(), driver.ActionDoubleClick, params)
	if err != nil {
		t.Fatalf("Dispatch(double_click): %v", err)
	}
	if !strings.Contains(obs.Text, "double-clicked") {
		t.Fatalf("post-double-click snapshot = %q, want status text to have changed", obs.Text)
	}
}

func TestDispatchRightClickActuallyRightClicksTheRealPage(t *testing.T) {
	d := startMouseTestDriver(t)
	params, _ := json.Marshal(driver.ClickParams{Target: "#rightclick-target"})
	obs, err := d.Dispatch(context.Background(), driver.ActionRightClick, params)
	if err != nil {
		t.Fatalf("Dispatch(right_click): %v", err)
	}
	if !strings.Contains(obs.Text, "right-clicked") {
		t.Fatalf("post-right-click snapshot = %q, want status text to have changed", obs.Text)
	}
}

func TestDispatchMouseMoveActuallyHoversTheRealPage(t *testing.T) {
	d := startMouseTestDriver(t)
	params, _ := json.Marshal(driver.ClickParams{Target: "#hover-target"})
	obs, err := d.Dispatch(context.Background(), driver.ActionMouseMove, params)
	if err != nil {
		t.Fatalf("Dispatch(mouse_move): %v", err)
	}
	if !strings.Contains(obs.Text, "hovered") {
		t.Fatalf("post-hover snapshot = %q, want status text to have changed", obs.Text)
	}
}

func TestDispatchMouseMoveRequiresTargetOrCoordinate(t *testing.T) {
	d := startMouseTestDriver(t)
	params, _ := json.Marshal(driver.ClickParams{})
	_, err := d.Dispatch(context.Background(), driver.ActionMouseMove, params)
	if err == nil {
		t.Fatal("expected an error when neither target nor x/y is given")
	}
	var bpe *driver.BadParamsError
	if !errors.As(err, &bpe) {
		t.Fatalf("error = %T (%v), want *driver.BadParamsError", err, err)
	}
}

func TestDispatchScrollActuallyScrollsTheRealPage(t *testing.T) {
	d := startMouseTestDriver(t)
	params, _ := json.Marshal(driver.ScrollParams{Target: "#scroll-target"})
	if _, err := d.Dispatch(context.Background(), driver.ActionScroll, params); err != nil {
		t.Fatalf("Dispatch(scroll): %v", err)
	}
	// The page's "scroll" event fires asynchronously relative to the
	// scrollIntoView call, so give it a moment to settle before checking
	// the DOM reflects it — the same reason Observe/Dispatch elsewhere in
	// this driver settle for a beat before snapshotting.
	obs, err := d.Observe(context.Background(), 200*time.Millisecond)
	if err != nil {
		t.Fatalf("Observe: %v", err)
	}
	if !strings.Contains(obs.Text, "scrolled-into-view") {
		t.Fatalf("post-scroll snapshot = %q, want status text to have changed", obs.Text)
	}
}

func TestDispatchDragActuallyDragsOnTheRealPage(t *testing.T) {
	d := startMouseTestDriver(t)
	params, _ := json.Marshal(driver.DragParams{From: "#drag-source", To: "#drop-zone"})
	obs, err := d.Dispatch(context.Background(), driver.ActionDrag, params)
	if err != nil {
		t.Fatalf("Dispatch(drag): %v", err)
	}
	if !strings.Contains(obs.Text, "dropped") {
		t.Fatalf("post-drag snapshot = %q, want status text to have changed", obs.Text)
	}
}

func TestDispatchDragEmptyTargetsIsBadParams(t *testing.T) {
	d := startMouseTestDriver(t)
	params, _ := json.Marshal(driver.DragParams{From: "", To: "#drop-zone"})
	_, err := d.Dispatch(context.Background(), driver.ActionDrag, params)
	if err == nil {
		t.Fatal("expected an error for an empty drag source")
	}
	var bpe *driver.BadParamsError
	if !errors.As(err, &bpe) {
		t.Fatalf("error = %T (%v), want *driver.BadParamsError", err, err)
	}
}

func TestDispatchDoubleClickEmptyTargetIsBadParams(t *testing.T) {
	d := startMouseTestDriver(t)
	params, _ := json.Marshal(driver.ClickParams{Target: ""})
	_, err := d.Dispatch(context.Background(), driver.ActionDoubleClick, params)
	if err == nil {
		t.Fatal("expected an error for an empty double_click target")
	}
	var bpe *driver.BadParamsError
	if !errors.As(err, &bpe) {
		t.Fatalf("error = %T (%v), want *driver.BadParamsError", err, err)
	}
}

func TestDispatchNavigate(t *testing.T) {
	d := startTestDriver(t, startTestPage(t))
	// Navigate to a second, distinct URL (query string forces a real
	// navigation Chromium won't skip as a no-op).
	params, _ := json.Marshal(NavigateParams{URL: startTestPage(t) + "?x=2"})
	obs, err := d.Dispatch(context.Background(), ActionNavigate, params)
	if err != nil {
		t.Fatalf("Dispatch(navigate): %v", err)
	}
	if !strings.Contains(obs.Text, "browserdriver test page") {
		t.Fatalf("post-navigate snapshot = %q, want the page to have loaded", obs.Text)
	}
}

// TestDispatchNavigateRelativeFileURL confirms a relative URL resolves
// against the current page the way examples/browser-form-test.md relies
// on: a form page and its confirmation page are sibling files, and the
// spec navigates between them with a bare relative filename rather than
// requiring the model to construct an absolute file:// path (which
// would vary per checkout). file:// navigation is worth its own test
// separate from TestDispatchNavigate's http:// case — some browsers
// impose extra restrictions on file:// URLs that http:// doesn't hit.
func TestDispatchNavigateRelativeFileURL(t *testing.T) {
	abs, err := filepath.Abs("testdata/page.html")
	if err != nil {
		t.Fatalf("filepath.Abs: %v", err)
	}
	d := startTestDriver(t, "file://"+abs)

	params, _ := json.Marshal(NavigateParams{URL: "page2.html"})
	obs, err := d.Dispatch(context.Background(), ActionNavigate, params)
	if err != nil {
		t.Fatalf("Dispatch(navigate, relative URL): %v", err)
	}
	if !strings.Contains(obs.Text, "browserdriver second test page") {
		t.Fatalf("post-navigate snapshot = %q, want the relative navigation to have loaded page2.html", obs.Text)
	}
}

func TestDispatchUnsupportedAction(t *testing.T) {
	d := startTestDriver(t, startTestPage(t))
	// swipe is a defined Layer-1 primitive with no touch-capable driver to
	// dispatch it yet (see driver.ActionSwipe's doc comment) — a stable
	// choice for "an action this driver doesn't support".
	if _, err := d.Dispatch(context.Background(), "swipe", json.RawMessage(`{}`)); err == nil {
		t.Fatal("expected an error for an action this driver doesn't support")
	}
}

// TestDispatchNavigateEmptyURLIsBadParams is the regression test for a
// bug found live against a real model: a missing/empty "url" used to
// resolve silently to the current page (a no-op with no error), giving
// the model no signal its params were dropped. It must now be a loud,
// recoverable driver.BadParamsError instead.
func TestDispatchNavigateEmptyURLIsBadParams(t *testing.T) {
	d := startTestDriver(t, startTestPage(t))
	params, _ := json.Marshal(NavigateParams{URL: ""})
	_, err := d.Dispatch(context.Background(), ActionNavigate, params)
	if err == nil {
		t.Fatal("expected an error for an empty navigate URL")
	}
	var bpe *driver.BadParamsError
	if !errors.As(err, &bpe) {
		t.Fatalf("error = %T (%v), want *driver.BadParamsError", err, err)
	}
}

func TestDispatchClickEmptyTargetIsBadParams(t *testing.T) {
	d := startTestDriver(t, startTestPage(t))
	params, _ := json.Marshal(driver.ClickParams{Target: ""})
	_, err := d.Dispatch(context.Background(), driver.ActionClick, params)
	if err == nil {
		t.Fatal("expected an error for an empty click target")
	}
	var bpe *driver.BadParamsError
	if !errors.As(err, &bpe) {
		t.Fatalf("error = %T (%v), want *driver.BadParamsError", err, err)
	}
}

func TestAliveAndClose(t *testing.T) {
	d := startTestDriver(t, startTestPage(t))
	if !d.Alive() {
		t.Fatal("freshly-started driver should be alive")
	}
	if err := d.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if d.Alive() {
		t.Fatal("closed driver should not report alive")
	}
}

func TestActionsAndPromptFragmentNonEmpty(t *testing.T) {
	d := startTestDriver(t, startTestPage(t))
	if d.Name() != "browser" {
		t.Fatalf("Name() = %q, want browser", d.Name())
	}
	if len(d.Actions()) < 3 {
		t.Fatalf("expected at least 3 actions, got %d", len(d.Actions()))
	}
	if d.PromptFragment() == "" {
		t.Fatal("PromptFragment should not be empty")
	}
}

func TestObserveRespectsContextCancellation(t *testing.T) {
	d := startTestDriver(t, startTestPage(t))
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := d.Observe(ctx, 5*time.Second); err == nil {
		t.Fatal("expected Observe to return promptly on a cancelled context")
	}
}
