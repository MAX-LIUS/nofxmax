#!/usr/bin/env python3
"""Fee drag in R = 2*FEE/risk_pct. It falls as risk grows. So: do the
large-risk trades -- where the toll is small -- actually pay?
This is the one direction the data itself points to."""
import sys, glob, os, math, statistics as st
sys.path.insert(0,'.')
from engine2 import backtest2
SYMS=sorted({os.path.basename(p).split('-')[0] for p in glob.glob("/tmp/usopen/*-5m-*.csv")})
allt=[]
for pat in ("wick","engulf","reclaim","volsurge"):
    for mult in (2,4,6,12):
        for s in SYMS:
            for t in backtest2(s,mult,pat):
                allt.append((t[5],t[2],t[0],pat,mult*5))
print(f"总样本 n={len(allt)}")
print(f"\n{'risk%区间':<16}{'n':>6}{'净均值':>9}{'中位':>9}{'胜率':>8}{'理论费拖累':>11}")
bands=[(0,0.5),(0.5,1.0),(1.0,1.5),(1.5,2.5),(2.5,99)]
for lo,hi in bands:
    sub=[x for x in allt if lo<=x[0]<hi]
    if len(sub)<20: continue
    r=[x[1] for x in sub]
    mid=st.median([x[0] for x in sub])
    print(f"{lo:.1f}-{hi:<4.1f}%{'':<7}{len(r):>6}{st.mean(r):>+9.3f}{st.median(r):>+9.3f}"
          f"{sum(1 for x in r if x>0)/len(r):>7.1%}{2*0.0005/(mid/100):>+11.3f}")
# day-aggregated t-test on the largest band
big=[x for x in allt if x[0]>=1.5]
byday={}
for x in big: byday.setdefault(x[2],[]).append(x[1])
d=[st.mean(v) for v in byday.values()]
if len(d)>2:
    m=st.mean(d); sd=st.pstdev(d); tt=m/(sd/math.sqrt(len(d)))
    print(f"\nrisk>=1.5% 按日聚合: n={len(d)} 日均={m:+.4f} t={tt:+.2f} "
          f"{'显著' if abs(tt)>2 else '不显著'}")
    ds=sorted(byday); mid=ds[len(ds)//2]
    for lbl,sel in (("前半",[k for k in ds if k<mid]),("后半",[k for k in ds if k>=mid])):
        v=[st.mean(byday[k]) for k in sel]
        print(f"  {lbl}: n={len(v)} 日均={st.mean(v):+.4f}")
