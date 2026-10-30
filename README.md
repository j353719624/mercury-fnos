# MERCURY · 飞牛 fnOS 交换机管理

MERCURY 是运行在 fnOS 上的交换机集中管理应用，面向水星（MERCURY）交换机（SE106 Pro 等）。后端直连交换机私有 CGI 接口，前端提供设备发现、状态监控、配置下发与诊断运维。

## 功能

- 自动发现局域网内的交换机，点击设备登录后进入配置
- 基本信息、端口状态与统计、MAC 地址表
- 网络配置：DHCP / 静态地址 / DNS
- 端口配置、端口聚合、VLAN 扩展（MTU / 端口 VLAN / PVID）
- 端口镜像、端口隔离、环回保护、QoS（模式 / 优先级 / 限速 / 风暴抑制）
- 线缆检测、重启设备、固件升级
- 配置备份 / 还原、恢复出厂
- 诊断工具：Ping、DNS 查询、端口扫描、路由追踪、抓包分析、网络测速

## 架构

```
backend/
  meshlan_service.go   主服务：CGI 适配、路由、静态托管、诊断、测速
  advanced.go          交换机高级功能（统计/线缆/镜像/隔离/环回/QoS/VLAN/备份）
  setup.go             设备配置、首次配置、局域网发现
www/                   前端静态资源（构建产物）
package/
  manifest.template    FPK 清单
  cmd/                 fnOS 启停与生命周期钩子
  config/              权限与共享目录声明
  app-config/          运行时权限与共享目录
ui/                    fnOS 桌面入口与 index.cgi
build.sh               一键打包
```

## 打包

```bash
./build.sh                          # 使用 manifest 版本号
VERSION=V1.5.0-261026 ./build.sh    # 临时改版本号
SKIP_BUILD=1 ./build.sh             # 跳过编译，用 backend/prebuilt 里的 linux 二进制
```

产物：`dist/MERCURY-<版本>.fpk`

## 安装与使用

1. 在飞牛应用中心安装 `MERCURY-*.fpk`
2. 打开应用，首次进入会自动扫描局域网
3. 点击识别到的交换机，输入交换机账号密码登录；登录成功后设备会保存，下次直接打开
4. 设备详情页上方是设备配置页签，下方是「线缆检测 / 端口镜像 / 端口隔离 / 环回保护 / QoS / VLAN 扩展」等高级功能

设备配置保存在 NAS 本机 `/var/apps/mercury/var/devices.json`（0600），不会上传，也不会进入仓库。

## 交换机的通信方式

水星 / TP-Link 系交换机的旧版 Web 管理接口：

- 登录：`/logon.cgi`，密码经 `securityEncode` 处理
- 页面：读取 `*Rpm.htm` 中的 `*_ds` JS 对象
- 写入：多数配置通过 GET 请求下发，携带 `g_tid` token

后端对这些接口做了会话缓存与短时页面缓存，避免每次翻页都重新登录。

## 安全

- 仓库内不包含任何真实设备地址、账号或密码
- `config/devices.example.json` 仅作格式示例
- 运行配置只在 NAS 本机，随应用数据目录保留/删除
