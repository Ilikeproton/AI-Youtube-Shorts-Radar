//go:build windows

package main

import "github.com/lxn/win"

func hideConsoleWindow() {
	hwnd := win.GetConsoleWindow()
	if hwnd != 0 {
		win.ShowWindow(hwnd, win.SW_HIDE)
	}
}
