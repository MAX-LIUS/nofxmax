"""Build the entry-gate event table: causal features + forward outcomes per side.

DESIGN
======
An entry gate has two separable jobs, and conflating them is why the previous
round kept concluding "nothing works":

  (a) DIRECTION  — given we are going to trade this symbol now, which side?
  (b) ABSTENTION — should we trade at all?

(b) fights the unconditional drift of the book. (a) compares two sides measured
on the SAME bar, so market-wide drift cancels. The live book's dominant loss is
asymmetry (LONG far worse than SHORT), which is job (a). So the event table
carries the forward outcome of BOTH sides at every event, letting us score a
feature on the LONG-minus-SHORT spread rather than only on a one-sided mean.

CAUSALITY CONTRACT (identical to every prior study in this repo)
  - every feature at index i uses bars <= i (closed bars only);
  - fill at bar i+1 OPEN;
  - stop/target checked from bar i+1 onward;
  - when stop and target fall in the same bar, the ADVERSE one is taken.

Exit geometry is FIXED and identical for every gate variant, so a gate can only
change WHICH events are taken, never HOW they are managed. That is the control
that makes the comparison about selection alone.
"""
from __future__ import annotations
import os
import numpy as np
import pandas as pd

import bars

OUT = "/root/projects/nofxmax/research/entry-gate/cache"
FEE_BPS = 5.0
SLIP_BPS = 2.0
STOP_ATR = 1.5
TARG_ATR = 3.0
MAX_HOLD = 48
STRIDE = 6


def _ema(x: np.ndarray, span: int) -> np.ndarray:
    a = 2.0 / (span + 1.0)
    out = np.full(len(x), np.nan)
    if len(x) == 0:
        return out
    acc = x[0]
    out[0] = acc
    for i in range(1, len(x)):
        acc = a * x[i] + (1 - a) * acc
        out[i] = acc
    return out


def _roll_max(x: np.ndarray, w: int) -> np.ndarray:
    return pd.Series(x).rolling(w).max().to_numpy()


def _roll_min(x: np.ndarray, w: int) -> np.ndarray:
    return pd.Series(x).rolling(w).min().to_numpy()


def _roll_sum(x: np.ndarray, w: int) -> np.ndarray:
    return pd.Series(x).rolling(w).sum().to_numpy()


def _roll_mean(x: np.ndarray, w: int) -> np.ndarray:
    return pd.Series(x).rolling(w).mean().to_numpy()


def _roll_std(x: np.ndarray, w: int) -> np.ndarray:
    return pd.Series(x).rolling(w).std().to_numpy()


def _roll_rank(x: np.ndarray, w: int) -> np.ndarray:
    """Percentile rank of the current value within the trailing window (0..1)."""
    s = pd.Series(x)
    return s.rolling(w).apply(lambda a: (a[:-1] < a[-1]).mean(), raw=True).to_numpy()


def adx(h, l, c, period=14):
    n = len(c)
    up = np.full(n, np.nan)
    dn = np.full(n, np.nan)
    up[1:] = h[1:] - h[:-1]
    dn[1:] = l[:-1] - l[1:]
    plus = np.where((up > dn) & (up > 0), up, 0.0)
    minus = np.where((dn > up) & (dn > 0), dn, 0.0)
    tr = np.full(n, np.nan)
    pc = np.roll(c, 1)
    tr[1:] = np.maximum(h[1:] - l[1:],
                        np.maximum(np.abs(h[1:] - pc[1:]), np.abs(l[1:] - pc[1:])))
    atr_ = _roll_mean(tr, period)
    pdi = 100.0 * _roll_mean(plus, period) / atr_
    mdi = 100.0 * _roll_mean(minus, period) / atr_
    dx = 100.0 * np.abs(pdi - mdi) / np.maximum(pdi + mdi, 1e-12)
    return _roll_mean(dx, period)


def chop(h, l, c, period=14):
    n = len(c)
    tr = np.full(n, np.nan)
    pc = np.roll(c, 1)
    tr[1:] = np.maximum(h[1:] - l[1:],
                        np.maximum(np.abs(h[1:] - pc[1:]), np.abs(l[1:] - pc[1:])))
    s = _roll_sum(tr, period)
    rng = _roll_max(h, period) - _roll_min(l, period)
    return 100.0 * np.log10(np.maximum(s, 1e-12) / np.maximum(rng, 1e-12)) / np.log10(period)


def efficiency(c, w):
    """Kaufman efficiency ratio: |net move| / total path over w bars."""
    net = np.abs(c - np.roll(c, w))
    step = np.abs(np.diff(c, prepend=c[0]))
    path = _roll_sum(step, w)
    out = net / np.maximum(path, 1e-12)
    out[:w] = np.nan
    return out


# Feature registry. Every entry: name -> callable(df-derived arrays) -> np.ndarray.
# All are computed on CLOSED bars only. Names are stable; the evaluator refers to
# them by string so adding a feature needs no other wiring.
def build_features(f: pd.DataFrame) -> dict[str, np.ndarray]:
    h = f["high"].to_numpy()
    l = f["low"].to_numpy()
    c = f["close"].to_numpy()
    qv = f["quote_volume"].to_numpy()
    tbq = f["taker_buy_quote_volume"].to_numpy()
    atr = bars.wilder_atr(h, l, c, 14)
    run = bars.contiguous_run(f["gap"].to_numpy())

    feats: dict[str, np.ndarray] = {}

    # ── Location within the recent range ───────────────────────────────────────
    for w in (30, 72):
        hi = _roll_max(h, w)
        lo = _roll_min(l, w)
        feats[f"rpos{w}"] = (c - lo) / np.maximum(hi - lo, 1e-12)
        # Distance to each edge in ATR units — scale-free, unlike raw rpos.
        feats[f"dist_hi{w}_atr"] = (hi - c) / np.maximum(atr, 1e-12)
        feats[f"dist_lo{w}_atr"] = (c - lo) / np.maximum(atr, 1e-12)

    # ── Path quality / chop ────────────────────────────────────────────────────
    feats["er24"] = efficiency(c, 24)
    feats["er72"] = efficiency(c, 72)
    feats["adx14"] = adx(h, l, c, 14)
    feats["chop14"] = chop(h, l, c, 14)

    # ── Trend / momentum ───────────────────────────────────────────────────────
    feats["slope24"] = bars.ols_slope(c, 24)
    feats["slope72"] = bars.ols_slope(c, 72)
    feats["slope168"] = bars.ols_slope(c, 168)
    ema20 = _ema(c, 20)
    feats["ema20_dev"] = (c - ema20) / np.maximum(ema20, 1e-12) * 100.0
    for w in (4, 24, 72):
        feats[f"ret{w}"] = (c / np.roll(c, w) - 1.0) * 100.0
        feats[f"ret{w}"][:w] = np.nan
    # Trailing return normalized by ATR — "how many ATRs has it already moved".
    feats["ret24_atr"] = (c - np.roll(c, 24)) / np.maximum(atr, 1e-12)
    feats["ret24_atr"][:24] = np.nan

    # ── Volatility state ───────────────────────────────────────────────────────
    atrp = atr / np.maximum(c, 1e-12) * 100.0
    feats["atr_pct"] = atrp
    feats["atr_rank168"] = _roll_rank(atrp, 168)

    # ── Candle shape (John Wick / rejection) ───────────────────────────────────
    rng = np.maximum(h - l, 1e-12)
    body = np.abs(c - f["open"].to_numpy())
    upper_sh = h - np.maximum(c, f["open"].to_numpy())
    lower_sh = np.minimum(c, f["open"].to_numpy()) - l
    feats["lower_wick_frac"] = lower_sh / rng
    feats["upper_wick_frac"] = upper_sh / rng
    feats["body_frac"] = body / rng
    feats["close_pos_in_bar"] = (c - l) / rng

    # ── Flow ───────────────────────────────────────────────────────────────────
    feats["taker_imb"] = (2.0 * tbq - qv) / np.maximum(qv, 1e-12)
    vz = (qv - _roll_mean(qv, 72)) / np.maximum(_roll_std(qv, 72), 1e-12)
    feats["vol_z72"] = vz

    # ── Invalidate every rolling feature across data discontinuities ───────────
    # A window of length W is only trustworthy where contiguous_run >= W-1.
    need = {"rpos30": 30, "dist_hi30_atr": 30, "dist_lo30_atr": 30,
            "rpos72": 72, "dist_hi72_atr": 72, "dist_lo72_atr": 72,
            "er24": 24, "er72": 72, "adx14": 28, "chop14": 14,
            "slope24": 24, "slope72": 72, "slope168": 168,
            "ema20_dev": 20, "ret4": 4, "ret24": 24, "ret72": 72,
            "ret24_atr": 24, "atr_pct": 15, "atr_rank168": 168,
            "lower_wick_frac": 1, "upper_wick_frac": 1, "body_frac": 1,
            "close_pos_in_bar": 1, "taker_imb": 1, "vol_z72": 72}
    for k, w in need.items():
        if k in feats:
            feats[k] = np.where(run >= w - 1, feats[k], np.nan)

    feats["_atr"] = np.where(run >= 14, atr, np.nan)
    return feats


def _windows(x: np.ndarray, idx: np.ndarray, n: int) -> np.ndarray:
    """Rows of x[idx[k] : idx[k]+n]. Out-of-range tail is filled with nan."""
    pad = np.full(n, np.nan)
    xp = np.concatenate([x, pad])
    off = np.arange(n)
    return xp[idx[:, None] + off[None, :]]


def simulate(f: pd.DataFrame, idx: np.ndarray, atr: np.ndarray, side: str) -> np.ndarray:
    """Net bps for entering `side` at bar idx+1 open with fixed ATR geometry.

    Adverse-first: if stop and target both fall inside one bar, the stop is taken.
    That is the pessimistic assumption, and it must be pessimistic because the
    alternative silently manufactures edge out of intrabar ordering we cannot see.
    """
    o = f["open"].to_numpy()
    h = f["high"].to_numpy()
    l = f["low"].to_numpy()
    c = f["close"].to_numpy()
    n = len(c)

    ent_i = idx + 1
    ok = ent_i < n
    entry = np.where(ok, o[np.minimum(ent_i, n - 1)], np.nan)
    a = atr[idx]

    if side == "LONG":
        stop = entry - STOP_ATR * a
        targ = entry + TARG_ATR * a
    else:
        stop = entry + STOP_ATR * a
        targ = entry - TARG_ATR * a

    fh = _windows(h, ent_i, MAX_HOLD)
    fl = _windows(l, ent_i, MAX_HOLD)
    fc = _windows(c, ent_i, MAX_HOLD)

    if side == "LONG":
        hit_stop = fl <= stop[:, None]
        hit_targ = fh >= targ[:, None]
    else:
        hit_stop = fh >= stop[:, None]
        hit_targ = fl <= targ[:, None]

    big = MAX_HOLD + 10
    first_stop = np.where(hit_stop.any(axis=1), hit_stop.argmax(axis=1), big)
    first_targ = np.where(hit_targ.any(axis=1), hit_targ.argmax(axis=1), big)

    # Adverse-first: on a tie the stop wins.
    stop_first = first_stop <= first_targ
    exit_px = np.where(
        (first_stop == big) & (first_targ == big),
        np.nan,                                    # neither: fall through to time exit
        np.where(stop_first, stop, targ),
    )
    # Time exit at the last valid bar of the window.
    valid = ~np.isnan(fc)
    last_valid = np.where(valid.any(axis=1), valid.shape[1] - 1 - valid[:, ::-1].argmax(axis=1), 0)
    time_px = fc[np.arange(len(fc)), last_valid]
    exit_px = np.where(np.isnan(exit_px), time_px, exit_px)

    if side == "LONG":
        gross = (exit_px / entry - 1.0) * 10000.0
    else:
        gross = (1.0 - exit_px / entry) * 10000.0
    net = gross - 2.0 * (FEE_BPS + SLIP_BPS)
    net = np.where(ok & ~np.isnan(a) & (a > 0) & ~np.isnan(entry), net, np.nan)
    return net


def build_symbol(sym: str) -> pd.DataFrame | None:
    f = bars.load_symbol(sym)
    if f is None or len(f) < 400:
        return None
    feats = build_features(f)
    atr = feats.pop("_atr")

    n = len(f)
    idx = np.arange(200, n - MAX_HOLD - 2, STRIDE)
    if len(idx) == 0:
        return None

    d = {"sym": sym, "t": f.index.to_numpy()[idx]}
    for k, v in feats.items():
        d[k] = v[idx]
    d["atr_abs"] = atr[idx]
    d["long_bps"] = simulate(f, idx, atr, "LONG")
    d["short_bps"] = simulate(f, idx, atr, "SHORT")
    out = pd.DataFrame(d)
    # Drop rows where either leg is unusable — both legs must exist for the
    # direction-spread test to be an apples-to-apples comparison on one bar.
    out = out[np.isfinite(out["long_bps"]) & np.isfinite(out["short_bps"])]
    return out


def all_symbols() -> list[str]:
    fs = os.listdir(bars.KL)
    return sorted({x.split("-", 1)[0] for x in fs if x.endswith(".csv")})


def main():
    os.makedirs(OUT, exist_ok=True)
    syms = all_symbols()
    print(f"symbols: {len(syms)}")
    frames = []
    for i, s in enumerate(syms):
        try:
            df = build_symbol(s)
        except Exception as e:
            print(f"  {s}: FAIL {e}")
            continue
        if df is None or len(df) == 0:
            print(f"  {s}: empty")
            continue
        frames.append(df)
        print(f"  [{i+1}/{len(syms)}] {s}: {len(df)} events", flush=True)
    ev = pd.concat(frames, ignore_index=True)
    ev["q"] = pd.PeriodIndex(pd.to_datetime(ev["t"], utc=True), freq="Q").astype(str)
    ev.to_parquet(f"{OUT}/events.parquet")
    print(f"\ntotal events: {len(ev)}  quarters: {ev['q'].nunique()}")
    print(ev.groupby("q").size().to_string())


if __name__ == "__main__":
    main()
