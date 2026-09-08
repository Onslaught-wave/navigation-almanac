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

`msi/manifest.json` (~2.6 KB, plain) and `msi/warnings.bin` (~200 KB).

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

Put the hex value in the `NAVWARN_KEY` repository secret (Settings → Secrets and
variables → Actions) and paste the printed Swift literal into the client. To run
from cron on a server instead of Actions:

```
0 * * * *  cd /srv/navigation-almanac && NAVWARN_KEY=… ./feed -out msi && git -C . add msi && git -C . commit -qm msi && git -C . push
```

Rotating the key requires an app release, so treat it as long-lived.

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
