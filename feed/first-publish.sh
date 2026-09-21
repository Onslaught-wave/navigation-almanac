#!/bin/bash
# One-time bring-up: push the work, publish the first feed, point Pages at it.
#
#   gh auth login          # once, in a browser — nothing is typed into a chat
#   feed/first-publish.sh
#
# Everything after the login is scripted so the credentials never leave the
# keychain. Re-running is safe: the push is a no-op once done, the feed only
# republishes when the warnings have changed, and the Pages source is only set
# if it is not already correct.

set -euo pipefail

REPO="$(cd "$(dirname "${BASH_SOURCE[0]}")/.." && pwd)"
SLUG="Onslaught-wave/navigation-almanac"
BRANCH="gh-pages"
cd "$REPO"

step() { printf '\n── %s\n' "$*"; }

if ! command -v gh >/dev/null; then
  echo "gh is not installed:  brew install gh && gh auth login"
  exit 1
fi
if ! gh auth status >/dev/null 2>&1; then
  echo "not signed in:  gh auth login"
  exit 1
fi
gh auth setup-git >/dev/null 2>&1 || true

step "1/3  pushing $(git rev-list --count origin/main..HEAD) commits to main"
git push origin main

step "2/3  building and publishing the feed"
if [ ! -s feed/.navwarn-key ]; then
  echo "feed/.navwarn-key is missing — generate one with 'go run ./feed -genkey'"
  exit 1
fi
NAVWARN_KEY="$(cat feed/.navwarn-key)" feed/deploy.sh "$REPO"

step "3/3  pointing GitHub Pages at $BRANCH"
current=$(gh api "repos/$SLUG/pages" --jq '.source.branch' 2>/dev/null || echo none)
if [ "$current" = "$BRANCH" ]; then
  echo "already serving from $BRANCH"
else
  # The Pages site already exists, so this is an update rather than a create.
  if gh api -X PUT "repos/$SLUG/pages" \
        -f "source[branch]=$BRANCH" -f "source[path]=/" >/dev/null 2>&1; then
    echo "source set to $BRANCH"
  else
    echo "could not set it from here — the token needs the Pages permission."
    echo "Set it by hand: Settings → Pages → Deploy from a branch → $BRANCH → /"
  fi
fi

step "done"
echo "  https://onslaught-wave.github.io/navigation-almanac/msi/manifest.json"
echo "  https://onslaught-wave.github.io/navigation-almanac/msi/warnings.bin"
echo
echo "Pages takes a minute or two to build the first time."
