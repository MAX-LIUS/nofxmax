#!/usr/bin/env python3
"""The only cell that looked like a real effect points the OPPOSITE way to the
hypothesis: LONG with LOTS of room above (opp>=1.5) won only 49.3%.
Before reporting it as a finding, check it survives time-split and placebo --
it is one cell among ~30 inspected, so it is a prime multiple-comparison trap.
"""
import json, statistics as st
rows=[json.loads(l) for l in open('trades.jsonl')]
for r in rows:
    d=1.0 if r["side"]=="LONG" else -1.0
    r["nret"]=(r["exit"]-r["entry"])*d/r["atr_price"]
med_t=st.median(r["entry_time"] for r in rows)
def w(sub): return (len(sub), sum(1 for r in sub if r["nret"]>0)/len(sub), st.median([r["nret"] for r in sub])) if sub else (0,0,0)
print("=== LONG opp>=1.5 时间半分 ===")
for lbl,pool in (("前半",[r for r in rows if r["entry_time"]<med_t]),("后半",[r for r in rows if r["entry_time"]>=med_t])):
    sub=[r for r in pool if r["side"]=="LONG" and r.get("opp_atr") is not None and r["opp_atr"]>=1.5]
    n,wr,m=w(sub); print(f"  {lbl} n={n} win={wr:.1%} med={m:+.3f}")
print("=== 安慰剂：LONG prot>=1.5（保护侧，应无效）===")
sub=[r for r in rows if r["side"]=="LONG" and r.get("prot_atr") is not None and r["prot_atr"]>=1.5]
n,wr,m=w(sub); print(f"  n={n} win={wr:.1%} med={m:+.3f}")
print("=== SHORT 对称位（若几何为真，应同样弱）===")
sub=[r for r in rows if r["side"]=="SHORT" and r.get("opp_atr") is not None and r["opp_atr"]>=1.5]
n,wr,m=w(sub); print(f"  SHORT opp>=1.5 n={n} win={wr:.1%} med={m:+.3f}  <-- 实际 63.6%,不对称")
