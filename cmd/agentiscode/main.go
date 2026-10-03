package main

import (
	"context"
	"os"
	"os/signal"
	"sync/atomic"
	"syscall"

	"agentiscode"
)

func main() { os.Exit(run()) }
func run() int {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	signals := make(chan os.Signal, 1)
	signal.Notify(signals, os.Interrupt, syscall.SIGTERM)
	defer signal.Stop(signals)
	var interrupted atomic.Int32
	go func() {
		select {
		case sig := <-signals:
			code := int32(130)
			if sig == syscall.SIGTERM {
				code = 143
			}
			interrupted.Store(code)
			cancel()
		case <-ctx.Done():
		}
	}()
	code := agentiscode.RunCLI(ctx, os.Args[1:], os.Stdin, os.Stdout, os.Stderr, os.Getenv)
	if signalCode := interrupted.Load(); signalCode != 0 {
		return int(signalCode)
	}
	return code
}
