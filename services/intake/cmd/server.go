package main

import (
	"context"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"sync"
	"time"
)

// serveWebhook keeps receipt delivery independent of request handling. Queue
// relay remains a separate process, just as it is for account polling.
func serveWebhook(ctx context.Context, address string, handler http.Handler, acknowledge func(context.Context) error) error {
	listener, err := net.Listen("tcp", address)
	if err != nil {
		return errors.New("open webhook listener")
	}
	workerContext, cancel := context.WithCancel(ctx)
	defer cancel()
	server := &http.Server{Handler: handler, ReadHeaderTimeout: 5 * time.Second, ReadTimeout: 10 * time.Second, WriteTimeout: 10 * time.Second, IdleTimeout: time.Minute, MaxHeaderBytes: 16 << 10, BaseContext: func(net.Listener) context.Context { return workerContext }}
	var workers sync.WaitGroup
	workers.Go(func() {
		for {
			pass, stop := context.WithTimeout(workerContext, time.Minute)
			err := acknowledge(pass)
			stop()
			if err != nil && workerContext.Err() == nil {
				fmt.Fprintln(os.Stderr, "receipt delivery failed:", err)
			}
			timer := time.NewTimer(time.Minute)
			select {
			case <-workerContext.Done():
				timer.Stop()
				return
			case <-timer.C:
			}
		}
	})
	workers.Go(func() {
		<-workerContext.Done()
		shutdown, stop := context.WithTimeout(context.Background(), 10*time.Second)
		defer stop()
		if err := server.Shutdown(shutdown); err != nil {
			server.Close()
		}
	})
	err = server.Serve(listener)
	cancel()
	workers.Wait()
	if errors.Is(err, http.ErrServerClosed) {
		return nil
	}
	return err
}
