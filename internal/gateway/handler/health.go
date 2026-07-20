package handler

import (
	"encoding/json"
	"net/http"
)

// Health reports basic liveness. It does not require a tenant_id header.
func Health(w http.ResponseWriter, r *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]string{"status": "ok"})
}
