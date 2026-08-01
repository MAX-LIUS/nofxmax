package trader

import (
	"sort"
	"strings"

	"nofx/logger"
)

// anchorLadderTakeProfitToEntry rewrites a ladder take-profit plan so that each
// tier's close size is measured against the ORIGINAL entry quantity, and tiers
// that have already been filled (the position was reduced past their cumulative
// size) are dropped instead of being re-placed.
//
// Background (2026-06-07): ladder TP order quantity was computed as
// currentQuantity * closeRatioPct, and the reconciler re-applied the plan every
// cycle once a filled TP order disappeared. On a position that touched TP1 this
// re-fired TP1 against the shrinking remainder every cycle (e.g. WLD short
// 51→31→19→11→7→3→2), grinding the position into a dust tail instead of closing
// 40% once then 35% at the next tier.
//
// This function leaves the StopLossOrders untouched: a single 100% ladder stop
// must always cover the full remaining position and must never be treated as
// "already executed" just because TP reduced the size.
//
// liveTakeProfitPrices is the set of ladder TP prices STILL OPEN on the exchange,
// read from the same fresh snapshot the caller used to decide a tier is missing.
// It is load-bearing for correctness, not a hint — see the two rules below.
//
// Why the quantity arithmetic alone is not enough (2026-07-28, CLUSDT SHORT):
// the caller's position quantity and its open-orders snapshot are two reads of
// the same exchange at DIFFERENT freshness. The reconciler loop reads positions
// once (cached) and re-reads open orders per position, so a tier that filled in
// between shows up as "gone from orders" while the position still reads its
// pre-fill size. That combination is indistinguishable from "the exchange lost
// my order" by quantity alone, and the old code picked the wrong branch: it
// early-returned on entryQuantity <= currentQuantity, kept all four tiers, and
// the caller re-placed the tier that had just filled — at a price the market had
// already crossed, so it filled again seconds later and closed 0.6 extra.
//
// Then the over-close fed back in: with closedQty double-counting tier 1, the
// greedy walk attributed the surplus to tier 2 and declared it executed — while
// tier 2's order was sitting live on the exchange the whole time. The caller
// canceled it as a stale duplicate. A tier that never triggered lost its target.
func anchorLadderTakeProfitToEntry(plan *ProtectionPlan, action string, entryQuantity, currentQuantity float64, liveTakeProfitPrices []float64) {
	if plan == nil || len(plan.TakeProfitOrders) == 0 {
		return
	}
	if entryQuantity <= 0 || currentQuantity <= 0 {
		return
	}

	isLong := action == "open_long"
	isShort := action == "open_short"
	if !isLong && !isShort {
		return
	}

	// Order tiers by fill order: the tier closest to entry fills first.
	// Long TPs sit above entry (lowest price first); short TPs sit below entry
	// (highest price first).
	tiers := make([]ProtectionOrder, len(plan.TakeProfitOrders))
	copy(tiers, plan.TakeProfitOrders)
	sort.SliceStable(tiers, func(i, j int) bool {
		if isLong {
			return tiers[i].Price < tiers[j].Price
		}
		return tiers[i].Price > tiers[j].Price
	})

	closedQty := entryQuantity - currentQuantity
	if closedQty < 0 {
		closedQty = 0
	}
	remainingClosed := closedQty
	kept := make([]ProtectionOrder, 0, len(tiers))
	// Prices of tiers this call decides are already executed. They leave the plan but
	// must stay cancel-immune — see AllowedExtraTakeProfitPrices for why dropping
	// without tolerating turns an inference into a self-fulfilling cancel loop.
	dropped := make([]float64, 0, len(tiers))

	// Rule 2 (applied while walking, below): a tier whose order is STILL LIVE on the
	// exchange has provably not fired, whatever the quantity arithmetic says. It also
	// must not consume any of closedQty — letting it absorb a share is exactly how an
	// over-close on an inner tier got attributed to an outer one and canceled it.
	// Surplus closedQty that no tier can claim is left unattributed on purpose: the
	// position shrank for a reason this function cannot see (manual close, drawdown
	// trailing, an over-close), and guessing a tier for it is what caused the bug.
	//
	// Rule 1 (below): a tier missing from the live snapshot while an OUTER tier is
	// still live has fired. Ladder TPs fill in price order outward from entry, so the
	// market cannot have reached tier k+1's price without passing tier k's. This holds
	// regardless of how stale the position quantity is, which is the whole point — it
	// is the one inference that does not depend on the two snapshots agreeing.
	//
	// A middle tier canceled externally (by hand, or by the exchange) with no fill is
	// read as "fired" by Rule 1 and not re-placed. That is a deliberate false negative:
	// the cost is one TP target, while the alternative — re-placing at a price the
	// market already crossed — closes size that should still be running. The stop-loss
	// ladder is untouched either way, so protection is never what is traded away.
	outerTierLive := make([]bool, len(tiers))
	anyOuterLive := false
	for i := len(tiers) - 1; i >= 0; i-- {
		outerTierLive[i] = anyOuterLive
		if ladderPriceIsLive(liveTakeProfitPrices, tiers[i].Price) {
			anyOuterLive = true
		}
	}

	// When the position has not shrunk (or the DB entry size is stale and reads
	// smaller than live), there is nothing to re-anchor: keep the open-time ratios
	// and let the normal path size them. Rule 1/2 still run — a tier can be gone
	// from the exchange while the position quantity has not caught up yet, which is
	// precisely the case that produced this bug.
	noReduction := entryQuantity <= currentQuantity

	for i, tier := range tiers {
		if tier.CloseRatioPct <= 0 {
			continue
		}
		tierQty := entryQuantity * tier.CloseRatioPct / 100.0

		tierIsLive := ladderPriceIsLive(liveTakeProfitPrices, tier.Price)
		if tierIsLive {
			// Rule 2: provably not fired — keep the tier. But the RATIO must be
			// re-expressed against the CURRENT quantity, exactly like the reduction
			// path below does.
			//
			// 为什么(ZEC ping-pong 的根因):这里原本保留"开仓量口径的原始比例",注释
			// 的理由是"在场单已按开仓量 sized,而调用方按价位判满足所以不会重挂"。
			// 那个理由在 2026-06-22 加入可执行性门禁后失效了 ——
			// validateProtectionPlanExecution 会在 missing/unexpected 判定**之前**
			// 用 `当前量 × 比例` 过一遍交易所最小量,而原始比例配的是开仓量:
			// ZEC 12% 档 → 0.08×12%=0.0096 → 0.96 张 < lotSz 1 → 整档被滤掉 →
			// 不在 allowed 里 → 在场那张 455.40 被判 stale duplicate 撤掉;下一轮它
			// 不在场了,走下面的 reduction 路径算出 0.012/0.08=15% → 1.2 张 → 门禁
			// 通过 → 判 missing → 又挂回来。**在场就变得不可挂、不在场就变得可挂**,
			// 于是每 ~2.5min 撤一张挂一张,无限循环。
			// 换算到当前量口径后两条路径给出同一个比例,循环消失。
			// 注意不会因此重挂在场单:missing 检测只比价位,而数量等价性(v1.16.24)
			// 两边都过 FormatQuantity,0.012 与 0.0096 同为 1 张,交易所分辨不出。
			if !noReduction && currentQuantity > 0 && tierQty > 0 {
				anchoredRatio := tierQty / currentQuantity * 100.0
				if anchoredRatio > 100 {
					anchoredRatio = 100
				}
				tier.CloseRatioPct = anchoredRatio
			}
			kept = append(kept, tier)
			continue
		}

		if outerTierLive[i] {
			// Rule 1: gone from the exchange while a farther tier is still live ⇒ it fired.
			logger.Infof("  🪜 Ladder TP tier already executed (outer tier still live ⇒ price was crossed): price=%.6f ratio=%.1f%% tierQty=%.6f — not re-placing",
				tier.Price, tier.CloseRatioPct, tierQty)
			dropped = append(dropped, tier.Price)
			if remainingClosed > tierQty {
				remainingClosed -= tierQty
			} else {
				remainingClosed = 0
			}
			continue
		}

		filled := remainingClosed
		if filled > tierQty {
			filled = tierQty
		}
		if filled < 0 {
			filled = 0
		}
		remainingClosed -= filled
		unfilled := tierQty - filled

		// Treat the tier as already executed when at least half of it has filled.
		// Mirrors the drawdown native-fill detection tolerance.
		if unfilled <= tierQty*0.5 {
			logger.Infof("  🪜 Ladder TP tier already executed (anchored to entry): price=%.6f ratio=%.1f%% tierQty=%.6f filled=%.6f — not re-placing",
				tier.Price, tier.CloseRatioPct, tierQty, filled)
			dropped = append(dropped, tier.Price)
			continue
		}

		if noReduction {
			kept = append(kept, tier)
			continue
		}

		// Re-express the unfilled remainder as a ratio of the CURRENT position so the
		// downstream executor (quantity * ratio) places exactly the unfilled size.
		anchoredRatio := unfilled / currentQuantity * 100.0
		if anchoredRatio > 100 {
			anchoredRatio = 100
		}
		tier.CloseRatioPct = anchoredRatio
		kept = append(kept, tier)
	}

	plan.TakeProfitOrders = kept
	plan.NeedsTakeProfit = len(kept) > 0
	for _, p := range dropped {
		if p > 0 {
			plan.AllowedExtraTakeProfitPrices = append(plan.AllowedExtraTakeProfitPrices, p)
		}
	}
	if len(kept) == 0 {
		plan.TakeProfitPrice = 0
		logger.Infof("  🪜 All ladder TP tiers already executed; no take-profit re-applied (entry=%.6f current=%.6f closed=%.6f)",
			entryQuantity, currentQuantity, closedQty)
	} else if len(kept) == 1 {
		plan.TakeProfitPrice = kept[0].Price
	}
}

// ladderPriceIsLive reports whether a ladder tier price is still present in the
// live open-order snapshot. It is a membership test, NOT a consuming match: the
// same tier price is asked about twice (once building the outer-tier map, once
// walking), and two distinct tiers of one ladder are never within the price
// tolerance of each other, so consuming would only create order-dependent bugs.
func ladderPriceIsLive(livePrices []float64, tierPrice float64) bool {
	for _, live := range livePrices {
		if approximatelyEqualPrice(live, tierPrice) {
			return true
		}
	}
	return false
}

// liveLadderTakeProfitPrices extracts the take-profit trigger prices still open on
// the exchange for one position side, from the caller's fresh snapshot. Trailing
// orders are excluded: they are the drawdown owner's, priced by callback rather
// than by a ladder tier, and matching one to a tier would be meaningless.
func liveLadderTakeProfitPrices(openOrders []OpenOrder, positionSide string) []float64 {
	prices := make([]float64, 0, len(openOrders))
	for _, order := range openOrders {
		if positionSide != "" && order.PositionSide != "" && !strings.EqualFold(order.PositionSide, positionSide) {
			continue
		}
		if strings.Contains(strings.ToUpper(order.Type), "TRAILING") {
			continue
		}
		if !looksLikeTakeProfit(order) {
			continue
		}
		price := order.StopPrice
		if price <= 0 {
			price = order.Price
		}
		if price > 0 {
			prices = append(prices, price)
		}
	}
	return prices
}

// farthestLadderTakeProfitPrice returns the take-profit tier that fills last
// (furthest from entry): the highest price for longs, the lowest for shorts.
// Used to collapse an all-below-minimum ladder into a single full-position TP.
//
// Why the FURTHEST tier and not the nearest (2026-08-01). Collapsing an
// all-below-minimum ladder means the whole position now exits at one price, so
// the choice is "where does the runner end", not "where is the first scale-out".
// Picking the nearest tier caps the entire position at the first target, which
// on a ZEC-class position (8.276 contracts, 20/18/15/12% ladder) moved the
// dropped share's exit from 3.6 ATR to 1.1 ATR and made the weighted RR WORSE
// than leaving the tier off entirely (-0.035).
//
// This is only safe because the downside is owned by mechanisms that do not
// depend on the TP ladder at all, and they arm well below the furthest target:
// break_even_stop BE1 at 1.3 ATR profit (offset 0.3, close 100%), BE2 at 2.5 ATR,
// drawdown_take_profit dd1 at 2.5 ATR profit with a 1.5 ATR giveback, plus
// giveback_guard breadth at 1.15 ATR. A position that reaches 3.0 ATR and turns
// does not lose the move — dd1 takes it. So reaching for the far target costs
// nothing that is not already protected, while the nearest-tier choice
// permanently forfeits the tail.
//
// Secondary benefit: for a long the far tier sits further ABOVE mark, so it is
// less likely to be already-passed and rejected by the venue (OKX 51279), which
// is the failure the 2026-06-09 executability guard exists to catch.
func farthestLadderTakeProfitPrice(orders []ProtectionOrder, side string) float64 {
	best := 0.0
	for _, o := range orders {
		if o.Price <= 0 {
			continue
		}
		if best == 0 {
			best = o.Price
			continue
		}
		if side == "long" {
			if o.Price > best {
				best = o.Price
			}
		} else {
			if o.Price < best {
				best = o.Price
			}
		}
	}
	return best
}

// tightestLadderStopPrice returns the stop tier closest to current price (the
// tightest protection): the highest stop for longs, the lowest for shorts.
// Used to collapse an all-below-minimum ladder into a single full-position stop.
func tightestLadderStopPrice(orders []ProtectionOrder, side string) float64 {
	best := 0.0
	for _, o := range orders {
		if o.Price <= 0 {
			continue
		}
		if best == 0 {
			best = o.Price
			continue
		}
		if side == "long" {
			if o.Price > best {
				best = o.Price
			}
		} else {
			if o.Price < best {
				best = o.Price
			}
		}
	}
	return best
}
