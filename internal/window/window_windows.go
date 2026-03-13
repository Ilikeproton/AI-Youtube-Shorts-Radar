//go:build windows

package window

import (
	"errors"
	"fmt"
	"unsafe"

	webview2 "github.com/jchv/go-webview2"
	"github.com/lxn/win"
	"golang.org/x/sys/windows"

	"youtubeshort/internal/config"
)

const (
	appTitle          = "AI YouTube Shorts Radar"
	appIconResourceID = 1
	iconSmall         = 0
	iconBig           = 1
)

var (
	user32Proc         = windows.NewLazySystemDLL("user32.dll")
	procSetWindowTextW = user32Proc.NewProc("SetWindowTextW")
)

func Open(url string, cfg config.App) error {
	width := uint(win.GetSystemMetrics(win.SM_CXSCREEN))
	height := uint(win.GetSystemMetrics(win.SM_CYSCREEN))

	w := webview2.NewWithOptions(webview2.WebViewOptions{
		Debug:     false,
		DataPath:  cfg.DataDir,
		AutoFocus: true,
		WindowOptions: webview2.WindowOptions{
			Title:  appTitle,
			Width:  1,
			Height: 1,
			IconId: appIconResourceID,
		},
	})
	if w == nil {
		return errors.New("failed to create webview window")
	}
	defer w.Destroy()

	hwnd := win.HWND(uintptr(w.Window()))
	if hwnd == 0 {
		return errors.New("webview did not return a native window handle")
	}

	applyWindowTitle(hwnd)
	applyWindowIcon(hwnd)
	configureWindow(hwnd)
	if err := w.Bind("appMinimize", func() error {
		win.ShowWindow(hwnd, win.SW_MINIMIZE)
		return nil
	}); err != nil {
		return err
	}
	if err := w.Bind("appClose", func() error {
		win.PostMessage(hwnd, win.WM_CLOSE, 0, 0)
		return nil
	}); err != nil {
		return err
	}
	w.SetTitle(appTitle)
	applyWindowTitle(hwnd)
	w.SetSize(int(width), int(height), webview2.HintFixed)
	w.SetHtml(startupHTML(url))
	w.Run()
	return nil
}

func applyWindowTitle(hwnd win.HWND) {
	setWindowText(hwnd, appTitle)

	if root := win.GetAncestor(hwnd, win.GA_ROOT); root != 0 && root != hwnd {
		setWindowText(root, appTitle)
	}
	if owner := win.GetAncestor(hwnd, win.GA_ROOTOWNER); owner != 0 && owner != hwnd {
		setWindowText(owner, appTitle)
	}
}

func setWindowText(hwnd win.HWND, title string) {
	if hwnd == 0 {
		return
	}
	ptr, err := windows.UTF16PtrFromString(title)
	if err != nil {
		return
	}
	procSetWindowTextW.Call(uintptr(hwnd), uintptr(unsafe.Pointer(ptr)))
}

func applyWindowIcon(hwnd win.HWND) {
	module := win.GetModuleHandle(nil)
	if module == 0 {
		return
	}

	smallIcon := win.HICON(win.LoadImage(
		module,
		win.MAKEINTRESOURCE(appIconResourceID),
		win.IMAGE_ICON,
		int32(win.GetSystemMetrics(win.SM_CXSMICON)),
		int32(win.GetSystemMetrics(win.SM_CYSMICON)),
		win.LR_DEFAULTSIZE|win.LR_SHARED,
	))
	if smallIcon != 0 {
		win.SendMessage(hwnd, win.WM_SETICON, iconSmall, uintptr(smallIcon))
	}

	largeIcon := win.HICON(win.LoadImage(
		module,
		win.MAKEINTRESOURCE(appIconResourceID),
		win.IMAGE_ICON,
		int32(win.GetSystemMetrics(win.SM_CXICON)),
		int32(win.GetSystemMetrics(win.SM_CYICON)),
		win.LR_DEFAULTSIZE|win.LR_SHARED,
	))
	if largeIcon != 0 {
		win.SendMessage(hwnd, win.WM_SETICON, iconBig, uintptr(largeIcon))
	}
}

func configureWindow(hwnd win.HWND) {
	style := uint32(win.GetWindowLong(hwnd, win.GWL_STYLE))
	style &^= uint32(win.WS_CAPTION | win.WS_THICKFRAME | win.WS_MINIMIZE | win.WS_MAXIMIZE | win.WS_SYSMENU)
	style |= uint32(win.WS_POPUP | win.WS_VISIBLE)
	win.SetWindowLong(hwnd, win.GWL_STYLE, int32(style))

	exStyle := uint32(win.GetWindowLong(hwnd, win.GWL_EXSTYLE))
	exStyle &^= uint32(win.WS_EX_DLGMODALFRAME | win.WS_EX_CLIENTEDGE | win.WS_EX_STATICEDGE)
	win.SetWindowLong(hwnd, win.GWL_EXSTYLE, int32(exStyle))

	width := int32(win.GetSystemMetrics(win.SM_CXSCREEN))
	height := int32(win.GetSystemMetrics(win.SM_CYSCREEN))
	win.SetWindowPos(hwnd, win.HWND_TOP, 0, 0, width, height, win.SWP_FRAMECHANGED|win.SWP_SHOWWINDOW)
	win.ShowWindow(hwnd, win.SW_SHOWMAXIMIZED)
	win.SetForegroundWindow(hwnd)
}

func startupHTML(targetURL string) string {
	return fmt.Sprintf(`<!doctype html>
<html lang="en">
<head>
  <meta charset="utf-8">
  <meta name="viewport" content="width=device-width, initial-scale=1">
  <title>%s</title>
  <style>
    :root { color-scheme: dark; }
    html, body { height: 100%%; margin: 0; background: #04060c; font-family: "Segoe UI", sans-serif; overflow: hidden; }
    body {
      display: flex;
      align-items: center;
      justify-content: center;
      background:
        radial-gradient(circle at 20%% 20%%, rgba(24, 56, 91, 0.9) 0%%, rgba(7, 16, 30, 0.96) 42%%, rgba(2, 4, 10, 1) 100%%);
      color: #f8fafc;
    }
    .boot-shell { display: flex; flex-direction: column; align-items: center; gap: 20px; text-align: center; }
    .boot-radar {
      position: relative;
      width: 140px;
      height: 140px;
      border-radius: 50%%;
      border: 1px solid rgba(255, 255, 255, 0.1);
      background:
        radial-gradient(circle at center, rgba(249, 115, 22, 0.22) 0, rgba(249, 115, 22, 0.08) 22%%, rgba(255, 255, 255, 0.02) 23%%, rgba(255, 255, 255, 0.02) 24%%, transparent 25%%),
        radial-gradient(circle at center, transparent 0 38%%, rgba(255, 255, 255, 0.06) 38.5%%, transparent 39.5%%),
        radial-gradient(circle at center, transparent 0 58%%, rgba(255, 255, 255, 0.06) 58.5%%, transparent 59.5%%);
      box-shadow: 0 24px 64px rgba(0, 0, 0, 0.35), inset 0 0 34px rgba(56, 189, 248, 0.08);
      overflow: hidden;
    }
    .boot-radar::before {
      content: "";
      position: absolute;
      inset: 10px;
      border-radius: 50%%;
      border: 1px solid rgba(56, 189, 248, 0.16);
    }
    .boot-radar::after {
      content: "";
      position: absolute;
      left: 50%%;
      top: 50%%;
      width: 12px;
      height: 12px;
      margin-left: -6px;
      margin-top: -6px;
      border-radius: 50%%;
      background: #f97316;
      box-shadow: 0 0 24px rgba(249, 115, 22, 0.7);
      animation: pulse 1.4s ease-in-out infinite;
    }
    .boot-sweep {
      position: absolute;
      inset: -12%%;
      border-radius: 50%%;
      background: conic-gradient(from 210deg, transparent 0deg, rgba(56, 189, 248, 0.04) 235deg, rgba(56, 189, 248, 0.38) 284deg, rgba(56, 189, 248, 0.04) 312deg, transparent 360deg);
      animation: sweep 2.2s linear infinite;
    }
    .boot-title { font-size: 1.45rem; font-weight: 700; letter-spacing: 0.04em; }
    .boot-subtle { color: rgba(255, 255, 255, 0.64); font-size: 0.92rem; }
    .boot-dots { display: inline-flex; gap: 6px; align-items: center; justify-content: center; }
    .boot-dots span {
      width: 8px;
      height: 8px;
      border-radius: 50%%;
      background: rgba(249, 115, 22, 0.42);
      animation: dot 1s ease-in-out infinite;
    }
    .boot-dots span:nth-child(2) { animation-delay: 120ms; }
    .boot-dots span:nth-child(3) { animation-delay: 240ms; }
    @keyframes sweep { from { transform: rotate(0deg); } to { transform: rotate(360deg); } }
    @keyframes pulse { 0%%, 100%% { transform: scale(0.92); opacity: 0.78; } 50%% { transform: scale(1.18); opacity: 1; } }
    @keyframes dot { 0%%, 100%% { transform: translateY(0); opacity: 0.35; } 50%% { transform: translateY(-6px); opacity: 1; } }
  </style>
</head>
<body>
  <div class="boot-shell">
    <div class="boot-radar" aria-hidden="true">
      <div class="boot-sweep"></div>
    </div>
    <div>
      <div class="boot-title">%s</div>
      <div class="boot-subtle">Loading your local trend radar</div>
    </div>
    <div class="boot-dots" aria-hidden="true">
      <span></span>
      <span></span>
      <span></span>
    </div>
  </div>
  <script>
    window.addEventListener("load", function () {
      window.setTimeout(function () {
        window.location.replace(%q);
      }, 120);
    });
  </script>
</body>
</html>`, appTitle, appTitle, targetURL)
}
