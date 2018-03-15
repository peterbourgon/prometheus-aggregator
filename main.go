package main

import (
	"bufio"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net"
	"os"
	"os/signal"
	"sort"
	"strings"
	"syscall"

	"github.com/go-kit/kit/log"
	"github.com/go-kit/kit/log/level"
	"github.com/oklog/run"
)

func main() {
	var (
		netw    = flag.String("netw", "tcp", "listen network")
		addr    = flag.String("addr", "127.0.0.1:8192", "listen address")
		metrics = flag.String("metrics", "127.0.0.1:8193", "/metrics address")
		debug   = flag.Bool("debug", false, "log debug information")
	)
	flag.Parse()

	var logger log.Logger
	{
		logger = log.NewLogfmtLogger(os.Stdout)
		loglevel := level.AllowInfo()
		if *debug {
			loglevel = level.AllowDebug()
		}
		logger = level.NewFilter(logger, loglevel)
	}

	var ln net.Listener
	{
		var err error
		ln, err = net.Listen(*netw, *addr)
		if err != nil {
			level.Error(logger).Log("err", err)
			os.Exit(1)
		}
		level.Info(logger).Log("network", *netw, "address", *addr)
	}

	var vs vectorspace
	{
		vs = vectorspace{}
		// TODO(pb): initialize via config file
	}

	var g run.Group
	{
		g.Add(func() error {
			for {
				conn, err := ln.Accept()
				if err != nil {
					return err
				}
				connlogger := log.With(logger, "remote_addr", conn.RemoteAddr().String())
				go handle(conn, vs, connlogger)
			}
		}, func(error) {
			if err := ln.Close(); err != nil {
				level.Error(logger).Log("err", err)
			}
		})
	}
	{
		// TODO(pb): /metrics endpoint
	}
	{
		ctx, cancel := context.WithCancel(context.Background())
		g.Add(func() error {
			c := make(chan os.Signal, 1)
			signal.Notify(c, syscall.SIGINT, syscall.SIGTERM)
			select {
			case sig := <-c:
				return fmt.Errorf("received signal %s", sig)
			case <-ctx.Done():
				return ctx.Err()
			}
		}, func(error) {
			cancel()
		})
	}
	level.Info(logger).Log("exit", g.Run())
}

//
//
//

func handle(src io.Reader, dst vectorspace, logger log.Logger) error {
	var (
		s = bufio.NewScanner(src)
		o observation
	)
	for s.Scan() {
		if err := json.Unmarshal(s.Bytes(), &o); err != nil {
			level.Error(logger).Log("line", "rejected", "err", err)
			continue
		}
		if err := dst.observe(o); err != nil {
			level.Error(logger).Log("line", "rejected", "err", err)
			continue
		}
		level.Debug(logger).Log("line", "accepted", "name", o.Name)
	}
	return s.Err()
}

//
//
//

type (
	metricname string // e.g. `http_requests_total`
	metrickey  string // e.g. `http_requests_total method="GET" status_code="200"`
)

type vectorspace map[metricname]metricspace

func (vs vectorspace) observe(o observation) error {
	if _, ok := vs[o.name()]; !ok {
		vs[o.name()] = metricspace{}
	}
	return vs[o.name()].observe(o)
}

type metricspace map[metrickey]metric

func (ms metricspace) observe(o observation) error {
	if _, ok := ms[o.key()]; !ok {
		m, err := newMetric(o)
		if err != nil {
			return err
		}
		ms[o.key()] = m
	}
	return ms[o.key()].observe(o)
}

//
//
//

type observation struct {
	Op      string            `json:"op"`
	Type    string            `json:"type"`
	Name    string            `json:"name"`
	Help    string            `json:"help"`
	Buckets []float64         `json:"buckets"`
	Labels  map[string]string `json:"labels"`
	Value   float64           `json:"value"`
}

func (o observation) name() metricname { return metricname(o.Name) }
func (o observation) key() metrickey   { return makeKey(o.Name, o.Labels) }

//
//
//

type metric interface {
	name() metricname
	key() metrickey
	typ() string
	help() string
	observe(observation) error
	render() string
}

func newMetric(o observation) (metric, error) {
	if o.Name == "" {
		return nil, fmt.Errorf("observation requires name")
	}
	switch o.Type {
	case "":
		return nil, fmt.Errorf("%s: type unspecified", o.Name)
	case "counter":
		return newCounter(o)
	case "gauge":
		return newGauge(o)
	case "histogram":
		return newHistogram(o)
	default:
		return nil, fmt.Errorf("%s: unsupported type %s", o.Name, o.Type)
	}
}

func headerFor(m metric) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "# HELP %s %s\n", m.name(), m.help())
	fmt.Fprintf(&sb, "# TYPE %s %s\n", m.name(), m.typ())
	return sb.String()
}

//
//
//

type counter struct {
	n      string
	h      string
	labels map[string]string
	value  float64
}

func newCounter(o observation) (*counter, error) {
	// TODO(pb): validation
	return &counter{
		n:      o.Name,
		h:      o.Help,
		labels: o.Labels,
	}, nil
}

func (c *counter) name() metricname { return metricname(c.n) }
func (c *counter) key() metrickey   { return makeKey(c.n, c.labels) }
func (c *counter) typ() string      { return "counter" }
func (c *counter) help() string     { return c.h }

func (c *counter) observe(o observation) error {
	c.value += o.Value
	return nil
}

func (c *counter) render() string {
	return fmt.Sprintf("%s%s %f\n", c.name(), renderLabels(c.labels), c.value)
}

//
//
//

type gauge struct {
	n      string
	h      string
	labels map[string]string
	value  float64
}

func newGauge(o observation) (*gauge, error) {
	// TODO(pb): validation
	return &gauge{
		n:      o.Name,
		h:      o.Help,
		labels: o.Labels,
	}, nil
}

func (g *gauge) name() metricname { return metricname(g.n) }
func (g *gauge) key() metrickey   { return makeKey(g.n, g.labels) }
func (g *gauge) typ() string      { return "gauge" }
func (g *gauge) help() string     { return g.h }

func (g *gauge) observe(o observation) error {
	switch o.Op {
	case "add":
		g.value += o.Value
	case "set":
		g.value = o.Value
	default:
		return fmt.Errorf("unsupported gauge operation %q", o.Op)
	}
	return nil
}

func (g *gauge) render() string {
	return fmt.Sprintf("%s%s %f\n", g.name(), renderLabels(g.labels), g.value)
}

//
//
//

type histogram struct {
	n       string
	h       string
	labels  map[string]string
	sum     float64
	count   uint64
	buckets []bucket
}

type bucket struct {
	max   float64
	count uint64
}

func newHistogram(o observation) (*histogram, error) {
	// TODO(pb): validation
	return &histogram{
		n:       o.Name,
		h:       o.Help,
		labels:  o.Labels,
		buckets: makeBuckets(o.Buckets),
	}, nil
}

func (h *histogram) name() metricname { return metricname(h.n) }
func (h *histogram) key() metrickey   { return makeKey(h.n, h.labels) }
func (h *histogram) typ() string      { return "histogram" }
func (h *histogram) help() string     { return h.h }

func (h *histogram) observe(o observation) error {
	h.sum += o.Value
	h.count++
	for i := range h.buckets {
		if o.Value > h.buckets[i].max {
			break
		}
		h.buckets[i].count++
	}
	return nil
}

func (h *histogram) render() string {
	var sb strings.Builder
	labels := h.labels
	for _, b := range h.buckets {
		labels["le"] = fmt.Sprint(b.max)
		fmt.Fprintf(&sb, "%s%s %d\n", h.name(), renderLabels(labels), b.count)
	}
	labels["le"] = "+Inf"
	fmt.Fprintf(&sb, "%s_sum%s %f\n", h.name(), renderLabels(labels), h.sum)
	fmt.Fprintf(&sb, "%s_count%s %d\n", h.name(), renderLabels(labels), h.count)
	return sb.String()
}

func makeBuckets(maxes []float64) []bucket {
	buckets := make([]bucket, len(maxes))
	for i, v := range maxes {
		buckets[i] = bucket{max: v}
	}
	return buckets
}

//
//
//

func makeKey(name string, labels map[string]string) metrickey {
	parts := make([]string, 1+len(labels))
	parts[0] = name
	for i, k := range sortKeys(labels) {
		parts[i+1] = fmt.Sprintf(`%s="%s"`, k, labels[k])
	}
	return metrickey(strings.Join(parts, " "))
}

func sortKeys(labels map[string]string) (keys []string) {
	keys = make([]string, 0, len(labels))
	for k := range labels {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

func renderLabels(labels map[string]string) string {
	parts := make([]string, len(labels))
	for i, k := range sortKeys(labels) {
		parts[i] = fmt.Sprintf(`%s="%s"`, k, labels[k])
	}
	return "{" + strings.Join(parts, ",") + "}"
}
