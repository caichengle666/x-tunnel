//go:build windows

package main

import (
	"encoding/json"
	"log"
	"net/http"
)

func handleTunToggle(w http.ResponseWriter, r *http.Request) {
	if !requireLocalAPI(w, r) {
		return
	}
	if r.Method != "POST" {
		http.Error(w, "POST only", http.StatusMethodNotAllowed)
		return
	}
	var req struct {
		Enable bool `json:"enable"`
	}
	if err := json.NewDecoder(r.Body).Decode(&req); err != nil {
		http.Error(w, "JSON parse error: "+err.Error(), http.StatusBadRequest)
		return
	}

	tunMu.Lock()
	if req.Enable == tunMode {
		active := tunActive
		tunMu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"success":  true,
			"tun_mode": tunMode,
			"active":   active,
			"message":  "TUN 已在请求状态",
		})
		return
	}

	tunnelConfig.TunMode = req.Enable
	tunMode = req.Enable
	log.Printf("[TUN] 切换 TUN 模式为: %v", tunMode)
	_ = saveConfigToFile()
	tunMu.Unlock()

	// 先发送 HTTP 响应（flush 到客户端）
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"success":  true,
		"tun_mode": req.Enable,
		"active":   false,
		"message": func() string {
			if req.Enable {
				return "TUN 模式已启用，正在重启服务..."
			} else {
				return "TUN 模式已关闭，正在重启服务..."
			}
		}(),
	})

	// 再启动新进程（会停旧监听器、起新进程、旧进程退出）
	go spawnNewProcess(req.Enable)
}

func handleTunStatus(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"tun_mode": tunMode,
		"active":   IsTunActive(),
	})
}
