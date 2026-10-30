// 把「使用说明」页的抬头改成与测速说明一致（左对齐大标题 + 导语），并给每个工具加一张示意图
const fs = require('fs');
const path = require('path');
const DIR = path.join(__dirname, 'compat-go');

const CSS_NEW = `
  /* 使用说明 */
  .tabs { display: flex; gap: 8px; margin: 0 0 16px; }
  .tabs button {
    height: 36px; padding: 0 16px; border-radius: 999px; cursor: pointer;
    border: 1px solid var(--field-border); background: var(--card); color: var(--muted);
    font-size: 13px; font-family: inherit; transition: all .16s ease;
  }
  .tabs button:hover { color: var(--primary); border-color: var(--primary); }
  .tabs button.on { background: var(--primary); border-color: var(--primary); color: #fff; }
  .doc-title { margin: 0 0 10px; font-size: 26px; font-weight: 700; letter-spacing: .2px; line-height: 1.3; }
  .doc-lead { margin: 0 0 22px; font-size: 13.5px; line-height: 1.9; color: var(--text); }
  .doc-fig {
    margin: 0 0 26px; padding: 18px; border-radius: 12px;
    background: #f7f8fa; border: 1px solid #e8eaf0; text-align: center;
  }
  .doc-fig svg { width: 100%; max-width: 720px; height: auto; display: block; margin: 0 auto; }
  html[data-theme="dark"] .doc-fig { background: #f2f3f5; border-color: #dfe2e8; }
  .doc h3 {
    margin: 26px 0 10px; padding-bottom: 8px; font-size: 14px; font-weight: 700;
    border-bottom: 1px solid var(--border);
  }
  .doc h3:first-of-type { margin-top: 0; }
  .doc p { margin: 0 0 10px; font-size: 13.5px; line-height: 1.9; color: var(--text); }
  .doc ul, .doc ol { margin: 0 0 10px; padding-left: 22px; font-size: 13.5px; line-height: 1.9; }
  .doc li { margin: 2px 0; }
  .doc code {
    padding: 1px 5px; border-radius: 4px; background: var(--out-bg);
    border: 1px solid var(--out-border); font-family: var(--mono); font-size: 12px;
  }
  .doc .note { margin-top: 18px; }
`;

const SVG_STYLE = `<defs><marker id="ar" viewBox="0 0 10 10" refX="9" refY="5" markerWidth="7" markerHeight="7" orient="auto"><path d="M0,0 L10,5 L0,10 z" fill="#4f6ef7"/></marker><marker id="ag" viewBox="0 0 10 10" refX="9" refY="5" markerWidth="7" markerHeight="7" orient="auto"><path d="M0,0 L10,5 L0,10 z" fill="#3fae5a"/></marker></defs>`;

const box = (x, y, w, h, title, sub, fill) => `
  <rect x="${x}" y="${y}" width="${w}" height="${h}" rx="10" fill="${fill || '#ffffff'}" stroke="#dfe3ec"/>
  <text x="${x + w / 2}" y="${y + (sub ? h / 2 - 4 : h / 2 + 5)}" text-anchor="middle" font-size="14" font-weight="600" fill="#303133">${title}</text>
  ${sub ? `<text x="${x + w / 2}" y="${y + h / 2 + 16}" text-anchor="middle" font-size="11" fill="#909399">${sub}</text>` : ''}`;

const FIGS = {
  ping: `<svg viewBox="0 0 720 220" xmlns="http://www.w3.org/2000/svg">${SVG_STYLE}
  <text x="360" y="26" text-anchor="middle" font-size="12" fill="#606266">TCP 可达性探测：飞牛 → 目标主机的常见服务端口</text>
  ${box(60, 70, 170, 74, '飞牛主机', '探测发起端')}
  ${box(490, 70, 170, 74, '目标主机', 'IP / 域名')}
  <line x1="238" y1="96" x2="482" y2="96" stroke="#4f6ef7" stroke-width="2" marker-end="url(#ar)"/>
  <text x="360" y="88" text-anchor="middle" font-size="11" fill="#4f6ef7">TCP SYN</text>
  <line x1="482" y1="122" x2="238" y2="122" stroke="#3fae5a" stroke-width="2" marker-end="url(#ag)"/>
  <text x="360" y="140" text-anchor="middle" font-size="11" fill="#3fae5a">SYN / ACK → 记录握手耗时</text>
  <text x="360" y="182" text-anchor="middle" font-size="11" fill="#909399">探测端口：80 · 443 · 22 · 5666 · 5201（任一成功即可达）</text>
</svg>`,
  dns: `<svg viewBox="0 0 720 230" xmlns="http://www.w3.org/2000/svg">${SVG_STYLE}
  <text x="360" y="26" text-anchor="middle" font-size="12" fill="#606266">域名解析链路：飞牛 → 系统 DNS → 记录</text>
  ${box(40, 80, 150, 70, '飞牛主机', '发起查询')}
  ${box(285, 80, 150, 70, 'DNS 服务器', 'resolv.conf')}
  ${box(530, 56, 150, 120, '解析结果', 'A / AAAA / CNAME', '#f2f6ff')}
  <line x1="198" y1="105" x2="277" y2="105" stroke="#4f6ef7" stroke-width="2" marker-end="url(#ar)"/>
  <text x="237" y="97" text-anchor="middle" font-size="11" fill="#4f6ef7">查询</text>
  <line x1="277" y1="128" x2="198" y2="128" stroke="#3fae5a" stroke-width="2" marker-end="url(#ag)"/>
  <text x="237" y="146" text-anchor="middle" font-size="11" fill="#3fae5a">应答</text>
  <line x1="443" y1="115" x2="522" y2="115" stroke="#4f6ef7" stroke-width="2" marker-end="url(#ar)"/>
  <text x="483" y="107" text-anchor="middle" font-size="11" fill="#4f6ef7">记录</text>
  <text x="605" y="150" text-anchor="middle" font-size="11" fill="#909399">MX / NS</text>
  <text x="360" y="205" text-anchor="middle" font-size="11" fill="#909399">只做查询，不修改任何 DNS 设置</text>
</svg>`,
  portscan: `<svg viewBox="0 0 720 230" xmlns="http://www.w3.org/2000/svg">${SVG_STYLE}
  <text x="360" y="26" text-anchor="middle" font-size="12" fill="#606266">端口探测：对目标逐个尝试 TCP 连接（20 并发 · 单口 700ms）</text>
  ${box(40, 78, 160, 74, '飞牛主机', '扫描发起端')}
  ${box(500, 46, 180, 140, '目标主机', '', '#f7fbff')}
  <line x1="208" y1="115" x2="492" y2="80" stroke="#c0c4cc" stroke-width="1.6" marker-end="url(#ar)"/>
  <line x1="208" y1="115" x2="492" y2="106" stroke="#3fae5a" stroke-width="2" marker-end="url(#ag)"/>
  <line x1="208" y1="115" x2="492" y2="132" stroke="#3fae5a" stroke-width="2" marker-end="url(#ag)"/>
  <line x1="208" y1="115" x2="492" y2="158" stroke="#c0c4cc" stroke-width="1.6" marker-end="url(#ar)"/>
  <text x="512" y="70" font-size="11" fill="#909399">21 关闭</text>
  <text x="512" y="101" font-size="11" fill="#3fae5a">22 开放</text>
  <text x="512" y="127" font-size="11" fill="#3fae5a">80 开放</text>
  <text x="512" y="153" font-size="11" fill="#909399">443 关闭</text>
  <text x="360" y="212" text-anchor="middle" font-size="11" fill="#909399">只列出握手成功的端口</text>
</svg>`,
  traceroute: `<svg viewBox="0 0 720 210" xmlns="http://www.w3.org/2000/svg">${SVG_STYLE}
  <text x="360" y="26" text-anchor="middle" font-size="12" fill="#606266">逐跳追踪：TTL 从 1 递增，直到到达目标</text>
  ${box(20, 76, 140, 66, '飞牛主机', '起点')}
  ${box(205, 76, 130, 66, '路由 1', 'TTL=1')}
  ${box(375, 76, 130, 66, '路由 2', 'TTL=2')}
  ${box(545, 76, 140, 66, '目标主机', 'TTL=n')}
  <line x1="164" y1="109" x2="197" y2="109" stroke="#4f6ef7" stroke-width="2" marker-end="url(#ar)"/>
  <line x1="339" y1="109" x2="367" y2="109" stroke="#4f6ef7" stroke-width="2" marker-end="url(#ar)"/>
  <line x1="509" y1="109" x2="537" y2="109" stroke="#4f6ef7" stroke-width="2" marker-end="url(#ar)"/>
  <text x="360" y="172" text-anchor="middle" font-size="11" fill="#909399">每跳显示 IP 与耗时；显示 * 表示该跳不回探测包</text>
</svg>`,
  capture: `<svg viewBox="0 0 720 230" xmlns="http://www.w3.org/2000/svg">${SVG_STYLE}
  <text x="360" y="26" text-anchor="middle" font-size="12" fill="#606266">抓包链路：网卡原始套接字 → pcap 文件 → Wireshark</text>
  ${box(30, 76, 150, 78, '网卡', 'eth0 / enp*')}
  ${box(240, 76, 160, 78, '原始套接字', 'AF_PACKET')}
  ${box(460, 76, 140, 78, 'pcap 文件', '单次 ≤ 30s', '#f2f6ff')}
  <line x1="188" y1="115" x2="232" y2="115" stroke="#4f6ef7" stroke-width="2" marker-end="url(#ar)"/>
  <line x1="408" y1="115" x2="452" y2="115" stroke="#4f6ef7" stroke-width="2" marker-end="url(#ar)"/>
  <line x1="608" y1="115" x2="650" y2="115" stroke="#3fae5a" stroke-width="2" marker-end="url(#ag)"/>
  <text x="676" y="119" font-size="11" fill="#3fae5a">Wireshark</text>
  <text x="360" y="192" text-anchor="middle" font-size="11" fill="#909399">截断长度（snaplen）越小，文件越小</text>
</svg>`
};

const DOCS = [
  ['tools-ping.html', 'ping', 'Ping 检测', `本工具用于检测从飞牛主机到目标主机的可达性与往返时延。通过 TCP 握手探测判断目标是否在线，可用于快速定位“这台设备到底通不通”的问题。`, `
      <p>本工具从飞牛主机向目标发起 TCP 探测，用握手是否成功和耗时判断目标是否可达、往返时延有多大。</p>
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
      <div class="note">这是「TCP 可达性探测」，不是 ICMP ping。若目标只开放 8080 等非常规端口，结果会显示不可达，属正常。</div>`],
  ['tools-dns.html', 'dns', 'DNS 查询', `本工具用于验证域名能否被正确解析。通过飞牛的 DNS 服务器查询 A / AAAA / CNAME / MX / NS 记录，快速区分是“域名解析问题”还是“网络连通问题”。`, `
      <p>本工具用飞牛主机上配置的 DNS 服务器解析域名，返回 A / AAAA / CNAME / MX / NS 记录，用来判断域名解析是否正常。</p>
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
      <div class="note">本工具只做查询，不修改飞牛或任何设备的 DNS 设置。</div>`],
  ['tools-portscan.html', 'portscan', '端口扫描', `本工具用于查看目标主机上哪些 TCP 端口是开放的。适合排查“服务已经安装但外部连不上”“端口有没有被占用”这类问题。`, `
      <p>本工具探测目标主机上哪些 TCP 端口处于开放状态，用于排查“服务装了但连不上”这类问题。</p>
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
      <div class="note">请只扫描自己拥有或已获授权的设备。对他人主机做端口扫描可能违反规定。</div>`],
  ['tools-traceroute.html', 'traceroute', '路由追踪', `本工具用于查看数据包从飞牛到目标经过了哪些路由设备。当目标是“能通但很慢”或“中间某一跳之后断了”时，先看这张路径图。`, `
      <p>本工具追踪从飞牛到目标主机的整条路径，逐跳列出中间经过的路由设备，用于定位“哪一跳开始不通”。</p>
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
      <div class="note">追踪只能看到去程路径；回程路径可能不同，这是正常的网络现象。</div>`],
  ['tools-capture.html', 'capture', '抓包分析', `本工具用于在飞牛网卡上实时抓取报文并导出 pcap。适合排查“报文到底有没有到达”“有没有重传”“VLAN 标签对不对”这类只能看包才能确认的问题。`, `
      <p>本工具在飞牛网卡上实时抓取网络报文并生成 pcap 文件，下载后可用 Wireshark 打开分析。</p>
      <h3>工作原理</h3>
      <ul>
        <li>后端直接打开网卡的原始套接字（<code>AF_PACKET</code>），按标准 pcap 格式写入，不依赖 tcpdump。</li>
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
      <div class="note">抓包会占用一定 CPU 和磁盘写入，非排查期间建议不要长时间开启。</div>`],
];

for (const [file, key, title, lead, body] of DOCS) {
  const p = path.join(DIR, file);
  let s = fs.readFileSync(p, 'utf8');

  // 1) 样式整块替换
  s = s.replace(/  \/\* 使用说明 \*\/[\s\S]*?\.doc-wrap \{ max-width: 1040px; margin: 0 auto; \}\n/, CSS_NEW);

  // 2) 说明页整块替换（抬头改为大标题 + 导语 + 示意图）
  const docSection = `  <section class="panel" id="doc" style="display:none">
    <div class="doc">
      <h2 class="doc-title">${title}使用说明</h2>
      <p class="doc-lead">${lead}</p>
      <div class="doc-fig">${FIGS[key]}</div>
${body}
    </div>
  </section>
`;
  s = s.replace(/\n  <section class="panel" id="doc"[\s\S]*?\n  <\/section>\n/, '\n' + docSection);

  fs.writeFileSync(p, s);
  console.log('doc restyled', file);
}
console.log('DOCS_V2_OK');
