"""The symmetric rule on OUR real fills, both sides, at bps.

Panel evidence says the lean is directional and roughly equally informative on
both legs, so the symmetric rule (damp LONG when bearish, damp SHORT when
bullish) should beat the LONG-only M5. Test that on real closed fills.

Discipline carried over from the earlier evaluation:
  - normalise to bps = pnl / (qty * entry_price) * 10000. USDT sums are
    dominated by sizing noise (notional spans 4.62 .. 555.4).
  - threshold from history only (pre-2026Q3 median), lag 1h.
  - circular-shift null: preserves autocorrelation, coverage and the total
    amount of damping, destroys only the timing.
  - power warning: ~4 weeks of fills = ~4 weekly blocks. This is a sign-and-
    magnitude check, not the primary evidence.
"""
from __future__ import annotations
import glob
import numpy as np
import pandas as pd
from scipy import stats

BASE = "/root/.claude/jobs/5cbb3cf4/tmp/cdeval"
RNG = np.random.default_rng(20260729)
MULT, LAG = 0.5, 1


def main():
    d = pd.read_pickle(f"{BASE}/wf_scored/2026-07-01.pkl")
    d.index = pd.to_datetime(d.index, utc=True)
    d = d[np.isfinite(d.raw_up)]
    g = d.groupby(d.index)
    mk = pd.DataFrame({"lean": g["raw_up"].mean(), "n": g.size()})
    mk = mk[mk.n >= 10].sort_index()
    mk["lean_pub"] = mk.lean.shift(LAG)
    mk = mk.dropna()

    hist = []
    for p in sorted(glob.glob(f"{BASE}/wf_scored/*.pkl")):
        if p.endswith("2026-07-01.pkl"):
            continue
        x = pd.read_pickle(p)
        x.index = pd.to_datetime(x.index, utc=True)
        x = x[np.isfinite(x.raw_up)]
        hist.append(x.groupby(x.index)["raw_up"].mean())
        del x
    cut = float(pd.concat(hist).median())
    print(f"阈值 = 2026Q3 之前全部季度中位数 {cut:.5f}")

    f = pd.read_csv(f"{BASE}/real_fills.csv")
    f["entry"] = pd.to_datetime(f.entry_time, unit="ms", utc=True)
    f["notional"] = f.quantity.astype(float) * f.entry_price.astype(float)
    f = f[f.notional > 0].copy()
    f["bps"] = f.realized_pnl.astype(float) / f.notional * 10_000.0
    f["pnl"] = f.realized_pnl.astype(float)
    f = f.sort_values("entry").reset_index(drop=True)
    f = pd.merge_asof(f, mk.reset_index(names="t")[["t", "lean_pub"]],
                      left_on="entry", right_on="t", direction="backward",
                      tolerance=pd.Timedelta(hours=6)).drop(columns=["t"])
    f = f[f.lean_pub.notna()].copy()
    f["bear"] = f.lean_pub <= cut
    print(f"可评估 {len(f)} 笔 (LONG {(f.side=='LONG').sum()} / "
          f"SHORT {(f.side=='SHORT').sum()}), 看空时点占 {f.bear.mean()*100:.0f}%\n")

    print("=== 分离度: 读数与实际结果的方向是否对上 ===")
    for sd in ("LONG", "SHORT"):
        s = f[f.side == sd]
        a = s.bps.to_numpy()[s.bear.to_numpy()]
        b = s.bps.to_numpy()[~s.bear.to_numpy()]
        if len(a) < 5 or len(b) < 5:
            print(f"  {sd}: 样本不足")
            continue
        # for LONG we want bear group worse; for SHORT we want bull group worse
        want = "看空组更差" if sd == "LONG" else "看多组更差"
        u = stats.mannwhitneyu(b, a, alternative="greater" if sd == "LONG" else "less")
        print(f"  {sd:5s} 看空组 {a.mean():+7.2f}bps (n={len(a)}) | "
              f"看多组 {b.mean():+7.2f}bps (n={len(b)}) | "
              f"分离 {(b.mean()-a.mean()):+7.2f} | 期望 {want} | MWU p={u.pvalue:.4f}")

    bps = f.bps.to_numpy()
    pnl = f.pnl.to_numpy()
    isL = (f.side == "LONG").to_numpy()
    bear = f.bear.to_numpy()

    def weights(bear_v):
        w = np.ones(len(f))
        w[isL & bear_v] = MULT          # damp LONG when bearish
        w[~isL & ~bear_v] = MULT        # damp SHORT when bullish
        return w

    def w_longonly(bear_v):
        w = np.ones(len(f))
        w[isL & bear_v] = MULT
        return w

    print("\n=== 规则对比 (全簿 915 笔) ===")
    print(f"  基线不动: {bps.mean():+.3f} bps/笔, 合计 {pnl.sum():+.2f} USDT")
    for nm, fn in (("M5 只减 LONG", w_longonly), ("对称 两侧都减不利侧", weights)):
        w = fn(bear)
        eff = (w * bps - bps).mean()
        eff_u = (w * pnl - pnl).sum()
        touched = int((w != 1).sum())
        print(f"  {nm:20s} 改善 {eff:+7.3f} bps/笔, 现金 {eff_u:+8.2f} USDT, "
              f"影响 {touched:3d} 笔")

    print("\n=== 环形位移零假设 (5000 次, 保留自相关/覆盖/减仓总量) ===")
    hrs = ((f.entry - f.entry.min()) / pd.Timedelta(hours=1)).to_numpy().astype(int)
    span = hrs.max() + 1
    sig = np.zeros(span, dtype=bool)
    sig[hrs[bear]] = True
    for nm, fn in (("M5 只减 LONG", w_longonly), ("对称", weights)):
        real = (fn(bear) * bps - bps).mean()
        null = np.empty(5000)
        for i in range(5000):
            rot = np.roll(sig, RNG.integers(1, span))[hrs]
            null[i] = (fn(rot) * bps - bps).mean()
        p = float((null >= real).mean())
        print(f"  {nm:14s} 真实 {real:+.3f} | 零假设均 {null.mean():+.3f} "
              f"p95 {np.percentile(null,95):+.3f} | p={p:.4f} "
              f"| 净技巧 {real-null.mean():+.3f} {'显著' if p<0.05 else '(功效不足)'}")

    print("\n=== 逐周 (样本极小, 只看方向一致性) ===")
    f["wk"] = f.entry.dt.to_period("W")
    for wk, gw in f.groupby("wk", observed=True):
        if len(gw) < 10:
            continue
        m = gw.index.to_numpy()
        w = weights(bear)[np.searchsorted(f.index.to_numpy(), m)]
        e = (w * gw.bps.to_numpy() - gw.bps.to_numpy()).mean()
        print(f"  {str(wk)[:10]}: {len(gw):3d} 笔 原始 {gw.bps.mean():+7.2f}bps "
              f"对称改善 {e:+6.3f}bps")


if __name__ == "__main__":
    main()
