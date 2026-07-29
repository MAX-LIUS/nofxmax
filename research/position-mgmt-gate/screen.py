"""决策时刻信息量筛子(通用)。任何候选输入都必须先过这里再谈建模。

为什么要有这道筛子
==================
上一轮已证明:纯路径式管理是零和重分配,因为决策时刻**没有新信息进入**
(74171 个决策点上本地特征 AUC=0.4999,打乱零假设 0.4999)。所以现在唯一值得找的
是"不在单币价格路径里"的输入。这道筛子 20 秒跑完,过不了就不必进模拟器 ——
能省掉一整轮网格。

筛子的构造(每一处都是为了不骗自己)
====================================
- **决策时刻**:持仓 12h 且浮亏 > 0.5 ATR。这是管理规则真正要做决定的地方,
  不是随便取的时间点。
- **标签**:该点之后剩余持有期(最多再 36h)的收益是否为正 —— "会不会恢复"。
- **切分**:严格按时间(2024-07-01),不做随机切分。72h 标签会让相邻样本近似重复,
  随机切分等于泄漏。
- **零假设**:打乱标签重算 AUC 200 次。**不用 0.5 当零假设** —— 类别不平衡与
  预测值分布会让实际零假设偏离 0.5,必须实测。
- **方向归一**:带方向的特征乘以 side,让多空可比。否则空头样本会把系数抵消掉。
- **对照基线**:同时报"只用本地路径特征"的 AUC。候选输入必须**超过它**才算带来新信息,
  而不是只超过 0.5。

判据(写死,避免事后挪门槛)
==========================
  ΔAUC = 候选组 AUC − 本地组 AUC
  过筛 = ΔAUC >= 0.01 且 打乱检验 p < 0.05 且 五分位剩余收益单调性检验 p < 0.05
0.01 的 AUC 不算大,但对"是否值得建模"这个二元决定已经够了 —— 低于这个连方向都谈不上。
"""
from __future__ import annotations
import os
import numpy as np
import pandas as pd
from scipy import stats
from sklearn.linear_model import LogisticRegression
from sklearn.metrics import roc_auc_score

import pmgate as P
import fastsim as F
import tournament as T

RNG = np.random.default_rng(7)
SPLIT = pd.Timestamp("2024-07-01", tz="UTC")
LOCAL = ["slope168", "slope48", "mom6", "mom24", "mom72", "vol_ratio",
         "taker_imb24", "atr_pct"]
DIRECTIONAL = {"slope168", "slope48", "mom6", "mom24", "mom72", "taker_imb24"}
HOLD_COL = 12          # decision point: 12 bars held
LOSS_ATR = 0.5         # and underwater by >0.5 ATR
REMAIN = 36            # label horizon after the decision point
AUC_GATE = 0.01


def decision_points(extra_cols=None):
    """构造决策样本表。extra_cols: {列名: {symbol: Series}} 形式的候选输入。"""
    syms = sorted(f[:-4] for f in os.listdir(P.CACHE) if f.endswith(".pkl"))
    rows = []
    for s in syms:
        f = P.cached_features(s)
        if f is None:
            continue
        idx, sides = P.entry_points(f)
        pp = F.build_paths(f, idx, sides)
        if pp is None:
            del f
            continue
        pp["ts"] = f.index.view("int64")[pp["idx"]]
        r_cl, valid, end = pp["r_cl"], pp["valid"], pp["end_col"]
        a = pp["atr0"]
        col = HOLD_COL
        ok = (end >= col + 12) & valid[:, col] & (r_cl[:, col] <= -LOSS_ATR * a)
        if ok.sum() < 20:
            del f
            continue
        w = np.where(ok)[0]
        rem = np.array([r_cl[i, min(end[i], col + REMAIN)] - r_cl[i, col] for i in w])
        gs = pp["sides"][ok]
        dec_abs = np.minimum(pp["fill"][ok] + col, len(f) - 1)
        d = {"sym": s, "ts": f.index.view("int64")[dec_abs], "rem": rem, "side": gs,
             "r_now": r_cl[ok, col], "atr0": a[ok]}
        fv = f[LOCAL].to_numpy()[dec_abs]
        for j, nm in enumerate(LOCAL):
            v = fv[:, j]
            d[nm] = v * gs if nm in DIRECTIONAL else v
        if extra_cols:
            for cname, per_sym in extra_cols.items():
                ser = per_sym.get(s)
                if ser is None:
                    d[cname] = np.full(len(w), np.nan)
                else:
                    # 用决策时刻的绝对下标去对齐,只取 <= 该 bar 的值
                    arr = ser.reindex(f.index, method="ffill").to_numpy()
                    v = arr[dec_abs]
                    d[cname] = v * gs if cname.endswith("_dir") else v
        rows.append(pd.DataFrame(d))
        del f
    return pd.concat(rows, ignore_index=True)


def _fit_auc(tr, te, feats):
    ytr = (tr.rem > 0).to_numpy().astype(int)
    yte = (te.rem > 0).to_numpy().astype(int)
    Xtr, Xte = tr[feats].to_numpy(), te[feats].to_numpy()
    mu, sd = Xtr.mean(0), Xtr.std(0)
    sd[sd == 0] = 1
    m = LogisticRegression(max_iter=3000, C=0.1).fit((Xtr - mu) / sd, ytr)
    p = m.predict_proba((Xte - mu) / sd)[:, 1]
    return roc_auc_score(yte, p), p, yte, m


def run(tag: str, df: pd.DataFrame, cand_feats: list[str], split=None):
    """跑一次筛子。返回是否过筛。"""
    need = LOCAL + cand_feats + ["rem", "ts", "side"]
    d = df[need].replace([np.inf, -np.inf], np.nan).dropna()
    d = d.assign(t=pd.to_datetime(d.ts, utc=True))
    # 候选数据覆盖期不同(OI metrics 只从 2024-06-01 起),允许按候选前移/后移切分点,
    # 但切分点必须在**看任何结果之前**定好 —— 这里由调用方一次性传入,不做事后调参。
    sp = SPLIT if split is None else pd.Timestamp(split, tz="UTC")
    tr, te = d[d.t < sp], d[d.t >= sp]
    print(f"\n{'='*72}\n候选: {tag}")
    print(f"  可用样本 {len(d)}(丢弃缺失后), 训练 {len(tr)} 验收 {len(te)}")
    if len(te) < 3000 or len(tr) < 3000:
        print(f"  ✗ 样本不足,无法判定")
        return False
    auc_loc, _, yte, _ = _fit_auc(tr, te, LOCAL)
    auc_all, p_all, _, m = _fit_auc(tr, te, LOCAL + cand_feats)
    null = np.array([roc_auc_score(RNG.permutation(yte), p_all) for _ in range(200)])
    p_null = float((null >= auc_all).mean())
    d_auc = auc_all - auc_loc
    print(f"  本地路径特征 AUC        = {auc_loc:.4f}")
    print(f"  加入候选后 AUC          = {auc_all:.4f}   ΔAUC = {d_auc:+.4f}")
    print(f"  打乱标签零假设 AUC      = {null.mean():.4f} "
          f"[{np.percentile(null,2.5):.4f},{np.percentile(null,97.5):.4f}]  p={p_null:.4f}")

    q = pd.qcut(p_all, 5, labels=False, duplicates="drop")
    rem = te.rem.to_numpy()
    print(f"  {'五分位':<8}{'n':>7}{'恢复率':>9}{'剩余收益bps':>13}")
    means = []
    for k in sorted(set(q)):
        sub = rem[q == k]
        means.append(sub.mean())
        print(f"    Q{k+1:<5}{len(sub):>7}{float((sub>0).mean()):>9.3f}{float(sub.mean()*1e4):>13.1f}")
    lo, hi = rem[q == min(q)], rem[q == max(q)]
    t_s, p_s = stats.ttest_ind(hi, lo, equal_var=False)
    rho, p_rho = stats.spearmanr(np.arange(len(means)), means)
    print(f"  最高−最低分位 剩余收益差 = {(hi.mean()-lo.mean())*1e4:+.1f}bps  p={p_s:.4f}")
    print(f"  五分位单调性 Spearman    = {rho:+.3f}  p={p_rho:.4f}")
    coefs = dict(zip(LOCAL + cand_feats, m.coef_[0]))
    show = {k: v for k, v in coefs.items() if k in cand_feats}
    print("  候选项系数(标准化后): " + ", ".join(f"{k}={v:+.3f}" for k, v in show.items()))

    passed = (d_auc >= AUC_GATE) and (p_null < 0.05) and (p_s < 0.05)
    print(f"  {'✓ 过筛' if passed else '✗ 未过筛'}  "
          f"(判据 ΔAUC>={AUC_GATE} 且 打乱p<0.05 且 分位差p<0.05)")
    return passed
