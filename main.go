package main

import (
	"encoding/json"
	"fmt"
	"net/http"
	"os"
)

// API Key middleware
func authMiddleware(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		apiKey := os.Getenv("MANAGER_API_KEY")
		requestKey := r.Header.Get("X-API-Key")

		if requestKey == "" || requestKey != apiKey {
			w.Header().Set("Content-Type", "application/json")
			w.WriteHeader(http.StatusUnauthorized)
			json.NewEncoder(w).Encode(map[string]string{
				"error": "Invalid or missing API key",
			})
			return
		}

		next(w, r)
	}
}

func main() {
	k8s, err := NewK8sClient()
	if err != nil {
		fmt.Println("K8s connect error:", err)
		os.Exit(1)
	}

	http.HandleFunc("/stream/start", authMiddleware(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}

		var cfg StreamConfig
		if err := json.NewDecoder(r.Body).Decode(&cfg); err != nil {
			http.Error(w, "Invalid JSON", http.StatusBadRequest)
			return
		}

		if cfg.Name == "" || cfg.StreamID == "" || cfg.VideoURLs == "" {
			http.Error(w, "Name, StreamID, VideoURLs required", http.StatusBadRequest)
			return
		}

		// ✅ StreamInfo সহ receive করুন
		info, err := k8s.StartStream(cfg)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"message":    "Stream started",
			"name":       info.Name,
			"port":       info.Port,
			"access_url": info.AccessURL, // ✅ client এই URL এ connect করবে
		})
	}))

	http.HandleFunc("/stream/stop", authMiddleware(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodPost {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}

		var body struct {
			Name      string `json:"name"`
			Namespace string `json:"namespace"`
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			http.Error(w, "Invalid JSON", http.StatusBadRequest)
			return
		}

		if body.Name == "" {
			http.Error(w, "name required", http.StatusBadRequest)
			return
		}

		if err := k8s.StopStream(body.Name, body.Namespace); err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]string{
			"message": "Stream stopped",
			"name":    body.Name,
		})
	}))

	http.HandleFunc("/stream/list", authMiddleware(func(w http.ResponseWriter, r *http.Request) {
		if r.Method != http.MethodGet {
			http.Error(w, "Method not allowed", http.StatusMethodNotAllowed)
			return
		}

		namespace := r.URL.Query().Get("namespace")
		streams, err := k8s.ListStreams(namespace)
		if err != nil {
			http.Error(w, err.Error(), http.StatusInternalServerError)
			return
		}

		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(map[string]interface{}{
			"streams": streams,
			"total":   len(streams),
		})
	}))

	fmt.Println("🚀 Server running on :9090")
	http.ListenAndServe(":9090", nil)
}
