package command

import (
	"os"
	"os/signal"
	"syscall"
)

// CloseOnSignal stops the manager's processes before the runtime terminates on
// an interrupt or termination signal. The returned function unregisters it.
func (manager BackgroundCommandManager) CloseOnSignal() func() {
	signals := make(chan os.Signal, 1)
	done := make(chan struct{})
	signal.Notify(signals, os.Interrupt, syscall.SIGTERM)
	go func() {
		select {
		case received := <-signals:
			_ = manager.Close()
			signal.Stop(signals)
			if received == os.Interrupt {
				os.Exit(130)
			}
			os.Exit(143)
		case <-done:
		}
	}()
	return func() {
		signal.Stop(signals)
		close(done)
	}
}
