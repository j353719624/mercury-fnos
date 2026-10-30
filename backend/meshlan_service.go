package main

import (
	"context"
	"encoding/binary"
		"encoding/json"
	"fmt"
	"io"
	"math"
	"net"
	"net/http"
	"net/url"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"sync"
	"syscall"
	"time"
)

const socketEnv = "MESHLAN_HTTP_SOCKET_PATH"

// probePort 可由环境变量覆盖，便于同机并存多个实例
func probePort() int {
	if v := os.Getenv("MESHLAN_PROBE_PORT"); v != "" {
		if n, err := strconv.Atoi(v); err == nil {
			return n
		}
	}
	return 55556
}

// appName 从可执行文件路径推导应用名（.../@appcenter/<name>/server/<bin>）
func appName() string {
	exe, err := os.Executable()
	if err != nil {
		return "mercury"
	}
	serverDir := filepath.Dir(exe)
	appDir := filepath.Dir(serverDir)
	return filepath.Base(appDir)
}

func appHome() string { return filepath.Join("/var/apps", appName()) }

/* ------------------------------------------------------------------ */
/* 水星交换机客户端                                                     */
/* ------------------------------------------------------------------ */

const encodeKey = "RDpbLfCPsJZ7fiv"
const encodeCharset = "yLwVl0zKqws7LgKPRQ84Mdt708T1qQ3Ha7xv3H7NyU84p21BriUWBU43odz3iP4rBL3cD02KZciXTysVXiV8ngg6vL48rPJyAUw0HurW20xqxv9aYb4M9wK1Ae0wlro510qXeU07kV57fQMc8L6aLgMLwygtc0F10a0Dg70TOoouyFhdysuRMO51yY5ZlOZZLEal1h0t9YQW0Ko7oBwmCAHoic4HYbUyVeU3sfQ1xtXcPcf1aT303wAQhv66qzW"

// encodePassword 复刻固件里的 securityEncode：明文与固定密钥逐位异或后查表。
func encodePassword(plain string) string {
	n := len(plain)
	if len(encodeKey) > n {
		n = len(encodeKey)
	}
	out := make([]byte, 0, n)
	for i := 0; i < n; i++ {
		l, m := byte(187), byte(187)
		if i >= len(plain) {
			m = encodeKey[i]
		} else if i >= len(encodeKey) {
			l = plain[i]
		} else {
			l = plain[i]
			m = encodeKey[i]
		}
		out = append(out, encodeCharset[int(l^m)%len(encodeCharset)])
	}
	return string(out)
}

var speedLabels = map[int]string{
	0: "断开", 1: "自动", 2: "10M半双工", 3: "10M全双工", 4: "100M半双工",
	5: "100M全双工", 6: "1000M全双工", 7: "2.5G全双工", 8: "10G全双工",
}

type mercury struct {
	host     string
	port     int
	username string
	password string
	cookie   string
	token    string
	http     *http.Client
}

func newMercury(host string, port int, user, pass string) *mercury {
	return &mercury{
		host: host, port: port, username: user, password: pass,
		cookie: cachedCookie(host, port),
		http:   &http.Client{Timeout: 8 * time.Second},
	}
}

func (m *mercury) base() string {
	return "http://" + net.JoinHostPort(m.host, strconv.Itoa(m.port))
}

func (m *mercury) get(path string) (string, error) {
	req, err := http.NewRequest("GET", m.base()+path, nil)
	if err != nil {
		return "", err
	}
	req.Header.Set("User-Agent", "meshlan-compat/1.0")
	if m.cookie != "" {
		req.Header.Set("Cookie", m.cookie)
	}
	res, err := m.http.Do(req)
	if err != nil {
		return "", err
	}
	defer res.Body.Close()
	if sc := res.Header.Get("Set-Cookie"); sc != "" {
		if pair := strings.SplitN(sc, ";", 2)[0]; strings.Contains(pair, "=") {
			m.cookie = pair
		}
	}
	b, _ := io.ReadAll(res.Body)
	return string(b), nil
}

func (m *mercury) login() error {
	body := url.Values{}
	body.Set("username", m.username)
	body.Set("password", encodePassword(m.password))
	body.Set("isIe", "false")
	body.Set("code", "")
	body.Set("logon", "登录")

	req, err := http.NewRequest("POST", m.base()+"/logon.cgi", strings.NewReader(body.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("User-Agent", "meshlan-compat/1.0")
	res, err := m.http.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	if sc := res.Header.Get("Set-Cookie"); sc != "" {
		if pair := strings.SplitN(sc, ";", 2)[0]; strings.Contains(pair, "=") {
			m.cookie = pair
		}
	}
	b, _ := io.ReadAll(res.Body)
	match := regexp.MustCompile(`var\s+logonInfo\s*=\s*new\s+Array\(\s*(\d+)`).FindStringSubmatch(string(b))
	if len(match) < 2 || match[1] != "0" {
		return fmt.Errorf("登录交换机失败")
	}
	m.token = ""
	storeCookie(m.host, m.port, m.cookie)
	return nil
}

// loginAs 用指定账号口令重新建立会话，用于验证改密结果。
func (m *mercury) loginAs(user, password string) error {
	prevUser, prevPass := m.username, m.password
	prevCookie, prevToken := m.cookie, m.token
	m.username, m.password = user, password
	m.cookie, m.token = "", ""
	err := m.login()
	if err != nil {
		m.username, m.password = prevUser, prevPass
		m.cookie, m.token = prevCookie, prevToken
	}
	return err
}

// changePassword 调用固件的账号设置接口；前端只提交新密码，旧密码取自设备配置。
func (m *mercury) changePassword(user, oldPwd, newPwd string) error {
	if err := m.ensure(); err != nil {
		return err
	}
	v := url.Values{}
	v.Set("txt_username", user)
	v.Set("txt_oldpwd", encodePassword(oldPwd))
	v.Set("txt_userpwd", encodePassword(newPwd))
	v.Set("txt_confirmpwd", encodePassword(newPwd))
	v.Set("token", m.token)
	page, err := m.get("/usr_account_set.cgi?" + v.Encode())
	m.token = ""
	if err != nil {
		return err
	}
	// 固件把失败原因放在 tip 里，成功时为空
	re := regexp.MustCompile(`var\s+tip\s*=\s*"([^"]*)"`)
	if mm := re.FindStringSubmatch(page); len(mm) > 1 && mm[1] != "" {
		return fmt.Errorf("%s", mm[1])
	}
	return nil
}

// updateStoredPassword 把改后的口令写回设备配置文件，避免管理面失联。
func updateStoredPassword(mac, password string) error {
	for _, p := range configPaths() {
		b, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		var list []deviceConf
		if err := json.Unmarshal(b, &list); err != nil {
			continue
		}
		changed := false
		for i := range list {
			if strings.EqualFold(list[i].Mac, mac) {
				list[i].Password = password
				changed = true
			}
		}
		if !changed {
			continue
		}
		nb, err := json.MarshalIndent(list, "", "  ")
		if err != nil {
			return err
		}
		return os.WriteFile(p, nb, 0o644)
	}
	return fmt.Errorf("未找到设备配置文件，新密码未保存")
}

func (m *mercury) ensure() error {
	if m.cookie == "" {
		if err := m.login(); err != nil {
			return err
		}
	}
	if m.token == "" {
		page, err := m.get("/")
		if err != nil {
			return err
		}
		m.token = extractNumber(page, "g_tid")
		if m.token == "" {
			m.cookie = ""
			if err := m.login(); err != nil {
				return err
			}
			page, err = m.get("/")
			if err != nil {
				return err
			}
			m.token = extractNumber(page, "g_tid")
		}
	}
	return nil
}

// 会话与页面缓存：交换机每次请求都要登录一次太慢，
// 这里缓存已登录的 Cookie，并给读页面一个很短的缓存（1.5 秒），写操作会清掉。
var sessMu sync.Mutex
var sessCookie = map[string]string{}

var pageCacheMu sync.Mutex
var pageCache = map[string]pageEntry{}

type pageEntry struct {
	body string
	at   time.Time
}

const pageTTL = 1500 * time.Millisecond

func deviceKey(host string, port int) string {
	return host + ":" + strconv.Itoa(port)
}

func cachedCookie(host string, port int) string {
	sessMu.Lock()
	defer sessMu.Unlock()
	return sessCookie[deviceKey(host, port)]
}

func storeCookie(host string, port int, cookie string) {
	if cookie == "" {
		return
	}
	sessMu.Lock()
	sessCookie[deviceKey(host, port)] = cookie
	sessMu.Unlock()
}

func invalidatePages(host string, port int) {
	prefix := deviceKey(host, port)
	pageCacheMu.Lock()
	for k := range pageCache {
		if strings.HasPrefix(k, prefix) {
			delete(pageCache, k)
		}
	}
	pageCacheMu.Unlock()
}

func (m *mercury) readPageRaw(path string) (string, error) {
	if err := m.ensure(); err != nil {
		return "", err
	}
	page, err := m.get(path)
	if err != nil {
		return "", err
	}
	if strings.Contains(page, `name="plain_password"`) {
		m.cookie, m.token = "", ""
		sessMu.Lock()
		delete(sessCookie, deviceKey(m.host, m.port))
		sessMu.Unlock()
		if err := m.ensure(); err != nil {
			return "", err
		}
		return m.get(path)
	}
	return page, nil
}

func (m *mercury) readPage(path string) (string, error) {
	key := deviceKey(m.host, m.port) + path
	pageCacheMu.Lock()
	if e, ok := pageCache[key]; ok && time.Since(e.at) < pageTTL {
		pageCacheMu.Unlock()
		return e.body, nil
	}
	pageCacheMu.Unlock()

	page, err := m.readPageRaw(path)
	if err != nil {
		return "", err
	}
	pageCacheMu.Lock()
	pageCache[key] = pageEntry{body: page, at: time.Now()}
	pageCacheMu.Unlock()
	return page, nil
}

func (m *mercury) writeCGI(path string, fields url.Values) error {
	if err := m.ensure(); err != nil {
		return err
	}
	fields.Set("token", m.token)
	_, err := m.get(path + "?" + fields.Encode())
	m.token = ""
	invalidatePages(m.host, m.port)
	return err
}

/* ---------- 数据解析 ---------- */

func extractObject(text, name string) map[string]interface{} {
	re := regexp.MustCompile(`var\s+` + regexp.QuoteMeta(name) + `\s*=\s*\{`)
	loc := re.FindStringIndex(text)
	if loc == nil {
		return nil
	}
	start := strings.Index(text[loc[0]:], "{") + loc[0]
	depth, end := 0, -1
	for i := start; i < len(text); i++ {
		switch text[i] {
		case '{':
			depth++
		case '}':
			depth--
			if depth == 0 {
				end = i
			}
		}
		if end >= 0 {
			break
		}
	}
	if end < 0 {
		return nil
	}
	body := text[start : end+1]
	body = regexp.MustCompile(`([{,]\s*)([A-Za-z_$][\w$]*)\s*:`).ReplaceAllString(body, `$1"$2":`)
	body = regexp.MustCompile(`0x([0-9a-fA-F]+)`).ReplaceAllStringFunc(body, func(s string) string {
		v, _ := strconv.ParseInt(s[2:], 16, 64)
		return strconv.FormatInt(v, 10)
	})
	body = regexp.MustCompile(`,\s*([}\]])`).ReplaceAllString(body, `$1`)
	var out map[string]interface{}
	if err := json.Unmarshal([]byte(body), &out); err != nil {
		return nil
	}
	return out
}

func extractNumber(text, name string) string {
	re := regexp.MustCompile(`\b` + regexp.QuoteMeta(name) + `\s*=\s*(-?\d+)`)
	if m := re.FindStringSubmatch(text); len(m) > 1 {
		return m[1]
	}
	return ""
}

func numSlice(v interface{}) []float64 {
	arr, ok := v.([]interface{})
	if !ok {
		return nil
	}
	out := make([]float64, len(arr))
	for i, x := range arr {
		if f, ok := x.(float64); ok {
			out[i] = f
		}
	}
	return out
}

func strSlice(v interface{}) []string {
	arr, ok := v.([]interface{})
	if !ok {
		return nil
	}
	out := make([]string, len(arr))
	for i, x := range arr {
		if s, ok := x.(string); ok {
			out[i] = s
		}
	}
	return out
}

func atF(s []float64, i int) float64 {
	if i < len(s) {
		return s[i]
	}
	return 0
}

func atS(s []string, i int) string {
	if i < len(s) {
		return s[i]
	}
	return ""
}

// scalarInt 兼容固件里同一字段既有标量也有单元素数组两种写法。
func scalarInt(v interface{}) int {
	switch x := v.(type) {
	case float64:
		return int(x)
	case string:
		if n, err := strconv.Atoi(strings.TrimSpace(x)); err == nil {
			return n
		}
	case []interface{}:
		if len(x) > 0 {
			return scalarInt(x[0])
		}
	}
	return 0
}

func firstStr(v interface{}) string {
	switch x := v.(type) {
	case string:
		return x
	case []interface{}:
		if len(x) > 0 {
			return firstStr(x[0])
		}
	}
	return ""
}

var uptimeRe = regexp.MustCompile(`(\d+)\s*day[^-]*-?\s*(\d+)\s*hour[^-]*-?\s*(\d+)\s*min[^-]*-?\s*(\d+)\s*sec`)

func parseUptime(text string) int64 {
	m := uptimeRe.FindStringSubmatch(text)
	if len(m) < 5 {
		return 0
	}
	d, _ := strconv.ParseInt(m[1], 10, 64)
	h, _ := strconv.ParseInt(m[2], 10, 64)
	mi, _ := strconv.ParseInt(m[3], 10, 64)
	s, _ := strconv.ParseInt(m[4], 10, 64)
	return d*86400 + h*3600 + mi*60 + s
}

// parsePairs 解析形如 port_pos = [[0,0],[1,0],...] 的坐标数组。
func parsePairs(page, name string) [][2]int {
	re := regexp.MustCompile(`\b` + regexp.QuoteMeta(name) + `\s*=\s*([\s\S]*?);`)
	m := re.FindStringSubmatch(page)
	if len(m) < 2 {
		return nil
	}
	pairRe := regexp.MustCompile(`\[\s*(-?\d+)\s*,\s*(-?\d+)\s*\]`)
	out := [][2]int{}
	for _, p := range pairRe.FindAllStringSubmatch(m[1], -1) {
		x, _ := strconv.Atoi(p[1])
		y, _ := strconv.Atoi(p[2])
		out = append(out, [2]int{x, y})
	}
	return out
}

type portState struct {
	Port        int     `json:"port"`
	Enable      bool    `json:"enable"`
	Link        bool    `json:"link"`
	Speed       string  `json:"speed"`
	SpeedCode   int     `json:"speed_code"`
	CfgSpeed    string  `json:"cfg_speed"`
	CfgSpeedCod int     `json:"cfg_speed_code"`
	Rx          float64 `json:"rx"`
	Tx          float64 `json:"tx"`
	Flow        bool    `json:"flow"`
	FlowAct     bool    `json:"flow_act"`
	Loop        bool    `json:"loop"`
	PortType    int     `json:"port_type"`
	PosX        int     `json:"pos_x"`
	PosY        int     `json:"pos_y"`
}

// speedMbps 把固件速率编号换算成 Mbps，0 表示未连接。
func speedMbps(code int) int {
	switch code {
	case 2, 3:
		return 10
	case 4, 5:
		return 100
	case 6:
		return 1000
	case 7:
		return 2500
	case 8:
		return 10000
	}
	return 0
}

// speedCode 是 speedMbps 的反向换算，用于下发端口配置。
func speedCode(mbps int, duplex string) int {
	switch mbps {
	case 10:
		if duplex == "half" {
			return 2
		}
		return 3
	case 100:
		if duplex == "half" {
			return 4
		}
		return 5
	case 1000:
		return 6
	case 2500:
		return 7
	case 10000:
		return 8
	}
	return 1
}

func portDuplex(code int) string {
	if code == 2 || code == 4 {
		return "half"
	}
	return "full"
}

func portMedia(ps portState) string {
	if ps.PortType == 1 {
		return "fiber"
	}
	return "copper"
}

// portAbility 生成编辑弹窗里可选的速率/双工组合。
func portAbility(ps portState) map[string]interface{} {
	speeds := []map[string]interface{}{}
	if ps.PortType == 1 {
		speeds = []map[string]interface{}{
			{"speed": 1000, "duplex": []string{"full"}},
			{"speed": 2500, "duplex": []string{"full"}},
			{"speed": 10000, "duplex": []string{"full"}},
		}
	} else {
		speeds = []map[string]interface{}{
			{"speed": 10, "duplex": []string{"half", "full"}},
			{"speed": 100, "duplex": []string{"half", "full"}},
			{"speed": 1000, "duplex": []string{"full"}},
			{"speed": 2500, "duplex": []string{"full"}},
			{"speed": 10000, "duplex": []string{"full"}},
		}
	}
	return map[string]interface{}{"speed": speeds}
}

type deviceInfo struct {
	Model           string `json:"model"`
	Mac             string `json:"mac"`
	IP              string `json:"ip"`
	FirmwareVersion string `json:"firmware_version"`
	Hardware        string `json:"hardware"`
	Gateway         string `json:"gateway"`
	Netmask         string `json:"netmask"`
	Uptime          int64  `json:"uptime"`
	Online          bool   `json:"online"`
}


// portWire 把内部端口结构转成前端期望的形状（含嵌套 portInfo）。
func portWire(ps portState) map[string]interface{} {
	actSpeed := speedMbps(ps.SpeedCode)
	cfgSpeed := speedMbps(ps.CfgSpeedCod)
	return map[string]interface{}{
		"port": ps.Port, "port_id": ps.Port,
		"enable": ps.Enable, "link": ps.Link, "speed": ps.Speed,
		"speed_code": ps.SpeedCode, "rx": ps.Rx, "tx": ps.Tx,
		"portInfo": map[string]interface{}{
			"name": fmt.Sprintf("PORT%d", ps.Port),
			"link": map[string]interface{}{
				"up": ps.Link, "speed": actSpeed, "duplex": portDuplex(ps.SpeedCode),
				"media": portMedia(ps), "flow_control": ps.FlowAct,
			},
			"link_admin": map[string]interface{}{
				"up": ps.Enable, "auto_nego": ps.CfgSpeedCod == 1,
				"speed": cfgSpeed, "duplex": portDuplex(ps.CfgSpeedCod),
				"flow_control": ps.Flow,
			},
			"link_ability": portAbility(ps),
			"statistics":   map[string]interface{}{"rx_rate": ps.Rx, "tx_rate": ps.Tx},
		},
	}
}

func portWires(ps []portState) []map[string]interface{} {
	out := make([]map[string]interface{}, 0, len(ps))
	for _, p := range ps {
		out = append(out, portWire(p))
	}
	return out
}

// portFlat 对应 PortConfig 组件读取的扁平结构。
func portFlat(ps portState) map[string]interface{} {
	return map[string]interface{}{
		"port": ps.Port,
		"up": ps.Link,
		"enable": ps.Enable,
		"mode": fmt.Sprintf("%d", ps.CfgSpeedCod),
		"mode_text": ps.CfgSpeed,
		"speed": ps.Speed,
		"speed_code": ps.SpeedCode,
		"flow_control": ps.Flow,
		"mac": "00:00:00:00:00:00",
		"name": fmt.Sprintf("PORT%d", ps.Port),
		"rx": ps.Rx,
		"tx": ps.Tx,
	}
}

func portFlats(ps []portState) []map[string]interface{} {
	out := make([]map[string]interface{}, 0, len(ps))
	for _, p := range ps {
		out = append(out, portFlat(p))
	}
	return out
}

// portCard 对应 PortConfig / SwitchPortPanel 读取的端口项：link / link_admin 在顶层。
func portCard(ps portState) map[string]interface{} {
	actSpeed := speedMbps(ps.SpeedCode)
	cfgSpeed := speedMbps(ps.CfgSpeedCod)
	return map[string]interface{}{
		"port": ps.Port,
		"port_id": ps.Port,
		"name": fmt.Sprintf("PORT%d", ps.Port),
		"link": map[string]interface{}{
			"up": ps.Link, "speed": actSpeed, "duplex": portDuplex(ps.SpeedCode),
			"media": portMedia(ps), "flow_control": ps.FlowAct,
		},
		"link_admin": map[string]interface{}{
			"up": ps.Enable, "auto_nego": ps.CfgSpeedCod == 1,
			"speed": cfgSpeed, "duplex": portDuplex(ps.CfgSpeedCod),
			"flow_control": ps.Flow,
		},
		"link_ability": portAbility(ps),
		"port_type":    ps.PortType,
		"flow_control": ps.Flow,
		"mac":          "",
		"rx":           ps.Rx, "tx": ps.Tx,
	}
}

func portCards(ps []portState) []map[string]interface{} {
	out := make([]map[string]interface{}, 0, len(ps))
	for _, p := range ps {
		out = append(out, portCard(p))
	}
	return out
}

// panelGroups 按固件给的 port_pos 坐标生成面板矩阵：[[上排, 下排]]。
// 渲染组件会直接解构两行，空位用 -1 占位。
func panelGroups(ps []portState) [][][]int {
	type cell struct{ x, row, port int }
	cells := make([]cell, 0, len(ps))
	maxRow := 0
	for _, p := range ps {
		cells = append(cells, cell{p.PosX, p.PosY, p.Port})
		if p.PosY > maxRow {
			maxRow = p.PosY
		}
	}
	if maxRow > 1 {
		maxRow = 1
	}
	rows := make([][]int, 2)
	for r := 0; r < 2; r++ {
		rows[r] = []int{}
	}
	for r := 0; r <= maxRow; r++ {
		picked := []cell{}
		for _, c := range cells {
			if c.row == r {
				picked = append(picked, c)
			}
		}
		sort.Slice(picked, func(i, j int) bool { return picked[i].x < picked[j].x })
		for _, c := range picked {
			rows[r] = append(rows[r], c.port)
		}
	}
	width := 0
	for _, r := range rows {
		if len(r) > width {
			width = len(r)
		}
	}
	for r := range rows {
		for len(rows[r]) < width {
			rows[r] = append(rows[r], -1)
		}
	}
	return [][][]int{{rows[0], rows[1]}}
}

func (m *mercury) status() (deviceInfo, []portState, error) {
	var info deviceInfo
	page, err := m.readPage("/MainRpm.htm")
	if err != nil {
		return info, nil, err
	}
	ds := extractObject(page, "info_ds")
	pi := extractObject(page, "port_info")
	loop := extractObject(page, "loopPortConf")
	maxPorts := 6
	if s := extractNumber(page, "max_port_num"); s != "" {
		maxPorts, _ = strconv.Atoi(s)
	}
	if ds != nil {
		info.Model = atS(strSlice(ds["descriStr"]), 0)
		info.Mac = atS(strSlice(ds["macStr"]), 0)
		info.IP = atS(strSlice(ds["ipStr"]), 0)
		info.FirmwareVersion = atS(strSlice(ds["firmwareStr"]), 0)
		info.Hardware = atS(strSlice(ds["hardwareStr"]), 0)
		info.Gateway = atS(strSlice(ds["gatewayStr"]), 0)
		info.Netmask = atS(strSlice(ds["netmaskStr"]), 0)
		info.Uptime = parseUptime(atS(strSlice(ds["workTime"]), 0))
	}
	if info.Model == "" {
		info.Model = "Switch"
	}
	if info.IP == "" {
		info.IP = m.host
	}
	info.Online = true

	state := numSlice(pi["state"])
	spdCfg := numSlice(pi["spd_cfg"])
	spdAct := numSlice(pi["spd_act"])
	rx := numSlice(pi["rx_rate"])
	tx := numSlice(pi["tx_rate"])
	fc := numSlice(pi["fc_cfg"])
	fcAct := numSlice(pi["fc_act"])
	types := numSlice(pi["port_type"])
	loopPorts := numSlice(loop["ports"])
	pos := parsePairs(page, "port_pos")

	ports := make([]portState, 0, maxPorts)
	for i := 0; i < maxPorts; i++ {
		code := int(atF(spdAct, i))
		label := speedLabels[code]
		if label == "" {
			label = "未知"
		}
		cfgCode := int(atF(spdCfg, i))
		cfgLabel := speedLabels[cfgCode]
		if cfgLabel == "" {
			cfgLabel = "未知"
		}
		ps := portState{
			Port: i + 1, Enable: atF(state, i) == 1, Link: code > 0,
			Speed: label, SpeedCode: code, CfgSpeed: cfgLabel, CfgSpeedCod: cfgCode,
			Rx: atF(rx, i) / 1000, Tx: atF(tx, i) / 1000,
			Flow: atF(fc, i) == 1, FlowAct: atF(fcAct, i) == 1,
			Loop: atF(loopPorts, i) == 1, PortType: int(atF(types, i)),
		}
		if i < len(pos) {
			ps.PosX, ps.PosY = pos[i][0], pos[i][1]
		} else {
			ps.PosX, ps.PosY = i, 0
		}
		ports = append(ports, ps)
	}
	return info, ports, nil
}

type portConfig struct {
	Port     int    `json:"port"`
	Enable   bool   `json:"enable"`
	Speed    string `json:"speed"`
	SpeedCod int    `json:"speed_code"`
	Actual   string `json:"actual"`
	ActCode  int    `json:"actual_code"`
	Flow     bool   `json:"flow"`
}

func (m *mercury) portConfigs() ([]portConfig, error) {
	page, err := m.readPage("/PortSettingRpm.htm")
	if err != nil {
		return nil, err
	}
	all := extractObject(page, "all_info")
	maxPorts := 6
	if s := extractNumber(page, "max_port_num"); s != "" {
		maxPorts, _ = strconv.Atoi(s)
	}
	state := numSlice(all["state"])
	spdCfg := numSlice(all["spd_cfg"])
	spdAct := numSlice(all["spd_act"])
	fc := numSlice(all["fc_cfg"])
	out := make([]portConfig, 0, maxPorts)
	for i := 0; i < maxPorts; i++ {
		cfg := int(atF(spdCfg, i))
		act := int(atF(spdAct, i))
		cfgLabel, actLabel := speedLabels[cfg], speedLabels[act]
		if cfgLabel == "" {
			cfgLabel = "未知"
		}
		if actLabel == "" {
			actLabel = "未知"
		}
		out = append(out, portConfig{
			Port: i + 1, Enable: atF(state, i) == 1, Speed: cfgLabel, SpeedCod: cfg,
			Actual: actLabel, ActCode: act, Flow: atF(fc, i) == 1,
		})
	}
	return out, nil
}

func (m *mercury) setPorts(ports []int, state string, speed int, flow int) error {
	v := url.Values{}
	for _, p := range ports {
		v.Add("portid", strconv.Itoa(p))
	}
	switch state {
	case "enable":
		v.Set("state", "1")
	case "disable":
		v.Set("state", "0")
	default:
		v.Set("state", "7")
	}
	if speed < 0 {
		v.Set("speed", "9")
	} else {
		v.Set("speed", strconv.Itoa(speed))
	}
	if flow < 0 {
		v.Set("flowcontrol", "7")
	} else {
		v.Set("flowcontrol", strconv.Itoa(flow))
	}
	return m.writeCGI("/port_setting.cgi", v)
}

type vlanEntry struct {
	Vid      int    `json:"vid"`
	Name     string `json:"name"`
	Tagged   []int  `json:"tagged"`
	Untagged []int  `json:"untagged"`
}

type vlanData struct {
	Enable  bool        `json:"enable"`
	PortNum int         `json:"port_num"`
	MaxVids int         `json:"max_vids"`
	List    []vlanEntry `json:"list"`
}

func maskToPorts(mask float64, portNum int) []int {
	out := []int{}
	m := int(mask)
	for i := 0; i < portNum; i++ {
		if m&(1<<i) != 0 {
			out = append(out, i+1)
		}
	}
	return out
}

func (m *mercury) vlans() (vlanData, error) {
	var out vlanData
	page, err := m.readPage("/Vlan8021QRpm.htm")
	if err != nil {
		return out, err
	}
	ds := extractObject(page, "qvlan_ds")
	portNum := 6
	if f := atF(numSlice(ds["portNum"]), 0); f > 0 {
		portNum = int(f)
	}
	maxVids := 32
	if f := atF(numSlice(ds["maxVids"]), 0); f > 0 {
		maxVids = int(f)
	}
	out.PortNum = portNum
	out.MaxVids = maxVids
	out.Enable = atF(numSlice(ds["state"]), 0) == 1
	vids := numSlice(ds["vids"])
	names := strSlice(ds["names"])
	tag := numSlice(ds["tagMbrs"])
	untag := numSlice(ds["untagMbrs"])
	out.List = make([]vlanEntry, 0, len(vids))
	for i := range vids {
		out.List = append(out.List, vlanEntry{
			Vid:      int(vids[i]),
			Name:     atS(names, i),
			Tagged:   maskToPorts(atF(tag, i), portNum),
			Untagged: maskToPorts(atF(untag, i), portNum),
		})
	}
	return out, nil
}

func (m *mercury) saveVlan(vid int, name string, tagged, untagged []int, portNum int) error {
	v := url.Values{}
	v.Set("qvlan_add", "添加/编辑")
	v.Set("vid", strconv.Itoa(vid))
	v.Set("vname", name)
	inTagged := map[int]bool{}
	for _, p := range tagged {
		inTagged[p] = true
	}
	inUntagged := map[int]bool{}
	for _, p := range untagged {
		inUntagged[p] = true
	}
	for p := 1; p <= portNum; p++ {
		mode := "2"
		if inUntagged[p] {
			mode = "0"
		} else if inTagged[p] {
			mode = "1"
		}
		v.Set(fmt.Sprintf("selType_%d", p), mode)
	}
	return m.writeCGI("/qvlanSet.cgi", v)
}

func (m *mercury) deleteVlan(vid int) error {
	v := url.Values{}
	v.Set("qvlan_del", "删除")
	v.Set("selVlans", strconv.Itoa(vid))
	return m.writeCGI("/qvlanSet.cgi", v)
}

type lagGroup struct {
	Id    int   `json:"id"`
	Ports []int `json:"ports"`
	Lacp  bool  `json:"lacp"`
}

// lagGroups 从固件的 trunk_conf 还原静态聚合组。每组固定占 4 个槽位。
func (m *mercury) lagGroups() ([]lagGroup, error) {
	page, err := m.readPage("/PortTrunkRpm.htm")
	if err != nil {
		return nil, err
	}
	conf := extractObject(page, "trunk_conf")
	out := []lagGroup{}
	if conf == nil {
		return out, nil
	}
	max := int(atF(numSlice(conf["maxTrunkNum"]), 0))
	if max <= 0 {
		return out, nil
	}
	for g := 1; g <= max; g++ {
		arr := numSlice(conf[fmt.Sprintf("portStr_g%d", g)])
		base := 4 * (g - 1)
		ports := []int{}
		for i := 0; i < 4; i++ {
			if atF(arr, base+i) == 1 {
				ports = append(ports, base+i+1)
			}
		}
		if len(ports) >= 2 {
			out = append(out, lagGroup{Id: g, Ports: ports, Lacp: false})
		}
	}
	return out, nil
}

// addLag 把成员端口写进第一个空闲聚合组。
func (m *mercury) addLag(ports []int) error {
	groups, err := m.lagGroups()
	if err != nil {
		return err
	}
	used := map[int]bool{}
	for _, g := range groups {
		used[g.Id] = true
	}
	gid := 0
	for i := 1; i <= 8; i++ {
		if !used[i] {
			gid = i
			break
		}
	}
	if gid == 0 {
		return fmt.Errorf("聚合组已满")
	}
	v := url.Values{}
	v.Set("groupId", strconv.Itoa(gid))
	for _, p := range ports {
		v.Add("portid", strconv.Itoa(p))
	}
	if err := m.writeCGI("/port_trunk_set.cgi", v); err != nil {
		return err
	}
	return m.saveConfig()
}

// delLag 通过汇聚显示表单的复选框删除指定聚合组。
func (m *mercury) delLag(id int) error {
	v := url.Values{}
	v.Set(fmt.Sprintf("chk%d", id), "on")
	if err := m.writeCGI("/port_trunk_display.cgi", v); err != nil {
		return err
	}
	return m.saveConfig()
}

type macEntry struct {
	Mac  string `json:"mac"`
	Vid  int    `json:"vid"`
	Port int    `json:"port"`
	Type string `json:"type"`
}

// macTableRow 是前端 MAC 表组件读取的行结构。
type macTableRow struct {
	Mac  string `json:"mac"`
	Vlan int    `json:"vlan"`
	Port int    `json:"port"`
	Type string `json:"type"`
}

// forwardResult 允许分发层给内层信封附加 total 等字段。
type forwardResult struct {
	Data     interface{}
	Total    int
	HasTotal bool
}

// macDashes 把任意分隔符的 MAC 统一成固件接受的 XX-XX-XX-XX-XX-XX。
func macDashes(raw string) string {
	hex := strings.ToUpper(strings.Map(func(r rune) rune {
		if (r >= '0' && r <= '9') || (r >= 'a' && r <= 'f') || (r >= 'A' && r <= 'F') {
			return r
		}
		return -1
	}, raw))
	if len(hex) != 12 {
		return raw
	}
	var b strings.Builder
	for i := 0; i < 12; i += 2 {
		if i > 0 {
			b.WriteByte('-')
		}
		b.WriteString(hex[i : i+2])
	}
	return b.String()
}

func normalizeMac(raw string) string {
	hex := strings.ToUpper(strings.Map(func(r rune) rune {
		if (r >= '0' && r <= '9') || (r >= 'a' && r <= 'f') || (r >= 'A' && r <= 'F') {
			return r
		}
		return -1
	}, raw))
	if len(hex) != 12 {
		return strings.ToUpper(raw)
	}
	var b strings.Builder
	for i := 0; i < 12; i += 2 {
		if i > 0 {
			b.WriteByte(':')
		}
		b.WriteString(hex[i : i+2])
	}
	return b.String()
}

func (m *mercury) searchMac(mac string) ([]macEntry, error) {
	if err := m.ensure(); err != nil {
		return nil, err
	}
	v := url.Values{}
	v.Set("txt_macAddress_search", macDashes(mac))
	v.Set("txt_vid_search", "")
	v.Set("token", m.token)
	page, err := m.get("/mac_address_search.cgi?" + v.Encode())
	m.token = ""
	if err != nil {
		return nil, err
	}
	ds := extractObject(page, "mac_ds")
	rows := []macEntry{}
	if ds == nil {
		return rows, nil
	}
	raw, ok := ds["mac_info"].([]interface{})
	if !ok {
		return rows, nil
	}
	for _, item := range raw {
		obj, ok := item.(map[string]interface{})
		if !ok {
			continue
		}
		macVal, _ := obj["mac"].(string)
		port, _ := obj["port"].(float64)
		vid, _ := obj["vid"].(float64)
		if macVal == "" || macVal == "0" || strings.Trim(macVal, "0-:") == "" || port <= 0 {
			continue
		}
		rows = append(rows, macEntry{Mac: normalizeMac(macVal), Vid: int(vid), Port: int(port), Type: "dynamic"})
	}
	return rows, nil
}

func (m *mercury) saveConfig() error {
	v := url.Values{}
	v.Set("action_op", "保存")
	v.Set("system_act", "save")
	return m.writeCGI("/savingconfig.cgi", v)
}

/* ------------------------------------------------------------------ */
/* 设备配置                                                            */
/* ------------------------------------------------------------------ */

type deviceConf struct {
	Name     string `json:"name"`
	Mac      string `json:"mac"`
	IP       string `json:"ip"`
	Port     int    `json:"port"`
	Username string `json:"username"`
	Password string `json:"password"`
	Model    string `json:"model"`
}

func configPaths() []string {
	var out []string
	if p := os.Getenv("MERCURY_CONFIG"); p != "" {
		out = append(out, p)
	}
	out = append(out, filepath.Join(appHome(), "var", "devices.json"))
	if sp := socketPath(); sp != "" {
		out = append(out, filepath.Join(filepath.Dir(sp), "config", "devices.json"))
	}
	return out
}

func socketPath() string {
	if p := os.Getenv(socketEnv); p != "" {
		return p
	}
	return filepath.Join(appHome(), "target", "mercury")
}

func loadDevices() []deviceConf {
	for _, p := range configPaths() {
		b, err := os.ReadFile(p)
		if err != nil {
			continue
		}
		var list []deviceConf
		if err := json.Unmarshal(b, &list); err == nil {
			return list
		}
	}
	return nil
}

/* ------------------------------------------------------------------ */
/* HTTP 服务                                                            */
/* ------------------------------------------------------------------ */

func writeOK(w http.ResponseWriter, data interface{}) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"code": 200, "message": "操作成功", "data": data,
	})
}

func writeErr(w http.ResponseWriter, msg string) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"code": 500, "message": msg, "data": nil,
	})
}

// handleDeviceUpload 接收前端 multipart 上传：固件升级或配置还原。
func handleDeviceUpload(w http.ResponseWriter, r *http.Request, dev deviceConf) {
	r.Body = http.MaxBytesReader(w, r.Body, 120<<20)
	if err := r.ParseMultipartForm(16 << 20); err != nil {
		writeErr(w, "读取上传文件失败："+err.Error())
		return
	}
	method := r.FormValue("method")
	file, hdr, err := r.FormFile("file")
	if err != nil {
		file, hdr, err = r.FormFile("firmware")
	}
	if err != nil {
		file, hdr, err = r.FormFile("configfile")
	}
	if err != nil {
		writeErr(w, "未收到上传文件")
		return
	}
	defer file.Close()
	data, err := io.ReadAll(io.LimitReader(file, 100<<20))
	if err != nil {
		writeErr(w, "读取文件失败："+err.Error())
		return
	}
	name := hdr.Filename
	if v := r.FormValue("filename"); v != "" {
		name = v
	}
	m := newMercury(dev.IP, dev.Port, dev.Username, dev.Password)
	switch method {
	case "", "sys/fw/upgrade":
		if err := m.upgradeFirmware(name, data); err != nil {
			writeErr(w, err.Error())
			return
		}
		writeOK(w, map[string]interface{}{
			"uploaded": true, "filename": filepath.Base(strings.ReplaceAll(name, "\\", "/")),
			"size": len(data), "phase": "uploaded",
		})
	case "config/restore":
		if err := m.configRestore(name, data); err != nil {
			writeErr(w, err.Error())
			return
		}
		writeOK(w, map[string]interface{}{
			"restored": true, "filename": filepath.Base(strings.ReplaceAll(name, "\\", "/")),
			"size": len(data),
		})
	default:
		writeErr(w, "不支持的上传接口："+method)
	}
}

// handleConfigBackup 从设备导出 .cfg 并作为附件下载。
func handleConfigBackup(w http.ResponseWriter, r *http.Request) {
	mac := r.URL.Query().Get("mac")
	var dev *deviceConf
	devs := loadDevices()
	for i := range devs {
		if mac == "" || strings.EqualFold(devs[i].Mac, mac) {
			dev = &devs[i]
			break
		}
	}
	if dev == nil {
		writeErr(w, "设备不存在")
		return
	}
	m := newMercury(dev.IP, dev.Port, dev.Username, dev.Password)
	data, err := m.configBackup()
	if err != nil {
		writeErr(w, err.Error())
		return
	}
	name := fmt.Sprintf("%s-%s.cfg", strings.ReplaceAll(dev.Model, " ", "_"), time.Now().Format("20060102-150405"))
	w.Header().Set("Content-Type", "application/octet-stream")
	w.Header().Set("Content-Disposition", `attachment; filename="`+name+`"`)
	w.Header().Set("Cache-Control", "no-store")
	w.Write(data)
}

type forwardReq struct {
	Method string                 `json:"method"`
	Params map[string]interface{} `json:"params"`
}

func paramInt(p map[string]interface{}, key string, def int) int {
	if p == nil {
		return def
	}
	if v, ok := p[key]; ok {
		switch t := v.(type) {
		case float64:
			return int(t)
		case string:
			if n, err := strconv.Atoi(t); err == nil {
				return n
			}
		}
	}
	return def
}

func paramInts(p map[string]interface{}, key string) []int {
	out := []int{}
	if p == nil {
		return out
	}
	arr, ok := p[key].([]interface{})
	if !ok {
		return out
	}
	for _, x := range arr {
		if f, ok := x.(float64); ok {
			out = append(out, int(f))
		}
	}
	return out
}

func paramBool(p map[string]interface{}, key string) (bool, bool) {
	if p == nil {
		return false, false
	}
	v, ok := p[key]
	if !ok {
		return false, false
	}
	b, ok := v.(bool)
	return b, ok
}

func logf(format string, args ...interface{}) {
	f, err := os.OpenFile(filepath.Join(appHome(), "var", "req.log"), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		return
	}
	defer f.Close()
	fmt.Fprintln(f, time.Now().Format("15:04:05 ")+fmt.Sprintf(format, args...))
}

func dispatch(dev deviceConf, req forwardReq) (interface{}, error) {
	logf("forward method=%s params=%v", req.Method, req.Params)
	c := newMercury(dev.IP, dev.Port, dev.Username, dev.Password)
	switch req.Method {
	case "sys/info", "sys/top":
		info, ports, err := c.status()
		if err != nil {
			return nil, err
		}
		payload := map[string]interface{}{
			"model": info.Model, "mac": info.Mac, "ip": info.IP,
			"firmware_version": info.FirmwareVersion, "hardware": info.Hardware,
			"gateway": info.Gateway, "netmask": info.Netmask,
			"uptime": info.Uptime, "online": true, "is_online": true,
			"status": "online",
			"cpu": 0, "cpu_usage": 0, "mem": 0, "mem_usage": 0, "memory": 0,
			"port_num": len(ports), "ports": portWires(ports),
		}
		return payload, nil
	case "sys/functions":
		// 前端可能按数组读取；同时给出数组与对象两种形态
		return []string{"base", "port", "vlan", "mac", "lag", "network"}, nil
	case "sys/user/init":
		return map[string]interface{}{"need_login": false, "logined": true}, nil
	case "sys/user/login":
		user, _ := req.Params["username"].(string)
		pass, _ := req.Params["password"].(string)
		if user == "" {
			user = dev.Username
		}
		if user == "" {
			user = "admin"
		}
		if pass == "" {
			return nil, fmt.Errorf("请输入交换机密码")
		}
		probe := newMercury(dev.IP, dev.Port, user, pass)
		info, _, err := probe.status()
		if err != nil {
			return nil, fmt.Errorf("登录失败：%v", err)
		}
		saved := deviceConf{
			Name: dev.Name, IP: dev.IP, Port: dev.Port,
			Username: user, Password: pass, Model: info.Model, Mac: strings.ToUpper(info.Mac),
		}
		if saved.Name == "" {
			saved.Name = "水星交换机"
		}
		if saved.Mac == "" {
			saved.Mac = dev.Mac
		}
		if err := saveDevice(saved); err != nil {
			return nil, fmt.Errorf("保存设备失败：%v", err)
		}
		return map[string]interface{}{"token": "local", "user": user, "mac": saved.Mac}, nil
	case "sys/user/logout":
		return map[string]interface{}{}, nil
	case "sys/user/__config":
		// 固件要求 5-31 位可见 ASCII 字符；message 留空让前端使用本地化文案
		return map[string]interface{}{
			"__config": map[string]interface{}{
				"password": map[string]interface{}{
					"regex":   `^[!-~]{5,31}$`,
					"message": "",
				},
			},
		}, nil
	case "sys/user/update":
		user := "admin"
		if s, ok := req.Params["username"].(string); ok && s != "" {
			user = s
		}
		newPwd, _ := req.Params["password"].(string)
		if newPwd == "" {
			return nil, fmt.Errorf("新密码不能为空")
		}
		if err := c.changePassword(user, dev.Password, newPwd); err != nil {
			return nil, err
		}
		if err := c.loginAs(user, newPwd); err != nil {
			return nil, fmt.Errorf("密码修改未生效，请确认原密码后重试")
		}
		if err := updateStoredPassword(dev.Mac, newPwd); err != nil {
			return nil, err
		}
		return map[string]interface{}{}, nil
	case "port/panel":
		_, ports, err := c.status()
		if err != nil {
			return nil, err
		}
		return panelGroups(ports), nil
	case "port/link/list":
		_, ports, err := c.status()
		if err != nil {
			return nil, err
		}
		return portCards(ports), nil
	case "port/link/set":
		ports := paramInts(req.Params, "ports")
		if p := paramInt(req.Params, "port", 0); p > 0 {
			ports = []int{p}
		}
		if len(ports) == 0 {
			return map[string]interface{}{}, nil
		}
		state := ""
		if b, ok := paramBool(req.Params, "up"); ok {
			if b {
				state = "enable"
			} else {
				state = "disable"
			}
		}
		if b, ok := paramBool(req.Params, "enable"); ok {
			if b {
				state = "enable"
			} else {
				state = "disable"
			}
		}
		speed := -1
		if auto, ok := paramBool(req.Params, "auto_nego"); ok && auto {
			speed = 1
		} else if v := paramInt(req.Params, "speed", 0); v > 0 {
			duplex, _ := req.Params["duplex"].(string)
			speed = speedCode(v, duplex)
		} else if v := paramInt(req.Params, "speed_code", -1); v >= 0 {
			speed = v
		}
		flow := -1
		if b, ok := paramBool(req.Params, "flow_control"); ok {
			if b {
				flow = 1
			} else {
				flow = 0
			}
		}
		if b, ok := paramBool(req.Params, "flow"); ok {
			if b {
				flow = 1
			} else {
				flow = 0
			}
		}
		if err := c.setPorts(ports, state, speed, flow); err != nil {
			return nil, err
		}
		_ = c.saveConfig()
		return map[string]interface{}{}, nil
	case "port/lag/__config":
		// 该固件只支持静态聚合，没有哈希算法选项
		return map[string]interface{}{
			"__config": map[string]interface{}{"hash_algorithm": []interface{}{}},
		}, nil
	case "port/lag/static/groups":
		groups, err := c.lagGroups()
		if err != nil {
			return nil, err
		}
		return groups, nil
	case "port/lag/group/add":
		ports := paramInts(req.Params, "ports")
		if len(ports) < 2 {
			return nil, fmt.Errorf("至少需要选择 2 个端口")
		}
		if err := c.addLag(ports); err != nil {
			return nil, err
		}
		return map[string]interface{}{}, nil
	case "port/lag/group/del":
		if err := c.delLag(paramInt(req.Params, "id", 0)); err != nil {
			return nil, err
		}
		return map[string]interface{}{}, nil
	case "vlan/list":
		return c.vlans()
	case "vlan/add", "vlan/update":
		_, ports, err := c.status()
		if err != nil {
			return nil, err
		}
		name := ""
		if s, ok := req.Params["name"].(string); ok {
			name = s
		}
		if err := c.saveVlan(
			paramInt(req.Params, "vid", 0),
			name,
			paramInts(req.Params, "tagged"),
			paramInts(req.Params, "untagged"),
			len(ports),
		); err != nil {
			return nil, err
		}
		_ = c.saveConfig()
		return map[string]interface{}{}, nil
	case "vlan/delete":
		if err := c.deleteVlan(paramInt(req.Params, "vid", 0)); err != nil {
			return nil, err
		}
		_ = c.saveConfig()
		return map[string]interface{}{}, nil
	case "mactable/list":
		keyword := ""
		if s, ok := req.Params["keyword"].(string); ok {
			keyword = strings.TrimSpace(s)
		}
		rows := []macTableRow{}
		if keyword != "" {
			found, err := c.searchMac(keyword)
			if err != nil {
				return nil, err
			}
			for _, r := range found {
				rows = append(rows, macTableRow{Mac: r.Mac, Vlan: r.Vid, Port: r.Port, Type: r.Type})
			}
		}
		return &forwardResult{Data: rows, Total: len(rows), HasTotal: true}, nil
	case "mactable/add", "mactable/delete", "mactable/clear":
		return nil, fmt.Errorf("该交换机固件不提供静态 MAC 表管理接口")
	case "sys/param/save":
		return map[string]interface{}{}, c.saveConfig()
	case "sys/network/ipv4":
		return c.networkConfig()
	case "sys/network/ipv4/set":
		method, _ := req.Params["method"].(string)
		address, _ := req.Params["address"].(string)
		mask, _ := req.Params["mask"].(string)
		gateway, _ := req.Params["gateway"].(string)
		dnsList := []string{}
		if arr, ok := req.Params["dns"].([]interface{}); ok {
			for _, d := range arr {
				if s, ok := d.(string); ok && s != "" {
					dnsList = append(dnsList, s)
				}
			}
		}
		if method == "" {
			method = "static"
		}
		if err := c.setNetworkConfig(method, address, mask, gateway, dnsList); err != nil {
			return nil, err
		}
		return map[string]interface{}{
			"method": method, "address": address, "mask": mask,
			"gateway": gateway, "dns": dnsList,
		}, nil
	case "sys/reboot":
		save := true
		if b, ok := paramBool(req.Params, "save"); ok {
			save = b
		}
		if err := c.rebootDevice(save); err != nil {
			return nil, err
		}
		return map[string]interface{}{}, nil
	case "sys/fw/upgrade":
		return nil, fmt.Errorf("固件需以 multipart 表单上传")
	case "upgrade/ota/check":
		// 本机型 cloudMngtFlag=0，设备端“在线升级”面板本身是隐藏的
		return []interface{}{}, nil
	case "upgrade/ota/start", "upgrade/ota/info", "upgrade/ota/progress":
		return nil, fmt.Errorf("该设备未启用云端升级，请使用本地升级上传 .bin 固件")

	/* ---- 设备高级功能 ---- */
	case "statistics/list":
		return c.portStatistics()
	case "statistics/clear":
		return map[string]interface{}{}, c.clearStatistics()
	case "cable/list":
		return c.cableDiag()
	case "cable/run":
		return map[string]interface{}{}, c.cableDiagRun(paramInts(req.Params, "ports"))
	case "mirror/get":
		return c.mirrorConfig()
	case "mirror/set":
		enabled, _ := paramBool(req.Params, "enabled")
		return map[string]interface{}{}, c.mirrorEnableSet(enabled, paramInt(req.Params, "monitor_port", 0))
	case "mirror/source/set":
		return map[string]interface{}{}, c.mirrorSourceSet(
			paramInts(req.Params, "ports"),
			paramInt(req.Params, "ingress", 0),
			paramInt(req.Params, "egress", 0))
	case "isolation/get":
		return c.isolationConfig()
	case "isolation/set":
		return map[string]interface{}{}, c.isolationSet(
			paramInts(req.Params, "group_ports"), paramInts(req.Params, "forward_ports"))
	case "loop/get":
		return c.loopConfig()
	case "loop/set":
		enabled, _ := paramBool(req.Params, "enabled")
		return map[string]interface{}{}, c.loopSet(enabled)
	case "qos/get":
		return c.qosConfig()
	case "qos/mode/set":
		return map[string]interface{}{}, c.qosModeSet(paramInt(req.Params, "mode", -1))
	case "qos/priority/set":
		return map[string]interface{}{}, c.qosPrioritySet(
			paramInts(req.Params, "ports"), paramInt(req.Params, "queue", -1))
	case "qos/rate/set":
		return map[string]interface{}{}, c.qosRateSet(
			paramInts(req.Params, "ports"),
			paramInt(req.Params, "ingress_kbps", 0),
			paramInt(req.Params, "egress_kbps", 0))
	case "qos/storm/set":
		enabled, _ := paramBool(req.Params, "enabled")
		return map[string]interface{}{}, c.qosStormSet(
			paramInts(req.Params, "ports"), enabled,
			paramInt(req.Params, "rate_kbps", 0), paramInts(req.Params, "types"))
	case "vlan/mtu/get":
		return c.mtuVlanConfig()
	case "vlan/mtu/set":
		return map[string]interface{}{}, c.mtuVlanSet(
			paramBoolPtr(req.Params, "enabled"), paramIntPtr(req.Params, "uplink"))
	case "vlan/portbased/get":
		return c.portVlanConfig()
	case "vlan/portbased/enable":
		enabled, _ := paramBool(req.Params, "enabled")
		return map[string]interface{}{}, c.portVlanEnable(enabled)
	case "vlan/portbased/add":
		return map[string]interface{}{}, c.portVlanAdd(
			paramInt(req.Params, "vid", 0), paramInts(req.Params, "ports"))
	case "vlan/portbased/del":
		return map[string]interface{}{}, c.portVlanDel(paramInts(req.Params, "vlans"))
	case "vlan/pvid/get":
		return c.pvidConfig()
	case "vlan/pvid/set":
		return map[string]interface{}{}, c.pvidSet(
			paramInt(req.Params, "pbm", 0), paramInt(req.Params, "pvid", 0))
	case "sys/factory/reset":
		if s, _ := req.Params["confirm"].(string); s != "RESET" {
			return nil, fmt.Errorf("恢复出厂需要确认参数 confirm=RESET")
		}
		return map[string]interface{}{}, c.factoryReset()
	default:
		return map[string]interface{}{}, nil
	}
}

func handler(w http.ResponseWriter, r *http.Request) {
	rawPath := r.URL.Path
	path := rawPath
	if i := strings.Index(path, "/api/"); i >= 0 {
		path = path[i:]
	}

	if strings.HasPrefix(path, "/api/") {
		if strings.HasPrefix(path, "/api/speedtest") && r.URL.RawQuery != "" {
			logf("api %s %s?%s", r.Method, path, r.URL.RawQuery)
		} else {
			logf("api %s %s", r.Method, path)
		}
	}

	// 非 API 请求交给静态文件
	if !strings.HasPrefix(path, "/api/") {
		serveStatic(w, r, rawPath)
		return
	}

	switch {
	case path == "/api/auth/userinfo":
		writeOK(w, map[string]interface{}{"user_id": 1000, "user_name": "admin", "is_admin": true})
	case path == "/api/speedtest/discovered":
		writeOK(w, speedDiscovered(strings.TrimSpace(r.URL.Query().Get("ip"))))
	case path == "/api/speedtest/status":
		writeOK(w, speed.snapshot())
	case path == "/api/speedtest/start":
		var body struct {
			Client string `json:"client"`
			Server string `json:"server"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		if err := speed.start(body.Client, body.Server); err != nil {
			writeErr(w, err.Error())
			return
		}
		writeOK(w, map[string]interface{}{})
	case path == "/api/speedtest/download/info":
		pkg, ok := lanperfPackage(r.URL.Query().Get("platform"))
		if !ok {
			writeErr(w, "安装包不存在")
			return
		}
		writeOK(w, map[string]interface{}{"version": pkg.Version, "updated": pkg.Updated, "size": pkg.Size})
	case path == "/api/speedtest/download":
		pkg, ok := lanperfPackage(r.URL.Query().Get("platform"))
		if !ok {
			writeErr(w, "安装包不存在")
			return
		}
		w.Header().Set("Content-Type", "application/octet-stream")
		w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", filepath.Base(pkg.Path)))
		http.ServeFile(w, r, pkg.Path)
		return
	case path == "/api/tools/ping":
		q := r.URL.Query()
		writeOK(w, toolsPing(q.Get("host"), paramInt(map[string]interface{}{"n": q.Get("count")}, "n", 4)))
	case path == "/api/tools/dns":
		writeOK(w, toolsDNS(r.URL.Query().Get("name")))
	case path == "/api/tools/portscan":
		q := r.URL.Query()
		writeOK(w, toolsPortScan(q.Get("host"), q.Get("ports")))
	case path == "/api/setup/discover":
		setupDiscover(w, r)
	case path == "/api/setup/devices":
		if r.Method == "DELETE" {
			setupDeleteDevice(w, r)
		} else if r.Method == "POST" {
			setupAddDevice(w, r)
		} else {
			setupListDevices(w, r)
		}
	case path == "/api/setup/device/test":
		setupTestDevice(w, r)
	case path == "/api/setup/status":
		setupStatus(w, r)
	case path == "/api/setup/device":
		setupSaveDevice(w, r)
	case path == "/api/config/backup":
		handleConfigBackup(w, r)
	case path == "/api/tools/traceroute":
		q := r.URL.Query()
		writeOK(w, toolsTraceroute(
			q.Get("host"),
			paramInt(map[string]interface{}{"v": q.Get("max")}, "v", 20),
			paramInt(map[string]interface{}{"v": q.Get("q")}, "v", 1),
		))
	case path == "/api/tools/ifaces":
		writeOK(w, toolsInterfaces())
	case path == "/api/tools/capture":
		q := r.URL.Query()
		data, packets, err := toolsCapture(
			q.Get("iface"),
			paramInt(map[string]interface{}{"v": q.Get("seconds")}, "v", 10),
			paramInt(map[string]interface{}{"v": q.Get("snap")}, "v", 0),
		)
		if err != nil {
			writeErr(w, err.Error())
			return
		}
		w.Header().Set("Content-Type", "application/vnd.tcpdump.pcap")
		w.Header().Set("X-Packet-Count", strconv.Itoa(packets))
		w.Header().Set("X-Byte-Count", strconv.Itoa(len(data)))
		w.Header().Set("Content-Disposition", fmt.Sprintf("attachment; filename=%q", "capture-"+time.Now().Format("20060102-150405")+".pcap"))
		_, _ = w.Write(data)
		return
	case path == "/api/popup":
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"code": 200, "type": "none", "message": "操作成功", "data": nil,
		})
	case path == "/api/notifications":
		writeOK(w, map[string]interface{}{"list": []interface{}{}, "unread": 0})
	case path == "/api/notifications/read":
		writeOK(w, map[string]interface{}{})
	case path == "/api/devices":
		ensureScan()
		devices := loadDevices()
		list := make([]map[string]interface{}, 0, len(devices))
		for _, d := range devices {
			model, fw := d.Model, ""
			online := false
			if info, _, err := newMercury(d.IP, d.Port, d.Username, d.Password).status(); err == nil {
				online = true
				if info.Model != "" {
					model = info.Model
				}
				fw = info.FirmwareVersion
			}
			name := d.Name
			if name == "" {
				name = model
			}
			list = append(list, map[string]interface{}{
				"mac": d.Mac, "name": name, "model": model, "firmware_version": fw,
				"ip": d.IP, "is_online": online, "online": online, "is_login": true,
			})
		}
		list = append(list, discoveredList(devices)...)
		writeOK(w, list)
	case strings.HasPrefix(path, "/api/forward/"):
		rest := strings.TrimPrefix(path, "/api/forward/")
		if unescaped, err := url.PathUnescape(rest); err == nil {
			rest = unescaped
		}
		mac, sub := rest, ""
		if i := strings.IndexByte(rest, '/'); i >= 0 {
			mac, sub = rest[:i], rest[i+1:]
		}
		var dev *deviceConf
		devs := loadDevices()
		for i := range devs {
			if strings.EqualFold(devs[i].Mac, mac) {
				dev = &devs[i]
				break
			}
		}
		if dev == nil {
			if d, ok := discoveredByMac(mac); ok {
				dev = &d
			}
		}
		if dev == nil {
			writeErr(w, "设备不存在")
			return
		}
		if sub == "upgrade/status" {
			writeOK(w, map[string]interface{}{"state": "idle", "phase": "idle", "progress": 0})
			return
		}
		if strings.HasPrefix(r.Header.Get("Content-Type"), "multipart/form-data") {
			handleDeviceUpload(w, r, *dev)
			return
		}
		var req forwardReq
		if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
			req = forwardReq{}
		}
		data, err := dispatch(*dev, req)
		if err != nil {
			writeErr(w, err.Error())
			return
		}
		if true {
			inner := map[string]interface{}{"code": 0, "message": "success", "data": data}
			if fr, ok := data.(*forwardResult); ok {
				inner["data"] = fr.Data
				if fr.HasTotal {
					inner["total"] = fr.Total
				}
			}
			w.Header().Set("Content-Type", "application/json; charset=utf-8")
			json.NewEncoder(w).Encode(map[string]interface{}{
				"code": 200, "message": "操作成功",
				"data": inner,
			})
			return
		}
		writeOK(w, data)
	default:
		writeOK(w, map[string]interface{}{})
	}
}

func serve() {
	sp := socketPath()
	_ = os.Remove(sp)
	_ = os.MkdirAll(filepath.Dir(sp), 0o755)

	ln, err := net.Listen("unix", sp)
	if err != nil {
		fmt.Fprintln(os.Stderr, "listen unix:", err)
		os.Exit(1)
	}
	_ = os.Chmod(sp, 0o666)

	srv := &http.Server{Handler: http.HandlerFunc(handler)}
	go func() { _ = srv.Serve(ln) }()

	go ensureScan()

	// 桌面端收到测速回执后会直接连 5201，服务端必须常驻：
	// 端口空闲就拉起自带 iperf3，被其他服务占用则复用之。
	go ensureSpeedServerSync()
	go startSpeedResponder()

	probeHandler := http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Access-Control-Allow-Origin", "*")
		w.Header().Set("Content-Type", "application/json; charset=utf-8")
		if strings.HasSuffix(r.URL.Path, "/api/myip") {
			json.NewEncoder(w).Encode(map[string]interface{}{
				"code": 200, "message": "操作成功",
				"data": map[string]string{"ip": clientIP(r)},
			})
			return
		}
		fmt.Fprint(w, `{"code":200,"message":"操作成功","data":{}}`)
	})
	probe := &http.Server{Addr: fmt.Sprintf("0.0.0.0:%d", probePort()), Handler: probeHandler}
	go func() { _ = probe.ListenAndServe() }()

	startFnSwitchHolder(http.HandlerFunc(handler))
	// 5201 由启动时的 ensureSpeedServerSync 常驻持有，测速控制请求到达时再补拉一次。

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, syscall.SIGTERM, syscall.SIGINT)
	<-sig
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	_ = srv.Shutdown(ctx)
	_ = probe.Shutdown(ctx)
	stopSpeedServer()
	_ = os.Remove(sp)
}

// stopSpeedServer 结束本进程拉起的 iperf3 服务端。
// 只清自己启动的那一个；复用外部已有服务时不动。
func stopSpeedServer() {
	speedServerMu.Lock()
	cmd := speedServerCmd
	speedServerCmd = nil
	speedServerMu.Unlock()
	if cmd == nil || cmd.Process == nil {
		return
	}
	_ = cmd.Process.Kill()
	logf("speed: iperf3 服务端已停止 pid=%d", cmd.Process.Pid)
}

func socketReady(sp string) bool {
	fi, err := os.Stat(sp)
	if err != nil {
		return false
	}
	return fi.Mode()&os.ModeSocket != 0
}

// daemonize：脱离终端后台运行并立刻返回，
// 否则 cmd/main 里的 "$APP_BIN" --daemon 会一直阻塞。

/* ---- 官方 NMS socket 兼容持有 ----
   桌面端测速工具通过 fnOS 判断「官方 NMS 后端是否在运行」来列出设备。
   当官方应用已安装但后端未运行时，本应用代为持有其网关 socket，
   使桌面端能识别设备；官方后端一旦启动则自动让位，互不冲突。 */

const fnSwitchSocketPath = "/var/apps/fnSwitch/target/fnSwitch"

func fnSwitchInstalled() bool {
	st, err := os.Stat("/var/apps/fnSwitch/target")
	return err == nil && st.IsDir()
}

func unixPeerAlive(path string) bool {
	c, err := net.Dial("unix", path)
	if err != nil {
		return false
	}
	_ = c.Close()
	return true
}


func startFnSwitchHolder(handler http.Handler) {
	go func() {
		var ln net.Listener
		var ourIno uint64
		holderActive := false
		for {
			if !fnSwitchInstalled() {
				if holderActive {
					_ = ln.Close()
					ln = nil
					holderActive = false
				}
				time.Sleep(5 * time.Second)
				continue
			}
			fi, statErr := os.Stat(fnSwitchSocketPath)
			if holderActive {
				ino := uint64(0)
				if statErr == nil {
					if sys, ok := fi.Sys().(*syscall.Stat_t); ok {
						ino = sys.Ino
					}
				}
				if statErr != nil || ino != ourIno {
					// socket 被移除或被官方后端接管，让位
					_ = ln.Close()
					ln = nil
					holderActive = false
					time.Sleep(5 * time.Second)
					continue
				}
				time.Sleep(3 * time.Second)
				continue
			}
			if statErr == nil && unixPeerAlive(fnSwitchSocketPath) {
				// 官方后端在运行，不介入
				time.Sleep(5 * time.Second)
				continue
			}
			_ = os.Remove(fnSwitchSocketPath)
			_ = os.MkdirAll(filepath.Dir(fnSwitchSocketPath), 0o755)
			l, err := net.Listen("unix", fnSwitchSocketPath)
			if err != nil {
				time.Sleep(5 * time.Second)
				continue
			}
			_ = os.Chmod(fnSwitchSocketPath, 0o666)
			ln = l
			holderActive = true
			if fi2, err2 := os.Stat(fnSwitchSocketPath); err2 == nil {
				if sys, ok := fi2.Sys().(*syscall.Stat_t); ok {
					ourIno = sys.Ino
				}
			}
			srv := &http.Server{Handler: handler}
			go func(s *http.Server, l net.Listener) { _ = s.Serve(l) }(srv, ln)
			logf("speed: 已代持官方 NMS 网关 socket %s", fnSwitchSocketPath)
			time.Sleep(3 * time.Second)
		}
	}()
}

func daemonize() {
	sp := socketPath()
	if socketReady(sp) {
		fmt.Println("already running")
		return
	}
	_ = os.MkdirAll(filepath.Dir(sp), 0o755)
	logPath := filepath.Join(appHome(), "var", "meshlan.log")
	_ = os.MkdirAll(filepath.Dir(logPath), 0o755)
	logFile, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		logFile = os.Stdout
	}

	exe, err := os.Executable()
	if err != nil {
		exe = os.Args[0]
	}
	cmd := exec.Command(exe, "--serve")
	cmd.Stdout = logFile
	cmd.Stderr = logFile
	cmd.Stdin = nil
	cmd.SysProcAttr = &syscall.SysProcAttr{Setsid: true}
	if err := cmd.Start(); err != nil {
		fmt.Fprintln(os.Stderr, "start failed:", err)
		os.Exit(1)
	}
	_ = cmd.Process.Release()

	// 等待 socket 就绪，最多 15 秒
	for i := 0; i < 150; i++ {
		if socketReady(sp) {
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	fmt.Fprintln(os.Stderr, "socket not ready")
	os.Exit(1)
}

func main() {
	arg := "--status"
	if len(os.Args) > 1 {
		arg = os.Args[1]
	}
	switch arg {
	case "--status":
		if socketReady(socketPath()) {
			fmt.Println("Running")
			os.Exit(0)
		}
		fmt.Println("Stopped")
		os.Exit(3)
	case "--daemon":
		daemonize()
	case "--stop":
		os.Exit(0)
	default:
		serve()
	}
}

/* ------------------------------------------------------------------ */
/* 静态资源：前端页面由后端托管                                         */
/* ------------------------------------------------------------------ */

// wwwDir 返回前端目录（与可执行文件同级）。
func wwwDir() string {
	exe, err := os.Executable()
	if err != nil {
		exe = os.Args[0]
	}
	dir := filepath.Join(filepath.Dir(exe), "www")
	if _, err := os.Stat(dir); err == nil {
		return dir
	}
	return filepath.Join("/usr/local/apps/@appcenter", appName(), "server", "www")
}

func serveStatic(w http.ResponseWriter, r *http.Request, requestPath string) {
	root := wwwDir()

	// 去掉网关前缀，只保留相对路径
	rel := requestPath
	for _, prefix := range []string{"/app/" + appName()} {
		if strings.HasPrefix(rel, prefix) {
			rel = strings.TrimPrefix(rel, prefix)
			break
		}
	}
	if rel == "" || rel == "/" {
		rel = "/index.html"
	}

	full := filepath.Join(root, filepath.FromSlash(rel))
	// 目录穿越防护
	if !strings.HasPrefix(full, filepath.Clean(root)+string(os.PathSeparator)) &&
		filepath.Clean(full) != filepath.Join(root, "index.html") {
		http.NotFound(w, r)
		return
	}

	if fi, err := os.Stat(full); err == nil && !fi.IsDir() {
		// 前端资源每次都要回源校验，避免升级后仍跑旧 chunk
		switch strings.ToLower(filepath.Ext(full)) {
		case ".html", ".js", ".css", ".mjs":
			w.Header().Set("Cache-Control", "no-cache, must-revalidate")
		}
		http.ServeFile(w, r, full)
		return
	}

	// SPA 回退：未知路径一律交给 index.html 由前端路由处理
	http.ServeFile(w, r, filepath.Join(root, "index.html"))
}

/* ------------------------------------------------------------------ */
/* 网络测速：/api/speedtest/*                */
/* ------------------------------------------------------------------ */

const (
	speedPort    = 5201
	speedStreams = 4
	speedSeconds = 10
)

type speedNode struct {
	IP           string `json:"ip"`
	Name         string `json:"name"`
	MAC          string `json:"mac,omitempty"`
	Iperf3Enable bool   `json:"iperf3_enable"`
	Port         int    `json:"port"`
}

type speedRunResult struct {
	Host            string   `json:"host"`
	Port            int      `json:"port"`
	State           string   `json:"state"`
	Running         bool     `json:"running"`
	Output          string   `json:"output"`
	ExitCode        int      `json:"exit_code"`
	ParallelStreams int      `json:"parallel_streams"`
	MaxMbps         *float64 `json:"max_mbps,omitempty"`
	MaxMBs          *float64 `json:"max_mb_s,omitempty"`
	SummaryMbps     *float64 `json:"summary_mbps,omitempty"`
	SummaryMBs      *float64 `json:"summary_mb_s,omitempty"`
}

type speedState struct {
	mu  sync.Mutex
	res speedRunResult
	buf strings.Builder
}

var speed = &speedState{res: speedRunResult{State: "idle", ParallelStreams: speedStreams}}

func (s *speedState) snapshot() speedRunResult {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := s.res
	out.Output = s.buf.String()
	return out
}

type speedWriter struct{ s *speedState }

func (w speedWriter) Write(p []byte) (int, error) {
	w.s.mu.Lock()
	w.s.buf.Write(p)
	w.s.mu.Unlock()
	return len(p), nil
}

// start 在 NAS 侧拉起 iperf3 客户端；server 指向本机时用 -R 反向测上传。
func (s *speedState) start(client, server string) error {
	s.mu.Lock()
	if s.res.Running {
		s.mu.Unlock()
		return fmt.Errorf("测速正在进行中")
	}
	s.mu.Unlock()

	nasIP := localIPFor(client)
	target, reverse := client, false
	switch {
	case client == "" || client == nasIP:
		target = server
	case server == nasIP:
		target, reverse = client, true
	}
	if target == "" {
		return fmt.Errorf("测速目标为空")
	}
	bin := iperfBinary()
	if bin == "" {
		return fmt.Errorf("未找到 iperf3 可执行文件")
	}

	// 对端若是 LANPerf 桌面端，先让它把 5201 服务端拉起来
	ensureSpeedClient(target)

	newCmd := func() *exec.Cmd {
		args := []string{
			"-c", target, "-p", strconv.Itoa(speedPort),
			"-t", strconv.Itoa(speedSeconds), "-i", "1",
			"-P", strconv.Itoa(speedStreams),
			"--connect-timeout", "5000", "--forceflush",
		}
		if reverse {
			args = append(args, "-R")
		}
		return iperfExec(bin, args)
	}
	cmd := newCmd()

	s.mu.Lock()
	s.buf.Reset()
	s.res = speedRunResult{
		Host: target, Port: speedPort, State: "running",
		Running: true, ExitCode: -1, ParallelStreams: speedStreams,
	}
	s.mu.Unlock()

	w := speedWriter{s: s}
	cmd.Stdout = w
	cmd.Stderr = w
	if err := cmd.Start(); err != nil {
		s.mu.Lock()
		s.res.State = "failed"
		s.res.Running = false
		s.buf.WriteString(err.Error())
		s.mu.Unlock()
		return fmt.Errorf("启动测速失败: %v", err)
	}

	go func() {
		err := cmd.Wait()
		out := s.buf.String()
		// 桌面端服务端可能还没就绪，连接失败时重试一次
		if err != nil && (strings.Contains(out, "unable to connect") || strings.Contains(out, "Connection refused")) {
			if _, isPeer := speedPeer(target); isPeer {
				logf("speed: 首次连接 %s 失败，重新拉起服务端后重试", target)
				ensureSpeedClient(target)
				s.mu.Lock()
				s.buf.Reset()
				s.mu.Unlock()
				cmd = newCmd()
				cmd.Stdout = w
				cmd.Stderr = w
				if e := cmd.Start(); e == nil {
					err = cmd.Wait()
					out = s.buf.String()
				}
			}
		}

		s.mu.Lock()
		defer s.mu.Unlock()
		s.res.Running = false
		if cmd.ProcessState != nil {
			s.res.ExitCode = cmd.ProcessState.ExitCode()
		}
		maxMbps, sumMbps := parseSpeedMetrics(out)
		// 测速结束后才给出 max/summary，运行中前端改用实时值
		maxV, maxMB, sumV, sumMB := round1(maxMbps), round1(maxMbps/8), round1(sumMbps), round1(sumMbps/8)
		s.res.MaxMbps, s.res.MaxMBs = &maxV, &maxMB
		s.res.SummaryMbps, s.res.SummaryMBs = &sumV, &sumMB
		if err == nil {
			s.res.State = "done"
			speedPeerMu.Lock()
			speedReadyAt[target] = time.Now()
			speedPeerMu.Unlock()
		} else {
			s.res.State = "failed"
			speedPeerMu.Lock()
			delete(speedReadyAt, target)
			speedPeerMu.Unlock()
		}
	}()
	return nil
}

func round1(v float64) float64 {
	return math.Round(v*10) / 10
}

// shellQuote 给 script -c 的参数字符串做安全引用。
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// iperfExec 用伪终端包一层：iperf3 是静态链接的，走管道时 stdio 全缓冲，
// 每个采样要攒满 4KB 才输出；给个 TTY 才会按行刷新，实时曲线才能每秒更新。
func iperfExec(bin string, args []string) *exec.Cmd {
	if sp, err := exec.LookPath("script"); err == nil {
		parts := make([]string, 0, len(args)+1)
		parts = append(parts, shellQuote(bin))
		for _, a := range args {
			parts = append(parts, shellQuote(a))
		}
		c := exec.Command(sp, "-q", "-e", "-c", strings.Join(parts, " "), "/dev/null")
		c.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
		return c
	}
	c := exec.Command(bin, args...)
	c.SysProcAttr = &syscall.SysProcAttr{Setpgid: true}
	return c
}

var speedIntervalRe = regexp.MustCompile(`^\[\s*(SUM|\d+)\]\s+[\d.]+-[\d.]+\s+sec\s+[\d.]+\s+\S*Bytes\s+([\d.]+)\s+([KMG]?)bits/sec`)

func speedBitrateMbps(v float64, unit string) float64 {
	switch unit {
	case "K":
		return v / 1000
	case "G":
		return v * 1000
	}
	return v
}

// parseSpeedMetrics 返回（峰值 Mbps，汇总 Mbps），与前端图表口径一致。
func parseSpeedMetrics(out string) (maxMbps, summaryMbps float64) {
	var lastSum float64
	for _, line := range strings.Split(out, "\n") {
		line = strings.TrimSpace(line)
		m := speedIntervalRe.FindStringSubmatch(line)
		if m == nil || strings.ToUpper(m[1]) != "SUM" {
			continue
		}
		val, _ := strconv.ParseFloat(m[2], 64)
		v := speedBitrateMbps(val, m[3])
		if strings.Contains(line, "sender") {
			summaryMbps = v
			continue
		}
		if strings.Contains(line, "receiver") {
			continue
		}
		if v > maxMbps {
			maxMbps = v
		}
		lastSum = v
	}
	if summaryMbps == 0 {
		summaryMbps = lastSum
	}
	return maxMbps, summaryMbps
}

func tcpOpen(host string, port int, timeout time.Duration) bool {
	if host == "" {
		return false
	}
	c, err := net.DialTimeout("tcp", net.JoinHostPort(host, strconv.Itoa(port)), timeout)
	if err != nil {
		return false
	}
	_ = c.Close()
	return true
}

/* ---- LANPerf 桌面端发现/控制协议（UDP 3399） ----
   报文：magic(4) ver(2) type(1) dstMAC(6) srcMAC(6) rpc(4) len(2) payload
   payload：kind(1) + 2B + ver(1) + sub(1) + 1B + 字段数(1) + TLV(tag 1B, len 2B BE, value)
   kind=1 探测/控制请求，kind=2 通告，kind=3 广播查询；TLV 0x25=MAC 0x27=主机名 0x30=IPv4
   控制：sub=5，TLV 0x40=1 → 让桌面端起 5201 服务端 */

const (
	speedUDPPort   = 3399
	soReusePortOpt = 15
)

type speedPeerInfo struct {
	Name    string
	MAC     string
	Version string
	Seen    time.Time
}

var (
	speedPeerMu   sync.Mutex
	speedPeers    = map[string]speedPeerInfo{}
	speedReadyAt  = map[string]time.Time{}
	speedLastScan time.Time
)

func speedPeer(ip string) (speedPeerInfo, bool) {
	speedPeerMu.Lock()
	defer speedPeerMu.Unlock()
	p, ok := speedPeers[ip]
	if !ok || time.Since(p.Seen) > 60*time.Second {
		return speedPeerInfo{}, false
	}
	return p, true
}

func rememberSpeedPeer(ip string, p speedPeerInfo) {
	speedPeerMu.Lock()
	p.Seen = time.Now()
	speedPeers[ip] = p
	speedPeerMu.Unlock()
}

func parseSpeedAnnounce(b []byte) (p speedPeerInfo, ok bool) {
	if len(b) < 32 || b[0] != 0xab || b[1] != 0xcd || b[2] != 0x98 || b[3] != 0x76 || b[6] != 1 {
		return p, false
	}
	payload := b[25:]
	if len(payload) < 7 || (payload[0] != 2 && payload[0] != 3) {
		return p, false
	}
	count := int(payload[6])
	off := 7
	for i := 0; i < count && off+3 <= len(payload); i++ {
		tag := payload[off]
		l := int(payload[off+1])<<8 | int(payload[off+2])
		if off+3+l > len(payload) {
			break
		}
		val := payload[off+3 : off+3+l]
		switch tag {
		case 0x25:
			if l == 6 {
				p.MAC = net.HardwareAddr(val).String()
			}
		case 0x26:
			p.Version = string(val)
		case 0x27:
			p.Name = string(val)
		}
		off += 3 + l
	}
	return p, p.Name != "" || p.MAC != ""
}

func localMACFor(ip string) net.HardwareAddr {
	target := net.ParseIP(ip)
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil
	}
	for _, ifc := range ifaces {
		if ifc.Flags&net.FlagUp == 0 || len(ifc.HardwareAddr) != 6 {
			continue
		}
		addrs, err := ifc.Addrs()
		if err != nil {
			continue
		}
		for _, a := range addrs {
			if ipnet, ok := a.(*net.IPNet); ok && target != nil && ipnet.IP.Equal(target) {
				return ifc.HardwareAddr
			}
		}
	}
	for _, ifc := range ifaces {
		if ifc.Flags&net.FlagUp == 0 || ifc.Flags&net.FlagLoopback != 0 || len(ifc.HardwareAddr) != 6 {
			continue
		}
		if strings.HasPrefix(ifc.Name, "docker") || strings.HasPrefix(ifc.Name, "br-") || strings.HasPrefix(ifc.Name, "veth") {
			continue
		}
		return ifc.HardwareAddr
	}
	return nil
}

func buildSpeedPacket(dst, src net.HardwareAddr, payload []byte) []byte {
	buf := make([]byte, 0, 25+len(payload))
	buf = append(buf, 0xab, 0xcd, 0x98, 0x76, 0x00, 0x01, 0x01)
	d := make([]byte, 6)
	copy(d, dst)
	buf = append(buf, d...)
	s := make([]byte, 6)
	copy(s, src)
	buf = append(buf, s...)
	var rpc [4]byte
	binary.BigEndian.PutUint32(rpc[:], uint32(time.Now().UnixNano()))
	buf = append(buf, rpc[:]...)
	var l [2]byte
	binary.BigEndian.PutUint16(l[:], uint16(len(payload)))
	buf = append(buf, l[:]...)
	return append(buf, payload...)
}

func speedProbePayload() []byte {
	return []byte{0x01, 0x00, 0x00, 0x0b, 0x01, 0x00, 0x00}
}

func speedControlPayload(cmd byte) []byte {
	return []byte{0x01, 0x00, 0x0b, 0x0b, 0x05, 0x00, 0x01, 0x40, 0x00, 0x01, cmd}
}

// discoverSpeedPeers 短暂占用 3399 广播探测，收集桌面端通告。
func discoverSpeedPeers(timeout time.Duration) {
	speedPeerMu.Lock()
	if time.Since(speedLastScan) < 20*time.Second {
		speedPeerMu.Unlock()
		return
	}
	speedLastScan = time.Now()
	speedPeerMu.Unlock()

	if speedUDPConn != nil {
		myMAC := localMACFor(localIPFor(""))
		if len(myMAC) == 6 {
			probe := buildSpeedPacket(net.HardwareAddr{0xff,0xff,0xff,0xff,0xff,0xff}, myMAC, speedProbePayload())
			if addr, err := net.ResolveUDPAddr("udp4", fmt.Sprintf("255.255.255.255:%d", speedUDPPort)); err == nil {
				_, _ = speedUDPConn.WriteTo(probe, addr)
			}
		}
		if timeout > 0 { time.Sleep(timeout) }
		return
	}

	lc := net.ListenConfig{Control: func(network, address string, c syscall.RawConn) error {
		c.Control(func(fd uintptr) {
			_ = syscall.SetsockoptInt(int(fd), syscall.SOL_SOCKET, syscall.SO_REUSEADDR, 1)
			_ = syscall.SetsockoptInt(int(fd), syscall.SOL_SOCKET, soReusePortOpt, 1)
			_ = syscall.SetsockoptInt(int(fd), syscall.SOL_SOCKET, syscall.SO_BROADCAST, 1)
		})
		return nil
	}}
	pc, err := lc.ListenPacket(context.Background(), "udp4", fmt.Sprintf("0.0.0.0:%d", speedUDPPort))
	if err != nil {
		logf("speed: 绑定 UDP %d 失败: %v", speedUDPPort, err)
		return
	}
	defer pc.Close()

	myMAC := localMACFor(localIPFor(""))
	if len(myMAC) == 0 {
		return
	}
	probe := buildSpeedPacket(net.HardwareAddr{0xff, 0xff, 0xff, 0xff, 0xff, 0xff}, myMAC, speedProbePayload())
	if addr, err := net.ResolveUDPAddr("udp4", fmt.Sprintf("255.255.255.255:%d", speedUDPPort)); err == nil {
		_, _ = pc.WriteTo(probe, addr)
	}

	deadline := time.Now().Add(timeout)
	buf := make([]byte, 2048)
	for {
		remain := time.Until(deadline)
		if remain <= 0 {
			break
		}
		_ = pc.SetReadDeadline(time.Now().Add(remain))
		n, addr, err := pc.ReadFrom(buf)
		if err != nil {
			break
		}
		udpAddr, ok := addr.(*net.UDPAddr)
		if !ok {
			continue
		}
		if peer, ok := parseSpeedAnnounce(buf[:n]); ok {
			rememberSpeedPeer(udpAddr.IP.String(), peer)
			logf("speed: 发现 %s name=%s mac=%s ver=%s", udpAddr.IP, peer.Name, peer.MAC, peer.Version)
		}
	}
}

// peerMACFromARP 读取邻居表里的 MAC，必要时先触发一次 ARP。
func peerMACFromARP(ip string) string {
	read := func() string {
		b, err := os.ReadFile("/proc/net/arp")
		if err != nil {
			return ""
		}
		for _, line := range strings.Split(string(b), "\n")[1:] {
			f := strings.Fields(line)
			if len(f) >= 4 && f[0] == ip && f[3] != "00:00:00:00:00:00" {
				return f[3]
			}
		}
		return ""
	}
	if mac := read(); mac != "" {
		return mac
	}
	if c, err := net.DialTimeout("tcp", net.JoinHostPort(ip, strconv.Itoa(speedPort)), 300*time.Millisecond); err == nil {
		_ = c.Close()
	}
	time.Sleep(80 * time.Millisecond)
	return read()
}

// ensureSpeedClient 让桌面端把 5201 服务端拉起来。
// 注意：不要在这里做 TCP 探测，桌面端只接受一个连接，探测会把名额占掉；
// 也不等待固定时长——iperf3 的 connect-timeout 会替我们重试 SYN，启动更快。
func ensureSpeedClient(ip string) {
	if ip == "" {
		return
	}
	var peerMAC net.HardwareAddr
	if p, ok := speedPeer(ip); ok && p.MAC != "" {
		peerMAC, _ = net.ParseMAC(p.MAC)
	}
	if len(peerMAC) == 0 {
		if mac := peerMACFromARP(ip); mac != "" {
			peerMAC, _ = net.ParseMAC(mac)
		}
	}
	if len(peerMAC) == 0 {
		return
	}
	sendSpeedControl(ip, peerMAC, 1)
	// 桌面端约 100ms 内绑定 5201；稍等一下，避免 iperf3 白等一次 SYN 重传
	time.Sleep(300 * time.Millisecond)
}

func sendSpeedControl(ip string, peerMAC net.HardwareAddr, cmd byte) {
	myIP := localIPFor(ip)
	myMAC := localMACFor(myIP)
	if len(myMAC) == 0 {
		return
	}
	conn, err := net.Dial("udp4", net.JoinHostPort(ip, strconv.Itoa(speedUDPPort)))
	if err != nil {
		logf("speed: 发送控制包失败: %v", err)
		return
	}
	defer conn.Close()
	pkt := buildSpeedPacket(peerMAC, myMAC, speedControlPayload(cmd))
	if _, err := conn.Write(pkt); err != nil {
		logf("speed: 发送控制包失败: %v", err)
		return
	}
	logf("speed: 已向 %s 发送控制指令 %d", ip, cmd)
}

var (
	speedServerCmd   *exec.Cmd
	speedServerMu    sync.Mutex
)

/* ---- 常驻 LANPerf 发现响应器 ----
   桌面端 LANPerf 通过 UDP 3399 广播查询（kind=1/3），设备需要回 kind=2 通告，
   通告里带 MAC(0x25)、版本(0x26)、主机名(0x27)、IPv4(0x30)。
   同时兼容控制包 TLV 0x40=1：要求本机把 iperf3 服务端（5201）拉起来。 */

var speedUDPConn net.PacketConn

func speedTLV(tag byte, val []byte) []byte {
	out := []byte{tag, byte(len(val) >> 8), byte(len(val))}
	return append(out, val...)
}

/* 桌面端 LANPerf 只把通告里 0x21=="MeshLAN" 的设备算作"已装 NMS"，
   所以这里的 TLV 集必须与官方一致：
   0x21 应用名 / 0x22 固定 04 / 0x23 型号 / 0x24 设备SN / 0x25 MAC /
   0x26 版本 / 0x27 主机名 / 0x28 Unix 时间戳 / 0x29 语言 /
   0x30 IPv4 / 0x31、0x32 能力位。 */
const (
	meshLanAppName = "MeshLAN"
	meshLanModel   = "SR-MESHLAN"
	appVersion     = "1.5.6"
)

var (
	machineSerialOnce sync.Once
	machineSerialVal  string
)

func machineSerial() string {
	machineSerialOnce.Do(func() {
		for _, p := range []string{"/etc/machine-id", "/var/lib/dbus/machine-id"} {
			b, err := os.ReadFile(p)
			if err != nil {
				continue
			}
			s := strings.TrimSpace(string(b))
			if len(s) >= 16 && len(s) <= 64 {
				machineSerialVal = s
				return
			}
		}
	})
	return machineSerialVal
}

func meshLanAnnounce(kind byte, seq uint16, ver, sub byte, mac net.HardwareAddr, ip, version string) []byte {
	tlvs := make([][]byte, 0, 12)
	tlvs = append(tlvs, speedTLV(0x21, []byte(meshLanAppName)))
	tlvs = append(tlvs, speedTLV(0x22, []byte{0x04}))
	tlvs = append(tlvs, speedTLV(0x23, []byte(meshLanModel)))
	if sn := machineSerial(); sn != "" {
		tlvs = append(tlvs, speedTLV(0x24, []byte(sn)))
	}
	if len(mac) == 6 {
		tlvs = append(tlvs, speedTLV(0x25, []byte(mac)))
	}
	tlvs = append(tlvs, speedTLV(0x26, []byte(version)))
	tlvs = append(tlvs, speedTLV(0x27, []byte(hostName())))
	var stamp [4]byte
	binary.BigEndian.PutUint32(stamp[:], uint32(time.Now().Unix()))
	tlvs = append(tlvs, speedTLV(0x28, stamp[:]))
	tlvs = append(tlvs, speedTLV(0x29, []byte("zh-hans")))
	if v4 := net.ParseIP(ip).To4(); v4 != nil {
		tlvs = append(tlvs, speedTLV(0x30, []byte(v4)))
	}
	tlvs = append(tlvs, speedTLV(0x31, []byte{0x01}))
	tlvs = append(tlvs, speedTLV(0x32, []byte{0x06}))
	payload := []byte{kind, byte(seq >> 8), byte(seq), ver, sub, 0x00, byte(len(tlvs))}
	for _, t := range tlvs {
		payload = append(payload, t...)
	}
	return payload
}

// meshLanVersion 是通告里 0x26 的取值，与关于页展示的版本保持一致。
func meshLanVersion() string {
	return "V" + appVersion
}

func speedParseTLVs(payload []byte) map[byte][]byte {
	out := map[byte][]byte{}
	if len(payload) < 7 {
		return out
	}
	count := int(payload[6])
	off := 7
	for i := 0; i < count && off+3 <= len(payload); i++ {
		tag := payload[off]
		l := int(payload[off+1])<<8 | int(payload[off+2])
		if off+3+l > len(payload) {
			break
		}
		out[tag] = payload[off+3 : off+3+l]
		off += 3 + l
	}
	return out
}

func startSpeedResponder() {
	lc := net.ListenConfig{Control: func(network, address string, c syscall.RawConn) error {
		c.Control(func(fd uintptr) {
			_ = syscall.SetsockoptInt(int(fd), syscall.SOL_SOCKET, syscall.SO_REUSEADDR, 1)
			_ = syscall.SetsockoptInt(int(fd), syscall.SOL_SOCKET, soReusePortOpt, 1)
			_ = syscall.SetsockoptInt(int(fd), syscall.SOL_SOCKET, syscall.SO_BROADCAST, 1)
		})
		return nil
	}}
	pc, err := lc.ListenPacket(context.Background(), "udp4", fmt.Sprintf("0.0.0.0:%d", speedUDPPort))
	if err != nil {
		logf("speed: 发现响应器绑定 UDP %d 失败: %v", speedUDPPort, err)
		return
	}
	speedUDPConn = pc
	logf("speed: LANPerf 发现响应器已就绪 (UDP %d)", speedUDPPort)
	go speedAnnounceLoop(pc)
	buf := make([]byte, 2048)
	for {
		n, addr, err := pc.ReadFrom(buf)
		if err != nil {
			continue
		}
		pkt := append([]byte(nil), buf[:n]...)
		udpAddr, ok := addr.(*net.UDPAddr)
		if !ok {
			continue
		}
		handleSpeedRequest(pc, pkt, udpAddr)
	}
}


// speedAnnounceLoop 按官方节奏每 10 秒广播一次 kind=3 通告。
// 只发 255.255.255.255，避免两个网卡各发一份把设备列表刷成两条。
func speedAnnounceLoop(pc net.PacketConn) {
	for {
		myIP := localIPFor("")
		myMAC := localMACFor(myIP)
		if len(myMAC) == 6 && myIP != "" {
			announce := meshLanAnnounce(3, 0, 0x0b, 0x03, myMAC, myIP, meshLanVersion())
			pkt := buildSpeedPacket(net.HardwareAddr{0xff, 0xff, 0xff, 0xff, 0xff, 0xff}, myMAC, announce)
			if addr, err := net.ResolveUDPAddr("udp4", fmt.Sprintf("255.255.255.255:%d", speedUDPPort)); err == nil {
				_, _ = pc.WriteTo(pkt, addr)
			}
		}
		time.Sleep(10 * time.Second)
	}
}

// speedControlReply 构造 sub=0x05 控制回执：kind=2，TLV 0x01 四字节状态码 + TLV 0x02 UTF-8 文本。
// 桌面端只认这个形状，回普通通告它会一直等到"等待设备响应超时"。
func speedControlReply(seq uint16, ver, status byte, msg string) []byte {
	tlvs := [][]byte{
		speedTLV(0x01, []byte{0, 0, 0, status}),
		speedTLV(0x02, []byte(msg)),
	}
	payload := []byte{0x02, byte(seq >> 8), byte(seq), ver, 0x05, 0x00, byte(len(tlvs))}
	for _, t := range tlvs {
		payload = append(payload, t...)
	}
	return payload
}

func handleSpeedRequest(pc net.PacketConn, pkt []byte, from *net.UDPAddr) {
	if len(pkt) < 32 || pkt[0] != 0xab || pkt[1] != 0xcd || pkt[2] != 0x98 || pkt[3] != 0x76 {
		return
	}
	payload := pkt[25:]
	if len(payload) < 7 {
		return
	}
	kind := payload[0]
	if kind == 2 || kind == 3 {
		if peer, ok := parseSpeedAnnounce(pkt); ok {
			rememberSpeedPeer(from.IP.String(), peer)
		}
	}
	// kind=3 是广播通告，只用于互相发现；1(广播查询) 和 2(点对点查询) 都要回应
	if kind != 1 && kind != 2 {
		return
	}
	tlvs := speedParseTLVs(payload)
	// sub=0x05 是测速控制请求（TLV 0x40=1：把设备端 iperf3 服务端拉起来），
	// 必须用专用回执应答，不能回发现通告。
	if payload[4] == 0x05 {
		v, ok := tlvs[0x40]
		if !ok || len(v) == 0 || v[0] != 1 {
			return
		}
		myIP := localIPFor(from.IP.String())
		if myIP == "" || from.IP.String() == myIP {
			return
		}
		myMAC := localMACFor(myIP)
		if len(myMAC) != 6 {
			return
		}
		status, msg := byte(0), "测速服务已启动"
		if !ensureSpeedServerSync() {
			status, msg = 1, "测速服务启动超时，端口未就绪"
		}
		seq := uint16(payload[1])<<8 | uint16(payload[2])
		dst := net.HardwareAddr(pkt[13:19])
		if len(dst) != 6 || dst.String() == "00:00:00:00:00:00" || dst.String() == "ff:ff:ff:ff:ff:ff" {
			dst = net.HardwareAddr{0xff, 0xff, 0xff, 0xff, 0xff, 0xff}
		}
		reply := buildSpeedPacket(dst, myMAC, speedControlReply(seq, payload[3], status, msg))
		copy(reply[19:23], pkt[19:23])
		_, _ = pc.WriteTo(reply, from)
		logf("speed: 测速控制请求 %s → 状态 %d（%s）", from.IP, status, msg)
		return
	}
	if v, ok := tlvs[0x40]; ok && len(v) > 0 && v[0] == 1 {
		go ensureSpeedServer()
	}
	// 忽略本机自己的探测包
	myIP := localIPFor(from.IP.String())
	if myIP == "" || from.IP.String() == myIP {
		return
	}
	myMAC := localMACFor(myIP)
	if len(myMAC) != 6 {
		return
	}
	dst := net.HardwareAddr(pkt[13:19])
	if len(dst) != 6 || dst.String() == "00:00:00:00:00:00" || dst.String() == "ff:ff:ff:ff:ff:ff" {
		dst = net.HardwareAddr{0xff, 0xff, 0xff, 0xff, 0xff, 0xff}
	}
	ver := payload[3]
	seq := uint16(payload[1])<<8 | uint16(payload[2])
	announce := meshLanAnnounce(2, seq, ver, 0x01, myMAC, myIP, meshLanVersion())
	reply := buildSpeedPacket(dst, myMAC, announce)
	copy(reply[19:23], pkt[19:23])
	_, _ = pc.WriteTo(reply, from)
}

// ensureSpeedServer 在 5201 空闲时拉起本机 iperf3 服务端；
// 已有服务（容器或其他应用）占用时直接复用。
func ensureSpeedServer() {
	_ = ensureSpeedServerSync()
}

// ensureSpeedServerSync 同步拉起本机 iperf3 服务端，返回 5201 是否就绪。
// 已有服务（容器或其他应用）在监听时直接视为就绪——桌面端只要求对端有 iperf3，
// 不关心是谁起的。
func ensureSpeedServerSync() bool {
	if tcpOpen("127.0.0.1", speedPort, 300*time.Millisecond) {
		return true
	}
	bin := iperfBinary()
	if bin == "" {
		logf("speed: 未找到 iperf3，无法启动测速服务端")
		return false
	}
	logPath := filepath.Join(appHome(), "var", "iperf3.log")
	f, err := os.OpenFile(logPath, os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o644)
	if err != nil {
		f = nil
	}
	cmd := exec.Command(bin, "-s", "-p", strconv.Itoa(speedPort))
	if f != nil {
		cmd.Stdout = f
		cmd.Stderr = f
	}
	if err := cmd.Start(); err != nil {
		logf("speed: 启动 iperf3 服务端失败: %v", err)
		if f != nil {
			_ = f.Close()
		}
		return false
	}
	speedServerMu.Lock()
	speedServerCmd = cmd
	speedServerMu.Unlock()
	logf("speed: iperf3 服务端已启动 pid=%d", cmd.Process.Pid)
	go func() {
		_ = cmd.Wait()
		if f != nil {
			_ = f.Close()
		}
	}()
	deadline := time.Now().Add(4 * time.Second)
	for time.Now().Before(deadline) {
		if tcpOpen("127.0.0.1", speedPort, 200*time.Millisecond) {
			return true
		}
		time.Sleep(150 * time.Millisecond)
	}
	return false
}

// localIPFor 取本机到目标地址所使用的出口 IP。
func localIPFor(remote string) string {
	if remote != "" {
		if c, err := net.Dial("udp", net.JoinHostPort(remote, "9")); err == nil {
			ip := c.LocalAddr().(*net.UDPAddr).IP
			_ = c.Close()
			if ip != nil && !ip.IsLoopback() {
				return ip.String()
			}
		}
	}
	return firstLANIPv4()
}

func firstLANIPv4() string {
	addrs, err := net.InterfaceAddrs()
	if err != nil {
		return ""
	}
	fallback := ""
	for _, a := range addrs {
		ipnet, ok := a.(*net.IPNet)
		if !ok || ipnet.IP.To4() == nil || ipnet.IP.IsLoopback() {
			continue
		}
		ip := ipnet.IP.String()
		if strings.HasPrefix(ip, "192.168.") || strings.HasPrefix(ip, "10.") {
			return ip
		}
		if fallback == "" {
			fallback = ip
		}
	}
	return fallback
}

func lookupName(ip string) string {
	ctx, cancel := context.WithTimeout(context.Background(), 1200*time.Millisecond)
	defer cancel()
	names, err := net.DefaultResolver.LookupAddr(ctx, ip)
	if err != nil || len(names) == 0 {
		return ""
	}
	return strings.TrimSuffix(names[0], ".")
}

func iperfBinary() string {
	exe, err := os.Executable()
	if err != nil {
		exe = os.Args[0]
	}
	candidates := []string{
		filepath.Join(filepath.Dir(exe), "bin", "iperf3"),
		filepath.Join("/usr/local/apps/@appcenter", appName(), "server", "bin", "iperf3"),
		"/usr/bin/iperf3", "/usr/local/bin/iperf3", "/opt/bin/iperf3", "/bin/iperf3",
	}
	for _, p := range candidates {
		if fi, err := os.Stat(p); err == nil && !fi.IsDir() {
			return p
		}
	}
	if p, err := exec.LookPath("iperf3"); err == nil {
		return p
	}
	return ""
}

// speedDiscovered 汇总两端信息：NAS 侧本机地址与 iperf3 状态、客户端地址与状态。
func speedDiscovered(clientIP string) map[string]interface{} {
	// 前端取不到自身 IP 时（myip 不可达、页面缓存等），退回最近发现的桌面端
	if clientIP == "" {
		if ip, ok := latestSpeedPeer(); ok {
			clientIP = ip
			logf("speed: 未带 ip 参数，使用最近发现的桌面端 %s", ip)
		} else {
			discoverSpeedPeers(700 * time.Millisecond)
			if ip, ok := latestSpeedPeer(); ok {
				clientIP = ip
				logf("speed: 未带 ip 参数，扫描后使用 %s", ip)
			}
		}
	}
	nasIP := localIPFor(clientIP)
	// NAS 侧始终由本应用自己发起 iperf3 客户端，只要二进制在就算就绪，
	// 不再要求 5201 被监听。
	nas := speedNode{
		IP: nasIP, Name: hostName(), Port: speedPort,
		Iperf3Enable: iperfBinary() != "",
	}
	local := speedNode{IP: clientIP, Port: speedPort}
	if clientIP != "" {
		peer, ok := speedPeer(clientIP)
		if !ok {
			discoverSpeedPeers(700 * time.Millisecond)
			peer, ok = speedPeer(clientIP)
		}
		if ok {
			local.Name = peer.Name
			local.MAC = peer.MAC
		}
		if local.Name == "" {
			local.Name = lookupName(clientIP)
		}
		// 装了 LANPerf 桌面端就算就绪：5201 由控制指令按需拉起
		local.Iperf3Enable = ok || tcpOpen(clientIP, speedPort, 1200*time.Millisecond)
	}
	return map[string]interface{}{"local": local, "nas": nas}
}

// latestSpeedPeer 返回最近一次通告的客户端地址。
func latestSpeedPeer() (string, bool) {
	speedPeerMu.Lock()
	defer speedPeerMu.Unlock()
	bestIP := ""
	var bestAt time.Time
	for ip, p := range speedPeers {
		if time.Since(p.Seen) > 90*time.Second {
			continue
		}
		if bestIP == "" || p.Seen.After(bestAt) {
			bestIP, bestAt = ip, p.Seen
		}
	}
	return bestIP, bestIP != ""
}

func hostName() string {
	if n, err := os.Hostname(); err == nil {
		return n
	}
	return "fnOS"
}

type lanperfPkg struct {
	Path    string
	Version string
	Updated string
	Size    string
}

var lanperfNameRe = regexp.MustCompile(`LANPerf_\w+?_(V[\d.]+)-(\d{6})`)

func lanperfPackage(platform string) (lanperfPkg, bool) {
	dir := wwwDir()
	entries, err := os.ReadDir(dir)
	if err != nil {
		return lanperfPkg{}, false
	}
	want := "Win"
	if platform == "mac" {
		want = "Mac"
	}
	for _, e := range entries {
		name := e.Name()
		if !strings.HasPrefix(name, "LANPerf_") || !strings.Contains(name, want) {
			continue
		}
		fi, err := e.Info()
		if err != nil {
			continue
		}
		pkg := lanperfPkg{Path: filepath.Join(dir, name), Size: fmt.Sprintf("%.2fM", float64(fi.Size())/(1024*1024))}
		if m := lanperfNameRe.FindStringSubmatch(name); m != nil {
			pkg.Version, pkg.Updated = m[1], m[2]
		}
		return pkg, true
	}
	return lanperfPkg{}, false
}

// clientIP 解析请求来源地址，供 /api/myip 回显。
func clientIP(r *http.Request) string {
	if xff := r.Header.Get("X-Forwarded-For"); xff != "" {
		if i := strings.Index(xff, ","); i > 0 {
			return strings.TrimSpace(xff[:i])
		}
		return strings.TrimSpace(xff)
	}
	if host, _, err := net.SplitHostPort(r.RemoteAddr); err == nil {
		return host
	}
	return r.RemoteAddr
}

/* ------------------------------------------------------------------ */
/* 诊断工具：Ping / DNS / 端口扫描                                       */
/* ------------------------------------------------------------------ */

func tcpRTT(host string, port int, timeout time.Duration) (int64, bool) {
	start := time.Now()
	c, err := net.DialTimeout("tcp", net.JoinHostPort(host, strconv.Itoa(port)), timeout)
	if err != nil {
		return 0, false
	}
	ms := time.Since(start).Milliseconds()
	_ = c.Close()
	return ms, true
}

// toolsPing 用 TCP 握手近似 ICMP：容器里没有 raw socket 权限，发不了 ICMP。
func toolsPing(host string, count int) map[string]interface{} {
	host = strings.TrimSpace(host)
	if host == "" {
		return map[string]interface{}{"output": "请输入目标主机", "ok": false}
	}
	if count <= 0 || count > 20 {
		count = 4
	}
	probePorts := []int{80, 443, 22, 5666, 5201}
	lines := []string{}
	ok := 0
	var min, max, sum int64
	min = -1
	for i := 0; i < count; i++ {
		var rtt int64
		used := 0
		found := false
		for _, p := range probePorts {
			if v, good := tcpRTT(host, p, 1200*time.Millisecond); good {
				rtt, used, found = v, p, true
				break
			}
		}
		if found {
			ok++
			if min < 0 || rtt < min {
				min = rtt
			}
			if rtt > max {
				max = rtt
			}
			sum += rtt
			lines = append(lines, fmt.Sprintf("来自 %s 端口 %d 的响应: 时间=%dms", host, used, rtt))
		} else {
			lines = append(lines, fmt.Sprintf("请求 %s 超时", host))
		}
	}
	lines = append(lines, "", fmt.Sprintf("--- %s 连通性统计 ---", host))
	lines = append(lines, fmt.Sprintf("发往 %s 的探测: %d 次，成功 %d 次，失败 %d 次，丢包率 %d%%",
		host, count, ok, count-ok, int(float64(count-ok)/float64(count)*100)))
	if ok > 0 {
		lines = append(lines, fmt.Sprintf("往返时间 最小=%dms 最大=%dms 平均=%dms", min, max, sum/int64(ok)))
	}
	lines = append(lines, "", "说明：应用内无法发送 ICMP，这里用 TCP 握手近似测量可达性与时延。")
	return map[string]interface{}{"output": strings.Join(lines, "\n"), "ok": ok > 0}
}

func toolsDNS(name string) map[string]interface{} {
	name = strings.TrimSpace(name)
	if name == "" {
		return map[string]interface{}{"output": "请输入域名", "ok": false}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	lines := []string{}
	add := func(label string, vals []string, err error) {
		if err != nil {
			lines = append(lines, fmt.Sprintf("%s: %v", label, err))
			return
		}
		if len(vals) == 0 {
			lines = append(lines, fmt.Sprintf("%s: (空)", label))
			return
		}
		lines = append(lines, fmt.Sprintf("%s: %s", label, strings.Join(vals, ", ")))
	}
	if ips, err := net.DefaultResolver.LookupHost(ctx, name); err == nil {
		v4, v6 := []string{}, []string{}
		for _, ip := range ips {
			if strings.Contains(ip, ":") {
				v6 = append(v6, ip)
			} else {
				v4 = append(v4, ip)
			}
		}
		add("A    ", v4, nil)
		add("AAAA ", v6, nil)
	} else {
		add("A/AAAA", nil, err)
	}
	if cname, err := net.DefaultResolver.LookupCNAME(ctx, name); err == nil {
		add("CNAME", []string{cname}, nil)
	} else {
		add("CNAME", nil, err)
	}
	if mxs, err := net.DefaultResolver.LookupMX(ctx, name); err == nil {
		out := []string{}
		for _, mx := range mxs {
			out = append(out, fmt.Sprintf("%s (优先 %d)", mx.Host, mx.Pref))
		}
		add("MX   ", out, nil)
	} else {
		add("MX   ", nil, err)
	}
	if nss, err := net.DefaultResolver.LookupNS(ctx, name); err == nil {
		out := []string{}
		for _, ns := range nss {
			out = append(out, ns.Host)
		}
		add("NS   ", out, nil)
	} else {
		add("NS   ", nil, err)
	}
	return map[string]interface{}{"output": strings.Join(lines, "\n"), "ok": true}
}

func parsePortSpec(spec string) []int {
	seen := map[int]bool{}
	out := []int{}
	for _, part := range strings.Split(spec, ",") {
		seg := strings.TrimSpace(part)
		if seg == "" {
			continue
		}
		if i := strings.Index(seg, "-"); i > 0 {
			a, err1 := strconv.Atoi(strings.TrimSpace(seg[:i]))
			b, err2 := strconv.Atoi(strings.TrimSpace(seg[i+1:]))
			if err1 != nil || err2 != nil {
				continue
			}
			if a > b {
				a, b = b, a
			}
			for p := a; p <= b && p < 65536; p++ {
				if p > 0 && !seen[p] {
					seen[p] = true
					out = append(out, p)
				}
			}
			continue
		}
		if p, err := strconv.Atoi(seg); err == nil && p > 0 && p < 65536 && !seen[p] {
			seen[p] = true
			out = append(out, p)
		}
	}
	sort.Ints(out)
	return out
}

// toolsPortScan 刻意限制规模：最多 1024 个端口、并发 20，避免压垮便宜设备。
func toolsPortScan(host, spec string) map[string]interface{} {
	host = strings.TrimSpace(host)
	if host == "" {
		return map[string]interface{}{"output": "请输入目标主机", "ok": false}
	}
	if strings.TrimSpace(spec) == "" {
		spec = "1-1024"
	}
	list := parsePortSpec(spec)
	if len(list) == 0 {
		return map[string]interface{}{"output": "端口范围无效", "ok": false}
	}
	if len(list) > 1024 {
		list = list[:1024]
	}
	start := time.Now()
	sem := make(chan struct{}, 20)
	var mu sync.Mutex
	open := []int{}
	var wg sync.WaitGroup
	for _, p := range list {
		wg.Add(1)
		sem <- struct{}{}
		go func(port int) {
			defer wg.Done()
			defer func() { <-sem }()
			if _, ok := tcpRTT(host, port, 700*time.Millisecond); ok {
				mu.Lock()
				open = append(open, port)
				mu.Unlock()
			}
		}(p)
	}
	wg.Wait()
	sort.Ints(open)
	lines := []string{fmt.Sprintf("扫描 %s 的 %d 个端口，耗时 %dms", host, len(list), time.Since(start).Milliseconds())}
	if len(open) == 0 {
		lines = append(lines, "未发现开放端口")
	} else {
		lines = append(lines, fmt.Sprintf("开放端口（%d 个）：", len(open)))
		for _, p := range open {
			lines = append(lines, fmt.Sprintf("  %d/tcp  开放", p))
		}
	}
	return map[string]interface{}{"output": strings.Join(lines, "\n"), "ok": true, "open": open, "scanned": len(list)}
}

/* ---- 路由追踪 ---- */

// toolsTraceroute 调用系统的 traceroute；只接受安全字符，避免参数注入。
func toolsTraceroute(host string, maxHops, probes int) map[string]interface{} {
	host = strings.TrimSpace(host)
	if host == "" {
		return map[string]interface{}{"output": "请输入目标主机", "ok": false}
	}
	for _, r := range host {
		if !(r == '.' || r == ':' || r == '-' || r == '_' ||
			(r >= '0' && r <= '9') || (r >= 'a' && r <= 'z') || (r >= 'A' && r <= 'Z')) {
			return map[string]interface{}{"output": "目标主机包含非法字符", "ok": false}
		}
	}
	if strings.HasPrefix(host, "-") {
		return map[string]interface{}{"output": "目标主机不合法", "ok": false}
	}
	if maxHops <= 0 || maxHops > 30 {
		maxHops = 20
	}
	if probes <= 0 || probes > 3 {
		probes = 1
	}
	bin := ""
	for _, p := range []string{"/usr/sbin/traceroute", "/usr/bin/traceroute", "/sbin/traceroute"} {
		if _, err := os.Stat(p); err == nil {
			bin = p
			break
		}
	}
	if bin == "" {
		return map[string]interface{}{"output": "系统未安装 traceroute", "ok": false}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	args := []string{"-n", "-w", "1", "-q", strconv.Itoa(probes), "-m", strconv.Itoa(maxHops), host}
	out, err := exec.CommandContext(ctx, bin, args...).CombinedOutput()
	text := strings.TrimRight(string(out), "\n")
	if text == "" && err != nil {
		text = "追踪失败：" + err.Error()
	}
	return map[string]interface{}{"output": text, "ok": err == nil}
}

/* ---- 抓包分析（AF_PACKET + pcap） ---- */

type captureState struct {
	mu      sync.Mutex
	running bool
}

var capture = &captureState{}

func htons(v uint16) uint16 { return v<<8 | v>>8 }

func pcapHeader(snap int) []byte {
	b := make([]byte, 24)
	binary.LittleEndian.PutUint32(b[0:], 0xa1b2c3d4)
	binary.LittleEndian.PutUint16(b[4:], 2)
	binary.LittleEndian.PutUint16(b[6:], 4)
	binary.LittleEndian.PutUint32(b[16:], uint32(snap))
	binary.LittleEndian.PutUint32(b[20:], 1) // Ethernet
	return b
}

func pcapRecord(buf []byte, ts time.Time, pkt []byte, origLen int) []byte {
	h := make([]byte, 16)
	binary.LittleEndian.PutUint32(h[0:], uint32(ts.Unix()))
	binary.LittleEndian.PutUint32(h[4:], uint32(ts.Nanosecond()/1000))
	binary.LittleEndian.PutUint32(h[8:], uint32(len(pkt)))
	binary.LittleEndian.PutUint32(h[12:], uint32(origLen))
	buf = append(buf, h...)
	return append(buf, pkt...)
}

// toolsCapture 在指定网卡上抓 N 秒流量，返回 pcap 内容。
func toolsCapture(iface string, seconds, snap int) (data []byte, packets int, err error) {
	if seconds <= 0 || seconds > 30 {
		seconds = 10
	}
	if snap <= 0 || snap > 65535 {
		snap = 65535
	}
	ifi, e := net.InterfaceByName(iface)
	if e != nil {
		return nil, 0, fmt.Errorf("网卡不存在: %s", iface)
	}
	capture.mu.Lock()
	if capture.running {
		capture.mu.Unlock()
		return nil, 0, fmt.Errorf("已有抓包任务在进行中")
	}
	capture.running = true
	capture.mu.Unlock()
	defer func() {
		capture.mu.Lock()
		capture.running = false
		capture.mu.Unlock()
	}()

	fd, e := syscall.Socket(syscall.AF_PACKET, syscall.SOCK_RAW, int(htons(syscall.ETH_P_ALL)))
	if e != nil {
		return nil, 0, fmt.Errorf("打开原始套接字失败（需要 root）: %v", e)
	}
	defer syscall.Close(fd)
	sa := &syscall.SockaddrLinklayer{Protocol: htons(syscall.ETH_P_ALL), Ifindex: ifi.Index}
	if e := syscall.Bind(fd, sa); e != nil {
		return nil, 0, fmt.Errorf("绑定网卡失败: %v", e)
	}
	tv := syscall.NsecToTimeval(int64(400 * time.Millisecond))
	_ = syscall.SetsockoptTimeval(fd, syscall.SOL_SOCKET, syscall.SO_RCVTIMEO, &tv)

	data = pcapHeader(snap)
	buf := make([]byte, snap)
	deadline := time.Now().Add(time.Duration(seconds) * time.Second)
	for time.Now().Before(deadline) {
		n, _, e := syscall.Recvfrom(fd, buf, 0)
		if e != nil {
			if e == syscall.EAGAIN || e == syscall.EWOULDBLOCK || e == syscall.EINTR {
				continue
			}
			break
		}
		if n <= 0 {
			continue
		}
		if n > snap {
			n = snap
		}
		data = pcapRecord(data, time.Now(), buf[:n], n)
		packets++
	}
	return data, packets, nil
}

// toolsInterfaces 列出可抓包网卡。
func toolsInterfaces() map[string]interface{} {
	ifaces, _ := net.Interfaces()
	list := make([]map[string]interface{}, 0, len(ifaces))
	for _, i := range ifaces {
		addrs := []string{}
		if as, err := i.Addrs(); err == nil {
			for _, a := range as {
				addrs = append(addrs, a.String())
			}
		}
		list = append(list, map[string]interface{}{
			"name": i.Name, "mac": i.HardwareAddr.String(),
			"up": i.Flags&net.FlagUp != 0, "loopback": i.Flags&net.FlagLoopback != 0,
			"addrs": addrs,
		})
	}
	return map[string]interface{}{"list": list}
}

/* ---- 网络配置 / 重启 / 固件升级 ---- */

// networkConfig 读取设备 IP 设置（IpSettingRpm.htm 的 ip_ds）。
func (m *mercury) networkConfig() (map[string]interface{}, error) {
	page, err := m.readPage("/IpSettingRpm.htm")
	if err != nil {
		return nil, err
	}
	ds := extractObject(page, "ip_ds")
	if ds == nil {
		return nil, fmt.Errorf("读取 IP 配置失败")
	}
	state := scalarInt(ds["state"]) // IpSettingRpm.htm: 1==ip_ds.state?enable:disable
	method := "static"
	if state == 1 {
		method = "dynamic"
	}
	vlan := 1
	if s := extractNumber(page, "manage_vlan"); s != "" {
		if n, e := strconv.Atoi(s); e == nil {
			vlan = n
		}
	}
	dns := strSlice(ds["dnsStr"])
	if dns == nil {
		if one := firstStr(ds["dnsStr"]); one != "" {
			dns = []string{one}
		} else {
			dns = []string{}
		}
	}
	return map[string]interface{}{
		"method":  method,
		"address": firstStr(ds["ipStr"]),
		"mask":    firstStr(ds["netmaskStr"]),
		"gateway": firstStr(ds["gatewayStr"]),
		"dns":     dns,
		// dnsFlag==0 的固件（本机即是）不渲染 DNS 输入框，交换机自带页面也不下发 DNS。
		"dns_editable": strings.Contains(page, "var dnsFlag = 1"),
		"manage_vlan":  vlan,
	}, nil
}

// setNetworkConfig 下发 IP 设置；改 IP 会立刻切走，调用方需自行处理失联。
func (m *mercury) setNetworkConfig(method, address, mask, gateway string, dns []string) error {
	address = strings.TrimSpace(address)
	mask = strings.TrimSpace(mask)
	gateway = strings.TrimSpace(gateway)
	if method != "dynamic" {
		if net.ParseIP(address).To4() == nil {
			return fmt.Errorf("IP 地址不合法：%s", address)
		}
		if net.ParseIP(mask).To4() == nil {
			return fmt.Errorf("子网掩码不合法：%s", mask)
		}
	}
	vlan := 1
	if page, err := m.readPage("/IpSettingRpm.htm"); err == nil {
		if s := extractNumber(page, "manage_vlan"); s != "" {
			if n, e := strconv.Atoi(s); e == nil {
				vlan = n
			}
		}
	}
	v := url.Values{}
	v.Set("manage_vlan", strconv.Itoa(vlan))
	if method == "dynamic" {
		v.Set("dhcpSetting", "enable")
	} else {
		v.Set("dhcpSetting", "disable")
	}
	v.Set("ip_address", address)
	v.Set("ip_netmask", mask)
	v.Set("ip_gateway", gateway)
	// 本机固件 dnsFlag=0，交换机自带表单不下发 DNS；仅在确有地址时附带。
	if len(dns) > 0 {
		v.Set("dns_enable", "enable")
		v.Set("dns_addr", strings.Join(dns, ","))
	}
	return m.writeCGI("/ip_setting.cgi", v)
}

// postCGI 用于只接受 POST 的接口（重启、复位）。
func (m *mercury) postCGI(path string, fields url.Values) error {
	if err := m.ensure(); err != nil {
		return err
	}
	fields.Set("token", m.token)
	req, err := http.NewRequest("POST", m.base()+path, strings.NewReader(fields.Encode()))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("User-Agent", "meshlan-compat/1.0")
	if m.cookie != "" {
		req.Header.Set("Cookie", m.cookie)
	}
	res, err := m.http.Do(req)
	if err != nil {
		return err
	}
	defer res.Body.Close()
	_, _ = io.ReadAll(res.Body)
	m.token = ""
	invalidatePages(m.host, m.port)
	return nil
}

// rebootDevice 重启交换机，可选择先保存配置。
func (m *mercury) rebootDevice(save bool) error {
	v := url.Values{}
	v.Set("reboot_op", "reboot")
	if save {
		v.Set("save_op", "true")
	} else {
		v.Set("save_op", "false")
	}
	return m.postCGI("/reboot.cgi", v)
}

// postCGIText 与 postCGI 相同，但返回响应正文（Conflict 检查需要读内容）。
func (m *mercury) postCGIText(path string) (string, error) {
	if err := m.ensure(); err != nil {
		return "", err
	}
	req, err := http.NewRequest("POST", m.base()+path, strings.NewReader(""))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/x-www-form-urlencoded")
	req.Header.Set("User-Agent", "meshlan-compat/1.0")
	if m.cookie != "" {
		req.Header.Set("Cookie", m.cookie)
	}
	res, err := m.http.Do(req)
	if err != nil {
		return "", err
	}
	defer res.Body.Close()
	body, _ := io.ReadAll(res.Body)
	return string(body), nil
}

// upgradeBusy 读取 upgradeConflict.cgi；返回 true 表示设备正在升级中。
func (m *mercury) upgradeBusy() bool {
	text, err := m.postCGIText("/upgradeConflict.cgi?token=" + url.QueryEscape(m.token))
	if err != nil {
		return false
	}
	// 页面按 indexOf("value")+7 取值，响应形如 value="0"
	if i := strings.Index(text, "value"); i >= 0 {
		for _, ch := range text[i+5:] {
			if ch == '0' || ch == '1' || ch == '2' {
				return ch == '1'
			}
		}
	}
	return false
}

// upgradeFirmware 把固件推给 httpupg.cgi（与设备自带本地升级页同一接口），
// 设备校验通过后会自行重启，调用方不要再发其它指令。
func (m *mercury) upgradeFirmware(filename string, data []byte) error {
	name := filepath.Base(strings.ReplaceAll(filename, "\\", "/"))
	if !strings.EqualFold(filepath.Ext(name), ".bin") {
		return fmt.Errorf("交换机只接受 .bin 固件文件，当前文件：%s", name)
	}
	if len(name) > 63 {
		return fmt.Errorf("固件文件名需少于 64 个字符")
	}
	if len(data) == 0 {
		return fmt.Errorf("固件文件为空")
	}
	if len(data) > 100<<20 {
		return fmt.Errorf("固件文件超过 100MB")
	}
	if err := m.ensure(); err != nil {
		return err
	}
	if m.upgradeBusy() {
		return fmt.Errorf("设备正在升级中，请等待完成后再试")
	}
	// 上传大文件不能套用 8s 的默认超时
	return m.uploadMultipart("/httpupg.cgi?cmd=fw_upgrade&token=", "firmware", name, data, 6*time.Minute)
}
