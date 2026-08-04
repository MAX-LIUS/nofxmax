#!/usr/bin/env python3
import sys
sys.path.insert(0, '.')
from engine import backtest
import statistics as st

# Primary registered combo: wick + 15m + R=2.0 + arm 1.0R + trail 1.0ATR
trades = backtest("SPYUSDT", mult=3, pat="wick", N_recent=3, M_prior=6,
                  k_atr=0.5, R_mul=2.0, arm_R=1.0, trail_atr=1.0)
print(f"n={len(trades)}")
if trades:
    r = [t[2] for t in trades]
    print(f"mean={st.mean(r):+.3f} med={st.median(r):+.3f} "
          f"win={sum(1 for x in r if x>0)/len(r):.1%}")
    for t in trades[:5]:
        print(f"  {t[0]} {t[1]:<5} R={t[2]:+.3f} {t[3]} held={t[4]}")
