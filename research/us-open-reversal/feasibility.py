#!/usr/bin/env python3
"""The decisive calculation: is a 1-ATR stop even payable after fees?

R = pnl/risk. If risk = 1 ATR and the round-trip taker fee is F% of price,
then the fee alone costs F/ATR_pct in R units. When that approaches or exceeds
1.0, no entry rule can be profitable at 1-ATR stops -- the edge required is
larger than the entire move being traded.
"""
import csv, os, statistics as st, datetime, glob
FEE_RT = 0.0010  # 0.05% taker x 2 sides
print(f"{'symbol':<13}{'price':>10}{'ATR5m':>10}{'ATR%':>8}{'费/ATR':>9}{'1ATR止损需胜率':>14}")
rows=[]
for p in sorted(glob.glob("/tmp/usopen/*-5m-2026-06.csv")):
    sym=os.path.basename(p).split('-')[0]
    trs=[]; px=[]
    with open(p) as fh:
        for r in csv.reader(fh):
            if not r or r[0].startswith("open_time"): continue
            hi,lo,c=float(r[2]),float(r[3]),float(r[4])
            trs.append(hi-lo); px.append(c)
    if len(trs)<100: continue
    atr=st.mean(trs); price=st.median(px)
    atr_pct=atr/price*100
    fee_r=FEE_RT/(atr/price)          # fee expressed in R at 1-ATR risk
    # breakeven win rate at 2R target with fee_r cost
    # p*2 - (1-p)*1 - fee_r = 0  ->  p = (1+fee_r)/3
    be=(1+fee_r)/3
    rows.append((sym,price,atr,atr_pct,fee_r,be))
for x in sorted(rows,key=lambda y:y[4]):
    print(f"{x[0]:<13}{x[1]:>10.3f}{x[2]:>10.4f}{x[3]:>7.3f}%{x[4]:>9.2f}{x[5]:>13.1%}")
