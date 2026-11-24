// MyGithubTracker 是一个追踪 GitHub 星标仓库 Release 更新的本地小工具。
package main

import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"net"
	"net/http"
	"os"
	"os/signal"
	"path/filepath"
	"sync"
	"syscall"
	"time"

	"mygithubtracker/internal/config"
	"mygithubtracker/internal/download"
	"mygithubtracker/internal/logx"
	"mygithubtracker/internal/manager"
	"mygithubtracker/internal/server"
	"mygithubtracker/internal/store"
	"mygithubtracker/internal/window"
)

const (
	appVersion   = "1.2.0"
	basePort     = 17653
	portAttempts = 12
)

var errAlreadyRunning = errors.New("程序已在运行")

func main() {
	dir := config.Dir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		fmt.Fprintln(os.Stderr, "创建配置目录失败:", err)
	}

	lg, err := logx.Open(filepath.Join(dir, "app.log"))
	if err != nil {
		fmt.Fprintln(os.Stderr, "打开日志失败:", err)
	}
	defer lg.Close()
	lg.Printf("=== MyGithubTracker %s 启动 ===", appVersion)

	cfg, err := config.Load()
	if err != nil {
		lg.Errorf("加载配置失败: %v", err)
		cfg = config.Default()
	}

	st, err := store.Open(filepath.Join(dir, "state.json"), lg.Printf)
	if err != nil {
		lg.Errorf("打开状态文件失败: %v", err)
		fmt.Fprintln(os.Stderr, "打开状态文件失败:", err)
		os.Exit(1)
	}

	hub := server.NewHub()
	mgr := manager.New(cfg, st, hub.Broadcast, lg.Printf)
	dl := download.New(cfg.HTTPClient(0), cfg.DownloadDir, cfg.Token, func(t download.Task) {
		hub.Broadcast(manager.Event{Type: "download", Data: t})
	}, lg.Printf)

	token := randomToken()
	srv := server.New(server.Options{
		Config:     cfg,
		Manager:    mgr,
		Downloader: dl,
		Store:      st,
		Hub:        hub,
		Log:        lg,
		Token:      token,
		Version:    appVersion,
	})

	ln, err := listenOrWake()
	if err != nil {
		if errors.Is(err, errAlreadyRunning) {
			lg.Printf("检测到已有实例，已唤起其窗口，本次退出")
			return
		}
		lg.Errorf("监听端口失败: %v", err)
		fmt.Fprintln(os.Stderr, "监听端口失败:", err)
		os.Exit(1)
	}

	baseURL := "http://" + ln.Addr().String()
	appURL := baseURL + "/?t=" + token

	hs := &http.Server{Handler: srv.Handler(), ReadHeaderTimeout: 15 * time.Second}
	go func() {
		if err := hs.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			lg.Errorf("HTTP 服务异常退出: %v", err)
			srv.Shutdown()
		}
	}()
	lg.Printf("本地服务已启动: %s", baseURL)

	stopSave := make(chan struct{})
	var stopOnce sync.Once
	stop := func() { stopOnce.Do(func() { close(stopSave) }) }
	defer stop()
	go st.SaveLoop(stopSave, 2*time.Second)

	exitCh := make(chan struct{})
	var exitOnce sync.Once
	triggerExit := func(reason string) {
		exitOnce.Do(func() {
			lg.Printf("退出信号: %s", reason)
			close(exitCh)
		})
	}

	profileDir := filepath.Join(dir, "window-profile")
	startedAt := time.Now()
	cmd, werr := window.OpenAppWindow(appURL, profileDir, 1200, 820)
	if werr != nil {
		lg.Errorf("打开应用窗口失败: %v（回退到默认浏览器）", werr)
		if err := window.OpenURL(appURL); err != nil {
			lg.Errorf("打开默认浏览器失败: %v", err)
		}
		srv.EnableHeartbeatWatch()
	} else {
		go func() {
			_ = cmd.Wait()
			if time.Since(startedAt) > 2*time.Second {
				triggerExit("窗口已关闭")
				return
			}
			lg.Printf("浏览器已把窗口转交给已有实例，启用心跳兜底")
			srv.EnableHeartbeatWatch()
		}()
	}

	// 第二实例唤起时再开一个窗口（同一 profile，Edge 会转交给已有实例）。
	go func() {
		for {
			select {
			case <-exitCh:
				return
			case <-srv.Wake():
				lg.Printf("收到唤起请求，重新打开窗口")
				if _, err := window.OpenAppWindow(appURL, profileDir, 1200, 820); err != nil {
					_ = window.OpenURL(appURL)
				}
			}
		}
	}()

	// 心跳看门狗：仅在窗口被转交、无法通过进程退出来判断时生效。
	go func() {
		ticker := time.NewTicker(10 * time.Second)
		defer ticker.Stop()
		for {
			select {
			case <-exitCh:
				return
			case <-ticker.C:
				if srv.HeartbeatExpired(4 * time.Minute) {
					triggerExit("窗口心跳超时")
					return
				}
			}
		}
	}()

	sig := make(chan os.Signal, 1)
	signal.Notify(sig, os.Interrupt, syscall.SIGTERM)

	select {
	case <-srv.Quit():
		lg.Printf("收到退出请求")
	case <-exitCh:
	case <-sig:
		lg.Printf("收到系统信号")
	}

	if err := st.Flush(); err != nil {
		lg.Errorf("退出前保存状态失败: %v", err)
	}
	_ = hs.Close()
	lg.Printf("=== 已退出 ===")
}

func randomToken() string {
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		return fmt.Sprintf("%d", time.Now().UnixNano())
	}
	return hex.EncodeToString(b)
}

func listenOrWake() (net.Listener, error) {
	for i := 0; i < portAttempts; i++ {
		addr := fmt.Sprintf("127.0.0.1:%d", basePort+i)
		if probeExisting(addr) {
			wakeExisting(addr)
			return nil, errAlreadyRunning
		}
		if ln, err := net.Listen("tcp", addr); err == nil {
			return ln, nil
		}
	}
	return net.Listen("tcp", "127.0.0.1:0")
}

func probeExisting(addr string) bool {
	client := &http.Client{Timeout: 400 * time.Millisecond}
	resp, err := client.Get("http://" + addr + "/api/ping")
	if err != nil {
		return false
	}
	defer func() { _ = resp.Body.Close() }()
	var out struct {
		App string `json:"app"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return false
	}
	return out.App == "MyGithubTracker"
}

func wakeExisting(addr string) {
	client := &http.Client{Timeout: 2 * time.Second}
	resp, err := client.Post("http://"+addr+"/api/wake", "application/json", nil)
	if err == nil {
		_ = resp.Body.Close()
	}
}
