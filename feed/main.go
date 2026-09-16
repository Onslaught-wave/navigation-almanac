// Command feed builds the navigational-warnings bundle the app downloads.
//
//	feed -genkey                       # once, on the build host
//	feed -out ../msi                   # fetch every source and publish
//	feed -out ../msi -only france,uk   # one or two sources, for development
//	feed -check                        # fetch and report, publish nothing
//
// It writes two files:
//
//	manifest.json   ~1 KB, plain — build number, timestamp, sha256 of the blob,
//	                and the status of every source including the ones that are
//	                unreachable. The app polls this.
//	warnings.bin    the feed: compact JSON, raw-deflated, then AES-256-GCM.
//
// Compression is what makes hourly publishing free — roughly 900 KB of JSON
// becomes 200 KB. The encryption matters less for secrecy (the key ships
// inside the app, so it is obfuscation rather than protection) than for the
// GCM authentication tag: this file sits on public hosting, and a client must
// not accept a modified bundle as navigation data.
//
// The key never lives in this repository. Generate it once with -genkey, keep
// it in NAVWARN_KEY on the build host or in a CI secret, and paste the printed
// Swift literal into the client.
package main

import (
	"bytes"
	"compress/flate"
	"crypto/aes"
	"crypto/cipher"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
)

const schema = 1

var magic = []byte("NAW1")

// SourceStatus is what the manifest reports per coordinator. A source that
// failed is published as failed: an area with no warnings must never be
// mistaken by the app for an area with nothing in force.
type SourceStatus struct {
	Status     string `json:"status"` // ok | stale | failed | unavailable
	Count      int    `json:"count"`
	Areas      string `json:"areas,omitempty"`
	NewestYear int    `json:"newest_year,omitempty"`
	Note       string `json:"note,omitempty"`
}

type Manifest struct {
	Schema       int                     `json:"schema"`
	Build        int                     `json:"build"`
	GeneratedAt  string                  `json:"generated_at"`
	Blob         string                  `json:"blob"`
	SHA256       string                  `json:"sha256"`  // of the blob, for integrity
	Content      string                  `json:"content"` // of the warnings, for change detection
	Bytes        int                     `json:"bytes"`
	Warnings     int                     `json:"warnings"`
	WithPosition int                     `json:"with_position"`
	Sources      map[string]SourceStatus `json:"sources"`
}

type payload struct {
	Schema      int                     `json:"schema"`
	GeneratedAt string                  `json:"generated_at"`
	Sources     map[string]SourceStatus `json:"sources"`
	Warnings    []Warning               `json:"warnings"`
}

func main() {
	var (
		out    = flag.String("out", "../msi", "directory to publish into")
		only   = flag.String("only", "", "comma-separated source names")
		keyHex = flag.String("key", "", "hex key; defaults to $NAVWARN_KEY")
		genkey = flag.Bool("genkey", false, "print a fresh key and exit")
		check  = flag.Bool("check", false, "fetch and report, publish nothing")
	)
	flag.Parse()

	if *genkey {
		generateKey()
		return
	}

	warnings, status := collect(*only)
	report(status)

	if *check {
		return
	}
	if len(warnings) == 0 {
		fail("refusing to publish an empty feed")
	}
	if err := publish(*out, warnings, status, resolveKey(*keyHex)); err != nil {
		fail(err.Error())
	}
}

func fail(msg string) {
	fmt.Fprintln(os.Stderr, "error: "+msg)
	os.Exit(1)
}

func resolveKey(flagValue string) []byte {
	raw := flagValue
	if raw == "" {
		raw = os.Getenv("NAVWARN_KEY")
	}
	if raw == "" {
		fail("no key: set NAVWARN_KEY, or run with -genkey to make one")
	}
	key, err := hex.DecodeString(strings.TrimSpace(raw))
	if err != nil {
		fail("key is not valid hex: " + err.Error())
	}
	if len(key) != 32 {
		fail(fmt.Sprintf("key must be 32 bytes, got %d", len(key)))
	}
	return key
}

func generateKey() {
	key := make([]byte, 32)
	if _, err := rand.Read(key); err != nil {
		fail(err.Error())
	}
	fmt.Println("keep this secret — build host only:")
	fmt.Printf("\n  export NAVWARN_KEY=%s\n", hex.EncodeToString(key))
	fmt.Println("\npaste into the client:")
	fmt.Println("\n    private static let key = SymmetricKey(data: Data([")
	for i := 0; i < 32; i += 8 {
		parts := make([]string, 0, 8)
		for _, b := range key[i : i+8] {
			parts = append(parts, fmt.Sprintf("0x%02x", b))
		}
		fmt.Printf("        %s,\n", strings.Join(parts, ", "))
	}
	fmt.Println("    ]))")
}

// collect runs every parser, keeping the failures.
func collect(only string) ([]Warning, map[string]SourceStatus) {
	wanted := map[string]bool{}
	for _, name := range strings.Split(only, ",") {
		if name = strings.TrimSpace(name); name != "" {
			wanted[name] = true
		}
	}

	client := NewClient()
	status := map[string]SourceStatus{}
	var all []Warning
	thisYear := time.Now().UTC().Year()

	for _, src := range Sources {
		if len(wanted) > 0 && !wanted[src.Name] {
			continue
		}
		started := time.Now()
		got, err := src.Fetch(client)
		if err != nil {
			status[src.Name] = SourceStatus{Status: "failed", Areas: src.Areas,
				Note: truncate(err.Error(), 200)}
			fmt.Printf("  %-11s FAILED after %-6s %s\n", src.Name,
				time.Since(started).Round(time.Millisecond), truncate(err.Error(), 90))
			continue
		}
		newest := 0
		for _, w := range got {
			if w.Year > newest {
				newest = w.Year
			}
		}
		state := "ok"
		if newest > 0 && newest < thisYear-1 {
			state = "stale"
		}
		all = append(all, got...)
		status[src.Name] = SourceStatus{Status: state, Count: len(got),
			Areas: src.Areas, NewestYear: newest}
		fmt.Printf("  %-11s %-6s %5d warnings, newest %d  (%s)\n",
			src.Name, state, len(got), newest, time.Since(started).Round(time.Millisecond))
	}

	if len(wanted) == 0 {
		for _, b := range Blocked {
			status[b.Name] = SourceStatus{Status: "unavailable", Areas: b.Areas, Note: b.Why}
		}
	}
	return all, status
}

func report(status map[string]SourceStatus) {
	var ok, stale, failed, blocked, total int
	for _, s := range status {
		total += s.Count
		switch s.Status {
		case "ok":
			ok++
		case "stale":
			stale++
		case "failed":
			failed++
		case "unavailable":
			blocked++
		}
	}
	fmt.Printf("\n%d warnings — %d sources live, %d stale, %d failed, %d unavailable\n",
		total, ok, stale, failed, blocked)
}

// publish writes the blob and manifest, skipping the write entirely when the
// content has not changed so the published history stays meaningful.
func publish(dir string, warnings []Warning, status map[string]SourceStatus, key []byte) error {
	generated := time.Now().UTC().Truncate(time.Second).Format(time.RFC3339)

	// Change detection has to be over the warnings alone. The blob's own hash
	// cannot serve: the GCM nonce is random and the timestamp moves, so an
	// identical feed still seals to different bytes every run.
	content, err := json.Marshal(struct {
		S map[string]SourceStatus `json:"s"`
		W []Warning               `json:"w"`
	}{status, warnings})
	if err != nil {
		return err
	}
	contentSum := sha256.Sum256(content)
	contentHash := hex.EncodeToString(contentSum[:])

	body, err := json.Marshal(payload{schema, generated, status, warnings})
	if err != nil {
		return err
	}
	squeezed, err := deflate(body)
	if err != nil {
		return err
	}
	sealed, err := seal(squeezed, key)
	if err != nil {
		return err
	}
	sum := sha256.Sum256(sealed)
	digest := hex.EncodeToString(sum[:])

	if err := os.MkdirAll(dir, 0o755); err != nil {
		return err
	}
	manifestPath := filepath.Join(dir, "manifest.json")

	build := 1
	if previous, err := os.ReadFile(manifestPath); err == nil {
		var old Manifest
		if json.Unmarshal(previous, &old) == nil {
			if old.Content == contentHash {
				fmt.Printf("unchanged — nothing published (build %d, %s…)\n",
					old.Build, contentHash[:12])
				return nil
			}
			build = old.Build + 1
		}
	}

	withPosition := 0
	for _, w := range warnings {
		if len(w.Coordinates) > 0 {
			withPosition++
		}
	}
	manifest := Manifest{schema, build, generated, "warnings.bin", digest, contentHash,
		len(sealed), len(warnings), withPosition, status}
	encoded, err := json.MarshalIndent(manifest, "", " ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(dir, "warnings.bin"), sealed, 0o644); err != nil {
		return err
	}
	if err := os.WriteFile(manifestPath, encoded, 0o644); err != nil {
		return err
	}

	fmt.Printf("build %d: json %.1f KB -> blob %.1f KB (%.0f%%), manifest %d B\n",
		build, float64(len(body))/1024, float64(len(sealed))/1024,
		100*float64(len(sealed))/float64(len(body)), len(encoded))
	fmt.Printf("  sha256 %s\n", digest)
	return nil
}

// deflate compresses with no zlib wrapper.
//
// Apple's Data.decompressed(using: .zlib) wants a *raw* deflate stream and
// rejects the 2-byte header and adler32 trailer that a zlib writer adds.
// Getting this wrong yields a client that fails only on device.
func deflate(data []byte) ([]byte, error) {
	var buf bytes.Buffer
	w, err := flate.NewWriter(&buf, flate.BestCompression)
	if err != nil {
		return nil, err
	}
	if _, err := w.Write(data); err != nil {
		return nil, err
	}
	if err := w.Close(); err != nil {
		return nil, err
	}
	return buf.Bytes(), nil
}

// seal returns magic || nonce || ciphertext+tag.
func seal(plaintext, key []byte) ([]byte, error) {
	block, err := aes.NewCipher(key)
	if err != nil {
		return nil, err
	}
	gcm, err := cipher.NewGCM(block)
	if err != nil {
		return nil, err
	}
	nonce := make([]byte, gcm.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	out := append([]byte{}, magic...)
	out = append(out, nonce...)
	return gcm.Seal(out, nonce, plaintext, nil), nil
}

func truncate(s string, n int) string {
	s = strings.ReplaceAll(strings.TrimSpace(s), "\n", " ")
	if len(s) <= n {
		return s
	}
	return s[:n] + "…"
}
