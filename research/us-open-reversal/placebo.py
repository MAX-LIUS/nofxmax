#!/usr/bin/env python3
"""Three placebos: random time window, shifted zones, off-hours."""
import sys, random, statistics as st
sys.path.insert(0, '.')
from engine import backtest, load5, agg, sessions, atr5d, zones, detect, run_trade, touches, minute, daykey, FEE

def placebo_random_window(sym, mult, pat):
    """Replace 13:30-14:30 with a random 60-minute window each day."""
    random.seed(42)
    bars5 = load5(sym); bars = agg(bars5, mult); sess = sessions(bars)
    trades = []
    for d in sorted(sess.keys()):
        atr = atr5d(sess, d)
        if not atr: continue
        z = zones(sess, d, 3, 6, 0.5, atr)
        if not z: continue
        res_lo, res_hi, sup_lo, sup_hi = z
        day_bars = sess[d]
        # random 60-min window instead of fixed 13:30-14:30
        start_min = random.randint(0, 23*60 - 60)
        window = [b for b in day_bars if start_min <= minute(b[0]) % (24*60) < start_min + 60]
        for i, b in enumerate(day_bars):
            if b not in window: continue
            if touches(b, res_lo, res_hi):
                if detect(day_bars, day_bars.index(b), pat, "SHORT", res_lo, res_hi):
                    ei = day_bars.index(b)
                    if ei + 1 >= len(day_bars): continue
                    entry_px = day_bars[ei + 1][1]
                    stop_px = res_hi + 0.25 * atr; risk = abs(entry_px - stop_px)
                    tgt_px = entry_px - 2.0 * risk
                    ex_px, _, _ = run_trade(day_bars, ei, "SHORT", entry_px, stop_px, tgt_px, 1.0, 1.0, atr)
                    gross = (entry_px - ex_px) / risk; cost = 2 * FEE * entry_px / risk
                    trades.append((d, "SHORT", gross - cost))
            if touches(b, sup_lo, sup_hi):
                if detect(day_bars, day_bars.index(b), pat, "LONG", sup_lo, sup_hi):
                    ei = day_bars.index(b)
                    if ei + 1 >= len(day_bars): continue
                    entry_px = day_bars[ei + 1][1]
                    stop_px = sup_lo - 0.25 * atr; risk = abs(entry_px - stop_px)
                    tgt_px = entry_px + 2.0 * risk
                    ex_px, _, _ = run_trade(day_bars, ei, "LONG", entry_px, stop_px, tgt_px, 1.0, 1.0, atr)
                    gross = (ex_px - entry_px) / risk; cost = 2 * FEE * entry_px / risk
                    trades.append((d, "LONG", gross - cost))
    return trades

# Test on SPY wick 15m
t = placebo_random_window("SPYUSDT", 3, "wick")
print(f"placebo_random_window: n={len(t)} mean={st.mean([x[2] for x in t]) if t else 0:+.3f}")

# Placebo 2: shift zones by 2 ATR
t2 = backtest("SPYUSDT", 3, "wick", 3, 6, 0.5, 2.0, 1.0, 1.0, shift=2.0)
print(f"placebo_shifted_zone: n={len(t2)} mean={st.mean([x[2] for x in t2]) if t2 else 0:+.3f}")

# Placebo 3: use 03:30 UTC (off-hours, US market closed)
# Modify the OPEN_UTC temporarily
import engine
_orig = engine.OPEN_UTC
engine.OPEN_UTC = 3 * 60 + 30
t3 = backtest("SPYUSDT", 3, "wick", 3, 6, 0.5, 2.0, 1.0, 1.0)
engine.OPEN_UTC = _orig
print(f"placebo_off_hours: n={len(t3)} mean={st.mean([x[2] for x in t3]) if t3 else 0:+.3f}")
