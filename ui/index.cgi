#!/bin/bash

# 职责：1. 触发后端启动 2. 等待后端 Socket 就绪 3. 跳转到网关入口
set -u

APP_VAR_ROOT="${TRIM_APPDEST:-/var/apps/mercury}"
CMD_MAIN="${APP_VAR_ROOT}/cmd/main"
SOCKET_PATH="${APP_VAR_ROOT}/target/mercury"

check_ready() {
  [ -S "$SOCKET_PATH" ] && return 0 || return 1
}

# 触发启动
[ -x "$CMD_MAIN" ] && "$CMD_MAIN" start >/dev/null 2>&1

# 最多等待 10 秒
READY=0
for i in {1..10}; do
  if check_ready; then READY=1; break; fi
  sleep 1
done

if [ "$READY" = "1" ]; then
  echo "Status: 302 Found"
  echo "Location: /app/mercury/"
  echo
  exit 0
fi

echo "Content-Type: text/html; charset=utf-8"
echo
cat <<EOF
<!doctype html>
<html>
<head><meta charset="utf-8"><title>启动失败</title></head>
<body>
  <h3>MERCURY 服务启动失败</h3>
  <p>后端服务未就绪。由于 Socket 文件 ($SOCKET_PATH) 未生成。</p>
  <p>诊断：启动脚本 $CMD_MAIN ($( [ -x "$CMD_MAIN" ] && echo "权限正常" || echo "不可执行或缺失" ))</p>
  <p>请检查日志：/var/apps/mercury/var/meshlan.log</p>
</body>
</html>
EOF

