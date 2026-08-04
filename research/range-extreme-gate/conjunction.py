#!/usr/bin/env python3
import json, statistics as st
rows=[json.loads(l) for l in open('trades.jsonl')]
for r in rows:
    d=1.0 if r["side"]=="LONG" else -1.0
    r["nret"]=(r["exit"]-r["entry"])*d/r["atr_price"]
    rp=r.get("range_pos")
    r["adv"]=None if rp is None else (rp if r["side"]=="LONG" else 1-rp)

def s(lbl,sub):
    if len(sub)<5: print(f"  {lbl:<34} n={len(sub)} (样本不足)"); return
    n=[r["nret"] for r in sub]
    print(f"  {lbl:<34} n={len(n):<5} med={st.median(n):+.3f} win={sum(1 for x in n if x>0)/len(n):.1%}")

print("=== 用户描述的位置在样本中有多常见（按方向签名）===")
for side in ("LONG","SHORT"):
    p=[r for r in rows if r["side"]==side and r.get("range_pos") is not None]
    hi=[r for r in p if r["range_pos"]>=0.75]; lo=[r for r in p if r["range_pos"]<0.25]
    print(f"  {side}: 共{len(p)} 笔  区间上部(>=0.75)={len(hi)}  区间下部(<0.25)={len(lo)}")

print("\n=== adverse_pos = 逆势极限位置（多头在顶/空头在底）===")
s("adv>=0.75 逆势极限", [r for r in rows if r.get("adv") is not None and r["adv"]>=0.75])
s("adv 0.25~0.75 区间中部", [r for r in rows if r.get("adv") is not None and 0.25<=r["adv"]<0.75])
s("adv<0.25 顺势极限", [r for r in rows if r.get("adv") is not None and r["adv"]<0.25])

print("\n=== 交易 1926 所属组合：逆向A级区<0.5x + 逆势极限 ===")
tgt=[r for r in rows if r.get("opp_atr") is not None and r["opp_atr"]<0.5
     and r.get("opp_grade")=="A" and r.get("adv") is not None and r["adv"]>=0.75]
oth=[r for r in rows if r not in tgt]
s("该组合", tgt); s("其余", oth)

print("\n=== 分方向复核（避免 side 混淆）===")
for side in ("LONG","SHORT"):
    p=[r for r in rows if r["side"]==side and r.get("opp_atr") is not None]
    s(f"{side} opp<0.5",[r for r in p if r["opp_atr"]<0.5])
    s(f"{side} opp>=1.5",[r for r in p if r["opp_atr"]>=1.5])
