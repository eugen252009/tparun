#!/bin/sh
set -eu
. "$(dirname "$0")/common.sh"
compose restart app
wait_ready
compose ps
