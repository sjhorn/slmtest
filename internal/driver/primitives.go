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
)

// PressKeyParams is ActionPressKey's param shape.
type PressKeyParams struct {
	// Key is a logical key name. The well-known set is "enter",
	// "escape", "up", "down", "left", "right", "back", "select"; a
	// driver may accept additional names of its own as a free-text
	// fallback, documented in its own PromptFragment.
	Key string `json:"key"`
}

// NavigateDirectionParams is ActionNavigateDirection's param shape.
type NavigateDirectionParams struct {
	Direction string `json:"direction"` // "up" | "down" | "left" | "right"
}

// ClickParams is ActionClick's param shape.
type ClickParams struct {
	// Target identifies what to click. Driver-defined: a CSS selector,
	// an accessible name, an "x,y" coordinate — see the offering
	// driver's own PromptFragment for the exact contract.
	Target string `json:"target"`
}

// TypeTextParams is ActionTypeText's param shape.
type TypeTextParams struct {
	Text string `json:"text"`
}

func mustSchema(s string) json.RawMessage { return json.RawMessage(s) }

// PrimitivePressKey is the canonical ActionSpec for ActionPressKey.
// Drivers that support key/button presses reference this value directly
// (rather than redefining their own description) so the prompt text and
// param schema stay identical across every driver that offers it.
var PrimitivePressKey = ActionSpec{
	Type: ActionPressKey,
	Description: "Press a named logical key: \"enter\", \"escape\", \"up\", \"down\", \"left\", \"right\", " +
		"\"back\", or \"select\". The driver translates this to whatever the underlying UI actually needs.",
	ParamSchema: mustSchema(`{
		"type": "object",
		"properties": {
			"key": {"type": "string", "description": "Logical key name, e.g. \"enter\", \"escape\", \"up\", \"down\", \"left\", \"right\", \"back\", \"select\"."}
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
	Description: "Activate whatever is at/identified by \"target\" (a click or tap, depending on the device).",
	ParamSchema: mustSchema(`{
		"type": "object",
		"properties": {
			"target": {"type": "string", "description": "Driver-defined target identifier — see this driver's own action notes for the exact format."}
		},
		"required": ["target"]
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
