"""Re-do everything in R units, because the live book is risk-budget sized.

WHY THIS CORRECTION MATTERS
===========================
Per-notional bps is the wrong yardstick for a book that sizes positions as
  size = risk_budget / stop_distance
because then a wider stop automatically buys a smaller position. Under that
sizing, what lands in the P&L is the return divided by the stop distance — the
R multiple — not the raw percentage move.

Measured in bps, "high ATR is worse" is arithmetically forced: geometry scales
with ATR, so a negative per-ATR expectancy times a bigger ATR is a bigger
negative number. It says nothing about market state. In R units that effect
vanished entirely (verified in mech.py), which is how I caught it.

So: R units for everything from here. Costs must also be converted, and this is
where the real asymmetry lives — a fixed 14bps round-trip cost is a LARGER
fraction of R when ATR is small. Cost drag in R = 14bps / (1.5 * atr_bps).
"""
from __future__ import annotations
import numpy as np, pandas as pd
from scipy import stats
import evalgate as E

STOP_ATR = 1.5
COST_BPS = 14.0


def load_r() -> pd.DataFrame:
    ev = E.load()
    atr_bps = (ev.atr_pct.to_numpy() / 100.0) * 10000.0
    r_unit = STOP_ATR * atr_bps                       # bps per 1R
    # Guard: a near-zero ATR makes R explode. Require 1R >= 20bps, which is
    # ~1.4x the round-trip cost — below that the trade is uneconomic anyway.
    ok = np.isfinite(r_unit) & (r_unit >= 20.0)
    ev = ev[ok].copy()
    r_unit = r_unit[ok]
    ev["r_unit_bps"] = r_unit
    ev["long_R"] = ev.long_bps.to_numpy() / r_unit
    ev["short_R"] = ev.short_bps.to_numpy() / r_unit
    ev["coin_R"] = 0.5 * (ev.long_R + ev.short_R)
    ev["cost_R"] = COST_BPS / r_unit
    return ev


if __name__ == "__main__":
    ev = load_r()
    print(f"events={len(ev)}  quarters={ev.q.nunique()}")
    print(f"1R in bps: p10={np.percentile(ev.r_unit_bps,10):.0f} "
          f"p50={np.percentile(ev.r_unit_bps,50):.0f} "
          f"p90={np.percentile(ev.r_unit_bps,90):.0f}")
    print(f"cost drag in R: p10={np.percentile(ev.cost_R,10):.4f} "
          f"p50={np.percentile(ev.cost_R,50):.4f} "
          f"p90={np.percentile(ev.cost_R,90):.4f}")
    print(f"\nbaseline: long={ev.long_R.mean():+.4f}R short={ev.short_R.mean():+.4f}R "
          f"coin={ev.coin_R.mean():+.4f}R")

    sub = ev.copy()
    sub["a3"] = pd.qcut(sub.groupby("sym").adx14.rank(pct=True), 3,
                        labels=["adx_lo","adx_mid","adx_hi"])
    sub["v3"] = pd.qcut(sub.groupby("sym").atr_pct.rank(pct=True), 3,
                        labels=["atr_lo","atr_mid","atr_hi"])
    g = sub.groupby(["q","a3","v3"], observed=True).coin_R.mean().groupby(["a3","v3"]).mean().unstack()
    print("\n=== coin_R by (adx x atr) grid — the bps effect should be GONE ===")
    print(g.round(4).to_string())

    print("\n=== cost drag in R by the same grid (this is where the real gradient is) ===")
    gc = sub.groupby(["q","a3","v3"], observed=True).cost_R.mean().groupby(["a3","v3"]).mean().unstack()
    print(gc.round(4).to_string())
