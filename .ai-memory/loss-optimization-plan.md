
## 方案A 实现完成 (选项2, 未部署) — 2026-07-21

- store/strategy.go RiskControlConfig 加字段: RiskSizingEnabled bool, RiskPerTradePctOfEquity float64
- trader/risk_sizing.go 新建:
  - effectiveStopDistancePct(): 取 max(AI声明止损距离, 配置结构止损距离). 结构侧: close_confirm→BackstopATRMul×ATR; 否则遍历ladder SL规则 atr/structural单位→pct×ATR, percent→pct, 取最宽. 用 cfg.ATRProtection.EffectivePercent 换算(夹取MinEffPct/MaxEffPct).
  - riskBasedPositionSize(): size = (equity×pct/100)/(effPct/100). <0.05%止损距离拒绝. 
  - riskBasedPositionSizeGuarded(): nil-safe 包装.
- trader/auto_trader_orders.go: 两处注入(long ~193, short ~392), 在 applyVolatilitySizing 之前. 传入 at.extractExecutionATR14(marketData).
- trader/risk_sizing_test.go: 6用例全过. 关键"config stop wider wins": AI2% vs 结构5%→用5%→仓位96(而非240).
- 编译通过, 测试通过. 未部署, 待用户确认部署参数(哪些交易员/百分比几).
- ATRProtection 在 StrategyConfig 顶层(cfg.ATRProtection), 不在 cfg.Protection 下.
- 未做: 系统提示词告知AI仓位自动反算(AI的position_size_usd会被覆盖,但仍需好的stop_loss); UI字段.

## 方案A 全部上线完成 — 2026-07-21
- 部署: 后端pid=1606949(含系统提示词风险反算notice). 前端dist已docker cp进nofx-frontend容器.
- 配置(策略级, 选B维持策略级不做per-trader): claude4.5% BN4.5% GPT6% Claude-R20%, risk_sizing_enabled=true.
- 系统提示词: engine_prompt.go 仓位指导段后加条件块(riskControl.RiskSizingEnabled时). 
- UI: web RiskControlEditor.tsx 加开关+百分比输入; types/strategy.ts加字段; i18n加riskSizing/riskPerTradePct键.
- 备份: /tmp/riskA_backup_20260721_060021/ (旧二进制x2 + 4策略原配置). 容器内 /usr/share/nginx/html_bak_20260721.

## 下一步: 资产分类底座 (方案4+时段管理的共同基础)
- OKX: /api/v5/public/instruments?instType=SWAP 字段 instCategory (1=crypto 3=stock 4=commodity). 已有 OKXInstrument结构+instrumentsCache (trader/okx/trader.go:34,86).
- 币安: fapi/v1/exchangeInfo, contractType(TRADIFI_PERPETUAL vs PERPETUAL) + underlyingType(KR_EQUITY/EQUITY/COMMODITY/COIN).
- 方案4回测结论: 止损确认周期最优=原生2×细(1h→30m: +10.81PnL 回撤88→77). 过细(4×)翻车. 股票/商品在细周期被whipsaw最惨→加密可细化,股票保持原生+时段管理.

## 资产分类底座 完成(未部署) — 2026-07-21
- market/asset_class.go: AssetClass(crypto/stock/commodity/unknown) + 缓存(base→class) + baseAsset()归一(去xyz:前缀/OKX BASE-QUOTE-SWAP/币安BASEUSDT). ClassifyAsset()未知默认crypto(24/7安全兜底,不改现有行为). IsScheduledAsset()=stock||commodity.
- market/asset_class_fetch.go: RefreshAssetClassesOKX(instCategory 3=stock 4=commodity 其余crypto) + RefreshAssetClassesBinance(underlyingType KR_EQUITY/EQUITY=stock COMMODITY=commodity 其余crypto) + RefreshAssetClasses(两家best-effort) + StartAssetClassRefresher(间隔+启动即刷,失败不fatal).
- main.go: market.StartAssetClassRefresher(time.Hour, logger.Infof) 挂在市场数据日志后.
- 实弹验证: OKX拉411个全对(131股 AAPL/TSLA/NVDA/SKHYNIX/SPCX; 8商品 XAU/XAG/CL/NG/XPD/XPT/XCU/BZ; 272 crypto). 币安本地451地理封锁,生产走BINANCE_PROXY_URL代理正常. 3个OKX交易员已覆盖.
- market/asset_class_test.go: baseAsset/ClassifyAsset默认/set+classify 全过.

## 方案4 实现(未部署,开关默认关) — 2026-07-21
- market/confirm_timeframe.go: ConfirmTimeframe(nativeTF, symbol) — 股/商品(及未知按crypto)返回native; crypto返回降一档finer,但仅当 native/finer比值≤2(干净2×). 低频段3×跳(15m→5m)不细化避免whipsaw. 精确贴合"2×最优、过细翻车"回测结论.
- store/strategy.go StructuralSLConfig 加 AssetAdaptiveConfirmTF bool(零值=关=native everywhere=现有行为).
- trader/structural_sl_guard.go: evaluateStructuralSLClose 里 GetKlines 前, ss.AssetAdaptiveConfirmTF开启时用 market.ConfirmTimeframe换确认TF. 只改测哪根收盘bar, boundary/backstop数学不变. trail路径复用同一bars(crypto更细=trail更灵敏), 双重gate(TrailEnabled+新flag).
- web: types/strategy.ts加asset_adaptive_confirm_tf; ProtectionEditor.tsx close-confirm勾选后加方案4开关(依赖close_confirm). tsc通过.
- market/confirm_timeframe_test.go: refine(1h→30m,4h→2h,15m保持,1m保持) + 资产感知(股保持/crypto细化/未知细化) 全过.
- 顺手修陈旧测试: structural_clamp_test/structural_sl_test 期望值从FallbackATRMul 3.0对齐到2.5(上个commit改的默认). trader包全绿.
- go build -o nofx . 通过. 未部署.

## 待用户决策
- 方案4 rollout范围: 4个交易员哪些开 asset_adaptive_confirm_tf? (需先开close_confirm; 当前4个都空structural_sl用默认, close_confirm状态待查)
- 时段管理产品参数: 哪些窗口(开盘/收盘前N分钟)? 限制开仓还是收紧止损? 各市场时段表(KRX/NYSE/COMEX+夏令时)自建.

## 时段管理 实现+上线 — 2026-07-21
- market/session.go: Market{Name,TZ(IANA),OpenHHMM,tradingWeekday}. 用 time.LoadLocation(IANA)自动处理夏令时(不硬编码EST/EDT). 美国至今仍切换夏令时(Sunshine Protection Act 2022过参议院但未立法);改永久夏令时只需更新tzdata,代码不动.
  - 市场: US_EQUITY(America/New_York 09:30 Mon-Fri), KRX(Asia/Seoul 09:00), TSE(Asia/Tokyo 09:00), COMMODITY(America/New_York 18:00 CME Globex重开 Sun-Fri).
  - stockMarketOverride: 非美本土股→ KRX(SKHYNIX/SKHY/SAMSUNG/HYUNDAI) TSE(SONY/SOFTBANK/KIOXIA). 其余股票默认US_EQUITY(含美股/ETF/ADR如TSM/ASML/ARM/NOK—它们在美盘跳空).
  - InPreOpenWindow(now,winMin): [open-win, open)本地时区判定, 分钟级DST正确. 跨午夜处理(winStart<0). 不建节假日表(节假日只多拦最多winMin新开仓,不碰持仓/平仓,可接受).
  - InPreOpenBlock(symbol,now,winMin): 顶层门. crypto/未知恒false不拦.
- store/strategy.go RiskControlConfig: SessionPreOpenBlockEnabled bool + SessionPreOpenWindowMinutes int(0→60默认).
- trader/auto_trader_orders.go: sessionPreOpenBlocked()辅助; 在 executeOpenLong/ShortWithRecord 冷却检查后调用, 命中则return err跳过开仓. 只拦新开仓, 不碰平仓/保护.
- web: types/strategy.ts加字段; RiskControlEditor.tsx加时段卡片(开关+窗口分钟输入5-240); i18n加sessionPreOpen*键(zh/en/es).
- 测试: market/session_test.go 全过(市场解析/美股窗口/夏令时vs标准时一致/crypto不拦).

## 部署完成 — 2026-07-21 (方案4 + 时段管理)
- 备份: /tmp/method4_session_backup_20260721_072915/ (nofx.old + data.db.bak 4.4GB + 4策略json). 容器内 /usr/share/nginx/html_bak_20260721_m4.
- DB配置(4策略全开): asset_adaptive_confirm_tf=true, session_pre_open_block_enabled=true, session_pre_open_window_minutes=60. close_confirm 4个本来就是true(前提满足).
- 二进制: 停pid1606949→cp→start.sh→新pid1618510. 启动日志: AssetClass classified 1203 assets(OKX+币安经代理都成功), 4交易员全auto-start, 无panic.
- 前端: docker cp dist + nginx reload.
- 陈旧测试修复(上个commit 1.5/2.5默认遗留): store/atr_protection_test.go backstop 4.5→2.5; trader/structural_clamp_test.go 300→250; trader/structural_sl_test.go 6%→5%. 全绿.

## 待办/可选
- 时段管理: SONY/SOFTBANK/KIOXIA归TSE、韩股归KRX是基于"无美股ADR"判断; 若OKX实际按美股ADR定价, 这几个应改回US_EQUITY. 需观察实际跳空行为确认. 节假日未建表(可接受).
- 未提交git(本session所有改动).

## 节假日日历 实现+上线 — 2026-07-21
- market/holidays.go:
  - 美国NYSE(calUS)+CME(calCME): 算法计算, 任意年份正确零维护. 固定日+第N个周几(nthWeekday)+周末顺延(usObserved: 六→前周五,日→次周一; 元旦六不顺延)+耶稣受难日(easterSunday Meeus/Jones/Butcher算法, Easter-2). 验证2026/2027全对(含2026 July4六→7/3五, 2027 July4日→7/5一, 2027 Christmas六→12/24五).
  - CME与NYSE差异: CME在MLK/总统日不休(仅缩短,晚间18:00重开仍在), 只休元旦/受难日/阵亡将士/六月节/独立日/劳动节/感恩节/圣诞.
  - 韩国KRX(calKRX)+日本TSE(calTSE): 含农历(春节/中秋/佛诞)+春分秋分(日本), 无法算法+无法联网核实 → 硬编码best-effort表(2026/2027). 表外年份降级为仅周末(HolidayCalendarKnownYear可查). 错误影响小: 至多多拦≤window或漏跳过一个本就休市日.
- market/session.go: Market加cal字段; isTradingDay(local)=交易周几 && 非节假日; InPreOpenWindow改用isTradingDay(跨午夜时查次日). 商品weekday改为 排除周五+周六(周五晚无18:00重开,休到周日).
- 测试: 美股感恩节/独立日观察日不拦、前一交易日拦; KRX春节不拦、2030降级周末仍拦; 商品周五晚不拦、周日晚拦. 全过.
- 部署: 备份/tmp/holidays_backup_20260721_074527. 停pid1618510→cp→新pid1620776. 无DB改动(纯代码). 1203分类正常,4交易员auto-start.

## 待核实(用户可年度校准)
- KRX/TSE 2026/2027农历&春分秋分日期是best-effort, 建议对照交易所官方日历核对(尤其Seollal/Chuseok/佛诞/春分秋分及周末顺延). 表在 market/holidays.go krxHolidaysByYear/tseHolidaysByYear.
