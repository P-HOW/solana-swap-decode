package holder

import (
	"bufio"
	"context"
	"github.com/davecgh/go-spew/spew"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// testMint: use the mint you specified
const testMint = "EPjFWdd5AufqSSqeM2qN1xzybapC8G4wEGGkZwyTDt1v"

// ensureEnv loads .env if needed and fails the test if the RPC URL is not set.
func ensureEnv(t *testing.T) string {
	t.Helper()

	// If already set, use it.
	if v := strings.TrimSpace(os.Getenv(EnvRPCForCounter)); v != "" {
		return v
	}

	// Try to load a local .env (no external deps; mirrors production loader).
	try := func(p string) bool {
		f, err := os.Open(p)
		if err != nil {
			return false
		}
		defer f.Close()
		sc := bufio.NewScanner(f)
		for sc.Scan() {
			line := strings.TrimSpace(sc.Text())
			if line == "" || strings.HasPrefix(line, "#") {
				continue
			}
			if i := strings.Index(line, "#"); i >= 0 {
				line = strings.TrimSpace(line[:i])
			}
			kv := strings.SplitN(line, "=", 2)
			if len(kv) != 2 {
				continue
			}
			k := strings.TrimSpace(kv[0])
			v := strings.Trim(strings.TrimSpace(kv[1]), `"'`)
			_ = os.Setenv(k, v)
		}
		return true
	}

	// Search a few common locations
	_ = try(".env") ||
		try(filepath.Join("..", ".env")) ||
		try(filepath.Join("..", "..", ".env"))

	v := strings.TrimSpace(os.Getenv(EnvRPCForCounter))
	if v == "" {
		t.Fatalf("%s not set. Put it in your environment or .env so tests can run.", EnvRPCForCounter)
	}
	return v
}

func TestCountHoldersForMint_Auto(t *testing.T) {
	_ = ensureEnv(t)

	// Give the counter ample time; it already retries internally.
	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	res, err := CountHoldersForMint(ctx, testMint)

	spew.Dump(res)

	if err != nil {
		t.Fatalf("CountHoldersForMint error: %v", err)
	}
	if res == nil {
		t.Fatalf("nil result")
	}
	// Non-negative invariants; we don't assume any specific holder count.
	if res.Holders < 0 {
		t.Fatalf("holders < 0: %d", res.Holders)
	}
	if res.TotalAccounts < 0 {
		t.Fatalf("totalAccounts < 0: %d", res.TotalAccounts)
	}
	// Sanity: if there are matched accounts, holders should be <= total
	if res.TotalAccounts > 0 && res.Holders > res.TotalAccounts {
		t.Fatalf("holders(%d) > totalAccounts(%d)", res.Holders, res.TotalAccounts)
	}
}

func TestCountHoldersForMint_InvalidMintString(t *testing.T) {
	_ = ensureEnv(t)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	if _, err := CountHoldersForMint(ctx, "not-a-base58"); err == nil {
		t.Fatalf("expected error for invalid mint, got nil")
	}
}
