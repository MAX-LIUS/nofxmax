#!/usr/bin/env python3
"""Judge the counter-trend filter against CRITERIA_COUNTERTREND.md."""
import json, statistics as st
rows=[json.loads(l) for l in open('trades.jsonl')]
for r in rows:
    d=1.0 if r["side"]=="LONG" else -1.0
    r["nret"]=(r["exit"]-r["entry"])*d/r["atr_price"]
    r["atr_pct"]=r["atr_price"]/r["entry"]*100

def stat(sub):
    n=[r["nret"] for r in sub]
    return dict(n=len(n),med=st.median(n),mean=st.mean(n),
                win=sum(1 for x in n if x>0)/len(n))
def line(lbl,sub):
    if len(sub)<10: print(f"  {lbl:<30} n={len(sub)} (样本不足)"); return None
    s=stat(sub)
    print(f"  {lbl:<30} n={s['n']:<5} med={s['med']:+.3f} mean={s['mean']:+.3f} win={s['win']:.1%}")
    return s

ct=[r for r in rows if r.get("counter_trend") is True]
wt=[r for r in rows if r.get("counter_trend") is False]
print("=== [1] 效应量：逆势 vs 顺势（门槛 med差<=-0.10 且 胜率差<=-3pp）===")
a=line("逆势 counter_trend",ct); b=line("顺势 with_trend",wt)
print(f"  -> 中位差 {a['med']-b['med']:+.3f} ATR, 胜率差 {(a['win']-b['win'])*100:+.1f}pp")

print("\n=== [2] 时间半分（两半须同号）===")
mt=st.median(r["entry_time"] for r in rows)
for lbl,pool in (("前半",[r for r in rows if r["entry_time"]<mt]),
                 ("后半",[r for r in rows if r["entry_time"]>=mt])):
    c=[r for r in pool if r.get("counter_trend") is True]
    w=[r for r in pool if r.get("counter_trend") is False]
    sc,sw=stat(c),stat(w)
    print(f"  {lbl}: 逆势 n={sc['n']} med={sc['med']:+.3f} win={sc['win']:.1%} | "
          f"顺势 n={sw['n']} med={sw['med']:+.3f} win={sw['win']:.1%} | "
          f"中位差 {sc['med']-sw['med']:+.3f}")

print("\n=== [4] 分方向各自成立？（只单边成立可能是 beta）===")
for side in ("LONG","SHORT"):
    c=[r for r in rows if r["side"]==side and r.get("counter_trend") is True]
    w=[r for r in rows if r["side"]==side and r.get("counter_trend") is False]
    sc=line(f"{side} 逆势",c); sw=line(f"{side} 顺势",w)
    if sc and sw:
        print(f"    -> {side} 中位差 {sc['med']-sw['med']:+.3f} 胜率差 {(sc['win']-sw['win'])*100:+.1f}pp")

print("\n=== [5] 是否只是止损宽度的代言（控制 atr_pct）===")
med_atr=st.median(r["atr_pct"] for r in rows)
print(f"  逆势组 atr_pct 中位={st.median([r['atr_pct'] for r in ct]):.3f} "
      f"顺势组={st.median([r['atr_pct'] for r in wt]):.3f}")
for lbl,lo,hi in (("低波动 atr_pct<中位",None,med_atr),("高波动 atr_pct>=中位",med_atr,None)):
    c=[r for r in ct if (lo is None or r["atr_pct"]>=lo) and (hi is None or r["atr_pct"]<hi)]
    w=[r for r in wt if (lo is None or r["atr_pct"]>=lo) and (hi is None or r["atr_pct"]<hi)]
    sc,sw=stat(c),stat(w)
    print(f"  {lbl}: 逆势 n={sc['n']} med={sc['med']:+.3f} | 顺势 n={sw['n']} med={sw['med']:+.3f} | 差 {sc['med']-sw['med']:+.3f}")

print("\n=== 附：按 regime 细分逆势 ===")
from collections import defaultdict
g=defaultdict(list)
for r in ct: g[r.get("regime")].append(r)
for k,v in sorted(g.items(),key=lambda x:-len(x[1])):
    line(f"逆势 @ {k}",v)
