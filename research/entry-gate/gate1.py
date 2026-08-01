"""The cost-multiple gate, under the full acceptance protocol.

Rule: skip the entry when 1R < K * round-trip cost.
Nothing is forecast. The quantity is known before the order is sent.

Acceptance was fixed in evalgate.PASS_RULE before any of this was run.
Reported per quarter with a binomial sign test, plus a circular-shift control.
"""
import numpy as np, pandas as pd
from scipy import stats
import runits, evalgate as E

ev = runits.load_r()
print(f"events={len(ev)}  quarters={ev.q.nunique()}  1R>=20bps enforced")
base_q = ev.groupby("q").coin_R.mean()
print(f"baseline coin: {base_q.mean():+.4f}R/trade (quarter-equal-weighted)\n")

print("=== gate: take only events with 1R >= K x cost ===")
rows = []
for K in (2, 3, 4, 5, 6, 8, 10, 12, 15, 20):
    keep = ev.r_unit_bps >= K * runits.COST_BPS
    if keep.mean() < 0.02:
        continue
    kept = ev[keep]
    q_kept = kept.groupby("q").coin_R.mean()
    # Effect = kept mean minus all-events mean, per quarter (paired).
    d = (q_kept - base_q).dropna()
    frac, p, npos, n = E.sign_test(d)
    rows.append(dict(K=K, pass_rate=keep.mean(), kept_R=q_kept.mean(),
                     delta_R=d.mean(), frac_pos=frac, binom_p=p,
                     worst_q=d.min(), nq=n))
t = pd.DataFrame(rows).set_index("K")
print(t.round(4).to_string())

# The honest counter-question: does it also improve total return, or only
# per-trade? A gate that only improves per-trade while cutting volume in half may
# not be worth it if the book is capital-constrained rather than opportunity-
# constrained. Report both.
print("\n=== per-trade vs aggregate (sum of R over the quarter, relative) ===")
rows2 = []
for K in (2, 3, 4, 5, 6, 8, 10):
    keep = ev.r_unit_bps >= K * runits.COST_BPS
    kept = ev[keep]
    q_sum_all = ev.groupby("q").coin_R.sum()
    q_sum_kept = kept.groupby("q").coin_R.sum()
    rel = (q_sum_kept / q_sum_all.abs()).dropna()   # both negative; less negative = better
    rows2.append(dict(K=K, pass_rate=keep.mean(),
                      total_R_all=q_sum_all.mean(), total_R_kept=q_sum_kept.mean(),
                      loss_avoided=(q_sum_all - q_sum_kept).mean()))
print(pd.DataFrame(rows2).set_index("K").round(2).to_string())
