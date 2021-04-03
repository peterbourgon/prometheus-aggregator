package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io/ioutil"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"text/tabwriter"
	"time"

	"github.com/go-kit/kit/log"
	"github.com/go-kit/kit/log/level"
	"github.com/oklog/run"
)

var version = "HEAD (dev/unreleased)"

func main() {
	fs := flag.NewFlagSet("prometheus-aggregator", flag.ExitOnError)
	var (
		sockAddr = fs.String("socket", "tcp://127.0.0.1:8191", "address for direct socket metric writes")
		promAddr = fs.String("prometheus", "tcp://127.0.0.1:8192/metrics", "address for Prometheus scrapes")
		declfile = fs.String("declfile", "", "file containing JSON metric declarations")
		declpath = fs.String("declpath", "", "sibling path to /metrics serving initial metric declarations")
		example  = fs.Bool("example", false, "print example declfile to stdout and return")
		debug    = fs.Bool("debug", false, "log debug information")
		strict   = fs.Bool("strict", false, "disconnect clients when they send bad data")
	)
	fs.Usage = usageFor(fs, "prometheus-aggregator [flags]")
	fs.Parse(os.Args[1:])

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

	var initial []observation
	{
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
	}

	var u *universe
	{
		var err error
		u, err = newUniverse(initial...)
		if err != nil {
			level.Error(logger).Log("err", err)
			os.Exit(1)
		}
	}

	var socketLn net.Listener
	var forwardFunc func() error
	var forwardClose func() error
	{
		sockURL, err := url.Parse(*sockAddr)
		if err != nil {
			level.Error(logger).Log("socket", *sockAddr, "err", err)
			os.Exit(1)
		}

		var address string
		switch strings.ToLower(sockURL.Scheme) {
		case "udp", "udp4", "udp6", "tcp", "tcp4", "tcp6":
			address = sockURL.Host
		case "unix", "unixgram", "unipacket":
			address = sockURL.Path
		default:
			level.Error(logger).Log("socket", *sockAddr, "err", "unsupported network", "network", sockURL.Scheme)
			os.Exit(1)
		}

		switch strings.ToLower(sockURL.Scheme) {
		case "udp", "udp4", "udp6", "unixgram":
			laddr, err := net.ResolveUDPAddr(sockURL.Scheme, address)
			if err != nil {
				level.Error(logger).Log("socket", *sockAddr, "err", err)
				os.Exit(1)
			}
			conn, err := net.ListenUDP(sockURL.Scheme, laddr)
			if err != nil {
				level.Error(logger).Log("socket", *sockAddr, "err", err)
				os.Exit(1)
			}
			forwardFunc = func() error { return forwardPacketConn(conn, u, logger) }
			forwardClose = conn.Close

		case "tcp", "tcp4", "tcp6", "unix", "unixpacket":
			ln, err := net.Listen(sockURL.Scheme, address)
			if err != nil {
				level.Error(logger).Log("socket", *sockAddr, "err", err)
				os.Exit(1)
			}
			socketLn = ln
			forwardFunc = func() error { return forwardListener(ln, u, *strict, logger) }
			forwardClose = ln.Close
		}
	}

	var metricsLn net.Listener
	var metricsPath string
	{
		u, err := url.Parse(*promAddr)
		if err != nil {
			level.Error(logger).Log("prometheus", *promAddr, "err", err)
			os.Exit(1)
		}
		metricsLn, err = net.Listen(u.Scheme, u.Host)
		if err != nil {
			level.Error(logger).Log("prometheus", *promAddr, "err", err)
			os.Exit(1)
		}
		metricsPath = u.Path
		if metricsPath == "" {
			metricsPath = "/"
		}
	}

	var declPath string
	var declHandler http.Handler
	{
		if *declpath != "" {
			*declpath = "/" + strings.Trim(*declpath, "/ ")
			scheme := metricsLn.Addr().Network()
			hostport := metricsLn.Addr().String()
			u, err := url.Parse(scheme + "://" + hostport + *declpath)
			if err != nil {
				level.Error(logger).Log("declpath", *declpath, "err", err)
				os.Exit(1)
			}
			declPath = u.Path
			declHandler = http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set("content-type", "application/json; charset=utf-8")
				p, _ := json.MarshalIndent(initial, "", "    ")
				w.Write(p)
			})
		}
	}

	var g run.Group
	{
		g.Add(func() error {
			level.Info(logger).Log("listener", "socket_writes", "network", socketLn.Addr().Network(), "address", socketLn.Addr().String())
			return forwardFunc()
		}, func(error) {
			forwardClose()
		})
	}
	{
		mux := http.NewServeMux()
		mux.Handle(metricsPath, u)
		if declPath != "" {
			mux.Handle(declPath, declHandler)
		}
		server := http.Server{Handler: mux}
		g.Add(func() error {
			keyvals := []interface{}{"listener", "prometheus_scrapes", "network", metricsLn.Addr().Network(), "address", metricsLn.Addr().String(), "path", metricsPath}
			if declPath != "" {
				keyvals = append(keyvals, "declarations", declPath)
			}
			level.Info(logger).Log(keyvals...)
			return server.Serve(metricsLn)
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

func usageFor(fs *flag.FlagSet, short string) func() {
	return func() {
		fmt.Fprintf(os.Stderr, "USAGE\n")
		fmt.Fprintf(os.Stderr, "  %s\n", short)
		fmt.Fprintf(os.Stderr, "\n")
		fmt.Fprintf(os.Stderr, "FLAGS\n")
		w := tabwriter.NewWriter(os.Stderr, 0, 2, 2, ' ', 0)
		fs.VisitAll(func(f *flag.Flag) {
			def := f.DefValue
			if def == "" {
				def = "..."
			}
			fmt.Fprintf(w, "\t-%s %s\t%s\n", f.Name, def, f.Usage)
		})
		w.Flush()
		fmt.Fprintf(os.Stderr, "\n")
		fmt.Fprintf(os.Stderr, "VERSION\n")
		fmt.Fprintf(os.Stderr, "  %s\n", version)
		fmt.Fprintf(os.Stderr, "\n")
	}
}

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
