package tools

import (
	"context"
	"log"
	"os"
	"os/signal"
	"syscall"
	"time"
)

type noTimeout struct {
	ctx context.Context
}

func (c noTimeout) Deadline() (time.Time, bool)       { return time.Time{}, false }
func (c noTimeout) Done() <-chan struct{}             { return nil }
func (c noTimeout) Err() error                        { return nil }
func (c noTimeout) Value(key interface{}) interface{} { return c.ctx.Value(key) }

// WithoutCancel returns a context that is never canceled.
func WithoutTimeout(ctx context.Context) context.Context {
	return noTimeout{ctx: ctx}
}

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
