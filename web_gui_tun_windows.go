//go:build windows

package main

import (
	"encoding/json"
	"log"
	"net/http"
)

func handleTunToggle(w http.ResponseWriter, r *http.Request) {
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
	_ = saveConfigToFile()
	tunMu.Unlock()

	if req.Enable {
		log.Printf("[TUN] 正在切换到 TUN 模式（需要管理员权限）...")
		go spawnNewProcess(true)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"success":  true,
			"tun_mode": true,
			"active":   false,
			"message":  "TUN 模式已启用，正在重启进程...",
		})
	} else {
		log.Printf("[TUN] 正在关闭 TUN 模式...")
		go spawnNewProcess(false)
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"success":  true,
			"tun_mode": false,
			"active":   false,
			"message":  "TUN 模式已关闭，正在重启进程...",
		})
	}
}

func handleTunStatus(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"tun_mode": tunMode,
		"active":   IsTunActive(),
	})
}
