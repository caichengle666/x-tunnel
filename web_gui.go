package main

import (
	"container/ring"
	"encoding/json"
	"fmt"
	"log"
	"net/http"
	"flag"
	"sync"
	"sync/atomic"

)

var (
	webListen string
	logBuffer *ring.Ring
	logMu     sync.RWMutex
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
	echPool.wsConnsMu.RLock()
	for i, sess := range echPool.smuxConns {
		if sess == nil {
			continue
		}
		status := "已连接"
		if sess.IsClosed() {
			status = "已断开"
		}
		rtt := atomic.LoadInt64(&echPool.channelRTT[i])
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
	echPool.wsConnsMu.RUnlock()

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
	json.NewEncoder(w).Encode(map[string]interface{}{
		"listen":   listenAddr,
		"forward":  forwardAddr,
		"token":    token,
		"conn_num": connectionNum,
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

<div class="container mx-auto p-6">
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

fetchStatus(); fetchConfig(); fetchLogs();
setInterval(fetchStatus, 3000);
setInterval(fetchConfig, 10000);
setInterval(fetchLogs, 2000);
</script>
</body>
</html>`
