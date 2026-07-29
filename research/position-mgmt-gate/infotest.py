"""决定性一测:在**决策时刻**,本地特征对"接下来是恢复还是继续恶化"有没有信息?

为什么这一测决定整个方向的天花板
================================
前面所有变种都只用 r/peak/trough —— 纯路径函数,决策时刻**没有任何新信息进入**。
所以它们只能重新切分收益分布(实测正是如此:胜率 +29.1pp 极稳,期望/夏普 9/18 噪声)。
M5 的立意恰恰相反:把一个**外部方向信号**引入决策点。厂商那条路已判失败(15 季按季
等权为负),那么问题变成:**本地特征自己有没有决策时刻的信息?**

有 → 值得做条件式管理(下一轮的方向);没有 → 纯管理的天花板就是零和,应当直说。

做法:取所有"当前浮亏"的持仓时刻(这是管理规则真正要做决定的地方),
用决策时刻可得的本地特征去预测**剩余持有期收益**,严格按时间切分训练/验收,
报 AUC 与分组收益差。同时给一个**打乱标签**的对照来定 AUC 的零假设水平。
"""
import os, numpy as np, pandas as pd
from scipy import stats
import pmgate as P, fastsim as F, tournament as T
from sklearn.linear_model import LogisticRegression
from sklearn.metrics import roc_auc_score

RNG=np.random.default_rng(7)
SPLIT=pd.Timestamp("2024-07-01",tz="UTC")
FEATS=["slope168","slope48","mom6","mom24","mom72","vol_ratio","taker_imb24","atr_pct"]

syms=sorted(f[:-4] for f in os.listdir(P.CACHE) if f.endswith('.pkl'))
rows=[]
for s in syms:
    f=P.cached_features(s)
    if f is None: continue
    idx,sides=P.entry_points(f)
    pp=F.build_paths(f,idx,sides)
    if pp is None: continue
    pp["ts"]=f.index.view("int64")[pp["idx"]]
    r_cl=pp["r_cl"]; valid=pp["valid"]; end=pp["end_col"]; a=pp["atr0"][:,None]
    # 决策时刻:持仓 12 根、且当前浮亏超过 0.5 ATR(管理规则真正要做决定的地方)
    col=12
    ok=(end>=col+12)&valid[:,col]&(r_cl[:,col]<=-0.5*a[:,0])
    if ok.sum()<20: 
        del f; continue
    gi=pp["idx"][ok]; gf=pp["fill"][ok]; gs=pp["sides"][ok]
    # 剩余持有期收益:从 col 到 end 的收益变化
    rem=np.array([r_cl[i,min(end[i],col+36)]-r_cl[i,col] for i in np.where(ok)[0]])
    fv=f[FEATS].to_numpy()
    dec=gf+col   # 决策所在 bar 的绝对下标(特征只取 <= 该 bar)
    dec=np.minimum(dec,len(f)-1)
    X=fv[dec]*np.where(np.array(FEATS)[None,:]=="__",1,1)
    # 方向相关的特征乘以 side,使多空可比
    X=X.copy()
    for j,nm in enumerate(FEATS):
        if nm.startswith("slope") or nm.startswith("mom") or nm=="taker_imb24":
            X[:,j]=X[:,j]*gs
    rows.append(pd.DataFrame({"ts":pp["ts"][ok],"rem":rem,"side":gs,
        **{nm:X[:,j] for j,nm in enumerate(FEATS)}}))
    del f
d=pd.concat(rows,ignore_index=True)
d["t"]=pd.to_datetime(d.ts,utc=True)
d=d.replace([np.inf,-np.inf],np.nan).dropna()
print(f"决策样本 {len(d)} 个(持仓 12h 且浮亏 >0.5ATR)")
tr=d[d.t<SPLIT]; te=d[d.t>=SPLIT]
print(f"训练 {len(tr)}  验收 {len(te)}\n")
ytr=(tr.rem>0).to_numpy().astype(int); yte=(te.rem>0).to_numpy().astype(int)
Xtr=tr[FEATS].to_numpy(); Xte=te[FEATS].to_numpy()
mu,sd=Xtr.mean(0),Xtr.std(0); sd[sd==0]=1
m=LogisticRegression(max_iter=2000,C=0.1).fit((Xtr-mu)/sd,ytr)
p=m.predict_proba((Xte-mu)/sd)[:,1]
auc=roc_auc_score(yte,p)
null=np.array([roc_auc_score(RNG.permutation(yte),p) for _ in range(200)])
print(f"验收段 AUC = {auc:.4f}")
print(f"打乱标签零假设 AUC = {null.mean():.4f} [{np.percentile(null,2.5):.4f},{np.percentile(null,97.5):.4f}]")
print(f"超出零假设 = {auc-null.mean():+.4f}   p = {(null>=auc).mean():.4f}\n")
q=pd.qcut(p,5,labels=False,duplicates="drop")
print(f"{'五分位':<8}{'n':>7}{'恢复率':>9}{'剩余收益bps':>13}")
for k in sorted(set(q)):
    sub=te.rem.to_numpy()[q==k]
    print(f"  Q{k+1:<5}{len(sub):>7}{float((sub>0).mean()):>9.3f}{float(sub.mean()*1e4):>13.1f}")
lo=te.rem.to_numpy()[q==min(q)]; hi=te.rem.to_numpy()[q==max(q)]
t,pv=stats.ttest_ind(hi,lo,equal_var=False)
print(f"\n最高分位 - 最低分位 剩余收益差 = {(hi.mean()-lo.mean())*1e4:+.1f}bps  p={pv:.4f}")
print(f"系数(标准化后): "+", ".join(f"{n}={c:+.3f}" for n,c in zip(FEATS,m.coef_[0])))
