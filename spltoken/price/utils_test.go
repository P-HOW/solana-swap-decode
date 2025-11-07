package pricepackage

import (
	"github.com/gagliardetto/solana-go/rpc"
	"math"
)
price

import (
"context"
"math"
"testing"
"time"

"github.com/gagliardetto/solana-go/rpc"
)

// NOTE: We reuse loadDotEnvNearRepoRoot and pickRPCFromEnv from filter_test.go.
// Ensure filter_test.go is in the same package so these helpers are available.

func TestVWAPWithLogFence_OutlierFiltered(t *testing.T) {
	values := []float64{1.00, 1.01, 10.0} // 10.0 is an outlier
	weights := []float64{1.0, 1.0, 1.0}
	r := 2.0               // ln fence with r=2
	minWeight := 0.0       // no dust
	v, kept, wsum, ok := VWAPWithLogFence(values, weights, r, minWeight)
	if !ok {
		t.Fatalf("VWAPWithLogFence returned not ok")
	}
	// Expect outlier filtered: kept should be 2, v ~ 1.005
	if kept != 2 {
		t.Fatalf("expected kept=2, got %d", kept)
	}
	if wsum <= 0 {
		t.Fatalf("expected positive weight sum, got %f", wsum)
	}
	if math.Abs(v-1.005) > 0.01 {
		t.Fatalf("unexpected vwap: got %.6f", v)
	}
}

func TestSlotAtClosest_Basic(t *testing.T) {
	loadDotEnvNearRepoRoot(t)
	rpcURL := pickRPCFromEnv()
	if rpcURL == "" {
		t.Skip("no RPC found in env; set SOLANA_RPC_URL_FOR_PRICE / SOLANA_RPC_URL / HELIUS_RPC")
	}
	client := rpc.New(rpcURL)
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	// Pick a finalized reference slot and its timestamp.
	refSlot, err := client.GetSlot(ctx, rpc.CommitmentFinalized)
	if err != nil {
		t.Fatalf("GetSlot(finalized): %v", err)
	}
	btPtr, err := client.GetBlockTime(ctx, refSlot)
	if err != nil || btPtr == nil {
		t.Fatalf("GetBlockTime(%d): %v / %v", refSlot, err, btPtr)
	}
	tUnix := int64(*btPtr)

	best, tie, err := SlotAtClosest(ctx, client, tUnix, 512)
	if err != nil {
		t.Fatalf("SlotAtClosest error: %v", err)
	}

	// Fetch the chosen slot time and compare to neighbors.
	bestBT, err := client.GetBlockTime(ctx, best)
	if err != nil || bestBT == nil {
		t.Fatalf("GetBlockTime(best=%d): %v / %v", best, err, bestBT)
	}
	bestDiff := absI64(int64(*bestBT) - tUnix)

	// Check neighbor (best-1) if available.
	if best > 1 {
		leftBT, _ := client.GetBlockTime(ctx, best-1) // ignore error/nil
		if leftBT != nil {
			if absI64(int64(*leftBT)-tUnix) < bestDiff {
				t.Fatalf("neighbor left is closer than best")
			}
		}
	}
	// Check neighbor (best+1).
	rightBT, _ := client.GetBlockTime(ctx, best+1)
	if rightBT != nil {
		if absI64(int64(*rightBT)-tUnix) < bestDiff {
			t.Fatalf("neighbor right is closer than best")
		}
	}

	// If tie was reported, confirm equal absolute deltas.
	if tie != nil {
		tieBT, _ := client.GetBlockTime(ctx, *tie)
		if tieBT != nil {
			if absI64(int64(*tieBT)-tUnix) != bestDiff {
				t.Fatalf("reported tie is not actually equidistant")
			}
		}
	}
}

func absI64(x int64) int64 {
	if x < 0 {
		return -x
	}
	return x
}

