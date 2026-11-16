package tools

import (
	"context"
	"log"
	"os"
	"os/signal"
	"syscall"
)

// ShutdownContext ends when CancelOnShutdownSignal observes SIGINT or SIGTERM.
var ShutdownContext, cancelShutdown = context.WithCancel(context.Background())

// CancelOnShutdownSignal turns SIGINT and SIGTERM into a graceful shutdown of ShutdownContext.
// Only long-running programs call it; every other importer keeps the default signal behavior.
func CancelOnShutdownSignal() {
	c := make(chan os.Signal, 1)
	signal.Notify(c, syscall.SIGINT, syscall.SIGTERM)
	go func() {
		oscall := <-c
		log.Printf("system call: %+v", oscall)
		cancelShutdown()
	}()
}
