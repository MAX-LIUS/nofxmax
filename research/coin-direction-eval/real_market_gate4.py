"""Same test, normalized to return-per-notional so position sizing stops
dominating the variance.

USDT PnL mixes a 4016-dollar XAUUSDT fill with a 0.41-dollar WLDUSDT fill, so a
USDT-sum test measures sizing noise, not signal. bps = pnl / (qty*entry_price).
That is the quantity a gate actually controls: whether to take the trade.
"""
from __future__ import annotations
import glob
import numpy as np
import pandas as pd
from scipy import stats

BASE = "/root/.claude/jobs/5cbb3cf4/tmp/cdeval"
RNG = np.random.default_rng(20260729)
T0 = pd.Timestamp("2026-06-25", tz="UTC")


def block_ci(t, v, n_boot=4000, block_days=2):
    if len(v) == 0:
        return (np.nan, np.nan, np.nan)
    t = np.asarray(t, dtype="datetime64[ns]")
    blk = (((t - t.min()) / np.timedelta64(1, "D")).astype("int64") // block_days)
    uniq = np.unique(blk)
    if len(uniq) < 3:
        return (float(np.mean(v)), np.nan, np.nan)
    by = {b: v[blk == b] for b in uniq}
    s = np.empty(n_boot)
    for i in range(n_boot):
        pick = RNG.choice(uniq, size=len(uniq), replace=True)
        s[i] = np.concatenate([by[b] for b in pick]).mean()
    return (float(np.mean(v)), *np.percentile(s, [2.5, 97.5]))


def naive_market():
    acc = []
    for p in sorted(glob.glob(f"{BASE}/wf_cache/*.pkl")):
        d = pd.read_pickle(p)
        d.index = pd.to_datetime(d.index, utc=True)
        d = d[d.index >= T0]
        if "ret_24" not in d.columns or d.empty:
            continue
        acc.append(d[["ret_24"]])
        del d
    a = pd.concat(acc)
    del acc
    g = a.groupby(a.index)
    out = pd.DataFrame({"m_ret24": g["ret_24"].mean(),
                        "dn24": (a.ret_24 < 0).groupby(a.index).mean(),
                        "nn": g.size()})
    return out[out.nn >= 10].sort_index()


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
    cuts = {"lean": float(mk.lean.median()), "our_share": float(mk.our_share.median()),
            "m_ret24": float(nv.m_ret24.median()), "dn24": float(nv.dn24.median())}

    f = pd.read_csv(f"{BASE}/real_fills.csv")
    f["entry"] = pd.to_datetime(f.entry_time, unit="ms", utc=True)
    f["notional"] = f.quantity.astype(float) * f.entry_price.astype(float)
    f = f[f.notional > 0].copy()
    f["bps"] = f.realized_pnl.astype(float) / f.notional * 10_000.0
    f["pnl"] = f.realized_pnl.astype(float)
    f = f.sort_values("entry").reset_index(drop=True)
    print(f"成交 {len(f)} 笔 | 名义中位数 {f.notional.median():.1f} USDT "
          f"(min {f.notional.min():.2f} max {f.notional.max():.1f})")
    print(f"整体 bps 均值 {f.bps.mean():+.2f} 中位 {f.bps.median():+.2f}")

    for src, cols in ((mk, ["lean", "our_share"]), (nv, ["m_ret24", "dn24"])):
        f = pd.merge_asof(f, src.reset_index(names="t")[["t"] + cols], left_on="entry",
                          right_on="t", direction="backward",
                          tolerance=pd.Timedelta(hours=6)).drop(columns=["t"])
    f = f[f.lean.notna() & f.m_ret24.notna()].copy()
    L = f[f.side == "LONG"].reset_index(drop=True)
    S = f[f.side == "SHORT"].reset_index(drop=True)
    print(f"可评估 {len(f)} 笔: LONG {len(L)} (bps均 {L.bps.mean():+.2f}) "
          f"SHORT {len(S)} (bps均 {S.bps.mean():+.2f})\n")

    sigs = {"模型 lean": ("lean", "le"), "我们 slope 占比": ("our_share", "ge"),
            "朴素 市场均24h": ("m_ret24", "le"), "朴素 下跌币占比": ("dn24", "ge")}

    print("=== A. LONG 收益率分离 (bps/笔) + Mann-Whitney ===")
    for lbl, (col, cmp_) in sigs.items():
        b = (L[col] <= cuts[col]).to_numpy() if cmp_ == "le" else (L[col] >= cuts[col]).to_numpy()
        x, y = L.bps[b].to_numpy(), L.bps[~b].to_numpy()
        if len(x) < 5 or len(y) < 5:
            continue
        u = stats.mannwhitneyu(y, x, alternative="greater")
        print(f"  {lbl:14s}: 看空 {len(x):3d} 笔 {x.mean():+7.2f}bps | "
              f"看多 {len(y):3d} 笔 {y.mean():+7.2f}bps | 差 {y.mean()-x.mean():+7.2f} "
              f"| MWU p={u.pvalue:.4f} {'显著' if u.pvalue<0.05 else ''}")

    print("\n=== B. SHORT 收益率分离 (方向应相反: 看空时 SHORT 该更好) ===")
    for lbl, (col, cmp_) in sigs.items():
        b = (S[col] <= cuts[col]).to_numpy() if cmp_ == "le" else (S[col] >= cuts[col]).to_numpy()
        x, y = S.bps[b].to_numpy(), S.bps[~b].to_numpy()
        if len(x) < 5 or len(y) < 5:
            continue
        u = stats.mannwhitneyu(x, y, alternative="greater")
        print(f"  {lbl:14s}: 看空 {len(x):3d} 笔 {x.mean():+7.2f}bps | "
              f"看多 {len(y):3d} 笔 {y.mean():+7.2f}bps | 差 {x.mean()-y.mean():+7.2f} "
              f"| MWU p={u.pvalue:.4f} {'显著' if u.pvalue<0.05 else ''}")

    print("\n=== C. 半仓压制 LONG 的平均节省 (bps/笔) + 2日块CI + 环形位移 ===")
    bps = L.bps.to_numpy()
    t = L.entry.dt.tz_localize(None).to_numpy("datetime64[ns]")
    hrs = ((L.entry - L.entry.min()) / pd.Timedelta(hours=1)).to_numpy().astype(int)
    span = hrs.max() + 1
    for lbl, (col, cmp_) in sigs.items():
        b = (L[col] <= cuts[col]).to_numpy() if cmp_ == "le" else (L[col] >= cuts[col]).to_numpy()
        # portfolio-level effect: mean bps of the whole LONG book after damping
        after = np.where(b, 0.5 * bps, bps)
        eff = after - bps
        s, lo, hi = block_ci(t, eff)
        sig_by_h = np.zeros(span, dtype=bool)
        sig_by_h[hrs[b]] = True
        null = np.empty(2000)
        for i in range(2000):
            rot = np.roll(sig_by_h, RNG.integers(1, span))[hrs]
            null[i] = (np.where(rot, 0.5 * bps, bps) - bps).mean()
        p = float((null >= s).mean())
        print(f"  {lbl:14s}: 全簿改善 {s:+7.3f}bps/笔 CI2d [{lo:+7.3f},{hi:+7.3f}] "
              f"零假设均 {null.mean():+6.3f} p={p:.4f} {'显著' if p<0.05 else ''}")

    print("\n=== D. 组合口径: 全簿 bps 均值 (LONG 半仓压制后) ===")
    allb = f.bps.to_numpy()
    isL = (f.side == "LONG").to_numpy()
    print(f"  原始全簿: {allb.mean():+.3f} bps/笔 ({len(f)} 笔)")
    for lbl, (col, cmp_) in sigs.items():
        b = (f[col] <= cuts[col]).to_numpy() if cmp_ == "le" else (f[col] >= cuts[col]).to_numpy()
        damp = isL & b
        print(f"  {lbl:14s}: {np.where(damp, 0.5*allb, allb).mean():+.3f} bps/笔 "
              f"(压制 {damp.sum()} 笔)")


if __name__ == "__main__":
    main()
