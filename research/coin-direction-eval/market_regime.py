"""Second-stage development: the signal is market-level, so model it that way.

Evidence this is built on (all from the 15-quarter honest walk-forward):
  - per-coin skill after removing the hourly cross-section is ~0 (CI crosses 0 in
    all three calibrations), and per-coin BA is 49.24% -> the per-coin gate is dead
  - but A2 (vendor calibration) top3 beats our veto by ~0.27%/trade out of sample
  - DOWN coverage is 0% or 100% in 59% of hours -> it behaves as a market switch

So: stop asking "will THIS coin fall" and ask "is the whole book's long side
unattractive right now". Aggregate the model's raw lean across the universe into
one number per hour and test it as a market gate, against our 168h slope gate.

All returns use the same frozen ATR exit sim + 10bps already baked into the panels.
"""
from __future__ import annotations
import os
import numpy as np
import pandas as pd

BASE = "/root/.claude/jobs/5cbb3cf4/tmp/cdeval"
SDIR = f"{BASE}/wf_scored"
RNG = np.random.default_rng(20260729)


def block_ci(t, v, n_boot=2000, block_days=14):
    if len(v) == 0:
        return (np.nan, np.nan, np.nan)
    t = np.asarray(t, dtype="datetime64[ns]")
    blk = (((t - t.min()) / np.timedelta64(1, "D")).astype("int64") // block_days)
    uniq = np.unique(blk)
    if len(uniq) < 3:
        return (float(np.mean(v)), np.nan, np.nan)
    by = {b: v[blk == b] for b in uniq}
    m = np.empty(n_boot)
    for i in range(n_boot):
        pick = RNG.choice(uniq, size=len(uniq), replace=True)
        m[i] = np.concatenate([by[b] for b in pick]).mean()
    return (float(np.mean(v)), *np.percentile(m, [2.5, 97.5]))


def pct(x):
    return "  n/a   " if not np.isfinite(x) else f"{x*100:+.4f}%"


def main():
    files = sorted(f for f in os.listdir(SDIR) if f.endswith(".pkl"))
    df = pd.concat([pd.read_pickle(f"{SDIR}/{f}") for f in files]).sort_index()
    df.index = pd.to_datetime(df.index, utc=True)
    df = df[np.isfinite(df.net_long) & np.isfinite(df.net_short)]
    df["dt64"] = df.index.tz_localize(None).to_numpy("datetime64[ns]")
    print(f"样本: {len(df)} 点, {df.symbol.nunique()} 币, {len(files)} 个滚动季度, "
          f"{df.index.min().date()}~{df.index.max().date()}\n")

    # ---- market-level aggregation, computed causally per hour ----
    g = df.groupby("dt64")
    mk = pd.DataFrame({
        "lean": g["raw_up"].mean(),          # model's average directional lean
        "lean_med": g["raw_up"].median(),
        "down_share": g.apply(lambda x: float((x.raw_up < 0.5).mean()), include_groups=False),
        "our_down_share": g.apply(lambda x: float((x.slope168 < 0).mean()), include_groups=False),
        "mL": g["net_long"].mean(),          # what an equal-weight long book earned
        "mS": g["net_short"].mean(),
        "n": g.size(),
    })
    mk = mk[mk.n >= 10]
    t = mk.index.to_numpy()
    print(f"小时级样本: {len(mk)} 小时\n")

    print("=== 市场择时能力: 聚合信号能否预测'等权多头账本'的收益 ===")
    for lbl, col, lo_is_bear in (("模型聚合 lean (均值)", "lean", True),
                                 ("模型聚合 lean (中位)", "lean_med", True),
                                 ("模型 DOWN 占比", "down_share", False),
                                 ("我们 slope168<0 占比", "our_down_share", False)):
        v = mk[col].to_numpy()
        # split at the median so both arms have equal size (no threshold tuning)
        cut = np.median(v)
        bear = (v <= cut) if lo_is_bear else (v >= cut)
        mL_bear, lo1, hi1 = block_ci(t[bear], mk.mL.to_numpy()[bear])
        mL_bull, lo2, hi2 = block_ci(t[~bear], mk.mL.to_numpy()[~bear])
        spread, slo, shi = block_ci(t, np.where(bear, -mk.mL.to_numpy(), mk.mL.to_numpy()))
        print(f"\n  {lbl}  (按中位数切,各占一半)")
        print(f"    判为'空头环境'时 多头账本 {pct(mL_bear)} CI [{pct(lo1)},{pct(hi1)}]")
        print(f"    判为'多头环境'时 多头账本 {pct(mL_bull)} CI [{pct(lo2)},{pct(hi2)}]")
        print(f"    差异(多头环境 - 空头环境)  {pct(mL_bull - mL_bear)}")
        print(f"    照此切换方向的每小时收益   {pct(spread)} CI [{pct(slo)},{pct(shi)}]")

    print("\n\n=== 直接对比: 三种'只做一边'的市场门禁 ===")
    base_all_long, blo, bhi = block_ci(t, mk.mL.to_numpy())
    print(f"  永远做多(不判断)        {pct(base_all_long)} CI [{pct(blo)},{pct(bhi)}]")
    for lbl, col, lo_is_bear in (("模型 lean 门禁", "lean", True),
                                 ("模型 DOWN占比 门禁", "down_share", False),
                                 ("我们 slope占比 门禁", "our_down_share", False)):
        v = mk[col].to_numpy()
        cut = np.median(v)
        bear = (v <= cut) if lo_is_bear else (v >= cut)
        # gate = skip the long book entirely in a bear read (return 0 that hour)
        gated = np.where(bear, 0.0, mk.mL.to_numpy())
        m, lo, hi = block_ci(t, gated)
        # flip = go short instead
        flip = np.where(bear, mk.mS.to_numpy(), mk.mL.to_numpy())
        m2, lo2, hi2 = block_ci(t, flip)
        print(f"  {lbl:<22} 空仓版 {pct(m)} CI [{pct(lo)},{pct(hi)}] | "
              f"翻空版 {pct(m2)} CI [{pct(lo2)},{pct(hi2)}]")

    # ---- per quarter, so we can see stability rather than one pooled number ----
    print("\n\n=== 逐季度稳定性 (翻空版) ===")
    mk["q"] = pd.PeriodIndex(mk.index, freq="Q")
    rows = []
    for q, sub in mk.groupby("q"):
        if len(sub) < 100:
            continue
        r = {"q": str(q), "hours": len(sub), "baseL": float(sub.mL.mean())}
        for lbl, col, lo_is_bear in (("lean", "lean", True),
                                     ("dshare", "down_share", False),
                                     ("ours", "our_down_share", False)):
            v = sub[col].to_numpy()
            bear = (v <= np.median(v)) if lo_is_bear else (v >= np.median(v))
            r[lbl] = float(np.where(bear, sub.mS.to_numpy(), sub.mL.to_numpy()).mean())
        rows.append(r)
    qd = pd.DataFrame(rows)
    for r in qd.itertuples():
        print(f"  {r.q}  h={r.hours:>4}  基线多头 {pct(r.baseL)} | "
              f"lean {pct(r.lean)}  DOWN占比 {pct(r.dshare)}  我们 {pct(r.ours)}")
    print()
    for c in ("lean", "dshare", "ours", "baseL"):
        print(f"  {c:<7} 为正 {int((qd[c]>0).sum())}/{len(qd)}  均 {pct(qd[c].mean())}  "
              f"中位 {pct(qd[c].median())}  最差 {pct(qd[c].min())}")
    qd.to_csv(f"{BASE}/market_regime.csv", index=False)


if __name__ == "__main__":
    main()
