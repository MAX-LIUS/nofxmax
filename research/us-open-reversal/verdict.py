#!/usr/bin/env python3
"""engulf 30m is the only net-positive cell out of 24. With 24 cells tested,
one marginal positive is exactly what noise produces. Test it properly."""
import sys, glob, os, math, random, statistics as st
sys.path.insert(0,'.')
import engine2
from engine2 import backtest2
SYMS=sorted({os.path.basename(p).split('-')[0] for p in glob.glob("/tmp/usopen/*-5m-*.csv")})

def collect(**kw):
    out=[]
    for s in SYMS: out+=backtest2(s,4,"engulf",**kw)
    return out

t=collect()
byday={}
for x in t: byday.setdefault(x[0],[]).append(x[2])
daily=[st.mean(v) for v in byday.values()]
m=st.mean(daily); sd=st.pstdev(daily); n=len(daily)
tstat=m/(sd/math.sqrt(n)) if sd>0 else 0
print(f"=== engulf 30m 主检验（按交易日聚合，n={n} 日）===")
print(f"  日均 R={m:+.4f}  sd={sd:.3f}  t={tstat:+.2f}  "
      f"{'p<0.05 显著' if abs(tstat)>2.0 else 'p>0.05 不显著'}")
print(f"  门槛要求 > +0.10R -> {'过' if m>0.10 else '不过'}")

print("\n=== 时间半分 ===")
ds=sorted(byday); mid=ds[len(ds)//2]
for lbl,sel in (("前半",[k for k in ds if k<mid]),("后半",[k for k in ds if k>=mid])):
    v=[st.mean(byday[k]) for k in sel]
    print(f"  {lbl}: n={len(v)} 日均={st.mean(v):+.4f}")

print("\n=== 安慰剂 ===")
p1=collect(shift_pct=0.02)
print(f"  区间平移 2%:      n={len(p1)} 均值={st.mean([x[2] for x in p1]):+.3f}" if p1 else "  平移: 无信号")
p2=collect(open_utc=3*60+30)
print(f"  非开盘时段 03:30:  n={len(p2)} 均值={st.mean([x[2] for x in p2]):+.3f}" if p2 else "  非开盘: 无信号")
p3=collect(open_utc=17*60)
print(f"  盘中 17:00:       n={len(p3)} 均值={st.mean([x[2] for x in p3]):+.3f}" if p3 else "  盘中: 无信号")

print("\n=== 成本敏感性 ===")
for f,lbl in ((0.0002,"maker 0.02%"),(0.0005,"taker 0.05%"),(0.0010,"taker x2")):
    engine2.FEE=f
    r=[x[2] for x in collect()]
    print(f"  {lbl:<14} 均值={st.mean(r):+.3f}")
engine2.FEE=0.0005
