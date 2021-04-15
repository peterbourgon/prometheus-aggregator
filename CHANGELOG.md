## v0.0.15

- Fix crashing bug with UDP -socket addresses.

## v0.0.14

- Add `-declpath` flag.

## v0.0.13

- First tagged version that is modules-aware.
- Stop vendoring dependencies.
- Switch to GitHub Actions for CI.
- Minor fixes to satisfy new static analysis tools.

## v0.0.12

- A typo in an error message when parsing Prometheus exposition format lines has been corrected.

## v0.0.11

- Bug fix: don't render timeseries that haven't collected any observations.

## v0.0.10

- Make histograms abide the convention of using `_bucket` suffixes on the metric name.

## v0.0.9

- Fix bug when parsing UNIX domain sockets.

## v0.0.8

- Add UDP support for incoming socket writes.

## v0.0.7

- Fixes a crashing bug if you don't specify an explicit -prometheus path.

## v0.0.6

- Renamed -direct to -socket.

## v0.0.5

- Removed discourse/prometheus_exporter compatibility.
- It was a bad idea.

## v0.0.4

- Added discourse/prometheus_exporter compatibility.
- This might be a good idea!

## v0.0.3

- Fixed tests.

## v0.0.2

- Emit HELP before TYPE in Prometheus /metrics endpoint.

## v0.0.1

- Initial release.

