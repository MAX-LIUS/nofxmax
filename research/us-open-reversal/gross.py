#!/usr/bin/env python3
"""Is the negative result fee drag or no edge? Recompute with FEE=0.
If gross is positive and net is negative, the strategy works but cannot pay
the toll -- a cost problem (fixable by maker fills / bigger risk / better
symbols). If gross is also negative, the entry rule has no edge."""
import sys, glob, os, statistics as st
sys.path.insert(0,'.')
import engine2
from engine2 import backtest2

SYMS = sorted({os.path.basename(p).split('-')[0] for p in glob.glob("/tmp/usopen/*-5m-*.csv")})
print(f"{'pat':<9}{'tf':>5}{'n':>6}{'毛均值':>9}{'净均值':>9}{'费拖累':>9}{'毛胜率':>8}{'中位risk%':>10}")
for pat in ("wick","engulf","reclaim","volsurge"):
    for mult in (2,4,6,12):
        engine2.FEE = 0.0
        g = []
        rp = []
        for s in SYMS:
            for t in backtest2(s, mult, pat):
                g.append(t[2]); rp.append(t[5])
        engine2.FEE = 0.0005
        n = []
        for s in SYMS:
            for t in backtest2(s, mult, pat):
                n.append(t[2])
        if len(g) < 20: continue
        print(f"{pat:<9}{mult*5:>4}m{len(g):>6}{st.mean(g):>+9.3f}{st.mean(n):>+9.3f}"
              f"{st.mean(g)-st.mean(n):>+9.3f}{sum(1 for x in g if x>0)/len(g):>7.1%}"
              f"{st.median(rp):>9.3f}%")
