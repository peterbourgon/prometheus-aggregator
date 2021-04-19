# Prometheus metrics aggregator [![Latest Release](https://img.shields.io/github/release/peterbourgon/prometheus-aggregator.svg?style=flat-square)](https://github.com/peterbourgon/prometheus-aggregator/releases/latest) [![Build Status](https://img.shields.io/endpoint.svg?url=https%3A%2F%2Factions-badge.atrox.dev%2Fpeterbourgon%2Fprometheus-aggregator%2Fbadge%3Fref%3Dmain&style=flat)](https://actions-badge.atrox.dev/peterbourgon/prometheus-aggregator/goto?ref=main)

Receive and aggregate metrics for consumption by a Prometheus server.

**DON'T USE THIS TOOL**. If at all possible, you should expose a /metrics
endpoint in your service and have Prometheus scrape it directly. If you're
running a cron job, prefer [the pushgateway][pushgateway]. This tool only exists
to help with edge case scenarios, for example Perl web services that use a
[Unicorn][unicorn]-style forked process model to handle concurrency, and are
difficult or impossible to get Prometheus to scrape.

Related work:

- [prometheus/pushgateway][pushgateway] doesn't do aggregation but works OK for things like batch or cron jobs
- [prometheus/statsd_exporter][statsd] accepts StatsD writes, but requires a big YAML config for mappings
- [weaveworks/prom-aggregation-gateway][pag] accepts HTTP POSTs from its corresponding [JavaScript client][jsc]
- [discourse/prometheus_exporter][dpe] is a Ruby solution with client gem, but no histograms

[pushgateway]: https://github.com/prometheus/pushgateway
[unicorn]: https://bogomips.org/unicorn/
[statsd]: https://github.com/prometheus/statsd_exporter/
[pag]: https://github.com/weaveworks/prom-aggregation-gateway/
[jsc]: https://github.com/weaveworks/promjs/
[dpe]: https://github.com/discourse/prometheus_exporter/

## Getting

[Download the latest release][release] if you're lazy, or build it yourself from
latest master if you have the Go toolchain installed and have YOLO tattooed on
your knuckles or whatever.

[release]: https://github.com/peterbourgon/prometheus-aggregator/releases/latest

```
go get github.com/peterbourgon/prometheus-aggregator
$GOPATH/bin/prometheus-aggregator -h
```

## Running

I mean you just kinda run it.

```
USAGE
  prometheus-aggregator [flags]

FLAGS
  -debug false                              log debug information
  -declfile ...                             file containing JSON metric declarations
  -declpath ...                             sibling path to /metrics serving declfile contents
  -example false                            print example declfile to stdout and return
  -prometheus tcp://127.0.0.1:8192/metrics  address for Prometheus scrapes
  -socket tcp://127.0.0.1:8191              address for direct socket metric writes
  -strict false                             disconnect clients when they send bad data

VERSION
  0.0.15
```

## How it works

The prometheus-aggregator expects clients to connect and emit newline-delimited
JSON objects for each metric observation. Each object needs to be fully
specified with name, type, help, and value. Here's an example of three counter
increments.

```
{"name": "myapp_foo_total", "type": "counter", "help": "Total number of foos.", "value": 1}
{"name": "myapp_foo_total", "type": "counter", "help": "Total number of foos.", "value": 1}
{"name": "myapp_foo_total", "type": "counter", "help": "Total number of foos.", "value": 1}
```

Obviously this is wildly inefficient, so, as an optimization, once a metric has
been "declared" with name, type, and help, subsequent emissions may refer to it
simply by name.

```
{"name": "myapp_foo_total", "type": "counter", "help": "Total number of foos.", "value": 1}
{"name": "myapp_foo_total", "value": 1}
{"name": "myapp_foo_total", "value": 1}
```

You can delare a metric without making an observation by omitting the value.

```
{"name": "myapp_foo_total", "type": "counter", "help": "Total number of foos."}
{"name": "myapp_foo_total", "value": 1}  # value is now 1
{"name": "myapp_foo_total", "value": 2}  # value is now 3
```

You can declare metrics at runtime, like this, or you can predeclare metrics in
a file containing a JSON array of multiple JSON objects, and pass it to the
program at startup via the `-declfile` flag. Or mix and match both! Life is
full of possibility.

New! Exciting! Great Value! An optional `-declpath` flag allows you to serve
your initial metric declarations on a sibling path to your Prometheus metrics
telemetry. This can be useful if you want to programmatically verify the state
of a prometheus-aggregator instance.

## Prometheus exposition format

If serializing JSON is a bottleneck, you can optionally emit observations (but
not declarations) in the [Prometheus exposition format][pef]. Note that the
parser (such as it is) is pretty strict, so don't get crazy with whitespace or
whatever.

[pef]: https://prometheus.io/docs/instrumenting/exposition_formats/

```
{"name": "myapp_foo_total", "type": "counter", "help": "Total number of foos."}
myapp_foo_total{} 1
myapp_foo_total{} 2
```

## Labels

Labels are supported in both formats as you might expect.

```
{"name": "myapp_foo_total", "labels": {"success": "false", "code": "401"}, "value": 1}
myapp_foo_total{success="true",code="200"} 1
```

## Supported types

Counters are obviously supported. Gauges are also supported and work just like
counters, but default to setting themselves to the most recent value.

```
{"name": "myapp_worker_pool", "type": "gauge", "help": "Size of worker pool."}
myapp_worker_pool{} 1  # value is now 1
myapp_worker_pool{} 2  # value is now 2
```

Histograms are supported too. Provide buckets with the declaration.

```
{"name": "myapp_req_dur_seconds", "type": "histogram",
  "help": "Duration of request in seconds.",
    "buckets": [0.01, 0.05, 0.1, 0.5, 1, 2, 5, 10]}
myapp_req_dur_seconds{} 0.0123
myapp_req_dur_seconds{} 0.99
```

**Summaries are not supported**. This is fine, you can't do meaningful
aggregation over summaries at query time anyway. You'll need to define some
buckets and I know that sounds hard, and it _is_ hard, life is hard, I'm sorry
for that.

## Bad data

By default, if a client sends bad data, the only thing that happens is the
prometheus-aggregator will log an error, the client won't know about it. This is
a good idea for production but maybe for dev you want to pass the `-strict`
flag, which means if a client sends bad data it gets disconnected!! Harsh!!

## UDP

You can specify a socket write address as e.g. `udp://127.0.0.1:8191` and then
you can emit UDP observations! The same rules apply, one metric per datagram.
The `-strict` flag has no meaning in this mode as UDP is connectionless.
