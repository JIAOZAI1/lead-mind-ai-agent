package handler

import (
	"encoding/json"
	"net/http"
)

// Health 报告基础存活状态，不要求携带身份请求头。
func Health(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}
