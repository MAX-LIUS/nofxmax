package api

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

// handleShadowMonitorJS serves the vanilla-JS logic for the monitor page.
func (s *Server) handleShadowMonitorJS(c *gin.Context) {
	c.Data(http.StatusOK, "application/javascript; charset=utf-8", []byte(shadowMonitorJS))
}

const shadowMonitorJS = `
// ---- auto-pick token ----
// Same origin as the main app, so its localStorage.auth_token is readable here.
// Priority: previously-saved sg_tok > the main app's live auth_token.
(function(){
  var saved=localStorage.getItem('sg_tok')||'';
  var appTok=localStorage.getItem('auth_token')||'';
  if(!saved && appTok){ localStorage.setItem('sg_tok', appTok); saved=appTok; }
  document.getElementById('tok').value=saved;
  document.getElementById('trader').value=localStorage.getItem('sg_trader')||'';
  if(appTok){ document.getElementById('status').innerHTML='<span class="pos">已自动读取主站登录 token</span>'; }
  else if(!saved){ document.getElementById('status').innerHTML='<span class="amb">未登录主站：请先在本浏览器登录主站，或手动粘贴 token</span>'; }
})();

async function loadRules(){
  const d=await api('/shadow-gates/stats'+qs());
  const rows=(d.rules||[]).sort((a,b)=>b.edge-a.edge);
  let matched=0,pending=0;
  rows.forEach(r=>{matched+=r.block_n+r.keep_n;pending+=r.pending_n;});
  const maxEdge=Math.max(0.001,...rows.map(r=>Math.abs(r.edge)));
  let h='<div class="card"><div class="kpis">'
    +kpi(rows.length,'候选规则数')
    +kpi(matched,'已配对(有真实PnL)')
    +kpi(pending,'待平仓回填')
    +kpi(rows.filter(r=>r.edge>0&&r.block_n>=5).length,'Edge&gt;0 且样本≥5')
    +'</div></div>';
  h+='<div class="card"><table><thead><tr>'
    +'<th>规则</th><th>会拦N</th><th>拦·总PnL</th><th>拦·均值</th><th>拦·胜率</th>'
    +'<th>会放N</th><th>放·均值</th><th>放·胜率</th><th>Edge(放-拦)</th><th>待回填</th></tr></thead><tbody>';
  if(!rows.length)h+='<tr><td colspan="10" class="mut">暂无数据。影子门刚部署，需等 AI 产生开仓决策后逐步累积。</td></tr>';
  rows.forEach(r=>{
    const ew=Math.round(100*Math.abs(r.edge)/maxEdge);
    const ec=r.edge>0?'var(--grn)':'var(--red)';
    h+='<tr><td>'+r.rule_name+'</td>'
      +'<td>'+r.block_n+'</td>'
      +'<td class="'+cls(r.block_pnl)+'">'+fmt(r.block_pnl)+'</td>'
      +'<td class="'+cls(r.block_avg)+'">'+fmt(r.block_avg,3)+'</td>'
      +'<td>'+r.block_win_pct.toFixed(1)+'%</td>'
      +'<td>'+r.keep_n+'</td>'
      +'<td class="'+cls(r.keep_avg)+'">'+fmt(r.keep_avg,3)+'</td>'
      +'<td>'+r.keep_win_pct.toFixed(1)+'%</td>'
      +'<td class="'+cls(r.edge)+'">'+fmt(r.edge,3)
        +'<div class="edge-bar"><div class="edge-fill" style="width:'+ew+'%;background:'+ec+'"></div></div></td>'
      +'<td class="mut">'+r.pending_n+'</td></tr>';
  });
  h+='</tbody></table></div>';
  document.getElementById('view').innerHTML=h;
}

async function loadFeed(){
  const d=await api('/shadow-gates/feed'+qs()+(qs()?'&':'?')+'limit=150');
  const v=d.verdicts||[];
  let h='<div class="card feed"><table><thead><tr>'
    +'<th>时间(UTC)</th><th>币种</th><th>方向</th><th>规则</th><th>判定</th>'
    +'<th>Regime</th><th>信心</th><th>live放行</th><th>细节</th></tr></thead><tbody>';
  if(!v.length)h+='<tr><td colspan="9" class="mut">暂无验证记录。</td></tr>';
  v.forEach(x=>{
    h+='<tr><td class="mut">'+x.time.replace('T',' ').replace('Z','')+'</td>'
      +'<td>'+x.symbol+'</td>'
      +'<td class="'+(x.side==='LONG'?'pos':'neg')+'">'+x.side+'</td>'
      +'<td>'+x.rule+'</td>'
      +'<td><span class="pill '+(x.would_block?'blk':'pas')+'">'+(x.would_block?'会拦':'放行')+'</span></td>'
      +'<td class="mut">'+x.regime+'</td>'
      +'<td>'+(x.confidence||'')+'</td>'
      +'<td>'+(x.live_allowed?'✓':'✗')+'</td>'
      +'<td class="mut" style="text-align:left">'+x.detail+'</td></tr>';
  });
  h+='</tbody></table></div>';
  document.getElementById('view').innerHTML=h;
}

// Investor view: what the LIVE book actually did (real PnL by regime/side buckets
// as seen through the shadow tags) + which rule, if it had enforced, would have
// most improved realized PnL. This is the "am I making money and where" lens.
async function loadInvestor(){
  const d=await api('/shadow-gates/stats'+qs());
  const rows=(d.rules||[]);
  // The counter-trend / downtrend-long rules carry the regime+side realized PnL.
  const keepByRule={}; rows.forEach(r=>keepByRule[r.rule_name]=r);
  // Best "if enforced" improvement = the blocked bucket's total PnL removed from book
  // (only meaningful directionally; true validation is offline shadowrank).
  const ranked=rows.filter(r=>r.block_n>=3).sort((a,b)=>a.block_avg-b.block_avg);
  const best=ranked[0];
  let bookPnL=0,bookN=0,bookWin=0;
  if(rows.length){ // reconstruct book from any one rule's block+keep buckets
    const r=rows[0];
    bookPnL=r.block_pnl+r.keep_pnl; bookN=r.block_n+r.keep_n;
    bookWin=Math.round(100*((r.block_win_pct*r.block_n+r.keep_win_pct*r.keep_n)/100)/(bookN||1));
  }
  let h='<div class="card"><div class="kpis">'
    +kpi('<span class="'+cls(bookPnL)+'">'+fmt(bookPnL)+'</span>','真实账本总PnL(已配对)')
    +kpi(bookN,'已平仓开仓数')
    +kpi(bookWin+'%','账本胜率')
    +kpi(best?best.rule_name:'—','当前最优候选门')
    +'</div></div>';
  if(best){
    const saved=(-best.block_pnl); // removing a negative-avg bucket adds this back
    h+='<div class="card"><b>若启用最优候选门 '+best.rule_name+'（仅方向性估计，非显著性结论）</b>'
      +'<div class="kpis" style="margin-top:12px">'
      +kpi('<span class="'+cls(saved)+'">'+fmt(saved)+'</span>','移除其拦截单后账本变化')
      +kpi(best.block_n,'会被拦掉的单数')
      +kpi('<span class="'+cls(best.block_avg)+'">'+fmt(best.block_avg,3)+'</span>','被拦单·均值PnL')
      +kpi('<span class="'+cls(best.keep_avg)+'">'+fmt(best.keep_avg,3)+'</span>','保留单·均值PnL')
      +'</div>'
      +'<div class="hint">⚠ 这是"历史配对"的方向性估计，不代表启用后真实收益（不建模仓位反馈）。'
      +'启用前必须用 shadowrank 过随机对照 + H1/H2 走查。</div></div>';
  }
  h+='<div class="card"><b>各候选门·真实拦截单盈亏（投资者视角：哪条门在替我省钱）</b>'
    +'<table style="margin-top:10px"><thead><tr><th>规则</th><th>会拦N</th><th>拦掉的单·总PnL</th>'
    +'<th>拦·均值</th><th>解读</th></tr></thead><tbody>';
  rows.sort((a,b)=>a.block_avg-b.block_avg).forEach(r=>{
    let verdict='<span class="mut">样本不足</span>';
    if(r.block_n>=5){verdict = r.block_avg<0 ? '<span class="pos">拦的是亏损单 ✓</span>' : '<span class="neg">拦到赢家 ✗</span>';}
    h+='<tr><td>'+r.rule_name+'</td><td>'+r.block_n+'</td>'
      +'<td class="'+cls(r.block_pnl)+'">'+fmt(r.block_pnl)+'</td>'
      +'<td class="'+cls(r.block_avg)+'">'+fmt(r.block_avg,3)+'</td>'
      +'<td style="text-align:left">'+verdict+'</td></tr>';
  });
  h+='</tbody></table></div>';
  document.getElementById('view').innerHTML=h;
}

// Virtual Trader Bench: each candidate gate is a "trader" that trades the SAME
// real book minus the opens it would have blocked, on shared capital, measured in
// realized R. Renders a leaderboard + overlaid equity curves so the effect reads
// like several paper accounts run by different rulebooks side by side.
// segBar renders the data-segment selector shared by bench + conf tabs.
// all=both, backfill=pre-deploy in-sample (price/trend gates only), forward=live OOS.
function segBar(){
  const b=(s,label,hint)=>'<button class="segbtn'+(SEG===s?' on':'')+'" data-s="'+s+'" onclick="setSeg(\''+s+'\')" title="'+hint+'">'+label+'</button>';
  return '<div class="card" style="padding:10px"><b>数据段：</b> '
    +b('all','全部','回填+前向混合，仅看整体方向')
    +b('backfill','回填(上线前·样本内)','老系统历史成交，样本大；价格/趋势门有效，信心门无效')
    +b('forward','前向(上线后·真OOS)','实时评估，最终裁判；样本仍在积累')
    +'<span class="mut" style="margin-left:10px;font-size:12px">回填=样本内(部分规则据此设计，偏乐观)；前向=样本外(含被实盘拒绝的意图评估)</span></div>';
}

async function loadBench(){
  const d=await api('/shadow-gates/bench?segment='+SEG);
  if(!d.ready){
    document.getElementById('view').innerHTML=segBar()+'<div class="card mut">'
      +(d.computing?'虚拟对战正在后台计算中（首次约需数十秒），稍后自动刷新…':('暂无结果'+(d.error?('：'+d.error):'')))+'</div>';
    return;
  }
  const r=d.result, base=r.baseline, traders=r.traders||[];
  const staleMin=Math.round((d.stale_sec||0)/60);
  const segNote = r.segment==='forward'?'（仅前向真样本外）':(r.segment==='backfill'?'（仅回填样本内）':'（回填+前向）');
  let h=segBar()+'<div class="card"><div class="kpis">'
    +kpi(r.book,'本段开仓数')
    +kpi(r.r_eligible,'有初始风险(可计R)')
    +kpi(r.forward_closed+' / '+r.backfill_closed,'前向 / 回填')
    +kpi('<span class="'+cls(base.final_r)+'">'+fmt(base.final_r,1)+'R</span>','基准(全收)累计R')
    +'</div><div class="hint">当前口径 '+segNote+'。R 单位=盈亏/初始风险。虚拟交易员只能"少开"其会拦的单（真实账本子集），'
    +'不能开系统没开的单；平仓沿用真实保护结果。R分位 P5/P50/P95='
    +fmt(r.r_p5,2)+' / '+fmt(r.r_p50,2)+' / '+fmt(r.r_p95,2)+'。计算于 '+staleMin+' 分钟前。</div></div>';

  // ---- overlaid equity curves ----
  h+='<div class="card"><b>累计R权益曲线（基准 vs 各虚拟交易员）</b>'+equitySVG(base,traders)+'</div>';

  // ---- leaderboard ----
  h+='<div class="card"><b>虚拟交易员计分榜（按累计R排序）</b>'
    +'<table style="margin-top:10px"><thead><tr>'
    +'<th>交易员(门)</th><th>开单</th><th>拦单</th><th>累计R</th><th>期望R</th>'
    +'<th>胜率</th><th>盈亏比</th><th>最大回撤R</th><th>CVaR95</th><th>Sortino</th>'
    +'<th>vs随机</th><th>H1</th><th>H2</th><th>Boot5%</th><th>裁定</th></tr></thead><tbody>';
  h+=benchRow(base,true);
  traders.forEach(t=>h+=benchRow(t,false));
  h+='</tbody></table>'
    +'<div class="hint">裁定=PASS 需同时满足：累计R&gt;基准 且 vs随机&gt;95 且 H1&gt;95 且 H2&gt;95 且 Boot5%&gt;0。'
    +(r.segment==='forward'?'当前为<b>前向真样本外</b>，样本少时结论不稳，随时间累积才可信。':'当前含<b>回填样本内</b>，是方向性参考，最终裁判看"前向"段。')
    +' 标 n/a 的信心门在含回填口径下不触发（回填 conf=0），须切到"前向"段看其真实效果。</div></div>';
  document.getElementById('view').innerHTML=h;
}

function benchRow(t,isBase){
  const passTag = isBase ? '<span class="mut">基准</span>'
    : (t.conf_invalid?'<span class="pill" style="opacity:.6">n/a本段</span>'
      : (t.pass?'<span class="pill pas">PASS</span>':'<span class="pill blk">未过</span>'));
  const nm = isBase ? '<b>基准·全收</b>' : (t.conf_invalid?('<span class="mut">'+t.name+' ⚠信心门</span>'):t.name);
  const inf = v => (v===null||v===undefined||!isFinite(v))?'∞':fmt(v,2);
  return '<tr'+(isBase?' style="background:rgba(255,255,255,.04)"':'')+'>'
    +'<td style="text-align:left">'+nm+'</td>'
    +'<td>'+t.n_trades+'</td>'
    +'<td class="mut">'+(t.n_blocked||0)+'</td>'
    +'<td class="'+cls(t.final_r)+'">'+fmt(t.final_r,1)+'</td>'
    +'<td class="'+cls(t.exp_r)+'">'+fmt(t.exp_r,3)+'</td>'
    +'<td>'+(t.win_rate||0).toFixed(1)+'%</td>'
    +'<td>'+inf(t.profit_factor)+'</td>'
    +'<td class="neg">-'+fmt(Math.abs(t.max_dd),1).replace('+','')+'</td>'
    +'<td class="'+cls(t.cvar95)+'">'+fmt(t.cvar95,2)+'</td>'
    +'<td>'+inf(t.sortino)+'</td>'
    +'<td class="'+(isBase?'mut':(t.vs_rand>95?'pos':''))+'">'+(isBase?'—':t.vs_rand.toFixed(0))+'</td>'
    +'<td class="'+(isBase?'mut':(t.h1>95?'pos':''))+'">'+(isBase?'—':t.h1.toFixed(0))+'</td>'
    +'<td class="'+(isBase?'mut':(t.h2>95?'pos':''))+'">'+(isBase?'—':t.h2.toFixed(0))+'</td>'
    +'<td class="'+(isBase?'mut':cls(t.ci_lo))+'">'+(isBase?'—':fmt(t.ci_lo,3))+'</td>'
    +'<td>'+passTag+'</td></tr>';
}

// Overlaid cumulative-R curves as inline SVG. Baseline is bold white; each trader
// a distinct hue. Curves are sampled to a fixed width so long books stay light.
function equitySVG(base, traders){
  const W=900,H=280,PADL=44,PADB=22,PADT=12;
  const series=[{name:'基准',eq:base.equity||[],color:'#e6e6e6',w:2.4}];
  const palette=['#4ade80','#f472b6','#60a5fa','#fbbf24','#a78bfa','#f87171','#34d399','#fb923c','#22d3ee','#e879f9'];
  traders.forEach((t,i)=>series.push({name:t.name,eq:t.equity||[],color:palette[i%palette.length],w:1.4}));
  let lo=0,hi=0,maxLen=0;
  series.forEach(s=>{s.eq.forEach(v=>{if(v<lo)lo=v;if(v>hi)hi=v;});if(s.eq.length>maxLen)maxLen=s.eq.length;});
  if(maxLen<2)return '<div class="mut" style="margin-top:8px">样本不足，无法绘制曲线。</div>';
  if(hi===lo)hi=lo+1;
  const px=i=>PADL+(W-PADL-6)*(i/(maxLen-1));
  const py=v=>PADT+(H-PADT-PADB)*(1-(v-lo)/(hi-lo));
  const path=eq=>{if(!eq.length)return '';let dd='M '+px(0)+' '+py(eq[0]);
    for(let i=1;i<eq.length;i++)dd+=' L '+px(i)+' '+py(eq[i]);return dd;};
  let svg='<svg viewBox="0 0 '+W+' '+H+'" style="width:100%;height:auto;margin-top:8px">';
  // zero line
  const zy=py(0);
  svg+='<line x1="'+PADL+'" y1="'+zy+'" x2="'+(W-6)+'" y2="'+zy+'" stroke="#555" stroke-dasharray="3 3"/>';
  svg+='<text x="4" y="'+(zy+4)+'" fill="#888" font-size="11">0R</text>';
  svg+='<text x="4" y="'+(py(hi)+4)+'" fill="#888" font-size="11">'+fmt(hi,0)+'</text>';
  svg+='<text x="4" y="'+(py(lo)+4)+'" fill="#888" font-size="11">'+fmt(lo,0)+'</text>';
  series.forEach(s=>{svg+='<path d="'+path(s.eq)+'" fill="none" stroke="'+s.color+'" stroke-width="'+s.w+'" opacity="0.9"/>';});
  svg+='</svg>';
  // legend
  let leg='<div style="display:flex;flex-wrap:wrap;gap:10px;margin-top:6px;font-size:11px">';
  series.forEach(s=>{const last=s.eq.length?s.eq[s.eq.length-1]:0;
    leg+='<span style="display:inline-flex;align-items:center;gap:4px">'
      +'<span style="width:12px;height:3px;background:'+s.color+';display:inline-block"></span>'
      +s.name+' <span class="'+cls(last)+'">'+fmt(last,1)+'R</span></span>';});
  leg+='</div>';
  return svg+leg;
}

// Confidence layers: (1) global calibration — does higher AI confidence actually
// earn higher R? (2) each gate × confidence layer — in which confidence band does
// a gate actually block the losers (Lift>0)? Both come from /shadow-gates/bench
// (conf_layers + gate_conf), computed over the full R-eligible book (conf 100%).
async function loadConf(){
  const d=await api('/shadow-gates/bench?segment='+SEG);
  if(!d.ready){
    document.getElementById('view').innerHTML=segBar()+'<div class="card mut">'
      +(d.computing?'后台计算中，稍后自动刷新…':('暂无结果'+(d.error?('：'+d.error):'')))+'</div>';
    return;
  }
  const r=d.result, layers=r.conf_layers||[], gc=r.gate_conf||[];
  const segNote = r.segment==='forward'?'（前向真样本外）':(r.segment==='backfill'?'（回填样本内）':'（回填+前向）');
  // ---- global calibration ----
  let h=segBar()+'<div class="card"><b>AI 信心校准 '+segNote+'（本段 R-eligible，信心取自开仓决策，回填/前向均为真值）</b>'
    +'<table style="margin-top:10px"><thead><tr><th>信心层</th><th>N</th><th>期望R</th><th>胜率</th><th>累计R</th><th>解读</th></tr></thead><tbody>';
  if(!layers.length)h+='<tr><td colspan="6" class="mut">暂无数据</td></tr>';
  // find best-expR layer to comment on monotonicity
  layers.forEach(l=>{
    let note='';
    if(l.exp_r<-0.1)note='<span class="neg">期望明显为负</span>';
    else if(l.exp_r>0)note='<span class="pos">期望为正</span>';
    else note='<span class="mut">接近盈亏平衡</span>';
    h+='<tr><td>'+l.name+'</td><td>'+l.n_trades+'</td>'
      +'<td class="'+cls(l.exp_r)+'">'+fmt(l.exp_r,3)+'</td>'
      +'<td>'+l.win_rate.toFixed(1)+'%</td>'
      +'<td class="'+cls(l.sum_r)+'">'+fmt(l.sum_r,1)+'</td>'
      +'<td style="text-align:left">'+note+'</td></tr>';
  });
  h+='</tbody></table><div class="hint">若期望R不随信心单调上升，说明 AI 的高信心并不更可靠——这正是门控要纠正的地方。</div></div>';

  // ---- gate × confidence layer ----
  h+='<div class="card"><b>各门 × 信心层：在哪个信心带上真正拦对了亏损单</b>'
    +'<table style="margin-top:10px"><thead><tr>'
    +'<th>门</th><th>信心层</th><th>拦N</th><th>拦·均值R</th><th>留·均值R</th>'
    +'<th>Edge(留-拦)</th><th>Lift(留-层基线)</th><th>解读</th></tr></thead><tbody>';
  if(!gc.length)h+='<tr><td colspan="8" class="mut">暂无数据</td></tr>';
  gc.forEach(g=>{
    (g.cells||[]).forEach((c,i)=>{
      let note='<span class="mut">样本少</span>';
      if(c.block_n>=10){
        if(c.lift>0.05)note='<span class="pos">此层纠错强 ✓</span>';
        else if(c.lift<-0.05)note='<span class="neg">此层拦错(拦到赢家)</span>';
        else note='<span class="mut">此层影响中性</span>';
      }
      h+='<tr>'+(i===0?'<td rowspan="'+g.cells.length+'"><b>'+g.name+'</b></td>':'')
        +'<td>'+c.layer+'</td><td>'+c.block_n+'</td>'
        +'<td class="'+cls(c.block_exp_r)+'">'+fmt(c.block_exp_r,3)+'</td>'
        +'<td class="'+cls(c.keep_exp_r)+'">'+fmt(c.keep_exp_r,3)+'</td>'
        +'<td class="'+cls(c.edge)+'">'+fmt(c.edge,3)+'</td>'
        +'<td class="'+cls(c.lift)+'" style="font-weight:600">'+fmt(c.lift,3)+'</td>'
        +'<td style="text-align:left">'+note+'</td></tr>';
    });
  });
  h+='</tbody></table>'
    +'<div class="hint">拦·均值R 越负=拦掉的越是亏损单；Lift&gt;0=该门在此信心层能把留下单的期望抬到层基线之上（真正创造价值的地方）。'
    +'这是样本内回填口径，chop 类在回填期信心可能不全。</div></div>';
  document.getElementById('view').innerHTML=h;
}

function kpi(v,l){return '<div class="kpi"><div class="v">'+v+'</div><div class="l">'+l+'</div></div>';}

// initial paint
load();
`
