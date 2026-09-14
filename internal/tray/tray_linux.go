//go:build linux

package tray

import (
	"fmt"
	"os"
	"time"

	"github.com/godbus/dbus/v5"
	"github.com/godbus/dbus/v5/prop"
)

type sni struct {
	activate func()
}

func (s *sni) ContextMenu(x, y int32) *dbus.Error {
	s.activate()
	return nil
}

func (s *sni) Activate(x, y int32) *dbus.Error {
	s.activate()
	return nil
}

func (s *sni) SecondaryActivate(x, y int32) *dbus.Error {
	s.activate()
	return nil
}

func (s *sni) Scroll(delta int32, orientation string) *dbus.Error {
	return nil
}

type pixEntry struct {
	W int32
	H int32
	D []byte
}

type tip struct {
	IconName string
	Pix      []pixEntry
	Title    string
	Text     string
}

// pollInterval drives the blink phase. Health itself is cheap local state,
// so every tick re-reads it; D-Bus emits only go out when pixels or text
// actually change.
const pollInterval = 500 * time.Millisecond

// Run registers the panel icon: green = healthy, red = stopped/faulted,
// blinking green/dim while a translation is in flight. status reports
// (healthy, translating, tooltip); a nil status means always ok, idle.
// Clicks (left, right, middle) all open the Control Page via onActivate.
func Run(onActivate func(), status func() (bool, bool, string)) error {
	conn, err := dbus.ConnectSessionBus()
	if err != nil {
		return err
	}
	item := &sni{activate: onActivate}
	if err := conn.Export(item, "/StatusNotifierItem", "org.kde.StatusNotifierItem"); err != nil {
		conn.Close()
		return err
	}
	iconProp := &prop.Prop{Value: statusPixmap(true, false), Writable: false, Emit: prop.EmitTrue}
	tipProp := &prop.Prop{Value: statusTip(true, "Codex Lang 启动中…"), Writable: false, Emit: prop.EmitTrue}
	if _, err := prop.Export(conn, "/StatusNotifierItem", map[string]map[string]*prop.Prop{
		"org.kde.StatusNotifierItem": {
			"Category":   {Value: "ApplicationStatus", Writable: false, Emit: prop.EmitTrue},
			"Id":         {Value: "codex-lang", Writable: false, Emit: prop.EmitTrue},
			"Title":      {Value: "Codex Lang", Writable: false, Emit: prop.EmitTrue},
			"Status":     {Value: "Active", Writable: false, Emit: prop.EmitTrue},
			"WindowId":   {Value: int32(0), Writable: false, Emit: prop.EmitTrue},
			"IconName":   {Value: "", Writable: false, Emit: prop.EmitTrue},
			"IconPixmap": iconProp,
			"ToolTip":    tipProp,
			"ItemIsMenu": {Value: false, Writable: false, Emit: prop.EmitTrue},
			"Menu":       {Value: dbus.ObjectPath("/NO_DBUSMENU"), Writable: false, Emit: prop.EmitTrue},
		},
	}); err != nil {
		conn.Close()
		return err
	}
	name := fmt.Sprintf("org.kde.StatusNotifierItem-%d-1", os.Getpid())
	reply, err := conn.RequestName(name, dbus.NameFlagDoNotQueue)
	if err != nil || reply != dbus.RequestNameReplyPrimaryOwner {
		conn.Close()
		if err != nil {
			return err
		}
		return fmt.Errorf("tray name already taken")
	}
	call := conn.Object("org.kde.StatusNotifierWatcher", "/StatusNotifierWatcher").Call(
		"org.kde.StatusNotifierWatcher.RegisterStatusNotifierItem", 0, name,
	)
	if call.Err != nil {
		conn.Close()
		return call.Err
	}
	emit := func(icon []pixEntry, tip tip) {
		iconProp.Value = icon
		tipProp.Value = tip
		_ = conn.Emit("/StatusNotifierItem", "org.freedesktop.DBus.Properties.PropertiesChanged",
			"org.kde.StatusNotifierItem",
			map[string]dbus.Variant{
				"IconPixmap": dbus.MakeVariant(iconProp.Value),
				"ToolTip":    dbus.MakeVariant(tipProp.Value),
			},
			[]string{},
		)
	}
	// Painted signature, so idle ticks stay silent on the bus.
	last := ""
	phase := false
	var lastActive time.Time
	tick := func() {
		now := time.Now()
		ok, translating, text := true, false, "Codex Lang 运行中，点按打开 Control Page"
		if status != nil {
			ok, translating, text = status()
		}
		phase = !phase
		if translating && ok {
			lastActive = now
		}
		dim := blinkDim(ok, phase, lastActive, now)
		sig := paintSig(ok, dim, text)
		if sig == last {
			return
		}
		last = sig
		emit(statusPixmap(ok, dim), statusTip(ok, text))
	}
	tick()
	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()
	for range ticker.C {
		tick()
	}
	return nil
}

// blinkHold keeps the blink going a few seconds after the last observed
// translating poll, so even a sub-second call reads as a visible blink.
const blinkHold = 5 * time.Second

// blinkDim reports whether the dim blink phase shows: healthy, inside the
// trailing hold window of observed translating activity, on blink phase.
func blinkDim(ok, phase bool, lastActive, now time.Time) bool {
	if !ok || lastActive.IsZero() {
		return false
	}
	return !now.After(lastActive.Add(blinkHold)) && phase
}

func paintSig(ok, dim bool, text string) string {
	m := "g"
	if !ok {
		m = "r"
	} else if dim {
		m = "d"
	}
	return m + "\x00" + text
}

func statusPixmap(ok, dim bool) []pixEntry {
	var w, h int32
	var pix []byte
	if dim {
		w, h, pix = IconPixmapDim(32)
	} else {
		w, h, pix = IconPixmapFor(32, ok)
	}
	return []pixEntry{{W: w, H: h, D: pix}}
}

func statusTip(ok bool, text string) tip {
	return tip{IconName: "", Pix: statusPixmap(ok, false), Title: "Codex Lang", Text: text}
}

func Available() bool { return true }
