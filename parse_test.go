package main

import (
	"testing"

	"github.com/google/go-cmp/cmp"
)

func TestParsePrometheus(t *testing.T) {
	fp := func(f float64) (p *float64) {
		p = new(float64)
		*p = f
		return p
	}

	for name, testcase := range map[string]struct {
		input string
		obs   observation
		err   bool
	}{
		"only spaces": {
			input: `    `,
			err:   true,
		},
		"empty": {
			input: ``,
			err:   true,
		},
		"no braces": {
			input: `foo 1`,
			err:   true,
		},
		"leading space": {
			input: ` foo{} 1`,
			obs:   observation{Name: "foo", Value: fp(1.00), Labels: map[string]string{}},
		},
		"trailing space": {
			input: `foo{} 1 `,
			obs:   observation{Name: "foo", Value: fp(1.00), Labels: map[string]string{}},
		},
		"ascii value": {
			input: `foo{} A`,
			err:   true,
		},
		"most basic": {
			input: `foo{} 1`,
			obs:   observation{Name: "foo", Value: fp(1.00), Labels: map[string]string{}},
		},
		"with label": {
			input: `foo{code="200"} 2.34`,
			obs:   observation{Name: "foo", Value: fp(2.34), Labels: map[string]string{"code": "200"}},
		},
		"missing quotes": {
			input: `foo{code=200} 2.34`,
			err:   true,
		},
		"missing closing brace": {
			input: `foo{code="200" 7`,
			err:   true,
		},
		"double closing brace": {
			input: `foo{code="200"}} 7`,
			err:   true,
		},
		"two labels": {
			input: `foo{code="200",err="false"} 7`,
			obs:   observation{Name: "foo", Value: fp(7.00), Labels: map[string]string{"code": "200", "err": "false"}},
		},
		"space between labels": {
			input: `foo{code="200", err="false"} 7`,
			err:   true,
		},
		"space instead of comma": {
			input: `foo{code="200" err="false"} 7`,
			err:   true,
		},
	} {
		t.Run(name, func(t *testing.T) {
			var obs observation
			err := prometheusUnmarshal([]byte(testcase.input), &obs)
			if want, have := testcase.err, err != nil; want != have {
				t.Fatalf("err: want %v, have %v (%v)", want, have, err)
			}
			if want, have := testcase.obs, obs; !cmp.Equal(want, have) {
				t.Fatal(cmp.Diff(want, have))
			}
		})
	}
}

func TestParseURI(t *testing.T) {
	for name, testcase := range map[string]struct {
		input   string
		network string
		address string
		path    string
		err     bool
	}{
		"empty string": {
			input: "",
			err:   true,
		},
		"bad scheme": {
			input: ":/localhost:1234/x",
			err:   true,
		},
		"TCP with path": {
			input:   "tcp://localhost:1234/x",
			network: "tcp",
			address: "localhost:1234",
			path:    "/x",
		},
		"TCP4 without path": {
			input:   "tcp4://localhost:1234",
			network: "tcp4",
			address: "localhost:1234",
			path:    "",
		},
		"UNIX socket": {
			input:   "unix:///var/tmp/my.sock",
			network: "unix",
			address: "/var/tmp/my.sock",
			path:    "",
		},
		"UNIX socket with host": {
			input:   "unix://ignored-host/var/tmp/my.sock",
			network: "unix",
			address: "/var/tmp/my.sock",
			path:    "",
		},
	} {
		t.Run(name, func(t *testing.T) {
			network, address, path, err := parseURI(testcase.input)
			if testcase.err && err == nil {
				t.Fatal("wanted error, but got none")
			}
			if want, have := testcase.network, network; want != have {
				t.Errorf("network: want %q, have %q", want, have)
			}
			if want, have := testcase.address, address; want != have {
				t.Errorf("address: want %q, have %q", want, have)
			}
			if want, have := testcase.path, path; want != have {
				t.Errorf("path: want %q, have %q", want, have)
			}
		})
	}
}
