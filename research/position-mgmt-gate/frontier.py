"""胜率 / 净期望的权衡前沿,以及冠军变种的逐季稳定性。

要回答的三个问题
================
Q1 前沿长什么样?是否存在"胜率明显更高、且净期望不低于基线"的点?
   循环选拔的合并结果里似乎有(`tr0.25a1.0` 胜率 +0.293 且期望 +1.5bps),
   但合并会骗人 —— 我在厂商模型那一轮已经栽过一次(合并 p=0.0000、按季等权翻负)。
Q2 冠军 `tr0.25a1.0` 的增益**逐季**是否稳住?按季等权后还剩多少?
Q3 高胜率是不是纯粹用"小赢多、大亏少"换来的?
   量化:平均盈利 / 平均亏损 / 盈亏比 / 尾部亏损占比。

另外做一件循环选拔没做的事:**固定变种的逐季符号检验**。
"""
from __future__ import annotations
import os
import numpy as np
import pandas as pd
from scipy import stats

import pmgate as P
import fastsim as F
import tournament as T

WORK = "/root/.claude/jobs/5cbb3cf4/tmp/cdeval"
CHAMP = "tr0.25a1.0"


def main():
    syms = sorted(f[:-4] for f in os.listdir(P.CACHE) if f.endswith(".pkl"))
    lib = T.variant_library()
    paths = T.load_paths(syms)
    edges = pd.date_range("2022-01-01", "2026-07-01", freq="QS", tz="UTC")

    # ---------------- Q1: 全期前沿 ----------------
    allo = T.run_window(paths, lib, edges[0], edges[-1])
    bb = F.summarize_arr(allo["baseline"])
    print(f"基线(零管理): 胜率 {bb['win_rate']:.3f}  净期望 {bb['exp_bps']:+.1f}bps  "
          f"PF {bb['pf']:.3f}  均盈 {bb['avg_win_bps']:.0f} 均亏 {bb['avg_loss_bps']:.0f}  "
          f"n={bb['n']}")
    print("注意基线净期望为负 —— 固定入场规则本身不赚钱。所以下面比的是"
          "「管理能不能把一个不赚钱的入场集合变好」,不是「策略赚不赚」。\n")

    rows = []
    for name in lib:
        if name == "baseline":
            continue
        s = F.summarize_arr(allo[name])
        if s.get("n", 0) < T.MIN_N:
            continue
        rows.append({"name": name, "win": s["win_rate"], "exp": s["exp_bps"],
                     "pf": s["pf"], "aw": s["avg_win_bps"], "al": s["avg_loss_bps"],
                     "red": s["reduces"], "sd": s["sd_bps"], "n": s["n"]})
    df = pd.DataFrame(rows)
    both = df[(df.win > bb["win_rate"]) & (df.exp >= bb["exp_bps"])].sort_values(
        "win", ascending=False)
    print(f"===== Q1 同时满足「胜率>基线」且「净期望>=基线」的变种: "
          f"{len(both)}/{len(df)} =====")
    if len(both):
        print(f"  {'变种':<24}{'胜率':>7}{'Δ胜率':>8}{'期望bps':>9}{'Δ期望':>8}"
              f"{'PF':>6}{'盈亏比':>7}{'减仓':>6}")
        for _, r in both.head(10).iterrows():
            print(f"  {r['name']:<24}{r['win']:>7.3f}{r['win']-bb['win_rate']:>+8.3f}"
                  f"{r['exp']:>9.1f}{r['exp']-bb['exp_bps']:>+8.1f}{r['pf']:>6.2f}"
                  f"{abs(r['aw']/r['al']):>7.3f}{r['red']:>6.2f}")
    else:
        print("  一个都没有。")

    # ---------------- Q2: 冠军逐季 ----------------
    print(f"\n===== Q2 冠军 {CHAMP} 逐季(等权,不合并) =====")
    print(f"  {'季度':<12}{'n':>7}{'胜率':>8}{'基线':>8}{'Δ胜率':>8}"
          f"{'期望':>9}{'基线':>9}{'Δ期望':>9}")
    dwin, dexp = [], []
    for k in range(len(edges) - 1):
        w = T.run_window(paths, {"baseline": {}, CHAMP: lib[CHAMP]}, edges[k], edges[k + 1])
        b = F.summarize_arr(w["baseline"])
        c = F.summarize_arr(w[CHAMP])
        if b.get("n", 0) < T.MIN_N:
            continue
        dwin.append(c["win_rate"] - b["win_rate"])
        dexp.append(c["exp_bps"] - b["exp_bps"])
        print(f"  {str(edges[k].date()):<12}{c['n']:>7}{c['win_rate']:>8.3f}"
              f"{b['win_rate']:>8.3f}{dwin[-1]:>+8.3f}"
              f"{c['exp_bps']:>9.1f}{b['exp_bps']:>9.1f}{dexp[-1]:>+9.1f}")
    dwin, dexp = np.array(dwin), np.array(dexp)
    tw, pw = stats.ttest_1samp(dwin, 0.0)
    te, pe = stats.ttest_1samp(dexp, 0.0)
    print(f"\n  Δ胜率: 均值 {dwin.mean():+.4f} 中位 {np.median(dwin):+.4f} "
          f"正 {(dwin>0).sum()}/{len(dwin)} 二项p={stats.binomtest((dwin>0).sum(),len(dwin),0.5).pvalue:.4f} "
          f"t检验p={pw:.6f}")
    print(f"  Δ期望: 均值 {dexp.mean():+.2f}bps 中位 {np.median(dexp):+.2f} "
          f"正 {(dexp>0).sum()}/{len(dexp)} 二项p={stats.binomtest((dexp>0).sum(),len(dexp),0.5).pvalue:.4f} "
          f"t检验p={pe:.6f}")
    print(f"  最差季 Δ胜率 {dwin.min():+.4f}  最差季 Δ期望 {dexp.min():+.2f}bps")

    # ---------------- Q3: 高胜率的构成 ----------------
    print(f"\n===== Q3 高胜率是怎么来的({CHAMP} vs 基线,全期) =====")
    cd = allo[CHAMP]
    bd = allo["baseline"]
    for tag, d in (("基线", bd), (CHAMP, cd)):
        net = d["net"]
        w = net > 0
        q = np.percentile(net, [1, 5, 25, 50, 75, 95, 99]) * 1e4
        tail = net[net <= np.percentile(net, 5)].sum() / net.sum() if net.sum() != 0 else np.nan
        print(f"  {tag:<12} 胜率 {w.mean():.3f}  均盈 {net[w].mean()*1e4:>7.0f}bps  "
              f"均亏 {net[~w].mean()*1e4:>8.0f}bps  盈亏比 {abs(net[w].mean()/net[~w].mean()):.3f}")
        print(f"  {'':<12} 分位 1%={q[0]:.0f} 5%={q[1]:.0f} 25%={q[2]:.0f} "
              f"50%={q[3]:.0f} 75%={q[4]:.0f} 95%={q[5]:.0f} 99%={q[6]:.0f}")
    print("  → 若中位数被推到正值而 1%/5% 分位更深,就是'多数小赢、少数巨亏',"
          "这种胜率不能单独作为上线依据。")

    df.to_csv(f"{WORK}/frontier.csv", index=False)


if __name__ == "__main__":
    main()
