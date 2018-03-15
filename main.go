package main

import (
	"bufio"
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"io/ioutil"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"sort"
	"strings"
	"sync"
	"syscall"
	"time"

	"github.com/go-kit/kit/log"
	"github.com/go-kit/kit/log/level"
	"github.com/oklog/run"
	"github.com/pkg/errors"
)

func main() {
	var (
		inaddr   = flag.String("in", "tcp://127.0.0.1:8192", "listen for metric writes")
		outaddr  = flag.String("out", "tcp://127.0.0.1:8193/metrics", "listen for Prometheus scrapes")
		declfile = flag.String("declfile", "", "file containing JSON metric declarations")
		example  = flag.Bool("example", false, "print example declfile to stdout and return")
		debug    = flag.Bool("debug", false, "log debug information")
	)
	flag.Parse()

	if *example {
		buf, _ := json.MarshalIndent(exampleDecls, "", "    ")
		fmt.Fprintf(os.Stdout, "%s\n", buf)
		os.Exit(0)
	}

	var logger log.Logger
	{
		logger = log.NewLogfmtLogger(os.Stdout)
		loglevel := level.AllowInfo()
		if *debug {
			loglevel = level.AllowDebug()
		}
		logger = level.NewFilter(logger, loglevel)
	}

	var inln net.Listener
	{
		u, err := url.Parse(*inaddr)
		if err != nil {
			level.Error(logger).Log("in", *inaddr, "err", err)
			os.Exit(1)
		}
		inln, err = net.Listen(u.Scheme, u.Host)
		if err != nil {
			level.Error(logger).Log("in", *inaddr, "err", err)
			os.Exit(1)
		}
	}

	var outln net.Listener
	var path string
	{
		u, err := url.Parse(*outaddr)
		if err != nil {
			level.Error(logger).Log("out", *outaddr, "err", err)
			os.Exit(1)
		}
		outln, err = net.Listen(u.Scheme, u.Host)
		if err != nil {
			level.Error(logger).Log("out", *outaddr, "err", err)
			os.Exit(1)
		}
		path = u.Path
	}

	var initial []observation
	if *declfile != "" {
		buf, err := ioutil.ReadFile(*declfile)
		if err != nil {
			level.Error(logger).Log("err", err)
			os.Exit(1)
		}
		if err := json.Unmarshal(buf, &initial); err != nil {
			level.Error(logger).Log("err", err)
			os.Exit(1)
		}
	}

	var u *universe
	{
		var err error
		u, err = newUniverse(initial)
		if err != nil {
			level.Error(logger).Log("err", err)
			os.Exit(1)
		}
	}

	var g run.Group
	{
		g.Add(func() error {
			level.Info(logger).Log("listener", "user", "addr", inln.Addr().String())
			for {
				conn, err := inln.Accept()
				if err != nil {
					return err
				}
				connlogger := log.With(logger, "remote_addr", conn.RemoteAddr().String())
				go handleConn(conn, u, connlogger)
			}
		}, func(error) {
			if err := inln.Close(); err != nil {
				level.Error(logger).Log("err", err)
			}
		})
	}
	{
		mux := http.NewServeMux()
		mux.Handle(path, u)
		server := http.Server{Handler: mux}
		g.Add(func() error {
			level.Info(logger).Log("listener", "Prometheus", "addr", outln.Addr().String(), "path", path)
			return server.Serve(outln)
		}, func(error) {
			ctx, cancel := context.WithTimeout(context.Background(), time.Second)
			defer cancel()
			if err := server.Shutdown(ctx); err != nil {
				level.Error(logger).Log("err", err)
			}
		})
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

func handleConn(src io.Reader, dst interface{ observe(observation) error }, logger log.Logger) error {
	s := bufio.NewScanner(src)
	for s.Scan() {
		var o observation
		if err := json.Unmarshal(s.Bytes(), &o); err != nil {
			level.Error(logger).Log("line", "rejected", "err", errors.Wrap(err, "error unmarshaling line"))
			continue
		}
		if err := dst.observe(o); err != nil {
			level.Error(logger).Log("line", "rejected", "err", errors.Wrap(err, "error making observation"))
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
	// universe of all received observations by metric name.
	universe struct {
		mtx         sync.Mutex
		collections map[metricName]*timeseriesCollection
	}

	// metricName e.g. `http_requests_total`.
	metricName string

	// timeseriesCollection corresponds to one high order Prometheus metric.
	// It has multiple timeseriesValues uniquely identified by their labels.
	timeseriesCollection struct {
		typ     string
		help    string
		buckets []float64 // only used by histograms
		values  map[timeseriesKey]timeseriesValue
	}

	// timeseriesKey is universally unique e.g. `http_requests_total method="GET" status_code="200"`.
	timeseriesKey string

	// timeseriesValue is a set of observations for
	// a unique metric name and set of labels.
	timeseriesValue interface {
		metricName() metricName
		timeseriesKey() timeseriesKey
		observe(observation) error
		renderText() string
	}
)

func newUniverse(initial []observation) (*universe, error) {
	u := &universe{
		collections: map[metricName]*timeseriesCollection{},
	}
	for _, o := range initial {
		if err := u.declare(o); err != nil {
			return nil, errors.Wrap(err, "loading initial set of declarations")
		}
	}
	return u, nil
}

func (u *universe) ensure(o observation) (*timeseriesCollection, error) {
	n := o.metricName()
	if _, ok := u.collections[n]; !ok {
		c, err := newTimeseriesCollection(o.Type, o.Help, o.Buckets)
		if err != nil {
			return nil, errors.Wrap(err, "error creating new timeseries collection")
		}
		u.collections[n] = c
	}
	return u.collections[n], nil
}

func (u *universe) declare(o observation) error {
	u.mtx.Lock()
	defer u.mtx.Unlock()
	c, err := u.ensure(o)
	if err != nil {
		return errors.Wrap(err, "error ensuring timeseries collection")
	}
	return c.declare(o)
}

func (u *universe) observe(o observation) error {
	u.mtx.Lock()
	defer u.mtx.Unlock()
	c, err := u.ensure(o)
	if err != nil {
		return errors.Wrap(err, "error ensuring timeseries collection")
	}
	return c.observe(o)
}

func newTimeseriesCollection(typ, help string, buckets []float64) (*timeseriesCollection, error) {
	switch typ {
	case "counter", "gauge", "histogram":
	default:
		return nil, fmt.Errorf("invalid type '%s'", typ)
	}
	if help == "" {
		return nil, fmt.Errorf("help string cannot be empty")
	}
	return &timeseriesCollection{
		typ:     typ,
		help:    help,
		buckets: buckets,
		values:  map[timeseriesKey]timeseriesValue{},
	}, nil
}

func (c *timeseriesCollection) ensure(o observation) (timeseriesValue, error) {
	o.Type, o.Help, o.Buckets = c.typ, c.help, c.buckets // first writer wins
	k := o.timeseriesKey()
	if _, ok := c.values[k]; !ok {
		v, err := newTimeseriesValue(c.typ, o)
		if err != nil {
			return nil, errors.Wrap(err, "error creating new timeseries")
		}
		c.values[k] = v
	}
	return c.values[k], nil
}

func (c *timeseriesCollection) declare(o observation) error {
	_, err := c.ensure(o)
	return err
}

func (c *timeseriesCollection) observe(o observation) error {
	v, err := c.ensure(o)
	if err != nil {
		return errors.Wrap(err, "error ensuring timeseries in collection")
	}
	return v.observe(o)
}

func newTimeseriesValue(typ string, o observation) (timeseriesValue, error) {
	if o.Name == "" {
		return nil, fmt.Errorf("a new timeseries value requires a name")
	}
	switch typ {
	case "counter":
		return newCounter(o)
	case "gauge":
		return newGauge(o)
	case "histogram":
		return newHistogram(o)
	default:
		return nil, fmt.Errorf("invalid timeseries type '%s' (programmer error)", typ)
	}
}

//
//
//

func (u *universe) ServeHTTP(w http.ResponseWriter, r *http.Request) {
	u.mtx.Lock()
	defer u.mtx.Unlock()

	w.Header().Set("Content-Type", "text/plain; version=0.0.4")
	for _, n := range sortTimeseriesCollectionKeys(u.collections) {
		c := u.collections[n]
		fmt.Fprintf(w, "# TYPE %s %s\n", n, c.typ)
		fmt.Fprintf(w, "# HELP %s %s\n", n, c.help)
		for _, k := range sortTimeseriesValueKeys(c.values) {
			v := c.values[k]
			fmt.Fprintf(w, v.renderText())
		}
		fmt.Fprintln(w)
	}
}

func sortTimeseriesCollectionKeys(collections map[metricName]*timeseriesCollection) (keys []metricName) {
	keys = make([]metricName, 0, len(collections))
	for k := range collections {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i] < keys[j] })
	return keys
}

func sortTimeseriesValueKeys(values map[timeseriesKey]timeseriesValue) (keys []timeseriesKey) {
	keys = make([]timeseriesKey, 0, len(values))
	for k := range values {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i] < keys[j] })
	return keys
}

//
//
//

type observation struct {
	Op      string            `json:"op,omitempty"`
	Name    string            `json:"name"`
	Type    string            `json:"type"`
	Help    string            `json:"help"`
	Buckets []float64         `json:"buckets,omitempty"`
	Labels  map[string]string `json:"labels,omitempty"`
	Value   float64           `json:"value,omitempty"`
}

func (o observation) metricName() metricName       { return metricName(o.Name) }
func (o observation) timeseriesKey() timeseriesKey { return makeTimeseriesKey(o.Name, o.Labels) }

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
	return &counter{
		n:      o.Name,
		h:      o.Help,
		labels: o.Labels,
	}, nil
}

func (c *counter) metricName() metricName       { return metricName(c.n) }
func (c *counter) timeseriesKey() timeseriesKey { return makeTimeseriesKey(c.n, c.labels) }

func (c *counter) observe(o observation) error {
	c.value += o.Value
	return nil
}

func (c *counter) renderText() string {
	return fmt.Sprintf("%s%s %f\n", c.n, renderLabels(c.labels), c.value)
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
	return &gauge{
		n:      o.Name,
		h:      o.Help,
		labels: o.Labels,
	}, nil
}

func (g *gauge) metricName() metricName       { return metricName(g.n) }
func (g *gauge) timeseriesKey() timeseriesKey { return makeTimeseriesKey(g.n, g.labels) }

func (g *gauge) observe(o observation) error {
	switch o.Op {
	case "add":
		g.value += o.Value
	default:
		g.value = o.Value
	}
	return nil
}

func (g *gauge) renderText() string {
	return fmt.Sprintf("%s%s %f\n", g.n, renderLabels(g.labels), g.value)
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
	buckets := make([]bucket, len(o.Buckets))
	for i, v := range o.Buckets {
		buckets[i] = bucket{max: v}
	}
	return &histogram{
		n:       o.Name,
		h:       o.Help,
		labels:  o.Labels,
		buckets: buckets,
	}, nil
}

func (h *histogram) metricName() metricName       { return metricName(h.n) }
func (h *histogram) timeseriesKey() timeseriesKey { return makeTimeseriesKey(h.n, h.labels) }

func (h *histogram) observe(o observation) error {
	h.sum += o.Value
	h.count++
	for i := range h.buckets {
		if o.Value <= h.buckets[i].max {
			h.buckets[i].count++
		}
	}
	return nil
}

func (h *histogram) renderText() string {
	var sb strings.Builder
	{
		// Render all of the individual buckets,
		// including a terminal +Inf bucket.
		labels := h.labels
		if labels == nil {
			labels = map[string]string{}
		}
		for _, b := range h.buckets {
			labels["le"] = fmt.Sprint(b.max)
			fmt.Fprintf(&sb, "%s%s %d\n", h.n, renderLabels(labels), b.count)
		}
		labels["le"] = "+Inf"
		fmt.Fprintf(&sb, "%s%s %d\n", h.n, renderLabels(labels), h.count)
	}
	{
		// Render the aggregate statistics.
		fmt.Fprintf(&sb, "%s_sum%s %f\n", h.n, renderLabels(h.labels), h.sum)
		fmt.Fprintf(&sb, "%s_count%s %d\n", h.n, renderLabels(h.labels), h.count)
	}
	return sb.String()
}

//
//
//

func makeTimeseriesKey(name string, labels map[string]string) timeseriesKey {
	return timeseriesKey(name + " " + renderLabels(labels))
}

func renderLabels(labels map[string]string) string {
	parts := make([]string, len(labels))
	for i, k := range sortLabelKeys(labels) {
		parts[i] = fmt.Sprintf(`%s="%s"`, k, labels[k])
	}
	return "{" + strings.Join(parts, ",") + "}"
}

func sortLabelKeys(labels map[string]string) (keys []string) {
	keys = make([]string, 0, len(labels))
	for k := range labels {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	return keys
}

//
//
//

var exampleDecls = []observation{
	{
		Name: "myservice_jobs_processed_total",
		Type: "counter",
		Help: "Total number of jobs processed.",
	},
	{
		Name: "myservice_cache_size_bytes",
		Type: "gauge",
		Help: "Current size of cache in bytes.",
	},
	{
		Name:    "myservice_http_request_duration_seconds",
		Type:    "histogram",
		Help:    "HTTP request duraton in seconds.",
		Buckets: []float64{.005, .01, .025, .05, .1, .25, .5, 1, 2.5, 5, 10},
	},
}
