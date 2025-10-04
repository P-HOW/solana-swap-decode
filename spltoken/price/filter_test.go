package price

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/joho/godotenv"

	"github.com/gagliardetto/solana-go"
	"github.com/gagliardetto/solana-go/rpc"
)

// ---- helpers: load .env from repo root ----

func loadDotEnvNearRepoRoot(t *testing.T) {
	t.Helper()
	// When `go test` runs inside spltoken/price, repo root is ../../
	candidates := []string{
		"../../.env",
		"../.env",
		".env",
		"../../../.env",
	}
	for _, p := range candidates {
		if _, err := os.Stat(p); err == nil {
			_ = godotenv.Load(p)
			return
		}
	}
	// Fallback: walk up until root
	wd, _ := os.Getwd()
	dir := wd
	for {
		envPath := filepath.Join(dir, ".env")
		if _, err := os.Stat(envPath); err == nil {
			_ = godotenv.Load(envPath)
			return
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			break
		}
		dir = parent
	}
}

func pickRPCFromEnv() string {
	if v := os.Getenv("SOLANA_RPC_URL_FOR_PRICE"); v != "" {
		return v
	}
	if v := os.Getenv("SOLANA_RPC_URL"); v != "" {
		return v
	}
	if v := os.Getenv("HELIUS_RPC"); v != "" {
		return v
	}
	return ""
}

// ---- test ----

func TestFilterTxsByMint_Slot371148735(t *testing.T) {
	loadDotEnvNearRepoRoot(t)

	rpcURL := pickRPCFromEnv()
	if rpcURL == "" {
		t.Fatalf("no RPC found in env (expected SOLANA_RPC_URL_FOR_PRICE / SOLANA_RPC_URL / HELIUS_RPC)")
	}

	client := rpc.New(rpcURL)

	// inputs
	slot := uint64(371148735)
	mint := solana.MustPublicKeyFromBase58("83kGGSggYGP2ZEEyvX54SkZR1kFn84RgGCDyptbDbonk")

	ctx := context.Background()
	filtered, err := FilterTxsByMint(ctx, client, slot, mint)
	if err != nil {
		t.Fatalf("FilterTxsByMint error: %v", err)
	}

	t.Logf("slot=%d → %d tx(s) touched mint %s", slot, len(filtered), mint)

	for i, ft := range filtered {
		t.Logf("[%d] blockTime=%d totalDelta(base units)=%s accountsTouched=%d",
			i, ft.BlockTime, ft.TotalDelta.String(), len(ft.PerAccountDelta))
	}

	// Optional assertion if you expect activity in that block:
	// if len(filtered) == 0 { t.Fatal("expected ≥1 tx touching the mint") }
}
