#!/bin/sh
# Test proxies against a coordinator that refuses this host, and print the ones
# that actually deliver the document.
#
#   tools/check-proxies.sh candidates.txt > working.txt
#   tools/check-proxies.sh candidates.txt "$OTHER_URL" > working.txt
#
# Free proxies die constantly, so the list baked into proxy.go is a starting
# point rather than the configuration. When NAVAREA XV starts failing again,
# gather fresh candidates and re-run this, then either replace the list in
# proxy.go or point NAVWARN_PROXIES at the output.
#
# Candidate lists, one host:port per line, come from the usual public
# aggregators, for instance:
#
#   curl -s https://raw.githubusercontent.com/TheSpeedX/PROXY-List/master/http.txt
#   curl -s https://raw.githubusercontent.com/monosans/proxy-list/main/proxies/http.txt
#   curl -s https://raw.githubusercontent.com/proxifly/free-proxy-list/main/proxies/protocols/http/data.txt
#
# Run it on the build host, not on a laptop: the question is which proxies that
# machine can reach, and from a residential address the source is not blocked
# in the first place.
#
# The test is the real request. A proxy that answers 200 with its own error
# page is no use, so the document's own signature is what counts — for SHOA, a
# PDF header and a plausible size. Note there is no -k anywhere: the
# certificate must validate. That is what stops a proxy operator from serving
# their own document in place of the coordinator's, and it is the reason this
# is safe to do with safety information at all.

set -eu

UA='Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/140.0 Safari/537.36'
PARALLEL="${PARALLEL:-100}"
DEFAULT_URL='https://www.shoa.cl/php/radioAvisosPDF.php?documento=NAVAREA&tipo=3'

probe() {
  p="$1"
  out=$(mktemp)
  res=$(curl -s --proxy "http://$p" --max-time 25 --connect-timeout 8 \
          -A "$UA" -o "$out" -w '%{http_code} %{time_total}' "$URL" 2>/dev/null || true)
  code=${res%% *}
  secs=${res##* }
  size=$(wc -c < "$out" | tr -d ' ')
  magic=$(head -c 4 "$out" 2>/dev/null || true)
  rm -f "$out"
  # %PDF is SHOA's signature; for another URL, change this test to something
  # only the real document would satisfy.
  if [ "$code" = "200" ] && [ "$magic" = "%PDF" ] && [ "$size" -gt 8000 ]; then
    printf '%s %s %s\n' "$secs" "$p" "$size"
  fi
}

# Re-entry: xargs calls this script back, one proxy per invocation.
if [ "${1:-}" = "--probe-one" ]; then
  URL="${URL:-$DEFAULT_URL}"
  probe "$2"
  exit 0
fi

LIST="${1:?usage: check-proxies.sh <candidates.txt> [url]}"
URL="${2:-$DEFAULT_URL}"
export URL

# Fastest first, which is the order proxy.go wants them in.
xargs -P "$PARALLEL" -n 1 "$0" --probe-one < "$LIST" 2>/dev/null \
  | sort -n \
  | awk '{print $2}'
