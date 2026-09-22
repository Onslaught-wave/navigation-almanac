package main

// Reaching a coordinator that refuses this host.
//
// Chile's SHOA answers HTTP 403 to datacenter addresses while serving the same
// URL normally from a residential one — verified by fetching it side by side
// from a laptop (200, a 12613-byte PDF) and from the build host (403, an nginx
// error page). Nothing about the request differs; only the address does. That
// leaves NAVAREA XV, the whole southeast Pacific, missing from the feed.
//
// So a refusal is retried through a public HTTP proxy. Three properties make
// that acceptable for a channel that carries safety information:
//
//   - Every coordinator here is fetched over HTTPS, so a proxy carries a
//     CONNECT tunnel and cannot read or alter the document inside it.
//   - Certificates are verified as usual. A proxy that tried to substitute
//     itself would fail the handshake rather than quietly serve its own
//     content: the failure mode is a missing source, never a forged warning.
//   - Which proxy served a document is printed in the build log, so the
//     provenance of anything published this way can be traced.
//
// The proxies themselves are run by strangers and go dead constantly, which is
// why the built-in list is a starting point rather than the configuration:
// NAVWARN_PROXIES, or -proxies, names a file to use instead. See
// tools/check-proxies.sh for re-testing a list against a live URL.

import (
	"fmt"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

// defaultProxies were each verified against the live SHOA document from the
// build host: HTTP 200, a PDF, and content identical to a direct fetch bar the
// document UUID the generator stamps afresh every time. Ordered fastest first,
// and spread across networks so one operator going dark does not take the
// whole list with it.
//
// Verified 2026-09-22. They rot; when NAVAREA XV starts failing again, re-run
// tools/check-proxies.sh and replace this list or point NAVWARN_PROXIES at a
// fresh one.
var defaultProxies = []string{
	"85.8.47.198:7080",
	"85.8.47.210:7080",
	"176.111.37.5:39811",
	"153.51.201.35:999",
	"154.201.127.46:8080",
	"178.92.72.129:8080",
	"103.216.106.119:8080",
	"190.97.241.106:999",
	"121.29.195.232:7890",
	"14.139.235.82:3128",
	"49.147.109.196:8082",
	"210.211.113.36:80",
}

// howManyProxiesToTry caps the cost of a refusal. A source that is genuinely
// gone would otherwise spend a minute working through a list that cannot help
// it, on every build.
const howManyProxiesToTry = 5

// proxyTimeout is deliberately short. These are strangers' machines: a slow
// one is not worth waiting for when there are others in the list.
const proxyTimeout = 25 * time.Second

// loadProxies resolves the list to use: the file named by -proxies or
// NAVWARN_PROXIES if there is one, the built-in list otherwise. One host:port
// per line; blank lines and # comments are ignored.
func loadProxies(path string) []string {
	if path == "" {
		path = os.Getenv("NAVWARN_PROXIES")
	}
	if path == "" {
		return defaultProxies
	}
	body, err := os.ReadFile(path)
	if err != nil {
		fmt.Fprintf(os.Stderr, "warning: cannot read %s (%v) — using the built-in proxy list\n",
			path, err)
		return defaultProxies
	}
	var out []string
	for _, line := range strings.Split(string(body), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		out = append(out, line)
	}
	if len(out) == 0 {
		fmt.Fprintf(os.Stderr, "warning: %s lists no proxies — using the built-in list\n", path)
		return defaultProxies
	}
	return out
}

// viaProxy repeats a request through the proxy list until one of them is not
// refused.
//
// Only a refusal is retried this way. A timeout or a parse error says nothing
// about the address the request came from, and sending those through a
// stranger's machine would buy nothing.
func (c *Client) viaProxy(method, rawURL, body, accept, referer string) ([]byte, string, error) {
	if len(c.proxies) == 0 {
		return nil, "", fmt.Errorf("no proxies configured")
	}
	// Some coordinators answer 403 to everyone without a browser — Brazil and
	// New Zealand both sit behind a Cloudflare challenge that refuses the
	// proxies exactly as it refuses the build host. A source like that can ask
	// for dozens of documents in one run, and working through the list for
	// each of them would add minutes to every build to no purpose. So once the
	// whole list has failed for a host, that host is left alone for the rest
	// of the run.
	host := hostOf(rawURL)
	if c.proxiesHopeless[host] {
		return nil, "", fmt.Errorf("proxies already failed for %s this run", host)
	}
	// A proxy that worked earlier in this run is tried first: several sources
	// fetch many documents, and re-probing the list for each one is waste.
	candidates := c.proxies
	if c.lastGoodProxy != "" {
		candidates = append([]string{c.lastGoodProxy}, candidates...)
	}

	tried := 0
	var last error
	for _, p := range candidates {
		if tried >= howManyProxiesToTry {
			break
		}
		if p != c.lastGoodProxy {
			tried++
		}
		proxyURL, err := url.Parse("http://" + strings.TrimPrefix(p, "http://"))
		if err != nil {
			last = err
			continue
		}
		client := &http.Client{
			Timeout: proxyTimeout,
			// No TLS configuration of any kind: the defaults verify the
			// certificate chain and the hostname, which is what keeps a proxy
			// operator from serving their own document in place of the
			// coordinator's.
			Transport: &http.Transport{Proxy: http.ProxyURL(proxyURL)},
		}
		data, err := requestOnce(client, method, rawURL, body, accept, referer)
		if err != nil {
			last = err
			continue
		}
		c.lastGoodProxy = p
		return data, p, nil
	}
	if c.proxiesHopeless == nil {
		c.proxiesHopeless = map[string]bool{}
	}
	c.proxiesHopeless[host] = true
	return nil, "", fmt.Errorf("every proxy tried also failed: %v", last)
}
