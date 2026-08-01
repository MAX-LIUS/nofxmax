"""Quantify the cost-drag gradient and test it as a gate.

In R units the dominant gradient across every grid I built is not a market-state
effect at all — it is the fixed round-trip cost divided by the stop distance.
That is mechanical, knowable at decision time, and has nothing to do with
forecasting. Which makes it the most promising gate lever found so far.

The rule under test: require 1R to be a sufficient multiple of the round-trip
cost. Equivalently, require the stop distance to be wide enough that costs are a
small fraction of the risk being taken.
"""
import numpy as np, pandas as pd
from scipy import stats
import runits, evalgate as E

ev = runits.load_r()
print(f"events={len(ev)} quarters={ev.q.nunique()}")
print(f"baseline coin={ev.coin_R.mean():+.4f}R long={ev.long_R.mean():+.4f}R short={ev.short_R.mean():+.4f}R")

# Gross of costs, to separate "market" from "friction".
ev["coin_R_gross"] = ev.coin_R + ev.cost_R
print(f"gross of costs: coin={ev.coin_R_gross.mean():+.4f}R\n")

ev["cost_mult"] = ev.r_unit_bps / runits.COST_BPS      # how many costs fit in 1R
print("=== outcome by cost-multiple decile (1R / round-trip cost) ===")
ev["d"] = pd.qcut(ev.cost_mult, 10, labels=False, duplicates="drop")
t = ev.groupby(["q","d"]).agg(net=("coin_R","mean"), gross=("coin_R_gross","mean"),
                              drag=("cost_R","mean")).groupby("d").mean()
t["r_bps"] = ev.groupby("d").r_unit_bps.median()
print(t.round(4).to_string())
rho_net = stats.spearmanr(t.index, t.net).statistic
rho_gr  = stats.spearmanr(t.index, t.gross).statistic
print(f"\nmonotonicity: net rho={rho_net:+.2f}  gross rho={rho_gr:+.2f}")
print("(net rising while gross is flat/falling => the gradient IS the cost, not the market)")
