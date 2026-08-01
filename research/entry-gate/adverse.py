"""Reframed target: predict the IMMEDIATE-ADVERSE event, not the mean outcome.

The live book wins 62% of the time for +65bps (break-even stop doing its job) and
loses it all on 18.7% of trades that average -220bps. Those losers are trades that
went adverse before they ever armed break-even. So the gate's real job is not
"is this a good trade" but "will this go against me immediately".

That is a different statistical object: a binary event with a much larger signal-
to-noise ratio than a mean return, and it is exactly what got averaged away in
every earlier test.

Definition (side-specific, causal):
    immediate_adverse = the trade reaches -1.0R before it ever reaches +0.5R.

Scored by AUC on decision-time features, with a label-shuffle null.
"""
import numpy as np, pandas as pd
from scipy import stats
import bars, events as EV

MAX_HOLD = 48
ARM_R = 0.5
STOP_R = 1.0


def label_adverse(f, idx, atr, side, stop_atr=1.5):
    o = f["open"].to_numpy(); h = f["high"].to_numpy(); l = f["low"].to_numpy()
    n = len(o)
    ent = idx + 1
    ok = ent < n
    entry = np.where(ok, o[np.minimum(ent, n-1)], np.nan)
    risk = stop_atr * atr[idx]
    fh = EV._windows(h, ent, MAX_HOLD)
    fl = EV._windows(l, ent, MAX_HOLD)
    if side == "LONG":
        fav = (fh - entry[:, None]) / risk[:, None]
        adv = (entry[:, None] - fl) / risk[:, None]
    else:
        fav = (entry[:, None] - fl) / risk[:, None]
        adv = (fh - entry[:, None]) / risk[:, None]
    big = MAX_HOLD + 10
    f_arm = np.where((fav >= ARM_R).any(axis=1), (fav >= ARM_R).argmax(axis=1), big)
    f_adv = np.where((adv >= STOP_R).any(axis=1), (adv >= STOP_R).argmax(axis=1), big)
    lab = (f_adv <= f_arm) & (f_adv < big)
    valid = ok & np.isfinite(risk) & (risk > 0)
    return np.where(valid, lab, np.nan)


FEATS = ["rpos30","rpos72","er24","er72","adx14","chop14","atr_pct","atr_rank168",
         "vol_z72","ema20_dev","ret4","ret24","ret72","ret24_atr","slope24","slope72",
         "slope168","taker_imb","lower_wick_frac","upper_wick_frac","body_frac",
         "close_pos_in_bar","dist_hi30_atr","dist_lo30_atr"]


def build(sym):
    f = bars.load_symbol(sym)
    if f is None or len(f) < 400:
        return None
    feats = EV.build_features(f)
    atr = feats.pop("_atr")
    n = len(f)
    idx = np.arange(200, n - MAX_HOLD - 2, EV.STRIDE)
    recs = []
    for side in ("LONG","SHORT"):
        lab = label_adverse(f, idx, atr, side)
        d = {"sym": sym, "t": f.index.to_numpy()[idx], "side": side, "adverse": lab}
        for k in FEATS:
            d[k] = feats[k][idx]
        recs.append(pd.DataFrame(d))
    out = pd.concat(recs, ignore_index=True)
    return out[np.isfinite(out.adverse)]


def auc(x, y):
    ok = np.isfinite(x) & np.isfinite(y)
    x, y = x[ok], y[ok]
    if len(np.unique(y)) < 2:
        return np.nan
    r = stats.rankdata(x)
    n1 = (y == 1).sum(); n0 = (y == 0).sum()
    return (r[y == 1].sum() - n1*(n1+1)/2) / (n1*n0)


if __name__ == "__main__":
    syms = EV.all_symbols()
    frames = []
    for i, s in enumerate(syms):
        try:
            df = build(s)
        except Exception as e:
            continue
        if df is not None and len(df):
            frames.append(df)
        if (i+1) % 15 == 0:
            print(f"  [{i+1}/{len(syms)}]", flush=True)
    ev = pd.concat(frames, ignore_index=True)
    ev["q"] = pd.PeriodIndex(pd.to_datetime(ev.t, utc=True), freq="Q").astype(str)
    ev.to_parquet("/root/projects/nofxmax/research/entry-gate/cache/adverse.parquet")
    print(f"\nrows={len(ev)}  base rate={ev.adverse.mean():.4f}")
    print(f"by side: {ev.groupby('side').adverse.mean().round(4).to_dict()}")
