//go:build windows

// The Windows tray is written directly against shell32/user32 with syscall:
// no cgo, so it cross-compiles from any host and needs no third-party module
// (the only dependency stays godbus, which Windows does not use yet).
//
// Status: compiles for windows/amd64, windows/arm64 and windows/386. It has
// not been exercised on a real Windows desktop yet — every Win32 call is
// guarded, and Run recovers from panics so a failure degrades to "no icon"
// instead of taking the watchdog down with it.
package tray

import (
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sync"
	"syscall"
	"time"
	"unsafe"
)

var (
	user32   = syscall.NewLazyDLL("user32.dll")
	shell32  = syscall.NewLazyDLL("shell32.dll")
	kernel32 = syscall.NewLazyDLL("kernel32.dll")

	procRegisterClassExW       = user32.NewProc("RegisterClassExW")
	procCreateWindowExW        = user32.NewProc("CreateWindowExW")
	procDefWindowProcW         = user32.NewProc("DefWindowProcW")
	procDestroyWindow          = user32.NewProc("DestroyWindow")
	procGetMessageW            = user32.NewProc("GetMessageW")
	procTranslateMessage       = user32.NewProc("TranslateMessage")
	procDispatchMessageW       = user32.NewProc("DispatchMessageW")
	procPostQuitMessage        = user32.NewProc("PostQuitMessage")
	procLoadImageW             = user32.NewProc("LoadImageW")
	procRegisterWindowMessageW = user32.NewProc("RegisterWindowMessageW")
	procShellNotifyIconW       = shell32.NewProc("Shell_NotifyIconW")
	procGetModuleHandleW       = kernel32.NewProc("GetModuleHandleW")
	procDestroyIcon            = user32.NewProc("DestroyIcon")
)

const (
	wmApp           = 0x8000
	trayCallbackMsg = wmApp + 1
	wmDestroy       = 0x0002
	wmClose         = 0x0010
	wmLButtonUp     = 0x0202
	wmLButtonDblClk = 0x0203
	wmRButtonUp     = 0x0205
	wsPopup         = 0x80000000
	wsExToolWindow  = 0x00000080
	imageIcon       = 1
	lrLoadFromFile  = 0x0010
	lrDefaultSize   = 0x0040
	nimAdd          = 0
	nimModify       = 1
	nimDelete       = 2
	nifMessage      = 0x00000001
	nifIcon         = 0x00000002
	nifTip          = 0x00000004
	tipChars        = 128
)

type wndClassExW struct {
	cbSize        uint32
	style         uint32
	lpfnWndProc   uintptr
	cbClsExtra    int32
	cbWndExtra    int32
	hInstance     syscall.Handle
	hIcon         syscall.Handle
	hCursor       syscall.Handle
	hbrBackground syscall.Handle
	lpszMenuName  *uint16
	lpszClassName *uint16
	hIconSm       syscall.Handle
}

type point struct{ x, y int32 }

type msgW struct {
	hwnd    syscall.Handle
	message uint32
	wParam  uintptr
	lParam  uintptr
	time    uint32
	pt      point
}

// notifyIconDataW mirrors NOTIFYICONDATAW (szInfo/uIntOrVersion is a union in
// C; the fields below reproduce its offsets and total size, which the
// compile-time asserts in tray_windows_size*.go pin).
type notifyIconDataW struct {
	cbSize           uint32
	hWnd             syscall.Handle
	uID              uint32
	uFlags           uint32
	uCallbackMessage uint32
	hIcon            syscall.Handle
	szTip            [tipChars]uint16
	dwState          uint32
	dwStateMask      uint32
	szInfo           [256]uint16
	szInfoTitle      [64]uint16
	dwInfoFlags      uint32
	guidItem         [16]byte
	hBalloonIcon     syscall.Handle
}

type winTray struct {
	hwnd     syscall.Handle
	uID      uint32
	activate func()
	status   func() (bool, bool, string)
	logf     func(string)
	icons    map[string]syscall.Handle
	iconDir  string
	taskbar  uint32
	nid      notifyIconDataW

	mu         sync.Mutex
	warned     map[string]bool
	phase      bool
	lastActive time.Time
	lastSig    string
	done       chan struct{}
}

var (
	trayMu    sync.Mutex
	trayState *winTray
	// The callback must outlive the program: NewCallback's trampoline is
	// referenced by the window class, not by Go's GC.
	trayProc = syscall.NewCallback(trayWndProc)
)

func Run(onActivate func(), status func() (bool, bool, string), logf func(string)) (err error) {
	if logf == nil {
		logf = func(string) {}
	}
	// A mis-sized Win32 call would be a hard crash on Windows; degrade to an
	// error instead and let the watchdog keep serving the Control Page.
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("tray: %v", r)
		}
	}()

	// The window class, the window and the message loop are thread-bound.
	runtime.LockOSThread()
	defer runtime.UnlockOSThread()

	t := &winTray{
		uID:      1,
		activate: onActivate,
		status:   status,
		logf:     logf,
		icons:    map[string]syscall.Handle{},
		done:     make(chan struct{}),
	}
	if err := t.loadIcons(); err != nil {
		return err
	}
	defer t.cleanupIcons()

	hInst, _, _ := procGetModuleHandleW.Call(0)
	className, err := syscall.UTF16PtrFromString("CodexLangTrayWindow")
	if err != nil {
		return err
	}
	wc := wndClassExW{
		cbSize:        uint32(unsafe.Sizeof(wndClassExW{})),
		lpfnWndProc:   trayProc,
		hInstance:     syscall.Handle(hInst),
		lpszClassName: className,
	}
	if atom, _, callErr := procRegisterClassExW.Call(uintptr(unsafe.Pointer(&wc))); atom == 0 {
		return fmt.Errorf("RegisterClassExW: %v", callErr)
	}
	hwnd, _, callErr := procCreateWindowExW.Call(
		wsExToolWindow,
		uintptr(unsafe.Pointer(className)),
		0,
		wsPopup,
		0, 0, 0, 0,
		0, 0, hInst, 0,
	)
	if hwnd == 0 {
		return fmt.Errorf("CreateWindowExW: %v", callErr)
	}
	t.hwnd = syscall.Handle(hwnd)
	defer procDestroyWindow.Call(hwnd)

	trayMu.Lock()
	trayState = t
	trayMu.Unlock()
	defer func() {
		trayMu.Lock()
		trayState = nil
		trayMu.Unlock()
	}()

	taskbarMsg, _, _ := procRegisterWindowMessageW.Call(uintptr(unsafe.Pointer(mustUTF16("TaskbarCreated"))))
	t.taskbar = uint32(taskbarMsg)

	t.nid = notifyIconDataW{
		cbSize:           uint32(unsafe.Sizeof(notifyIconDataW{})),
		hWnd:             t.hwnd,
		uID:              t.uID,
		uFlags:           nifMessage | nifIcon | nifTip,
		uCallbackMessage: trayCallbackMsg,
	}
	t.paint(true)
	if !t.notify(nimAdd) {
		return fmt.Errorf("Shell_NotifyIconW could not add the icon")
	}
	logf("tray: notification-area icon added")

	go func() {
		ticker := time.NewTicker(pollInterval)
		defer ticker.Stop()
		for {
			select {
			case <-t.done:
				return
			case <-ticker.C:
				t.paint(false)
			}
		}
	}()
	defer close(t.done)

	var msg msgW
	for {
		ret, _, callErr := procGetMessageW.Call(uintptr(unsafe.Pointer(&msg)), 0, 0, 0)
		if int32(ret) == -1 {
			return fmt.Errorf("GetMessageW: %v", callErr)
		}
		if ret == 0 {
			break // WM_QUIT
		}
		procTranslateMessage.Call(uintptr(unsafe.Pointer(&msg)))
		procDispatchMessageW.Call(uintptr(unsafe.Pointer(&msg)))
	}
	t.notify(nimDelete)
	return nil
}

func mustUTF16(s string) *uint16 {
	p, err := syscall.UTF16PtrFromString(s)
	if err != nil {
		return nil
	}
	return p
}

// paint renders the current state into the notification area. forced is the
// first frame, which must land even if nothing changed since the last one.
func (t *winTray) paint(forced bool) {
	ok, translating := true, false
	if t.status != nil {
		ok, translating, _ = t.status()
	}
	now := time.Now()
	t.mu.Lock()
	if translating {
		t.lastActive = now
	}
	dim := blinkPhase(t.phase, t.lastActive, now)
	t.phase = !t.phase
	st := panelStateFor(ok, dim, translating)
	sig := paintSig(st.ok, st.dim, st.text)
	if !forced && sig == t.lastSig {
		t.mu.Unlock()
		return
	}
	t.lastSig = sig
	t.mu.Unlock()

	icon := t.icons[iconKey(st.ok, st.dim)]
	if icon == 0 {
		return
	}
	nid := t.nid
	nid.hIcon = icon
	copyUTF16(nid.szTip[:], st.text)
	t.nid = nid
	t.notify(nimModify)
}

func (t *winTray) notify(action uintptr) bool {
	nid := t.nid
	nid.cbSize = uint32(unsafe.Sizeof(notifyIconDataW{}))
	nid.uFlags = nifMessage | nifIcon | nifTip
	ok, _, err := procShellNotifyIconW.Call(action, uintptr(unsafe.Pointer(&nid)))
	if ok == 0 {
		t.warnOnce("notify"+fmt.Sprint(action), fmt.Sprintf("tray: Shell_NotifyIconW(%d) failed: %v", action, err))
		return false
	}
	return true
}

func (t *winTray) warnOnce(key, msg string) {
	t.mu.Lock()
	seen := t.warnedLocked(key)
	t.mu.Unlock()
	if !seen {
		t.logf(msg)
	}
}

func (t *winTray) warnedLocked(key string) bool {
	if t.warned == nil {
		t.warned = map[string]bool{}
	}
	seen := t.warned[key]
	t.warned[key] = true
	return seen
}

// loadIcons renders the four frames to .ico files and loads them as HICONs.
// LoadImageW with LR_LOADFROMFILE is the boring, universally supported path;
// the files live in a temp directory removed when the tray stops.
func (t *winTray) loadIcons() error {
	dir, err := os.MkdirTemp("", "codex-lang-icons-")
	if err != nil {
		return err
	}
	t.iconDir = dir
	frames := map[string]struct{ ok, dim bool }{
		"ok":        {true, false},
		"ok-dim":    {true, true},
		"fault":     {false, false},
		"fault-dim": {false, true},
	}
	hInst, _, _ := procGetModuleHandleW.Call(0)
	for name, frame := range frames {
		data := icoForState(32, frame.ok, frame.dim)
		if len(data) == 0 {
			return fmt.Errorf("tray: empty icon for %s", name)
		}
		path := filepath.Join(dir, name+".ico")
		if err := os.WriteFile(path, data, 0o600); err != nil {
			return err
		}
		ptr, err := syscall.UTF16PtrFromString(path)
		if err != nil {
			return err
		}
		h, _, callErr := procLoadImageW.Call(
			hInst, uintptr(unsafe.Pointer(ptr)), imageIcon, 0, 0, lrLoadFromFile|lrDefaultSize,
		)
		if h == 0 {
			return fmt.Errorf("tray: LoadImageW(%s): %v", path, callErr)
		}
		t.icons[name] = syscall.Handle(h)
	}
	return nil
}

func (t *winTray) cleanupIcons() {
	for _, h := range t.icons {
		if h != 0 {
			procDestroyIcon.Call(uintptr(h))
		}
	}
	if t.iconDir != "" {
		_ = os.RemoveAll(t.iconDir)
	}
}

// trayWndProc is called by Windows on the main thread. It must never panic
// across the syscall boundary.
func trayWndProc(hwnd syscall.Handle, msg uint32, wparam, lparam uintptr) (result uintptr) {
	defer func() {
		if recover() != nil {
			result = 0
		}
	}()
	trayMu.Lock()
	t := trayState
	trayMu.Unlock()
	if t == nil {
		return defWindowProc(hwnd, msg, wparam, lparam)
	}
	if t.taskbar != 0 && msg == t.taskbar {
		// Explorer restarted: every notification-area icon is gone.
		t.logf("tray: Explorer restarted; re-adding the icon")
		if t.notify(nimAdd) {
			t.mu.Lock()
			t.lastSig = ""
			t.mu.Unlock()
			t.paint(true)
		}
		return 0
	}
	switch msg {
	case trayCallbackMsg:
		switch uint32(lparam) & 0xffff {
		case wmLButtonUp, wmLButtonDblClk, wmRButtonUp:
			if t.activate != nil {
				t.activate()
			}
		}
		return 0
	case wmClose:
		procDestroyWindow.Call(uintptr(hwnd))
		return 0
	case wmDestroy:
		procPostQuitMessage.Call(0)
		return 0
	default:
		return defWindowProc(hwnd, msg, wparam, lparam)
	}
}

func defWindowProc(hwnd syscall.Handle, msg uint32, wparam, lparam uintptr) uintptr {
	ret, _, _ := procDefWindowProcW.Call(uintptr(hwnd), uintptr(msg), wparam, lparam)
	return ret
}

func Available() bool { return true }
