package tools

import (
	"context"
	"log"
	"os"
	"os/signal"
	"syscall"
)

var (
	ShutdownContext context.Context
)

func init() {
	c := make(chan os.Signal, 1)
	signal.Notify(c, syscall.SIGINT, syscall.SIGTERM)

	bgctx, cancel := context.WithCancel(context.Background())
	go func() {
		oscall := <-c
		log.Printf("system call: %+v", oscall)
		cancel()
	}()

	ShutdownContext = bgctx
}
