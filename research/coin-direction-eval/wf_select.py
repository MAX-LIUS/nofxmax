"""Selection test on WALK-FORWARD quarters only — every row scored by a model
that never saw it. This is the honest version of select_test's headline.

Arms match select_test2 so the numbers are directly comparable:
  A  V4 rank + V4 side      C  our veto + V4 rank
  B  our veto + random      E  random coin + V4 side
Reported per quarter and pooled, with a 14-day block bootstrap.
"""
from __future__ import annotations
import os
import numpy as np
import pandas as pd

BASE = "/root/.claude/jobs/5cbb3cf4/tmp/cdeval"
SDIR = f"{BASE}/wf_scored"
RNG = np.random.default_rng(20260729)
N_DRAW = 20


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


def run(sub, K, mode):
    ts_list, val_list = [], []
    for ts, g in sub.groupby("dt64", sort=True):
        # veto = our gate only permits the side aligned with the 168h slope, so
        # a V4 pick survives only when the two agree (a real intersection).
        if mode.get("veto"):
            sd = mode["side"]
            gg = g[(g.sl_side != 0) & ((sd == "sl_side") | (g[sd] == g.sl_side))]
        else:
            gg = g
        side = gg[mode["side"]].to_numpy()
        keep = side != 0
        gg, side = gg[keep], side[keep]
        if len(gg) == 0:
            continue
        if mode["rank"] == "random":
            idx = (np.arange(len(gg)) if len(gg) <= K
                   else RNG.choice(len(gg), size=K, replace=False))
        else:
            idx = np.argsort(-np.abs(gg[mode["rank"]].to_numpy()))[:K]
        net = np.where(side[idx] > 0, gg.net_long.to_numpy()[idx], gg.net_short.to_numpy()[idx])
        ts_list.append(ts)
        val_list.append(net.mean())
    return np.array(ts_list, dtype="datetime64[ns]"), np.array(val_list)


ARMS = [
    ("A  我方校准 排序+方向", dict(rank="v4_score", side="v4_side")),
    ("A2 厂商校准 排序+方向", dict(rank="vd_score", side="vd_side")),
    ("A3 原始分 排序+方向", dict(rank="raw_score", side="raw_side")),
    ("B  我们否决+随机选币", dict(rank="random", side="sl_side", veto=True)),
    ("C  我们否决+原始分排序", dict(rank="raw_score", side="raw_side", veto=True)),
    ("E  随机选币+原始分方向", dict(rank="random", side="raw_side")),
]


def prep(df):
    df = df.copy()
    df.index = pd.to_datetime(df.index, utc=True)
    df = df[df.index.hour % 6 == 0]
    df["dt64"] = df.index.tz_localize(None).to_numpy("datetime64[ns]")
    df["v4_side"] = np.sign(df["state"]).astype(int)
    df["v4_score"] = df["state"].astype(float)
    df["sl_side"] = np.sign(df["slope168"]).astype(int)
    # vendor-constant calibration (what they would actually ship)
    df["vd_side"] = np.sign(df["state_vendor"]).astype(int)
    df["vd_score"] = df["state_vendor"].astype(float)
    # RAW score: ranking needs only monotonicity, so this is calibration-free and
    # is the most generous possible test of "can it pick coin + side".
    df["raw_side"] = np.where(df["raw_up"] >= 0.5, 1, -1).astype(int)
    df["raw_score"] = (df["raw_up"] - 0.5).astype(float)
    return df


def summarize(sub, label):
    print("=" * 110)
    print(f"### {label}   {sub.symbol.nunique()} 币  {len(sub)} 点  "
          f"{sub.index.min().date()}~{sub.index.max().date()}")
    print("=" * 110)
    t = sub["dt64"].to_numpy()
    for lbl, v in (("基线 全做多", sub.net_long.to_numpy(dtype="float64")),
                   ("基线 全做空", sub.net_short.to_numpy(dtype="float64"))):
        m, lo, hi = block_ci(t, v)
        print(f"  {lbl:<24} 每笔 {pct(m)}  CI [{pct(lo)},{pct(hi)}]")
    for K in (3, 5):
        print("-" * 110)
        for lbl, mode in ARMS:
            reps = N_DRAW if mode["rank"] == "random" else 1
            outs = [run(sub, K, mode) for _ in range(reps)]
            if reps > 1:
                L = min(len(v) for _, v in outs)
                tt = outs[0][0][:L]
                vv = np.mean([v[:L] for _, v in outs], axis=0)
            else:
                tt, vv = outs[0]
            m, lo, hi = block_ci(tt, vv)
            flag = "  ★显著" if np.isfinite(lo) and lo > 0 else ""
            print(f"  top{K} {lbl:<24} 小时={len(vv):>5}  每笔 {pct(m)}  CI [{pct(lo)},{pct(hi)}]{flag}")


def main():
    files = sorted(f for f in os.listdir(SDIR) if f.endswith(".pkl"))
    if not files:
        print("没有已打分的季度"); return
    print(f"walk-forward 季度: {len(files)} -> {[f[:-4] for f in files]}\n")
    per_q = []
    frames = []
    for f in files:
        d = prep(pd.read_pickle(f"{SDIR}/{f}"))
        frames.append(d)
        row = {"q": f[:-4], "n": len(d)}
        for lbl, mode in ARMS:
            reps = N_DRAW if mode["rank"] == "random" else 1
            outs = [run(d, 5, mode) for _ in range(reps)]
            if reps > 1:
                L = min(len(v) for _, v in outs)
                vv = np.mean([v[:L] for _, v in outs], axis=0)
            else:
                vv = outs[0][1]
            row[lbl.split()[0]] = float(vv.mean()) if len(vv) else np.nan
        row["baseL"] = float(d.net_long.mean())
        row["baseS"] = float(d.net_short.mean())
        per_q.append(row)
        print(f"  {row['q']}  n={row['n']:>6}  A {pct(row['A'])}  B {pct(row['B'])}  "
              f"A2 {pct(row['A2'])}  A3 {pct(row['A3'])}  B {pct(row['B'])}  C {pct(row['C'])}  "
              f"E {pct(row['E'])} | 基线L {pct(row['baseL'])} S {pct(row['baseS'])}",
              flush=True)

    q = pd.DataFrame(per_q)
    q.to_csv(f"{BASE}/wf_select.csv", index=False)
    print("\n每季度胜负 (top5):")
    for a in ("A", "A2", "A3", "B", "C", "E"):
        print(f"  {a}: 为正 {int((q[a]>0).sum())}/{len(q)}  均 {pct(q[a].mean())}  "
              f"中位 {pct(q[a].median())}  最差 {pct(q[a].min())}")
    print(f"  A3(原始分) 打败 B(我们否决+随机) 的季度: {int((q.A3>q.B).sum())}/{len(q)}")
    print()
    summarize(pd.concat(frames).sort_index(), "全部 walk-forward 季度合并 (全样本外)")


if __name__ == "__main__":
    main()
