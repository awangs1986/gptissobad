package control

import (
	"context"
	"encoding/json"
	"io/fs"
	"net/http"
	"strconv"
	"time"
)

func Handler(rt *Runtime, frontend fs.FS) http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /api/state", func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, rt.State())
	})
	mux.HandleFunc("PUT /api/config", func(w http.ResponseWriter, r *http.Request) {
		var in ConfigInput
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "配置格式不对"})
			return
		}
		if err := rt.UpdateConfig(in); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, rt.State())
	})
	mux.HandleFunc("POST /api/enabled", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Enabled bool `json:"enabled"`
		}
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "请求格式不对"})
			return
		}
		if err := rt.SetEnabled(in.Enabled); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, rt.State())
	})
	mux.HandleFunc("POST /api/front", func(w http.ResponseWriter, r *http.Request) {
		var in struct {
			Enabled bool `json:"enabled"`
		}
		if err := json.NewDecoder(r.Body).Decode(&in); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "请求格式不对"})
			return
		}
		if err := rt.SetFront(in.Enabled); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, rt.State())
	})
	mux.HandleFunc("POST /api/test", func(w http.ResponseWriter, r *http.Request) {
		ctx, cancel := context.WithTimeout(r.Context(), 20*time.Second)
		defer cancel()
		if err := rt.CheckTranslator(ctx); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]string{"error": "登录验证失败：" + err.Error()})
			return
		}
		rt.Log("translator login check passed")
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "message": "登录有效，翻译服务响应正常"})
	})
	mux.HandleFunc("GET /api/logs", func(w http.ResponseWriter, r *http.Request) {
		after, _ := strconv.Atoi(r.URL.Query().Get("after"))
		writeJSON(w, http.StatusOK, map[string]any{"lines": rt.LogsAfter(after)})
	})
	mux.HandleFunc("GET /api/logs/stream", func(w http.ResponseWriter, r *http.Request) {
		flusher, ok := w.(http.Flusher)
		if !ok {
			http.Error(w, "stream unsupported", http.StatusInternalServerError)
			return
		}
		w.Header().Set("Content-Type", "text/event-stream")
		w.Header().Set("Cache-Control", "no-cache")
		w.Header().Set("Connection", "keep-alive")
		after, _ := strconv.Atoi(r.URL.Query().Get("after"))
		for {
			lines := rt.LogsAfter(after)
			for _, line := range lines {
				raw, _ := json.Marshal(line)
				_, _ = w.Write([]byte("data: "))
				_, _ = w.Write(raw)
				_, _ = w.Write([]byte("\n\n"))
				after = line.Seq
			}
			flusher.Flush()
			select {
			case <-r.Context().Done():
				return
			case <-rt.waitLogs(after):
			case <-time.After(15 * time.Second):
				_, _ = w.Write([]byte(": keep\n\n"))
				flusher.Flush()
			}
		}
	})
	mux.HandleFunc("GET /{$}", serveFSFile(frontend, "index.html", "text/html; charset=utf-8"))
	mux.HandleFunc("GET /app.css", serveFSFile(frontend, "app.css", "text/css; charset=utf-8"))
	mux.HandleFunc("GET /app.js", serveFSFile(frontend, "app.js", "text/javascript; charset=utf-8"))
	return mux
}

func serveFSFile(frontend fs.FS, name, contentType string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		data, err := fs.ReadFile(frontend, name)
		if err != nil {
			http.NotFound(w, r)
			return
		}
		w.Header().Set("Content-Type", contentType)
		_, _ = w.Write(data)
	}
}

func writeJSON(w http.ResponseWriter, status int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(status)
	_ = json.NewEncoder(w).Encode(v)
}
