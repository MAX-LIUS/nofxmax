#!/usr/bin/env python3
"""Score the range-extreme hypothesis against CRITERIA.md (pre-registered)."""
import json, os, statistics as st

HERE = os.path.dirname(os.path.abspath(__file__))
rows = [json.loads(l) for l in open(os.path.join(HERE, "trades.jsonl"))]

# Primary metric: ATR-normalised return. NOT realized_pnl -- size/leverage vary
# across the sample, so money would conflate "big bet" with "good signal".
for r in rows:
    d = 1.0 if r["side"] == "LONG" else -1.0
    r["nret"] = (r["exit"] - r["entry"]) * d / r["atr_price"]

def summarise(sub):
    if not sub:
        return None
    n = [r["nret"] for r in sub]
    return dict(n=len(n), mean=st.mean(n),
                med=st.median(n),
                win=sum(1 for x in n if x > 0) / len(n))

def show(label, sub):
    s = summarise(sub)
    if not s:
        print(f"  {label:<22} (empty)"); return
    print(f"  {label:<22} n={s['n']:<5} mean={s['mean']:+.3f} "
          f"med={s['med']:+.3f} win={s['win']:.1%}")

def bins(var, edges, pool):
    out = []
    for lo, hi in zip([None] + edges, edges + [None]):
        sub = [r for r in pool if r.get(var) is not None
               and (lo is None or r[var] >= lo)
               and (hi is None or r[var] < hi)]
        lbl = f"{var} {'' if lo is None else f'{lo:g}'}~{'inf' if hi is None else f'{hi:g}'}"
        out.append((lbl, sub))
    return out

EDGES = [0.5, 1.0, 1.5, 2.5]

print(f"=== 样本 {len(rows)} 笔（已平仓，几何可解析）===")
show("ALL", rows)

print("\n=== [1] 单调性：opp_atr（逆向侧，真实假设）===")
for lbl, sub in bins("opp_atr", EDGES, rows):
    show(lbl, sub)

print("\n=== [2] 安慰剂：prot_atr（同向保护侧，应无效）===")
for lbl, sub in bins("prot_atr", EDGES, rows):
    show(lbl, sub)

print("\n=== [3] 时间半分（按 entry_time 中位数切两半）===")
med_t = st.median(r["entry_time"] for r in rows)
for half, pool in (("前半", [r for r in rows if r["entry_time"] < med_t]),
                   ("后半", [r for r in rows if r["entry_time"] >= med_t])):
    tight = [r for r in pool if r.get("opp_atr") is not None and r["opp_atr"] < 0.5]
    wide = [r for r in pool if r.get("opp_atr") is not None and r["opp_atr"] >= 1.5]
    st_ = summarise(tight); sw = summarise(wide)
    if st_ and sw:
        print(f"  {half}: tight(<0.5) n={st_['n']} mean={st_['mean']:+.3f} | "
              f"wide(>=1.5) n={sw['n']} mean={sw['mean']:+.3f} | "
              f"差 {st_['mean'] - sw['mean']:+.3f}")

print("\n=== [4] 其它候选变量 ===")
for var in ("dense_atr", "period_atr"):
    print(f"  -- {var} --")
    for lbl, sub in bins(var, [0.5, 1.0, 2.0], rows):
        show("   " + lbl, sub)
print("  -- range_pos（用户提到的区间极限位置，未签名方向）--")
for lbl, sub in bins("range_pos", [0.25, 0.75], rows):
    show("   " + lbl, sub)
print("  -- range_pos 按方向签名：extreme = 多头在顶/空头在底 --")
for r in rows:
    rp = r.get("range_pos")
    r["adverse_pos"] = None if rp is None else (rp if r["side"] == "LONG" else 1 - rp)
for lbl, sub in bins("adverse_pos", [0.25, 0.75], rows):
    show("   " + lbl, sub)

print("\n=== [5] 缩仓分支（风险重归一化）===")
# Comparing "1/3 size" against "full size" on raw money is meaningless: betting
# less always loses less. Renormalise -- if the sub-population's per-unit-risk
# return is negative, cutting size helps; if it is positive, cutting size costs.
for thr in (0.5, 0.75, 1.0):
    sub = [r for r in rows if r.get("opp_atr") is not None and r["opp_atr"] < thr]
    s = summarise(sub)
    if not s:
        continue
    full = s["mean"] * s["n"]
    third = s["mean"] / 3 * s["n"]
    print(f"  opp_atr<{thr}: n={s['n']} 单位风险均值={s['mean']:+.3f} "
          f"全仓合计={full:+.1f} 1/3仓合计={third:+.1f} "
          f"节省={full - third:+.1f} ATR")
