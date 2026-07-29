"""Decisive deployment test: does the MARKET-LEVEL lean help our REAL fills?

Uses the 2026Q3 walk-forward model, which was trained on data ending
2026-06-28 minus a 72h purge, so every real fill here (entry >= 2026-06-28) is
genuinely out of sample for it. No frozen vendor artifact involved.

Gate under test: at the fill's entry hour, take the cross-sectional mean of the
model's raw directional lean over the whole universe. If that reads bearish,
damp LONG intents by a multiplier. Saving = (1 - m) * (-realized_pnl), i.e. the
PnL we would NOT have taken. Positive saving = the gate helped.

Compared against our 168h slope gate applied the same way, and against a
random gate of matched coverage (the null: any gate that cuts trades in a
losing period looks good, so matched-coverage random is the honest control).
"""
from __future__ import annotations
import os
import numpy as np
import pandas as pd

BASE = "/root/.claude/jobs/5cbb3cf4/tmp/cdeval"
SDIR = f"{BASE}/wf_scored"
RNG = np.random.default_rng(20260729)


def block_ci(t, v, n_boot=3000, block_days=7):
    if len(v) == 0:
        return (np.nan, np.nan, np.nan)
    t = np.asarray(t, dtype="datetime64[ns]")
    blk = (((t - t.min()) / np.timedelta64(1, "D")).astype("int64") // block_days)
    uniq = np.unique(blk)
    if len(uniq) < 3:
        return (float(np.sum(v)), np.nan, np.nan)
    by = {b: v[blk == b] for b in uniq}
    s = np.empty(n_boot)
    for i in range(n_boot):
        pick = RNG.choice(uniq, size=len(uniq), replace=True)
        s[i] = np.concatenate([by[b] for b in pick]).sum()
    return (float(np.sum(v)), *np.percentile(s, [2.5, 97.5]))


def main():
    # ---- hourly market lean from the honest 2026Q3 walk-forward panel ----
    p = f"{SDIR}/2026-07-01.pkl"
    d = pd.read_pickle(p)
    d.index = pd.to_datetime(d.index, utc=True)
    d = d[np.isfinite(d.raw_up)]
    g = d.groupby(d.index)
    mk = pd.DataFrame({
        "lean": g["raw_up"].mean(),
        "our_share": g.apply(lambda x: float((x.slope168 < 0).mean()), include_groups=False),
        "n": g.size(),
    })
    mk = mk[mk.n >= 10]
    print(f"模型小时级覆盖: {len(mk)} 小时  {mk.index.min()} ~ {mk.index.max()}")

    # thresholds fixed at the panel median (no tuning on the fills)
    lean_cut = float(mk.lean.median())
    our_cut = float(mk.our_share.median())
    print(f"阈值(取自模型面板中位数,不在成交上调参): lean<={lean_cut:.5f}, our_share>={our_cut:.4f}\n")

    # ---- real fills ----
    f = pd.read_csv(f"{BASE}/real_fills.csv")
    f["entry"] = pd.to_datetime(f.entry_time, unit="ms", utc=True)
    f["hour"] = f.entry.dt.floor("h")
    f["pnl"] = f.realized_pnl.astype(float)
    print(f"真实成交: {len(f)} 笔, 净盈亏 {f.pnl.sum():+.2f} USDT")
    print(f"  LONG {int((f.side=='LONG').sum())} 笔 {f.loc[f.side=='LONG','pnl'].sum():+.2f} | "
          f"SHORT {int((f.side=='SHORT').sum())} 笔 {f.loc[f.side=='SHORT','pnl'].sum():+.2f}")

    f = f.join(mk[["lean", "our_share"]], on="hour")
    cov = f.lean.notna()
    print(f"  能对齐模型小时的: {int(cov.sum())}/{len(f)} 笔 "
          f"(盈亏 {f.loc[cov,'pnl'].sum():+.2f})\n")
    f = f[cov].copy()

    longs = f[f.side == "LONG"].copy()
    print(f"LONG 可评估 {len(longs)} 笔, 盈亏 {longs.pnl.sum():+.2f} USDT\n")

    print("=== 门禁反事实节省 (只作用于 LONG) ===")
    results = {}
    for lbl, bear in (("模型市场 lean 看空", longs.lean <= lean_cut),
                      ("我们 slope 占比看空", longs.our_share >= our_cut)):
        hit = longs[bear]
        t = hit.entry.dt.tz_localize(None).to_numpy("datetime64[ns]")
        print(f"\n  {lbl}: 命中 {len(hit)}/{len(longs)} 笔 "
              f"(覆盖 {len(hit)/max(len(longs),1)*100:.1f}%), 这批实际盈亏 {hit.pnl.sum():+.2f}")
        for m in (0.5, 0.7, 0.0):
            sav = (1.0 - m) * (-hit.pnl.to_numpy())
            s, lo, hi = block_ci(t, sav)
            ok = "通过" if np.isfinite(lo) and lo > 0 else "不通过"
            print(f"    倍率 {m:.1f}: 节省 {s:+.4f} USDT  7日块95%CI "
                  f"[{lo:+.4f},{hi:+.4f}]  -> {ok}")
            if m == 0.7:
                results[lbl] = (s, lo, hi, len(hit))

    # ---- matched-coverage random control ----
    print("\n=== 对照: 同覆盖率随机门禁 (200 次) ===")
    for lbl, bear in (("模型市场 lean 看空", longs.lean <= lean_cut),
                      ("我们 slope 占比看空", longs.our_share >= our_cut)):
        k = int(bear.sum())
        if k == 0:
            continue
        sims = []
        for _ in range(200):
            idx = RNG.choice(len(longs), size=k, replace=False)
            sims.append((0.3 * (-longs.pnl.to_numpy()[idx])).sum())
        sims = np.array(sims)
        actual = results[lbl][0]
        pctl = float((sims < actual).mean())
        print(f"  {lbl}: 实际节省 {actual:+.4f} vs 随机均值 {sims.mean():+.4f} "
              f"(随机 p5={np.percentile(sims,5):+.4f} p95={np.percentile(sims,95):+.4f})  "
              f"实际优于 {pctl*100:.0f}% 的随机门禁")

    # ---- would it have helped SHORT too? (should not damp shorts) ----
    shorts = f[f.side == "SHORT"]
    if len(shorts):
        bear = shorts.lean <= lean_cut
        print(f"\n=== 副作用检查: 同一看空读数下的 SHORT ===")
        print(f"  看空时 SHORT {int(bear.sum())} 笔 盈亏 {shorts[bear].pnl.sum():+.2f} | "
              f"看多时 SHORT {int((~bear).sum())} 笔 盈亏 {shorts[~bear].pnl.sum():+.2f}")
        print("  ^ 若看空时 SHORT 明显更赚, 说明这个读数确实抓到了下行环境")


if __name__ == "__main__":
    main()
