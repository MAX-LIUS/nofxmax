"""AUC of each decision-time feature against the immediate-adverse label.

Gate for taking anything seriously (fixed before running, mirroring the PMG study
that established the 0.4999 information ceiling for path features):
  - |AUC - 0.5| must beat the label-shuffle null;
  - must hold with the SAME SIGN in the majority of quarters (sign stability);
  - must be side-appropriate (a feature that only works because it detects "market
    is falling" will show opposite signs on the two sides and is beta, not skill).
"""
import numpy as np, pandas as pd
from scipy import stats
from adverse import auc, FEATS

ev = pd.read_parquet("/root/projects/nofxmax/research/entry-gate/cache/adverse.parquet")
print(f"rows={len(ev)} base={ev.adverse.mean():.4f} quarters={ev.q.nunique()}")

rng = np.random.default_rng(0)
rows = []
for side in ("LONG","SHORT"):
    s = ev[ev.side == side]
    y = s.adverse.to_numpy()
    for f in FEATS:
        x = s[f].to_numpy().astype(float)
        a = auc(x, y)
        # per-quarter AUC for sign stability
        qa = []
        for q, g in s.groupby("q"):
            v = auc(g[f].to_numpy().astype(float), g.adverse.to_numpy())
            if np.isfinite(v):
                qa.append(v)
        qa = np.array(qa)
        frac = float((np.sign(qa - 0.5) == np.sign(a - 0.5)).mean()) if len(qa) else np.nan
        rows.append(dict(side=side, feat=f, auc=a, dev=abs(a-0.5),
                         q_sign_frac=frac, nq=len(qa)))
t = pd.DataFrame(rows)

# Null: shuffle labels within symbol, recompute the max |dev| we would see anyway.
nulls = []
for _ in range(20):
    s = ev[ev.side=="LONG"]
    ysh = s.groupby("sym").adverse.transform(lambda v: pd.Series(rng.permutation(v.values), index=v.index))
    devs = [abs(auc(s[f].to_numpy().astype(float), ysh.to_numpy()) - 0.5) for f in FEATS[:8]]
    nulls.append(np.nanmax(devs))
null_max = float(np.mean(nulls))
print(f"\nlabel-shuffle null: mean max|AUC-0.5| over 8 features = {null_max:.4f}")

print("\n=== top features by |AUC-0.5| ===")
print(t.sort_values("dev", ascending=False).head(16).round(4).to_string(index=False))

print("\n=== side-consistency check (same feature, both sides) ===")
p = t.pivot(index="feat", columns="side", values="auc")
p["both_above"] = (p.LONG > 0.5) & (p.SHORT > 0.5)
p["both_below"] = (p.LONG < 0.5) & (p.SHORT < 0.5)
p["consistent"] = p.both_above | p.both_below
p["min_dev"] = np.minimum((p.LONG-0.5).abs(), (p.SHORT-0.5).abs())
print(p.sort_values("min_dev", ascending=False).head(12).round(4).to_string())
print("\n(consistent=True means the feature says the same thing for both sides =>")
print(" it is about TRADE QUALITY. Inconsistent => it is a market-direction read.)")
