#!/usr/bin/env python3
"""Decisive control. risk% larger -> fee/R smaller, mechanically. So the
monotone risk-band table may be pure arithmetic with no edge in it.

Control: identical machinery (same window, same structural stop from the bar's
own extreme, same trailing exit, same fee), but the ENTRY BAR IS RANDOM inside
the window and no zone/pattern is required. If random entries show the same
monotone risk-band table, the table measures geometry, not the strategy.
"""
import sys, glob, os, random, math, statistics as st
sys.path.insert(0,'.')
import engine2
from engine2 import load5, agg, sessions, minute, daykey, run_trail, OPEN_UTC, WINDOW_MIN, FEE

SYMS=sorted({os.path.basename(p).split('-')[0] for p in glob.glob("/tmp/usopen/*-5m-*.csv")})
random.seed(20260804)
out=[]
for mult in (2,4,6,12):
    for s in SYMS:
        bars=agg(load5(s),mult); sess=sessions(bars)
        idx={b[0]:i for i,b in enumerate(bars)}
        for d in sorted(sess):
            day=sess[d]
            win=[b for b in day if OPEN_UTC<=minute(b[0])<OPEN_UTC+WINDOW_MIN]
            if len(win)<2: continue
            b=random.choice(win[:-1])
            gi=idx.get(b[0])
            if gi is None or gi+1>=len(bars): continue
            side=random.choice(("LONG","SHORT"))
            entry=bars[gi+1][1]
            if side=="SHORT":
                stop=b[2]*(1+0.0015)
                if stop<=entry: continue
                risk=stop-entry
            else:
                stop=b[3]*(1-0.0015)
                if stop>=entry: continue
                risk=entry-stop
            if risk/entry<0.0015: continue
            ex,why,held=run_trail(bars,gi,side,entry,stop,0.006,0.004,True,3)
            gross=(entry-ex)/risk if side=="SHORT" else (ex-entry)/risk
            out.append((risk/entry*100, gross-2*FEE*entry/risk, d))
print(f"随机入场对照 n={len(out)}")
print(f"\n{'risk%区间':<16}{'n':>6}{'净均值':>9}{'中位':>9}{'胜率':>8}")
for lo,hi in ((0,0.5),(0.5,1.0),(1.0,1.5),(1.5,2.5),(2.5,99)):
    sub=[x for x in out if lo<=x[0]<hi]
    if len(sub)<20: continue
    r=[x[1] for x in sub]
    print(f"{lo:.1f}-{hi:<4.1f}%{'':<7}{len(r):>6}{st.mean(r):>+9.3f}{st.median(r):>+9.3f}"
          f"{sum(1 for x in r if x>0)/len(r):>7.1%}")
big=[x for x in out if x[0]>=1.5]
r=[x[1] for x in big]
print(f"\n随机 risk>=1.5%: n={len(r)} 均值={st.mean(r):+.3f} 胜率={sum(1 for x in r if x>0)/len(r):.1%}")
print("(战法同档为 +0.026/+0.074, 胜率 56.3%/67.7%)")
