# Performance benchmarks

`scripts/benchmark.sh` reports allocation-aware Go benchmarks for two
control-plane hot paths: scheduling across 1,000 nodes and resolving a service
snapshot containing 1,000 endpoints. Output uses Go's standard benchmark
format so it can be archived and compared with `benchstat`. Benchmarks avoid
network and disk noise and measure algorithmic cost; end-to-end deployment
latency remains environment-dependent and should be measured on the target
Linux cluster.
