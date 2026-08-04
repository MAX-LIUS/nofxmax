#!/usr/bin/env python3
"""Criterion 4 failed one-sided: LONG counter-trend looks real, SHORT does not.
Since any action would target LONG only, re-test criteria 2/3 ON THE LONG SUBSET,
and rule out the obvious confounds (one trader, one regime, one symbol)."""
import json, random, statistics as st
from collections import defaultdict
rows=[json.loads(l) for l in open('trades.jsonl')]
for r in rows:
    d=1.0 if r["side"]=="LONG" else -1.0
    r["nret"]=(r["exit"]-r["entry"])*d/r["atr_price"]
L=[r for r in rows if r["side"]=="LONG" and r.get("counter_trend") is not None]
ct=[r for r in L if r["counter_trend"]]; wt=[r for r in L if not r["counter_trend"]]
def med(s): return st.median([r["nret"] for r in s])
def win(s): return sum(1 for r in s if r["nret"]>0)/len(s)

print("=== LONG 逆势：时间半分（我要动手的子集，必须自己成立）===")
mt=st.median(r["entry_time"] for r in L)
for lbl,pool in (("前半",[r for r in L if r["entry_time"]<mt]),("后半",[r for r in L if r["entry_time"]>=mt])):
    c=[r for r in pool if r["counter_trend"]]; w=[r for r in pool if not r["counter_trend"]]
    if len(c)>=20 and len(w)>=20:
        print(f"  {lbl}: 逆势 n={len(c)} med={med(c):+.3f} win={win(c):.1%} | "
              f"顺势 n={len(w)} med={med(w):+.3f} win={win(w):.1%} | 差 {med(c)-med(w):+.3f}")

print("\n=== bootstrap 95%CI（LONG 逆势 - LONG 顺势 中位差）===")
random.seed(7)
a=[r["nret"] for r in ct]; b=[r["nret"] for r in wt]
ds=[]
for _ in range(4000):
    ds.append(st.median(random.choices(a,k=len(a)))-st.median(random.choices(b,k=len(b))))
ds.sort()
lo,hi=ds[100],ds[3899]
print(f"  点估计 {med(ct)-med(wt):+.3f}  95%CI [{lo:+.3f}, {hi:+.3f}]  "
      f"{'不含 0 -> 稳健' if hi<0 else '含 0 -> 不稳健'}")

print("\n=== 混杂 1：是不是某个交易员/模型的问题 ===")
g=defaultdict(lambda:[[],[]])
for r in L: g[r["trader"]][0 if r["counter_trend"] else 1].append(r)
for t,(c,w) in sorted(g.items(),key=lambda x:-len(x[1][0])):
    if len(c)>=25 and len(w)>=25:
        print(f"  {t}: 逆势 n={len(c)} med={med(c):+.3f} | 顺势 n={len(w)} med={med(w):+.3f} | 差 {med(c)-med(w):+.3f}")

print("\n=== 混杂 2：是不是某个 regime 独占 ===")
g=defaultdict(lambda:[[],[]])
for r in L: g[r.get("regime")][0 if r["counter_trend"] else 1].append(r)
for k,(c,w) in sorted(g.items(),key=lambda x:-len(x[1][0])):
    if len(c)>=20 and len(w)>=20:
        print(f"  {k:<12}: 逆势 n={len(c)} med={med(c):+.3f} win={win(c):.1%} | 顺势 n={len(w)} med={med(w):+.3f} | 差 {med(c)-med(w):+.3f}")

print("\n=== 混杂 3：是不是少数币种独占 ===")
g=defaultdict(list)
for r in ct: g[r["symbol"]].append(r)
top=sorted(g.items(),key=lambda x:-len(x[1]))[:5]
print("  逆势多单最集中的 5 个币:",[(k,len(v)) for k,v in top])
excl={k for k,_ in top}
c2=[r for r in ct if r["symbol"] not in excl]; w2=[r for r in wt if r["symbol"] not in excl]
print(f"  剔除这 5 币后: 逆势 n={len(c2)} med={med(c2):+.3f} | 顺势 n={len(w2)} med={med(w2):+.3f} | 差 {med(c2)-med(w2):+.3f}")

print("\n=== 缩仓收益（风险重归一化，判据 1 的教训）===")
for f,lbl in ((1/3,"缩到 1/3"),(1/2,"缩到 1/2")):
    m=st.mean(a)
    print(f"  {lbl}: 逆势多单单位风险均值={m:+.3f} n={len(a)} "
          f"全仓合计={m*len(a):+.1f} 缩后={m*f*len(a):+.1f} ATR 差={m*len(a)*(f-1):+.1f}")
