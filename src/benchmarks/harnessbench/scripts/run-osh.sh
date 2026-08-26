#!/bin/sh
set -eu
: "${OSH_BENCH_BINARY:?OSH_BENCH_BINARY is not set}"
exec "$OSH_BENCH_BINARY" --print < "$1"
