"""M5 on the real fills, at bps, with a circular-shift null.

M5 = when the market lean reads bearish, size LONG intents at 0.5x. Never
reverse. Applied to real closed fills 2026-06-30~2026-07-29 using the 2026Q3
walk-forward model (trained through 2026-06-28 minus 72h purge), lag 1h.

Power warning up front: ~4 weeks of fills gives ~4 weekly blocks, so this test
can only reject an effect that is enormous. It is a sanity check on sign and
magnitude, not the primary evidence. The primary evidence is the 13-quarter
panel.
"""
from __future__ import annotations
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
    # lag: publish the lean one panel step later, mimicking compute+order delay
    mk["lean_pub"] = mk.lean.shift(LAG)
    mk = mk.dropna()

    # threshold from history only: use the pre-2026Q3 panel median
    hist = []
    import glob
    for p in sorted(glob.glob(f"{BASE}/wf_scored/*.pkl")):
        if p.endswith("2026-07-01.pkl"):
            continue
        x = pd.read_pickle(p)
        x.index = pd.to_datetime(x.index, utc=True)
        x = x[np.isfinite(x.raw_up)]
        hist.append(x.groupby(x.index)["raw_up"].mean())
        del x
    hist = pd.concat(hist)
    cut = float(hist.median())
    print(f"阈值来自 2026Q3 之前的全部季度中位数 = {cut:.5f} (共 {len(hist)} 小时)")
    print(f"2026Q3 面板 {len(mk)} 小时, 其中读数<=阈值(看空) "
          f"{int((mk.lean_pub<=cut).sum())} 小时\n")

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
    L = f[f.side == "LONG"].reset_index(drop=True)
    print(f"可评估 {len(f)} 笔 (LONG {len(L)}), LONG 平均 {L.bps.mean():+.2f}bps "
          f"/ 合计 {L.pnl.sum():+.2f} USDT")

    bear = (L.lean_pub <= cut).to_numpy()
    print(f"看空命中 {bear.sum()}/{len(L)} 笔 ({bear.sum()/len(L)*100:.0f}%)")
    x, y = L.bps.to_numpy()[bear], L.bps.to_numpy()[~bear]
    print(f"  看空组 {x.mean():+.2f}bps (中位 {np.median(x):+.2f}) | "
          f"看多组 {y.mean():+.2f}bps (中位 {np.median(y):+.2f})")
    u = stats.mannwhitneyu(y, x, alternative="greater")
    print(f"  MWU 单边 p = {u.pvalue:.4f} {'显著' if u.pvalue<0.05 else '(样本不足,不作结论)'}")

    bps = L.bps.to_numpy()
    eff = (np.where(bear, MULT * bps, bps) - bps).mean()
    pnl = L.pnl.to_numpy()
    eff_usdt = (np.where(bear, MULT * pnl, pnl) - pnl).sum()
    print(f"\nM5 效果: 全 LONG 簿 {eff:+.3f} bps/笔, 现金口径 {eff_usdt:+.3f} USDT")

    hrs = ((L.entry - L.entry.min()) / pd.Timedelta(hours=1)).to_numpy().astype(int)
    span = hrs.max() + 1
    sig = np.zeros(span, dtype=bool)
    sig[hrs[bear]] = True
    null = np.empty(5000)
    for i in range(5000):
        rot = np.roll(sig, RNG.integers(1, span))[hrs]
        null[i] = (np.where(rot, MULT * bps, bps) - bps).mean()
    p = float((null >= eff).mean())
    print(f"环形位移零假设(5000次): 均 {null.mean():+.3f} bps, "
          f"p = {p:.4f} {'显著' if p<0.05 else ''}")
    print(f"  零假设分位: p50 {np.percentile(null,50):+.3f} "
          f"p95 {np.percentile(null,95):+.3f}")

    print("\n=== 逐周明细 (样本极小, 仅供观察方向一致性) ===")
    L["wk"] = L.entry.dt.to_period("W")
    for w, gw in L.groupby("wk", observed=True):
        b = (gw.lean_pub <= cut).to_numpy()
        if len(gw) < 5:
            continue
        e = (np.where(b, MULT * gw.bps.to_numpy(), gw.bps.to_numpy())
             - gw.bps.to_numpy()).mean()
        print(f"  {str(w)[:10]}: {len(gw):3d} 笔, 看空 {b.sum():3d}, "
              f"原始 {gw.bps.mean():+7.2f}bps, M5 改善 {e:+6.3f}bps")


if __name__ == "__main__":
    main()
