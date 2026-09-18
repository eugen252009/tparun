#!/bin/sh
set -eu
# Candidate bootstrap qualification now exercises the Debian-native package path.
exec "$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)/bootstrap-package-qualification.sh"
