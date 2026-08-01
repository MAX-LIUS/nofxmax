"""The reframe the evidence forces.

Decision-time gating is exhausted: direction selection nil (screen1), abstention
an ATR-scaling artifact (mech), and the immediate-adverse score dies out-of-sample
at AUC 0.503 over 16 walk-forward quarters (score.py). Meanwhile the live book
separates violently AFTER entry:

    losers  adv=2.60 ATR  fav=0.56 ATR
    winners adv=0.90 ATR  fav=2.21 ATR

So the question is not "which trades to skip" but "how few bars until a trade has
told us what it is". If the split is visible at bar 3-6, the right mechanism is
probation: enter, then cut fast on failure to make progress -- a decision that
needs no forecast, only observation.

Measured here, on 20 quarters, with the same causality contract as every other
study (signal bar i, fill i+1 open, adverse-first within a bar):
  1. AUC of the running path at bar k for the final sign of the trade
  2. whether an actual cut rule at bar k beats holding to the 1.5-ATR stop
"""
import numpy as np, pandas as pd
from scipy import stats
import bars, events as EV, evalgate as E

MAX_HOLD = 48
COST_BPS = 14.0
STOP_ATR = 1.5
CHECK_BARS = [2, 3, 4, 6, 8, 12]


def paths(f, idx, atr, side):
    """Running favourable/adverse excursion in ATR units, per bar, plus outcome."""
    o = f["open"].to_numpy(); h = f["high"].to_numpy()
    l = f["low"].to_numpy(); c = f["close"].to_numpy()
    n = len(o)
    ent = idx + 1
    ok = ent < n
    entry = np.where(ok, o[np.minimum(ent, n - 1)], np.nan)
    a = atr[idx]
    fh = EV._windows(h, ent, MAX_HOLD)
    fl = EV._windows(l, ent, MAX_HOLD)
    fc = EV._windows(c, ent, MAX_HOLD)
    if side == "LONG":
        fav = (fh - entry[:, None]) / a[:, None]
        adv = (entry[:, None] - fl) / a[:, None]
        clo = (fc - entry[:, None]) / a[:, None]
    else:
        fav = (entry[:, None] - fl) / a[:, None]
        adv = (fh - entry[:, None]) / a[:, None]
        clo = (entry[:, None] - fc) / a[:, None]
    runfav = np.fmax.accumulate(np.nan_to_num(fav, nan=-np.inf), axis=1)
    runadv = np.fmax.accumulate(np.nan_to_num(adv, nan=-np.inf), axis=1)
    runfav[~np.isfinite(runfav)] = np.nan
    runadv[~np.isfinite(runadv)] = np.nan

    # Outcome under a plain 1.5-ATR stop with break-even arm at 0.5R, in R units.
    r_unit_bps = (STOP_ATR * a / entry) * 10000.0
    cost_R = COST_BPS / np.maximum(r_unit_bps, 1e-9)
    m = len(idx)
    out = np.full(m, np.nan)
    for k in range(m):
        if not ok[k] or not np.isfinite(a[k]) or a[k] <= 0:
            continue
        floor, peak, armed = -1.0, 0.0, False
        j = 0; done = False
        for j in range(MAX_HOLD):
            if not np.isfinite(clo[k, j]):
                break
            fR = fav[k, j] / STOP_ATR
            aR = adv[k, j] / STOP_ATR
            if -aR <= floor:
                out[k] = floor - cost_R[k]; done = True; break
            peak = max(peak, fR)
            if (not armed) and peak >= 0.5:
                armed = True; floor = 0.0
            if armed and peak - 1.2 > floor:
                floor = peak - 1.2
        if not done:
            out[k] = clo[k, j] / STOP_ATR - cost_R[k]
    return runfav, runadv, out, cost_R, r_unit_bps


def run():
    syms = EV.all_symbols()
    acc = []
    for i, s in enumerate(syms):
        f = bars.load_symbol(s)
        if f is None or len(f) < 400:
            continue
        feats = EV.build_features(f)
        atr = feats.pop("_atr")
        n = len(f)
        idx = np.arange(200, n - MAX_HOLD - 2, EV.STRIDE * 4)
        if len(idx) == 0:
            continue
        atrp = feats["atr_pct"][idx]
        keep = np.isfinite(atrp) & (atrp >= 0.20)
        t = pd.PeriodIndex(pd.to_datetime(f.index.to_numpy()[idx], utc=True),
                           freq="Q").astype(str).to_numpy()
        for side in ("LONG", "SHORT"):
            rf, ra, out, cR, ru = paths(f, idx, atr, side)
            d = {"q": t, "side": side, "R": out, "costR": cR}
            for k in CHECK_BARS:
                d[f"fav{k}"] = rf[:, k - 1]
                d[f"adv{k}"] = ra[:, k - 1]
            df = pd.DataFrame(d)[keep]
            acc.append(df.dropna(subset=["R"]))
        if (i + 1) % 15 == 0:
            print(f"  [{i+1}/{len(syms)}]", flush=True)

    e = pd.concat(acc, ignore_index=True)
    print(f"\nevents={len(e)}  quarters={e.q.nunique()}")
    e["win"] = (e.R > 0).astype(int)

    print("\n=== how early does the path separate? (AUC for final win) ===")
    for k in CHECK_BARS:
        for col in (f"fav{k}", f"adv{k}"):
            v = e[col].to_numpy(); y = e.win.to_numpy()
            m = np.isfinite(v)
            auc = stats.mannwhitneyu(v[m][y[m] == 1], v[m][y[m] == 0],
                                     alternative="two-sided").statistic
            auc /= (y[m] == 1).sum() * (y[m] == 0).sum()
            print(f"  bar {k:>2}  {col:<7} AUC={auc:.4f}")

    print("\n=== net progress (fav-adv) at bar k, AUC ===")
    for k in CHECK_BARS:
        v = (e[f"fav{k}"] - e[f"adv{k}"]).to_numpy(); y = e.win.to_numpy()
        m = np.isfinite(v)
        auc = stats.mannwhitneyu(v[m][y[m] == 1], v[m][y[m] == 0],
                                alternative="two-sided").statistic
        auc /= (y[m] == 1).sum() * (y[m] == 0).sum()
        print(f"  bar {k:>2}  fav-adv  AUC={auc:.4f}")

    e.to_parquet("cache/probation.parquet", index=False)
    print("\nsaved cache/probation.parquet")


if __name__ == "__main__":
    run()
