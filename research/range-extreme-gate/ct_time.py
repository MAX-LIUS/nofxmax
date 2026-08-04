#!/usr/bin/env python3
import json, statistics as st, datetime
from collections import defaultdict
rows=[json.loads(l) for l in open('trades.jsonl')]
for r in rows:
    d=1.0 if r["side"]=="LONG" else -1.0
    r["nret"]=(r["exit"]-r["entry"])*d/r["atr_price"]
    r["mon"]=datetime.datetime.fromtimestamp(r["entry_time"]/1000,datetime.timezone.utc).strftime("%Y-%m")
L=[r for r in rows if r["side"]=="LONG" and r.get("counter_trend") is not None]
mt=st.median(r["entry_time"] for r in L)
print("时间半分边界:",datetime.datetime.fromtimestamp(mt/1000,datetime.timezone.utc).strftime("%Y-%m-%d %H:%M UTC"))
print(f"\n{'月份':<9}{'逆势n':>6}{'逆势med':>9}{'逆势win':>8}{'顺势n':>6}{'顺势med':>9}{'顺势win':>8}{'差':>8}")
g=defaultdict(lambda:[[],[]])
for r in L: g[r["mon"]][0 if r["counter_trend"] else 1].append(r)
for k in sorted(g):
    c,w=g[k]
    if len(c)>=10 and len(w)>=10:
        mc=st.median([r["nret"] for r in c]); mw=st.median([r["nret"] for r in w])
        wc=sum(1 for r in c if r["nret"]>0)/len(c); ww=sum(1 for r in w if r["nret"]>0)/len(w)
        print(f"{k:<9}{len(c):>6}{mc:>+9.3f}{wc:>7.1%}{len(w):>6}{mw:>+9.3f}{ww:>7.1%}{mc-mw:>+8.3f}")
    else:
        print(f"{k:<9}{len(c):>6}{'--':>9}{'--':>8}{len(w):>6}{'--':>9}{'--':>8}  样本不足")
