package main

import (
	"container/ring"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"log"
	"net/http"
	"os"
	"strings"
	"sync"
	"sync/atomic"
	"time"
)

var (
	webListen    string
	webGUIActive bool
	logBuffer    *ring.Ring
	logMu        sync.RWMutex

	reconnectMu     sync.Mutex
	reconnectNeeded bool
)

const logRingSize = 500

func init() {
	flag.StringVar(&webListen, "web", "", "Web GUI listen address (e.g. :9090)")
}

// logWriter is a writer that captures logs for the web dashboard.
type logWriter struct{}

func (w logWriter) Write(p []byte) (n int, err error) {
	logMu.Lock()
	if logBuffer != nil {
		logBuffer.Value = string(p)
		logBuffer = logBuffer.Next()
	}
	logMu.Unlock()
	return len(p), nil
}

func startWebGUI() {
	if webListen == "" {
		return
	}

	// Capture standard log output while keeping stderr output.
	logBuffer = ring.New(logRingSize)
	log.SetOutput(io.MultiWriter(logWriter{}, os.Stderr))

	// 服务端模式：web 与 WebSocket 共享端口
	// 如果 webListen 的端口号与 listenAddr 一致，则共用同一个 HTTP 服务
	if webListen != "" && listenAddr != "" {
		webPort := webListen
		if strings.Contains(listenAddr, webPort) {
			webGUIActive = true
			log.Printf("[Web GUI] 已合并到 WebSocket 端口 %s", webListen)
			http.HandleFunc("/", handleDashboard)
			http.HandleFunc("/api/status", handleStatus)
			http.HandleFunc("/api/logs", handleLogs)
			http.HandleFunc("/api/config", handleConfig)
			http.HandleFunc("/api/update", handleUpdateConfig)
			http.HandleFunc("/api/tun/toggle", handleTunToggle)
			http.HandleFunc("/api/tun/status", handleTunStatus)
			http.HandleFunc("/api/geo/upload", handleGeoUpload)
			http.HandleFunc("/api/geo/reload", handleGeoReload)
			http.HandleFunc("/api/geo/upgrade", handleGeoUpgrade)
			http.HandleFunc("/api/restart", handleRestartClient)
			http.HandleFunc("/api/servers/add", handleAddServer)
			http.HandleFunc("/api/servers/update", handleUpdateServer)
			http.HandleFunc("/api/servers/delete", handleDeleteServer)
			http.HandleFunc("/api/saveconfig", handleSaveConfig)
			return
		}
	}

	mux := http.NewServeMux()
	mux.HandleFunc("/", handleDashboard)
	mux.HandleFunc("/api/status", handleStatus)
	mux.HandleFunc("/api/logs", handleLogs)
	mux.HandleFunc("/api/config", handleConfig)
	mux.HandleFunc("/api/update", handleUpdateConfig)
	mux.HandleFunc("/api/tun/toggle", handleTunToggle)
	mux.HandleFunc("/api/tun/status", handleTunStatus)
	mux.HandleFunc("/api/geo/upload", handleGeoUpload)
	mux.HandleFunc("/api/geo/reload", handleGeoReload)
	mux.HandleFunc("/api/geo/upgrade", handleGeoUpgrade)
	mux.HandleFunc("/api/restart", handleRestartClient)
	mux.HandleFunc("/api/servers/add", handleAddServer)
	mux.HandleFunc("/api/servers/update", handleUpdateServer)
	mux.HandleFunc("/api/servers/delete", handleDeleteServer)
	mux.HandleFunc("/api/saveconfig", handleSaveConfig)

	go func() {
		log.Printf("[Web GUI] 监听 %s", webListen)
		if err := http.ListenAndServe(webListen, mux); err != nil {
			log.Printf("[Web GUI] 错误: %v", err)
		}
	}()
}

func countActiveClients() int {
	count := 0
	serverSessions.Range(func(_, v any) bool {
		if cs, ok := v.(*ClientSession); ok && cs != nil {
			cs.mu.RLock()
			for _, ch := range cs.channels {
				if ch != nil && ch.conn != nil {
					count++
				}
			}
			cs.mu.RUnlock()
		}
		return true
	})
	return count
}
func statusJSON() map[string]interface{} {
	result := map[string]interface{}{
		"uptime":     "running",
		"web_listen": webListen,
		"strategy":   tunnelConfig.Strategy,
	}
	result["tun_mode"] = tunMode

	// Server mode (no echPool)
	if echPool == nil {
		result["mode"] = "server"
		result["listen"] = listenAddr
		result["tunnel"] = "服务端模式"
		result["healthy"] = true
		result["clients"] = countActiveClients()
		return result
	}

	// Client mode
	result["mode"] = "client"
	result["listen"] = listenAddr
	result["server"] = forwardAddr
	result["client_id"] = clientID

	type chInfo struct {
		ID     int    `json:"id"`
		IP     string `json:"ip"`
		Status string `json:"status"`
		RTT    string `json:"rtt"`
	}

	channels := []chInfo{}
	// 从多服务器中获取第一个可用服务器的通道信息
	serverPools := echPool.ServerPools()
	if len(serverPools) > 0 {
		sp := serverPools[0]
		sp.mu.RLock()
		if sp.Pool != nil {
			for i, sess := range sp.Pool.smuxConns {
				if sess == nil {
					continue
				}
				status := "已连接"
				if sess.IsClosed() {
					status = "已断开"
				}
				rtt := atomic.LoadInt64(&sp.Pool.channelRTT[i])
				rttStr := fmt.Sprintf("%dms", rtt/1e6)
				if rtt == 0 {
					rttStr = "--"
				}
				channels = append(channels, chInfo{
					ID:     i + 1,
					IP:     "",
					Status: status,
					RTT:    rttStr,
				})
			}
		}
		sp.mu.RUnlock()
	}

	result["servers"] = echPool.Servers()
	result["channels"] = channels
	result["healthy"] = echPool.HasHealthyChannel()
	result["geoip"] = fmt.Sprint(len(geoIPMatcher.cidrs), " cidr")
	result["geosite"] = fmt.Sprint(len(geoSiteMatcher.domains), " domains")

	return result
}

func handleDashboard(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "text/html; charset=utf-8")
	w.Write([]byte(dashboardHTML))
}

func handleStatus(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(statusJSON())
}

func handleLogs(w http.ResponseWriter, r *http.Request) {
	logMu.RLock()
	logs := make([]string, 0, logRingSize)
	logBuffer.Do(func(v interface{}) {
		if s, ok := v.(string); ok && s != "" {
			logs = append(logs, s)
		}
	})
	logMu.RUnlock()

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(logs)
}

func handleConfig(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")

	// Global token display (masked)
	displayToken := ""
	if len(token) > 4 {
		displayToken = "***" + token[len(token)-4:]
	} else if token != "" {
		displayToken = "***"
	}

	// Server tokens (for multi-server mode)
	serverTokens := map[string]string{}
	for _, srv := range tunnelConfig.Servers {
		name := srv.Name
		if name == "" {
			name = srv.URL
		}
		if srv.Token != "" {
			if len(srv.Token) > 4 {
				serverTokens[name] = "***" + srv.Token[len(srv.Token)-4:]
			} else {
				serverTokens[name] = "***"
			}
		} else {
			serverTokens[name] = displayToken
		}
	}

	// Raw tokens for editing (unmasked)
	serverTokensRaw := map[string]string{}
	for _, srv := range tunnelConfig.Servers {
		name := srv.Name
		if name == "" {
			name = srv.URL
		}
		serverTokensRaw[name] = srv.Token
	}

	json.NewEncoder(w).Encode(map[string]interface{}{
		"listen":            listenAddr,
		"forward":           forwardAddr,
		"token":             displayToken,
		"server_tokens":     serverTokens,
		"server_tokens_raw": serverTokensRaw,
		"insecure":          insecure,
		"ips":               ips,
		"connections":       tunnelConfig.Connections,
		"strategy":          tunnelConfig.Strategy,
		"tun_mode":          tunnelConfig.TunMode,
		"geoip":             fmt.Sprint(len(geoIPMatcher.cidrs), " cidr"),
		"geosite":           fmt.Sprint(len(geoSiteMatcher.domains), " domains"),
		"mode": func() string {
			if echPool == nil && forwardAddr == "" {
				return "server"
			}
			return "client"
		}(),
	})
}

const dashboardHTML = `<!DOCTYPE html>
<html lang="zh-CN">
<head>
<meta charset="UTF-8">
<meta name="viewport" content="width=device-width, initial-scale=1.0">
<title>x-tunnel 管理面板</title>
<script src="https://cdn.tailwindcss.com"></script>
<style>
    body { font-family: -apple-system, BlinkMacSystemFont, 'Segoe UI', Roboto, sans-serif; }
    .log-line { font-family: 'Courier New', monospace; font-size: 12px; line-height: 1.4; }
    .status-dot { width: 10px; height: 10px; border-radius: 50%; display: inline-block; }
    .status-online { background: #22c55e; box-shadow: 0 0 5px #22c55e; }
    .status-offline { background: #ef4444; }
</style>
</head>
<body class="bg-gray-900 text-white min-h-screen">

<div class="max-w-7xl mx-auto p-6">
    <h1 class="text-3xl font-bold mb-8 text-blue-400">x-tunnel 管理面板</h1>

    <!-- Status Cards -->
    <div class="grid grid-cols-1 md:grid-cols-4 gap-4 mb-8">
        <div class="bg-gray-800 rounded-lg p-4">
            <div class="text-gray-400 text-sm" id="label-active">活动通道</div>
            <div class="text-2xl font-bold" id="active-channels">--</div>
        </div>
        <div class="bg-gray-800 rounded-lg p-4">
            <div class="text-gray-400 text-sm">健康状态</div>
            <div class="text-2xl font-bold" id="health-status">--</div>
        </div>
        <div class="bg-gray-800 rounded-lg p-4">
            <div class="text-gray-400 text-sm">监听地址</div>
            <div class="text-xl font-mono" id="cfg-listen">--</div>
        </div>
        <div class="bg-gray-800 rounded-lg p-4">
            <div class="text-gray-400 text-sm">策略</div>
            <div class="text-lg font-mono" id="cfg-strategy">--</div>
        </div>
        <div class="bg-gray-800 rounded-lg p-4">
            <div class="text-gray-400 text-sm">TUN 模式</div>
            <div class="text-lg font-mono" id="cfg-tun-mode">--</div>
        </div>
    </div>

    <!-- Server List -->
    <div class="bg-gray-800 rounded-lg p-6 mb-8">
        <div class="flex justify-between items-center mb-4">
            <h2 class="text-xl font-semibold">服务器</h2>
            <div class="flex items-center space-x-2">
                <span class="text-gray-400 text-sm">策略:</span>
                <select id="cfg-strategy-select" class="bg-gray-700 text-white rounded px-2 py-1 text-sm" onchange="changeStrategy()">
                    <option value="failover">故障转移</option>
                    <option value="loadbalance">负载均衡</option>
                    <option value="latency">最低延迟</option>
                </select>
                <button onclick="showAddServer()" class="px-3 py-1 bg-green-700 text-white rounded text-sm hover:bg-green-600">+ 添加</button>
                <button onclick="restartClient()" class="px-3 py-1 bg-yellow-700 text-white rounded text-sm hover:bg-yellow-600">重启</button>
            </div>
        </div>
        <div id="server-list" class="space-y-3">
            <div class="text-gray-500 text-center py-4">正在加载服务器列表...</div>
        </div>
    </div>

    <!-- Add Server Panel -->
    <div id="add-server-panel" class="bg-gray-800 rounded-lg p-6 mb-8 hidden">
        <h2 class="text-xl font-semibold mb-4">添加服务器</h2>
        <div class="grid grid-cols-1 md:grid-cols-2 gap-4">
            <div>
                <label class="text-gray-400 text-sm">名称</label>
                <input id="srv-name" class="w-full bg-gray-700 text-white rounded p-2 mt-1" placeholder="香港">
            </div>
            <div>
                <label class="text-gray-400 text-sm">地址 (URL)</label>
                <input id="srv-url" class="w-full bg-gray-700 text-white rounded p-2 mt-1" placeholder="ws://...">
            </div>
            <div>
                <label class="text-gray-400 text-sm">Token</label>
                <input id="srv-token" class="w-full bg-gray-700 text-white rounded p-2 mt-1" type="text">
            </div>
            <div>
                <label class="text-gray-400 text-sm">连接数</label>
                <input id="srv-conn" class="w-full bg-gray-700 text-white rounded p-2 mt-1" type="number" value="3" min="1" max="20">
            </div>
            <div>
                <label class="text-gray-400 text-sm">权重 (负载均衡用)</label>
                <input id="srv-weight" class="w-full bg-gray-700 text-white rounded p-2 mt-1" type="number" value="100" min="1">
            </div>
            <div class="flex items-center space-x-2 pt-7">
                <input id="srv-insecure" type="checkbox" class="w-4 h-4">
                <label class="text-gray-400 text-sm">跳过证书校验</label>
            </div>
        </div>
        <div class="mt-4 flex space-x-3">
            <button onclick="saveServer()" class="px-4 py-2 bg-green-600 text-white rounded hover:bg-green-700">保存</button>
            <button onclick="cancelAddServer()" class="px-4 py-2 bg-gray-600 text-white rounded hover:bg-gray-500">取消</button>
        </div>
        <div id="srv-msg" class="mt-2 text-sm hidden"></div>
    </div>

    <!-- Config Section -->
    <div class="bg-gray-800 rounded-lg p-6 mb-8">
        <div class="flex justify-between items-center mb-4">
            <h2 class="text-xl font-semibold">全局配置</h2>
            <button onclick="editGlobalConfig()" class="px-3 py-1 bg-blue-700 text-white rounded text-sm hover:bg-blue-600">修改</button>
        </div>
        <div class="grid grid-cols-1 md:grid-cols-2 gap-4 text-sm font-mono">
            <div><span class="text-gray-400">监听:</span> <span id="cfg-listen2">--</span></div>
            <div><span class="text-gray-400">连接数:</span> <span id="cfg-conn">--</span></div>
            <div><span class="text-gray-400">模式:</span> <span id="cfg-strategy2">--</span></div>
            <div><span class="text-gray-400">IP策略:</span> <span id="cfg-ips">--</span></div>
        </div>
    </div>


    <!-- Channels -->
    <div class="bg-gray-800 rounded-lg p-6 mb-8">
        <h2 class="text-xl font-semibold mb-4">隧道通道</h2>
        <div class="overflow-x-auto">
            <table class="w-full text-sm">
                <thead>
                    <tr class="text-gray-400 border-b border-gray-700">
                        <th class="text-left py-2 px-3">ID</th>
                        <th class="text-left py-2 px-3">状态</th>
                        <th class="text-left py-2 px-3">RTT</th>
                    </tr>
                </thead>
                <tbody id="channels-body"></tbody>
            </table>
        </div>
    </div>

    <!-- Geo Routing Data -->
    <div class="bg-gray-800 rounded-lg p-6 mb-8">
        <h2 class="text-xl font-semibold mb-4">GeoIP / GeoSite</h2>
        <p class="text-gray-400 text-sm mb-4">Upload .dat files — auto hot-reloads routing rules.</p>
        <div class="grid grid-cols-1 md:grid-cols-2 gap-4">
            <div class="bg-gray-700 rounded p-4">
                <p class="text-sm font-mono mb-2">geoip.dat <span id="geoip-status" class="text-xs text-gray-400">--</span></p>
                <input type="file" id="geoip-file" accept=".dat" class="text-xs mr-2"> <button onclick="uploadGeoFile('geoip')" class="px-2 py-1 bg-blue-600 text-white text-xs rounded hover:bg-blue-700">upload</button>
                <div id="geoip-msg" class="mt-1 text-xs hidden"></div>
            </div>
            <div class="bg-gray-700 rounded p-4">
                <p class="text-sm font-mono mb-2">geosite.dat <span id="geosite-status" class="text-xs text-gray-400">--</span></p>
                <input type="file" id="geosite-file" accept=".dat" class="text-xs mr-2"> <button onclick="uploadGeoFile('geosite')" class="px-2 py-1 bg-blue-600 text-white text-xs rounded hover:bg-blue-700">upload</button>
                <div id="geosite-msg" class="mt-1 text-xs hidden"></div>
            </div>
        </div>
        <div class="mt-3 flex space-x-3"><button onclick="onlineUpgrade()" class="px-4 py-2 bg-green-600 text-white text-sm rounded hover:bg-green-700">&#x1F310; Online Upgrade</button> <button onclick="reloadGeoData()" class="px-4 py-2 bg-yellow-600 text-white text-sm rounded hover:bg-yellow-700">Reload</button> <span id="geo-upgrade-msg" class="text-sm text-green-400 self-center hidden"></span> <span id="geo-reload-msg" class="text-sm self-center hidden"></span></div>
    </div>

    <!-- Logs -->
    <div class="bg-gray-800 rounded-lg p-6">
        <div class="flex justify-between items-center mb-4">
            <h2 class="text-xl font-semibold">实时日志</h2>
            <button onclick="clearLogs()" class="px-3 py-1 bg-gray-700 rounded text-sm hover:bg-gray-600">清空</button>
        </div>
        <div id="log-container" class="bg-black rounded-lg p-4 h-64 overflow-y-auto"></div>
    </div>
</div>

<script>
function fetchStatus() {
    fetch('/api/status').then(r => r.json()).then(data => {
        if (data.channels) {
            document.getElementById('active-channels').textContent = data.channels.length;
            let html = '';
            data.channels.forEach(ch => {
                const dot = ch.status === '已连接' ? 'status-online' : 'status-offline';
                html += '<tr class="border-b border-gray-700/50"><td class="py-2 px-3">#' + ch.id + '</td><td class="py-2 px-3"><span class="status-dot ' + dot + '"></span> ' + ch.status + '</td><td class="py-2 px-3">' + ch.rtt + '</td></tr>';
            });
            document.getElementById('channels-body').innerHTML = html;
        }
        const hs = document.getElementById('health-status');
        if (data.mode === 'server') {
            document.getElementById('active-channels').textContent = data.clients;
            hs.textContent = '✓ 正常'; hs.className = 'text-2xl font-bold text-green-400';
        } else if (data.healthy) {
            document.getElementById('active-channels').textContent = data.channels ? data.channels.length : 0;
            hs.textContent = '✓ 健康'; hs.className = 'text-2xl font-bold text-green-400';
        } else {
            document.getElementById('active-channels').textContent = '0';
            hs.textContent = '✗ 异常'; hs.className = 'text-2xl font-bold text-red-400';
        }
        // Update strategy display + select
        if (data.strategy) {
            var stratEl = document.getElementById('cfg-strategy');
            if (stratEl) stratEl.textContent = data.strategy;
            var sel = document.getElementById('cfg-strategy-select');
            if (sel) sel.value = data.strategy;
        }
        // Update TUN mode display
        var tunEl = document.getElementById('cfg-tun-mode');
        if (tunEl) tunEl.textContent = data.tun_mode ? '已启用' : '未启用';
        // Hide TUN card in server mode
        if (data.mode) pageMode = data.mode;
        if (tunEl && tunEl.parentElement) {
            tunEl.parentElement.style.display = (data.mode === 'server') ? 'none' : '';
        }
        // Update listen address
        var listenEl = document.getElementById('cfg-listen');
        if (listenEl) listenEl.textContent = data.listen || data.server || '--';
        
        // Render servers
        renderServers(data);
    }).catch(() => {});
}
function formatBytes(b) {
    if (b < 1024) return b + 'B';
    if (b < 1048576) return (b/1024).toFixed(1) + 'KB';
    if (b < 1073741824) return (b/1048576).toFixed(1) + 'MB';
    return (b/1073741824).toFixed(2) + 'GB';
}

function renderServers(data) {
    var container = document.getElementById('server-list');
    if (!container) return;
    if (!data.servers || data.servers.length === 0) {
        container.innerHTML = '<div class="text-gray-500 text-center py-4">没有服务器配置</div>';
        return;
    }
    var html = '';
    data.servers.forEach(function(s, i) {
        var stateMap = {healthy:'健康', degraded:'降级', dead:'死亡', unknown:'未知'};
        var stateColor = {healthy:'text-green-400', degraded:'text-yellow-400', dead:'text-red-400', unknown:'text-gray-400'};
        var dotColor = s.healthy ? 'bg-green-400' : 'bg-red-400';
        var sent = formatBytes(s.sent || 0);
        var recv = formatBytes(s.recv || 0);
        var sentSpd = formatBytes(s.sent_speed || 0) + '/s';
        var recvSpd = formatBytes(s.recv_speed || 0) + '/s';
        html += '<div class="bg-gray-900 rounded-lg p-4"><div class="flex items-center justify-between"><div class="flex items-center space-x-3"><span class="w-2 h-2 rounded-full ' + dotColor + '"></span><div><div class="font-semibold">' + escapeHtml(s.name || '') + '</div><div class="text-xs text-gray-400">' + escapeHtml(s.url || '') + '</div><div class="text-xs text-gray-500">↑ ' + sent + ' (' + sentSpd + ')  ↓ ' + recv + ' (' + recvSpd + ')' + '</div></div></div><div class="flex items-center space-x-3 text-sm"><span class="' + (stateColor[s.state] || 'text-gray-400') + '">' + (stateMap[s.state] || s.state) + '</span>' + (s.rtt > 0 ? '<span class="text-gray-400">' + s.rtt + 'ms</span>' : '') + '<span class="text-gray-400">' + s.channels + ' 通道</span><button onclick="editServer(' + i + ')" class="px-2 py-1 bg-blue-700 rounded text-xs hover:bg-blue-600">编辑</button><button onclick="deleteServer(' + i + ')" class="px-2 py-1 bg-red-700 rounded text-xs hover:bg-red-600">删除</button></div></div></div>';
    });
    container.innerHTML = html;
}

function editServer(idx) {
    Promise.all([
        fetch('/api/status').then(function(r){return r.json()}),
        fetch('/api/config').then(function(r){return r.json()})
    ]).then(function(results){
        var statusData = results[0];
        var configData = results[1];
        var s = statusData.servers[idx];
        if (!s) return;
        document.getElementById('srv-name').value = s.name || '';
        document.getElementById('srv-url').value = s.url || '';
        // 从 config 获取真实 token/config
        var rawTokens = configData.server_tokens_raw || {};
        var token = rawTokens[s.name] || rawTokens[s.url] || '';
        document.getElementById('srv-token').value = token;
        // 从 status 获取权重和通道数（servers 里面有 channels 字段）
        document.getElementById('srv-conn').value = s.channels || '3';
        document.getElementById('srv-weight').value = '100';
        document.getElementById('srv-msg').textContent = '编辑服务器: ' + (s.name || idx);
        document.getElementById('srv-msg').className = 'mt-2 text-sm text-blue-400';
        document.getElementById('srv-msg').classList.remove('hidden');
        document.getElementById('add-server-panel').classList.remove('hidden');
        document.getElementById('add-server-panel').setAttribute('data-edit-idx', String(idx));
    });
}

function deleteServer(idx) {
    if (!confirm('确定删除这台服务器？')) return;
    fetch('/api/servers/delete', {
        method: 'POST', headers: {'Content-Type': 'application/json'},
        body: JSON.stringify({index: idx})
    }).then(function(r){return r.json()}).then(function(resp){
        if (resp.success) { fetch('/api/restart', { method: 'POST' }); alert('已删除，正在重启...'); setTimeout(location.reload, 5000); }
        else { alert('删除失败'); }
    }).catch(function(){ alert('请求失败'); });
}

function changeStrategy() {
    // Send strategy change via config update
    var sel = document.getElementById('cfg-strategy-select');
    if (!sel) return;
    fetch('/api/update', {
        method: 'POST',
        headers: {'Content-Type': 'application/json'},
        body: JSON.stringify({strategy: sel.value})
    }).then(function(r) { return r.json(); }).then(function(resp) {
        if (resp.success && resp.restart) {
            setTimeout(function() { restartClient(); }, 2000);
        }
    }).catch(function() {});
}

function showAddServer() {
    document.getElementById('add-server-panel').classList.remove('hidden');
}

function cancelAddServer() {
    document.getElementById('add-server-panel').classList.add('hidden');
}

function saveServer() {
    var editIdx = document.getElementById('add-server-panel').getAttribute('data-edit-idx');
    var data = {
        name: document.getElementById('srv-name').value || '未命名',
        url: document.getElementById('srv-url').value,
        token: document.getElementById('srv-token').value || '',
        connections: parseInt(document.getElementById('srv-conn').value) || 3,
        weight: parseInt(document.getElementById('srv-weight').value) || 100
    };
    if (document.getElementById('srv-insecure').checked) data.insecure = true;
    if (!data.url) {
        var msgDiv = document.getElementById('srv-msg');
        msgDiv.className = 'mt-2 text-sm text-red-400';
        msgDiv.textContent = 'URL 不能为空';
        msgDiv.classList.remove('hidden');
        return;
    }
    var msgDiv = document.getElementById('srv-msg');
    msgDiv.className = 'mt-2 text-sm text-yellow-400';
    msgDiv.textContent = '正在保存...';
    msgDiv.classList.remove('hidden');
    var url, body;
    if (editIdx !== null && editIdx !== '') {
        url = '/api/servers/update';
        body = JSON.stringify({index: parseInt(editIdx), server: data});
    } else {
        url = '/api/servers/add';
        body = JSON.stringify(data);
    }
    fetch(url, { method: 'POST', headers: {'Content-Type': 'application/json'}, body: body })
    .then(function(r) { return r.json(); })
    .then(function(resp) {
        if (resp.success) {
            msgDiv.className = 'mt-2 text-sm text-green-400';
            msgDiv.textContent = '已保存，正在重启...';
            if (resp.restart) {
                fetch('/api/restart', { method: 'POST' });
            }
            fetch('/api/restart', { method: 'POST' });
            setTimeout(function(){ cancelAddServer(); location.reload(); }, 5000);
        } else {
            msgDiv.className = 'mt-2 text-sm text-red-400';
            msgDiv.textContent = '保存失败: ' + (resp.message || '');
        }
    }).catch(function() {
        msgDiv.className = 'mt-2 text-sm text-red-400';
        msgDiv.textContent = '请求失败';
    });
}

function fetchConfig() {
    fetch('/api/config').then(r => r.json()).then(data => {
        var el;
        el = document.getElementById('cfg-listen2');
        if (el) el.textContent = data.listen || '--';
        el = document.getElementById('cfg-conn');
        if (el) el.textContent = data.connections || '--';
        el = document.getElementById('cfg-strategy2');
        if (el) el.textContent = data.strategy || '--';
        el = document.getElementById('cfg-ips');
        if (el) el.textContent = data.ips || '默认';
    }).catch(function(e){ console.error('fetchConfig:', e); });
}

function fetchLogs() {
    fetch('/api/logs').then(r => r.json()).then(data => {
        const container = document.getElementById('log-container');
        container.innerHTML = data.map(l => '<div class="log-line text-gray-300">' + escapeHtml(l) + '</div>').join('');
        container.scrollTop = container.scrollHeight;
    }).catch(() => {});
}

function escapeHtml(s) { const d = document.createElement('div'); d.textContent = s; return d.innerHTML; }
function clearLogs() { document.getElementById('log-container').innerHTML = ''; }

function restartClient() {
    fetch("/api/restart", { method: "POST" })
    .then(function(r) { return r.json(); })
    .then(function(resp) {
        if (resp.success) {
            setTimeout(function(){ location.reload(); }, 3000);
        }
    }).catch(function() {});
}

function uploadGeoFile(t){var fi=document.getElementById(t+"-file"),m=document.getElementById(t+"-msg");if(!fi.files.length){m.textContent="no file";m.className="mt-1 text-xs text-yellow-400";m.classList.remove("hidden");return}var fd=new FormData();fd.append("file",fi.files[0]);fd.append("type",t);m.textContent="uploading...";m.className="mt-1 text-xs text-blue-400";m.classList.remove("hidden");fetch("/api/geo/upload",{method:"POST",body:fd}).then(function(r){return r.json()}).then(function(resp){m.textContent=resp.success?"OK: "+resp.message:"FAIL: "+resp.message;m.className=resp.success?"mt-1 text-xs text-green-400":"mt-1 text-xs text-red-400"}).catch(function(e){m.textContent="err:"+e;m.className="mt-1 text-xs text-red-400"})}function reloadGeoData(){var m=document.getElementById("geo-reload-msg");m.textContent="reloading...";m.classList.remove("hidden");fetch("/api/geo/reload",{method:"POST"}).then(function(r){return r.json()}).then(function(resp){m.textContent=resp.success?"OK":"FAIL";setTimeout(function(){m.classList.add("hidden")},4000)}).catch(function(){m.textContent="err"})}function updateGeoStatus(){fetch("/api/status").then(function(r){return r.json()}).then(function(d){if(d.geoip)document.getElementById("geoip-status").textContent=d.geoip;if(d.geosite)document.getElementById("geosite-status").textContent=d.geosite}).catch(function(){})}
function onlineUpgrade(){var m=document.getElementById("geo-upgrade-msg");m.textContent="Downloading...";m.className="text-sm text-blue-400 self-center";m.classList.remove("hidden");fetch("/api/geo/upgrade",{method:"POST"}).then(function(r){return r.json()}).then(function(resp){if(resp.success){m.textContent="OK: "+msgLoaded();m.className="text-sm text-green-400 self-center";updateGeoStatus()}else{m.textContent="FAIL: "+resp.message;m.className="text-sm text-red-400 self-center"}}).catch(function(e){m.textContent="FAIL: "+e;m.className="text-sm text-red-400 self-center"})}function msgLoaded(){return "geo data ready"}
fetchStatus(); fetchConfig(); fetchLogs();
setInterval(updateGeoStatus,30000);
setInterval(fetchStatus, 3000);

// ======================== 全局配置编辑 ========================

function editGlobalConfig() {
    fetch('/api/config').then(function(r){return r.json()}).then(function(data){
        document.getElementById('edit-listen').value = data.listen || '';
        document.getElementById('edit-connections').value = data.connections || '3';
        document.getElementById('edit-strategy').value = data.strategy || 'failover';
        document.getElementById('edit-ips').value = data.ips || '';
        document.getElementById('edit-tun-mode').checked = !!data.tun_mode;
        document.getElementById('global-config-panel').classList.remove('hidden');
    });
}

function cancelGlobalConfig() {
    document.getElementById('global-config-panel').classList.add('hidden');
}

function saveGlobalConfig() {
    var body = {
        listen: document.getElementById('edit-listen').value,
        connections: parseInt(document.getElementById('edit-connections').value) || 3,
        strategy: document.getElementById('edit-strategy').value,
        ips: document.getElementById('edit-ips').value,
        tun_mode: document.getElementById('edit-tun-mode').checked
    };
    var msgDiv = document.getElementById('edit-global-msg');
    msgDiv.className = 'mt-2 text-sm text-yellow-400';
    msgDiv.textContent = '正在保存...';
    msgDiv.classList.remove('hidden');
    fetch('/api/update', {
        method: 'POST',
        headers: {'Content-Type': 'application/json'},
        body: JSON.stringify(body)
    }).then(function(r){return r.json()}).then(function(resp){
        if (resp.success) {
            msgDiv.className = 'mt-2 text-sm text-green-400';
            msgDiv.textContent = '已保存，正在重启...';
            if (resp.restart) {
                fetch('/api/restart', { method: 'POST' });
            }
            setTimeout(function(){ cancelGlobalConfig(); location.reload(); }, 5000);
        } else {
            msgDiv.className = 'mt-2 text-sm text-red-400';
            msgDiv.textContent = '保存失败: ' + (resp.message || '');
        }
    }).catch(function(){
        msgDiv.className = 'mt-2 text-sm text-red-400';
        msgDiv.textContent = '请求失败';
    });
}
setInterval(fetchConfig, 10000);
setInterval(fetchLogs, 2000);
</script>

    <!-- Global Config Edit Modal -->
    <div id="global-config-panel" class="fixed inset-0 bg-black/70 flex items-center justify-center hidden" style="z-index:1000">
        <div class="bg-gray-800 rounded-lg p-6 w-full max-w-md">
            <h2 class="text-xl font-semibold mb-4">修改全局配置</h2>
            <div class="space-y-4">
                <div>
                    <label class="text-gray-400 text-sm">监听地址</label>
                    <input id="edit-listen" class="w-full bg-gray-700 text-white rounded p-2 mt-1" placeholder="socks5://127.0.0.1:1080,http://127.0.0.1:30001">
                </div>
                <div>
                    <label class="text-gray-400 text-sm">连接数</label>
                    <input id="edit-connections" type="number" class="w-full bg-gray-700 text-white rounded p-2 mt-1" value="3" min="1" max="20">
                </div>
                <div>
                    <label class="text-gray-400 text-sm">模式</label>
                    <select id="edit-strategy" class="w-full bg-gray-700 text-white rounded p-2 mt-1">
                        <option value="failover">故障转移</option>
                        <option value="loadbalance">负载均衡</option>
                        <option value="latency">最低延迟</option>
                    </select>
                </div>
                <div>
                    <label class="text-gray-400 text-sm">IP策略</label>
                    <input id="edit-ips" class="w-full bg-gray-700 text-white rounded p-2 mt-1" placeholder="">
                </div>
                <div class="flex items-center space-x-2 pt-2">
                    <input id="edit-tun-mode" type="checkbox" class="w-4 h-4" onchange="toggleTun(this.checked)">
                    <label class="text-gray-400 text-sm">TUN 模式 (仅 Windows) <span id="tun-toggle-status" class="text-xs"></span></label>
                </div>
            </div>
            <div class="mt-6 flex space-x-3">
                <button onclick="saveGlobalConfig()" class="px-4 py-2 bg-green-600 text-white rounded hover:bg-green-700">保存并重启</button>
                <button onclick="cancelGlobalConfig()" class="px-4 py-2 bg-gray-600 text-white rounded hover:bg-gray-500">取消</button>
            </div>
            <div id="edit-global-msg" class="mt-2 text-sm hidden"></div>
        </div>
    </div>

</body>
</html>`

// ======================== 热加载配置 ========================

type updateRequest struct {
	Token       string `json:"token,omitempty"`
	Listen      string `json:"listen,omitempty"`
	Connections int    `json:"connections,omitempty"`
	Forward     string `json:"forward,omitempty"`
	ConnNum     int    `json:"conn_num,omitempty"`
	Insecure    *bool  `json:"insecure,omitempty"`
	IPs         string `json:"ips,omitempty"`
	Strategy    string `json:"strategy,omitempty"`
	TunMode     *bool  `json:"tun_mode,omitempty"`
}

func handleUpdateConfig(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		http.Error(w, "仅支持 POST", http.StatusMethodNotAllowed)
		return
	}
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "读取失败", http.StatusBadRequest)
		return
	}
	var req updateRequest
	if err := json.Unmarshal(body, &req); err != nil {
		http.Error(w, "JSON 解析失败: "+err.Error(), http.StatusBadRequest)
		return
	}

	reconnectMu.Lock()
	defer reconnectMu.Unlock()

	changes := []string{}

	if req.Token != "" && req.Token != token {
		log.Printf("[热加载] token 已更新")
		token = req.Token
		changes = append(changes, "token")
		reconnectNeeded = true
	}
	if req.Forward != "" && req.Forward != forwardAddr {
		log.Printf("[热加载] forward: %s -> %s", forwardAddr, req.Forward)
		forwardAddr = req.Forward
		changes = append(changes, "forward")
		reconnectNeeded = true
	}
	if req.ConnNum > 0 && req.ConnNum != connectionNum {
		log.Printf("[热加载] conn_num: %d -> %d", connectionNum, req.ConnNum)
		connectionNum = req.ConnNum
		changes = append(changes, "conn_num")
		reconnectNeeded = true
	}
	if req.Insecure != nil && *req.Insecure != insecure {
		log.Printf("[热加载] insecure: %t -> %t", insecure, *req.Insecure)
		insecure = *req.Insecure
		changes = append(changes, "insecure")
		reconnectNeeded = true
	}
	if req.IPs != "" && req.IPs != ips {
		log.Printf("[热加载] ips: %s -> %s", ips, req.IPs)
		ips = req.IPs
		ipStrategy = parseIPStrategy(ips)
		changes = append(changes, "ips")
		reconnectNeeded = true
	}
	if req.Strategy != "" && req.Strategy != tunnelConfig.Strategy {
		log.Printf("[热加载] strategy: %s -> %s", tunnelConfig.Strategy, req.Strategy)
		tunnelConfig.Strategy = req.Strategy
		if echPool != nil {
			echPool.strategy = req.Strategy
		}
		changes = append(changes, "strategy")
	}
	if req.TunMode != nil && *req.TunMode != tunnelConfig.TunMode {
		log.Printf("[热加载] tun_mode: %t -> %t", tunnelConfig.TunMode, *req.TunMode)
		tunnelConfig.TunMode = *req.TunMode
		tunMode = *req.TunMode
		changes = append(changes, "tun_mode")
	}
	if req.Listen != "" && req.Listen != tunnelConfig.Listen {
		log.Printf("[热加载] listen: %s -> %s", tunnelConfig.Listen, req.Listen)
		tunnelConfig.Listen = req.Listen
		listenAddr = req.Listen
		changes = append(changes, "listen")
		reconnectNeeded = true
	}
	if req.Connections > 0 && req.Connections != tunnelConfig.Connections {
		log.Printf("[热加载] connections: %d -> %d", tunnelConfig.Connections, req.Connections)
		tunnelConfig.Connections = req.Connections
		connectionNum = req.Connections
		changes = append(changes, "connections")
		reconnectNeeded = true
	}

	msg := "配置已更新"
	if reconnectNeeded {
		msg += "，需要点击重启按钮生效"
	}

	// 保存配置到文件
	_ = saveConfigToFile()

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"success": true,
		"changes": changes,
		"restart": reconnectNeeded,
		"message": msg,
	})
}

// copyTunConfigAndStart copies current config to a TunConfig and calls runTUNModeIfNeeded
func copyTunConfigAndStart() {
	runTUNModeIfNeeded()
}

func handleRestartClient(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		http.Error(w, "仅支持 POST", http.StatusMethodNotAllowed)
		return
	}

	go func() {
		log.Printf("[热加载] 正在重启客户端...")
		reconnectMu.Lock()
		reconnectNeeded = false
		reconnectMu.Unlock()

		time.Sleep(1 * time.Second)

		if echPool != nil {
			echPool.Stop()
			echPool = nil
		}
		stopAllListeners()

		// Rebuild from current tunnelConfig (supports multi-server)
		if len(tunnelConfig.Servers) > 0 {
			for i := range tunnelConfig.Servers {
				if tunnelConfig.Servers[i].Name == "" {
					tunnelConfig.Servers[i].Name = tunnelConfig.Servers[i].URL
				}
				if tunnelConfig.Servers[i].Connections <= 0 {
					tunnelConfig.Servers[i].Connections = tunnelConfig.Connections
				}
				if tunnelConfig.Servers[i].Connections <= 0 {
					tunnelConfig.Servers[i].Connections = 3
				}
			}
			echPool = NewMultiPool(&tunnelConfig)
			echPool.Start()
			startListeners()
			if tunMode {
				go runTUNModeIfNeeded()
			}
			serverNames := make([]string, 0, len(tunnelConfig.Servers))
			for _, s := range tunnelConfig.Servers {
				serverNames = append(serverNames, s.Name)
			}
			log.Printf("[热加载] 客户端重启完成，%d 台服务器: %v", len(tunnelConfig.Servers), serverNames)
		} else if forwardAddr != "" {
			tunnelConfig.Servers = []ServerConfig{{Name: forwardAddr, URL: forwardAddr, Token: token, Connections: connectionNum}}
			if tunnelConfig.Strategy == "" {
				tunnelConfig.Strategy = "failover"
			}
			echPool = NewMultiPool(&tunnelConfig)
			echPool.Start()
			if tunMode {
				go runTUNModeIfNeeded()
			}
			log.Printf("[热加载] 客户端重启完成（单服务器模式）: %s", forwardAddr)
		}
	}()

	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"success": true,
		"message": "客户端正在重启...",
	})
}

// ======================== Multi-Server Management ========================

func handleAddServer(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		http.Error(w, "POST only", 405)
		return
	}
	body, err := io.ReadAll(r.Body)
	if err != nil {
		http.Error(w, "read error", 400)
		return
	}
	var srv ServerConfig
	if err := json.Unmarshal(body, &srv); err != nil {
		http.Error(w, "json error", 400)
		return
	}
	if srv.URL == "" {
		http.Error(w, "URL required", 400)
		return
	}
	if srv.Connections <= 0 {
		srv.Connections = 3
	}
	if srv.Weight <= 0 {
		srv.Weight = 100
	}
	tunnelConfig.Servers = append(tunnelConfig.Servers, srv)
	_ = saveConfigToFile()
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{"success": true, "restart": true})
	go func() {
		time.Sleep(500 * time.Millisecond)
		if echPool != nil {
			echPool.Stop()
		}
		echPool = NewMultiPool(&tunnelConfig)
		echPool.Start()
	}()
}

func handleUpdateServer(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		http.Error(w, "POST only", 405)
		return
	}
	body, _ := io.ReadAll(r.Body)
	var req struct {
		Index  int          `json:"index"`
		Server ServerConfig `json:"server"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		http.Error(w, "json error", 400)
		return
	}
	if req.Index < 0 || req.Index >= len(tunnelConfig.Servers) {
		http.Error(w, "bad index", 400)
		return
	}
	if req.Server.Connections <= 0 {
		req.Server.Connections = 3
	}
	if req.Server.Weight <= 0 {
		req.Server.Weight = 100
	}
	tunnelConfig.Servers[req.Index] = req.Server
	_ = saveConfigToFile()
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{"success": true, "restart": true})
	go func() {
		time.Sleep(500 * time.Millisecond)
		if echPool != nil {
			echPool.Stop()
		}
		echPool = NewMultiPool(&tunnelConfig)
		echPool.Start()
	}()
}

func handleDeleteServer(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		http.Error(w, "POST only", 405)
		return
	}
	body, _ := io.ReadAll(r.Body)
	var req struct {
		Index int `json:"index"`
	}
	if err := json.Unmarshal(body, &req); err != nil {
		http.Error(w, "json error", 400)
		return
	}
	if req.Index < 0 || req.Index >= len(tunnelConfig.Servers) {
		http.Error(w, "bad index", 400)
		return
	}
	tunnelConfig.Servers = append(tunnelConfig.Servers[:req.Index], tunnelConfig.Servers[req.Index+1:]...)
	_ = saveConfigToFile()
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{"success": true, "restart": true})
	go func() {
		time.Sleep(500 * time.Millisecond)
		if echPool != nil {
			echPool.Stop()
		}
		if len(tunnelConfig.Servers) > 0 {
			echPool = NewMultiPool(&tunnelConfig)
			echPool.Start()
		}
	}()
}

func handleSaveConfig(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		http.Error(w, "POST only", 405)
		return
	}
	body, _ := io.ReadAll(r.Body)
	if len(body) > 0 {
		var newCfg TunnelConfig
		if err := json.Unmarshal(body, &newCfg); err == nil {
			tunnelConfig = newCfg
		}
	}
	if err := saveConfigToFile(); err != nil {
		http.Error(w, "save error", 500)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{"success": true, "message": "saved"})
}

func saveConfigToFile() error {
	p := configFile
	if p == "" {
		p = FindConfig()
	}
	if p == "" {
		p = "config.json"
	}
	return SaveConfig(p, &tunnelConfig)
}

// ======================== Geo Data Upload & Reload ========================

func handleGeoUpload(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		http.Error(w, "POST only", 405)
		return
	}
	if err := r.ParseMultipartForm(100 << 20); err != nil {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{"success": false, "message": err.Error()})
		return
	}
	file, _, err := r.FormFile("file")
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{"success": false, "message": err.Error()})
		return
	}
	defer file.Close()
	geoType := r.FormValue("type")
	filename := "geoip.dat"
	if geoType == "geosite" {
		filename = "geosite.dat"
	}
	fl, err := os.Create(filename)
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{"success": false, "message": err.Error()})
		return
	}
	defer fl.Close()
	written, err := io.Copy(fl, file)
	if err != nil {
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{"success": false, "message": err.Error()})
		return
	}
	log.Printf("[Geo] updated %s (%d bytes)", filename, written)
	directRules = nil
	proxyRules = nil
	geoIPMatcher = nil
	geoSiteMatcher = nil
	resetGeoIPMatcherCache()
	resetGeoSiteMatcherCache()
	loadGeoIP()
	loadGeoSite()
	initRules(directStr, proxyStr, defaultRouteStr)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{"success": true, "message": fmt.Sprintf("%d bytes", written)})
}

func handleGeoReload(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		http.Error(w, "POST only", 405)
		return
	}
	directRules = nil
	proxyRules = nil
	geoIPMatcher = nil
	geoSiteMatcher = nil
	resetGeoIPMatcherCache()
	resetGeoSiteMatcherCache()
	loadGeoIP()
	loadGeoSite()
	initRules(directStr, proxyStr, defaultRouteStr)
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{"success": true})
}

// ======================== Geo Data Online Upgrade ========================
var geoUpgradeMirrors = map[string][]string{
	"geoip": {
		"https://ghfast.top/https://github.com/v2fly/geoip/releases/latest/download/geoip.dat",
		"https://github.com/v2fly/geoip/releases/latest/download/geoip.dat",
	},
	"geosite": {
		"https://ghfast.top/https://github.com/v2fly/domain-list-community/releases/latest/download/dlc.dat",
		"https://github.com/v2fly/domain-list-community/releases/latest/download/dlc.dat",
	},
}

func handleGeoUpgrade(w http.ResponseWriter, r *http.Request) {
	if r.Method != "POST" {
		http.Error(w, "POST only", 405)
		return
	}
	w.Header().Set("Content-Type", "application/json")
	wg := &sync.WaitGroup{}
	results := make(map[string]string)
	var mu sync.Mutex
	for geoType, mirrors := range geoUpgradeMirrors {
		wg.Add(1)
		go func(gt string, ms []string) {
			defer wg.Done()
			filename := gt + ".dat"
			for _, url := range ms {
				err := downloadGeoFile(url, filename)
				if err == nil {
					mu.Lock()
					results[filename] = "updated"
					mu.Unlock()
					return
				}
				log.Printf("[Geo] mirror failed %s: %v", url, err)
			}
			mu.Lock()
			results[filename] = "all mirrors failed"
			mu.Unlock()
		}(geoType, mirrors)
	}
	wg.Wait()
	allOk := true
	for _, v := range results {
		if v != "updated" {
			allOk = false
			break
		}
	}
	if allOk {
		directRules = nil
		proxyRules = nil
		geoIPMatcher = nil
		geoSiteMatcher = nil
		resetGeoIPMatcherCache()
		resetGeoSiteMatcherCache()
		loadGeoIP()
		loadGeoSite()
		initRules(directStr, proxyStr, defaultRouteStr)
		log.Printf("[Geo] upgraded and reloaded")
	}
	json.NewEncoder(w).Encode(map[string]interface{}{"success": allOk, "results": results})
}

func downloadGeoFile(url, filename string) error {
	client := &http.Client{Timeout: 5 * time.Minute}
	resp, err := client.Get(url)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != 200 {
		return fmt.Errorf("HTTP %d", resp.StatusCode)
	}
	tmpName := filename + ".tmp"
	f, err := os.Create(tmpName)
	if err != nil {
		return err
	}
	defer f.Close()
	n, err := io.Copy(f, resp.Body)
	if err != nil {
		os.Remove(tmpName)
		return err
	}
	if n < 1000 {
		os.Remove(tmpName)
		return fmt.Errorf("file too small (%d bytes)", n)
	}
	f.Close()
	return os.Rename(tmpName, filename)
}
