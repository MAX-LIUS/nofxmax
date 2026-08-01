"""Is the effect ACROSS symbols or WITHIN a symbol over time?

This matters enormously for what we build:
  - cross-sectional  -> the lever is symbol selection ("don't trade this coin");
  - within-symbol     -> the lever is timing ("don't trade this coin right now").
Our gate sits at decision time on a chosen symbol, so only the within-symbol
component is actionable by a gate. The cross-sectional part belongs to coin
screening, which is a different module.

Method: rank each feature WITHIN symbol (so every symbol contributes the same
distribution), and separately demean the outcome within symbol. Any surviving
quintile pattern is pure timing.
"""
import numpy as np, pandas as pd
from scipy import stats
import evalgate as E

ev = E.load()
ev = ev.assign(coin=0.5*(ev.long_bps+ev.short_bps))

FEATS = ["adx14","atr_pct","er24","er72","ret24_atr","ema20_dev","atr_rank168"]

print("=== RAW (mixes symbol-choice with timing) vs WITHIN-SYMBOL (pure timing) ===\n")
for name in FEATS:
    x = ev[name].to_numpy().astype(float)
    if name in ("ema20_dev","ret24_atr"):
        x = np.abs(x)
    sub = ev.assign(x=x).dropna(subset=["x"])

    # Within-symbol percentile rank of the feature.
    sub["xr"] = sub.groupby("sym").x.rank(pct=True)
    # Within-symbol demeaned outcome.
    sub["coin_dm"] = sub.coin - sub.groupby("sym").coin.transform("mean")

    sub["bin"] = pd.qcut(sub.xr, 5, labels=False, duplicates="drop")
    piv_raw = sub.groupby(["q","bin"]).coin.mean().unstack()
    piv_dm  = sub.groupby(["q","bin"]).coin_dm.mean().unstack()

    qr, qd = piv_raw.mean(), piv_dm.mean()
    rr = stats.spearmanr(np.arange(len(qr)), qr.to_numpy()).statistic
    rd = stats.spearmanr(np.arange(len(qd)), qd.to_numpy()).statistic
    dr = (piv_raw[piv_raw.columns[-1]] - piv_raw[piv_raw.columns[0]]).dropna()
    dd = (piv_dm[piv_dm.columns[-1]]  - piv_dm[piv_dm.columns[0]]).dropna()
    pr = stats.ttest_1samp(dr, 0).pvalue
    pd_ = stats.ttest_1samp(dd, 0).pvalue

    lbl = name + ("(|.|)" if name in ("ema20_dev","ret24_atr") else "")
    print(f"{lbl:16s} within-rank")
    print(f"   raw    " + " ".join(f"{v:8.2f}" for v in qr.to_numpy())
          + f" | rho={rr:+.2f} top-bot={dr.mean():+7.2f} p={pr:.4f}")
    print(f"   demean " + " ".join(f"{v:8.2f}" for v in qd.to_numpy())
          + f" | rho={rd:+.2f} top-bot={dd.mean():+7.2f} p={pd_:.4f}")
