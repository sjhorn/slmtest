package driver

import "encoding/json"

// Layer 1 — shared interaction primitives.
//
// The action vocabulary is neither "three core verbs, everything else
// bespoke" nor "every driver defines its own full vocabulary from
// scratch." Those extremes both lose something real: the first forces
// every UI paradigm through the terminal's own shape (send_keys makes
// no sense for a mouse); the second means a mouse click, a touchscreen
// tap, a TV remote's D-pad, and a watch crown each reinvent near-
// identical actions under different names, so the SLM (and anyone
// reading a prompt) has to re-learn each driver's private vocabulary.
//
// This file is the middle layer: a small, deliberately non-exhaustive
// set of well-known actions — a stable ActionType, a canonical param
// shape, and one canonical prompt description — that any driver whose
// device class genuinely has that kind of input can adopt verbatim by
// referencing the exported ActionSpec value directly (same string, same
// schema, same wording). A driver with no directional concept (a
// browser) simply doesn't list PrimitiveDirectional in its Actions().
//
// Where no shared primitive fits — raw control-byte injection, a URL
// navigation, a future watch driver's continuous crown rotation — a
// driver defines its own bespoke ActionSpec instead. Both layers are
// ordinary ActionSpec values; nothing here is special-cased by the
// runner beyond "whatever Actions() returns gets offered to the model."
const (
	// ActionPressKey is a named logical key/button press: "enter",
	// "escape", "up", "down", "left", "right", "back", "select", or a
	// driver-defined extra name. Covers a terminal's Enter/Escape/arrow
	// keys (translated to the actual bytes internally — see ptydriver),
	// a TV remote's D-pad+select+back, a watch crown's press-in, a
	// keyboard's arrow keys.
	ActionPressKey ActionType = "press_key"

	// ActionNavigateDirection is discrete up/down/left/right movement of
	// a focus/cursor. Kept distinct from ActionPressKey so continuous-
	// feeling input (a crown's rotation quantized to steps, a D-pad
	// hold-to-repeat) isn't forced through a single-press verb.
	ActionNavigateDirection ActionType = "navigate_direction"

	// ActionClick activates whatever is at/identified by Target, which
	// is driver-defined: a browser's CSS selector or accessible name, a
	// touchscreen's x/y or element label. One primitive covers mouse
	// click and touchscreen tap — from the model's point of view both
	// are "indicate a target, activate it."
	ActionClick ActionType = "click"

	// ActionTypeText enters a string into whatever currently has text
	// focus: a browser form field, a mobile keyboard, or (for the common
	// free-text case) a terminal.
	ActionTypeText ActionType = "type_text"

	// ActionDoubleClick is a double-click/double-tap on Target (or an
	// X/Y coordinate) — reuses ClickParams' shape.
	ActionDoubleClick ActionType = "double_click"

	// ActionRightClick is a right-click (secondary click/long-press) on
	// Target (or an X/Y coordinate) — reuses ClickParams' shape.
	ActionRightClick ActionType = "right_click"

	// ActionMouseMove moves the pointer to/hovers over Target (or an
	// X/Y coordinate), without clicking — reuses ClickParams' shape.
	ActionMouseMove ActionType = "mouse_move"

	// ActionScroll scrolls Target into view, or the viewport by a
	// delta when Target is empty.
	ActionScroll ActionType = "scroll"

	// ActionDrag drags from one target to another — a press-move-release
	// gesture, e.g. reordering a list or dragging a slider handle.
	ActionDrag ActionType = "drag"

	// ActionSwipe is a directional touch gesture (flick) on Target.
	// Defined for vocabulary completeness; no current driver dispatches
	// it (same status ActionNavigateDirection has today) — it exists so
	// a future touch-capable driver has a ready-made primitive to adopt
	// rather than inventing its own name.
	ActionSwipe ActionType = "swipe"

	// ActionPinch is a two-finger pinch/zoom gesture on Target. Like
	// ActionSwipe, defined but not dispatched by any current driver.
	ActionPinch ActionType = "pinch"
)

// PressKeyParams is ActionPressKey's param shape.
type PressKeyParams struct {
	// Key is a logical key name. The well-known set is "enter",
	// "escape", "tab", "backspace", "delete", "insert", "home", "end",
	// "pageup", "pagedown", "space", "up", "down", "left", "right",
	// "back", "select", "f1".."f12", or a single printable character; a
	// driver may accept additional names of its own as a free-text
	// fallback, documented in its own PromptFragment.
	Key string `json:"key"`

	// Modifiers are held down for the duration of the press: any of
	// "ctrl", "alt", "shift", "meta". Optional; omit for a bare
	// keypress.
	Modifiers []string `json:"modifiers,omitempty"`
}

// NavigateDirectionParams is ActionNavigateDirection's param shape.
type NavigateDirectionParams struct {
	Direction string `json:"direction"` // "up" | "down" | "left" | "right"
}

// ClickParams is ActionClick's param shape — also reused verbatim by
// ActionDoubleClick, ActionRightClick, and ActionMouseMove, since all four
// share the same "identify a target, act on it" shape.
type ClickParams struct {
	// Target identifies what to click. Driver-defined: a CSS selector,
	// an accessible name — see the offering driver's own PromptFragment
	// for the exact contract. Leave empty when using X/Y instead.
	Target string `json:"target"`

	// X, Y are an optional coordinate-based alternative to Target, for a
	// driver/action that supports pointing at raw coordinates instead of
	// a named element (e.g. mouse_move with no element to hover, or a
	// click where no stable selector exists). A driver uses whichever it
	// is given; Target takes precedence when both are set.
	X int `json:"x,omitempty"`
	Y int `json:"y,omitempty"`
}

// TypeTextParams is ActionTypeText's param shape.
type TypeTextParams struct {
	Text string `json:"text"`
}

// ScrollParams is ActionScroll's param shape.
type ScrollParams struct {
	// Target scrolls that element into view. Leave empty and use
	// DeltaX/DeltaY to scroll the viewport by an amount instead.
	Target string `json:"target,omitempty"`
	DeltaX int    `json:"delta_x,omitempty"`
	DeltaY int    `json:"delta_y,omitempty"`
}

// DragParams is ActionDrag's param shape: press on From, move to To,
// release — both driver-defined target identifiers, the same contract
// ClickParams.Target uses.
type DragParams struct {
	From string `json:"from"`
	To   string `json:"to"`
}

// SwipeParams is ActionSwipe's param shape.
type SwipeParams struct {
	Target    string `json:"target"`
	Direction string `json:"direction"` // "up" | "down" | "left" | "right"
	Distance  int    `json:"distance,omitempty"`
}

// PinchParams is ActionPinch's param shape.
type PinchParams struct {
	Target string  `json:"target"`
	Scale  float64 `json:"scale"` // <1 pinches in, >1 pinches out
}

func mustSchema(s string) json.RawMessage { return json.RawMessage(s) }

// PrimitivePressKey is the canonical ActionSpec for ActionPressKey.
// Drivers that support key/button presses reference this value directly
// (rather than redefining their own description) so the prompt text and
// param schema stay identical across every driver that offers it.
var PrimitivePressKey = ActionSpec{
	Type: ActionPressKey,
	Description: "Press a named logical key: \"enter\", \"escape\", \"tab\", \"backspace\", \"delete\", \"insert\", " +
		"\"home\", \"end\", \"pageup\", \"pagedown\", \"space\", \"up\", \"down\", \"left\", \"right\", \"back\", " +
		"\"select\", \"f1\".. \"f12\", or a single printable character. Optionally hold modifiers: \"ctrl\", \"alt\", " +
		"\"shift\", \"meta\" (e.g. key \"c\" with modifiers [\"ctrl\"] for Ctrl-C). The driver translates this to " +
		"whatever the underlying UI actually needs.",
	ParamSchema: mustSchema(`{
		"type": "object",
		"properties": {
			"key": {"type": "string", "description": "Logical key name, e.g. \"enter\", \"escape\", \"tab\", \"backspace\", \"delete\", \"up\", \"down\", \"left\", \"right\", \"back\", \"select\", \"f1\", or a single character."},
			"modifiers": {"type": "array", "items": {"type": "string", "enum": ["ctrl", "alt", "shift", "meta"]}, "description": "Optional modifier keys held during the press."}
		},
		"required": ["key"]
	}`),
}

// PrimitiveDirectional is the canonical ActionSpec for ActionNavigateDirection.
var PrimitiveDirectional = ActionSpec{
	Type:        ActionNavigateDirection,
	Description: "Move focus/cursor one discrete step in a direction: \"up\", \"down\", \"left\", or \"right\".",
	ParamSchema: mustSchema(`{
		"type": "object",
		"properties": {
			"direction": {"type": "string", "enum": ["up", "down", "left", "right"]}
		},
		"required": ["direction"]
	}`),
}

// PrimitiveClick is the canonical ActionSpec for ActionClick.
var PrimitiveClick = ActionSpec{
	Type:        ActionClick,
	Description: "Activate whatever is at/identified by \"target\" (a click or tap, depending on the device). Some drivers also accept \"x\"/\"y\" coordinates instead of a target.",
	ParamSchema: mustSchema(`{
		"type": "object",
		"properties": {
			"target": {"type": "string", "description": "Driver-defined target identifier — see this driver's own action notes for the exact format."},
			"x": {"type": "integer", "description": "Optional coordinate alternative to target."},
			"y": {"type": "integer", "description": "Optional coordinate alternative to target."}
		}
	}`),
}

// PrimitiveDoubleClick, PrimitiveRightClick, and PrimitiveMouseMove all
// reuse ClickParams' shape — see ActionDoubleClick/ActionRightClick/
// ActionMouseMove's doc comments for why one param shape fits all four
// "identify a target, act on it" primitives.
var PrimitiveDoubleClick = ActionSpec{
	Type:        ActionDoubleClick,
	Description: "Double-click/double-tap whatever is at/identified by \"target\" (or \"x\"/\"y\").",
	ParamSchema: PrimitiveClick.ParamSchema,
}

var PrimitiveRightClick = ActionSpec{
	Type:        ActionRightClick,
	Description: "Right-click (secondary click/long-press) whatever is at/identified by \"target\" (or \"x\"/\"y\").",
	ParamSchema: PrimitiveClick.ParamSchema,
}

var PrimitiveMouseMove = ActionSpec{
	Type:        ActionMouseMove,
	Description: "Move the pointer to/hover over whatever is at/identified by \"target\" (or \"x\"/\"y\"), without clicking.",
	ParamSchema: PrimitiveClick.ParamSchema,
}

// PrimitiveScroll is the canonical ActionSpec for ActionScroll.
var PrimitiveScroll = ActionSpec{
	Type:        ActionScroll,
	Description: "Scroll \"target\" into view, or scroll the viewport by (\"delta_x\", \"delta_y\") when target is omitted.",
	ParamSchema: mustSchema(`{
		"type": "object",
		"properties": {
			"target": {"type": "string", "description": "Optional — scroll this element into view."},
			"delta_x": {"type": "integer", "description": "Horizontal scroll amount when target is omitted."},
			"delta_y": {"type": "integer", "description": "Vertical scroll amount when target is omitted."}
		}
	}`),
}

// PrimitiveDrag is the canonical ActionSpec for ActionDrag.
var PrimitiveDrag = ActionSpec{
	Type:        ActionDrag,
	Description: "Press on \"from\", drag to \"to\", and release — both driver-defined target identifiers.",
	ParamSchema: mustSchema(`{
		"type": "object",
		"properties": {
			"from": {"type": "string"},
			"to": {"type": "string"}
		},
		"required": ["from", "to"]
	}`),
}

// PrimitiveSwipe is the canonical ActionSpec for ActionSwipe. No current
// driver dispatches it — see ActionSwipe's doc comment.
var PrimitiveSwipe = ActionSpec{
	Type:        ActionSwipe,
	Description: "Swipe on \"target\" in \"direction\" (\"up\", \"down\", \"left\", \"right\"), optionally by \"distance\".",
	ParamSchema: mustSchema(`{
		"type": "object",
		"properties": {
			"target": {"type": "string"},
			"direction": {"type": "string", "enum": ["up", "down", "left", "right"]},
			"distance": {"type": "integer"}
		},
		"required": ["target", "direction"]
	}`),
}

// PrimitivePinch is the canonical ActionSpec for ActionPinch. No current
// driver dispatches it — see ActionPinch's doc comment.
var PrimitivePinch = ActionSpec{
	Type:        ActionPinch,
	Description: "Pinch/zoom on \"target\" by \"scale\" (less than 1 pinches in, greater than 1 pinches out).",
	ParamSchema: mustSchema(`{
		"type": "object",
		"properties": {
			"target": {"type": "string"},
			"scale": {"type": "number"}
		},
		"required": ["target", "scale"]
	}`),
}

// PrimitiveTypeText is the canonical ActionSpec for ActionTypeText.
var PrimitiveTypeText = ActionSpec{
	Type:        ActionTypeText,
	Description: "Enter text into whatever currently has text focus.",
	ParamSchema: mustSchema(`{
		"type": "object",
		"properties": {
			"text": {"type": "string"}
		},
		"required": ["text"]
	}`),
}
