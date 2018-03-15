# Prometheus metrics aggregator

Receive and aggregate metrics for consumption by a Prometheus server.

Why should you use this tool? **You shouldn't**. If at all possible, you should
expose a /metrics endpoint in your service and have Prometheus scrape it
directly. If you're running a cron job, prefer [the pushgateway][pushgateway].
This tool only exists to help with edge case scenarios, like Perl web services
that use a [Unicorn][unicorn]-style forked process model to handle concurrency,
and are difficult or impossible to get Prometheus to scrape properly.

[pushgateway]: https://github.com/prometheus/pushgateway
[unicorn]: https://bogomips.org/unicorn/

## How it works

The prometheus-aggregator expects clients to connect and emit newline-delimited
JSON objects for each metric observation. Each object needs to be fully
specified with name, type, help, and value. Here's an example of three counter
increments.

```
{"name": "myapp_foo_total", "type": "counter", "help": "Total number of foos.", "value": 1}\n
{"name": "myapp_foo_total", "type": "counter", "help": "Total number of foos.", "value": 1}\n
{"name": "myapp_foo_total", "type": "counter", "help": "Total number of foos.", "value": 1}\n
```

Obviously this is wildly inefficient, so, as an optimization, once a metric has
been "declared" with name, type, and help, subsequent emissions may refer to it
simply by name.

```
{"name": "myapp_foo_total", "type": "counter", "help": "Total number of foos.", "value": 1}\n
{"name": "myapp_foo_total", "value": 1}\n
{"name": "myapp_foo_total", "value": 1}\n
```

You can delare a metric without making an observation by omitting the value.

```
{"name": "myapp_foo_total", "type": "counter", "help": "Total number of foos."}\n
{"name": "myapp_foo_total", "value": 1}\n  # value is now 1
{"name": "myapp_foo_total", "value": 2}\n  # value is now 3
```

You can declare metrics at runtime, like this, or you can predeclare metrics in
a JSON file, and pass it to the program at startup. Or mix and match both.

If serializing JSON is a bottleneck, you can optionally emit observations (but
not declarations) in the [Prometheus exposition format][pef].

[pef]: https://prometheus.io/docs/instrumenting/exposition_formats/

```
{"name": "myapp_foo_total", "type": "counter", "help": "Total number of foos."}\n
myapp_foo_total{} 1\n
myapp_foo_total{} 2\n
```

Labels are supported in both formats as you might expect.

```
{"name": "myapp_foo_total", "labels": {"success": "false", "code": "401"}, "value": 1}\n
myapp_foo_total{success="true",code="200"} 1\n
```

Gauges work just like counters, but default to setting the most recent value.

```
{"name": "myapp_worker_pool", "type": "gauge", "help": "Size of worker pool."}\n
myapp_worker_pool{} 1\n  # value is now 1
myapp_worker_pool{} 2\n  # value is now 2
```

Histograms take buckets as part of their declaration.

```
{"name": "myapp_req_dur_seconds", "type": "histogram",
  "help": "Duration of request in seconds.", 
    "buckets": [0.01, 0.05, 0.1, 0.5, 1, 2, 5, 10]}\n
myapp_req_dur_seconds{} 0.0123\n
myapp_req_dur_seconds{} 0.99\n
```
