// 给诊断工具页补「使用说明」标签页（结构与测速页一致：胶囊标签 + 说明卡片）
const fs = require('fs');
const path = require('path');
const DIR = path.join(__dirname, 'compat-go');

const CSS = `
  /* 使用说明 */
  .tabs { display: flex; gap: 8px; margin: 0 0 16px; }
  .tabs button {
    height: 36px; padding: 0 16px; border-radius: 999px; cursor: pointer;
    border: 1px solid var(--field-border); background: var(--card); color: var(--muted);
    font-size: 13px; font-family: inherit; transition: all .16s ease;
  }
  .tabs button:hover { color: var(--primary); border-color: var(--primary); }
  .tabs button.on { background: var(--primary); border-color: var(--primary); color: #fff; }
  .doc h3 {
    margin: 26px 0 10px; padding-bottom: 8px; font-size: 14px; font-weight: 700;
    border-bottom: 1px solid var(--border);
  }
  .doc h3:first-of-type { margin-top: 8px; }
  .doc p { margin: 0 0 10px; font-size: 13.5px; line-height: 1.9; color: var(--text); }
  .doc ul, .doc ol { margin: 0 0 10px; padding-left: 22px; font-size: 13.5px; line-height: 1.9; }
  .doc li { margin: 2px 0; }
  .doc code {
    padding: 1px 5px; border-radius: 4px; background: var(--out-bg);
    border: 1px solid var(--out-border); font-family: var(--mono); font-size: 12px;
  }
  .doc .note { margin-top: 18px; }
  .doc-wrap { max-width: 1040px; margin: 0 auto; }
`;

const TABS = `
    <div class="tabs" id="tabs">
      <button class="on" data-p="main">%TAB1%</button>
      <button data-p="doc">使用说明</button>
    </div>
`;

const TOOLS = [
  {
    file: 'tools-ping.html',
    title: 'Ping 检测',
    tab: 'Ping 检测',
    doc: `
      <p class="lead">本工具从飞牛主机向目标发起 TCP 探测，用握手是否成功和耗时判断目标是否可达、往返时延有多大。</p>
      <h3>工作原理</h3>
      <ul>
        <li>后端按顺序尝试与目标建立 TCP 连接，探测端口为 <code>80</code>、<code>443</code>、<code>22</code>、<code>5666</code>、<code>5201</code>。</li>
        <li>任意一个端口握手成功即判定目标可达，耗时记为本次延迟。</li>
        <li>不使用 ICMP，所以对端禁止 ping 时依然可以测；但也因此反映的是 TCP 建连时间，和系统 ping 的数值会略有差异。</li>
      </ul>
      <h3>准备工作</h3>
      <ul>
        <li>目标需与飞牛在同一网络或可路由到达。</li>
        <li>目标上至少有一个常见服务端口处于监听状态，否则会被判为不可达。</li>
      </ul>
      <h3>使用步骤</h3>
      <ol>
        <li>输入目标 IP 或域名，例如 <code>192.168.110.1</code>、<code>www.baidu.com</code>。</li>
        <li>填写探测次数（1-20 次），默认 4 次。</li>
        <li>点击「开始检测」，等待结果输出。</li>
      </ol>
      <h3>结果解读</h3>
      <ul>
        <li>每一行是一次探测：成功时给出耗时（毫秒），失败时标注不可达。</li>
        <li>末尾给出成功次数、平均/最小/最大时延。</li>
        <li>偶发几百毫秒的抖动通常来自对端排队或无线链路；持续超时先检查 IP、网段和目标服务。</li>
      </ul>
      <div class="note">这是「TCP 可达性探测」，不是 ICMP ping。若目标只开放 8080 等非常规端口，结果会显示不可达，属正常。</div>`
  },
  {
    file: 'tools-dns.html',
    title: 'DNS 查询',
    tab: 'DNS 查询',
    doc: `
      <p class="lead">本工具用飞牛主机上配置的 DNS 服务器解析域名，返回 A / AAAA / CNAME / MX / NS 记录，用来判断域名解析是否正常。</p>
      <h3>工作原理</h3>
      <ul>
        <li>查询由飞牛后端发起，走系统默认 DNS（<code>/etc/resolv.conf</code>）。</li>
        <li>依次查询 A（IPv4）、AAAA（IPv6）、CNAME（别名）、MX（邮件）、NS（权威服务器）。</li>
      </ul>
      <h3>使用步骤</h3>
      <ol>
        <li>输入域名，例如 <code>www.baidu.com</code>，不需要带 <code>http://</code>。</li>
        <li>点击「查询记录」。</li>
      </ol>
      <h3>结果解读</h3>
      <ul>
        <li>有 A 记录说明域名能解析到 IPv4 地址，这是浏览器访问的前提。</li>
        <li>返回 <code>NXDOMAIN</code> 表示域名不存在；返回空结果多为 DNS 服务器不可达或被拦截。</li>
        <li>如果这里能解析、但设备上打不开，问题通常在网关、代理或防火墙，而不是 DNS。</li>
      </ul>
      <div class="note">本工具只做查询，不修改飞牛或任何设备的 DNS 设置。</div>`
  },
  {
    file: 'tools-portscan.html',
    title: '端口扫描',
    tab: '端口扫描',
    doc: `
      <p class="lead">本工具探测目标主机上哪些 TCP 端口处于开放状态，用于排查“服务装了但连不上”这类问题。</p>
      <h3>工作原理</h3>
      <ul>
        <li>对目标 IP 的指定端口区间逐个发起 TCP 连接，单端口超时 700 毫秒，最多 20 个并发。</li>
        <li>单次最多扫描 1024 个端口；握手成功即认为该端口开放。</li>
      </ul>
      <h3>使用步骤</h3>
      <ol>
        <li>输入目标主机 IP。</li>
        <li>填写端口范围，格式如 <code>1-1024</code>、<code>22</code>、<code>80,443,8000-8100</code>。</li>
        <li>点击「开始扫描」，结束后会列出所有开放端口。</li>
      </ol>
      <h3>结果解读</h3>
      <ul>
        <li>只列出成功建连的端口；未列出的可能是关闭、被防火墙丢弃，或响应太慢超过超时。</li>
        <li>对本地设备扫描 1-1024 通常几秒到十几秒，跨网段会更慢。</li>
      </ul>
      <div class="note">请只扫描自己拥有或已获授权的设备。对他人主机做端口扫描可能违反规定。</div>`
  },
  {
    file: 'tools-traceroute.html',
    title: '路由追踪',
    tab: '路由追踪',
    doc: `
      <p class="lead">本工具追踪从飞牛到目标主机的整条路径，逐跳列出中间经过的路由设备，用于定位“哪一跳开始不通”。</p>
      <h3>工作原理</h3>
      <ul>
        <li>调用飞牛系统的 <code>traceroute</code>，逐跳增加 TTL 发送探测包。</li>
        <li>关闭反向域名解析（<code>-n</code>），直接显示 IP，速度快且不受 DNS 影响。</li>
        <li>每跳默认探测 1 次，单跳超时 1 秒。</li>
      </ul>
      <h3>使用步骤</h3>
      <ol>
        <li>输入目标 IP 或域名。</li>
        <li>设置最大跳数（1-30），默认 20。</li>
        <li>点击「开始追踪」。</li>
      </ol>
      <h3>结果解读</h3>
      <ul>
        <li>每行是一跳，显示跳数、IP 和耗时。</li>
        <li>出现 <code>*</code> 表示该跳没有回应探测包，很常见（路由器默认不回），不代表链路中断。</li>
        <li>如果从某一跳之后全部超时而目标始终没出现，问题多半出在该跳之后。</li>
      </ul>
      <div class="note">追踪只能看到去程路径；回程路径可能不同，这是正常的网络现象。</div>`
  },
  {
    file: 'tools-capture.html',
    title: '抓包分析',
    tab: '抓包分析',
    doc: `
      <p class="lead">本工具在飞牛网卡上实时抓取网络报文并生成 pcap 文件，下载后可用 Wireshark 打开分析。</p>
      <h3>工作原理</h3>
      <ul>
        <li>后端直接打开网卡的原始套接字（AF_PACKET），按标准 pcap 格式写入，不依赖 tcpdump。</li>
        <li>抓包单次最长 30 秒，且同一时间只允许一个抓包任务。</li>
        <li>可选抓包长度（snaplen）：取较小值可减小文件体积，但会截断报文尾部。</li>
      </ul>
      <h3>使用步骤</h3>
      <ol>
        <li>选择网卡（默认为承载应用流量的那块）。</li>
        <li>设置抓包时长与抓包长度。</li>
        <li>复现问题后点击「开始抓包」，结束后点击「下载 pcap 文件」。</li>
        <li>用 Wireshark 打开，按 IP、端口或协议过滤。</li>
      </ol>
      <h3>结果解读与建议</h3>
      <ul>
        <li>抓包期间流量越大，文件越大；排查具体问题时尽量缩短时长。</li>
        <li>常见用途：确认报文是否到达、看 TCP 重传与握手、判断 VLAN 标签是否正确。</li>
        <li>pcap 中可能包含明文账号密码等内容，请妥善保管，排查完及时删除。</li>
      </ul>
      <div class="note">抓包会占用一定 CPU 和磁盘写入，非排查期间建议不要长时间开启。</div>`
  }
];

const JS = `
  (function () {
    var tabs = document.getElementById('tabs');
    var main = document.getElementById('main');
    var doc = document.getElementById('doc');
    if (!tabs || !main || !doc) return;
    tabs.addEventListener('click', function (ev) {
      var b = ev.target.closest('button');
      if (!b) return;
      var showDoc = b.dataset.p === 'doc';
      main.style.display = showDoc ? 'none' : '';
      doc.style.display = showDoc ? '' : 'none';
      Array.prototype.forEach.call(tabs.children, function (x) {
        x.classList.toggle('on', x === b);
      });
      window.scrollTo(0, 0);
    });
  })();
`;

let done = 0;
for (const t of TOOLS) {
  const p = path.join(DIR, t.file);
  let s = fs.readFileSync(p, 'utf8');
  if (s.indexOf('使用说明') >= 0) { console.log('skip', t.file); continue; }

  // 1) 样式
  s = s.replace('</style>', CSS + '</style>');

  // 2) 标签栏 + 主面板 id
  s = s.replace('<section class="panel">', TABS.replace('%TAB1%', t.tab) + '\n  <section class="panel" id="main">');

  // 3) 说明面板
  const docPanel = `
  <section class="panel" id="doc" style="display:none">
    <div class="panel-head">
      <h2>${t.title}使用说明</h2>
      <p>${t.tab} 的工作原理、使用步骤与结果解读</p>
    </div>
    <div class="doc doc-wrap">${t.doc}
    </div>
  </section>
`;
  // 主面板结束后插入
  const idx = s.indexOf('</section>');
  const after = s.indexOf('</section>', idx + 1);
  if (idx < 0) { console.log('marker missing', t.file); continue; }
  // 找到主面板（第一次出现的 </section> 就是它）
  s = s.slice(0, idx + '</section>'.length) + docPanel + s.slice(idx + '</section>'.length);

  // 4) 切换脚本
  s = s.replace('</body>', '<script>' + JS + '</script>\n</body>');
  fs.writeFileSync(p, s);
  done++;
  console.log('docs added', t.file);
}
console.log('DONE', done);
