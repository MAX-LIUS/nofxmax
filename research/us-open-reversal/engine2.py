#!/usr/bin/env python3
"""Reversal-zone -> TREND engine. Corrects two design errors of engine.py.

1. STOP IS STRUCTURAL, NOT ATR.  engine.py put the stop at zone_edge +- 0.25*ATR,
   which could land arbitrarily close to (or the wrong side of) the fill, making
   risk -> 0 and R = pnl/risk explode (one -88R trade; 18.8% of signals had
   risk < 0.2 ATR). Here the stop is the reversal bar's own extreme plus a small
   percentage buffer, so risk is the size of the structure being traded and
   self-scales with volatility. No ATR anywhere in sizing.

2. NO FIXED TARGET.  The thesis is that a volume-dense zone releases a move that
   travels FAR, so the payoff is fat-tailed. Capping at 2R truncates exactly the
   part that pays. Exit is trailing-only, in PERCENT of price.
"""
import csv, datetime, os, statistics as st

DIR = os.environ.get("USOPEN_DIR", "/tmp/usopen")
OPEN_UTC = 13 * 60 + 30
WINDOW_MIN = 60
HARD_TRAIL_UTC = 19 * 60
CLOSE_UTC = 20 * 60
FEE = 0.0005
MONTHS = ("2026-04", "2026-05", "2026-06", "2026-07")


def load5(sym):
    rows = []
    for m in MONTHS:
        p = os.path.join(DIR, f"{sym}-5m-{m}.csv")
        if not os.path.exists(p):
            continue
        with open(p) as fh:
            for r in csv.reader(fh):
                if not r or r[0].startswith("open_time"):
                    continue
                rows.append((int(r[0]), float(r[1]), float(r[2]),
                             float(r[3]), float(r[4]), float(r[5])))
    rows.sort()
    return rows


def minute(ms):
    d = datetime.datetime.fromtimestamp(ms / 1000, datetime.timezone.utc)
    return d.hour * 60 + d.minute


def daykey(ms):
    return datetime.datetime.fromtimestamp(ms / 1000, datetime.timezone.utc).date()


def agg(bars, mult):
    if mult == 1:
        return bars
    out = []
    for b in bars:
        mins = minute(b[0])
        off = (mins - OPEN_UTC) % (mult * 5)
        bt = b[0] - off * 60_000
        if out and out[-1][0] == bt:
            p = out[-1]
            out[-1] = (bt, p[1], max(p[2], b[2]), min(p[3], b[3]), b[4], p[5] + b[5])
        else:
            out.append((bt, b[1], b[2], b[3], b[4], b[5]))
    return out


def sessions(bars):
    days = {}
    for b in bars:
        days.setdefault(daykey(b[0]), []).append(b)
    return {d: sorted(v) for d, v in days.items()
            if d.weekday() < 5
            and any(OPEN_UTC <= minute(x[0]) < OPEN_UTC + WINDOW_MIN for x in v)}


def dense_zones(sess, d, lookback, bin_pct):
    """Reversal zones from VOLUME-DENSE price shelves over prior sessions.

    The thesis is that these zones matter because volume piled up there, so
    build them from traded volume per price bin, not from session extremes.
    Returns (hi_lo, hi_hi, lo_lo, lo_hi) or None.
    """
    days = sorted(k for k in sess if k < d)[-lookback:]
    if len(days) < lookback:
        return None
    bars = [b for k in days for b in sess[k]]
    if not bars:
        return None
    ref = st.median([b[4] for b in bars])
    step = ref * bin_pct
    if step <= 0:
        return None
    vol = {}
    for b in bars:
        mid = (b[2] + b[3]) / 2.0
        vol[round(mid / step)] = vol.get(round(mid / step), 0.0) + b[5]
    if len(vol) < 4:
        return None
    hi_edge = max(b[2] for b in bars)
    lo_edge = min(b[3] for b in bars)
    mid_px = (hi_edge + lo_edge) / 2.0
    up = {k: v for k, v in vol.items() if k * step > mid_px}
    dn = {k: v for k, v in vol.items() if k * step <= mid_px}
    if not up or not dn:
        return None
    ku = max(up, key=up.get)
    kd = max(dn, key=dn.get)
    return (ku * step - step / 2, ku * step + step / 2,
            kd * step - step / 2, kd * step + step / 2)


def patterns(bars, i, side, zlo, zhi):
    """Reversal patterns at a zone. Returns set of names that fired on bar i."""
    out = set()
    if i < 1 or i >= len(bars):
        return out
    b, p = bars[i], bars[i - 1]
    o, h, l, c = b[1], b[2], b[3], b[4]
    body = abs(c - o)
    # wick rejection
    if side == "LONG":
        wick = min(o, c) - l
        if wick >= 1.5 * body and c > l:
            out.add("wick")
    else:
        wick = h - max(o, c)
        if wick >= 1.5 * body and c < h:
            out.add("wick")
    # engulfing
    pb = (min(p[1], p[4]), max(p[1], p[4]))
    bb = (min(o, c), max(o, c))
    if bb[0] <= pb[0] and bb[1] >= pb[1]:
        if (side == "LONG" and c > o) or (side == "SHORT" and c < o):
            out.add("engulf")
    # false-break reclaim: poked outside the zone then closed back inside
    if side == "LONG":
        if l < zlo and c > zlo:
            out.add("reclaim")
    else:
        if h > zhi and c < zhi:
            out.add("reclaim")
    # volume-confirmed break of the reversal bar (thesis: dense zone releases)
    if len(bars) > i:
        prior = [x[5] for x in bars[max(0, i - 10):i]]
        if prior and b[5] > 1.5 * st.mean(prior):
            out.add("volsurge")
    return out


def run_trail(bars, entry_i, side, entry_px, stop_px, trail_pct,
              arm_pct, allow_overnight, max_days):
    """Trailing-only exit. No fixed target: the thesis is the move runs far.

    trail_pct / arm_pct are PERCENT OF PRICE, deliberately not ATR.
    Adverse-first within a bar: stop is checked before extension.
    """
    trail = None
    peak = entry_px
    start_day = daykey(bars[entry_i][0])
    for j in range(entry_i + 1, len(bars)):
        b = bars[j]
        m = minute(b[0])
        dk = daykey(b[0])
        held_days = (dk - start_day).days
        if not allow_overnight and dk != start_day:
            return (bars[j - 1][4], "eod_flat", j - entry_i)
        if held_days > max_days:
            return (b[1], "max_days", j - entry_i)
        # adverse first
        if side == "LONG":
            if b[3] <= stop_px:
                return (stop_px, "stop", j - entry_i)
        else:
            if b[2] >= stop_px:
                return (stop_px, "stop", j - entry_i)
        # trail break
        if trail is not None:
            if side == "LONG" and b[3] <= trail:
                return (trail, "trail", j - entry_i)
            if side == "SHORT" and b[2] >= trail:
                return (trail, "trail", j - entry_i)
        # extend / arm
        if side == "LONG":
            peak = max(peak, b[2])
            if peak >= entry_px * (1 + arm_pct):
                t = peak * (1 - trail_pct)
                trail = t if trail is None else max(trail, t)
        else:
            peak = min(peak, b[3])
            if peak <= entry_px * (1 - arm_pct):
                t = peak * (1 + trail_pct)
                trail = t if trail is None else min(trail, t)
        # 15:00 ET: if trailing is armed, a break is a forced exit (same as the
        # trail check above, which already fires); if not armed by then and we
        # are intraday-only, flatten at the close.
        if not allow_overnight and m >= CLOSE_UTC:
            return (b[4], "close", j - entry_i)
    return (bars[-1][4], "data_end", len(bars) - entry_i - 1)


def backtest2(sym, mult, pat, lookback=5, bin_pct=0.004, buf_pct=0.0015,
              trail_pct=0.006, arm_pct=0.004, min_risk_pct=0.0015,
              allow_overnight=True, max_days=3, shift_pct=0.0,
              open_utc=None, window_min=None):
    """One config. Returns [(date, side, R_net, reason, held, risk_pct)]."""
    global OPEN_UTC, WINDOW_MIN
    o_save, w_save = OPEN_UTC, WINDOW_MIN
    if open_utc is not None:
        OPEN_UTC = open_utc
    if window_min is not None:
        WINDOW_MIN = window_min
    try:
        bars = agg(load5(sym), mult)
        sess = sessions(bars)
        all_bars = bars
        idx_of = {b[0]: i for i, b in enumerate(all_bars)}
        trades = []
        for d in sorted(sess.keys()):
            z = dense_zones(sess, d, lookback, bin_pct)
            if not z:
                continue
            hi_lo, hi_hi, lo_lo, lo_hi = z
            if shift_pct:
                ref = (hi_hi + lo_lo) / 2.0
                sh = ref * shift_pct
                hi_lo += sh; hi_hi += sh; lo_lo += sh; lo_hi += sh
            day = sess[d]
            for k, b in enumerate(day):
                m = minute(b[0])
                if not (OPEN_UTC <= m < OPEN_UTC + WINDOW_MIN):
                    continue
                gi = idx_of.get(b[0])
                if gi is None or gi + 1 >= len(all_bars):
                    continue
                # SHORT at the upper dense zone
                if b[3] <= hi_hi and b[2] >= hi_lo:
                    if pat in patterns(day, k, "SHORT", hi_lo, hi_hi):
                        entry = all_bars[gi + 1][1]
                        stop = b[2] * (1 + buf_pct)   # structural: bar's own high
                        if stop > entry:
                            risk = stop - entry
                            if risk / entry >= min_risk_pct:
                                ex, why, held = run_trail(
                                    all_bars, gi, "SHORT", entry, stop, trail_pct,
                                    arm_pct, allow_overnight, max_days)
                                gross = (entry - ex) / risk
                                cost = 2 * FEE * entry / risk
                                trades.append((d, "SHORT", gross - cost, why,
                                               held, risk / entry * 100))
                # LONG at the lower dense zone
                if b[3] <= lo_hi and b[2] >= lo_lo:
                    if pat in patterns(day, k, "LONG", lo_lo, lo_hi):
                        entry = all_bars[gi + 1][1]
                        stop = b[3] * (1 - buf_pct)   # structural: bar's own low
                        if stop < entry:
                            risk = entry - stop
                            if risk / entry >= min_risk_pct:
                                ex, why, held = run_trail(
                                    all_bars, gi, "LONG", entry, stop, trail_pct,
                                    arm_pct, allow_overnight, max_days)
                                gross = (ex - entry) / risk
                                cost = 2 * FEE * entry / risk
                                trades.append((d, "LONG", gross - cost, why,
                                               held, risk / entry * 100))
        return trades
    finally:
        OPEN_UTC, WINDOW_MIN = o_save, w_save
