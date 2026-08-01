"""Ladder-TP simulator, shaped like the live protection stack.

WHY
===
Every conclusion above used ONE target and a hard 48-bar cut. The live book does
not trade that: it takes a partial at a near target, then protects the runner with
a peak-drawdown ratchet. Under that geometry "low netRR" is not a bad trade — it
is the normal way TP1 works. So any recommendation about the RR floor MUST be
re-tested here or it is not supported.

Live shape (from the deployed protection configs recorded in .ai-memory):
  - full stop        : 1.5 ATR
  - TP1              : ~1.2 ATR, closes 30%
  - runner           : drawdown ratchet, arms at 2.5 ATR peak profit,
                       exits on a 1.8 ATR give-back from the peak
  - max hold         : 48 bars

Causality is unchanged: signal on closed bar i, fill at i+1 open, and within a
bar the ADVERSE event is taken first.
"""
from __future__ import annotations
import numpy as np, pandas as pd
import bars, events as EV

COST_BPS = 14.0
MAX_HOLD = 48


def sim_ladder(f: pd.DataFrame, idx: np.ndarray, atr: np.ndarray, side: str,
               stop_atr=1.5, tp1_atr=1.2, tp1_frac=0.30,
               arm_atr=2.5, give_atr=1.8) -> np.ndarray:
    """Return net R (per unit risk = stop_atr*ATR) for the ladder exit."""
    o = f["open"].to_numpy(); h = f["high"].to_numpy()
    l = f["low"].to_numpy(); c = f["close"].to_numpy()
    n = len(c)
    ent_i = idx + 1
    ok = ent_i < n
    entry = np.where(ok, o[np.minimum(ent_i, n - 1)], np.nan)
    a = atr[idx]
    sgn = 1.0 if side == "LONG" else -1.0

    fh = EV._windows(h, ent_i, MAX_HOLD)
    fl = EV._windows(l, ent_i, MAX_HOLD)
    fc = EV._windows(c, ent_i, MAX_HOLD)

    m = len(idx)
    risk = stop_atr * a                       # price distance of 1R
    # Favourable / adverse excursion per bar, in R units.
    if side == "LONG":
        fav = (fh - entry[:, None]) / risk[:, None]
        adv = (fl - entry[:, None]) / risk[:, None]
    else:
        fav = (entry[:, None] - fl) / risk[:, None]
        adv = (entry[:, None] - fh) / risk[:, None]
    close_R = sgn * (fc - entry[:, None]) / risk[:, None]

    tp1_R = tp1_atr / stop_atr
    arm_R = arm_atr / stop_atr
    give_R = give_atr / stop_atr

    out = np.full(m, np.nan)
    for k in range(m):
        if not ok[k] or not np.isfinite(a[k]) or a[k] <= 0:
            continue
        pos = 1.0
        realized = 0.0
        peak = 0.0
        armed = False
        tp1_done = False
        done = False
        for j in range(MAX_HOLD):
            if not np.isfinite(close_R[k, j]):
                break
            lo = adv[k, j]
            hi = fav[k, j]
            # ADVERSE FIRST inside the bar.
            if lo <= -1.0:
                realized += pos * (-1.0)
                pos = 0.0
                done = True
                break
            if armed and (peak - hi) >= 0 and (peak - max(hi, 0.0)) >= give_R:
                # give-back from the peak measured against this bar's low
                realized += pos * (peak - give_R)
                pos = 0.0
                done = True
                break
            if armed and (peak - lo) >= give_R:
                realized += pos * (peak - give_R)
                pos = 0.0
                done = True
                break
            if (not tp1_done) and hi >= tp1_R:
                realized += tp1_frac * tp1_R
                pos -= tp1_frac
                tp1_done = True
            if hi > peak:
                peak = hi
            if peak >= arm_R:
                armed = True
        if not done and pos > 0:
            jlast = j
            realized += pos * close_R[k, jlast]
        # Costs: entry once on full size, exits proportional. Approximate as one
        # round trip on the full notional — pessimistic and simple.
        cost_R = COST_BPS / ((risk[k] / entry[k]) * 10000.0)
        out[k] = realized - cost_R
    return out


def build(sym: str) -> pd.DataFrame | None:
    f = bars.load_symbol(sym)
    if f is None or len(f) < 400:
        return None
    feats = EV.build_features(f)
    atr = feats.pop("_atr")
    n = len(f)
    idx = np.arange(200, n - MAX_HOLD - 2, EV.STRIDE * 2)   # stride 12 for speed
    if len(idx) == 0:
        return None
    atr_bps = (feats["atr_pct"][idx] / 100.0) * 10000.0
    d = {"sym": sym, "t": f.index.to_numpy()[idx],
         "atr_pct": feats["atr_pct"][idx], "adx14": feats["adx14"][idx],
         "er24": feats["er24"][idx], "rpos30": feats["rpos30"][idx],
         "r_unit_bps": 1.5 * atr_bps}
    d["long_R"] = sim_ladder(f, idx, atr, "LONG")
    d["short_R"] = sim_ladder(f, idx, atr, "SHORT")
    out = pd.DataFrame(d)
    out = out[np.isfinite(out.long_R) & np.isfinite(out.short_R) & (out.r_unit_bps >= 20.0)]
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
        if (i + 1) % 10 == 0:
            print(f"  [{i+1}/{len(syms)}] {s}", flush=True)
    ev = pd.concat(frames, ignore_index=True)
    ev["q"] = pd.PeriodIndex(pd.to_datetime(ev["t"], utc=True), freq="Q").astype(str)
    ev["K"] = ev.r_unit_bps / COST_BPS
    ev["coin_R"] = 0.5 * (ev.long_R + ev.short_R)
    ev.to_parquet("/root/projects/nofxmax/research/entry-gate/cache/ladder.parquet")
    print(f"\nrows={len(ev)} quarters={ev.q.nunique()}")
    print(f"ladder baseline: long={ev.long_R.mean():+.4f}R short={ev.short_R.mean():+.4f}R "
          f"coin={ev.coin_R.mean():+.4f}R")


if __name__ == "__main__":
    main()
