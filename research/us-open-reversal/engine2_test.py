#!/usr/bin/env python3
"""Invariants. engine.py shipped a -88R trade because risk -> 0 made the fee
term explode; these pin that it cannot recur, and that a stop exit is ~-1R."""
import sys
sys.path.insert(0, '.')
from engine2 import backtest2

fail = 0
seen = 0
for sym, mult, pat in (("SPYUSDT", 3, "wick"), ("NVDAUSDT", 2, "reclaim"),
                       ("MSTRUSDT", 4, "engulf"), ("SOXLUSDT", 6, "volsurge")):
    t = backtest2(sym, mult, pat)
    seen += len(t)
    for d, side, R, why, held, riskpct in t:
        if why == "stop" and not (-1.9 < R < -0.9):
            print(f"  FAIL {sym} {d} {side}: stop 退出 R={R:+.3f} 应≈-1R"); fail += 1
        if riskpct < 0.149:
            print(f"  FAIL {sym} {d}: risk={riskpct:.3f}% 低于下限"); fail += 1
        if R < -3:
            print(f"  FAIL {sym} {d}: R={R:+.3f} 不可能"); fail += 1
print(f"trades={seen}  ENGINE2 TEST", "FAIL" if fail else "PASS", f"({fail})")
sys.exit(1 if fail else 0)
