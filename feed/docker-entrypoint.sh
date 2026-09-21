#!/bin/sh
# Build the feed and publish it to GitHub Pages, from inside the container.
#
# Runs once and exits by default, which suits host cron or a systemd timer.
# Set INTERVAL to a number of seconds to keep it running on its own schedule
# instead — that is what the compose file does, so the server needs nothing
# configured beyond `docker compose up -d`.

set -eu

: "${REPO_URL:?REPO_URL is not set}"
: "${BRANCH:=gh-pages}"
: "${INTERVAL:=0}"
WORK=/data/repo

log() { printf '%s  %s\n' "$(date -u '+%Y-%m-%dT%H:%M:%SZ')" "$*"; }

# The key may arrive as a variable or as a mounted file; the file is the safer
# of the two, since an environment variable is visible to anything that can
# inspect the container.
if [ -z "${NAVWARN_KEY:-}" ] && [ -r /run/secrets/navwarn_key ]; then
  NAVWARN_KEY=$(cat /run/secrets/navwarn_key)
fi
[ -n "${NAVWARN_KEY:-}" ] || { log "error: NAVWARN_KEY is not set"; exit 1; }
export NAVWARN_KEY

if [ -r /key ]; then
  mkdir -p "$HOME/.ssh" && chmod 700 "$HOME/.ssh"
  cp /key "$HOME/.ssh/id_ed25519" && chmod 600 "$HOME/.ssh/id_ed25519"
  ssh-keyscan -t rsa,ecdsa,ed25519 github.com >"$HOME/.ssh/known_hosts" 2>/dev/null
fi

git config --global user.name  "navigation-almanac"
git config --global user.email "noreply@localhost"
git config --global --add safe.directory "$WORK"

publish() {
  if [ ! -d "$WORK/.git" ]; then
    log "cloning $REPO_URL"
    git clone --quiet "$REPO_URL" "$WORK"
  fi
  cd "$WORK"

  # An older copy of the static pages with fresh warnings still beats not
  # publishing, so a fetch failure is a warning rather than the end of the run.
  git fetch --quiet origin main || log "warning: could not fetch origin/main"
  git checkout --quiet main
  git reset --hard --quiet origin/main 2>/dev/null || log "warning: main not reset"

  log "building the feed"
  feed -out "$WORK/msi"
  [ -s "$WORK/msi/warnings.bin" ] || { log "error: no bundle produced"; return 1; }

  # Change detection is over the warnings alone: the blob's own hash moves
  # every build because the GCM nonce is random.
  content=$(sed -n 's/.*"content": *"\([0-9a-f]*\)".*/\1/p' "$WORK/msi/manifest.json" | head -1)
  marker=/data/published
  if [ -n "$content" ] && [ -f "$marker" ] && [ "$content" = "$(cat "$marker")" ]; then
    log "warnings unchanged — nothing to publish"
    return 0
  fi

  stage=/data/stage
  rm -rf "$stage" && mkdir -p "$stage"
  git archive main | tar -x -C "$stage"
  rm -rf "$stage/feed" "$stage/.github" "$stage/.gitignore"
  cp -R "$WORK/msi" "$stage/msi"

  # The published branch is rebuilt from scratch every run, so it holds exactly
  # one commit however long this keeps running.
  cd "$stage"
  git init --quiet --initial-branch="$BRANCH"
  git add -A
  git commit --quiet -m "msi feed $(date -u '+%Y-%m-%dT%H:%MZ')"
  git push --quiet --force "$REPO_URL" "$BRANCH"
  cd /data && rm -rf "$stage"

  printf '%s' "$content" > "$marker"
  count=$(sed -n 's/.*"warnings": *\([0-9]*\).*/\1/p' "$WORK/msi/manifest.json" | head -1)
  log "published ${count:-?} warnings to $BRANCH"
}

if [ "$INTERVAL" -gt 0 ] 2>/dev/null; then
  log "running every ${INTERVAL}s"
  while :; do
    publish || log "run failed; keeping the previously published feed"
    sleep "$INTERVAL"
  done
else
  publish
fi
