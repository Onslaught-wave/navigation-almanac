#!/bin/bash
# Build the MSI feed and publish it to GitHub Pages. Runs from cron on srv-int.
#
#   NAVWARN_KEY=... feed/deploy.sh [/srv/navigation-almanac]
#
# The feed is ~210 KB of AES-GCM output: incompressible, and completely
# different after every build because the nonce is random. Committing it
# normally would add a fresh 200 KB object to the repository every hour,
# forever, for data that is worthless the moment the next build replaces it.
#
# So the published branch holds exactly ONE commit, amended and force-pushed
# each run. The site is served from that branch; `main` keeps the real history
# of the source and is never touched by this script.
#
# Content comes from main (index.html, privacy.html, the mirrored TLE set) plus
# the freshly built msi/. Pull main first so a change committed elsewhere goes
# live on the next run.

set -euo pipefail

REPO="${1:-$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)}"
BRANCH="gh-pages"
STAGE="$REPO/.publish"

log() { printf '%s  %s\n' "$(date -u '+%Y-%m-%dT%H:%M:%SZ')" "$*"; }

if [ -z "${NAVWARN_KEY:-}" ]; then
  log "error: NAVWARN_KEY is not set"
  exit 1
fi

cd "$REPO"

# 1. Refresh the source of the static pages. A failure here is not fatal — an
#    older copy of the site with fresh warnings still beats not publishing.
git fetch --quiet origin main || log "warning: could not fetch origin/main"
git checkout --quiet main
git merge --quiet --ff-only origin/main 2>/dev/null || log "warning: main not fast-forwarded"

# 2. Build. The binary refuses to publish an empty feed, so a total failure
#    leaves the previous bundle live rather than replacing it with nothing.
log "building the feed"
( cd "$REPO/feed" && go build -o .bin . )     # the module lives in feed/, not at the root
"$REPO/feed/.bin" -out "$REPO/msi"

if [ ! -s "$REPO/msi/warnings.bin" ] || [ ! -s "$REPO/msi/manifest.json" ]; then
  log "error: build produced no bundle — leaving the published feed untouched"
  exit 1
fi

# 3. Stop here if nothing changed. The builder leaves the bundle untouched when
#    the warnings are identical, so re-publishing would only rebuild Pages for
#    a byte-identical site. The manifest's content hash covers the warnings
#    alone — the blob's own hash moves every build, since the nonce is random.
CONTENT=$(sed -n 's/.*"content": *"\([0-9a-f]*\)".*/\1/p' "$REPO/msi/manifest.json" | head -1)
MARKER="$REPO/feed/.published"
if [ -n "$CONTENT" ] && [ -f "$MARKER" ] && [ "$CONTENT" = "$(cat "$MARKER")" ]; then
  log "warnings unchanged (${CONTENT:0:12}…) — nothing to publish"
  exit 0
fi

# 4. Stage the site: everything tracked on main, minus the builder's source,
#    plus the bundle.
rm -rf "$STAGE"
mkdir -p "$STAGE"
git archive main | tar -x -C "$STAGE"
rm -rf "$STAGE/feed" "$STAGE/.github" "$STAGE/.gitignore"
cp -R "$REPO/msi" "$STAGE/msi"

# 5. Publish as a single-commit branch. The branch is rebuilt from scratch each
#    run, so it never accumulates history no matter how long this runs.
cd "$STAGE"
git init --quiet --initial-branch="$BRANCH"
git add -A
git -c user.name="navigation-almanac" -c user.email="noreply@localhost" \
    commit --quiet -m "msi feed $(date -u '+%Y-%m-%dT%H:%MZ')"

REMOTE="$(git -C "$REPO" remote get-url origin)"
git remote add origin "$REMOTE"
git push --quiet --force origin "$BRANCH"

cd "$REPO"
rm -rf "$STAGE"
printf '%s' "$CONTENT" > "$MARKER"

BUILD=$(sed -n 's/.*"build": *\([0-9]*\).*/\1/p' "$REPO/msi/manifest.json" | head -1)
COUNT=$(sed -n 's/.*"warnings": *\([0-9]*\).*/\1/p' "$REPO/msi/manifest.json" | head -1)
log "published build ${BUILD:-?}, ${COUNT:-?} warnings, to $BRANCH"
