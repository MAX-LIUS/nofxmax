"""Correct kline loader + gap-aware feature helpers. Replaces harness.load_symbol.

WHY THIS EXISTS
===============
`harness.load_symbol` does `if "open_time" not in df.columns: continue` — it silently
drops every headerless CSV. 154 of 4620 files are headerless (an earlier fetch wrote
them without a header row). 129 are 2021-10..12 (merely a later start), but **25 are
inside the analysis window**: ICPUSDT 2022-01..06 and 20 symbols' 2022-03 (incl. SOL).

A mid-history gap is worse than a late start, because rolling windows (slope168,
ATR14) run straight across it and treat bars on either side as contiguous. The
features for the ~168 bars after each gap are then quietly wrong, with no NaN to
mark them.

So this module does two things harness never did:
  1. loads BOTH formats (header sniffed per file, not assumed);
  2. exposes `gap_mask` so rolling features can be invalidated across discontinuities
     instead of silently spanning them.

Everything here is completed-bar only: a value at index i uses bars <= i. No feature
may read bar i's close and be used to trade bar i.
"""
from __future__ import annotations
import os
import numpy as np
import pandas as pd

KL = "/root/.claude/jobs/5cbb3cf4/tmp/cdeval/klines"
BINANCE_COLS = ["open_time", "open", "high", "low", "close", "volume", "close_time",
                "quote_volume", "count", "taker_buy_volume", "taker_buy_quote_volume",
                "ignore"]
BAR_MS = 3_600_000


def _read_one(path: str) -> pd.DataFrame | None:
    """Read a kline CSV whether or not it carries a header row."""
    with open(path, "r") as fh:
        first = fh.readline()
    if first.startswith("open_time"):
        df = pd.read_csv(path)
    else:
        df = pd.read_csv(path, header=None, names=BINANCE_COLS)
    if "open_time" not in df.columns or len(df) == 0:
        return None
    return df


def load_symbol(sym: str) -> pd.DataFrame | None:
    files = sorted(f for f in os.listdir(KL)
                   if f.split("-", 1)[0] == sym and f.endswith(".csv"))
    frames = [d for d in (_read_one(f"{KL}/{f}") for f in files) if d is not None]
    if not frames:
        return None
    df = pd.concat(frames, ignore_index=True)
    df = df.drop_duplicates(subset="open_time").sort_values("open_time")
    ot = df["open_time"].astype("int64").to_numpy()
    idx = pd.to_datetime(ot, unit="ms", utc=True)
    out = pd.DataFrame({
        "open": df["open"].astype("float64").to_numpy(),
        "high": df["high"].astype("float64").to_numpy(),
        "low": df["low"].astype("float64").to_numpy(),
        "close": df["close"].astype("float64").to_numpy(),
        "quote_volume": df["quote_volume"].astype("float64").to_numpy(),
        "taker_buy_quote_volume": df["taker_buy_quote_volume"].astype("float64").to_numpy(),
    }, index=idx)
    out.index.name = "bar_open_time"
    # True where this bar does NOT continue the previous one.
    # NOTE: index 0 is deliberately False — the prepended synthetic bar makes
    # step[0] == BAR_MS. So gap.sum() is the count of REAL discontinuities and must
    # NOT have 1 subtracted from it (I got this wrong once and it hid a real gap).
    step = np.diff(ot, prepend=ot[0] - BAR_MS)
    out["gap"] = step != BAR_MS
    return out


def contiguous_run(gap: np.ndarray) -> np.ndarray:
    """Bars since the last discontinuity (0 at each gap start).

    A rolling window of length W is only trustworthy where contiguous_run >= W-1.
    """
    n = len(gap)
    run = np.zeros(n, dtype="int32")
    c = 0
    for i in range(n):
        c = 0 if gap[i] else c + 1
        run[i] = c
    return run


def wilder_atr(h, l, c, period=14):
    n = len(c)
    tr = np.full(n, np.nan)
    pc = np.roll(c, 1)
    tr[1:] = np.maximum(h[1:] - l[1:],
                        np.maximum(np.abs(h[1:] - pc[1:]), np.abs(l[1:] - pc[1:])))
    atr = np.full(n, np.nan)
    if n <= period:
        return atr
    atr[period] = np.nanmean(tr[1:period + 1])
    for i in range(period + 1, n):
        atr[i] = (atr[i - 1] * (period - 1) + tr[i]) / period
    return atr


def ols_slope(close: np.ndarray, win: int) -> np.ndarray:
    """OLS slope of log price over the trailing `win` bars, value at i uses (i-win+1..i)."""
    n = len(close)
    out = np.full(n, np.nan)
    if n < win:
        return out
    lp = np.log(close)
    x = np.arange(win, dtype="float64")
    x -= x.mean()
    denom = (x * x).sum()
    for i in range(win - 1, n):
        out[i] = float(np.dot(x, lp[i - win + 1:i + 1])) / denom
    return out


def audit():
    """Prove the fix: compare old vs new loader coverage on the worst symbols."""
    import harness
    print(f"{'symbol':<12}{'old rows':>10}{'new rows':>10}{'delta':>8}"
          f"{'old start':>13}{'new start':>13}{'gaps':>6}")
    for s in ["ICPUSDT", "SOLUSDT", "AAVEUSDT", "ZECUSDT", "BTCUSDT"]:
        o = harness.load_symbol(s)
        nw = load_symbol(s)
        if nw is None:
            continue
        ng = int(nw["gap"].sum())
        print(f"{s:<12}{len(o) if o is not None else 0:>10}{len(nw):>10}"
              f"{len(nw)-(len(o) if o is not None else 0):>8}"
              f"{str(o.index.min().date()) if o is not None else '-':>13}"
              f"{str(nw.index.min().date()):>13}{ng:>6}")
    print("\ngaps = 剩余不连续处(补齐 header 后仍存在的真实缺口),"
          "滚动特征必须靠 contiguous_run 作废这些点附近的值")


if __name__ == "__main__":
    audit()
