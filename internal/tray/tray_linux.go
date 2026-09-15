//go:build linux

package tray

import (
	"fmt"
	"os"
	"sync"
	"time"

	"github.com/godbus/dbus/v5"
	"github.com/godbus/dbus/v5/prop"
)

// SNI constants. A StatusNotifierItem is exported on a fixed path; the host
// (a panel) learns about it through org.kde.StatusNotifierWatcher.
const (
	itemPath    = "/StatusNotifierItem"
	itemIface   = "org.kde.StatusNotifierItem"
	propsIface  = "org.freedesktop.DBus.Properties"
	watcherName = "org.kde.StatusNotifierWatcher"
	watcherPath = "/StatusNotifierWatcher"
)

// registerRetry paces StatusNotifierWatcher registration attempts. Login
// autostart usually beats the panel, and the old code gave up forever after
// one failed attempt — the icon then never appeared for the whole session.
const registerRetry = 5 * time.Second

func statusPixmap(ok, dim bool) []pixEntry {
	var w, h int32
	var pix []byte
	if dim {
		w, h, pix = IconPixmapDimFor(32, ok)
	} else {
		w, h, pix = IconPixmapFor(32, ok)
	}
	return []pixEntry{{W: w, H: h, D: pix}}
}

func statusTip(ok bool, text string) tip {
	return tip{IconName: "", Pix: statusPixmap(ok, false), Title: "Codex Lang", Text: text}
}

// sni implements the item's method interface: every click opens the page.
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

func (s *sni) Scroll(delta int32, orientation string) *dbus.Error { return nil }

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

// itemProps owns the item's property state and implements
// org.freedesktop.DBus.Properties itself.
//
// Why not the prop package's Export: prop.Export deep-copies the props it is
// given, so the structs the tray kept mutating were no longer the ones served
// over D-Bus. Get/GetAll answered with the startup pixmap forever, and the
// only thing that ever changed was the hand-built PropertiesChanged payload —
// a host that re-reads properties saw a stale icon. Owning the handler keeps
// signal and queryable state in lockstep, and the mutex kills the data race
// between the D-Bus dispatch goroutine and the paint loop.
type itemProps struct {
	mu       sync.Mutex
	conn     *dbus.Conn
	logf     func(string)
	warned   map[string]bool
	category string
	id       string
	title    string
	status   string
	windowID int32
	iconName string
	icon     []pixEntry
	tooltip  tip
	itemMenu bool
	menu     dbus.ObjectPath
}

func newItemProps(conn *dbus.Conn, logf func(string)) *itemProps {
	return &itemProps{
		conn:     conn,
		logf:     logf,
		warned:   map[string]bool{},
		category: "ApplicationStatus",
		id:       "codex-lang",
		title:    "Codex Lang",
		status:   "Active",
		icon:     statusPixmap(true, false),
		tooltip:  statusTip(true, "Codex Lang 启动中…"),
		itemMenu: false,
		menu:     dbus.ObjectPath("/NO_DBUSMENU"),
	}
}

// Get implements org.freedesktop.DBus.Properties.Get.
func (p *itemProps) Get(iface, property string) (dbus.Variant, *dbus.Error) {
	if iface != itemIface {
		return dbus.Variant{}, prop.ErrIfaceNotFound
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	value, ok := p.valueLocked(property)
	if !ok {
		return dbus.Variant{}, prop.ErrPropNotFound
	}
	return dbus.MakeVariant(value), nil
}

// GetAll implements org.freedesktop.DBus.Properties.GetAll.
func (p *itemProps) GetAll(iface string) (map[string]dbus.Variant, *dbus.Error) {
	if iface != itemIface {
		return nil, prop.ErrIfaceNotFound
	}
	p.mu.Lock()
	defer p.mu.Unlock()
	out := make(map[string]dbus.Variant, 10)
	for _, name := range itemPropertyNames {
		if value, ok := p.valueLocked(name); ok {
			out[name] = dbus.MakeVariant(value)
		}
	}
	return out, nil
}

// Set implements org.freedesktop.DBus.Properties.Set: every property the item
// exposes is read-only, so a host that tries to write one gets the standard
// error instead of silence.
func (p *itemProps) Set(iface, property string, value dbus.Variant) *dbus.Error {
	if iface != itemIface {
		return prop.ErrIfaceNotFound
	}
	p.mu.Lock()
	_, known := p.valueLocked(property)
	p.mu.Unlock()
	if !known {
		return prop.ErrPropNotFound
	}
	return prop.ErrReadOnly
}

var itemPropertyNames = []string{
	"Category", "Id", "Title", "Status", "WindowId", "IconName",
	"IconPixmap", "ToolTip", "ItemIsMenu", "Menu",
}

// valueLocked returns one property's current value. The caller holds the lock.
func (p *itemProps) valueLocked(property string) (any, bool) {
	switch property {
	case "Category":
		return p.category, true
	case "Id":
		return p.id, true
	case "Title":
		return p.title, true
	case "Status":
		return p.status, true
	case "WindowId":
		return p.windowID, true
	case "IconName":
		return p.iconName, true
	case "IconPixmap":
		return p.icon, true
	case "ToolTip":
		return p.tooltip, true
	case "ItemIsMenu":
		return p.itemMenu, true
	case "Menu":
		return p.menu, true
	default:
		return nil, false
	}
}

// apply stores one frame and tells the panel about it.
//
// The old code emitted only org.freedesktop.DBus.Properties.PropertiesChanged.
// Hosts differ in what they listen for, and a host that waits for the SNI
// signals (NewIcon / NewToolTip / NewStatus) kept showing the first pixmap it
// had ever read. Both channels are now fed, and an emit error is logged once
// instead of being dropped on the floor.
func (p *itemProps) apply(st panelState) {
	p.mu.Lock()
	p.status = st.status
	p.icon = statusPixmap(st.ok, st.dim)
	p.tooltip = statusTip(st.ok, st.text)
	icon, tooltip, status := p.icon, p.tooltip, p.status
	p.mu.Unlock()

	p.emit(propsIface+".PropertiesChanged", itemIface,
		map[string]dbus.Variant{
			"IconPixmap": dbus.MakeVariant(icon),
			"ToolTip":    dbus.MakeVariant(tooltip),
			"Status":     dbus.MakeVariant(status),
		}, []string{})
	p.emit(itemIface + ".NewIcon")
	p.emit(itemIface + ".NewToolTip")
	p.emit(itemIface+".NewStatus", status)
}

func (p *itemProps) emit(name string, args ...any) {
	if p.conn == nil {
		return
	}
	if err := p.conn.Emit(dbus.ObjectPath(itemPath), name, args...); err != nil {
		p.warnOnce(name, "tray: emitting "+name+" failed: "+err.Error())
	}
}

func (p *itemProps) warnOnce(key, msg string) {
	p.mu.Lock()
	seen := p.warned[key]
	p.warned[key] = true
	p.mu.Unlock()
	if !seen {
		p.logf(msg)
	}
}

// Run registers the panel icon and blocks: green = healthy, red = stopped or
// faulted, alternating with a dim variant while a translation is in flight.
// status reports (healthy, translating, tooltip); logf receives one-line
// diagnostics and may be nil. Clicks open the Control Page via onActivate.
func Run(onActivate func(), status func() (bool, bool, string), logf func(string)) error {
	if logf == nil {
		logf = func(string) {}
	}
	conn, err := dbus.ConnectSessionBus()
	if err != nil {
		return err
	}
	if err := conn.Export(&sni{activate: onActivate}, dbus.ObjectPath(itemPath), itemIface); err != nil {
		conn.Close()
		return err
	}
	props := newItemProps(conn, logf)
	if err := conn.Export(props, dbus.ObjectPath(itemPath), propsIface); err != nil {
		conn.Close()
		return err
	}
	name := fmt.Sprintf("org.kde.StatusNotifierItem-%d-1", os.Getpid())
	reply, err := conn.RequestName(name, dbus.NameFlagDoNotQueue)
	if err != nil {
		conn.Close()
		return err
	}
	if reply != dbus.RequestNameReplyPrimaryOwner {
		conn.Close()
		return fmt.Errorf("tray name already taken")
	}

	// The watcher may not exist yet (login autostart races the panel) and may
	// come and go (panel restart, session switch). Watch its owner and
	// re-register whenever it (re)appears.
	registered := false
	register := func() bool {
		call := conn.Object(watcherName, dbus.ObjectPath(watcherPath)).Call(
			"org.kde.StatusNotifierWatcher.RegisterStatusNotifierItem", 0, name,
		)
		if call.Err != nil {
			registered = false
			logf("tray: org.kde.StatusNotifierWatcher not available yet: " + call.Err.Error())
			return false
		}
		if !registered {
			registered = true
			logf("tray: registered with org.kde.StatusNotifierWatcher as " + name)
		}
		return true
	}
	signals := make(chan *dbus.Signal, 32)
	conn.Signal(signals)
	if err := conn.AddMatchSignal(
		dbus.WithMatchObjectPath(dbus.ObjectPath("/org/freedesktop/DBus")),
		dbus.WithMatchInterface("org.freedesktop.DBus"),
		dbus.WithMatchMember("NameOwnerChanged"),
		dbus.WithMatchArg(0, watcherName),
	); err != nil {
		logf("tray: cannot watch the StatusNotifierWatcher name: " + err.Error())
	}
	register()

	// First frame right away, so a host that only reads properties when it
	// first sees the item already gets a correct icon.
	lastSig := ""
	phase := false
	var lastActive time.Time
	nextRegister := time.Now().Add(registerRetry)
	frame := func() {
		ok, translating, _ := true, false, ""
		if status != nil {
			ok, translating, _ = status()
		}
		now := time.Now()
		if translating {
			lastActive = now
		}
		dim := blinkPhase(phase, lastActive, now)
		phase = !phase
		st := panelStateFor(ok, dim, translating)
		sig := paintSig(st.ok, st.dim, st.text) + "\x00" + st.status
		if sig == lastSig {
			return
		}
		lastSig = sig
		props.apply(st)
	}
	frame()
	ticker := time.NewTicker(pollInterval)
	defer ticker.Stop()
	for {
		select {
		case sig, open := <-signals:
			if !open || sig == nil {
				continue
			}
			if sig.Name != "org.freedesktop.DBus.NameOwnerChanged" || len(sig.Body) < 3 {
				continue
			}
			if owner, _ := sig.Body[0].(string); owner != watcherName {
				continue
			}
			if newOwner, _ := sig.Body[2].(string); newOwner == "" {
				registered = false
				logf("tray: org.kde.StatusNotifierWatcher went away; will re-register")
				continue
			}
			logf("tray: org.kde.StatusNotifierWatcher appeared; registering again")
			register()
			nextRegister = time.Now().Add(registerRetry)
		case <-ticker.C:
			if !registered && !time.Now().Before(nextRegister) {
				register()
				nextRegister = time.Now().Add(registerRetry)
			}
			frame()
		}
	}
}

func Available() bool { return true }
