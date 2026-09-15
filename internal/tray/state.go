package tray

import "time"

// pollInterval drives the blink phase. Health is cheap local state; the paint
// path only emits when pixels, tooltip or status actually change. 200ms keeps
// a 1s translation visible as several alternating frames.
const pollInterval = 200 * time.Millisecond

// blinkHold keeps the blink going a few seconds after the last observed
// translating poll, so a sub-second call still reads as a visible blink.
const blinkHold = 5 * time.Second

// panelState is everything the icon shows, as data: testable without a panel.
type panelState struct {
	ok     bool
	dim    bool
	status string // "Active" or "NeedsAttention" on Linux; unused elsewhere
	text   string // tooltip body
}

// panelStateFor renders one frame. Health picks the colour and the tooltip,
// dim picks the blink half; nothing here consults health to decide *whether*
// to blink, because gating the blink on the health light is exactly the bug
// that made a red icon (missing key, front door down) blink-never, even while
// translations were in flight.
func panelStateFor(ok, dim, translating bool) panelState {
	st := panelState{ok: ok, dim: dim, status: "Active", text: StatusText(ok, translating)}
	if dim {
		// NeedsAttention is the mechanism panels are actually designed to
		// animate; pairing it with the dim pixmap gives hosts that ignore
		// pixmap swaps a second, spec-blessed way to show "busy".
		st.status = "NeedsAttention"
	}
	return st
}

// blinkPhase reports whether the dim half of the blink shows: inside the
// trailing hold window of observed translating activity, on blink phase.
// It takes no health argument on purpose.
func blinkPhase(phase bool, lastActive, now time.Time) bool {
	if lastActive.IsZero() {
		return false
	}
	return !now.After(lastActive.Add(blinkHold)) && phase
}

// paintSig is the "did anything the panel can see change" key for one frame.
func paintSig(ok, dim bool, text string) string {
	m := "g"
	if !ok {
		m = "r"
	} else if dim {
		m = "d"
	}
	return m + "\x00" + text
}

// StatusText is the tooltip body for a state; shared by every platform so the
// wording cannot drift between the trays.
func StatusText(ok, translating bool) string {
	switch {
	case !ok:
		return "已停止或故障，点按打开 Control Page"
	case translating:
		return "正在翻译…，点按打开 Control Page"
	default:
		return "翻译正常，点按打开 Control Page"
	}
}

// iconKey names the frame for a state. It lives here, next to the state
// itself, so the Windows tray (which keeps one HICON per name) and the tests
// share a single mapping even though the icons are loaded per platform.
func iconKey(ok, dim bool) string {
	switch {
	case !ok && dim:
		return "fault-dim"
	case !ok:
		return "fault"
	case dim:
		return "ok-dim"
	default:
		return "ok"
	}
}
