package app

import (
	"errors"
	"io"
	"os"
	"strings"
)

func characterDevice(value any) bool {
	file, ok := value.(*os.File)
	if !ok {
		return false
	}
	info, err := file.Stat()
	return err == nil && info.Mode()&os.ModeCharDevice != 0
}

func (r *runner) interactiveScreenEnabled() bool {
	if r == nil || strings.TrimSpace(os.Getenv("GETBEE_NO_CLEAR")) != "" || strings.EqualFold(strings.TrimSpace(os.Getenv("TERM")), "dumb") {
		return false
	}
	return characterDevice(r.in) && characterDevice(r.out)
}

func (r *runner) redrawInteractiveScreen() {
	if !r.interactiveScreenEnabled() {
		return
	}
	// Windows Terminal, modern PowerShell, and Unix terminals all understand
	// the ANSI erase-display and cursor-home sequence.
	_, _ = io.WriteString(r.out, "\x1b[2J\x1b[H")
	r.logoShown = false
	r.showLogo()
}

func (r *runner) pauseBeforeHome() {
	if !r.interactiveScreenEnabled() {
		return
	}
	_, err := r.askLocalized("\n按回车继续…", "\nPress Enter to continue…")
	if err != nil && !errors.Is(err, io.EOF) {
		return
	}
}
