package main

import (
	"bytes"
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"

	"github.com/pkg/errors"
)

type (
	// universe of all received observations by metric name.
	// Note it has a (very) coarse-grained mutex, therefore
	// all subtypes (counter, etc.) are NOT goroutine-safe.
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

	// timeseriesKey is universally unique e.g. `http_requests_total{method="GET",status_code="200"}`.
	timeseriesKey string

	// timeseriesValue is a set of observations for
	// a unique metric name and set of labels.
	timeseriesValue interface {
		metricName() metricName
		timeseriesKey() timeseriesKey
		touched() bool
		observe(observation) error
		renderText() string
	}
)

func newUniverse(initial ...observation) (*universe, error) {
	u := &universe{
		collections: map[metricName]*timeseriesCollection{},
	}
	for _, o := range initial {
		if err := u.observe(o); err != nil {
			return nil, errors.Wrap(err, "error loading initial set of observations")
		}
	}
	return u, nil
}

func (u *universe) observe(o observation) error {
	u.mtx.Lock()
	defer u.mtx.Unlock()
	n := o.metricName()
	if _, ok := u.collections[n]; !ok {
		c, err := newTimeseriesCollection(o.Type, o.Help, o.Buckets)
		if err != nil {
			return errors.Wrap(err, "error creating new timeseries collection")
		}
		u.collections[n] = c
	}
	return u.collections[n].observe(o)
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

// touched should return true if any timeseries in the collection
// has been touched. It's used to determine if we should render
// the header stanza in the /metrics output.
func (c *timeseriesCollection) touched() bool {
	for _, v := range c.values {
		if v.touched() {
			return true
		}
	}
	return false
}

func (c *timeseriesCollection) observe(o observation) error {
	o.Type, o.Help, o.Buckets = c.typ, c.help, c.buckets // first writer wins
	k := o.timeseriesKey()
	if _, ok := c.values[k]; !ok {
		v, err := newTimeseriesValue(c.typ, o)
		if err != nil {
			return errors.Wrap(err, "error creating new timeseries")
		}
		c.values[k] = v
	}
	return c.values[k].observe(o)
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
	var buf bytes.Buffer
	{
		u.mtx.Lock()
		for _, n := range sortMetricNames(u.collections) {
			c := u.collections[n]
			if !c.touched() {
				continue
			}
			fmt.Fprintf(&buf, "# HELP %s %s\n", n, c.help)
			fmt.Fprintf(&buf, "# TYPE %s %s\n", n, c.typ)
			for _, k := range sortTimeseriesKeys(c.values) {
				v := c.values[k]
				if !v.touched() {
					continue
				}
				fmt.Fprintf(&buf, v.renderText())
			}
			fmt.Fprintln(&buf)
		}
		u.mtx.Unlock()
	}
	w.Header().Set("Content-Type", "text/plain; version=0.0.4")
	w.Write(buf.Bytes())
}

func sortMetricNames(collections map[metricName]*timeseriesCollection) (keys []metricName) {
	keys = make([]metricName, 0, len(collections))
	for k := range collections {
		keys = append(keys, k)
	}
	sort.Slice(keys, func(i, j int) bool { return keys[i] < keys[j] })
	return keys
}

func sortTimeseriesKeys(values map[timeseriesKey]timeseriesValue) (keys []timeseriesKey) {
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
	Name    string            `json:"name"`
	Type    string            `json:"type"`
	Help    string            `json:"help"`
	Buckets []float64         `json:"buckets,omitempty"`
	Labels  map[string]string `json:"labels,omitempty"`
	Op      string            `json:"op,omitempty"`
	Value   *float64          `json:"value,omitempty"`
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
	touch  bool
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
	if o.Value == nil {
		return nil // declaration
	}
	c.touch = true
	c.value += *o.Value
	return nil
}

func (c *counter) touched() bool { return c.touch }

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
	touch  bool
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
	if o.Value == nil {
		return nil // declaration
	}
	switch o.Op {
	case "add":
		g.value += *o.Value
	default:
		g.value = *o.Value
	}
	g.touch = true
	return nil
}

func (g *gauge) touched() bool { return g.touch }

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
	if o.Value == nil {
		return nil // declaration
	}
	h.sum += *o.Value
	h.count++
	for i := range h.buckets {
		if *o.Value <= h.buckets[i].max {
			h.buckets[i].count++
		}
	}
	return nil
}

func (h *histogram) touched() bool { return h.count > 0 }

func (h *histogram) renderText() string {
	var sb strings.Builder
	{
		// Render all of the individual buckets,
		// including a terminal +Inf bucket.
		labelscopy := map[string]string{}
		for k, v := range h.labels {
			labelscopy[k] = v
		}
		for _, b := range h.buckets {
			labelscopy["le"] = fmt.Sprint(b.max)
			fmt.Fprintf(&sb, "%s_bucket%s %d\n", h.n, renderLabels(labelscopy), b.count)
		}
		labelscopy["le"] = "+Inf"
		fmt.Fprintf(&sb, "%s_bucket%s %d\n", h.n, renderLabels(labelscopy), h.count)
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
