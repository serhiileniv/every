#!/bin/sh
# Regenerate every golden fixture from the Ruby tree.
#
#   sh scripts/fixtures.sh
#
# Both generators write into testdata/golden and each clears only the
# subdirectories it owns, so they can also be run individually. This exists so
# nobody has to remember that there are two.
set -eu

cd "$(dirname "$0")/.."

command -v ruby >/dev/null 2>&1 || {
	echo "fixtures: no ruby on PATH -- the fixtures can only be regenerated" >&2
	echo "          while the Ruby tree still runs. They are committed for" >&2
	echo "          exactly this reason; you should not need to." >&2
	exit 1
}

ruby scripts/golden.rb
ruby scripts/surface.rb

echo
echo "fixtures: $(find testdata/golden -type f | wc -l | tr -d ' ') files"
