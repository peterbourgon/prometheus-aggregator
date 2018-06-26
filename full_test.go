package main

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func TestThreeTypes(t *testing.T) {
	u, _ := newUniverse()
	loadObservations(t, u, makeObservations(t, []string{
		`{"name":"foo_total","type":"counter","help":"Total number of foos."}`,
		`{"name":"foo_total","labels":{"code":"200"},"value": 1}`,
		`{"name":"foo_total","labels":{"code":"404"},"value": 2}`,
		`foo_total{code="200"} 4`,
		`foo_total{code="404"} 8`,

		`{"name":"bar_seconds","type":"histogram","help":"Bar duration in seconds.","buckets":[0.01, 0.05, 0.1, 0.5, 1, 2, 5, 10]}`,
		`{"name":"bar_seconds","value":0.123}`,
		`{"name":"bar_seconds","value":0.234}`,
		`{"name":"bar_seconds","value":0.501}`,
		`{"name":"bar_seconds","value":8.000}`,

		`{"name":"baz_size","type":"gauge","help":"Current size of baz widget."}`,
		`{"name":"baz_size","value": 1}`,
		`{"name":"baz_size","value": 2}`,
		`baz_size{} 4`,
	}))
	if want, have := normalizeResponse(`
		# HELP bar_seconds Bar duration in seconds.
		# TYPE bar_seconds histogram
		bar_seconds{le="0.01"} 0
		bar_seconds{le="0.05"} 0
		bar_seconds{le="0.1"} 0
		bar_seconds{le="0.5"} 2
		bar_seconds{le="1"} 3
		bar_seconds{le="2"} 3
		bar_seconds{le="5"} 3
		bar_seconds{le="10"} 4
		bar_seconds{le="+Inf"} 4
		bar_seconds_sum{} 8.858000
		bar_seconds_count{} 4
		
		# HELP baz_size Current size of baz widget.
		# TYPE baz_size gauge
		baz_size{} 4.000000
		
		# HELP foo_total Total number of foos.
		# TYPE foo_total counter
		foo_total{code="200"} 5.000000
		foo_total{code="404"} 10.000000
	`), normalizeResponse(scrape(t, u)); want != have {
		t.Fatalf("\n---WANT---\n%s\n\n---HAVE---\n%s\n", want, have)
	}
}

func TestInitialDeclarations(t *testing.T) {
	u, _ := newUniverse(makeObservations(t, []string{
		`{"name":"foo_total","type":"counter","help":"Total number of foos."}`,
		`{"name":"bar_seconds","type":"histogram","help":"Bar duration in seconds.","buckets":[0.01, 0.05, 0.1, 0.5, 1, 2, 5, 10]}`,
		`{"name":"baz_size","type":"gauge","help":"Current size of baz widget."}`,
		`{"name":"qux_count","type":"counter","help":"Count of qux events."}`,
	})...)
	loadObservations(t, u, makeObservations(t, []string{
		`foo_total{label="value"} 1`,
		`bar_seconds{} 0.234`,
		`baz_size{} 5`,
	}))
	if want, have := normalizeResponse(`
		# HELP bar_seconds Bar duration in seconds.
		# TYPE bar_seconds histogram
		bar_seconds{le="0.01"} 0
		bar_seconds{le="0.05"} 0
		bar_seconds{le="0.1"} 0
		bar_seconds{le="0.5"} 1
		bar_seconds{le="1"} 1
		bar_seconds{le="2"} 1
		bar_seconds{le="5"} 1
		bar_seconds{le="10"} 1
		bar_seconds{le="+Inf"} 1
		bar_seconds_sum{} 0.234000
		bar_seconds_count{} 1
		
		# HELP baz_size Current size of baz widget.
		# TYPE baz_size gauge
		baz_size{} 5.000000
		
		# HELP foo_total Total number of foos.
		# TYPE foo_total counter
		foo_total{label="value"} 1.000000
	`), normalizeResponse(scrape(t, u)); want != have {
		t.Fatalf("\n---WANT---\n%s\n\n---HAVE---\n%s\n", want, have)
	}
}

func makeObservations(t *testing.T, lines []string) []observation {
	t.Helper()
	observations := make([]observation, len(lines))
	for i, s := range lines {
		o, err := parseLine([]byte(s))
		if err != nil {
			t.Fatal(err)
		}
		observations[i] = o
	}
	return observations
}

func loadObservations(t *testing.T, obs observer, observations []observation) {
	t.Helper()
	for _, o := range observations {
		if err := obs.observe(o); err != nil {
			t.Fatalf("%+v: %v", o, err)
		}
	}
}

func scrape(t *testing.T, h http.Handler) string {
	rec := httptest.NewRecorder()
	req, _ := http.NewRequest("GET", "/", nil)
	h.ServeHTTP(rec, req)
	return rec.Body.String()
}

func normalizeResponse(s string) string {
	lines := strings.Split(s, "\n")
	for i := range lines {
		lines[i] = strings.TrimSpace(lines[i])
	}
	return strings.TrimSpace(strings.Join(lines, "\n"))
}
