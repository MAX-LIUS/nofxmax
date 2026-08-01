"""Decouple the cost-multiple K from netRR.

THE CRUX
========
The live gate already enforces a minimum netRR, and
    netRR = grossRR - cost/risk = grossRR - 1/K
so a netRR floor ALREADY penalises a small K. With a fixed target multiple (as in
events.py, grossRR == 2.0 for every row) K and netRR are the same number and the
question "does K add anything?" is unanswerable by construction.

So: simulate several target multiples per event. Then grossRR varies, K and netRR
decouple, and we can ask the only question that matters for a recommendation —
holding netRR fixed, does K still carry information?

If it does not, the honest recommendation is "retune the existing gate", not
"add a new one".
"""
from __future__ import annotations
import numpy as np, pandas as pd
import bars, events as EV

STOP_ATR = 1.5
TARGETS = (1.0, 1.5, 2.0, 3.0, 4.5, 6.0)     # grossRR = T/1.5 -> 0.67 .. 4.0
COST_BPS = 14.0
OUT = "/root/projects/nofxmax/research/entry-gate/cache/multi.parquet"


def simulate_t(f: pd.DataFrame, idx: np.ndarray, atr: np.ndarray, side: str,
               targ_atr: float) -> np.ndarray:
    o = f["open"].to_numpy(); h = f["high"].to_numpy()
    l = f["low"].to_numpy(); c = f["close"].to_numpy()
    n = len(c)
    ent_i = idx + 1
    ok = ent_i < n
    entry = np.where(ok, o[np.minimum(ent_i, n - 1)], np.nan)
    a = atr[idx]
    if side == "LONG":
        stop = entry - STOP_ATR * a; targ = entry + targ_atr * a
    else:
        stop = entry + STOP_ATR * a; targ = entry - targ_atr * a
    fh = EV._windows(h, ent_i, EV.MAX_HOLD)
    fl = EV._windows(l, ent_i, EV.MAX_HOLD)
    fc = EV._windows(c, ent_i, EV.MAX_HOLD)
    if side == "LONG":
        hs = fl <= stop[:, None]; ht = fh >= targ[:, None]
    else:
        hs = fh >= stop[:, None]; ht = fl <= targ[:, None]
    big = EV.MAX_HOLD + 10
    fs = np.where(hs.any(axis=1), hs.argmax(axis=1), big)
    ft = np.where(ht.any(axis=1), ht.argmax(axis=1), big)
    stop_first = fs <= ft                      # adverse-first on ties
    ex = np.where((fs == big) & (ft == big), np.nan, np.where(stop_first, stop, targ))
    valid = ~np.isnan(fc)
    lastv = np.where(valid.any(axis=1), valid.shape[1]-1-valid[:, ::-1].argmax(axis=1), 0)
    ex = np.where(np.isnan(ex), fc[np.arange(len(fc)), lastv], ex)
    gross = (ex/entry - 1.0)*10000.0 if side == "LONG" else (1.0 - ex/entry)*10000.0
    net = gross - COST_BPS
    return np.where(ok & np.isfinite(a) & (a > 0) & np.isfinite(entry), net, np.nan)


def build(sym: str) -> pd.DataFrame | None:
    f = bars.load_symbol(sym)
    if f is None or len(f) < 400:
        return None
    feats = EV.build_features(f)
    atr = feats.pop("_atr")
    n = len(f)
    idx = np.arange(200, n - EV.MAX_HOLD - 2, EV.STRIDE)
    if len(idx) == 0:
        return None
    atr_bps = (feats["atr_pct"][idx] / 100.0) * 10000.0
    r_bps = STOP_ATR * atr_bps
    recs = []
    for T in TARGETS:
        d = {"sym": sym, "t": f.index.to_numpy()[idx], "targ_atr": T,
             "r_unit_bps": r_bps, "atr_pct": feats["atr_pct"][idx],
             "adx14": feats["adx14"][idx], "rpos30": feats["rpos30"][idx]}
        d["long_bps"] = simulate_t(f, idx, atr, "LONG", T)
        d["short_bps"] = simulate_t(f, idx, atr, "SHORT", T)
        recs.append(pd.DataFrame(d))
    out = pd.concat(recs, ignore_index=True)
    out = out[np.isfinite(out.long_bps) & np.isfinite(out.short_bps) & (out.r_unit_bps >= 20.0)]
    return out


def main():
    syms = EV.all_symbols()
    frames = []
    for i, s in enumerate(syms):
        try:
            df = build(s)
        except Exception as e:
            print(f"  {s}: FAIL {e}"); continue
        if df is None or len(df) == 0:
            continue
        frames.append(df)
        if (i+1) % 10 == 0:
            print(f"  [{i+1}/{len(syms)}] {s}", flush=True)
    ev = pd.concat(frames, ignore_index=True)
    ev["q"] = pd.PeriodIndex(pd.to_datetime(ev["t"], utc=True), freq="Q").astype(str)
    ev["grossRR"] = ev.targ_atr / STOP_ATR
    ev["K"] = ev.r_unit_bps / COST_BPS
    ev["netRR"] = ev.grossRR - 1.0/ev.K
    ev["coin_R"] = 0.5*(ev.long_bps + ev.short_bps)/ev.r_unit_bps
    ev.to_parquet(OUT)
    print(f"\nrows={len(ev)} quarters={ev.q.nunique()}")
    print(f"grossRR values: {sorted(ev.grossRR.unique())}")
    print(f"K   p10={np.percentile(ev.K,10):.1f} p50={np.percentile(ev.K,50):.1f} p90={np.percentile(ev.K,90):.1f}")
    print(f"netRR p10={np.percentile(ev.netRR,10):.2f} p50={np.percentile(ev.netRR,50):.2f} p90={np.percentile(ev.netRR,90):.2f}")


if __name__ == "__main__":
    main()
