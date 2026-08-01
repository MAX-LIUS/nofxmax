"""Are adx14 and atr_pct the same signal? And what is the mechanism?

If they are collinear we keep one. If independent, a joint gate is stronger than
either. Mechanism matters because a gate we cannot explain is a gate we cannot
trust out of sample.
"""
import numpy as np, pandas as pd
from scipy import stats
import evalgate as E

ev = E.load()
ev = ev.assign(coin=0.5*(ev.long_bps+ev.short_bps))
sub = ev.dropna(subset=["adx14","atr_pct","er24"]).copy()
for c in ("adx14","atr_pct","er24"):
    sub[c+"_r"] = sub.groupby("sym")[c].rank(pct=True)

print("=== within-symbol rank correlations ===")
print(sub[["adx14_r","atr_pct_r","er24_r"]].corr().round(3).to_string())

# 3x3 joint grid on adx x atr, quarter-equal-weighted.
sub["a3"] = pd.qcut(sub.adx14_r, 3, labels=["adx_lo","adx_mid","adx_hi"])
sub["v3"] = pd.qcut(sub.atr_pct_r, 3, labels=["atr_lo","atr_mid","atr_hi"])
piv = sub.groupby(["q","a3","v3"], observed=True).coin.mean().groupby(["a3","v3"]).mean().unstack()
print("\n=== joint grid: coin bps by (adx rank x atr rank), quarter-equal-weighted ===")
print(piv.round(2).to_string())

# Mechanism: decompose into stop-rate and target-rate by ATR bucket.
# A whipsaw regime should show BOTH sides stopping more often.
both_stop = (sub.long_bps < -140) & (sub.short_bps < -140)   # ~ -1.5ATR both legs
sub["both_stop"] = both_stop
print("\n=== mechanism: double-stop (whipsaw) rate by atr rank tercile ===")
m = sub.groupby(["q","v3"], observed=True).both_stop.mean().groupby("v3").mean()
print(m.round(4).to_string())
print("\n=== and by adx rank tercile ===")
m2 = sub.groupby(["q","a3"], observed=True).both_stop.mean().groupby("a3").mean()
print(m2.round(4).to_string())
