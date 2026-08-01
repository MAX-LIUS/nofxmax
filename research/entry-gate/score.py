"""Walk-forward adverse-risk score, then its economic value under ladder exits.

PROTOCOL
========
- fit on all quarters strictly BEFORE the evaluation quarter (expanding window);
- logistic regression, features standardised with the TRAINING moments only;
- minimum 4 training quarters before the first evaluation;
- the score is then used as a SIZE MULTIPLIER, not a hard block, because the
  earlier rounds showed hard blocks throw away the good part of the tail.

Everything is reported per quarter with a binomial sign test. No pooled p-value
decides anything.
"""
import numpy as np, pandas as pd
from scipy import stats
from sklearn.linear_model import LogisticRegression
import evalgate as E
from adverse import auc

ADV = "/root/projects/nofxmax/research/entry-gate/cache/adverse.parquet"
LAD = "/root/projects/nofxmax/research/entry-gate/cache/ladder.parquet"

F = ["close_pos_in_bar","body_frac","chop14","er24","er72","rpos30","rpos72",
     "atr_pct","atr_rank168","lower_wick_frac","upper_wick_frac","adx14",
     "ret24_atr","vol_z72","taker_imb","dist_hi30_atr","dist_lo30_atr"]

ev = pd.read_parquet(ADV)
ev = ev.dropna(subset=F + ["adverse"])
qs = sorted(ev.q.unique())
print(f"rows={len(ev)} quarters={len(qs)} base_rate={ev.adverse.mean():.4f}")

rows = []
oos = []
for i, q in enumerate(qs):
    if i < 4:
        continue
    tr = ev[ev.q.isin(qs[:i])]
    te = ev[ev.q == q]
    for side in ("LONG","SHORT"):
        trs, tes = tr[tr.side == side], te[te.side == side]
        if len(trs) < 5000 or len(tes) < 500:
            continue
        Xtr = trs[F].to_numpy(); ytr = trs.adverse.to_numpy().astype(int)
        mu, sd = Xtr.mean(0), Xtr.std(0)
        sd = np.where(sd > 0, sd, 1.0)
        m = LogisticRegression(max_iter=2000, C=0.1)
        m.fit((Xtr - mu)/sd, ytr)
        Xte = (tes[F].to_numpy() - mu)/sd
        p = m.predict_proba(Xte)[:, 1]
        a = auc(p, tes.adverse.to_numpy())
        rows.append(dict(q=q, side=side, n_tr=len(trs), n_te=len(tes), auc_oos=a))
        oos.append(pd.DataFrame({"sym": tes.sym.to_numpy(), "t": tes.t.to_numpy(),
                                 "side": side, "q": q, "p_adv": p,
                                 "adverse": tes.adverse.to_numpy()}))

r = pd.DataFrame(rows)
print("\n=== out-of-sample AUC per quarter (expanding-window fit) ===")
print(r.pivot(index="q", columns="side", values="auc_oos").round(4).to_string())
for side in ("LONG","SHORT"):
    s = r[r.side == side].auc_oos
    frac, p, npos, n = E.sign_test(s - 0.5)
    print(f"{side}: mean AUC={s.mean():.4f}  >0.5 in {npos}/{n} quarters  binom_p={p:.4f}")

o = pd.concat(oos, ignore_index=True)
o.to_parquet("/root/projects/nofxmax/research/entry-gate/cache/oos_score.parquet")
print(f"\nOOS scored rows: {len(o)}")
print("\n=== realized adverse rate by OOS score decile ===")
o["d"] = o.groupby(["q","side"]).p_adv.transform(lambda s: pd.qcut(s, 10, labels=False, duplicates="drop"))
print(o.groupby("d").agg(n=("adverse","size"), adverse_rate=("adverse","mean"),
                         mean_p=("p_adv","mean")).round(4).to_string())
