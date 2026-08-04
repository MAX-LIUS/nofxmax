#!/usr/bin/env python3
import sys, statistics as st
sys.path.insert(0,'.')
from engine import *

bars5=load5("SPYUSDT"); bars=agg(bars5,3); sess=sessions(bars)
bad=0; tot=0
for d in sorted(sess.keys()):
    atr=atr5d(sess,d)
    if not atr: continue
    z=zones(sess,d,3,6,0.5,atr)
    if not z: continue
    res_lo,res_hi,sup_lo,sup_hi=z
    day=sess[d]
    win=[b for b in day if OPEN_UTC<=minute(b[0])<OPEN_UTC+WINDOW_MIN]
    for b in win:
        ei=day.index(b)
        if ei+1>=len(day): continue
        entry=day[ei+1][1]
        # SHORT: stop above entry required
        if touches(b,res_lo,res_hi) and detect(day,ei,"wick","SHORT",res_lo,res_hi):
            stop=res_hi+0.25*atr; tot+=1
            if stop<=entry:
                bad+=1
                print(f"  BAD SHORT {d}: entry={entry:.3f} stop={stop:.3f} (止损在入场下方!) risk={abs(entry-stop):.4f}")
        if touches(b,sup_lo,sup_hi) and detect(day,ei,"wick","LONG",sup_lo,sup_hi):
            stop=sup_lo-0.25*atr; tot+=1
            if stop>=entry:
                bad+=1
                print(f"  BAD LONG {d}: entry={entry:.3f} stop={stop:.3f} (止损在入场上方!) risk={abs(entry-stop):.4f}")
print(f"\n信号总数={tot} 止损方向错误={bad} ({bad/tot:.1%})" if tot else "no signals")
print(f"5日ATR={atr:.4f}  区间: res=[{res_lo:.3f},{res_hi:.3f}] sup=[{sup_lo:.3f},{sup_hi:.3f}]")
