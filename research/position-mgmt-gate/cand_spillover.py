"""候选 1:跨币溢出。同一时刻**其他币**的状态,单币价格路径里天然没有。

构造(全部只用已收盘 bar,且严格排除自己):
  mkt_mom24     全市场 24h 动量中位数(等权,剔除本币)
  mkt_breadth   同期上涨币占比(剔除本币)
  rel_mom24_dir 本币 24h 动量 − 全市场中位数,乘 side(相对强弱)
  mkt_volr      全市场波动比中位数(剔除本币)
  disp24        全市场 24h 动量的横截面离散度(高=各走各路,低=齐涨齐跌)
剔除本币这一步很关键 —— 不剔就把自己的动量灌进"市场"里,等于用自己预测自己。
"""
import os, numpy as np, pandas as pd
import pmgate as P, screen as S

syms=sorted(f[:-4] for f in os.listdir(P.CACHE) if f.endswith('.pkl'))
mom, volr = {}, {}
for s in syms:
    f=P.cached_features(s)
    if f is None: continue
    mom[s]=f["mom24"]; volr[s]=f["vol_ratio"]
    del f
M=pd.DataFrame(mom).sort_index()
V=pd.DataFrame(volr).sort_index()
n_ok=M.notna().sum(axis=1)
med=M.median(axis=1, skipna=True)
up=(M>0).sum(axis=1)
tot=M.notna().sum(axis=1)
vmed=V.median(axis=1, skipna=True)
disp=M.std(axis=1, skipna=True)
print(f"市场面板 {M.shape[0]} 行 × {M.shape[1]} 币, 有效币数中位 {int(n_ok.median())}")

extra={}
# 逐币剔除自己:中位数用"去掉该币后重算"的近似——币数>30 时用全体中位数误差可忽略,
# 但上涨占比与离散度必须精确剔除,因为它们对单个成员敏感。
mkt_mom={}; mkt_breadth={}; rel_mom={}; mkt_volr={}; mkt_disp={}
for s in syms:
    if s not in M.columns: continue
    others=M.drop(columns=[s])
    mkt_mom[s]=others.median(axis=1,skipna=True)
    ot=others.notna().sum(axis=1)
    mkt_breadth[s]=((others>0).sum(axis=1)/ot.replace(0,np.nan))
    rel_mom[s]=M[s]-mkt_mom[s]
    mkt_volr[s]=V.drop(columns=[s]).median(axis=1,skipna=True) if s in V.columns else None
    mkt_disp[s]=others.std(axis=1,skipna=True)
extra={"mkt_mom24":mkt_mom,"mkt_breadth":mkt_breadth,"rel_mom24_dir":rel_mom,
       "mkt_volr":mkt_volr,"disp24":mkt_disp}
df=S.decision_points(extra_cols=extra)
print(f"决策样本 {len(df)}")
S.run("跨币溢出 (mkt_mom24/mkt_breadth/rel_mom24_dir/mkt_volr/disp24)",
      df, ["mkt_mom24","mkt_breadth","rel_mom24_dir","mkt_volr","disp24"])
