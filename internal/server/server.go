// Package server 提供本地 HTTP 服务：静态页面、JSON API 与 SSE 事件推送。
package server

import (
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"mygithubtracker/internal/config"
	"mygithubtracker/internal/download"
	"mygithubtracker/internal/logx"
	"mygithubtracker/internal/manager"
	"mygithubtracker/internal/store"
	"mygithubtracker/internal/translate"
	"mygithubtracker/web"
)

// Hub 是 SSE 订阅中心。
type Hub struct {
	mu      sync.Mutex
	clients map[chan []byte]struct{}
}

// NewHub 创建订阅中心。
func NewHub() *Hub {
	return &Hub{clients: map[chan []byte]struct{}{}}
}

// Add 注册一个新订阅者。
func (h *Hub) Add() chan []byte {
	c := make(chan []byte, 32)
	h.mu.Lock()
	h.clients[c] = struct{}{}
	h.mu.Unlock()
	return c
}

// Remove 注销订阅者。
func (h *Hub) Remove(c chan []byte) {
	h.mu.Lock()
	if _, ok := h.clients[c]; ok {
		delete(h.clients, c)
		close(c)
	}
	h.mu.Unlock()
}

// Broadcast 向所有订阅者推送 JSON 事件（慢消费者丢帧，不阻塞）。
func (h *Hub) Broadcast(v any) {
	raw, err := json.Marshal(v)
	if err != nil {
		return
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	for c := range h.clients {
		select {
		case c <- raw:
		default:
		}
	}
}

// Options 是 Server 的依赖集合。
type Options struct {
	Config     *config.Config
	Manager    *manager.Manager
	Downloader *download.Downloader
	Store      *store.Store
	Hub        *Hub
	Log        *logx.Logger
	Token      string
	Version    string
}

// Server 是本地 HTTP 服务。
type Server struct {
	opt      Options
	cfgMu    sync.RWMutex
	cfg      *config.Config
	quit     chan struct{}
	quitOnce sync.Once
	wake     chan struct{}

	hb      atomic.Int64
	hbSeen  atomic.Bool
	hbWatch atomic.Bool

	trMu  sync.Mutex
	tr    *translate.Client
	trSig string

	logMu   sync.Mutex
	logRing []string
}

// New 创建服务实例。
func New(opt Options) *Server {
	s := &Server{
		opt:  opt,
		cfg:  opt.Config.Clone(),
		quit: make(chan struct{}),
		wake: make(chan struct{}, 1),
	}
	s.hb.Store(time.Now().UnixNano())
	return s
}

// Quit 返回程序退出信号。
func (s *Server) Quit() <-chan struct{} { return s.quit }

// Wake 返回「再开一个窗口」的通知通道。
func (s *Server) Wake() <-chan struct{} { return s.wake }

// Shutdown 触发退出。
func (s *Server) Shutdown() {
	s.quitOnce.Do(func() { close(s.quit) })
}

// Config 返回当前配置副本。
func (s *Server) Config() *config.Config {
	s.cfgMu.RLock()
	defer s.cfgMu.RUnlock()
	return s.cfg.Clone()
}

func (s *Server) setConfig(c *config.Config) {
	s.cfgMu.Lock()
	s.cfg = c
	s.cfgMu.Unlock()
}

// translator 返回翻译客户端；Key 或模型变化时重建（缓存随之清空）。
func (s *Server) translator() *translate.Client {
	cfg := s.Config()
	sig := cfg.EffectiveAIBaseURL() + "|" + cfg.EffectiveAIKey() + "|" + cfg.AIModel

	s.trMu.Lock()
	defer s.trMu.Unlock()
	if s.tr == nil || s.trSig != sig {
		s.tr = translate.New(cfg.HTTPClient(90*time.Second), cfg.EffectiveAIBaseURL(), cfg.EffectiveAIKey(), cfg.AIModel)
		s.trSig = sig
	}
	return s.tr
}

// Touch 记录前端心跳。
func (s *Server) Touch() {
	s.hb.Store(time.Now().UnixNano())
	s.hbSeen.Store(true)
}

// EnableHeartbeatWatch 启用基于心跳的退出兜底。
func (s *Server) EnableHeartbeatWatch() { s.hbWatch.Store(true) }

// HeartbeatExpired 判断心跳是否超时（需要先启用兜底且至少收到过一次心跳）。
func (s *Server) HeartbeatExpired(d time.Duration) bool {
	if !s.hbWatch.Load() || !s.hbSeen.Load() {
		return false
	}
	last := time.Unix(0, s.hb.Load())
	return time.Since(last) > d
}

// Handler 返回带中间件的处理器。
func (s *Server) Handler() http.Handler {
	mux := http.NewServeMux()
	mux.HandleFunc("/api/ping", s.handlePing)
	mux.HandleFunc("/api/wake", s.handleWake)
	mux.HandleFunc("/api/events", s.auth(s.handleEvents))
	mux.HandleFunc("/api/state", s.auth(s.handleState))
	mux.HandleFunc("/api/refresh", s.auth(s.handleRefresh))
	mux.HandleFunc("/api/cancel-refresh", s.auth(s.handleCancelRefresh))
	mux.HandleFunc("/api/read", s.auth(s.handleRead))
	mux.HandleFunc("/api/download", s.auth(s.handleDownload))
	mux.HandleFunc("/api/downloads", s.auth(s.handleDownloads))
	mux.HandleFunc("/api/releases", s.auth(s.handleReleases))
	mux.HandleFunc("/api/readme", s.auth(s.handleReadme))
	mux.HandleFunc("/api/pick-dir", s.auth(s.handlePickDir))
	mux.HandleFunc("/api/translate", s.auth(s.handleTranslate))
	mux.HandleFunc("/api/translate/test", s.auth(s.handleTranslateTest))
	mux.HandleFunc("/api/ai/models", s.auth(s.handleAIModels))
	mux.HandleFunc("/api/cancel", s.auth(s.handleCancel))
	mux.HandleFunc("/api/reveal", s.auth(s.handleReveal))
	mux.HandleFunc("/api/open", s.auth(s.handleOpen))
	mux.HandleFunc("/api/open-dir", s.auth(s.handleOpenDir))
	mux.HandleFunc("/api/reveal-log", s.auth(s.handleRevealLog))
	mux.HandleFunc("/api/config", s.auth(s.handleConfig))
	mux.HandleFunc("/api/test", s.auth(s.handleTest))
	mux.HandleFunc("/api/logs", s.auth(s.handleLogs))
	mux.HandleFunc("/api/quit", s.auth(s.handleQuit))
	mux.HandleFunc("/api/heartbeat", s.auth(s.handleHeartbeat))
	mux.HandleFunc("/", s.handleStatic)
	return s.hostGuard(mux)
}

// hostGuard 阻止非本机 Host 的访问（防 DNS rebinding）。
func (s *Server) hostGuard(next http.Handler) http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		host := r.Host
		if h, _, err := net.SplitHostPort(host); err == nil {
			host = h
		}
		host = strings.Trim(host, "[]")
		switch strings.ToLower(host) {
		case "127.0.0.1", "localhost", "::1":
		default:
			http.Error(w, "forbidden host", http.StatusForbidden)
			return
		}
		next.ServeHTTP(w, r)
	})
}

// auth 校验会话令牌（请求头或 ?t= 参数）。
func (s *Server) auth(next http.HandlerFunc) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		token := r.Header.Get("X-App-Token")
		if token == "" {
			token = r.URL.Query().Get("t")
		}
		if token != s.opt.Token {
			writeJSON(w, http.StatusUnauthorized, map[string]any{"ok": false, "error": "会话令牌无效"})
			return
		}
		next(w, r)
	}
}

func (s *Server) handleStatic(w http.ResponseWriter, r *http.Request) {
	path := r.URL.Path
	name := ""
	switch path {
	case "/", "/index.html":
		name = "index.html"
	case "/style.css":
		name = "style.css"
	case "/app.js":
		name = "app.js"
	case "/favicon.ico":
		name = "favicon.ico"
	default:
		http.NotFound(w, r)
		return
	}
	data, err := web.FS.ReadFile(name)
	if err != nil {
		http.Error(w, "资源缺失", http.StatusInternalServerError)
		return
	}
	ct := "text/plain; charset=utf-8"
	switch {
	case strings.HasSuffix(name, ".html"):
		ct = "text/html; charset=utf-8"
	case strings.HasSuffix(name, ".css"):
		ct = "text/css; charset=utf-8"
	case strings.HasSuffix(name, ".js"):
		ct = "application/javascript; charset=utf-8"
	case strings.HasSuffix(name, ".ico"):
		ct = "image/x-icon"
	}
	w.Header().Set("Content-Type", ct)
	w.Header().Set("Cache-Control", "no-store")
	_, _ = w.Write(data)
}

func (s *Server) handleEvents(w http.ResponseWriter, r *http.Request) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		http.Error(w, "不支持流式响应", http.StatusInternalServerError)
		return
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	w.WriteHeader(http.StatusOK)

	ch := s.opt.Hub.Add()
	defer s.opt.Hub.Remove(ch)

	fmt.Fprint(w, ": connected\n\n")
	flusher.Flush()

	ticker := time.NewTicker(20 * time.Second)
	defer ticker.Stop()

	for {
		select {
		case <-r.Context().Done():
			return
		case <-ticker.C:
			fmt.Fprint(w, ": ping\n\n")
			flusher.Flush()
		case msg, open := <-ch:
			if !open {
				return
			}
			fmt.Fprintf(w, "data: %s\n\n", msg)
			flusher.Flush()
		}
	}
}
