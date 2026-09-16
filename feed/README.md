# MSI feed builder

Builds the navigational-warnings bundle Navigation Almanac downloads.

No single free source publishes NAVAREA warnings worldwide. NautoShark and
SeaLagom sell exactly that, and the free apps in this niche either buy from
them or read a physical NAVTEX receiver. So this scrapes each coordinator
directly and bundles the result.

```
go run . -check                  # fetch everything, report, publish nothing
go run . -out ../msi             # fetch and publish (needs NAVWARN_KEY)
go run . -out ../msi -only uk    # one source, while working on its parser
go run . -genkey                 # print a new key, once
```

## What it publishes

`msi/manifest.json` (~2.6 KB, plain) and `msi/warnings.bin` (~200 KB), served at

```
https://onslaught-wave.github.io/navigation-almanac/msi/manifest.json
https://onslaught-wave.github.io/navigation-almanac/msi/warnings.bin
```

Neither file is committed to `main` — `msi/` is in `.gitignore`. That matters
more than it sounds: the blob is AES-GCM output and therefore incompressible,
so git cannot delta it, and an hourly commit would leave a fresh 200 KB object
in the history forever, for data that is worthless as soon as the next build
replaces it.

## How it is published

`feed/deploy.sh` runs from cron on srv-int. It pulls `main` for the static
pages, builds the bundle, and force-pushes everything as a **single-commit
`gh-pages` branch** — rebuilt from scratch each run, so the published branch
never accumulates history. GitHub Pages serves that branch.

```
7 * * * *  cd /srv/navigation-almanac && NAVWARN_KEY=$(cat feed/.navwarn-key) feed/deploy.sh >> /var/log/navwarn.log 2>&1
```

It stops before pushing when the warnings are unchanged, so Pages is only
rebuilt when there is something new. Settings → Pages → Source must be
**Deploy from a branch → `gh-pages` → `/`**.

Anything committed to `main` — a site edit, the mirrored TLE set — goes live on
the next run rather than immediately.

GitHub Actions does not publish. `.github/workflows/check-msi-sources.yml` only
fetches every source and fails if a parser breaks, which is the failure worth
catching early.

The app polls the manifest, compares `content` with what it already holds, and
downloads the blob only when that changes. `sha256` covers the blob itself and
is what the app checks after downloading.

The blob is compact JSON, raw-deflated, then AES-256-GCM: `NAW1` magic, a
12-byte nonce, then ciphertext and tag. Compression is what makes hourly
publishing cheap — about 920 KB of JSON becomes 200 KB. The encryption is worth
less than it looks: the key ships inside the app, so anyone who wants it can
pull it out of the binary (which is exactly how the backends of three competing
apps were identified). What it does buy is the GCM authentication tag — this
file sits on public hosting, and the app must not accept a modified bundle as
navigation data.

## The key

Generate once, keep it out of this repository:

```
go run . -genkey
```

Keep the hex value on srv-int (the script reads `feed/.navwarn-key`, which is
git-ignored) and paste the printed Swift literal into the client.

The key is fixed for the life of the format. Changing it strands every
installed copy of the app on a bundle it can no longer open, so a rotation
means shipping a release first and re-keying the feed only once that release is
out. `WarningsFeedTests.testTheShippingKeyOpensABundleTheBuilderProduced`
fails if the two ever drift apart.

srv-int needs push rights to the repository. A deploy key scoped to this one
repository is the right credential — not an account-wide token.

## Sources

Ten coordinators are covered. Every parser is written against that country's
own publication format, because there is no shared one:

| Source | Areas | Format |
|---|---|---|
| France | II + 23 coastal series | open REST API, bilingual, hazard classification |
| Spain | III | generated XML, bilingual |
| UK | I + UK coastal | server-rendered table |
| Sweden | Baltic NAVTEX | HTML, already grouped by named sea area |
| Norway | XIX | HTML |
| Australia | X + AUSCOAST | broadcast-format text |
| Canada | XVII, XVIII | HTML, one table per warning |
| Peru | XVI | HTML accordion |
| Pakistan | IX | one plain-text file per warning |
| USA (NGA) | IV, XII + HYDROLANT/PAC/ARC | JSON API |

Eight coordinators cannot be read from a plain HTTP client at all — Brazil and
New Zealand sit behind Cloudflare, Japan returns 403, India publishes PDFs into
a Liferay document library, Chile's endpoint is broken, Argentina's warnings
page serves the site home, South Africa publishes monthly PDFs, and the Russian
services time out. That list lives in `warnings.go` as `Blocked`, and every one
of them is published in the manifest with its reason, so the app can say
"source unavailable" instead of showing an area with nothing in it.

## Two rules the parsers follow

**A source that breaks is reported as broken.** An empty NAVAREA is
indistinguishable, to a mariner, from "no warnings in force". Every parser
returns an error rather than an empty list, and `-check` will show it.

**A feed's own liveness is not trusted.** NGA answers HTTP 200 with warnings
frozen at 2024-05-10; its own `latest-warning` endpoint confirms it. Staleness
is judged from the newest warning a source actually returns, and NGA is
currently published with `"status": "stale"`.

## Caveats worth keeping in mind

None of these coordinators grants a redistribution licence. NGA explicitly
disclaims copyright but requires its disclaimer on the app's launch and About
screens; Sweden, Norway and the UK state no reuse terms at all. Every warnings
screen should carry the SOLAS caveat verbatim — that the web is not a substitute
for receiving MSI over IMO-approved broadcast systems — as Sjöfartsverket and
NautoShark both do.

Positions are parsed out of free text, so about 87% of warnings carry
coordinates; the rest are mostly "warnings in force" summaries, which have no
position by nature.
