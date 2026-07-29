"""Is the SHORT separation (-79.42bps, p=0.0012) real, or a few disasters?

Checks:
  1. medians and trimmed means (a rank test already says it is not pure tail,
     but state it explicitly)
  2. drop the worst / best N fills and see if it survives
  3. per trader-ish split (exchange_type) and per symbol concentration
  4. is one week carrying it (the weekly table hinted at 2026-07-06)
  5. does it survive excluding the largest-notional fills (sizing noise)
"""
from __future__ import annotations
import glob
import numpy as np
import pandas as pd
from scipy import stats

BASE = "/root/.claude/jobs/5cbb3cf4/tmp/cdeval"
LAG = 1


def build():
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
    f = pd.read_csv(f"{BASE}/real_fills.csv")
    f["entry"] = pd.to_datetime(f.entry_time, unit="ms", utc=True)
    f["notional"] = f.quantity.astype(float) * f.entry_price.astype(float)
    f = f[f.notional > 0].copy()
    f["bps"] = f.realized_pnl.astype(float) / f.notional * 10_000.0
    f = f.sort_values("entry").reset_index(drop=True)
    f = pd.merge_asof(f, mk.reset_index(names="t")[["t", "lean_pub"]],
                      left_on="entry", right_on="t", direction="backward",
                      tolerance=pd.Timedelta(hours=6)).drop(columns=["t"])
    f = f[f.lean_pub.notna()].copy()
    f["bear"] = f.lean_pub <= cut
    return f, cut


def sep(s, side):
    a = s.bps.to_numpy()[s.bear.to_numpy()]
    b = s.bps.to_numpy()[~s.bear.to_numpy()]
    if len(a) < 5 or len(b) < 5:
        return None
    alt = "greater" if side == "LONG" else "less"
    u = stats.mannwhitneyu(b, a, alternative=alt)
    return dict(na=len(a), nb=len(b), ma=a.mean(), mb=b.mean(),
                meda=float(np.median(a)), medb=float(np.median(b)),
                d=b.mean() - a.mean(), p=u.pvalue)


def main():
    f, cut = build()
    S = f[f.side == "SHORT"]
    print(f"SHORT {len(S)} 笔, 看空 {int(S.bear.sum())} / 看多 {int((~S.bear).sum())}\n")

    r = sep(S, "SHORT")
    print("=== 1. 均值 vs 中位数 (中位数说明是否只有尾部) ===")
    print(f"  看空组 均值 {r['ma']:+.2f} 中位 {r['meda']:+.2f} (n={r['na']})")
    print(f"  看多组 均值 {r['mb']:+.2f} 中位 {r['medb']:+.2f} (n={r['nb']})")
    print(f"  均值分离 {r['d']:+.2f}  中位分离 {r['medb']-r['meda']:+.2f}  MWU p={r['p']:.4f}")
    for tr in (0.05, 0.10, 0.20):
        a = S.bps.to_numpy()[S.bear.to_numpy()]
        b = S.bps.to_numpy()[~S.bear.to_numpy()]
        ta = stats.trim_mean(a, tr)
        tb = stats.trim_mean(b, tr)
        print(f"  截尾 {tr*100:.0f}%: 看空 {ta:+.2f} 看多 {tb:+.2f} 分离 {tb-ta:+.2f}")

    print("\n=== 2. 剔除最差/最好 N 笔后是否仍成立 ===")
    for n in (1, 3, 5, 10):
        s2 = S.sort_values("bps").iloc[n:-n] if len(S) > 2 * n + 20 else None
        if s2 is None:
            continue
        r2 = sep(s2, "SHORT")
        print(f"  剔除各 {n:2d} 笔 (剩 {len(s2)}): 分离 {r2['d']:+.2f} p={r2['p']:.4f}")

    print("\n=== 3. 按交易所拆分 ===")
    for ex, g in S.groupby("exchange_type"):
        r3 = sep(g, "SHORT")
        if r3 is None:
            print(f"  {ex}: 样本不足 ({len(g)} 笔)")
            continue
        print(f"  {ex:10s} {len(g):3d} 笔: 看空 {r3['ma']:+7.2f} 看多 {r3['mb']:+7.2f} "
              f"分离 {r3['d']:+7.2f} p={r3['p']:.4f}")

    print("\n=== 4. 逐周拆分 (是否只有一周在扛) ===")
    S = S.copy()
    S["wk"] = S.entry.dt.tz_localize(None).dt.to_period("W")
    for wk, g in S.groupby("wk", observed=True):
        r4 = sep(g, "SHORT")
        if r4 is None:
            print(f"  {str(wk)[:10]}: {len(g):3d} 笔 样本不足")
            continue
        print(f"  {str(wk)[:10]}: {len(g):3d} 笔 看空 {r4['ma']:+7.2f} "
              f"看多 {r4['mb']:+7.2f} 分离 {r4['d']:+7.2f} p={r4['p']:.4f}")

    print("\n=== 5. 剔除最大名义额 10% (排除仓位噪声) ===")
    thr = S.notional.quantile(0.90)
    s5 = S[S.notional <= thr]
    r5 = sep(s5, "SHORT")
    print(f"  名义额 <= {thr:.1f} USDT 的 {len(s5)} 笔: 分离 {r5['d']:+.2f} p={r5['p']:.4f}")

    print("\n=== 6. 品种集中度 (前 5 个 symbol 占比) ===")
    vc = S.symbol.value_counts()
    print(f"  共 {len(vc)} 个品种, 前 5: "
          + ", ".join(f"{k}={v}" for k, v in vc.head(5).items())
          + f" (前5占 {vc.head(5).sum()/len(S)*100:.0f}%)")
    top = vc.head(3).index.tolist()
    s6 = S[~S.symbol.isin(top)]
    r6 = sep(s6, "SHORT")
    print(f"  剔除前 3 品种后 {len(s6)} 笔: 分离 {r6['d']:+.2f} p={r6['p']:.4f}")


if __name__ == "__main__":
    main()
