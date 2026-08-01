"""Does the K effect survive the realistic ladder exit? This is the load-bearing test."""
import numpy as np, pandas as pd
from scipy import stats
import evalgate as E

ev = pd.read_parquet("/root/projects/nofxmax/research/entry-gate/cache/ladder.parquet")
print(f"rows={len(ev)} quarters={ev.q.nunique()}")
print(f"baseline coin={ev.coin_R.mean():+.4f}R  long={ev.long_R.mean():+.4f}R short={ev.short_R.mean():+.4f}R")
print(f"K: p10={np.percentile(ev.K,10):.1f} p50={np.percentile(ev.K,50):.1f} p90={np.percentile(ev.K,90):.1f}\n")

base_q = ev.groupby("q").coin_R.mean()
print("=== K gate under LADDER exits ===")
rows=[]
for K in (6, 8, 10, 12, 14, 16, 20):
    keep = ev.K >= K
    if keep.mean() < 0.05: continue
    q = ev[keep].groupby("q").coin_R.mean()
    d = (q - base_q).dropna()
    fr,p,npos,n = E.sign_test(d)
    rows.append(dict(K_min=K, pass_rate=keep.mean(), kept_R=q.mean(), delta_R=d.mean(),
                     frac_pos=fr, binom_p=p, worst_q=d.min()))
print(pd.DataFrame(rows).set_index("K_min").round(4).to_string())

# Decompose: is it still friction, or does the ladder change the story?
ev["cost_R"] = 14.0/ev.r_unit_bps
ev["gross_R"] = ev.coin_R + ev.cost_R
bq = ev.groupby("q").agg(net=("coin_R","mean"), gross=("gross_R","mean"), drag=("cost_R","mean"))
print("\n=== decomposition under ladder ===")
rows=[]
for K in (8, 10, 12, 16):
    keep = ev.K >= K
    kq = ev[keep].groupby("q").agg(net=("coin_R","mean"), gross=("gross_R","mean"), drag=("cost_R","mean"))
    d = (kq-bq).dropna()
    rows.append(dict(K_min=K, d_net=d.net.mean(), d_gross=d.gross.mean(), d_drag=d.drag.mean(),
                     net_p=E.sign_test(d.net)[1], gross_p=E.sign_test(d.gross)[1]))
print(pd.DataFrame(rows).set_index("K_min").round(4).to_string())

# And the long/short asymmetry under ladder — does TP1 change it?
print("\n=== per-quarter legs under ladder ===")
g = ev.groupby("q").agg(long=("long_R","mean"), short=("short_R","mean"))
g["short_minus_long"] = g["short"] - g["long"]
fr,p,npos,n = E.sign_test(g.short_minus_long)
print(f"short-minus-long: mean={g.short_minus_long.mean():+.4f}R positive {npos}/{n} binom_p={p:.4f}")
