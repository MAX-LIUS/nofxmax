#!/usr/bin/env python3
import sys, os, glob, json, statistics as st
sys.path.insert(0, '.')
from engine2 import backtest2

SYMS = sorted({os.path.basename(p).split('-')[0]
               for p in glob.glob("/tmp/usopen/*-5m-*.csv")})
PATS = ["wick", "engulf", "reclaim", "volsurge"]
MULTS = [1, 2, 3, 4, 6, 12]   # 5,10,15,20,30,60m
out = []
for pat in PATS:
    for mult in MULTS:
        allt = []
        for s in SYMS:
            allt += backtest2(s, mult, pat)
        if len(allt) < 20:
            continue
        r = [x[2] for x in allt]
        # aggregate by DAY (same-day trades share market beta)
        byday = {}
        for x in allt:
            byday.setdefault(x[0], []).append(x[2])
        daily = [st.mean(v) for v in byday.values()]
        rec = dict(pat=pat, tf=mult * 5, n=len(r), days=len(daily),
                   mean=st.mean(r), med=st.median(r),
                   win=sum(1 for x in r if x > 0) / len(r),
                   dmean=st.mean(daily),
                   dsd=st.pstdev(daily) if len(daily) > 1 else 0,
                   best=max(r), worst=min(r))
        out.append(rec)
        print(f"{pat:<9}{mult*5:>3}m n={len(r):<5} 日数={len(daily):<4} "
              f"均值={st.mean(r):+.3f} 中位={st.median(r):+.3f} "
              f"胜率={rec['win']:.1%} 日均={rec['dmean']:+.3f} 最好={max(r):+.1f}",
              file=sys.stderr)
json.dump(out, open("/tmp/grid2.json", "w"))
print("WROTE", len(out), file=sys.stderr)
