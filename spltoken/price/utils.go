package price

import (
	"context"
	"errors"
	"github.com/gagliardetto/solana-go/rpc"
	"math"
)

// ---------- small helpers ----------

func absI64(x int64) int64 {
	if x < 0 {
		return -x
	}
	return x
}

// cappedAdd returns a+b but clamps within [0, max].
func cappedAdd(a uint64, b int64, max uint64) uint64 {
	if b == 0 {
		return a
	}
	if b > 0 {
		u := a + uint64(b)
		if u < a { // overflow
			return max
		}
		if u > max {
			return max
		}
		return u
	}
	// b < 0
	nb := uint64(-b)
	if nb > a {
		return 0
	}
	return a - nb
}

// ---------- main logic ----------

// SlotAtClosest finds the slot whose block-time is closest to targetUnix.
// It returns best, optional tie, and an error.
// The search never uses firstSlot; instead it brackets around an estimated guess
// with an adaptive window that doubles until time(lo) <= target <= time(hi).
func SlotAtClosest(ctx context.Context, client *rpc.Client, targetUnix int64, maxProbes int) (best uint64, tie *uint64, err error) {
	if maxProbes <= 0 {
		maxProbes = 1024
	}

	// 0) Edge: now slot/time
	nowSlot, err := client.GetSlot(ctx, rpc.CommitmentFinalized)
	if err != nil {
		return 0, nil, err
	}
	btNowPtr, err := client.GetBlockTime(ctx, nowSlot)
	if err != nil || btNowPtr == nil {
		return 0, nil, errors.New("failed to read block time for latest finalized slot")
	}
	btNow := int64(*btNowPtr)

	if targetUnix <= 0 {
		targetUnix = btNow
	}
	if targetUnix >= btNow {
		// Target in the future (or exactly now): closest is nowSlot.
		return nowSlot, nil, nil
	}

	// 1) Estimate slots/sec and an initial guess near target time.
	guess, sps := estimateSlotGuess(ctx, client, nowSlot, btNow, targetUnix)

	// 2) Build an initial window around guess using a % of lookback.
	lookbackSec := float64(btNow - targetUnix)
	if lookbackSec < 0 {
		lookbackSec = 0
	}
	// Base window seconds: max( fewSeconds , 5% * lookbackSec )
	const fewSeconds = 5.0
	winSec := lookbackSec * 0.05
	if winSec < fewSeconds {
		winSec = fewSeconds
	}
	// Convert to slots (respect a minimum slots window).
	minSlotsWindow := uint64(200) // floor to avoid tiny ranges for extreme near-now
	spanSlots := uint64(math.Round(sps * winSec))
	if spanSlots < minSlotsWindow {
		spanSlots = minSlotsWindow
	}
	// Clamp span so we stay inside [0, nowSlot].
	if spanSlots > nowSlot {
		spanSlots = nowSlot
	}

	// Probe budget & getBlockTime helper.
	getBT := func(slot uint64) (int64, bool) {
		if maxProbes <= 0 {
			return 0, false
		}
		maxProbes--
		ptr, err := client.GetBlockTime(ctx, slot)
		if err != nil || ptr == nil {
			return 0, false
		}
		return int64(*ptr), true
	}

	// 3) Try to resolve guess time (may be nil on pruned RPCs).
	tGuess, okGuess := getBT(guess)
	if !okGuess {
		// Light local nudge to find a nearby slot with time.
		for _, step := range []uint64{50, 200, 1000, 5000} {
			if guess >= step {
				if tg, ok := getBT(guess - step); ok {
					tGuess, okGuess = tg, true
					guess = guess - step
					break
				}
			}
			if guess+step <= nowSlot {
				if tg, ok := getBT(guess + step); ok {
					tGuess, okGuess = tg, true
					guess = guess + step
					break
				}
			}
		}
	}

	// 4) Adaptive bracketing:
	//    Start with [guess - spanSlots, guess + spanSlots],
	//    expand by doubling span until we satisfy time(lo) <= target <= time(hi).
	var lo, hi uint64
	var tLoOK, tHiOK bool
	var tLo, tHi int64

	expand := func(span uint64) (ok bool) {
		lo = cappedAdd(guess, -int64(span), nowSlot)
		hi = cappedAdd(guess, +int64(span), nowSlot)

		// Try to get times at lo/hi; if nil, nudge locally a bit.
		if tt, ok := getBT(lo); ok {
			tLo, tLoOK = tt, true
		} else {
			// small forward nudge
			for _, step := range []uint64{1_000, 5_000, 25_000} {
				if lo+step <= hi {
					if tt, ok := getBT(lo + step); ok {
						lo += step
						tLo, tLoOK = tt, true
						break
					}
				}
			}
		}
		if tt, ok := getBT(hi); ok {
			tHi, tHiOK = tt, true
		} else {
			// small backward nudge
			for _, step := range []uint64{1_000, 5_000, 25_000} {
				if hi > lo+step {
					if tt, ok := getBT(hi - step); ok {
						hi -= step
						tHi, tHiOK = tt, true
						break
					}
				}
			}
		}
		if !tLoOK && !tHiOK {
			return false
		}
		// If只有一侧有时间，也可能马上满足边界（下面会再处理）
		return true
	}

	// First attempt with initial window.
	if okGuess {
		// If we already know side, shrink one edge:
		if tGuess >= targetUnix {
			lo, hi = 0, guess
			tLoOK, tHiOK = false, true
			tHi = tGuess
		} else {
			lo, hi = guess, nowSlot
			tLoOK, tHiOK = true, false
			tLo = tGuess
		}
	} else {
		// No guess time; start with symmetric window.
		_ = expand(spanSlots)
	}

	// Expand until we bracket target or run out of probes.
	// Bracket condition（在两端都有时间时）：tLo <= target <= tHi。
	for tries := 0; tries < 32 && maxProbes > 0; tries++ {
		// Ensure lo/hi have times by expanding if needed.
		if !tLoOK || !tHiOK {
			if !expand(spanSlots) {
				// If even expansion can’t get either side, just break and fallback later.
				break
			}
		}

		// If both ends have time, check if already bracketed.
		if tLoOK && tHiOK {
			// Order endpoints if mis-ordered.
			if hi < lo {
				lo, hi = hi, lo
				tLo, tHi = tHi, tLo
			}
			if tLo <= targetUnix && targetUnix <= tHi {
				break // bracketed!
			}
			// Otherwise move window outward depending on which side target falls.
			if targetUnix < tLo {
				// move window backward
				newSpan := spanSlots * 2
				if newSpan == 0 {
					newSpan = spanSlots + 1
				}
				spanSlots = newSpan
				// centered on lo
				guess = lo
				tGuess = tLo
				okGuess = true
				tLoOK, tHiOK = false, false
				continue
			} else if targetUnix > tHi {
				// move window forward
				newSpan := spanSlots * 2
				if newSpan == 0 {
					newSpan = spanSlots + 1
				}
				spanSlots = newSpan
				// centered on hi
				guess = hi
				tGuess = tHi
				okGuess = true
				tLoOK, tHiOK = false, false
				continue
			}
		} else {
			// Only one side had time; double span to try to obtain the other side.
			newSpan := spanSlots * 2
			if newSpan == 0 {
				newSpan = spanSlots + 1
			}
			spanSlots = newSpan
			continue
		}
	}

	// If we still don’t have both endpoints/bracket, do best-effort from what we have.
	if !(tLoOK && tHiOK) {
		// Try to re-fetch once if budget allows.
		if !tLoOK {
			if tt, ok := getBT(lo); ok {
				tLo, tLoOK = tt, true
			}
		}
		if !tHiOK {
			if tt, ok := getBT(hi); ok {
				tHi, tHiOK = tt, true
			}
		}
		// If只有一端可用，就用那一端；如果都没有，就用guess或now。
		if tLoOK && !tHiOK {
			return lo, nil, nil
		}
		if tHiOK && !tLoOK {
			return hi, nil, nil
		}
		if okGuess {
			return guess, nil, nil
		}
		return nowSlot, nil, nil
	}

	// 5) Binary search within [lo, hi] using times.
	//    Robust to occasional nils by nudging a bit.
	for iter := 0; lo+1 < hi && iter < 64 && maxProbes > 0; iter++ {
		mid := lo + (hi-lo)/2
		tMid, okMid := getBT(mid)
		if !okMid {
			// Nudge to nearest direction where we have time at endpoints.
			// Pick side by comparing |mid-lo| vs |hi-mid| in slots (drift toward closer side).
			if (mid - lo) <= (hi - mid) {
				// lean to lo side
				if mid > lo {
					hi = mid
					tHi, tHiOK = tMid, false
				} else {
					lo = mid
					tLo, tLoOK = tMid, false
				}
			} else {
				// lean to hi side
				if hi > mid {
					lo = mid
					tLo, tLoOK = tMid, false
				} else {
					hi = mid
					tHi, tHiOK = tMid, false
				}
			}
			continue
		}
		if tMid == targetUnix {
			// Perfect hit; check neighbor for tie.
			best = mid
			if mid+1 <= hi {
				if tNext, okN := getBT(mid + 1); okN && absI64(tNext-targetUnix) == 0 {
					tie = new(uint64)
					*tie = mid + 1
				}
			}
			return best, tie, nil
		}
		if tMid < targetUnix {
			lo, tLo, tLoOK = mid, tMid, true
		} else {
			hi, tHi, tHiOK = mid, tMid, true
		}
	}

	// 6) Pick the closer endpoint; handle exact tie.
	dLo := absI64(tLo - targetUnix)
	dHi := absI64(tHi - targetUnix)
	if dLo < dHi {
		return lo, nil, nil
	}
	if dHi < dLo {
		return hi, nil, nil
	}
	// equidistant
	res := lo
	ti := hi
	return res, &ti, nil
}

// estimateSlotGuess computes a first guess at the target slot and returns (guessSlot, slotsPerSecondUsed).
// It prefers GetRecentPerformanceSamples; fallback is ~2.5 slots/sec.
func estimateSlotGuess(ctx context.Context, client *rpc.Client, nowSlot uint64, btNow int64, targetUnix int64) (guess uint64, sps float64) {
	limit := uint(60)
	samples, err := client.GetRecentPerformanceSamples(ctx, &limit)
	if err == nil && len(samples) > 0 {
		var slots uint64
		var secs uint64
		for _, s := range samples {
			secs += uint64(s.SamplePeriodSecs)
			slots += uint64(s.NumSlots)
		}
		if secs > 0 && slots > 0 {
			sps = float64(slots) / float64(secs) // slots per second
		}
	}

	if sps == 0 {
		sps = 2.5 // fallback heuristic
	}

	dt := float64(btNow - targetUnix)
	if dt < 0 {
		dt = 0
	}
	est := float64(nowSlot) - sps*dt
	if est < 0 {
		est = 0
	}
	guess = uint64(math.Round(est))
	// Clamp to [0, nowSlot]
	if guess > nowSlot {
		guess = nowSlot
	}
	return guess, sps
}
