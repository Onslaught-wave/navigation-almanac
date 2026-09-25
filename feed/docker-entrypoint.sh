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

# Git authenticates over HTTPS with a token written to ~/.netrc. The token
# arrives as a mounted secret rather than an environment variable, because a
# variable is readable by anything that can inspect the container — and it is
# never written into the repository or the image.
if [ -z "${GIT_TOKEN:-}" ] && [ -r /run/secrets/git_token ]; then
  GIT_TOKEN=$(cat /run/secrets/git_token)
fi
if [ -n "${GIT_TOKEN:-}" ]; then
  umask 077
  printf 'machine github.com\n  login x-access-token\n  password %s\n' "$GIT_TOKEN" \
    > "$HOME/.netrc"
  chmod 600 "$HOME/.netrc"
  unset GIT_TOKEN
fi

# A coordinator that refuses this host is retried through a public proxy; the
# binary carries a list, but those die constantly. Dropping a fresh one into
# the data volume overrides it without rebuilding the image:
#
#   docker cp working.txt navwarn-feed:/data/proxies.txt
#
# Regenerate with feed/tools/check-proxies.sh, run on this host.
if [ -z "${NAVWARN_PROXIES:-}" ] && [ -r /data/proxies.txt ]; then
  NAVWARN_PROXIES=/data/proxies.txt
  export NAVWARN_PROXIES
  log "using the proxy list at /data/proxies.txt"
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

  # The one-commit branch is what keeps this repository from growing without
  # bound. The bundle is AES output: incompressible, and different on every
  # build, so committing it in the ordinary way would add a permanent quarter
  # of a megabyte every hour, around 1.7 GB a year. Rebuilding the branch from
  # scratch instead leaves the old blob unreachable, and the host collects it.
  #
  # That invariant lives in a comment above, and a comment has to be read. So
  # check_branch_depth checks it as well, once a day.
}

# check_branch_depth reports how long the published branch's history is, using
# a shallow probe so it costs one object rather than the whole branch.
check_branch_depth() {
  probe=/data/depth-probe
  rm -rf "$probe"
  if git clone --quiet --depth 2 --branch "$BRANCH" "$REPO_URL" "$probe" 2>/dev/null; then
    depth=$(git -C "$probe" rev-list --count HEAD 2>/dev/null || echo 1)
    if [ "${depth:-1}" -gt 1 ]; then
      log "warning: $BRANCH carries $depth commits, expected 1 — the publish step is"
      log "         no longer rebuilding the branch, and the repository will grow by"
      log "         about 245 KB every hour. See the comment in publish()."
    fi
  fi
  rm -rf "$probe"
}

if [ "$INTERVAL" -gt 0 ] 2>/dev/null; then
  log "running every ${INTERVAL}s"
  cycle=0
  while :; do
    # The heartbeat is written only when a cycle actually succeeds. Writing it
    # unconditionally would hide exactly the failures worth catching — an
    # expired git token, for instance, leaves the loop running happily while
    # nothing reaches the app.
    if publish; then
      date -u '+%s' > /data/heartbeat
    else
      log "run failed; keeping the previously published feed and the old heartbeat"
    fi
    # Once at startup and once a day after that. The branch cannot start
    # growing between two cycles without a deliberate change to this file, so
    # checking hourly would only spend requests.
    if [ "$((cycle % 24))" -eq 0 ]; then
      check_branch_depth
    fi
    cycle=$((cycle + 1))
    sleep "$INTERVAL"
  done
else
  publish
fi
