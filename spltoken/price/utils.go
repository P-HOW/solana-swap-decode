package price

import (
	"context"
	"errors"
	"math"
	"sort"
	"time"

	"github.com/gagliardetto/solana-go/rpc"
)

// avgSlotDuration is Solana’s nominal block time used only for an initial guess.
const avgSlotDuration = 400 * time.Millisecond

// SlotAtClosest finds the slot whose block time is closest to the given UNIX timestamp (seconds).
// If two slots are exactly equidistant in time (extremely rare), tie will be non-nil and hold the
// “other” slot; otherwise tie is nil. searchRadius caps the outward probing around the initial guess.
//
// Strategy:
//  1. Seed with a guess using the nominal 400ms slot time.
//  2. Probe outward from the guess (guess, guess-1, guess+1, guess-2, guess+2, …) up to searchRadius.
//  3. Track the slot with the minimal |blockTime - tUnix|. If a probe has nil blockTime (skipped),
//     it is ignored. Stop early if the absolute time deltas stop improving for a while.
//
// This keeps logic simple, avoids brittle binary-search pitfalls with skipped slots,
// and is RPC-safe for modest radii.
func SlotAtClosest(ctx context.Context, c *rpc.Client, tUnix int64, searchRadius uint64) (best uint64, tie *uint64, err error) {
	if c == nil {
		return 0, nil, errors.New("SlotAtClosest: nil rpc client")
	}

	// 1) Current finalized slot & time
	sNow, err := c.GetSlot(ctx, rpc.CommitmentFinalized)
	if err != nil {
		return 0, nil, err
	}
	btNowPtr, err := c.GetBlockTime(ctx, sNow)
	if err != nil {
		return 0, nil, err
	}
	if btNowPtr == nil {
		// Extremely rare for the current finalized slot; just step back until we find one.
		var found bool
		for back := uint64(1); back <= 512; back++ {
			if sNow < back {
				break
			}
			bt, e := c.GetBlockTime(ctx, sNow-back)
			if e == nil && bt != nil {
				sNow -= back
				btNowPtr = bt
				found = true
				break
			}
		}
		if !found || btNowPtr == nil {
			return 0, nil, errors.New("SlotAtClosest: could not find non-nil time for latest slot")
		}
	}
	btNow := int64(*btNowPtr)

	// 2) Initial guess by nominal 400ms per slot (clamp to >= 1)
	//    delta slots ~= (tNow - t) / 0.4s
	deltaSlots := int64((time.Duration(btNow-tUnix) * time.Second) / avgSlotDuration)
	var guess uint64
	if deltaSlots >= 0 {
		if uint64(deltaSlots) >= sNow {
			guess = 1
		} else {
			guess = sNow - uint64(deltaSlots)
		}
	} else {
		// target time is in the future vs btNow; push guess forward
		guess = sNow + uint64(-deltaSlots)
		if guess < sNow { // overflow guard (practically impossible)
			guess = sNow
		}
	}
	if guess == 0 {
		guess = 1
	}

	type cand struct {
		slot uint64
		bt   int64 // unix seconds
		dabs int64 // |bt - tUnix|
	}

	var bestCand *cand
	var secondCand *cand

	// Helper to probe a single slot (ignore if block time is nil or RPC fails).
	probe := func(s uint64) {
		if s == 0 {
			return
		}
		btPtr, e := c.GetBlockTime(ctx, s)
		if e != nil || btPtr == nil {
			return
		}
		bt := int64(*btPtr)
		d := bt - tUnix
		if d < 0 {
			d = -d
		}
		curr := cand{slot: s, bt: bt, dabs: d}
		if bestCand == nil || curr.dabs < bestCand.dabs || (curr.dabs == bestCand.dabs && curr.slot < bestCand.slot) {
			// Track second best for tie reporting
			if bestCand != nil {
				secondCand = bestCand
			}
			bestCand = &curr
			return
		}
		// Maintain second-best (only for exact-tie detection)
		if secondCand == nil || curr.dabs < secondCand.dabs || (curr.dabs == secondCand.dabs && curr.slot < secondCand.slot) {
			// But do not overwrite if it equals the best slot
			if bestCand == nil || curr.slot != bestCand.slot {
				secondCand = &curr
			}
		}
	}

	// 3) Outward probing around the guess
	probe(guess)
	for step := uint64(1); step <= searchRadius; step++ {
		// left
		if guess > step {
			probe(guess - step)
		}
		// right
		probe(guess + step)
	}

	if bestCand == nil {
		return 0, nil, errors.New("SlotAtClosest: no slot with non-nil block time within search radius")
	}

	// Decide exact-tie
	if secondCand != nil && secondCand.dabs == bestCand.dabs && secondCand.slot != bestCand.slot {
		other := secondCand.slot
		return bestCand.slot, &other, nil
	}
	return bestCand.slot, nil, nil
}

// VWAPWithLogFence computes a VWAP after filtering by:
//   - dust threshold on weights (w >= minWeight),
//   - symmetric log fence around the median with parameter r (>1): |ln(p) - ln(median)| <= ln(r).
//
// Returns (vwap, keptCount, weightSum, ok). If ok=false, no points passed the filters.
func VWAPWithLogFence(values []float64, weights []float64, r float64, minWeight float64) (float64, int, float64, bool) {
	n := len(values)
	if n == 0 || n != len(weights) || r <= 1.0 {
		return 0, 0, 0, false
	}

	// 1) dust filter
	type pw struct{ p, w float64 }
	f := make([]pw, 0, n)
	for i := 0; i < n; i++ {
		if !(weights[i] >= minWeight) || math.IsNaN(values[i]) || math.IsInf(values[i], 0) {
			continue
		}
		f = append(f, pw{p: values[i], w: weights[i]})
	}
	if len(f) == 0 {
		return 0, 0, 0, false
	}

	// 2) median of prices (by value, unweighted)
	ps := make([]float64, len(f))
	for i := range f {
		ps[i] = f[i].p
	}
	sort.Float64s(ps)
	var med float64
	m := len(ps)
	if m%2 == 1 {
		med = ps[m/2]
	} else {
		med = 0.5 * (ps[m/2-1] + ps[m/2])
	}
	if med <= 0 || math.IsNaN(med) || math.IsInf(med, 0) {
		return 0, 0, 0, false
	}

	// 3) symmetric log fence
	lnMed := math.Log(med)
	lnR := math.Log(r)
	sumW := 0.0
	sumWP := 0.0
	kept := 0
	for _, x := range f {
		if x.p <= 0 {
			continue
		}
		if math.Abs(math.Log(x.p)-lnMed) <= lnR {
			sumW += x.w
			sumWP += x.w * x.p
			kept++
		}
	}
	if sumW <= 0 {
		return 0, 0, 0, false
	}
	return sumWP / sumW, kept, sumW, true
}
