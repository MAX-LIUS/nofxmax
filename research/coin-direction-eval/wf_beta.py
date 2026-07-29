"""Is V4's DOWN advantage per-coin skill, or just market beta?

The user's question is whether this is a reliable OBJECTIVE PER-COIN direction
gate. A signal that fires "DOWN" on everything whenever the whole market is
falling would show a big paired advantage while carrying zero per-coin
information. Two decompositions, both on walk-forward (unseen) quarters:

  1. DEMEANED / cross-sectional: within each decision hour, subtract that
     hour's cross-sectional mean advantage. What survives is purely "did V4
     pick the RIGHT COINS among those available this hour". This is the number
     that matters for a per-coin gate.
  2. Hour-level breadth: how concentrated are DOWN calls? If DOWN coverage per
     hour is ~0 or ~100%, the signal is a market-timing switch, not a per-coin
     discriminator.

Also reports direction BA per coin, so "reliable across coins" is testable.
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
    if not files:
        print("没有已打分的季度"); return
    df = pd.concat([pd.read_pickle(f"{SDIR}/{f}") for f in files]).sort_index()
    df.index = pd.to_datetime(df.index, utc=True)
    df = df[np.isfinite(df.net_long) & np.isfinite(df.net_short)]
    df["dt64"] = df.index.tz_localize(None).to_numpy("datetime64[ns]")
    # SHORT-minus-LONG advantage: positive means shorting was better this bar
    df["adv"] = df.net_short - df.net_long
    print(f"walk-forward 合并: {len(df)} 点, {df.symbol.nunique()} 币, "
          f"{df.index.min().date()}~{df.index.max().date()}, 季度 {len(files)}\n")

    t = df["dt64"].to_numpy()
    m, lo, hi = block_ci(t, df.adv.to_numpy(dtype="float64"))
    print(f"无条件 SHORT-LONG 价差(全样本)        {pct(m)}  CI [{pct(lo)},{pct(hi)}]")
    print("  ^ 这是'什么都不判断'的基准; V4 的 DOWN 优势必须显著高于它才算有信息\n")

    # demean ONCE on the full frame, then select rows positionally. Indexing a
    # transform result by a duplicate-timestamp index would blow up cartesian.
    df["adv_dm"] = df.adv - df.groupby("dt64")["adv"].transform("mean")
    df["raw_side"] = np.where(df["raw_up"] >= 0.5, 1, -1)
    for tag, mask in (("原始分(无校准)", df.raw_side < 0),
                      ("厂商校准", df.state_vendor < 0)):
        sd = df[mask]
        if len(sd) > 100:
            tt = sd["dt64"].to_numpy()
            m, lo, hi = block_ci(tt, sd.adv.to_numpy(dtype="float64"))
            m2, lo2, hi2 = block_ci(tt, sd.adv_dm.to_numpy(dtype="float64"))
            print(f"{tag} DOWN 优势 原始   {pct(m)} CI [{pct(lo)},{pct(hi)}]  n={len(sd)}")
            print(f"{tag} DOWN 优势 去均值 {pct(m2)} CI [{pct(lo2)},{pct(hi2)}]")
    print()

    down = df[df.state < 0]
    m, lo, hi = block_ci(down["dt64"].to_numpy(), down.adv.to_numpy(dtype="float64"))
    print(f"V4 DOWN 的 SHORT-LONG 优势 (原始)     {pct(m)}  CI [{pct(lo)},{pct(hi)}]  n={len(down)}")

    # ---- 1. cross-sectional demeaning: kill market timing, keep coin picking
    dmd = df[df.state < 0]
    m, lo, hi = block_ci(dmd["dt64"].to_numpy(), dmd.adv_dm.to_numpy(dtype="float64"))
    print(f"V4 DOWN 的优势 (小时内去均值, 纯选币)  {pct(m)}  CI [{pct(lo)},{pct(hi)}]")
    print("  ^ 若这个区间跨零, 说明 V4 的优势来自'大盘在跌'而不是'这个币会跌'\n")

    up = df[df.state > 0]
    m, lo, hi = block_ci(up["dt64"].to_numpy(), (-up.adv).to_numpy(dtype="float64"))
    print(f"V4 UP 的 LONG-SHORT 优势 (原始)       {pct(m)}  CI [{pct(lo)},{pct(hi)}]  n={len(up)}")
    m, lo, hi = block_ci(up["dt64"].to_numpy(), (-up.adv_dm).to_numpy(dtype="float64"))
    print(f"V4 UP 的优势 (小时内去均值, 纯选币)    {pct(m)}  CI [{pct(lo)},{pct(hi)}]\n")

    # our gate, same treatment
    ours = df[df.slope168 < 0]
    m, lo, hi = block_ci(ours["dt64"].to_numpy(), ours.adv.to_numpy(dtype="float64"))
    print(f"我们 slope168<0 优势 (原始)           {pct(m)}  CI [{pct(lo)},{pct(hi)}]  n={len(ours)}")
    m, lo, hi = block_ci(ours["dt64"].to_numpy(), ours.adv_dm.to_numpy(dtype="float64"))
    print(f"我们 slope168<0 优势 (去均值, 纯选币)  {pct(m)}  CI [{pct(lo)},{pct(hi)}]\n")

    # ---- 2. breadth: is DOWN a market switch or a per-coin discriminator?
    br = pd.DataFrame({
        "n": df.groupby("dt64").size(),
        "down_frac": (df.state < 0).groupby(df.dt64).mean(),
        "up_frac": (df.state > 0).groupby(df.dt64).mean()})
    print("每小时 DOWN 覆盖率分布 (广度):")
    for q in (0.05, 0.25, 0.50, 0.75, 0.95):
        print(f"  p{int(q*100):>2} = {br.down_frac.quantile(q)*100:5.1f}%", end="")
    print(f"\n  全 0 的小时 {int((br.down_frac==0).sum())}/{len(br)}  "
          f"全 1 的小时 {int((br.down_frac==1).sum())}/{len(br)}  "
          f"均值 {br.down_frac.mean()*100:.1f}%")
    print("  ^ 若分布集中在 0 或 1, 说明它是大盘开关; 若居中分散, 才是逐币判别\n")

    # ---- 3. per-coin direction BA on unseen quarters
    ok = np.isfinite(df.future_score_72)
    e = df[ok & (df.state != 0)].copy()
    e["pred_up"] = e.state > 0
    e["truth_up"] = e.future_score_72 > 0
    rows = []
    for s, g in e.groupby("symbol"):
        if len(g) < 200 or not g.truth_up.any() or g.truth_up.all():
            continue
        tpr = (g.pred_up & g.truth_up).sum() / g.truth_up.sum()
        tnr = ((~g.pred_up) & (~g.truth_up)).sum() / (~g.truth_up).sum()
        rows.append((s, len(g), 0.5 * (tpr + tnr)))
    ba = pd.DataFrame(rows, columns=["symbol", "n", "ba"]).sort_values("ba")
    ba.to_csv(f"{BASE}/wf_ba_by_symbol.csv", index=False)
    print(f"逐币方向 BA (样本外, {len(ba)} 币):")
    print(f"  >50% 的币 {int((ba.ba>0.5).sum())}/{len(ba)}   均 {ba.ba.mean()*100:.2f}%  "
          f"中位 {ba.ba.median()*100:.2f}%  最差 {ba.ba.min()*100:.2f}%  最好 {ba.ba.max()*100:.2f}%")
    print("  最差5:", ", ".join(f"{r.symbol} {r.ba*100:.1f}%" for r in ba.head(5).itertuples()))
    print("  最好5:", ", ".join(f"{r.symbol} {r.ba*100:.1f}%" for r in ba.tail(5).itertuples()))
    tot_tpr = (e.pred_up & e.truth_up).sum() / e.truth_up.sum()
    tot_tnr = ((~e.pred_up) & (~e.truth_up)).sum() / (~e.truth_up).sum()
    print(f"  合并 BA {0.5*(tot_tpr+tot_tnr)*100:.2f}%  (UP召回 {tot_tpr*100:.1f}%  DOWN召回 {tot_tnr*100:.1f}%)")


if __name__ == "__main__":
    main()
