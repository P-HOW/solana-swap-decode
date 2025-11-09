package price

import (
	"context"
	"errors"
	"math"
	"time"

	"github.com/gagliardetto/solana-go"
	"github.com/gagliardetto/solana-go/rpc"
)

// estimateBackoffSlotsForDays computes how many *slots* roughly occur over `days`,
// using recent performance samples when available (fallback ~2.5 slots/sec).
func estimateBackoffSlotsForDays(ctx context.Context, client *rpc.Client, days float64) int {
	if days <= 0 {
		return 0
	}
	const fallbackSPS = 2.5
	limit := uint(60)
	samples, err := client.GetRecentPerformanceSamples(ctx, &limit)
	sps := 0.0
	if err == nil && len(samples) > 0 {
		var totalSlots uint64
		var totalSecs uint64
		for _, s := range samples {
			totalSlots += uint64(s.NumSlots)
			totalSecs += uint64(s.SamplePeriodSecs)
		}
		if totalSecs > 0 && totalSlots > 0 {
			sps = float64(totalSlots) / float64(totalSecs)
		}
	}
	if sps <= 0 {
		sps = fallbackSPS
	}
	seconds := days * 24 * 60 * 60
	slots := int(math.Ceil(sps * seconds))
	if slots < 1 {
		slots = 1
	}
	return slots
}

// GetTokenUSDPriceAtUnix computes the USD price for 1 unit of `targetMint` at UNIX timestamp (seconds).
// Steps:
//  1. Find closest slot to t using SlotAtClosest (fast bracketing).
//  2. Gather swap-derived points at that slot; if empty, scan earlier slots (backoff) up to ~8d of slots.
//  3. Convert each point to USD (USDC/USDT 1:1; SOL via Binance minute close).
//  4. Return VWAP (log-fenced) over USD prices weighted by USD notional of the counter/base leg.
//
// Params (defaults applied when <=0):
//   - backoffSlots: how many earlier slots to scan if initially empty (default ≈ slots in past 8 days)
//   - fenceR: log-fence parameter r (>1) (default 1.5)
//   - minWUSD: minimum USD notional to count as dust (default 1e-6)
//
// Note: Backoff stops at the **first slot** that yields any priceable swaps (no minPoints threshold).
func GetTokenUSDPriceAtUnix(
	ctx context.Context,
	client *rpc.Client,
	targetMint solana.PublicKey,
	tUnix int64,
	backoffSlots int,
	fenceR float64,
	minWUSD float64,
) (vwapUSD float64, kept int, sumW float64, ok bool, err error) {

	if client == nil {
		return 0, 0, 0, false, errors.New("nil rpc client")
	}
	if tUnix <= 0 {
		return 0, 0, 0, false, errors.New("invalid timestamp")
	}
	// Default backoff: scan up to ~8 days worth of *slots* into the past.
	if backoffSlots <= 0 {
		backoffSlots = estimateBackoffSlotsForDays(ctx, client, 8.0)
	}
	if fenceR <= 1.0 || math.IsNaN(fenceR) {
		fenceR = 1.5
	}
	if minWUSD <= 0 || math.IsNaN(minWUSD) {
		minWUSD = 1e-6
	}

	// 1) time → closest slot
	best, _, err := SlotAtClosest(ctx, client, tUnix, 4096)
	if err != nil {
		return 0, 0, 0, false, err
	}

	// 2) collect price points; stop at the first slot that yields any priceable swaps
	values := make([]float64, 0, 8)
	weights := make([]float64, 0, 8)

	addPoints := func(ps []PricePoint) {
		for _, p := range ps {
			if p.PriceUSD <= 0 || p.TargetQtyFloat <= 0 {
				continue
			}
			// Compute USD notional of the counter/base leg (weight).
			var w float64
			if p.BaseIsStable {
				// baseUSD = baseAmount / 10^dec
				w = float64(p.BaseAmountRaw) / math.Pow10(p.BaseDecimals)
			} else if p.BaseIsSOL {
				// Stay consistent with USD computation for SOL pairs:
				// baseUSD ≈ PriceUSD * tokenQty
				w = p.PriceUSD * p.TargetQtyFloat
			} else {
				continue
			}
			if w <= 0 || math.IsNaN(w) || math.IsInf(w, 0) {
				continue
			}
			values = append(values, p.PriceUSD)
			weights = append(weights, w)
		}
	}

	// Try the closest slot first.
	if pts, err := GetPricesAtSlot(ctx, client, best, targetMint); err == nil && len(pts) > 0 {
		addPoints(pts)
	}

	// If still empty, walk backward until we find any priceable swaps or hit the cap.
	scanned := 0
	curr := best
	for len(values) == 0 && scanned < backoffSlots {
		if curr == 0 {
			break
		}
		curr--

		pts, err := GetPricesAtSlot(ctx, client, curr, targetMint)
		if err != nil {
			// if slot is missing/pruned, skip
			scanned++
			continue
		}
		if len(pts) > 0 {
			addPoints(pts)
			// **Stop immediately** on first slot that provides priceable swaps.
			break
		}
		scanned++
	}

	if len(values) == 0 {
		return 0, 0, 0, false, errors.New("no USD-priceable swaps found in the search window")
	}

	// 3) VWAP with log fence
	v, k, sw, ok := VWAPWithLogFence(values, weights, fenceR, minWUSD)
	return v, k, sw, ok, nil
}

// GetTokenUSDPriceAtTime convenience wrapper using time.Time (UTC assumed).
// Uses the default backoff window (~8 days in slots) and stops at first priceable slot.
func GetTokenUSDPriceAtTime(
	ctx context.Context,
	client *rpc.Client,
	targetMint solana.PublicKey,
	t time.Time,
) (float64, int, float64, bool, error) {
	return GetTokenUSDPriceAtUnix(ctx, client, targetMint, t.UTC().Unix(), 0, 1.5, 1e-6)
}
