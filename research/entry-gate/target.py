"""The last untested axis, and the only one that is a SETTING rather than a forecast.

Everything predictive has failed against a ~0.50 AUC ceiling (score.py 0.503
walk-forward; probation2.py 0.486-0.512 post-entry). But two things are not
forecasts, they are configuration:

  1. the stop width      -> geomwide.py: monotone friction gain, optimum ~4 ATR,
                            gross curve U-shaped, gross deltas individually n.s.
  2. the TARGET distance -> untested as a lever in its own right

multi.parquet swept 6 target multiples. If expectancy varies systematically and
monotonically with the target, that is directly actionable: it needs no prediction,
only a config change, and it is measurable per quarter.

Live constraint to respect: MinRiskRewardRatio=3.0 (AI guided), MaxNetRR=2.8,
MinRewardATRMul=1.8, MaxTargetATRMul=5.0.
"""
import numpy as np, pandas as pd
from scipy import stats
import evalgate as E

ev = pd.read_parquet("/root/projects/nofxmax/research/entry-gate/cache/multi.parquet")
print(f"rows={len(ev)} quarters={ev.q.nunique()}  cols={list(ev.columns)}")

key = "targ_atr" if "targ_atr" in ev.columns else None
if key is None:
    for c in ("targ", "target_atr", "tmul", "target"):
        if c in ev.columns:
            key = c; break
print(f"target column = {key}")

print("\n=== expectancy by target multiple (quarter-equal-weighted) ===")
g = ev.groupby([ "q", key], observed=True).coin_R.mean().groupby(key).agg(
    ["mean", "std", "count"])
g.columns = ["qmeanR", "q_std", "nq"]
g["meanR"] = ev.groupby(key).coin_R.mean()
g["n"] = ev.groupby(key).size()
g["win%"] = 100 * ev.groupby(key).coin_R.apply(lambda s: (s > 0).mean())
print(g.round(4).to_string())

print("\n=== paired per-quarter, each target vs the smallest ===")
piv = ev.groupby(["q", key], observed=True).coin_R.mean().unstack()
base = piv.columns.min()
rows = []
for c in piv.columns:
    if c == base:
        continue
    d = (piv[c] - piv[base]).dropna()
    fr, p, _, n = E.sign_test(d)
    rows.append(dict(target=c, delta_vs_min=d.mean(), frac_pos=fr,
                     binom_p=p, worst_q=d.min(), nq=n))
print(pd.DataFrame(rows).set_index("target").round(4).to_string())

print("\n=== step-by-step monotonicity ===")
cols = sorted(piv.columns)
for a, b in zip(cols, cols[1:]):
    d = (piv[b] - piv[a]).dropna()
    fr, p, _, _ = E.sign_test(d)
    print(f"  {a} -> {b}:  delta={d.mean():+.4f}  fracpos={fr:.2f}  p={p:.4f}")
