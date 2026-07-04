package main

import (
	"container/ring"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"flag"
	"io"
	"sync"
	"sync/atomic"
	"strings"
	"time"

)

var (
	webListen string
	logBuffer *ring.Ring
	logMu     sync.RWMutex

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

	// Capture standard log output.
	logBuffer = ring.New(logRingSize)
	log.SetOutput(logWriter{})

	mux := http.NewServeMux()
	mux.HandleFunc("/", handleDashboard)
	mux.HandleFunc("/api/status", handleStatus)
	mux.HandleFunc("/api/logs", handleLogs)
	mux.HandleFunc("/api/config", handleConfig)
	mux.HandleFunc("/api/update", handleUpdateConfig)
	mux.HandleFunc("/api/restart", handleRestartClient)

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
		"web_listen":  webListen,
	}

	// Server mode (no echPool)
	if echPool == nil {
		result["mode"]     = "server"
		result["listen"]   = listenAddr
		result["tunnel"]   = "服务端模式"
		result["healthy"]  = true
		result["clients"]  = countActiveClients()
		return result
	}

	// Client mode
	result["mode"]     = "client"
	result["server"]   = forwardAddr
	result["client_id"] = clientID

	type chInfo struct {
		ID       int    `json:"id"`
		IP       string `json:"ip"`
		Status   string `json:"status"`
		RTT      string `json:"rtt"`
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
	displayToken := ""
	if len(token) > 4 {
		displayToken = "***" + token[len(token)-4:]
	} else if token != "" {
		displayToken = "***"
	}
	json.NewEncoder(w).Encode(map[string]interface{}{
		"listen":   listenAddr,
		"forward":  forwardAddr,
		"token":    displayToken,
		"insecure": insecure,
		"ips":      ips,
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
            <div class="text-gray-400 text-sm">服务器</div>
            <div class="text-lg font-mono truncate" id="cfg-forward">--</div>
        </div>
    </div>

    <!-- Config Section -->
    <div class="bg-gray-800 rounded-lg p-6 mb-8">
        <h2 class="text-xl font-semibold mb-4">配置</h2>
        <div class="grid grid-cols-1 md:grid-cols-2 gap-4 text-sm font-mono">
            <div><span class="text-gray-400">监听:</span> <span id="cfg-listen2"></span></div>
            <div><span class="text-gray-400">转发:</span> <span id="cfg-forward2"></span></div>
            <div><span class="text-gray-400">Token:</span> <span id="cfg-token"></span></div>
            <div><span class="text-gray-400">连接数:</span> <span id="cfg-conn"></span></div>
            <div><span class="text-gray-400">Insecure:</span> <span id="cfg-insecure"></span></div>
            <div><span class="text-gray-400">IP策略:</span> <span id="cfg-ips"></span></div>
        </div>
        <div class="mt-4">
            <button onclick="showEditPanel()" class="px-4 py-2 bg-blue-600 text-white rounded hover:bg-blue-700">修改配置</button>
            <button onclick="restartClient()" class="ml-2 px-4 py-2 bg-yellow-600 text-white rounded hover:bg-yellow-700">重启客户端</button>
        </div>
    </div>

    <!-- 配置编辑面板 -->
    <div id="edit-panel" class="bg-gray-800 rounded-lg p-6 mb-8 hidden">
        <h2 class="text-xl font-semibold mb-4">修改配置</h2>
        <div class="grid grid-cols-1 md:grid-cols-2 gap-4">
            <div>
                <label class="text-gray-400 text-sm">服务器地址 (forward)</label>
                <input id="edit-forward" class="w-full bg-gray-700 text-white rounded p-2 mt-1" placeholder="ws://...">
            </div>
            <div>
                <label class="text-gray-400 text-sm">Token</label>
                <input id="edit-token" class="w-full bg-gray-700 text-white rounded p-2 mt-1" type="password">
            </div>
            <div>
                <label class="text-gray-400 text-sm">连接数 (conn_num)</label>
                <input id="edit-conn" class="w-full bg-gray-700 text-white rounded p-2 mt-1" type="number" min="1" max="20">
            </div>
            <div>
                <label class="text-gray-400 text-sm">IP 策略</label>
                <select id="edit-ips" class="w-full bg-gray-700 text-white rounded p-2 mt-1">
                    <option value="">默认</option>
                    <option value="4">仅 IPv4</option>
                    <option value="6">仅 IPv6</option>
                    <option value="4,6">IPv4 优先</option>
                    <option value="6,4">IPv6 优先</option>
                </select>
            </div>
            <div class="flex items-center space-x-2">
                <input id="edit-insecure" type="checkbox" class="w-4 h-4">
                <label class="text-gray-400 text-sm">跳过证书校验 (insecure)</label>
            </div>
        </div>
        <div class="mt-4 flex space-x-3">
            <button onclick="saveConfig()" class="px-4 py-2 bg-green-600 text-white rounded hover:bg-green-700">保存</button>
            <button onclick="cancelEdit()" class="px-4 py-2 bg-gray-600 text-white rounded hover:bg-gray-500">取消</button>
        </div>
        <div id="edit-msg" class="mt-2 text-sm hidden"></div>
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
                html += '<tr class="border-b border-gray-700/50"><td class="py-2 px-3">#' + ch.ID + '</td><td class="py-2 px-3"><span class="status-dot ' + dot + '"></span> ' + ch.status + '</td><td class="py-2 px-3">' + ch.rtt + '</td></tr>';
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
    }).catch(() => {});
}

function fetchConfig() {
    fetch('/api/config').then(r => r.json()).then(data => {
        document.getElementById('cfg-listen').textContent = data.listen || '--';
        document.getElementById('cfg-forward').textContent = data.forward || '--';
        if (data.mode === 'server') {
            document.getElementById('label-active').textContent = '已连接客户端';
        } else {
            document.getElementById('label-active').textContent = '活动通道';
        }
        document.getElementById('cfg-listen2').textContent = data.listen || '--';
        document.getElementById('cfg-forward2').textContent = data.forward || '--';
        document.getElementById('cfg-token').textContent = data.token ? '***' + data.token.slice(-4) : '--';
        document.getElementById('cfg-conn').textContent = data.conn_num || '--';
        document.getElementById('cfg-insecure').textContent = data.insecure;
        document.getElementById('cfg-ips').textContent = data.ips || '默认';
    }).catch(() => {});
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

function showEditPanel() {
    document.getElementById("edit-panel").classList.remove("hidden");
    document.getElementById("edit-forward").value = (document.getElementById("cfg-forward") || {}).textContent || "";
    document.getElementById("edit-conn").value = (document.getElementById("cfg-conn") || {}).textContent || "3";
    document.getElementById("edit-token").value = "";
    document.getElementById("edit-msg").classList.add("hidden");
}

function cancelEdit() {
    document.getElementById("edit-panel").classList.add("hidden");
}

function saveConfig() {
    var data = {
        forward: document.getElementById("edit-forward").value,
        conn_num: parseInt(document.getElementById("edit-conn").value) || 0,
        insecure: document.getElementById("edit-insecure").checked
    };
    var t = document.getElementById("edit-token").value;
    if (t) data.token = t;
    var msgDiv = document.getElementById("edit-msg");
    msgDiv.className = "mt-2 text-sm text-yellow-400";
    msgDiv.textContent = "正在保存...";
    msgDiv.classList.remove("hidden");
    fetch("/api/update", {
        method: "POST",
        headers: {"Content-Type": "application/json"},
        body: JSON.stringify(data)
    }).then(function(r) { return r.json(); }).then(function(resp) {
        if (resp.success) {
            msgDiv.className = "mt-2 text-sm text-green-400";
            msgDiv.textContent = resp.message || "已保存";
            if (resp.restart) {
                msgDiv.textContent += " 5 秒后自动重启";
                setTimeout(function() { restartClient(); }, 5000);
            } else {
                document.getElementById("edit-panel").classList.add("hidden");
            }
        }
    }).catch(function() {
        msgDiv.className = "mt-2 text-sm text-red-400";
        msgDiv.textContent = "保存失败";
    });
}

function restartClient() {
    var msgDiv = document.getElementById("edit-msg");
    msgDiv.className = "mt-2 text-sm text-yellow-400";
    msgDiv.textContent = "正在重启客户端...";
    msgDiv.classList.remove("hidden");
    fetch("/api/restart", { method: "POST" })
    .then(function(r) { return r.json(); })
    .then(function(resp) {
        if (resp.success) {
            msgDiv.className = "mt-2 text-sm text-green-400";
            msgDiv.textContent = resp.message || "重启中";
        }
    }).catch(function() {});
}

fetchStatus(); fetchConfig(); fetchLogs();
setInterval(fetchStatus, 3000);
setInterval(fetchConfig, 10000);
setInterval(fetchLogs, 2000);
</script>
</body>
</html>`


// ======================== 热加载配置 ========================

type updateRequest struct {
    Token    string `json:"token,omitempty"`
    Forward  string `json:"forward,omitempty"`
    ConnNum  int    `json:"conn_num,omitempty"`
    Insecure *bool   `json:"insecure,omitempty"`
    IPs      string `json:"ips,omitempty"`
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

    msg := "配置已更新"
    if reconnectNeeded {
        msg += "，需要点击重启按钮生效"
    }

    w.Header().Set("Content-Type", "application/json")
    json.NewEncoder(w).Encode(map[string]interface{}{
        "success": true,
        "changes": changes,
        "restart": reconnectNeeded,
        "message": msg,
    })
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

        if forwardAddr != "" && echPool != nil {
            echPool.Stop()
            echPool = nil
            var newIPs []string
            if ipAddr != "" {
                for _, p := range strings.Split(ipAddr, ",") {
                    if trimmed := strings.TrimSpace(p); trimmed != "" {
                        newIPs = append(newIPs, trimmed)
                    }
                }
            }
            simpleCfg := &TunnelConfig{
                Strategy: "failover",
                Servers:  []ServerConfig{{URL: forwardAddr, Token: token, Connections: connectionNum}},
            }
            tunnelConfig = *simpleCfg
            echPool = NewMultiPool(simpleCfg)
            echPool.Start()
            log.Printf("[热加载] 客户端重启完成，已连接 %s", forwardAddr)
        }
    }()

    w.Header().Set("Content-Type", "application/json")
    json.NewEncoder(w).Encode(map[string]interface{}{
        "success": true,
        "message": "客户端正在重启...",
    })
}

