// 生成三个独立的诊断工具页面，尺寸/位置对齐「网络测速」页
const fs = require('fs');
const path = require('path');

const OUT = path.join(__dirname, 'compat-go');

const CSS = `
  :root {
    --bg: #ffffff;
    --card: #ffffff;
    --border: #e4e7ed;
    --text: #303133;
    --muted: #909399;
    --dim: #a8abb2;
    --field: #ffffff;
    --field-border: #dcdfe6;
    --out-bg: #fafafa;
    --out-border: #e4e7ed;
    --out-text: #303133;
    --icon-tile: rgba(9, 9, 11, .08);
    --icon-color: #09090b;
    --primary: #409eff;
    --success: #67c23a;
    --danger: #f56c6c;
    --mono: ui-monospace, SFMono-Regular, Menlo, Consolas, "Liberation Mono", monospace;
  }
  html[data-theme="dark"] {
    --bg: #121212;
    --card: #141414;
    --border: #1f2937;
    --text: #e5eaf3;
    --muted: #a3a6ad;
    --dim: #8d9095;
    --field: #1d1e1f;
    --field-border: #4c4d4f;
    --out-bg: #1d1e1f;
    --out-border: #363637;
    --out-text: #cfd3dc;
    --icon-tile: #409eff;
    --icon-color: #ffffff;
  }
  * { box-sizing: border-box; }
  html, body { height: 100%; }
  body {
    margin: 0;
    background: var(--bg);
    color: var(--text);
    font: 16px/1.5 -apple-system, BlinkMacSystemFont, "Segoe UI", "PingFang SC", "Microsoft YaHei", sans-serif;
    -webkit-font-smoothing: antialiased;
  }
  .page { max-width: 1600px; margin: 0 auto; padding: 20px 28px 40px; }

  /* 与测速页一致：gap 12 / py 12 / mb 16 / 底部分隔线 */
  .head {
    display: flex; align-items: center; gap: 12px;
    padding: 12px 0; margin-bottom: 16px;
    border-bottom: 1px solid var(--border);
  }
  .back {
    width: 40px; height: 40px; border-radius: 50%; flex: none;
    border: 1px solid var(--field-border); background: var(--card);
    color: var(--muted); display: grid; place-items: center;
    text-decoration: none; transition: all .18s ease;
  }
  .back:hover { color: var(--primary); border-color: var(--primary); }
  .back svg { width: 14px; height: 14px; }
  .head-icon {
    width: 36px; height: 36px; border-radius: 8px; flex: none;
    display: grid; place-items: center;
    background: var(--icon-tile); color: var(--icon-color);
  }
  .head-icon svg { width: 18px; height: 18px; }
  .head-text { min-width: 0; flex: 1; }
  .head-text h1 { margin: 0; font-size: 16px; font-weight: 600; line-height: 24px; }
  .head-text p { margin: 2px 0 0; font-size: 12px; color: var(--muted); line-height: 16.5px; }

  /* 内容卡片：圆角 16 / padding 32 / 1px 边框 */
  .panel {
    border: 1px solid var(--border); border-radius: 16px;
    background: var(--card); padding: 32px; min-height: 485px;
  }
  .panel-head { text-align: center; margin-bottom: 28px; }
  .panel-head h2 { margin: 0; font-size: 18px; font-weight: 700; letter-spacing: .3px; }
  .panel-head p { margin: 4px 0 0; font-size: 12px; color: var(--muted); }

  .form { display: flex; gap: 12px; align-items: flex-end; justify-content: center; flex-wrap: wrap; max-width: 760px; margin: 0 auto; }
  .field { display: flex; flex-direction: column; gap: 6px; flex: 1 1 260px; min-width: 180px; }
  .field.narrow { flex: 0 0 130px; min-width: 110px; }
  .field label { font-size: 13px; color: var(--dim); }
  input {
    width: 100%; height: 40px; padding: 0 12px; border-radius: 6px; outline: none;
    background: var(--field); border: 1px solid var(--field-border);
    color: var(--text); font-size: 14px; font-family: inherit; transition: border-color .18s ease;
  }
  select {
    width: 100%; height: 40px; padding: 0 12px; border-radius: 6px; outline: none;
    background: var(--field); border: 1px solid var(--field-border);
    color: var(--text); font-size: 14px; font-family: inherit;
  }
  input::placeholder { color: var(--dim); }
  input:focus { border-color: var(--primary); }
  button.primary {
    height: 40px; padding: 0 20px; border-radius: 6px; border: none; cursor: pointer;
    background: var(--primary); color: #fff; font-size: 14px; font-weight: 500; font-family: inherit;
    transition: filter .18s ease;
  }
  button.primary:hover { filter: brightness(1.08); }
  button.primary:disabled { opacity: .6; cursor: not-allowed; filter: none; }

  .out {
    margin: 24px auto 0; max-width: 920px; padding: 14px 16px; min-height: 168px;
    border-radius: 8px; background: var(--out-bg); border: 1px solid var(--out-border);
    color: var(--out-text); font-family: var(--mono); font-size: 12.5px; line-height: 1.75;
    white-space: pre-wrap; word-break: break-all;
  }
  .out.err { color: var(--danger); }

  .foot { margin: 12px auto 0; max-width: 920px; display: flex; justify-content: center; align-items: center; gap: 8px; }
  .badge { display: none; align-items: center; gap: 6px; font-size: 12px; }
  .badge.show { display: inline-flex; }
  .badge.ok { color: var(--success); }
  .badge.err { color: var(--danger); }
  .spinner {
    width: 13px; height: 13px; border-radius: 50%; border: 2px solid var(--field-border);
    border-top-color: var(--primary); animation: spin .8s linear infinite; display: none;
  }
  .spinner.show { display: inline-block; }
  @keyframes spin { to { transform: rotate(360deg); } }

  @media (max-width: 640px) {
    .page { padding: 16px 16px 32px; }
    .panel { padding: 20px 16px 24px; }
  }
`;

const JS_COMMON = `
  // 跟随应用主题：应用用 vueuse 的 color-scheme 记录 light/dark/auto
  (function () {
    var mode = 'auto';
    try { mode = localStorage.getItem('vueuse-color-scheme') || 'auto'; } catch (e) {}
    if (mode !== 'light' && mode !== 'dark') {
      mode = window.matchMedia && window.matchMedia('(prefers-color-scheme: dark)').matches ? 'dark' : 'light';
    }
    document.documentElement.setAttribute('data-theme', mode);
  })();

  // 定点返回诊断工具页，不用 history.back()，避免跳到别的页面
  document.getElementById('back').addEventListener('click', function (ev) {
    ev.preventDefault();
    if (window.parent !== window && window.parent.__mercuryCloseTool) {
      window.parent.__mercuryCloseTool();
      return;
    }
    location.href = './#/diagnostic-tools';
  });

  var PREFIX = (function () {
    var p = location.pathname;
    var i = p.lastIndexOf('/');
    return i >= 0 ? p.slice(0, i) : '';
  })();

  var out = document.getElementById('out');
  var statusEl = document.getElementById('status');
  var spin = document.getElementById('spin');
  var runBtn = document.getElementById('run');

  function setStatus(ok) {
    statusEl.className = 'badge show ' + (ok ? 'ok' : 'err');
    statusEl.textContent = ok ? '完成' : '失败';
  }
  function fail(msg) {
    out.className = 'out err';
    out.textContent = msg;
    statusEl.className = 'badge';
  }
  function setBusy(on) {
    spin.className = 'spinner' + (on ? ' show' : '');
    runBtn.disabled = on;
    runBtn.textContent = on ? '执行中…' : runBtn.dataset.label;
  }
  async function run(url, tip) {
    setBusy(true);
    out.className = 'out';
    out.textContent = tip;
    statusEl.className = 'badge';
    try {
      var r = await fetch(PREFIX + url, { cache: 'no-store' });
      var j = await r.json();
      if (j.code !== 200 || !j.data) throw new Error(j.message || '请求失败');
      out.textContent = j.data.output || '(无输出)';
      setStatus(!!j.data.ok);
    } catch (e) {
      out.className = 'out err';
      out.textContent = '出错：' + (e.message || e);
      setStatus(false);
    } finally {
      setBusy(false);
    }
  }
  runBtn.addEventListener('click', onRun);
  document.querySelectorAll('input').forEach(function (el) {
    el.addEventListener('keydown', function (ev) { if (ev.key === 'Enter') onRun(); });
  });
`;

const ICONS = {
  ping: '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M22 12h-4l-3 9L9 3l-3 9H2"/></svg>',
  dns: '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><circle cx="11" cy="11" r="7"/><path d="M21 21l-4.3-4.3"/></svg>',
  portscan: '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><rect x="3" y="3" width="7" height="7" rx="1.5"/><rect x="14" y="3" width="7" height="7" rx="1.5"/><rect x="3" y="14" width="7" height="7" rx="1.5"/><rect x="14" y="14" width="7" height="7" rx="1.5"/></svg>'
};

const TOOLS = [
  {
    file: 'tools-ping.html',
    key: 'ping',
    title: 'Ping 检测',
    desc: '检测目标主机的可达性与往返时延',
    form: `
      <div class="field">
        <label>目标主机（IP 或域名）</label>
        <input id="host" placeholder="例如 192.168.110.1 或 www.baidu.com" autofocus />
      </div>
      <div class="field narrow">
        <label>探测次数</label>
        <input id="count" type="number" min="1" max="20" value="4" />
      </div>
      <button class="primary" id="run" data-label="开始检测">开始检测</button>`,
    onRun: `
  function onRun() {
    var host = document.getElementById('host').value.trim();
    if (!host) return fail('请输入目标主机');
    var count = document.getElementById('count').value || 4;
    run('/api/tools/ping?host=' + encodeURIComponent(host) + '&count=' + encodeURIComponent(count), '正在探测 ' + host + ' …');
  }`
  },
  {
    file: 'tools-dns.html',
    key: 'dns',
    title: 'DNS 查询',
    desc: '解析域名的 A / AAAA / CNAME / MX / NS 记录',
    form: `
      <div class="field">
        <label>域名</label>
        <input id="name" placeholder="例如 www.baidu.com" autofocus />
      </div>
      <button class="primary" id="run" data-label="查询记录">查询记录</button>`,
    onRun: `
  function onRun() {
    var name = document.getElementById('name').value.trim();
    if (!name) return fail('请输入域名');
    run('/api/tools/dns?name=' + encodeURIComponent(name), '正在解析 ' + name + ' …');
  }`
  },
  {
    file: 'tools-portscan.html',
    key: 'portscan',
    title: '端口扫描',
    desc: '探测目标主机开放的服务端口',
    form: `
      <div class="field">
        <label>目标主机</label>
        <input id="host" placeholder="例如 192.168.110.1" autofocus />
      </div>
      <div class="field narrow">
        <label>端口范围</label>
        <input id="ports" value="1-1024" />
      </div>
      <button class="primary" id="run" data-label="开始扫描">开始扫描</button>`,
    onRun: `
  function onRun() {
    var host = document.getElementById('host').value.trim();
    if (!host) return fail('请输入目标主机');
    var ports = document.getElementById('ports').value.trim() || '1-1024';
    run('/api/tools/portscan?host=' + encodeURIComponent(host) + '&ports=' + encodeURIComponent(ports), '正在扫描 ' + host + ' …');
  }`
  },
  {
    file: 'tools-traceroute.html',
    key: 'ping',
    title: '路由追踪',
    desc: '追踪数据包到目标主机经过的网络节点',
    form: `
      <div class="field">
        <label>目标主机（IP 或域名）</label>
        <input id="host" placeholder="例如 www.baidu.com" autofocus />
      </div>
      <div class="field narrow">
        <label>最大跳数</label>
        <input id="max" type="number" min="1" max="30" value="20" />
      </div>
      <button class="primary" id="run" data-label="开始追踪">开始追踪</button>`,
    onRun: `
  function onRun() {
    var host = document.getElementById('host').value.trim();
    if (!host) return fail('请输入目标主机');
    var max = document.getElementById('max').value || 20;
    run('/api/tools/traceroute?host=' + encodeURIComponent(host) + '&max=' + encodeURIComponent(max), '正在追踪到 ' + host + ' 的路径…');
  }`
  },
  {
    file: 'tools-capture.html',
    key: 'portscan',
    title: '抓包分析',
    desc: '在线抓取网络报文并下载分析',
    form: `
      <div class="field">
        <label>网卡</label>
        <select id="iface"></select>
      </div>
      <div class="field narrow">
        <label>时长（秒）</label>
        <input id="secs" type="number" min="1" max="30" value="10" />
      </div>
      <button class="primary" id="run" data-label="开始抓包">开始抓包</button>`,
    onInit: `
  (async function () {
    var sel = document.getElementById('iface');
    try {
      var r = await fetch(PREFIX + '/api/tools/ifaces', { cache: 'no-store' });
      var j = await r.json();
      var list = (j.data && j.data.list) || [];
      sel.innerHTML = '';
      list.filter(function (i) { return !i.loopback && !/^veth/.test(i.name) && i.name !== 'ovs-system' && i.name !== 'lo'; }).forEach(function (i) {
        var o = document.createElement('option');
        o.value = i.name;
        o.textContent = i.name + (i.addrs && i.addrs.length ? '   ' + i.addrs[0] : '');
        sel.appendChild(o);
      });
    } catch (e) { fail('读取网卡列表失败：' + e.message); }
  })();`,
    onRun: `
  async function onRun() {
    var iface = document.getElementById('iface').value;
    var secs = parseInt(document.getElementById('secs').value || '10', 10);
    if (!iface) return fail('请选择网卡');
    if (!(secs >= 1 && secs <= 30)) return fail('时长需在 1-30 秒之间');
    setBusy(true);
    out.className = 'out';
    out.textContent = '正在 ' + iface + ' 上抓包 ' + secs + ' 秒…';
    statusEl.className = 'badge';
    try {
      var r = await fetch(PREFIX + '/api/tools/capture?iface=' + encodeURIComponent(iface) + '&seconds=' + secs, { method: 'POST' });
      if (!r.ok) {
        var t = await r.text(), m = '';
        try { m = JSON.parse(t).message; } catch (e) {}
        throw new Error(m || ('HTTP ' + r.status));
      }
      var blob = await r.blob();
      var pk = r.headers.get('X-Packet-Count') || '?';
      var by = parseInt(r.headers.get('X-Byte-Count') || '0', 10);
      var stamp = new Date().toISOString().replace(/[-:]/g, '').replace('T', '-').slice(0, 15);
      var name = 'capture-' + iface + '-' + stamp + '.pcap';
      var a = document.createElement('a');
      a.href = URL.createObjectURL(blob);
      a.download = name;
      document.body.appendChild(a); a.click(); a.remove();
      var dl = document.getElementById('dl');
      if (dl) { dl.href = URL.createObjectURL(blob); dl.download = name; document.getElementById('dlwrap').style.display = 'flex'; }
      setTimeout(function () { URL.revokeObjectURL(a.href); }, 8000);
      out.textContent = '抓包完成\\n网卡：' + iface + '\\n时长：' + secs + ' 秒\\n报文：' + pk + ' 个\\n大小：' + (by / 1024).toFixed(1) + ' KB\\n已开始下载：' + name;
      setStatus(true);
    } catch (e) {
      out.className = 'out err';
      out.textContent = '出错：' + (e.message || e);
      setStatus(false);
    } finally {
      setBusy(false);
    }
  }`
  }
];

for (const tool of TOOLS) {
  const html = `<!DOCTYPE html>
<html lang="zh-CN">
<head>
<meta charset="utf-8" />
<meta name="viewport" content="width=device-width, initial-scale=1" />
<title>${tool.title} · MERCURY</title>
<style>${CSS}</style>
</head>
<body>
<div class="page">
  <header class="head">
    <a class="back" id="back" href="#" aria-label="返回">
      <svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2" stroke-linecap="round" stroke-linejoin="round"><path d="M15 18l-6-6 6-6"/></svg>
    </a>
    <div class="head-icon">${ICONS[tool.key]}</div>
    <div class="head-text">
      <h1>${tool.title}</h1>
      <p>${tool.desc}</p>
    </div>
  </header>

  <section class="panel">
    <div class="panel-head">
      <h2>${tool.title}</h2>
      <p>${tool.desc}</p>
    </div>

    <div class="form">${tool.form}
    </div>

    <pre class="out" id="out">就绪。输入目标后点击按钮开始。</pre>
    <div class="foot" id="dlwrap" style="display:none"><a id="dl" class="primary" style="text-decoration:none" href="#">下载 pcap 文件</a></div>
    <div class="foot">
      <span class="spinner" id="spin"></span>
      <span class="badge" id="status"></span>
    </div>
  </section>
</div>

<script>${JS_COMMON}${tool.onRun}${tool.onInit || ''}
</script>
</body>
</html>
`;
  fs.writeFileSync(path.join(OUT, tool.file), html, 'utf8');
  console.log('wrote', tool.file, html.length);
}
