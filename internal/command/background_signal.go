package command

import (
	"errors"
	"fmt"
	"io"
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
			if err := reportBackgroundShutdown(manager.Close(), os.Stderr); err != nil {
				os.Exit(1)
			}
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

// Return both cleanup and diagnostic write failures to the runtime.
func reportBackgroundShutdown(cleanupErr error, output io.Writer) error {
	if cleanupErr == nil {
		return nil
	}
	message := fmt.Sprintf("background command shutdown failed: %v\n", cleanupErr)
	written, writeErr := io.WriteString(output, message)
	if writeErr == nil && written != len(message) {
		writeErr = io.ErrShortWrite
	}
	return errors.Join(cleanupErr, writeErr)
}
