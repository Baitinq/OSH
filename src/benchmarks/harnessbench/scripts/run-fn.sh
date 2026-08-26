#!/bin/sh
set -eu
: "${FN_BENCH_BINARY:?FN_BENCH_BINARY is not set}"
exec "$FN_BENCH_BINARY" --print < "$1"
