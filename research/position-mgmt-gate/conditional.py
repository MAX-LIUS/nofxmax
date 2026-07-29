"""管理规则在**本来就赚钱**的入场集合上,还是不是零和?

为什么必须补这一测
==================
前面所有结论都建立在一个净期望 −4.7bps 的固定入场集合上。拿一个亏钱的入场集合去证明
"管理创造不了价值",逻辑上不完整 —— 管理本来就不该救活一个坏入场。
真正的决策问题是:**在我们实际会开的那种仓位上**,双向管理能不能既提胜率又不伤期望。

做法
====
1. 用完全不看未来的**分层变量**把入场集合切开(趋势强度、波动状态、方向、币),
   每一层的分层变量都在入场前可得;
2. 找出基线净期望**为正**的层;
3. 只在这些层上重跑冠军与备选,看 Δ胜率 / Δ期望 / Δ夏普;
4. 分层是**在选拔期数据上**定的阈值,再到后续时间上验收,避免"挑一个赚钱的子集"
   这种事后选择。

第 4 点是关键。直接在全样本上找"基线赚钱的层"然后在同一批数据上夸管理有效,
等于两次使用同一份数据 —— 我在厂商模型那一轮已经因为类似的合并口径栽过一次。
"""
from __future__ import annotations
import os
import numpy as np
import pandas as pd
from scipy import stats

import pmgate as P
import fastsim as F
import tournament as T

CHAMP = "tr0.25a1.0"
CANDS = [CHAMP, "sl4.0", "sl3.0", "tp1.0f0.5", "tp2.0f0.5", "tr0.5a1.0",
         "tr1.0a1.0", "c_sl3.0_g0.5_tp1.0f0.5", "time48r0.0"]
SPLIT = pd.Timestamp("2024-07-01", tz="UTC")   # 前段定阈值,后段验收


def gather(paths, lib, names):
    """把所有笔的 net 与分层变量拉平成一张表。"""
    want = {"baseline": {}}
    for n in names:
        if n in lib:
            want[n] = lib[n]
    cols = {k: [] for k in want}
    meta = {"ts": [], "side": [], "slope": [], "volr": [], "atr": [], "sym": []}
    for pp in paths:
        f = P.cached_features(pp["sym"])
        idx = pp["idx"]
        sub = pp
        for name, params in want.items():
            cols[name].append(F.simulate_variant(sub, params)["net"])
        meta["ts"].append(pp["ts"])
        meta["side"].append(pp["sides"])
        meta["slope"].append(f["slope168"].to_numpy()[idx])
        meta["volr"].append(f["vol_ratio"].to_numpy()[idx])
        meta["atr"].append(f["atr_pct"].to_numpy()[idx])
        meta["sym"].append(np.full(len(idx), pp["sym"], dtype=object))
        del f
    d = {k: np.concatenate(v) for k, v in cols.items()}
    m = {k: np.concatenate(v) for k, v in meta.items()}
    df = pd.DataFrame(m)
    for k, v in d.items():
        df["net_" + k] = v
    return df


def report(tag, sub, names):
    b = sub["net_baseline"].to_numpy()
    if len(b) < 500:
        print(f"  {tag}: 样本 {len(b)} 不足, 跳过")
        return
    bw, be = float((b > 0).mean()), float(b.mean() * 1e4)
    bsd = b.std(ddof=1)
    print(f"  {tag}  n={len(b)}  基线胜率 {bw:.3f} 期望 {be:+.1f}bps "
          f"夏普 {b.mean()/bsd if bsd>0 else np.nan:+.4f}")
    for n in names:
        c = sub["net_" + n].to_numpy()
        csd = c.std(ddof=1)
        _, p = stats.ttest_rel(c, b)
        print(f"    {n:<24} Δ胜率 {float((c>0).mean())-bw:+.3f}  "
              f"Δ期望 {float(c.mean()*1e4)-be:+7.1f}bps  "
              f"Δ夏普 {(c.mean()/csd if csd>0 else np.nan)-(b.mean()/bsd if bsd>0 else np.nan):+.4f}  "
              f"配对t p={p:.4f}")


def main():
    syms = sorted(f[:-4] for f in os.listdir(P.CACHE) if f.endswith(".pkl"))
    lib = T.variant_library()
    paths = T.load_paths(syms)
    df = gather(paths, lib, CANDS)
    df["t"] = pd.to_datetime(df.ts, utc=True)
    early = df[df.t < SPLIT]
    late = df[df.t >= SPLIT]
    print(f"总 {len(df)} 笔;定阈值段 {len(early)} 笔(<{SPLIT.date()}), "
          f"验收段 {len(late)} 笔\n")

    # ---- 在定阈值段找基线赚钱的层 ----
    print("===== 第一步:在定阈值段找基线净期望为正的层 =====")
    early = early.copy()
    early["absslope"] = early.slope.abs()
    qs = early.absslope.quantile([0.2, 0.4, 0.6, 0.8]).to_numpy()
    early["sbin"] = np.digitize(early.absslope, qs)
    vq = early.volr.quantile([0.33, 0.66]).to_numpy()
    early["vbin"] = np.digitize(early.volr, vq)
    good = []
    for (sb, vb), g in early.groupby(["sbin", "vbin"]):
        e = g.net_baseline.mean() * 1e4
        flag = "  ← 基线赚钱" if e > 0 else ""
        print(f"  趋势强度层{sb} 波动层{vb}: n={len(g):>6} 基线期望 {e:+7.1f}bps{flag}")
        if e > 0 and len(g) > 800:
            good.append((sb, vb))
    if not good:
        print("\n  定阈值段没有任何一层基线赚钱 —— 无法构造'本来就赚钱的入场集合',"
              "本测无法给出正面结论。")
        return
    print(f"\n  选中 {len(good)} 层: {good}")

    # ---- 在验收段用同样的阈值切,只看这些层 ----
    late = late.copy()
    late["absslope"] = late.slope.abs()
    late["sbin"] = np.digitize(late.absslope, qs)
    late["vbin"] = np.digitize(late.volr, vq)
    mask = np.zeros(len(late), dtype=bool)
    for (sb, vb) in good:
        mask |= (late.sbin.to_numpy() == sb) & (late.vbin.to_numpy() == vb)
    sel = late[mask]
    print(f"\n===== 第二步:验收段(阈值来自前段,未回头调整) =====")
    report("选中层", sel, CANDS)
    print()
    report("全部(对照)", late, CANDS)

    print("\n===== 分方向(验收段选中层) =====")
    for s, nm in ((1, "多头"), (-1, "空头")):
        report(nm, sel[sel.side == s], CANDS[:5])


if __name__ == "__main__":
    main()
