# Builds and publishes the navigational-warnings feed.
#
#   docker build -t navwarn feed/
#   docker run --rm -e NAVWARN_KEY=… -v navwarn:/data -v ~/.ssh/deploy_key:/key:ro navwarn
#
# The image carries git and ssh because publishing is a force-push of a
# single-commit branch: the bundle is AES-GCM output, incompressible and
# different on every build, so committing it normally would leave a permanent
# 200 KB object in the repository every hour.
#
# Nothing secret is baked in. The signing key arrives as NAVWARN_KEY and the
# deploy key is mounted read-only at run time.

FROM golang:1.26-alpine AS build
WORKDIR /src
# No third-party dependencies, so there is nothing to download — the module
# files are copied first only to keep this layer cached across source edits.
COPY go.mod ./
COPY *.go ./
RUN CGO_ENABLED=0 go build -trimpath -ldflags="-s -w" -o /feed .

FROM alpine:3.21
RUN apk add --no-cache git openssh-client ca-certificates tzdata \
 && adduser -D -u 10001 feed
COPY --from=build /feed /usr/local/bin/feed
COPY docker-entrypoint.sh /usr/local/bin/entrypoint
RUN chmod +x /usr/local/bin/entrypoint

# The clone and the "already published" marker live here, so a restart does
# not re-clone and an unchanged feed is not republished.
VOLUME /data
ENV REPO_URL=git@github.com:Onslaught-wave/navigation-almanac.git \
    BRANCH=gh-pages \
    INTERVAL=0

USER feed
WORKDIR /data
ENTRYPOINT ["/usr/local/bin/entrypoint"]
