#!/usr/bin/env python3
"""US-open reversal backtest engine. stdlib only (no pandas on this host).

Causality contract: a pattern confirmed on bar i is filled at bar i+1's OPEN.
No rule may read bar i's close and trade bar i. Intrabar order is resolved
ADVERSE-FIRST: if a bar's range spans both stop and target, the stop wins.
"""
import csv, datetime, os, statistics as st

DIR = os.environ.get("USOPEN_DIR", "/tmp/usopen")
OPEN_UTC = 13 * 60 + 30       # 09:30 ET = 13:30 UTC (DST; 2026-04..07 all DST)
WINDOW_MIN = 60               # entry window 0-60 min after open
HARD_TRAIL_UTC = 19 * 60      # 15:00 ET -> trail break forces exit
CLOSE_UTC = 20 * 60           # 16:00 ET -> unconditional flat
FEE = 0.0005                  # taker, per side


def load5(sym):
    rows = []
    for m in ("2026-04", "2026-05", "2026-06", "2026-07"):
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


def agg(bars, mult):
    """Aggregate 5m -> mult*5m, anchored so a bucket starts exactly at 13:30."""
    if mult == 1:
        return bars
    out = []
    span = mult * 5 * 60_000
    for b in bars:
        t = b[0]
        d = datetime.datetime.fromtimestamp(t / 1000, datetime.timezone.utc)
        mins = d.hour * 60 + d.minute
        # offset from session open keeps buckets aligned to 13:30
        off = (mins - OPEN_UTC) % (mult * 5)
        bt = t - off * 60_000
        if out and out[-1][0] == bt:
            p = out[-1]
            out[-1] = (bt, p[1], max(p[2], b[2]), min(p[3], b[3]), b[4], p[5] + b[5])
        else:
            out.append((bt, b[1], b[2], b[3], b[4], b[5]))
    return [tuple(x) for x in out]


def daykey(ms):
    return datetime.datetime.fromtimestamp(ms / 1000, datetime.timezone.utc).date()


def minute(ms):
    d = datetime.datetime.fromtimestamp(ms / 1000, datetime.timezone.utc)
    return d.hour * 60 + d.minute


def sessions(bars):
    """Group bars by US trading day, keeping only weekdays with a real open."""
    days = {}
    for b in bars:
        days.setdefault(daykey(b[0]), []).append(b)
    out = {}
    for d, v in days.items():
        if d.weekday() >= 5:
            continue
        # require bars covering the open window
        if not any(OPEN_UTC <= minute(x[0]) < OPEN_UTC + WINDOW_MIN for x in v):
            continue
        out[d] = sorted(v)
    return out


def atr5d(sess, d):
    """5-day ATR from completed sessions before d."""
    ks = sorted(k for k in sess if k < d)[-5:]
    if len(ks) < 5:
        return None
    tr = []
    for k in ks:
        for b in sess[k]:
            tr.append(b[2] - b[3])
    return st.mean(tr)


def sess_hl(sess, d):
    """Session high/low for date d."""
    bars = sess.get(d)
    if not bars:
        return None, None
    return max(b[2] for b in bars), min(b[3] for b in bars)


def zones(sess, d, N_recent, M_prior, k_atr, atr):
    """Build reversal zones. Returns (resist_lo, resist_hi, supp_lo, supp_hi)."""
    days = sorted(sess.keys())
    idx = days.index(d) if d in days else -1
    if idx < max(N_recent, M_prior):
        return None
    recent_d = days[idx - N_recent : idx]
    prior_d = days[idx - M_prior : idx - N_recent]
    rh = [sess_hl(sess, x)[0] for x in recent_d]
    rl = [sess_hl(sess, x)[1] for x in recent_d]
    ph = [sess_hl(sess, x)[0] for x in prior_d]
    pl = [sess_hl(sess, x)[1] for x in prior_d]
    rh = [x for x in rh if x]; rl = [x for x in rl if x]
    ph = [x for x in ph if x]; pl = [x for x in pl if x]
    if not rh or not rl or not ph or not pl:
        return None
    prior_hi, prior_lo = max(ph), min(pl)
    buf = k_atr * atr
    return (prior_hi - buf, prior_hi + buf, prior_lo - buf, prior_lo + buf)


def touches(bar, lo, hi):
    return bar[3] <= hi and bar[2] >= lo


def wick_rej(bars, i, side):
    if i < 0 or i >= len(bars):
        return False
    b = bars[i]
    o, c, h, l = b[1], b[4], b[2], b[3]
    body = abs(c - o)
    if side == "LONG":
        wick = o - l if c > o else c - l
        return wick >= 1.5 * body if body > 0 else wick > 0
    else:
        wick = h - o if c < o else h - c
        return wick >= 1.5 * body if body > 0 else wick > 0


def engulf(bars, i, side):
    if i < 1:
        return False
    p, b = bars[i - 1], bars[i]
    pb = (min(p[1], p[4]), max(p[1], p[4]))
    bb = (min(b[1], b[4]), max(b[1], b[4]))
    if side == "LONG":
        return b[4] > b[1] and bb[0] < pb[0] and bb[1] > pb[1]
    else:
        return b[4] < b[1] and bb[0] < pb[0] and bb[1] > pb[1]


def reclaim(bars, i, side, T, zone_lo, zone_hi):
    if i < T:
        return False
    for j in range(i - T, i + 1):
        b = bars[j]
        if side == "LONG":
            if b[3] < zone_lo and b[4] > zone_lo:
                return True
        else:
            if b[2] > zone_hi and b[4] < zone_hi:
                return True
    return False


def detect(bars, i, pat, side, zone_lo, zone_hi):
    if pat == "wick":
        return wick_rej(bars, i, side)
    elif pat == "engulf":
        return engulf(bars, i, side)
    elif pat == "reclaim":
        return reclaim(bars, i, side, 2, zone_lo, zone_hi)
    return False


def run_trade(bars, entry_i, side, entry_px, stop_px, tgt_px, arm_R, trail_atr, atr):
    """Simulate trade from entry_i+1 onward. Returns (exit_px, reason, bars_held)."""
    trail = None
    armed = False
    for j in range(entry_i + 1, len(bars)):
        b = bars[j]
        m = minute(b[0])
        # arm trailing stop once favorable movement >= arm_R
        if not armed:
            if side == "LONG":
                if b[2] >= entry_px + arm_R * abs(entry_px - stop_px):
                    armed = True
                    trail = b[3] - trail_atr * atr
            else:
                if b[3] <= entry_px - arm_R * abs(entry_px - stop_px):
                    armed = True
                    trail = b[2] + trail_atr * atr
        # update trail
        if armed:
            if side == "LONG":
                trail = max(trail, b[3] - trail_atr * atr)
            else:
                trail = min(trail, b[2] + trail_atr * atr)
        # after HARD_TRAIL_UTC, trail break is forced exit
        hard = m >= HARD_TRAIL_UTC
        # check stop (adverse first)
        if side == "LONG":
            if armed and trail and b[3] <= trail:
                return (trail, "trail_hard" if hard else "trail", j - entry_i)
            if b[3] <= stop_px:
                return (stop_px, "stop", j - entry_i)
        else:
            if armed and trail and b[2] >= trail:
                return (trail, "trail_hard" if hard else "trail", j - entry_i)
            if b[2] >= stop_px:
                return (stop_px, "stop", j - entry_i)
        # check target
        if side == "LONG":
            if b[2] >= tgt_px:
                return (tgt_px, "target", j - entry_i)
        else:
            if b[3] <= tgt_px:
                return (tgt_px, "target", j - entry_i)
        # forced close at session end
        if m >= CLOSE_UTC:
            return (b[4], "close", j - entry_i)
    # ran out of bars
    return (bars[-1][4], "eod", len(bars) - entry_i - 1)


def backtest(sym, mult, pat, N_recent, M_prior, k_atr, R_mul, arm_R, trail_atr, shift=0):
    """Run one config. Returns list of trades: (date, side, R_net, reason, held)."""
    bars5 = load5(sym)
    bars = agg(bars5, mult)
    sess = sessions(bars)
    trades = []
    for d in sorted(sess.keys()):
        atr = atr5d(sess, d)
        if not atr:
            continue
        z = zones(sess, d, N_recent, M_prior, k_atr, atr)
        if not z:
            continue
        res_lo, res_hi, sup_lo, sup_hi = z
        if shift != 0:
            res_lo += shift * atr
            res_hi += shift * atr
            sup_lo += shift * atr
            sup_hi += shift * atr
        day_bars = sess[d]
        window = [b for b in day_bars if OPEN_UTC <= minute(b[0]) < OPEN_UTC + WINDOW_MIN]
        for i, b in enumerate(day_bars):
            if b not in window:
                continue
            # check SHORT setup
            if touches(b, res_lo, res_hi):
                if detect(day_bars, day_bars.index(b), pat, "SHORT", res_lo, res_hi):
                    entry_px = day_bars[day_bars.index(b) + 1][1] if day_bars.index(b) + 1 < len(day_bars) else None
                    if entry_px:
                        stop_px = res_hi + 0.25 * atr
                        # Reject if the fill gapped PAST the zone edge, leaving the
                        # "stop" below entry on a SHORT. risk would then be a tiny
                        # or wrong-signed number and R = pnl/risk explodes -- this
                        # produced a -7.23R mean (impossible when the floor is -1R)
                        # and one -3.394R trade. 20% of SPY signals were affected.
                        if stop_px <= entry_px:
                            continue
                        risk = stop_px - entry_px
                        tgt_px = entry_px - R_mul * risk
                        ex_px, reason, held = run_trade(day_bars, day_bars.index(b), "SHORT", entry_px, stop_px, tgt_px, arm_R, trail_atr, atr)
                        gross = (entry_px - ex_px) / risk
                        cost = 2 * FEE * entry_px / risk
                        trades.append((d, "SHORT", gross - cost, reason, held))
            # check LONG setup
            if touches(b, sup_lo, sup_hi):
                if detect(day_bars, day_bars.index(b), pat, "LONG", sup_lo, sup_hi):
                    entry_px = day_bars[day_bars.index(b) + 1][1] if day_bars.index(b) + 1 < len(day_bars) else None
                    if entry_px:
                        stop_px = sup_lo - 0.25 * atr
                        # Mirror of the SHORT guard above.
                        if stop_px >= entry_px:
                            continue
                        risk = entry_px - stop_px
                        tgt_px = entry_px + R_mul * risk
                        ex_px, reason, held = run_trade(day_bars, day_bars.index(b), "LONG", entry_px, stop_px, tgt_px, arm_R, trail_atr, atr)
                        gross = (ex_px - entry_px) / risk
                        cost = 2 * FEE * entry_px / risk
                        trades.append((d, "LONG", gross - cost, reason, held))
    return trades
