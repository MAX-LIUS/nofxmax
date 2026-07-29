"""期望持平时,唯一还值得问的问题:风险调整后是不是真的更好?

前一步已经定死两件事:
  Δ胜率 +0.2913,18/18 季为正,最差季 +0.2358(极稳);
  Δ净期望 −9.24bps,9/18 季为正,t 检验 p=0.59(就是噪声,略偏负)。
所以"更赚钱"这个说法已经被否掉了。剩下的合法问题是:
  同样的期望,波动、回撤、夏普是不是显著改善?若是,它的价值在**风险形状**而非收益。

按时间顺序把每笔交易接成一条资金曲线(等权、不复利、不加杠杆),比较:
  波动率、下行波动、夏普、最大回撤、回撤持续、Calmar、单笔最差。
并用 bootstrap 给夏普差与最大回撤差一个区间 —— 单点数值不足以下结论。
"""
from __future__ import annotations
import os
import numpy as np
import pandas as pd

import pmgate as P
import fastsim as F
import tournament as T

CHAMP = "tr0.25a1.0"
ALT = ["sl4.0", "sl3.0", "c_sl3.0_g0.5_tp1.0f0.5", "tp1.0f0.5", "tr0.5a1.0"]
RNG = np.random.default_rng(31337)


def curve_stats(net: np.ndarray) -> dict:
    eq = np.cumsum(net)
    peak = np.maximum.accumulate(eq)
    dd = eq - peak
    mdd = float(dd.min())
    # drawdown duration in trades
    under = dd < -1e-12
    longest, cur = 0, 0
    for u in under:
        cur = cur + 1 if u else 0
        longest = max(longest, cur)
    sd = float(net.std(ddof=1))
    down = net[net < 0]
    dsd = float(down.std(ddof=1)) if len(down) > 1 else np.nan
    mean = float(net.mean())
    return {
        "total_pct": float(eq[-1] * 100), "mean_bps": mean * 1e4,
        "sd_bps": sd * 1e4, "dsd_bps": dsd * 1e4,
        "sharpe": mean / sd if sd > 0 else np.nan,
        "sortino": mean / dsd if dsd and dsd > 0 else np.nan,
        "mdd_pct": mdd * 100, "mdd_trades": longest,
        "calmar": (eq[-1] / -mdd) if mdd < 0 else np.nan,
        "worst_bps": float(net.min() * 1e4), "best_bps": float(net.max() * 1e4),
    }


def main():
    syms = sorted(f[:-4] for f in os.listdir(P.CACHE) if f.endswith(".pkl"))
    lib = T.variant_library()
    paths = T.load_paths(syms)
    edges = pd.date_range("2022-01-01", "2026-07-01", freq="QS", tz="UTC")

    # 需要按时间排序才能接资金曲线,所以带上入场时间
    want = {"baseline": {}, CHAMP: lib[CHAMP]}
    for a in ALT:
        if a in lib:
            want[a] = lib[a]

    nets, tss = {k: [] for k in want}, []
    for pp in paths:
        m = (pp["ts"] >= edges[0].value) & (pp["ts"] < edges[-1].value)
        if not m.any():
            continue
        sub = T._slice(pp, m)
        tss.append(pp["ts"][m])
        for name, params in want.items():
            d = F.simulate_variant(sub, params)
            nets[name].append(d["net"])
    ts = np.concatenate(tss)
    order = np.argsort(ts, kind="stable")
    series = {k: np.concatenate(v)[order] for k, v in nets.items()}

    print(f"按入场时间排序的等权资金曲线, n={len(ts)} 笔, "
          f"{pd.Timestamp(ts.min()).date()}~{pd.Timestamp(ts.max()).date()}\n")
    hdr = (f"{'变种':<24}{'总收益%':>9}{'均值bps':>9}{'波动bps':>9}{'夏普':>8}"
           f"{'索提诺':>8}{'最大回撤%':>10}{'回撤长':>7}{'Calmar':>8}{'最差bps':>9}")
    print(hdr)
    st = {}
    for name in want:
        s = curve_stats(series[name])
        st[name] = s
        print(f"{name:<24}{s['total_pct']:>9.1f}{s['mean_bps']:>9.2f}{s['sd_bps']:>9.0f}"
              f"{s['sharpe']:>8.4f}{s['sortino']:>8.4f}{s['mdd_pct']:>10.1f}"
              f"{s['mdd_trades']:>7d}{s['calmar']:>8.2f}{s['worst_bps']:>9.0f}")

    b = st["baseline"]
    c = st[CHAMP]
    print(f"\n===== {CHAMP} 相对基线 =====")
    print(f"  波动 {b['sd_bps']:.0f} → {c['sd_bps']:.0f} bps "
          f"({(c['sd_bps']/b['sd_bps']-1)*100:+.1f}%)")
    print(f"  夏普 {b['sharpe']:+.4f} → {c['sharpe']:+.4f}")
    print(f"  最大回撤 {b['mdd_pct']:.1f}% → {c['mdd_pct']:.1f}%")
    print(f"  单笔最差 {b['worst_bps']:.0f} → {c['worst_bps']:.0f} bps")

    # bootstrap:成对重采样,给夏普差与最大回撤差一个区间
    nb = 2000
    n = len(ts)
    ds, dm = np.empty(nb), np.empty(nb)
    bn, cn = series["baseline"], series[CHAMP]
    for i in range(nb):
        ix = RNG.integers(0, n, n)
        sb, sc = bn[ix], cn[ix]
        sdb, sdc = sb.std(ddof=1), sc.std(ddof=1)
        ds[i] = (sc.mean() / sdc if sdc > 0 else 0) - (sb.mean() / sdb if sdb > 0 else 0)
        eb, ec = np.cumsum(sb), np.cumsum(sc)
        dm[i] = ((ec - np.maximum.accumulate(ec)).min()
                 - (eb - np.maximum.accumulate(eb)).min()) * 100
    print(f"\n  bootstrap(成对重采样 {nb} 次):")
    print(f"    Δ夏普 均值 {ds.mean():+.4f}  90%区间 [{np.percentile(ds,5):+.4f}, "
          f"{np.percentile(ds,95):+.4f}]  >0 占比 {(ds>0).mean():.3f}")
    print(f"    Δ最大回撤 均值 {dm.mean():+.2f}pp  90%区间 [{np.percentile(dm,5):+.2f}, "
          f"{np.percentile(dm,95):+.2f}]  改善(>0)占比 {(dm>0).mean():.3f}")
    print("    (Δ最大回撤 >0 表示回撤更浅=更好,因为回撤是负数)")

    print("\n===== 结论口径 =====")
    if c["sharpe"] > b["sharpe"] and (ds > 0).mean() > 0.95:
        print("  期望持平但风险调整后显著更好 → 价值在风险形状,可作为'降波动'工具讨论。")
    elif abs(c["sharpe"] - b["sharpe"]) < 0.01 and (ds > 0).mean() < 0.95:
        print("  期望持平、夏普也没有显著改善 → 它只是把收益分布重新切了一刀,"
              "换来账面上更好看的胜率,**没有创造任何东西**。")
    else:
        print("  介于两者之间,按上面的区间自行判断,不要只看单点。")


if __name__ == "__main__":
    main()
