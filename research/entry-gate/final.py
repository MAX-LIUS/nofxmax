"""The recommendation, scoped to the window the live system actually enforces.

Live config: netRR floor 1.5 (FallbackMinRR), ceiling 2.8 (MaxNetRR).
So the only defensible claim is about events inside that window. Anything about
netRR < 1.5 is outside the operating envelope and, more importantly, my single-
target 48-bar simulation is not a faithful model of the live ladder-TP exit, so a
"lower the RR floor" recommendation would not be supported by this evidence.
"""
import numpy as np, pandas as pd
from scipy import stats
import evalgate as E

ev = pd.read_parquet("/root/projects/nofxmax/research/entry-gate/cache/multi.parquet")
win = ev[(ev.netRR >= 1.5) & (ev.netRR <= 2.8)].copy()
print(f"events inside live netRR window [1.5, 2.8]: {len(win)} ({len(win)/len(ev):.1%} of all)")
print(f"K distribution here: p10={np.percentile(win.K,10):.1f} p25={np.percentile(win.K,25):.1f} "
      f"p50={np.percentile(win.K,50):.1f} p75={np.percentile(win.K,75):.1f} p90={np.percentile(win.K,90):.1f}")

base_q = win.groupby("q").coin_R.mean()
print(f"\nbaseline inside window: {base_q.mean():+.4f}R/trade\n")

print("=== gate: additionally require K >= threshold ===")
rows = []
for K in (6, 8, 10, 12, 14, 16, 20):
    keep = win.K >= K
    if keep.mean() < 0.05:
        continue
    q = win[keep].groupby("q").coin_R.mean()
    d = (q - base_q).dropna()
    fr, p, npos, n = E.sign_test(d)
    rows.append(dict(K_min=K, pass_rate=keep.mean(), kept_R=q.mean(), delta_R=d.mean(),
                     frac_pos=fr, binom_p=p, worst_q=d.min(), nq=n))
t = pd.DataFrame(rows).set_index("K_min")
print(t.round(4).to_string())

print("\n=== what K threshold corresponds to in stop-distance terms ===")
print("K = (1.5 x ATR_bps) / 14bps, so required ATR_pct for a given K:")
for K in (6, 10, 12, 16):
    atr_bps_req = K * 14.0 / 1.5
    print(f"  K>={K:2d} -> 1R >= {K*14:.0f}bps -> ATR14 >= {atr_bps_req/100:.2f}% of price")
print(f"\nlive gate MinRiskDistancePct default = 0.4% -> 1R >= 40bps -> K = {40/14:.2f}")
print(f"live gate MinATR14Pct default = 1.2%  -> 1R >= {1.5*120:.0f}bps -> K = {1.5*120/14:.1f}")
