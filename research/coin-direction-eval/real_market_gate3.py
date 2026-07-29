"""Is the market-level lean anything more than lagged price? Plus a proper null.

Three additions over gate2:
  1. NAIVE comparator: cross-sectional mean ret_24 (and share of coins with
     ret_24<0) at the same hour. If a plain 24h market-return read does the same
     job, the 86-feature model adds nothing at the market level.
  2. CIRCULAR-SHIFT null: rotate the lean series against the fill timeline. This
     keeps the lean's autocorrelation and coverage exactly, and destroys only the
     true time alignment. With ~4 weeks of fills this has far more power than a
     7-day block bootstrap over ~5 blocks.
  3. 2-day block bootstrap as a secondary CI, since 7-day blocks leave ~5 blocks.
"""
from __future__ import annotations
import glob
import numpy as np
import pandas as pd

BASE = "/root/.claude/jobs/5cbb3cf4/tmp/cdeval"
RNG = np.random.default_rng(20260729)
T0 = pd.Timestamp("2026-06-25", tz="UTC")


def block_ci(t, v, n_boot=3000, block_days=2):
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


def naive_market():
    """Hourly cross-sectional 24h-return read, from the same cached universe."""
    acc = []
    for p in sorted(glob.glob(f"{BASE}/wf_cache/*.pkl")):
        d = pd.read_pickle(p)
        d.index = pd.to_datetime(d.index, utc=True)
        d = d[d.index >= T0]
        if "ret_24" not in d.columns or d.empty:
            continue
        acc.append(d[["ret_24"]].assign(sym=p.split("/")[-1][:-4]))
        del d
    a = pd.concat(acc)
    del acc
    g = a.groupby(a.index)
    out = pd.DataFrame({"m_ret24": g["ret_24"].mean(),
                        "dn24": (a.ret_24 < 0).groupby(a.index).mean(),
                        "nn": g.size()})
    return out[out.nn >= 10].sort_index()


def asof(f, mkr, cols):
    r = pd.merge_asof(f, mkr[["t"] + cols], left_on="entry", right_on="t",
                      direction="backward", tolerance=pd.Timedelta(hours=6))
    return r.drop(columns=["t"])


def main():
    d = pd.read_pickle(f"{BASE}/wf_scored/2026-07-01.pkl")
    d.index = pd.to_datetime(d.index, utc=True)
    d = d[np.isfinite(d.raw_up)]
    g = d.groupby(d.index)
    mk = pd.DataFrame({"lean": g["raw_up"].mean(),
                       "our_share": g.apply(lambda x: float((x.slope168 < 0).mean()),
                                            include_groups=False),
                       "n": g.size()})
    mk = mk[mk.n >= 10].sort_index()

    nv = naive_market()
    print(f"模型面板 {len(mk)}h | 朴素面板 {len(nv)}h ({nv.index.min()} ~ {nv.index.max()})")
    # correlation between model lean and the naive read, on shared hours
    j = mk.join(nv, how="inner")
    print(f"共同小时 {len(j)}: corr(lean, 市场均24h收益) = {j.lean.corr(j.m_ret24):+.3f}, "
          f"corr(lean, 下跌币占比) = {j.lean.corr(j.dn24):+.3f}")

    cuts = {"lean": float(mk.lean.median()), "our_share": float(mk.our_share.median()),
            "m_ret24": float(nv.m_ret24.median()), "dn24": float(nv.dn24.median())}
    print("阈值(各自面板中位数): " + " ".join(f"{k}={v:.5f}" for k, v in cuts.items()))

    f = pd.read_csv(f"{BASE}/real_fills.csv")
    f["entry"] = pd.to_datetime(f.entry_time, unit="ms", utc=True)
    f["pnl"] = f.realized_pnl.astype(float)
    f = f.sort_values("entry").reset_index(drop=True)

    mkr = mk.reset_index(names="t")
    nvr = nv.reset_index(names="t")
    f = asof(f, mkr, ["lean", "our_share"])
    f = asof(f, nvr, ["m_ret24", "dn24"])
    f = f[f.lean.notna() & f.m_ret24.notna()].copy()
    longs = f[f.side == "LONG"].copy().reset_index(drop=True)
    print(f"\n可评估 {len(f)} 笔 ({f.pnl.sum():+.2f}), 其中 LONG {len(longs)} 笔 "
          f"({longs.pnl.sum():+.2f})")

    # ---- separation on LONG, all four reads ----
    print("\n=== LONG 分离度 (看空组 vs 看多组, 每笔均值) ===")
    signals = {"模型 lean(低=看空)": (longs.lean <= cuts["lean"]),
               "我们 slope 占比(高=看空)": (longs.our_share >= cuts["our_share"]),
               "朴素 市场均24h收益(低=看空)": (longs.m_ret24 <= cuts["m_ret24"]),
               "朴素 下跌币占比(高=看空)": (longs.dn24 >= cuts["dn24"])}
    for lbl, bear in signals.items():
        b = bear.to_numpy()
        p1, p0 = longs.pnl[b], longs.pnl[~b]
        print(f"  {lbl:26s}: 看空 {b.sum():3d} 笔 均 {p1.mean():+.4f} | "
              f"看多 {(~b).sum():3d} 笔 均 {p0.mean():+.4f} | 差 {p0.mean()-p1.mean():+.4f}")

    # ---- saving + 2-day CI + circular-shift null ----
    print("\n=== 半仓压制 LONG: 节省 / 2日块CI / 环形位移零假设(1000次) ===")
    pnl = longs.pnl.to_numpy()
    t = longs.entry.dt.tz_localize(None).to_numpy("datetime64[ns]")
    hrs = ((longs.entry - longs.entry.min()) / pd.Timedelta(hours=1)).to_numpy()
    span = int(hrs.max()) + 1
    for lbl, bear in signals.items():
        b = bear.to_numpy()
        s, lo, hi = block_ci(t[b], 0.5 * (-pnl[b]))
        # circular shift: rotate the signal in time, keep coverage identical
        sig_by_h = np.zeros(span, dtype=bool)
        for h, bb in zip(hrs.astype(int), b):
            sig_by_h[h] = bb
        null = np.empty(1000)
        for i in range(1000):
            k = RNG.integers(1, span)
            rot = np.roll(sig_by_h, k)
            bs = rot[hrs.astype(int)]
            null[i] = 0.5 * (-pnl[bs]).sum()
        p = float((null >= s).mean())
        print(f"  {lbl:26s}: 节省 {s:+8.4f} CI2d [{lo:+8.4f},{hi:+8.4f}] "
              f"零假设均 {null.mean():+7.4f} p={p:.3f} "
              f"{'显著' if p < 0.05 else ''}")


if __name__ == "__main__":
    main()
