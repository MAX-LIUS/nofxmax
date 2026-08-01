"""Is the adverse-label signal just forward-volatility misestimation?

The label is "reaches -1.0R before +0.5R", with both levels scaled by the ATR
KNOWN AT DECISION TIME. Under a pure random walk, scaling both levels by the same
number leaves the probability unchanged — so uniform volatility change cannot
explain the signal.

But if decision-time ATR systematically UNDERSTATES forward volatility, the path
becomes rougher relative to the levels, and roughness favours hitting the far
level before the near one is confirmed... which is testable directly.

So: regress the adverse label on the features, then repeat CONTROLLING for
realized forward volatility. If the AUC collapses, the features were a volatility
proxy and there is no trade-quality signal.
"""
import numpy as np, pandas as pd
from scipy import stats
import bars, events as EV
from adverse import auc

MAX_HOLD = 48

def fwd_vol_ratio(sym):
    """realized forward ATR over the holding window / decision-time ATR."""
    f = bars.load_symbol(sym)
    if f is None or len(f) < 400:
        return None
    feats = EV.build_features(f)
    atr = feats.pop("_atr")
    h = f["high"].to_numpy(); l = f["low"].to_numpy(); c = f["close"].to_numpy()
    tr = np.full(len(c), np.nan)
    pc = np.roll(c, 1)
    tr[1:] = np.maximum(h[1:]-l[1:], np.maximum(np.abs(h[1:]-pc[1:]), np.abs(l[1:]-pc[1:])))
    n = len(c)
    idx = np.arange(200, n - MAX_HOLD - 2, EV.STRIDE)
    ftr = EV._windows(tr, idx+1, MAX_HOLD)
    fwd = np.nanmean(ftr, axis=1)
    return pd.DataFrame({"sym": sym, "t": f.index.to_numpy()[idx],
                         "vol_ratio": fwd / atr[idx]})

ev = pd.read_parquet("/root/projects/nofxmax/research/entry-gate/cache/adverse.parquet")
syms = sorted(ev.sym.unique())
frames = [fwd_vol_ratio(s) for s in syms]
vr = pd.concat([x for x in frames if x is not None], ignore_index=True)
ev = ev.merge(vr, on=["sym","t"], how="left")
ev = ev[np.isfinite(ev.vol_ratio)]
print(f"rows={len(ev)}  vol_ratio p10={np.percentile(ev.vol_ratio,10):.2f} "
      f"p50={np.percentile(ev.vol_ratio,50):.2f} p90={np.percentile(ev.vol_ratio,90):.2f}")

print(f"\nAUC of vol_ratio itself on the adverse label: {auc(ev.vol_ratio.to_numpy(), ev.adverse.to_numpy()):.4f}")
print("(this is FORWARD information — not usable as a gate, shown only to size the confound)")

TOP = ["close_pos_in_bar","body_frac","chop14","er24","rpos30","atr_pct","lower_wick_frac"]
print("\n=== raw AUC vs AUC within vol_ratio deciles (confound-controlled) ===")
ev["vd"] = pd.qcut(ev.vol_ratio, 10, labels=False, duplicates="drop")
for f in TOP:
    raw = auc(ev[f].to_numpy().astype(float), ev.adverse.to_numpy())
    inner = []
    for d, g in ev.groupby("vd"):
        v = auc(g[f].to_numpy().astype(float), g.adverse.to_numpy())
        if np.isfinite(v):
            inner.append(v)
    ctrl = float(np.mean(inner))
    print(f"{f:18s} raw={raw:.4f}  within-vol-decile={ctrl:.4f}  "
          f"shrink={100*(abs(raw-0.5)-abs(ctrl-0.5))/max(abs(raw-0.5),1e-9):+.0f}%")

print("\n=== how much do the features predict vol_ratio itself? (Spearman) ===")
for f in TOP:
    x = ev[f].to_numpy().astype(float)
    ok = np.isfinite(x)
    r = stats.spearmanr(x[ok], ev.vol_ratio.to_numpy()[ok]).statistic
    print(f"{f:18s} rho={r:+.3f}")
