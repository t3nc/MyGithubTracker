// Package window 负责定位系统浏览器、以 --app 模式承载本地页面，以及打开链接与文件夹。
package window

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"syscall"
)

const createNoWindow = 0x08000000

func hide(cmd *exec.Cmd) {
	// 只用 CREATE_NO_WINDOW 避免弹出控制台窗口。
	// 注意不能设置 HideWindow（SW_HIDE），否则浏览器/资源管理器的首个窗口会被隐藏。
	cmd.SysProcAttr = &syscall.SysProcAttr{CreationFlags: createNoWindow}
}

// findBrowser 依次查找 Edge、Chrome 的可执行文件。
func findBrowser() (string, error) {
	candidates := []string{}
	for _, env := range []string{"ProgramFiles(x86)", "ProgramFiles", "LOCALAPPDATA"} {
		base := os.Getenv(env)
		if base == "" {
			continue
		}
		candidates = append(candidates,
			filepath.Join(base, "Microsoft", "Edge", "Application", "msedge.exe"),
			filepath.Join(base, "Google", "Chrome", "Application", "chrome.exe"),
		)
	}
	for _, c := range candidates {
		if st, err := os.Stat(c); err == nil && !st.IsDir() {
			return c, nil
		}
	}
	for _, name := range []string{"msedge.exe", "chrome.exe"} {
		if p, err := exec.LookPath(name); err == nil {
			return p, nil
		}
	}
	return "", errors.New("未找到 Microsoft Edge 或 Google Chrome，已回退到默认浏览器")
}

// OpenAppWindow 以无地址栏的应用窗口打开 url，返回的进程可等待其退出。
// profileDir 固定可复用（Edge 为单例，重复启动会把窗口转交给已有实例并立即退出）。
func OpenAppWindow(rawURL, profileDir string, width, height int) (*exec.Cmd, error) {
	bin, err := findBrowser()
	if err != nil {
		return nil, err
	}
	if err := os.MkdirAll(profileDir, 0o755); err != nil {
		return nil, fmt.Errorf("创建窗口配置目录失败: %w", err)
	}
	args := []string{
		"--app=" + rawURL,
		"--user-data-dir=" + profileDir,
		fmt.Sprintf("--window-size=%d,%d", width, height),
		"--no-first-run",
		"--no-default-browser-check",
		"--no-service-autorun",
		// 本窗口只用于渲染本地页面：关闭扩展与后台服务，
		// 避免全新 profile 触发 Edge 的首次运行推广页 / 静默安装扩展。
		// 注意：不能禁用 Translate 特性，也不能禁用组件更新与后台网络，
		// 否则浏览器内置翻译（Translator API）会拿不到模型，create 直接失败。
		"--disable-extensions",
		"--disable-sync",
		"--disable-default-apps",
		"--disable-features=msEdgeSplitScreen,msEdgeFirstRunExperience",
		"--disable-background-mode",
	}
	cmd := exec.Command(bin, args...)
	hide(cmd)
	if err := cmd.Start(); err != nil {
		return nil, fmt.Errorf("启动浏览器失败: %w", err)
	}
	return cmd, nil
}

// OpenURL 用系统默认浏览器打开链接。
func OpenURL(rawURL string) error {
	cmd := exec.Command("rundll32.exe", "url.dll,FileProtocolHandler", rawURL)
	hide(cmd)
	return cmd.Start()
}

// RevealFile 在资源管理器中选中文件。
func RevealFile(path string) error {
	if _, err := os.Stat(path); err != nil {
		return fmt.Errorf("文件不存在: %w", err)
	}
	cmd := exec.Command("explorer.exe", "/select,"+path)
	hide(cmd)
	return cmd.Start()
}

// OpenDir 打开目录。
func OpenDir(dir string) error {
	if _, err := os.Stat(dir); err != nil {
		return fmt.Errorf("目录不存在: %w", err)
	}
	cmd := exec.Command("explorer.exe", dir)
	hide(cmd)
	return cmd.Start()
}
