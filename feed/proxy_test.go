package main

import (
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Only a refusal is worth repeating from somewhere else. A timeout or a
// malformed page says nothing about the address the request came from.
func TestOnlyARefusalCountsAsOne(t *testing.T) {
	for _, msg := range []string{
		"HTTP 403 for https://www.shoa.cl/php/radioAvisosPDF.php",
		"HTTP 451 for https://example.invalid/x",
	} {
		if !isRefusal(errorString(msg)) {
			t.Errorf("%q should be a refusal", msg)
		}
	}
	for _, msg := range []string{
		"HTTP 404 for https://example.invalid/x",
		"HTTP 500 for https://example.invalid/x",
		"empty response from https://example.invalid/x",
		"dial tcp: i/o timeout",
		"",
	} {
		if isRefusal(errorString(msg)) {
			t.Errorf("%q should not be a refusal", msg)
		}
	}
	if isRefusal(nil) {
		t.Error("no error is not a refusal")
	}
}

type errorString string

func (e errorString) Error() string { return string(e) }

func TestLoadProxies(t *testing.T) {
	dir := t.TempDir()

	// No file named anywhere: the built-in list, so a fresh build host works
	// without being configured.
	t.Setenv("NAVWARN_PROXIES", "")
	if got := loadProxies(""); len(got) != len(defaultProxies) {
		t.Errorf("got %d proxies, want the built-in %d", len(got), len(defaultProxies))
	}

	// A file wins, and comments and blank lines are not proxies.
	path := filepath.Join(dir, "proxies.txt")
	body := "# refreshed 2026-09-22\n\n1.2.3.4:8080\n  5.6.7.8:3128  \n\n# dead:\n"
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatal(err)
	}
	got := loadProxies(path)
	if len(got) != 2 || got[0] != "1.2.3.4:8080" || got[1] != "5.6.7.8:3128" {
		t.Errorf("loadProxies = %v, want the two entries", got)
	}

	// The environment is consulted when the flag is empty — that is how the
	// container passes one in.
	t.Setenv("NAVWARN_PROXIES", path)
	if got := loadProxies(""); len(got) != 2 {
		t.Errorf("NAVWARN_PROXIES ignored: got %v", got)
	}

	// An unreadable or empty file must not silently disable the fallback: a
	// typo in a path is not a reason to drop a NAVAREA.
	t.Setenv("NAVWARN_PROXIES", "")
	if got := loadProxies(filepath.Join(dir, "nope.txt")); len(got) != len(defaultProxies) {
		t.Errorf("a missing file should fall back to the built-in list, got %d", len(got))
	}
	empty := filepath.Join(dir, "empty.txt")
	if err := os.WriteFile(empty, []byte("# nothing here\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := loadProxies(empty); len(got) != len(defaultProxies) {
		t.Errorf("a file with no entries should fall back, got %d", len(got))
	}
}

// A 403 is retried through the list; the proxy that answers is remembered and
// reported.
func TestARefusalIsRetriedThroughAProxy(t *testing.T) {
	// Stands in for a proxy: it answers whatever is asked of it. A real one
	// would CONNECT, but the client code path — pick a proxy, repeat the
	// request, keep the winner — is the same.
	var proxied int
	proxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		proxied++
		w.Write([]byte("%PDF-1.7 the document"))
	}))
	defer proxy.Close()

	refusing := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "Forbidden", http.StatusForbidden)
	}))
	defer refusing.Close()

	c := NewClient().WithProxies([]string{
		"127.0.0.1:1", // dead, to prove the list is walked
		strings.TrimPrefix(proxy.URL, "http://"),
	})
	body, err := c.Get(refusing.URL, "")
	if err != nil {
		t.Fatalf("the refusal should have been retried through the proxy: %v", err)
	}
	if !strings.HasPrefix(string(body), "%PDF") {
		t.Errorf("got %q, want the proxied document", body)
	}
	if proxied != 1 {
		t.Errorf("the proxy served %d requests, want 1", proxied)
	}
	if len(c.ProxyNotes) != 1 || !strings.Contains(c.ProxyNotes[0], "HTTP 403") {
		t.Errorf("the build log should record the detour, got %v", c.ProxyNotes)
	}

	// The winner is remembered, so a source fetching many documents does not
	// re-probe the list for each one.
	if c.lastGoodProxy == "" {
		t.Error("the working proxy should have been remembered")
	}
}

// Brazil and New Zealand sit behind a challenge that refuses the proxies
// exactly as it refuses the build host. Walking the list once per document
// would add minutes to every build for nothing.
func TestAHostNoProxyCanReachIsGivenUpOn(t *testing.T) {
	refusing := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "Forbidden", http.StatusForbidden)
	}))
	defer refusing.Close()

	var attempts int
	deadProxy := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		attempts++
		http.Error(w, "Forbidden", http.StatusForbidden)
	}))
	defer deadProxy.Close()

	c := NewClient().WithProxies([]string{strings.TrimPrefix(deadProxy.URL, "http://")})
	if _, err := c.Get(refusing.URL, ""); err == nil {
		t.Fatal("expected a failure")
	}
	first := attempts

	if _, err := c.Get(refusing.URL+"/another", ""); err == nil {
		t.Fatal("expected a failure")
	}
	if attempts != first {
		t.Errorf("the second document tried the proxies again (%d then %d)", first, attempts)
	}
}

// The failure has to name both halves: what the coordinator said, and that
// going around it did not work either.
func TestTheErrorSaysBothThingsWentWrong(t *testing.T) {
	refusing := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "Forbidden", http.StatusForbidden)
	}))
	defer refusing.Close()

	c := NewClient().WithProxies([]string{"127.0.0.1:1"})
	_, err := c.Get(refusing.URL, "")
	if err == nil {
		t.Fatal("expected a failure")
	}
	if !strings.Contains(err.Error(), "HTTP 403") {
		t.Errorf("the coordinator's refusal is missing from %q", err)
	}
	if !strings.Contains(err.Error(), "proxy") {
		t.Errorf("the proxy attempt is missing from %q", err)
	}
}

// Without proxies the client behaves exactly as it did before.
func TestNoProxiesMeansNoChange(t *testing.T) {
	refusing := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "Forbidden", http.StatusForbidden)
	}))
	defer refusing.Close()

	c := NewClient()
	_, err := c.Get(refusing.URL, "")
	if err == nil || !strings.Contains(err.Error(), "HTTP 403") {
		t.Fatalf("got %v, want the plain refusal", err)
	}
	if strings.Contains(err.Error(), "proxy") {
		t.Errorf("no proxies were configured, so none should be mentioned: %v", err)
	}
	if len(c.ProxyNotes) != 0 {
		t.Errorf("nothing was proxied, got %v", c.ProxyNotes)
	}
}
