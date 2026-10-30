package main

// 设备高级功能的只读/写入封装：端口统计、线缆检测、镜像、隔离、环回保护、
// QoS、VLAN 扩展（MTU / 端口 VLAN / PVID）、配置备份与还原、恢复出厂。
// 全部按设备自带页面的 CGI 与参数名逐字实现。

import (
	"bytes"
	"fmt"
	"io"
	"mime/multipart"
	"net/http"
	"net/url"
	"path/filepath"
	"regexp"
	"strconv"
	"strings"
	"time"
)

/* ---------- 解析辅助 ---------- */

// extractNumArray 解析 `var x = new Array(...)` 与 `var x = [...]` 两种写法（含 0x 十六进制）。
func extractNumArray(text, name string) []float64 {
	re := regexp.MustCompile(`var\s+` + regexp.QuoteMeta(name) + `\s*=\s*(?:new\s+Array\s*)?([\[\(])`)
	loc := re.FindStringSubmatchIndex(text)
	if loc == nil {
		return nil
	}
	closeCh := "]"
	if text[loc[2]] == '(' {
		closeCh = ")"
	}
	start := loc[3]
	end := strings.Index(text[start:], closeCh)
	if end < 0 {
		return nil
	}
	out := []float64{}
	for _, f := range strings.Split(text[start:start+end], ",") {
		f = strings.TrimSpace(strings.Trim(f, `"'`))
		if f == "" {
			continue
		}
		if strings.HasPrefix(f, "0x") || strings.HasPrefix(f, "0X") {
			if n, err := strconv.ParseInt(f[2:], 16, 64); err == nil {
				out = append(out, float64(n))
				continue
			}
			out = append(out, 0)
			continue
		}
		if n, err := strconv.ParseFloat(f, 64); err == nil {
			out = append(out, n)
		} else {
			out = append(out, 0)
		}
	}
	return out
}

func numAt(a []float64, i int) int {
	if i >= 0 && i < len(a) {
		return int(a[i])
	}
	return 0
}

func paramIntPtr(p map[string]interface{}, key string) *int {
	if p == nil {
		return nil
	}
	switch x := p[key].(type) {
	case float64:
		v := int(x)
		return &v
	case string:
		if n, err := strconv.Atoi(strings.TrimSpace(x)); err == nil {
			return &n
		}
	}
	return nil
}

func paramBoolPtr(p map[string]interface{}, key string) *bool {
	if p == nil {
		return nil
	}
	if v, ok := p[key].(bool); ok {
		return &v
	}
	return nil
}

func maskPorts(mask, n int) []int {
	out := []int{}
	for i := 0; i < n; i++ {
		if mask&(1<<uint(i)) != 0 {
			out = append(out, i+1)
		}
	}
	return out
}

// cgiSlow 用于耗时较长的 CGI（线缆检测、配置备份），默认客户端只有 8 秒。
func (m *mercury) cgiSlow(path string, f url.Values, timeout time.Duration) ([]byte, error) {
	if err := m.ensure(); err != nil {
		return nil, err
	}
	if f != nil {
		f.Set("token", m.token)
		path = path + "?" + f.Encode()
	}
	req, err := http.NewRequest("GET", m.base()+path, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("User-Agent", "meshlan-compat/1.0")
	if m.cookie != "" {
		req.Header.Set("Cookie", m.cookie)
	}
	client := &http.Client{Timeout: timeout}
	res, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer res.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(res.Body, 32<<20))
	m.token = ""
	invalidatePages(m.host, m.port)
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return nil, fmt.Errorf("设备返回 HTTP %d", res.StatusCode)
	}
	return body, nil
}

/* ---------- 端口统计 ---------- */

func (m *mercury) portStatistics() (map[string]interface{}, error) {
	page, err := m.readPage("/PortStatisticsRpm.htm")
	if err != nil {
		return nil, err
	}
	ds := extractObject(page, "all_info")
	if ds == nil {
		return nil, fmt.Errorf("读取端口统计失败")
	}
	n := 6
	if s := extractNumber(page, "max_port_num"); s != "" {
		if v, e := strconv.Atoi(s); e == nil && v > 0 {
			n = v
		}
	}
	state := numSlice(ds["state"])
	link := numSlice(ds["link_status"])
	pkts := numSlice(ds["pkts"])
	list := make([]map[string]interface{}, 0, n)
	for i := 0; i < n; i++ {
		linkCode := numAt(link, i)
		list = append(list, map[string]interface{}{
			"port":       i + 1,
			"enabled":    numAt(state, i) == 1,
			"link":       linkCode > 1,
			"link_code":  linkCode,
			"link_text":  speedLabels[linkCode],
			"tx_good":    int64(numAtF(pkts, 4*i)),
			"tx_bad":     int64(numAtF(pkts, 4*i+1)),
			"rx_good":    int64(numAtF(pkts, 4*i+2)),
			"rx_bad":     int64(numAtF(pkts, 4*i+3)),
		})
	}
	return map[string]interface{}{"ports": list, "count": n}, nil
}

func numAtF(a []float64, i int) float64 {
	if i >= 0 && i < len(a) {
		return a[i]
	}
	return 0
}

func (m *mercury) clearStatistics() error {
	return m.writeCGI("/port_statistics_set.cgi", url.Values{})
}

/* ---------- 线缆检测 ---------- */

var cableStates = map[int]string{
	-1: "--", 0: "无线缆", 1: "正常", 2: "开路", 3: "短路",
	4: "开路&短路", 5: "阻抗失配", 6: "不支持线缆检测",
}

func (m *mercury) cableDiag() (map[string]interface{}, error) {
	page, err := m.readPage("/CableDiagRpm.htm")
	if err != nil {
		return nil, err
	}
	n := 6
	if s := extractNumber(page, "maxPort"); s != "" {
		if v, e := strconv.Atoi(s); e == nil && v > 0 {
			n = v
		}
	}
	states := extractNumArray(page, "cablestate")
	lengths := extractNumArray(page, "cablelength")
	types := extractNumArray(page, "port_type")
	disabled := extractNumArray(page, "portdisableid")
	list := make([]map[string]interface{}, 0, n)
	for p := 1; p <= n; p++ {
		st := -1
		if p-1 < len(states) {
			st = int(states[p-1])
		}
		ln := -1
		if p-1 < len(lengths) {
			ln = int(lengths[p-1])
		}
		pt := 0
		if p-1 < len(types) {
			pt = int(types[p-1])
		}
		// 设备页面用 portdisableid[端口号]（1 基）判断，这里保持同样口径
		supported := true
		if p < len(disabled) && disabled[p] == 0 {
			supported = false
		}
		text := cableStates[st]
		if text == "" {
			text = "其他"
		}
		list = append(list, map[string]interface{}{
			"port": p, "fiber": pt == 1, "supported": supported,
			"state": st, "state_text": text,
			"length": ln, "length_valid": ln >= 0,
		})
	}
	return map[string]interface{}{"ports": list, "count": n}, nil
}

func (m *mercury) cableDiagRun(ports []int) error {
	if len(ports) == 0 {
		return fmt.Errorf("请至少选择一个端口")
	}
	f := url.Values{}
	for _, p := range ports {
		if p < 1 || p > 24 {
			return fmt.Errorf("端口号不合法：%d", p)
		}
		f.Add(fmt.Sprintf("chk_%d", p), strconv.Itoa(p))
	}
	_, err := m.cgiSlow("/cable_diag_get.cgi", f, 120*time.Second)
	return err
}

/* ---------- 端口镜像 ---------- */

func (m *mercury) mirrorConfig() (map[string]interface{}, error) {
	page, err := m.readPage("/PortMirrorRpm.htm")
	if err != nil {
		return nil, err
	}
	en, _ := strconv.Atoi(orZero(extractNumber(page, "MirrEn")))
	mon, _ := strconv.Atoi(orZero(extractNumber(page, "MirrPort")))
	info := extractObject(page, "mirr_info")
	ingress := []int{}
	egress := []int{}
	if info != nil {
		for _, v := range numSlice(info["ingress"]) {
			ingress = append(ingress, int(v))
		}
		for _, v := range numSlice(info["egress"]) {
			egress = append(egress, int(v))
		}
	}
	// 设备的 mirr_info 数组长度固定为 8，按实际端口数截断
	n := 6
	if s := extractNumber(page, "max_port_num"); s != "" {
		if v, e := strconv.Atoi(s); e == nil && v > 0 && v < len(ingress) {
			n = v
		}
	}
	if len(ingress) < n {
		n = len(ingress)
	}
	ports := make([]map[string]interface{}, 0, n)
	for i := 0; i < n; i++ {
		ports = append(ports, map[string]interface{}{
			"port": i + 1, "ingress": ingress[i] == 1, "egress": i < len(egress) && egress[i] == 1,
		})
	}
	return map[string]interface{}{
		"enabled": en == 1, "monitor_port": mon, "sources": ports,
	}, nil
}

func orZero(s string) string {
	if s == "" {
		return "0"
	}
	return s
}

func (m *mercury) mirrorEnableSet(enabled bool, port int) error {
	if enabled && (port < 1 || port > 24) {
		return fmt.Errorf("监控端口不合法")
	}
	f := url.Values{}
	if enabled {
		f.Set("state", "1")
		f.Set("mirroringport", strconv.Itoa(port))
	} else {
		f.Set("state", "0")
		f.Set("mirroringport", "0")
	}
	return m.writeCGI("/mirror_enabled_set.cgi", f)
}

// mirrorSourceSet 设置被监控端口的入口/出口镜像；ingress/egress 取 0 或 1。
func (m *mercury) mirrorSourceSet(ports []int, ingress, egress int) error {
	if len(ports) == 0 {
		return fmt.Errorf("请至少选择一个被监控端口")
	}
	if ingress != 0 && ingress != 1 {
		return fmt.Errorf("入口参数不合法")
	}
	if egress != 0 && egress != 1 {
		return fmt.Errorf("出口参数不合法")
	}
	f := url.Values{}
	for _, p := range ports {
		if p < 1 || p > 24 {
			return fmt.Errorf("端口号不合法：%d", p)
		}
		f.Add("mirroredport", strconv.Itoa(p))
	}
	f.Set("ingressState", strconv.Itoa(ingress))
	f.Set("egressState", strconv.Itoa(egress))
	return m.writeCGI("/mirrored_port_set.cgi", f)
}

/* ---------- 端口隔离 ---------- */

func (m *mercury) isolationConfig() (map[string]interface{}, error) {
	page, err := m.readPage("/PortIsolationRpm.htm")
	if err != nil {
		return nil, err
	}
	ds := extractObject(page, "portIso_conf")
	if ds == nil {
		return nil, fmt.Errorf("读取端口隔离失败")
	}
	n := scalarInt(ds["portNum"])
	if n <= 0 {
		n = 6
	}
	forward := strSlice(ds["portStr_iso"])
	trunk := numSlice(ds["trunk_info"])
	list := make([]map[string]interface{}, 0, n)
	for i := 0; i < n; i++ {
		item := map[string]interface{}{"port": i + 1, "forward": atS(forward, i)}
		if i < len(trunk) {
			item["lag"] = int(trunk[i])
		}
		list = append(list, item)
	}
	return map[string]interface{}{"ports": list, "count": n}, nil
}

func (m *mercury) isolationSet(groupPorts, forwardPorts []int) error {
	if len(groupPorts) == 0 {
		return fmt.Errorf("请先选择要设置隔离的端口")
	}
	f := url.Values{}
	for _, p := range groupPorts {
		if p < 1 || p > 24 {
			return fmt.Errorf("端口号不合法：%d", p)
		}
		f.Add("groupId", strconv.Itoa(p))
	}
	if len(forwardPorts) == 0 {
		// 不选转发端口 = 该端口与其他端口全部隔离
		f.Add("portid", "-1")
	} else {
		for _, p := range forwardPorts {
			if p < 1 || p > 24 {
				return fmt.Errorf("转发端口不合法：%d", p)
			}
			f.Add("portid", strconv.Itoa(p))
		}
	}
	return m.writeCGI("/port_isolation_set.cgi", f)
}

/* ---------- 环回保护 ---------- */

func (m *mercury) loopConfig() (map[string]interface{}, error) {
	page, err := m.readPage("/LoopPreventionRpm.htm")
	if err != nil {
		return nil, err
	}
	en, _ := strconv.Atoi(orZero(extractNumber(page, "lpEn")))
	return map[string]interface{}{"enabled": en == 1}, nil
}

func (m *mercury) loopSet(enabled bool) error {
	f := url.Values{}
	if enabled {
		f.Set("lpEn", "1")
	} else {
		f.Set("lpEn", "0")
	}
	return m.writeCGI("/loop_prevention_set.cgi", f)
}

/* ---------- QoS ---------- */

var qosModeNames = map[int]string{0: "基于端口", 1: "基于802.1P", 2: "基于DSCP"}

func (m *mercury) qosConfig() (map[string]interface{}, error) {
	page, err := m.readPage("/QosBasicRpm.htm")
	if err != nil {
		return nil, err
	}
	mode, _ := strconv.Atoi(orZero(extractNumber(page, "qosMode")))
	n := 6
	if s := extractNumber(page, "portNumber"); s != "" {
		if v, e := strconv.Atoi(s); e == nil && v > 0 {
			n = v
		}
	}
	pPri := extractNumArray(page, "pPri")
	bcPage, _ := m.readPage("/QosBandWidthControlRpm.htm")
	scPage, _ := m.readPage("/QosStormControlRpm.htm")
	bc := extractNumArray(bcPage, "bcInfo")
	sc := extractNumArray(scPage, "scInfo")
	priName := map[int]string{1: "1（最低）", 2: "2（正常）", 3: "3（中等）", 4: "4（最高）"}
	ports := make([]map[string]interface{}, 0, n)
	for i := 0; i < n; i++ {
		pri := numAt(pPri, i)
		ingress := numAt(bc, 3*i)
		egress := numAt(bc, 3*i+1)
		stormRate := numAt(sc, 3*i)
		stormType := numAt(sc, 3*i+1)
		ports = append(ports, map[string]interface{}{
			"port": i + 1, "priority": pri, "priority_text": priName[pri],
			"ingress_kbps": ingress, "egress_kbps": egress,
			"storm_enabled": stormRate != 0 && stormType != 0,
			"storm_kbps":    stormRate, "storm_type": stormType,
		})
	}
	return map[string]interface{}{
		"mode": mode, "mode_text": qosModeNames[mode], "ports": ports,
	}, nil
}

func (m *mercury) qosModeSet(mode int) error {
	if mode < 0 || mode > 2 {
		return fmt.Errorf("QoS 模式不合法")
	}
	f := url.Values{}
	f.Set("rd_qosmode", strconv.Itoa(mode))
	return m.postCGI("/qos_mode_set.cgi", f)
}

func (m *mercury) qosPrioritySet(ports []int, queue int) error {
	if len(ports) == 0 {
		return fmt.Errorf("请至少选择一个端口")
	}
	if queue < 0 || queue > 3 {
		return fmt.Errorf("优先级队列不合法")
	}
	f := url.Values{}
	for _, p := range ports {
		if p < 1 || p > 24 {
			return fmt.Errorf("端口号不合法：%d", p)
		}
		f.Set(fmt.Sprintf("sel_%d", p), "1")
	}
	f.Set("port_queue", strconv.Itoa(queue))
	return m.postCGI("/qos_port_priority_set.cgi", f)
}

func (m *mercury) qosRateSet(ports []int, ingress, egress int) error {
	if len(ports) == 0 {
		return fmt.Errorf("请至少选择一个端口")
	}
	if err := checkRate(ingress, true); err != nil {
		return err
	}
	if err := checkRate(egress, true); err != nil {
		return err
	}
	f := url.Values{}
	for _, p := range ports {
		if p < 1 || p > 24 {
			return fmt.Errorf("端口号不合法：%d", p)
		}
		f.Set(fmt.Sprintf("sel_%d", p), "1")
	}
	f.Set("igrRate", strconv.Itoa(ingress))
	f.Set("egrRate", strconv.Itoa(egress))
	return m.postCGI("/qos_bandwidth_set.cgi", f)
}

// checkRate 0 表示不限速；否则需在 512..10000000 且为 64 的整数倍。
func checkRate(v int, allowZero bool) error {
	if v == 0 {
		if allowZero {
			return nil
		}
		return fmt.Errorf("速率不能为 0")
	}
	if v < 512 || v > 10000000 {
		return fmt.Errorf("速率需在 512-10000000 Kbps 之间")
	}
	if v%64 != 0 {
		return fmt.Errorf("速率需为 64 的整数倍")
	}
	return nil
}

func (m *mercury) qosStormSet(ports []int, enabled bool, rate int, types []int) error {
	if len(ports) == 0 {
		return fmt.Errorf("请至少选择一个端口")
	}
	f := url.Values{}
	for _, p := range ports {
		if p < 1 || p > 24 {
			return fmt.Errorf("端口号不合法：%d", p)
		}
		f.Set(fmt.Sprintf("sel_%d", p), "1")
	}
	if !enabled {
		f.Set("state", "0")
		f.Set("rate", "0")
		return m.postCGI("/qos_storm_set.cgi", f)
	}
	if err := checkRate(rate, false); err != nil {
		return err
	}
	if len(types) == 0 {
		return fmt.Errorf("请至少选择一种风暴类型")
	}
	for _, t := range types {
		if t != 1 && t != 2 && t != 4 {
			return fmt.Errorf("风暴类型不合法：%d", t)
		}
		f.Add("stormType", strconv.Itoa(t))
	}
	f.Set("state", "1")
	f.Set("rate", strconv.Itoa(rate))
	return m.postCGI("/qos_storm_set.cgi", f)
}

/* ---------- VLAN 扩展：MTU / 端口 VLAN / PVID ---------- */

func (m *mercury) mtuVlanConfig() (map[string]interface{}, error) {
	page, err := m.readPage("/VlanMtuRpm.htm")
	if err != nil {
		return nil, err
	}
	ds := extractObject(page, "mtu_ds")
	if ds == nil {
		return nil, fmt.Errorf("读取 MTU VLAN 失败")
	}
	n := scalarInt(ds["portNum"])
	if n <= 0 {
		n = 6
	}
	return map[string]interface{}{
		"enabled": scalarInt(ds["state"]) == 1,
		"uplink":  scalarInt(ds["uplinkPort"]),
		"count":   n,
	}, nil
}

func (m *mercury) mtuVlanSet(enabled *bool, uplink *int) error {
	f := url.Values{}
	if enabled != nil {
		if *enabled {
			f.Set("mtu_en", "1")
		} else {
			f.Set("mtu_en", "0")
		}
	}
	if uplink != nil {
		if *uplink < 1 || *uplink > 24 {
			return fmt.Errorf("Uplink 端口不合法")
		}
		f.Set("uplinkPort", strconv.Itoa(*uplink))
	}
	if len(f) == 0 {
		return fmt.Errorf("没有需要提交的变更")
	}
	return m.writeCGI("/mtuVlanSet.cgi", f)
}

func (m *mercury) portVlanConfig() (map[string]interface{}, error) {
	page, err := m.readPage("/VlanPortBasicRpm.htm")
	if err != nil {
		return nil, err
	}
	ds := extractObject(page, "pvlan_ds")
	if ds == nil {
		return nil, fmt.Errorf("读取端口 VLAN 失败")
	}
	n := scalarInt(ds["portNum"])
	if n <= 0 {
		n = 6
	}
	vids := numSlice(ds["vids"])
	mbrs := numSlice(ds["mbrs"])
	list := make([]map[string]interface{}, 0, len(vids))
	for i := range vids {
		mask := 0
		if i < len(mbrs) {
			mask = int(mbrs[i])
		}
		list = append(list, map[string]interface{}{
			"vlan": int(vids[i]), "mask": mask, "ports": maskPorts(mask, n),
		})
	}
	return map[string]interface{}{
		"enabled": scalarInt(ds["state"]) == 1, "count": n, "vlans": list,
	}, nil
}

func (m *mercury) portVlanEnable(enabled bool) error {
	f := url.Values{}
	if enabled {
		f.Set("pvlan_en", "1")
	} else {
		f.Set("pvlan_en", "0")
	}
	return m.writeCGI("/pvlanSet.cgi", f)
}

func (m *mercury) portVlanAdd(vid int, ports []int) error {
	if vid < 1 || vid > 4094 {
		return fmt.Errorf("VLAN ID 需在 1-4094 之间")
	}
	if len(ports) == 0 {
		return fmt.Errorf("请至少选择一个成员端口")
	}
	f := url.Values{}
	f.Set("vid", strconv.Itoa(vid))
	for _, p := range ports {
		if p < 1 || p > 24 {
			return fmt.Errorf("端口号不合法：%d", p)
		}
		f.Add("selPorts", strconv.Itoa(p))
	}
	return m.writeCGI("/pvlanSet.cgi", f)
}

func (m *mercury) portVlanDel(vids []int) error {
	if len(vids) == 0 {
		return fmt.Errorf("请至少选择一个 VLAN")
	}
	f := url.Values{}
	for _, v := range vids {
		if v < 1 || v > 4094 {
			return fmt.Errorf("VLAN ID 不合法：%d", v)
		}
		f.Add("selVlans", strconv.Itoa(v))
	}
	return m.writeCGI("/pvlanSet.cgi", f)
}

func (m *mercury) pvidConfig() (map[string]interface{}, error) {
	page, err := m.readPage("/Vlan8021QPvidRpm.htm")
	if err != nil {
		return nil, err
	}
	ds := extractObject(page, "pvid_ds")
	if ds == nil {
		return nil, fmt.Errorf("读取 PVID 失败")
	}
	n := scalarInt(ds["portNum"])
	if n <= 0 {
		n = 6
	}
	pvids := numSlice(ds["pvids"])
	list := make([]map[string]interface{}, 0, n)
	for i := 0; i < n; i++ {
		list = append(list, map[string]interface{}{"port": i + 1, "pvid": numAt(pvids, i)})
	}
	return map[string]interface{}{
		"dot1q_enabled": scalarInt(ds["state"]) == 1, "ports": list,
	}, nil
}

// pvidSet 只改选中端口的 PVID；pbm 是端口位掩码（端口 1 = bit0）。
func (m *mercury) pvidSet(pbm, pvid int) error {
	if pbm <= 0 {
		return fmt.Errorf("请至少选择一个端口")
	}
	if pvid < 1 || pvid > 4094 {
		return fmt.Errorf("PVID 需在 1-4094 之间")
	}
	f := url.Values{}
	f.Set("pbm", strconv.Itoa(pbm))
	f.Set("pvid", strconv.Itoa(pvid))
	return m.writeCGI("/vlanPvidSet.cgi", f)
}

/* ---------- 配置备份 / 还原 / 恢复出厂 ---------- */

func (m *mercury) configBackup() ([]byte, error) {
	body, err := m.cgiSlow("/config_back.cgi", url.Values{}, 180*time.Second)
	if err != nil {
		return nil, err
	}
	if len(body) == 0 {
		return nil, fmt.Errorf("设备返回了空的配置文件")
	}
	// 出错时设备会回一个 HTML 页面，正常备份是 .cfg 文本
	if strings.HasPrefix(strings.TrimSpace(string(body[:minInt(len(body), 64)])), "<") {
		return nil, fmt.Errorf("设备未返回配置文件（可能正在忙，请稍后重试）")
	}
	return body, nil
}

func minInt(a, b int) int {
	if a < b {
		return a
	}
	return b
}

func (m *mercury) configRestore(filename string, data []byte) error {
	name := filepath.Base(strings.ReplaceAll(filename, "\\", "/"))
	if !strings.EqualFold(filepath.Ext(name), ".cfg") {
		return fmt.Errorf("配置文件需为 .cfg，当前文件：%s", name)
	}
	if len(name) > 63 {
		return fmt.Errorf("配置文件长度应小于 64 个字符")
	}
	if len(data) == 0 {
		return fmt.Errorf("配置文件为空")
	}
	return m.uploadMultipart("/conf_restore.cgi?token=", "configfile", name, data, 180*time.Second)
}

func (m *mercury) factoryReset() error {
	f := url.Values{}
	f.Set("reset_op", "factory")
	return m.postCGI("/reset.cgi", f)
}

// uploadMultipart 把文件以 multipart 表单 POST 给设备；path 需以 `token=` 结尾，
// 设备把 token 放在查询串里（与自带页面一致）。
func (m *mercury) uploadMultipart(path, field, name string, data []byte, timeout time.Duration) error {
	if err := m.ensure(); err != nil {
		return err
	}
	var buf bytes.Buffer
	mw := multipart.NewWriter(&buf)
	part, err := mw.CreateFormFile(field, name)
	if err != nil {
		return err
	}
	if _, err := part.Write(data); err != nil {
		return err
	}
	if err := mw.Close(); err != nil {
		return err
	}
	u := m.base() + path + url.QueryEscape(m.token)
	req, err := http.NewRequest("POST", u, &buf)
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", mw.FormDataContentType())
	req.Header.Set("User-Agent", "meshlan-compat/1.0")
	if m.cookie != "" {
		req.Header.Set("Cookie", m.cookie)
	}
	client := &http.Client{Timeout: timeout}
	res, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("上传失败：%v", err)
	}
	defer res.Body.Close()
	body, _ := io.ReadAll(io.LimitReader(res.Body, 4096))
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return fmt.Errorf("设备拒绝请求（HTTP %d）：%s", res.StatusCode, strings.TrimSpace(string(body)))
	}
	m.token = ""
	return nil
}
