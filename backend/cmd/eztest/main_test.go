package main

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

// TestBaseContextCancellationUnblocksRequestContextHandler proves the shutdown
// contract for long-lived handlers such as the SSE streams: a handler that only
// waits on its request context returns as soon as the server's base context is
// canceled. Without this, srv.Shutdown would wait for every open SSE client
// until its timeout and the process would exit non-zero.
func TestBaseContextCancellationUnblocksRequestContextHandler(t *testing.T) {
	base, cancelBase := context.WithCancel(context.Background())
	defer cancelBase()

	handlerStarted := make(chan struct{})
	handlerReturned := make(chan struct{})
	srv := httptest.NewUnstartedServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		close(handlerStarted)
		<-r.Context().Done()
		close(handlerReturned)
	}))
	srv.Config.BaseContext = baseContext(base)
	srv.Start()
	t.Cleanup(srv.Close)

	requestDone := make(chan struct{})
	go func() {
		resp, err := http.Get(srv.URL)
		if err == nil {
			_ = resp.Body.Close()
		}
		close(requestDone)
	}()

	select {
	case <-handlerStarted:
	case <-time.After(2 * time.Second):
		t.Fatal("handler did not start")
	}

	cancelBase()
	select {
	case <-handlerReturned:
	case <-time.After(2 * time.Second):
		t.Fatal("handler blocked on the request context did not return after the base context was canceled")
	}
	select {
	case <-requestDone:
	case <-time.After(2 * time.Second):
		t.Fatal("client request did not finish after the base context was canceled")
	}
}
