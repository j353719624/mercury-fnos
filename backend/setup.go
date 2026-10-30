package main

// 首次配置：把交换机地址/账号保存到会随升级保留的目录，
// 卸载重装后只需要在页面上填一次，不用再手改 devices.json。

import (
	"net"
	"time"
	"sync"
	"sync/atomic"
	"sort"
	"io"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"path/filepath"
	"strings"
)

const placeholderPassword = "CHANGE_ME"

// primaryDevicesPath 与 loadDevices 的查找顺序一致：优先应用数据目录（升级不丢）。
func primaryDevicesPath() string {
	return filepath.Join(appHome(), "var", "devices.json")
}

func installDevicesPath() string {
	return filepath.Join(appHome(), "config", "devices.json")
}

// saveDevices 写配置文件；优先写持久目录，同时尽量同步到安装目录。
func saveDevices(list []deviceConf) error {
	body, err := json.MarshalIndent(list, "", "  ")
	if err != nil {
		return err
	}
	body = append(body, '\n')

	targets := []string{primaryDevicesPath(), installDevicesPath()}
	if sp := socketPath(); sp != "" {
		targets = append(targets, filepath.Join(filepath.Dir(sp), "config", "devices.json"))
	}

	var firstErr error
	wrote := false
	for _, p := range targets {
		dir := filepath.Dir(p)
		if err := os.MkdirAll(dir, 0o755); err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		if err := os.WriteFile(p, body, 0o644); err != nil {
			if firstErr == nil {
				firstErr = err
			}
			continue
		}
		wrote = true
	}
	if !wrote {
		return fmt.Errorf("无法写入配置文件：%v", firstErr)
	}
	return nil
}

// deviceConfigured 判断是否已经填过真实密码。
func deviceConfigured(list []deviceConf) bool {
	for _, d := range list {
		if d.IP != "" && d.Username != "" && d.Password != "" && d.Password != placeholderPassword {
			return true
		}
	}
	return false
}

func setupStatus(w http.ResponseWriter, r *http.Request) {
	list := loadDevices()
	resp := map[string]interface{}{
		"configured": deviceConfigured(list),
		"count":      len(list),
	}
	if len(list) > 0 {
		d := list[0]
		resp["device"] = map[string]interface{}{
			"name": d.Name, "ip": d.IP, "port": d.Port, "username": d.Username,
			"mac": d.Mac, "model": d.Model,
		}
	}
	writeOK(w, resp)
}

type setupDeviceReq struct {
	Name     string `json:"name"`
	IP       string `json:"ip"`
	Port     int    `json:"port"`
	Username string `json:"username"`
	Password string `json:"password"`
}

func setupSaveDevice(w http.ResponseWriter, r *http.Request) {
	var req setupDeviceReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, "请求解析失败")
		return
	}
	req.IP = strings.TrimSpace(req.IP)
	req.Username = strings.TrimSpace(req.Username)
	if req.Port == 0 {
		req.Port = 80
	}
	if req.IP == "" || req.Username == "" || req.Password == "" {
		writeErr(w, "IP、用户名、密码都不能为空")
		return
	}
	if req.Password == placeholderPassword {
		writeErr(w, "密码不能是占位符")
		return
	}
	if req.Name == "" {
		req.Name = "水星交换机"
	}
	if !strings.Contains(req.IP, ".") || len(req.IP) > 64 {
		writeErr(w, "IP 地址格式不对")
		return
	}

	// 先用这组账号探一次，确认能登录再保存
	probe := newMercury(req.IP, req.Port, req.Username, req.Password)
	info, _, err := probe.status()
	if err != nil {
		writeErr(w, "连接交换机失败："+err.Error())
		return
	}

	dev := deviceConf{
		Name: req.Name, IP: req.IP, Port: req.Port,
		Username: req.Username, Password: req.Password,
		Mac: strings.ToUpper(info.Mac), Model: info.Model,
	}
	if dev.Mac == "" {
		dev.Mac = "unknown"
	}
	if dev.Model == "" {
		dev.Model = "SE106 Pro"
	}
	if err := saveDevices([]deviceConf{dev}); err != nil {
		writeErr(w, "保存失败："+err.Error())
		return
	}
	writeOK(w, map[string]interface{}{
		"mac": dev.Mac, "model": dev.Model, "ip": dev.IP, "name": dev.Name,
	})
}

/* ---- 设备管理（多台交换机） ---- */

func setupListDevices(w http.ResponseWriter, r *http.Request) {
	list := loadDevices()
	out := make([]map[string]interface{}, 0, len(list))
	for _, d := range list {
		out = append(out, map[string]interface{}{
			"name": d.Name, "ip": d.IP, "port": d.Port,
			"username": d.Username, "mac": d.Mac, "model": d.Model,
			"configured": d.Password != "" && d.Password != placeholderPassword,
		})
	}
	writeOK(w, out)
}

func setupAddDevice(w http.ResponseWriter, r *http.Request) {
	var req setupDeviceReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, "请求解析失败")
		return
	}
	req.IP = strings.TrimSpace(req.IP)
	req.Username = strings.TrimSpace(req.Username)
	if req.Port == 0 {
		req.Port = 80
	}
	if req.IP == "" || req.Username == "" || req.Password == "" {
		writeErr(w, "IP、用户名、密码都不能为空")
		return
	}
	if req.Password == placeholderPassword {
		writeErr(w, "密码不能是占位符")
		return
	}
	if req.Name == "" {
		req.Name = "水星交换机"
	}

	probe := newMercury(req.IP, req.Port, req.Username, req.Password)
	info, _, err := probe.status()
	if err != nil {
		writeErr(w, "连接交换机失败："+err.Error())
		return
	}

	list := loadDevices()
	dev := deviceConf{
		Name: req.Name, IP: req.IP, Port: req.Port,
		Username: req.Username, Password: req.Password,
		Mac: strings.ToUpper(info.Mac), Model: info.Model,
	}
	if dev.Mac == "" {
		dev.Mac = "unknown"
	}
	replaced := false
	for i := range list {
		if list[i].IP == dev.IP {
			list[i] = dev
			replaced = true
			break
		}
	}
	if !replaced {
		list = append(list, dev)
	}
	if err := saveDevices(list); err != nil {
		writeErr(w, "保存失败："+err.Error())
		return
	}
	writeOK(w, map[string]interface{}{"ip": dev.IP, "mac": dev.Mac, "model": dev.Model, "count": len(list)})
}

func setupDeleteDevice(w http.ResponseWriter, r *http.Request) {
	ip := strings.TrimSpace(r.URL.Query().Get("ip"))
	if ip == "" {
		writeErr(w, "缺少 ip 参数")
		return
	}
	list := loadDevices()
	kept := make([]deviceConf, 0, len(list))
	for _, d := range list {
		if d.IP != ip {
			kept = append(kept, d)
		}
	}
	if len(kept) == len(list) {
		writeErr(w, "没有找到该设备")
		return
	}
	if err := saveDevices(kept); err != nil {
		writeErr(w, "保存失败："+err.Error())
		return
	}
	writeOK(w, map[string]interface{}{"count": len(kept)})
}

func setupTestDevice(w http.ResponseWriter, r *http.Request) {
	var req setupDeviceReq
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		writeErr(w, "请求解析失败")
		return
	}
	if req.Port == 0 {
		req.Port = 80
	}
	probe := newMercury(req.IP, req.Port, req.Username, req.Password)
	info, ports, err := probe.status()
	if err != nil {
		writeErr(w, "连接失败："+err.Error())
		return
	}
	writeOK(w, map[string]interface{}{
		"model": info.Model, "firmware": info.FirmwareVersion,
		"mac": info.Mac, "ports": len(ports),
	})
}

/* ---- 自动发现局域网里的水星/TP-Link 交换机 ---- */

func localIPv4() string {
	conn, err := net.Dial("udp", "223.5.5.5:53")
	if err != nil {
		return ""
	}
	defer conn.Close()
	if a, ok := conn.LocalAddr().(*net.UDPAddr); ok && a.IP.To4() != nil {
		return a.IP.String()
	}
	return ""
}

func arpTable() map[string]string {
	out := map[string]string{}
	b, err := os.ReadFile("/proc/net/arp")
	if err != nil {
		return out
	}
	for _, line := range strings.Split(string(b), "\n")[1:] {
		f := strings.Fields(line)
		if len(f) >= 4 && f[3] != "00:00:00:00:00:00" {
			out[f[0]] = strings.ToUpper(f[3])
		}
	}
	return out
}

// looksLikeMercury 判断页面是不是水星/TP-Link 系交换机的登录页。
func looksLikeMercury(body string) bool {
	for _, m := range []string{"美科星", "mercurycom", "plain_password", "logon.cgi"} {
		if strings.Contains(body, m) {
			return true
		}
	}
	return false
}

func setupDiscover(w http.ResponseWriter, r *http.Request) {
	local := localIPv4()
	if local == "" {
		writeErr(w, "无法确定本机局域网地址")
		return
	}
	parts := strings.Split(local, ".")
	if len(parts) != 4 {
		writeErr(w, "本机地址异常："+local)
		return
	}
	base := strings.Join(parts[:3], ".")
	arp := arpTable()

	type found struct {
		IP  string `json:"ip"`
		Mac string `json:"mac"`
	}
	var mu sync.Mutex
	res := []found{}
	sem := make(chan struct{}, 32)
	var wg sync.WaitGroup

	client := &http.Client{Timeout: 700 * time.Millisecond}
	for i := 1; i <= 254; i++ {
		ip := fmt.Sprintf("%s.%d", base, i)
		if ip == local {
			continue
		}
		wg.Add(1)
		sem <- struct{}{}
		go func(ip string) {
			defer wg.Done()
			defer func() { <-sem }()
			// 只探测 80 端口，握手成功再取一次首页
			conn, err := net.DialTimeout("tcp", ip+":80", 400*time.Millisecond)
			if err != nil {
				return
			}
			conn.Close()
			resp, err := client.Get("http://" + ip + "/")
			if err != nil {
				return
			}
			defer resp.Body.Close()
			body, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
			if !looksLikeMercury(string(body)) {
				return
			}
			mu.Lock()
			res = append(res, found{IP: ip, Mac: arp[ip]})
			rememberDiscovered(ip, arp[ip])
			mu.Unlock()
		}(ip)
	}
	wg.Wait()
	sort.Slice(res, func(i, j int) bool { return res[i].IP < res[j].IP })
	writeOK(w, map[string]interface{}{"subnet": base + ".0/24", "devices": res})
}

/* ---- 自动发现结果缓存：让"未配置"的设备也能在列表里被点开并登录 ---- */

type discoveredDev struct {
	IP  string
	Mac string
}

var discoveredMu sync.Mutex
var discoveredCache = map[string]discoveredDev{}

func rememberDiscovered(ip, mac string) {
	if ip == "" {
		return
	}
	key := strings.ToUpper(mac)
	if key == "" {
		key = ip
	}
	discoveredMu.Lock()
	discoveredCache[key] = discoveredDev{IP: ip, Mac: strings.ToUpper(mac)}
	discoveredMu.Unlock()
}

func discoveredByMac(mac string) (deviceConf, bool) {
	discoveredMu.Lock()
	d, ok := discoveredCache[strings.ToUpper(mac)]
	discoveredMu.Unlock()
	if !ok {
		return deviceConf{}, false
	}
	return deviceConf{
		Name: "水星交换机", IP: d.IP, Port: 80,
		Username: "admin", Mac: d.Mac, Model: "SE106 Pro",
	}, true
}

// discoveredList 返回尚未写入配置文件的已发现设备。
func discoveredList(configured []deviceConf) []map[string]interface{} {
	known := map[string]bool{}
	for _, d := range configured {
		known[strings.ToUpper(d.Mac)] = true
		known[d.IP] = true
	}
	out := []map[string]interface{}{}
	discoveredMu.Lock()
	defer discoveredMu.Unlock()
	for key, d := range discoveredCache {
		if known[key] || known[d.IP] {
			continue
		}
		mac := d.Mac
		if mac == "" {
			mac = key
		}
		out = append(out, map[string]interface{}{
			"mac": mac, "name": "水星交换机", "model": "SE106 Pro",
			"ip": d.IP, "is_online": true, "online": true, "is_login": false,
		})
	}
	sort.Slice(out, func(i, j int) bool {
		return out[i]["ip"].(string) < out[j]["ip"].(string)
	})
	return out
}

// saveDevice 在登录成功后把设备写入配置（已存在则更新账号）。
func saveDevice(dev deviceConf) error {
	list := loadDevices()
	for i := range list {
		if strings.EqualFold(list[i].Mac, dev.Mac) || list[i].IP == dev.IP {
			list[i] = dev
			return saveDevices(list)
		}
	}
	list = append(list, dev)
	return saveDevices(list)
}

/* ---- 打开应用就自动扫一次 ---- */

var scanOnce sync.Mutex
var lastScan time.Time
var scanRunning atomic.Int32

// ensureScan 立即返回：扫描放到后台跑，避免阻塞 /api/devices 让前端首屏等 10 秒。
func ensureScan() {
	scanOnce.Lock()
	if time.Since(lastScan) < 60*time.Second {
		scanOnce.Unlock()
		return
	}
	lastScan = time.Now()
	scanOnce.Unlock()
	go runScan()
}

func runScan() {
	if scanRunning.Swap(1) == 1 {
		return
	}
	defer scanRunning.Store(0)

	local := localIPv4()
	parts := strings.Split(local, ".")
	if len(parts) != 4 {
		return
	}
	base := strings.Join(parts[:3], ".")
	arp := arpTable()
	client := &http.Client{Timeout: 700 * time.Millisecond}
	sem := make(chan struct{}, 32)
	var wg sync.WaitGroup
	for i := 1; i <= 254; i++ {
		ip := fmt.Sprintf("%s.%d", base, i)
		if ip == local {
			continue
		}
		wg.Add(1)
		sem <- struct{}{}
		go func(ip string) {
			defer wg.Done()
			defer func() { <-sem }()
			conn, err := net.DialTimeout("tcp", ip+":80", 400*time.Millisecond)
			if err != nil {
				return
			}
			conn.Close()
			resp, err := client.Get("http://" + ip + "/")
			if err != nil {
				return
			}
			defer resp.Body.Close()
			body, _ := io.ReadAll(io.LimitReader(resp.Body, 64<<10))
			if looksLikeMercury(string(body)) {
				rememberDiscovered(ip, arp[ip])
			}
		}(ip)
	}
	wg.Wait()
}
