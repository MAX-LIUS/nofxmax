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

function kpi(v,l){return '<div class="kpi"><div class="v">'+v+'</div><div class="l">'+l+'</div></div>';}

// initial paint
load();
`
