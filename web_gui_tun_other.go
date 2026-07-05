//go:build !windows

package main

import (
	"encoding/json"
	"net/http"
)

func handleTunToggle(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"success": false,
		"message": "TUN 仅在 Windows 上可用",
	})
}

func handleTunStatus(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	json.NewEncoder(w).Encode(map[string]interface{}{
		"tun_mode": false,
		"active":   false,
	})
}
