"""Real-fill validation of the market-level lean, with as-of alignment.

The 2026Q3 walk-forward panel is sampled every 6h (TEST_STRIDE=6), so a strict
hour join only reaches 14% of fills. The lean is a slow, market-wide read, so we
align each fill to the most recent PAST panel hour within 6h. That is
information already available at fill time, so it is not lookahead.
"""
from __future__ import annotations
import numpy as np
import pandas as pd

BASE = "/root/.claude/jobs/5cbb3cf4/tmp/cdeval"
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
    d = pd.read_pickle(f"{BASE}/wf_scored/2026-07-01.pkl")
    d.index = pd.to_datetime(d.index, utc=True)
    d = d[np.isfinite(d.raw_up)]
    g = d.groupby(d.index)
    mk = pd.DataFrame({
        "lean": g["raw_up"].mean(),
        "our_share": g.apply(lambda x: float((x.slope168 < 0).mean()), include_groups=False),
        "n": g.size(),
    })
    mk = mk[mk.n >= 10].sort_index()
    lean_cut = float(mk.lean.median())
    our_cut = float(mk.our_share.median())
    print(f"面板 {len(mk)} 小时 {mk.index.min()} ~ {mk.index.max()}")
    print(f"阈值取面板中位数: lean<={lean_cut:.5f} our_share>={our_cut:.4f}")

    f = pd.read_csv(f"{BASE}/real_fills.csv")
    f["entry"] = pd.to_datetime(f.entry_time, unit="ms", utc=True)
    f["pnl"] = f.realized_pnl.astype(float)
    f = f.sort_values("entry")
    print(f"\n真实成交 {len(f)} 笔 净盈亏 {f.pnl.sum():+.2f} USDT "
          f"({f.entry.min().date()} ~ {f.entry.max().date()})")

    mkr = mk.reset_index().rename(columns={"index": "t"})
    mkr.columns = ["t", "lean", "our_share", "n"]
    f = pd.merge_asof(f, mkr[["t", "lean", "our_share"]], left_on="entry",
                      right_on="t", direction="backward",
                      tolerance=pd.Timedelta("6h"))
    ok = f.lean.notna()
    print(f"as-of 对齐(向后<=6h): {int(ok.sum())}/{len(f)} 笔, 盈亏 {f.loc[ok,'pnl'].sum():+.2f}")
    f = f[ok].copy()
    print(f"  LONG {int((f.side=='LONG').sum())} 笔 {f.loc[f.side=='LONG','pnl'].sum():+.2f} | "
          f"SHORT {int((f.side=='SHORT').sum())} 笔 {f.loc[f.side=='SHORT','pnl'].sum():+.2f}")

    # ---- 1. does the lean separate LONG outcomes at all? ----
    print("\n=== 1. 看空读数下 LONG/SHORT 的实际表现 ===")
    for lbl, col, cut, bearcmp in (("模型 lean", "lean", lean_cut, "le"),
                                   ("我们 slope 占比", "our_share", our_cut, "ge")):
        bear = f[col] <= cut if bearcmp == "le" else f[col] >= cut
        for side in ("LONG", "SHORT"):
            s = f[f.side == side]
            b = bear[f.side == side]
            n1, n0 = int(b.sum()), int((~b).sum())
            p1 = s.pnl[b.to_numpy()].sum() if n1 else 0.0
            p0 = s.pnl[(~b).to_numpy()].sum() if n0 else 0.0
            print(f"  {lbl:14s} {side}: 看空 {n1:3d} 笔 {p1:+8.2f} (均 {p1/max(n1,1):+.4f}) | "
                  f"看多 {n0:3d} 笔 {p0:+8.2f} (均 {p0/max(n0,1):+.4f})")

    # ---- 2. counterfactual saving on LONG ----
    longs = f[f.side == "LONG"].copy()
    print(f"\n=== 2. 只压制 LONG 的反事实节省 (LONG {len(longs)} 笔 {longs.pnl.sum():+.2f}) ===")
    res = {}
    for lbl, bear in (("模型 lean 看空", longs.lean <= lean_cut),
                      ("我们 slope 看空", longs.our_share >= our_cut)):
        hit = longs[bear.to_numpy()]
        t = hit.entry.dt.tz_localize(None).to_numpy("datetime64[ns]")
        print(f"\n  {lbl}: 命中 {len(hit)}/{len(longs)} ({len(hit)/max(len(longs),1)*100:.0f}%), "
              f"实际盈亏 {hit.pnl.sum():+.2f}")
        for m in (0.5, 0.0):
            s, lo, hi = block_ci(t, (1.0 - m) * (-hit.pnl.to_numpy()))
            print(f"    倍率 {m:.1f}: 节省 {s:+.4f} CI [{lo:+.4f},{hi:+.4f}] "
                  f"-> {'通过' if np.isfinite(lo) and lo>0 else '不通过'}")
            if m == 0.5:
                res[lbl] = (s, len(hit))

    # ---- 3. flip: use the lean to pick side, both directions ----
    print("\n=== 3. 双向: 看空压 LONG + 看多压 SHORT ===")
    for lbl, bl, bs in (("模型 lean", f.lean <= lean_cut, f.lean > lean_cut),
                        ("我们 slope", f.our_share >= our_cut, f.our_share < our_cut)):
        mis = ((f.side == "LONG") & bl) | ((f.side == "SHORT") & bs)
        hit = f[mis.to_numpy()]
        t = hit.entry.dt.tz_localize(None).to_numpy("datetime64[ns]")
        s, lo, hi = block_ci(t, 0.5 * (-hit.pnl.to_numpy()))
        print(f"  {lbl}: 逆向 {len(hit)}/{len(f)} 笔 实际 {hit.pnl.sum():+.2f}, "
              f"半仓节省 {s:+.4f} CI [{lo:+.4f},{hi:+.4f}] "
              f"-> {'通过' if np.isfinite(lo) and lo>0 else '不通过'}")

    # ---- 4. matched-coverage random control ----
    print("\n=== 4. 同覆盖率随机对照 (500 次, 倍率0.5) ===")
    for lbl, bear in (("模型 lean 看空", longs.lean <= lean_cut),
                      ("我们 slope 看空", longs.our_share >= our_cut)):
        k = int(bear.sum())
        if not k:
            continue
        p = -longs.pnl.to_numpy()
        sims = np.array([0.5 * p[RNG.choice(len(p), size=k, replace=False)].sum()
                         for _ in range(500)])
        a = res[lbl][0]
        print(f"  {lbl}: 实际 {a:+.4f} vs 随机 均{sims.mean():+.4f} "
              f"[p5 {np.percentile(sims,5):+.4f}, p95 {np.percentile(sims,95):+.4f}] "
              f"-> 优于 {float((sims<a).mean())*100:.0f}% 随机")


if __name__ == "__main__":
    main()
