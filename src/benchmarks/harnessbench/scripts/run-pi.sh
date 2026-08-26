#!/bin/sh
set -eu
exec pi --provider openai --model gpt-5.6-sol --thinking medium -p < "$1"
