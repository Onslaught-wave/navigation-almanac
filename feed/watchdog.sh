#!/bin/bash
# Restart the feed container if its publishing loop has stopped making
# progress. Run from cron on the host.
#
#   */30 * * * *  /srv/navigation-almanac/feed/watchdog.sh >> /var/log/navwarn-watchdog.log 2>&1
#
# `restart: unless-stopped` already covers a crash and a reboot. What it
# cannot see is a loop that is alive but wedged — a request that never returns
# despite its timeout, say — because the process is still running. The
# container writes a heartbeat after every cycle, so a stale one is the signal.
#
# It deliberately does nothing when the container is stopped: that is either
# someone's deliberate `docker compose stop` or a reboot Docker will handle,
# and starting it again would override an intended state.

set -uo pipefail

NAME=navwarn-feed
STALE_AFTER=$((3 * 3600))     # three cycles missed, not one late run
VOLUME=/var/lib/docker/volumes/feed_navwarn-data/_data

log() { printf '%s  %s\n' "$(date -u '+%Y-%m-%dT%H:%M:%SZ')" "$*"; }

running=$(docker inspect -f '{{.State.Running}}' "$NAME" 2>/dev/null) || {
  log "no such container — nothing to watch"; exit 0; }
[ "$running" = "true" ] || { log "container is stopped; leaving it alone"; exit 0; }

beat=$(sudo cat "$VOLUME/heartbeat" 2>/dev/null) || beat=""
if [ -z "$beat" ]; then
  # A container that has not finished its first cycle has no heartbeat yet.
  started=$(docker inspect -f '{{.State.StartedAt}}' "$NAME" 2>/dev/null)
  age=$(( $(date -u '+%s') - $(date -u -d "$started" '+%s' 2>/dev/null || echo 0) ))
  [ "$age" -lt "$STALE_AFTER" ] && { log "no heartbeat yet, started ${age}s ago — waiting"; exit 0; }
  log "no heartbeat and running for ${age}s — restarting"
  docker restart "$NAME" >/dev/null && log "restarted"
  exit 0
fi

age=$(( $(date -u '+%s') - beat ))
if [ "$age" -le "$STALE_AFTER" ]; then
  log "healthy, last publish ${age}s ago"
  exit 0
fi

# A restart fixes a wedged loop. It does not fix an expired token, a revoked
# one, or a source that has changed shape — and those look identical from
# here. So say what the container last complained about rather than
# restarting in a circle and calling it handled.
log "last publish ${age}s ago, over the ${STALE_AFTER}s limit"
docker logs --tail 40 "$NAME" 2>&1 | grep -iE "error|failed|denied|403|401" | tail -3 \
  | sed 's/^/    container said: /'
if [ "$age" -gt $((3 * STALE_AFTER)) ]; then
  log "restarting has not helped for $((age / 3600))h — this needs a human"
else
  docker restart "$NAME" >/dev/null && log "restarted"
fi
