#!/usr/bin/env python3
import sys, statistics as st
sys.path.insert(0,'.')
from engine import *
bars5=load5("SPYUSDT"); bars=agg(bars5,3); sess=sessions(bars)
rs=[]
for d in sorted(sess.keys()):
    atr=atr5d(sess,d)
    if not atr: continue
    z=zones(sess,d,3,6,0.5,atr)
    if not z: continue
    res_lo,res_hi,sup_lo,sup_hi=z
    day=sess[d]
    for b in [x for x in day if OPEN_UTC<=minute(x[0])<OPEN_UTC+WINDOW_MIN]:
        ei=day.index(b)
        if ei+1>=len(day): continue
        entry=day[ei+1][1]
        if touches(b,res_lo,res_hi) and detect(day,ei,"wick","SHORT",res_lo,res_hi):
            stop=res_hi+0.25*atr
            if stop>entry:
                risk=stop-entry
                rs.append((risk,risk/atr,2*FEE*entry/risk,d))
        if touches(b,sup_lo,sup_hi) and detect(day,ei,"wick","LONG",sup_lo,sup_hi):
            stop=sup_lo-0.25*atr
            if stop<entry:
                risk=entry-stop
                rs.append((risk,risk/atr,2*FEE*entry/risk,d))
rs.sort()
print(f"n={len(rs)}  risk/ATR 分布:")
print(f"  最小 {rs[0][1]:.4f} ATR -> 成本 {rs[0][2]:+.2f}R  ({rs[0][3]})")
print(f"  p25  {rs[len(rs)//4][1]:.4f} ATR -> 成本 {rs[len(rs)//4][2]:+.2f}R")
print(f"  中位 {rs[len(rs)//2][1]:.4f} ATR -> 成本 {rs[len(rs)//2][2]:+.2f}R")
print(f"  最大 {rs[-1][1]:.4f} ATR -> 成本 {rs[-1][2]:+.2f}R")
print(f"\nrisk < 0.2 ATR 的占比: {sum(1 for x in rs if x[1]<0.2)/len(rs):.1%}")
print(f"成本 > 0.5R 的占比:    {sum(1 for x in rs if x[2]>0.5)/len(rs):.1%}")
