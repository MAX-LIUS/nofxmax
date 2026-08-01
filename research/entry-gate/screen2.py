"""Job (b) reframed: is there a STATE in which BOTH sides do worse?

This is the abstention question, and it is direction-free by construction: we
score the coin baseline 0.5*(long+short). A feature that predicts it is telling
us "trading here is bad regardless of which way you face" — which is exactly the
thing a gate can act on without needing to forecast market direction.

Reported per quintile so we can check MONOTONICITY, not just a top-vs-bottom gap.
A non-monotonic "significant" gap is the funding-rate trap from the last round.
"""
import numpy as np, pandas as pd
from scipy import stats
import evalgate as E

ev = E.load()
coin = 0.5 * (ev.long_bps.to_numpy() + ev.short_bps.to_numpy())
ev = ev.assign(coin=coin)
print(f"overall coin baseline: {coin.mean():.2f} bps/trade  (costs = {2*(5.0+2.0):.0f} bps)")
print(f"gross of costs:        {coin.mean()+14:.2f} bps/trade\n")

FEATS = ["er24","er72","adx14","chop14","atr_pct","atr_rank168","vol_z72",
         "rpos30","rpos72","body_frac","ema20_dev","ret24_atr","taker_imb"]

for name in FEATS:
    x = ev[name].to_numpy().astype(float)
    ok = np.isfinite(x)
    sub = ev[ok].copy()
    xv = x[ok]
    if name == "ema20_dev" or name == "ret24_atr":
        xv = np.abs(xv)          # magnitude of stretch, sign-free
    try:
        qcut = pd.qcut(xv, 5, labels=False, duplicates="drop")
    except Exception:
        continue
    sub["bin"] = qcut
    # Per-quarter, per-bin means then averaged over quarters (equal weight).
    piv = sub.groupby(["q","bin"]).coin.mean().unstack()
    qm = piv.mean()
    # Monotonicity across bins.
    rho = stats.spearmanr(np.arange(len(qm)), qm.to_numpy()).statistic
    # Top-vs-bottom, tested per quarter (paired).
    d = (piv[piv.columns[-1]] - piv[piv.columns[0]]).dropna()
    tp = stats.ttest_1samp(d, 0.0).pvalue if len(d) > 2 else np.nan
    lbl = name + ("(|.|)" if name in ("ema20_dev","ret24_atr") else "")
    print(f"{lbl:16s} " + " ".join(f"{v:8.2f}" for v in qm.to_numpy())
          + f" | rho={rho:+.2f} top-bot={d.mean():+7.2f} p={tp:.4f}")
