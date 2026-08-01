"""Scoring protocol for entry-gate candidates.

WHY THIS FILE LOOKS PARANOID
============================
Three traps burned earlier rounds of this project, and every one of them
produced a confident wrong answer that survived a naive p-value:

  1. POOLED p-VALUES LIE. The vendor study got pooled +2.75bps p=0.0000 that
     collapsed to 8/15 quarters positive, binomial p=1.0000 — a beta expression
     masquerading as skill. So the primary statistic here is ALWAYS the
     quarter-equal-weighted mean plus a binomial sign test. Pooled numbers are
     reported but never decide.

  2. "TRADING LESS" IS NOT SKILL. When the baseline has negative expectancy,
     any filter that fires often looks profitable in total PnL. Per-trade bps
     already normalizes that, but a gate can still win by shifting WHICH
     quarters it trades. Hence the circular-shift control: keep each gate's
     firing frequency and per-symbol distribution, destroy only its timing.

  3. QUANTILE-DIFF SIGNIFICANCE WITHOUT SIGN/MONOTONICITY IS NOISE. The funding
     candidate showed p=0.0105 with the WRONG sign and no monotonicity. So a
     candidate must be directionally coherent, not merely non-random.

Acceptance is defined BEFORE looking at results (see PASS_RULE).
"""
from __future__ import annotations
import numpy as np
import pandas as pd
from scipy import stats

CACHE = "/root/projects/nofxmax/research/entry-gate/cache/events.parquet"

# Written before any result was inspected.
PASS_RULE = dict(
    min_quarters_positive_frac=0.65,   # sign stability, not just a mean
    max_binomial_p=0.10,               # sign test on quarters
    min_qmean_bps=2.0,                 # economically meaningful after costs
    min_pass_rate=0.10,                # must fire often enough to matter
    max_shift_control_frac=0.40,       # <=40% of the effect may be mechanical
)


def load() -> pd.DataFrame:
    return pd.read_parquet(CACHE)


def per_quarter(ev: pd.DataFrame, val: np.ndarray) -> pd.Series:
    """Quarter-equal-weighted mean of `val`, one number per quarter."""
    s = pd.Series(val, index=ev.index)
    return s.groupby(ev["q"]).mean()


def sign_test(qs: pd.Series) -> tuple[float, float, int, int]:
    """Binomial sign test on per-quarter values. Returns (frac_pos, p, npos, n)."""
    x = qs.dropna()
    n = len(x)
    if n == 0:
        return (np.nan, 1.0, 0, 0)
    npos = int((x > 0).sum())
    p = stats.binomtest(npos, n, 0.5, alternative="greater").pvalue
    return (npos / n, p, npos, n)


def summarize(qs: pd.Series, pooled: float, pass_rate: float) -> dict:
    frac, p, npos, n = sign_test(qs)
    x = qs.dropna()
    t_p = stats.ttest_1samp(x, 0.0).pvalue if len(x) > 2 else np.nan
    return dict(
        qmean=float(x.mean()) if len(x) else np.nan,
        qmedian=float(x.median()) if len(x) else np.nan,
        worst_q=float(x.min()) if len(x) else np.nan,
        best_q=float(x.max()) if len(x) else np.nan,
        frac_pos=frac, binom_p=p, npos=npos, nq=n,
        ttest_p=float(t_p) if t_p == t_p else np.nan,
        pooled=float(pooled), pass_rate=float(pass_rate),
    )


# ─────────────────────────────────────────────────────────────────────────────
# JOB (a): DIRECTION SELECTION
# ─────────────────────────────────────────────────────────────────────────────
# A direction rule maps a feature to a side preference. Its value is measured as
# the realized bps of the CHOSEN side minus the bps of a side-neutral baseline
# on the SAME events. Using the same events for both is what makes market drift
# cancel: we are not asking "is long good", we are asking "given this bar, did
# the feature pick the better side".
#
# Baseline choice matters. Two are reported:
#   coin  — the mean of the two legs (what a coin flip earns in expectation);
#   fixed — always LONG (what the live book effectively does).
# `coin` is the honest test of directional skill. `fixed` is the one that maps to
# our actual problem, because our book is long-biased.

def direction_eval(ev: pd.DataFrame, score: np.ndarray, thresh: float = 0.0,
                   min_abs: float | None = None) -> dict:
    """score > thresh -> prefer LONG; score < thresh -> prefer SHORT.

    min_abs, when set, abstains from events where |score-thresh| is below it.
    Abstained events earn the coin baseline (i.e. the rule declines to express a
    view), so abstention cannot manufacture edge.
    """
    L = ev["long_bps"].to_numpy()
    S = ev["short_bps"].to_numpy()
    coin = 0.5 * (L + S)

    dev = score - thresh
    fires = np.isfinite(dev)
    if min_abs is not None:
        fires = fires & (np.abs(dev) >= min_abs)

    chosen = np.where(dev > 0, L, S)
    chosen = np.where(fires, chosen, coin)

    edge = chosen - coin                      # skill vs a coin flip
    vs_long = chosen - L                      # improvement over an always-long book
    qs = per_quarter(ev, edge)
    qs_long = per_quarter(ev, vs_long)

    out = summarize(qs, float(np.nanmean(edge)), float(np.nanmean(fires)))
    out["vs_long_qmean"] = float(qs_long.dropna().mean())
    out["vs_long_frac_pos"] = sign_test(qs_long)[0]
    out["vs_long_binom_p"] = sign_test(qs_long)[1]
    return out


def shift_control(ev: pd.DataFrame, score: np.ndarray, thresh: float = 0.0,
                  min_abs: float | None = None, nshift: int = 24,
                  seed: int = 0) -> dict:
    """Circular-shift the SCORE within each symbol, keeping outcomes in place.

    This preserves the score's own autocorrelation, its firing rate, and its
    per-symbol distribution, while destroying its alignment with outcomes. The
    residual edge is the mechanical component; real skill must exceed it.
    """
    rng = np.random.default_rng(seed)
    sym = ev["sym"].to_numpy()
    order = np.argsort(sym, kind="stable")
    inv = np.empty_like(order)
    inv[order] = np.arange(len(order))
    s_sorted = score[order]
    sym_sorted = sym[order]
    bounds = np.flatnonzero(np.r_[True, sym_sorted[1:] != sym_sorted[:-1], True])

    vals = []
    for _ in range(nshift):
        sh = s_sorted.copy()
        for a, b in zip(bounds[:-1], bounds[1:]):
            k = int(rng.integers(1, max(2, b - a)))
            sh[a:b] = np.roll(sh[a:b], k)
        vals.append(direction_eval(ev, sh[inv], thresh, min_abs)["qmean"])
    return dict(shift_qmean=float(np.mean(vals)), shift_sd=float(np.std(vals)))
