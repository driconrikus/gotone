//go:build !windows

package main

import (
	"os"
	"os/signal"
	"syscall"

	"github.com/rvaldez/gotone/tui"
)

func watchSignals(ui *tui.TUI, onStop func()) {
	sigCh := make(chan os.Signal, 1)
	signal.Notify(sigCh, syscall.SIGINT, syscall.SIGTERM, syscall.SIGWINCH)
	go func() {
		for sig := range sigCh {
			switch sig {
			case syscall.SIGWINCH:
				ui.NotifyResize()
			default:
				onStop()
				return
			}
		}
	}()
}
