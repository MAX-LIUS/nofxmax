package api

import (
	"net/http"

	"github.com/gin-gonic/gin"
)

// handleShadowMonitorPage serves a self-contained monitoring window for the
// shadow entry-gate experiment. Public route (no auth) — the page itself asks
// for the JWT (reused from the main app's localStorage.auth_token) and calls the
// protected /shadow-gates/* endpoints. Intentionally dependency-free (vanilla JS)
// so it is a throwaway diagnostic surface, not part of the React build.
func (s *Server) handleShadowMonitorPage(c *gin.Context) {
	c.Data(http.StatusOK, "text/html; charset=utf-8", []byte(shadowMonitorHTML))
}

const shadowMonitorHTML = `<!DOCTYPE html>
<html lang="zh">
<head>
<meta charset="utf-8"/>
<meta name="viewport" content="width=device-width, initial-scale=1"/>
<title>影子门监控 · Shadow Gate Monitor</title>
<style>
 :root{--bg:#0d1117;--card:#161b22;--bd:#30363d;--fg:#e6edf3;--mut:#8b949e;--grn:#3fb950;--red:#f85149;--amb:#d29922;--acc:#58a6ff}
 *{box-sizing:border-box}
 body{margin:0;background:var(--bg);color:var(--fg);font:14px/1.5 -apple-system,Segoe UI,Roboto,Helvetica,Arial,"PingFang SC","Microsoft YaHei",sans-serif}
 header{padding:14px 20px;border-bottom:1px solid var(--bd);display:flex;gap:14px;align-items:center;flex-wrap:wrap}
 h1{font-size:16px;margin:0;font-weight:600}
 .mut{color:var(--mut)}
 input,select,button{background:var(--card);color:var(--fg);border:1px solid var(--bd);border-radius:6px;padding:6px 10px;font-size:13px}
 button{cursor:pointer}
 button:hover{border-color:var(--acc)}
 .wrap{padding:16px 20px;max-width:1400px;margin:0 auto}
 .tabs{display:flex;gap:8px;margin-bottom:14px;flex-wrap:wrap}
 .tab{padding:7px 14px;border:1px solid var(--bd);border-radius:6px;cursor:pointer;background:var(--card)}
 .tab.on{border-color:var(--acc);color:var(--acc)}
 .card{background:var(--card);border:1px solid var(--bd);border-radius:10px;padding:16px;margin-bottom:16px}
 .kpis{display:grid;grid-template-columns:repeat(auto-fit,minmax(150px,1fr));gap:12px}
 .kpi{background:var(--bg);border:1px solid var(--bd);border-radius:8px;padding:12px}
 .kpi .v{font-size:22px;font-weight:700}
 .kpi .l{color:var(--mut);font-size:12px;margin-top:2px}
 table{width:100%;border-collapse:collapse;font-size:13px}
 th,td{text-align:right;padding:8px 10px;border-bottom:1px solid var(--bd);white-space:nowrap}
 th:first-child,td:first-child{text-align:left}
 th{color:var(--mut);font-weight:600;position:sticky;top:0;background:var(--card)}
 tr:hover td{background:rgba(88,166,255,.06)}
 .pos{color:var(--grn)}.neg{color:var(--red)}.amb{color:var(--amb)}
 .pill{display:inline-block;padding:1px 7px;border-radius:20px;font-size:11px;border:1px solid var(--bd)}
 .blk{background:rgba(248,81,73,.12);color:var(--red);border-color:transparent}
 .pas{background:rgba(63,185,80,.12);color:var(--grn);border-color:transparent}
 .feed{max-height:560px;overflow:auto}
 .hint{font-size:12px;color:var(--mut);margin-top:8px}
 .edge-bar{height:6px;border-radius:3px;background:var(--bd);overflow:hidden;margin-top:4px}
 .edge-fill{height:100%}
</style>
</head>
<body>
<header>
  <h1>🛡 影子门监控 <span class="mut">Shadow Gate Monitor</span></h1>
  <input id="email" placeholder="主站邮箱" style="min-width:170px" autocomplete="username"/>
  <input id="pw" type="password" placeholder="密码" style="min-width:130px" autocomplete="current-password"/>
  <button onclick="doLogin()">登录取 token</button>
  <input id="trader" placeholder="trader_id (留空=全部)" style="min-width:170px"/>
  <button onclick="saveTok()">刷新</button>
  <label class="mut"><input type="checkbox" id="auto" onchange="toggleAuto()"/> 自动30s</label>
  <span id="status" class="mut"></span>
  <details style="width:100%;margin-top:4px"><summary class="mut" style="cursor:pointer;font-size:12px">或手动粘贴 token</summary>
   <input id="tok" placeholder="粘贴 JWT" style="min-width:420px;margin-top:6px"/></details>
</header>
<div class="wrap">
  <div class="tabs">
    <div class="tab on" data-t="rules" onclick="tab('rules')">规则计分板</div>
    <div class="tab" data-t="feed" onclick="tab('feed')">实时拦截流</div>
    <div class="tab" data-t="investor" onclick="tab('investor')">投资者实战视角</div>
  </div>
  <div id="view"></div>
  <div class="hint">
   说明：影子门只观察不拦截，真实交易照旧。规则计分板按"会拦的单实际盈亏(blkAvg) vs 会放的单(keepAvg)"打分，
   Edge = keepAvg − blkAvg；Edge&gt;0 表示该门拦掉的确实是更差的单。PnL 来自真实持仓平仓后回填。
   统计显著性(随机对照/H1H2)需样本攒够后用 shadowrank 工具离线判定，此页只做实时观测。
  </div>
</div>
<script>
const $=s=>document.querySelector(s);
let CUR='rules', timer=null;
function saveTok(){const t=$('#tok').value.trim();if(t)localStorage.setItem('sg_tok',t);localStorage.setItem('sg_trader',$('#trader').value.trim());load();}
async function doLogin(){
  const email=$('#email').value.trim(), pw=$('#pw').value;
  if(!email||!pw){$('#status').innerHTML='<span class="amb">请输入邮箱和密码</span>';return;}
  $('#status').textContent='登录中…';
  try{
    const r=await fetch('/api/login',{method:'POST',headers:{'Content-Type':'application/json'},body:JSON.stringify({email:email,password:pw})});
    const j=await r.json();
    if(!r.ok||!j.token){throw new Error(j.error||('HTTP '+r.status));}
    localStorage.setItem('sg_tok',j.token);
    if($('#tok'))$('#tok').value=j.token;
    localStorage.setItem('sg_trader',$('#trader').value.trim());
    $('#status').innerHTML='<span class="pos">登录成功，token 已保存</span>';
    load();
  }catch(e){$('#status').innerHTML='<span class="neg">登录失败: '+e.message+'</span>';}
}
function tab(t){CUR=t;document.querySelectorAll('.tab').forEach(e=>e.classList.toggle('on',e.dataset.t===t));load();}
function toggleAuto(){if($('#auto').checked){timer=setInterval(load,30000);}else{clearInterval(timer);}}
function hdr(){return {'Authorization':'Bearer '+(localStorage.getItem('sg_tok')||'')};}
function qs(){const t=localStorage.getItem('sg_trader')||'';return t?('?trader_id='+encodeURIComponent(t)):'';}
function cls(v){return v>0?'pos':(v<0?'neg':'');}
function fmt(v,d){return (v>0?'+':'')+Number(v).toFixed(d===undefined?2:d);}
async function api(path){const r=await fetch('/api'+path,{headers:hdr()});if(!r.ok)throw new Error(r.status+' '+r.statusText);return r.json();}
async function load(){
  $('#status').textContent='加载中…';
  try{
    if(CUR==='rules')await loadRules();
    else if(CUR==='feed')await loadFeed();
    else await loadInvestor();
    $('#status').textContent='更新于 '+new Date().toLocaleTimeString();
  }catch(e){$('#status').innerHTML='<span class="neg">错误: '+e.message+'（检查 token）</span>';}
}
</script>
<script src="/api/shadow-gates/monitor.js"></script>
</body>
</html>`
