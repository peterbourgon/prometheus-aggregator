package main

import (
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/go-kit/kit/log"
)

func TestDirectWrites(t *testing.T) {
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
		handleDirectWrites(src, dst, strict, logger)
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

func TestHTTPWrites(t *testing.T) {
	// Set up our little universe.
	var (
		dst, _ = newUniverse()
		strict = true
		logger = log.NewNopLogger()
	)

	// Create an HTTP server with our Transfer-Encoding: chunked handler.
	server := httptest.NewServer(handlePostWrites(dst, strict, logger))
	defer server.Close()

	// Create an HTTP request whose body is the output of a pipe.
	// We're trying to emulate what discourse/prometheus_exporter will do.
	// https://github.com/discourse/prometheus_exporter/blob/ec92e62/lib/prometheus_exporter/client.rb#L160-L167
	r, w := io.Pipe()
	req, _ := http.NewRequest("POST", server.URL, r)
	req.Header.Set("Transfer-Encoding", "chunked")
	req.Header.Set("Content-Type", "application/octet-stream")

	// Execute the HTTP request, which will block until the pipe is closed.
	done := make(chan struct{})
	go func() {
		defer close(done)
		http.DefaultClient.Do(req)
	}()

	// Make writes to the input of the pipe.
	// Again, emulate what discourse/prometheus_exporter will do.
	fmt.Fprintln(w, `{"name":"foo","type":"counter","help":"Total foos.","keys":{"code":"412"},"value":1}`)
	fmt.Fprintln(w, `{"name":"foo","type":"counter","help":"Total foos.","keys":{"code":"412"},"value":2}`)
	fmt.Fprintln(w, `{"name":"foo","type":"counter","help":"Total foos.","keys":{"code":"412"},"value":3}`)

	// Close the pipe, and wait for our HTTP request to complete.
	w.Close()
	<-done

	// Dump the Prometheus /metrics output, and verify it.
	rec := httptest.NewRecorder()
	dst.ServeHTTP(rec, &http.Request{})
	if want, have := normalizeResponse(`
		# HELP foo Total foos.
		# TYPE foo counter
		foo{code="412"} 6.000000
	`), normalizeResponse(rec.Body.String()); want != have {
		t.Fatalf("\n---WANT---\n%s\n\n---HAVE---\n%s\n", want, have)
	}
}
