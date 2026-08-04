#!/usr/bin/env python3
"""The user's claim is "带概率会被打回反转" -- a LEFT-TAIL claim, not a median
claim. Counter-trend LONG has median +0.116 but mean -0.094: wins often, loses
big. Test the tail directly.

Pre-stated before looking: the tail claim counts as established only if the
worst-decile mean is worse for counter-trend in BOTH time halves AND in >=2 of
3 traders. Same discipline as the median test that just failed.
"""
import json, random, statistics as st
from collections import defaultdict
rows=[json.loads(l) for l in open('trades.jsonl')]
for r in rows:
    d=1.0 if r["side"]=="LONG" else -1.0
    r["nret"]=(r["exit"]-r["entry"])*d/r["atr_price"]
L=[r for r in rows if r["side"]=="LONG" and r.get("counter_trend") is not None]
def tail(sub,q=0.10):
    x=sorted(r["nret"] for r in sub); k=max(1,int(len(x)*q))
    return st.mean(x[:k]), x[k-1]
def rep(lbl,c,w):
    tc,pc=tail(c); tw,pw=tail(w)
    print(f"  {lbl:<22} 逆势 n={len(c):<4} 最差十分位均值={tc:+.3f} p10={pc:+.3f} | "
          f"顺势 n={len(w):<4} {tw:+.3f} p10={pw:+.3f} | 差 {tc-tw:+.3f}")
    return tc-tw

ct=[r for r in L if r["counter_trend"]]; wt=[r for r in L if not r["counter_trend"]]
print("=== 全样本 ===")
rep("ALL LONG",ct,wt)
print(f"  均值 逆势={st.mean([r['nret'] for r in ct]):+.3f} 顺势={st.mean([r['nret'] for r in wt]):+.3f}")
print(f"  亏损单平均亏幅 逆势={st.mean([r['nret'] for r in ct if r['nret']<=0]):+.3f} "
      f"顺势={st.mean([r['nret'] for r in wt if r['nret']<=0]):+.3f}")
print(f"  <-2ATR 大亏占比 逆势={sum(1 for r in ct if r['nret']<-2)/len(ct):.1%} "
      f"顺势={sum(1 for r in wt if r['nret']<-2)/len(wt):.1%}")

print("\n=== 时间半分（须同号）===")
mt=st.median(r["entry_time"] for r in L); signs=[]
for lbl,pool in (("前半",[r for r in L if r["entry_time"]<mt]),("后半",[r for r in L if r["entry_time"]>=mt])):
    c=[r for r in pool if r["counter_trend"]]; w=[r for r in pool if not r["counter_trend"]]
    signs.append(rep(lbl,c,w))
print(f"  -> {'同号,通过' if signs[0]<0 and signs[1]<0 else '反号,不通过'}")

print("\n=== 分交易员（须 >=2/3 同号）===")
g=defaultdict(lambda:[[],[]]); ok=0; tot=0
for r in L: g[r["trader"]][0 if r["counter_trend"] else 1].append(r)
for t,(c,w) in sorted(g.items(),key=lambda x:-len(x[1][0])):
    if len(c)>=25 and len(w)>=25:
        d=rep(t,c,w); tot+=1; ok+= d<0
print(f"  -> {ok}/{tot} 同号 {'通过' if ok>=2 else '不通过'}")

print("\n=== bootstrap 95%CI（最差十分位均值差）===")
random.seed(11)
a=[r["nret"] for r in ct]; b=[r["nret"] for r in wt]
def td(x):
    x=sorted(x); k=max(1,int(len(x)*0.10)); return st.mean(x[:k])
ds=sorted(td(random.choices(a,k=len(a)))-td(random.choices(b,k=len(b))) for _ in range(4000))
print(f"  点估计 {td(a)-td(b):+.3f}  95%CI [{ds[100]:+.3f}, {ds[3899]:+.3f}]  "
      f"{'不含 0 -> 稳健' if ds[3899]<0 else '含 0 -> 不稳健'}")
