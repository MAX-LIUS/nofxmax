"""What actually changes across the adx x atr grid? Decompose the outcome mix.

A 13bps swing has to come from somewhere. Candidates:
  - win rate shifts (target hit less often);
  - exit composition shifts (more time exits, which land anywhere);
  - the SIZE of wins/losses shifts relative to ATR.
The third would be surprising because geometry is ATR-scaled, so if it appears it
means realized moves are not proportional to ATR — i.e. ATR misestimates forward
risk exactly when it is high. That is a mean-reversion-of-volatility story.
"""
import numpy as np, pandas as pd
import evalgate as E

ev = E.load()
L = ev.long_bps.to_numpy(); S = ev.short_bps.to_numpy()
ev = ev.assign(coin=0.5*(L+S))
sub = ev.dropna(subset=["adx14","atr_pct"]).copy()
sub["a3"] = pd.qcut(sub.groupby("sym").adx14.rank(pct=True), 3, labels=["adx_lo","adx_mid","adx_hi"])
sub["v3"] = pd.qcut(sub.groupby("sym").atr_pct.rank(pct=True), 3, labels=["atr_lo","atr_mid","atr_hi"])

# Classify each leg's exit. Target = +3ATR, stop = -1.5ATR, in bps terms these
# scale with atr_pct, so classify by proximity in ATR units instead.
atrbps = (sub.atr_abs / (sub.atr_abs/ (sub.atr_pct/100.0))) * 10000.0  # = atr_pct*100 bps
atrbps = (sub.atr_pct.to_numpy()/100.0) * 10000.0
for leg, col in (("L","long_bps"), ("S","short_bps")):
    r = sub[col].to_numpy() + 14.0                    # add costs back -> gross
    sub[f"{leg}_atr_units"] = r / np.maximum(atrbps, 1e-9)

print("=== gross outcome in ATR units, by grid cell (target=+3.0, stop=-1.5) ===")
for lab, col in (("LONG","L_atr_units"), ("SHORT","S_atr_units")):
    g = sub.groupby(["q","a3","v3"], observed=True)[col].mean().groupby(["a3","v3"]).mean().unstack()
    print(f"\n{lab} mean ATR units:")
    print(g.round(3).to_string())

print("\n=== target-hit rate (>= +2.9 ATR) and stop-rate (<= -1.45 ATR), LONG leg ===")
sub["L_win"] = sub.L_atr_units >= 2.9
sub["L_stop"] = sub.L_atr_units <= -1.45
sub["L_time"] = (~sub.L_win) & (~sub.L_stop)
for c in ("L_win","L_stop","L_time"):
    g = sub.groupby(["q","a3","v3"], observed=True)[c].mean().groupby(["a3","v3"]).mean().unstack()
    print(f"\n{c}:")
    print(g.round(3).to_string())
