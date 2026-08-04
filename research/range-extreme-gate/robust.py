#!/usr/bin/env python3
import json, os, statistics as st
HERE=os.path.dirname(os.path.abspath(__file__))
rows=[json.loads(l) for l in open(os.path.join(HERE,"trades.jsonl"))]
for r in rows:
    d=1.0 if r["side"]=="LONG" else -1.0
    r["nret"]=(r["exit"]-r["entry"])*d/r["atr_price"]

def auc(pool, var, label):
    """AUC of var predicting a LOSS. 0.50 = no information."""
    p=[r for r in pool if r.get(var) is not None]
    pos=[r[var] for r in p if r["nret"]<=0]
    neg=[r[var] for r in p if r["nret"]>0]
    if not pos or not neg: return
    wins=ties=0
    neg_s=sorted(neg)
    import bisect
    for x in pos:
        lo=bisect.bisect_left(neg_s,x); hi=bisect.bisect_right(neg_s,x)
        wins+=lo; ties+=hi-lo
    a=(wins+0.5*ties)/(len(pos)*len(neg))
    print(f"  AUC({label:<10} -> 亏损) = {a:.3f}   n_loss={len(pos)} n_win={len(neg)}")

print("=== AUC：变量预测亏损的能力（0.50 = 无信息）===")
for v in ("opp_atr","prot_atr","dense_atr","period_atr","adverse_pos"):
    for r in rows:
        rp=r.get("range_pos")
        r["adverse_pos"]=None if rp is None else (rp if r["side"]=="LONG" else 1-rp)
    auc(rows,v,v)

def trimmed(xs,frac=0.05):
    xs=sorted(xs); k=int(len(xs)*frac)
    return st.mean(xs[k:len(xs)-k]) if len(xs)-2*k>0 else float('nan')

print("\n=== 均值 vs 中位数 vs 5% 截尾均值（检验离群点主导）===")
for lo,hi,lbl in ((None,0.5,"opp<0.5"),(0.5,1.5,"0.5~1.5"),(1.5,None,"opp>=1.5")):
    sub=[r for r in rows if r.get("opp_atr") is not None and (lo is None or r["opp_atr"]>=lo) and (hi is None or r["opp_atr"]<hi)]
    n=[r["nret"] for r in sub]
    top=sorted(n)[-3:]
    print(f"  {lbl:<9} n={len(n):<5} mean={st.mean(n):+.3f} med={st.median(n):+.3f} "
          f"trim5%={trimmed(n):+.3f} 最大3笔={[round(x,1) for x in top]}")

print("\n=== 只看亏损单：opp_atr 能否预测亏得更多 ===")
for lo,hi,lbl in ((None,0.5,"opp<0.5"),(0.5,1.5,"0.5~1.5"),(1.5,None,"opp>=1.5")):
    sub=[r["nret"] for r in rows if r.get("opp_atr") is not None and r["nret"]<=0
         and (lo is None or r["opp_atr"]>=lo) and (hi is None or r["opp_atr"]<hi)]
    if sub: print(f"  {lbl:<9} n={len(sub):<5} 平均亏损={st.mean(sub):+.3f} 中位={st.median(sub):+.3f}")

print("\n=== A 级高置信逆向区 + 极近（用户描述的最坏情形）===")
worst=[r for r in rows if r.get("opp_atr") is not None and r["opp_atr"]<0.6
       and r.get("opp_grade")=="A" and (r.get("opp_conf") or 0)>=60]
rest=[r for r in rows if r not in worst]
for lbl,sub in (("worst(A,conf>=60,<0.6)",worst),("其余",rest)):
    n=[r["nret"] for r in sub]
    if n: print(f"  {lbl:<24} n={len(n):<5} mean={st.mean(n):+.3f} med={st.median(n):+.3f} "
                f"win={sum(1 for x in n if x>0)/len(n):.1%}")
