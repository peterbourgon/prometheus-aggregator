package main

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-kit/kit/log"
)

func TestSocketWrites(t *testing.T) {
	// Set up our little universe.
	var (
		dst, _ = newUniverse()
		src, w = io.Pipe()
		strict = true
		logger = log.NewNopLogger()
	)

	// Take writes from the output of the pipe into the universe.
	done := make(chan struct{})
	go func() {
		defer close(done)
		handleSocketWrites(src, dst, strict, logger)
	}()

	// Make writes to the input of the pipe.
	fmt.Fprintln(w, `{"name":"foo","type":"counter","help":"Total foos.","labels":{"code":"412"},"value":1}`)
	fmt.Fprintln(w, `{"name":"foo","labels":{"code":"412"},"value":2}`)
	fmt.Fprintln(w, `foo{code="412"} 4`)

	// Close the pipe, and wait for the handleDirectWrites goroutine to exit.
	w.Close()
	<-done

	// Dump the Prometheus /metrics output, and verify it.
	rec := httptest.NewRecorder()
	dst.ServeHTTP(rec, &http.Request{})
	if want, have := normalizeResponse(`
		# HELP foo Total foos.
		# TYPE foo counter
		foo{code="412"} 7.000000
	`), normalizeResponse(rec.Body.String()); want != have {
		t.Fatalf("\n---WANT---\n%s\n\n---HAVE---\n%s\n", want, have)
	}
}
