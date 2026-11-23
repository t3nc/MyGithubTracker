// Package config 负责配置文件的定位、加载、校验与保存，以及出站 HTTP 客户端（含代理）的构建。
package config

import (
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"time"

	"mygithubtracker/internal/translate"
)

// ProxyMode 定义代理模式。
type ProxyMode string

const (
	// ProxySystem 跟随系统代理（读取 HTTP_PROXY / HTTPS_PROXY 环境变量）。
	ProxySystem ProxyMode = "system"
	// ProxyNone 不使用代理。
	ProxyNone ProxyMode = "none"
	// ProxyCustom 使用用户填写的代理地址。
	ProxyCustom ProxyMode = "custom"
)

// 并发数取值范围。
const (
	DefaultConcurrency = 8
	MinConcurrency     = 1
	MaxConcurrency     = 16
)

// 每个仓库缓存的最近版本数量取值范围。
const (
	DefaultRecentCount = 3
	MinRecentCount     = 1
	MaxRecentCount     = 5
)

// Config 是持久化到 config.json 的用户配置。
type Config struct {
	Token       string    `json:"token"`
	User        string    `json:"user"`
	ProxyMode   ProxyMode `json:"proxyMode"`
	ProxyURL    string    `json:"proxyUrl"`
	DownloadDir string    `json:"downloadDir"`
	Concurrency int       `json:"concurrency"`
	RecentCount int       `json:"recentCount"`
	// HideOwned 为 true 时列表隐藏属于自己账号的仓库。
	HideOwned bool `json:"hideOwned"`

	// 翻译引擎：ai（调用 OpenAI 兼容接口）/ browser（Edge/Chromium 内置翻译）
	TranslateEngine string `json:"translateEngine"`

	// AI 翻译（OpenAI 兼容接口）
	AIBaseURL string `json:"aiBaseUrl"`
	AIKey     string `json:"aiKey"`
	AIModel   string `json:"aiModel"`

	// APIBase 覆盖 GitHub API 根地址（来自环境变量 MGT_API_BASE），不落盘。
	// 用于指向 GitHub Enterprise 或本地联调服务。
	APIBase string `json:"-"`
}

// 翻译引擎取值。
const (
	// EngineAI 通过 OpenAI 兼容接口翻译。
	EngineAI = "ai"
	// EngineBrowser 使用 Edge/Chromium 内置的翻译模型。
	EngineBrowser = "browser"
	// DefaultTranslateEngine 默认仍走 AI 接口，保持既有行为。
	DefaultTranslateEngine = EngineAI
)

// 默认翻译模型。
const DefaultAIModel = "sensenova-6.8-flash-lite"

// DefaultAIBaseURL 是默认的 OpenAI 兼容接口根地址。
const DefaultAIBaseURL = translate.DefaultBaseURL

// EffectiveAIBaseURL 返回实际使用的接口根地址（空值回退默认）。
func (c *Config) EffectiveAIBaseURL() string {
	if v := strings.TrimSpace(c.AIBaseURL); v != "" {
		return v
	}
	return DefaultAIBaseURL
}

// EffectiveAIKey 返回用户配置的翻译 Key（未配置时为空字符串）。
// 程序不再内置任何 Key，翻译前必须由用户自行填写。
func (c *Config) EffectiveAIKey() string {
	return strings.TrimSpace(c.AIKey)
}

// HasAIKey 表示是否已配置翻译 Key。
func (c *Config) HasAIKey() bool {
	return strings.TrimSpace(c.AIKey) != ""
}

// apiBaseEnv 是覆盖 API 根地址的环境变量名。
const apiBaseEnv = "MGT_API_BASE"

// Dir 返回本程序的配置目录（%APPDATA%\MyGithubTracker）。
func Dir() string {
	if v := os.Getenv("APPDATA"); v != "" {
		return filepath.Join(v, "MyGithubTracker")
	}
	if v, err := os.UserConfigDir(); err == nil {
		return filepath.Join(v, "MyGithubTracker")
	}
	return filepath.Join(os.TempDir(), "MyGithubTracker")
}

// Path 返回配置文件路径。
func Path() string {
	return filepath.Join(Dir(), "config.json")
}

func defaultDownloadDir() string {
	if home, err := os.UserHomeDir(); err == nil {
		p := filepath.Join(home, "Downloads")
		if _, err := os.Stat(p); err == nil {
			return p
		}
	}
	return filepath.Join(Dir(), "downloads")
}

// Default 返回默认配置。
func Default() *Config {
	return &Config{
		ProxyMode:       ProxySystem,
		DownloadDir:     defaultDownloadDir(),
		Concurrency:     DefaultConcurrency,
		RecentCount:     DefaultRecentCount,
		AIModel:         DefaultAIModel,
		TranslateEngine: DefaultTranslateEngine,
	}
}

// Clone 返回配置的深拷贝。
func (c *Config) Clone() *Config {
	if c == nil {
		return Default()
	}
	cp := *c
	return &cp
}

// Load 从磁盘读取配置，文件不存在时返回默认配置。
func Load() (*Config, error) {
	raw, err := os.ReadFile(Path())
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			cfg := Default()
			return cfg, nil
		}
		return nil, fmt.Errorf("读取配置失败: %w", err)
	}
	cfg := Default()
	if err := json.Unmarshal(raw, cfg); err != nil {
		return nil, fmt.Errorf("解析配置失败: %w", err)
	}
	cfg.normalize()
	return cfg, nil
}

func (c *Config) normalize() {
	if base := strings.TrimSpace(os.Getenv(apiBaseEnv)); base != "" {
		c.APIBase = strings.TrimRight(base, "/")
	}
	switch c.ProxyMode {
	case ProxySystem, ProxyNone, ProxyCustom:
	default:
		c.ProxyMode = ProxySystem
	}
	if c.Concurrency < MinConcurrency || c.Concurrency > MaxConcurrency {
		c.Concurrency = DefaultConcurrency
	}
	if c.RecentCount < MinRecentCount || c.RecentCount > MaxRecentCount {
		c.RecentCount = DefaultRecentCount
	}
	if strings.TrimSpace(c.DownloadDir) == "" {
		c.DownloadDir = defaultDownloadDir()
	}
	c.Token = strings.TrimSpace(c.Token)
	c.ProxyURL = strings.TrimSpace(c.ProxyURL)
	// 翻译引擎只有两种取值，空值或非法值一律回到默认的 AI 引擎。
	c.TranslateEngine = strings.ToLower(strings.TrimSpace(c.TranslateEngine))
	if c.TranslateEngine != EngineBrowser {
		c.TranslateEngine = EngineAI
	}
	c.AIBaseURL = strings.TrimSpace(c.AIBaseURL)
	c.AIKey = strings.TrimSpace(c.AIKey)
	c.AIModel = strings.TrimSpace(c.AIModel)
	if c.AIModel == "" {
		c.AIModel = DefaultAIModel
	}
}

// Validate 校验配置内容是否可用于发起请求。
func (c *Config) Validate() error {
	if c.Concurrency < MinConcurrency || c.Concurrency > MaxConcurrency {
		return fmt.Errorf("并发数需在 %d-%d 之间", MinConcurrency, MaxConcurrency)
	}
	if c.RecentCount < MinRecentCount || c.RecentCount > MaxRecentCount {
		return fmt.Errorf("最近版本数需在 %d-%d 之间", MinRecentCount, MaxRecentCount)
	}
	if c.ProxyMode == ProxyCustom {
		if _, err := ParseProxy(c.ProxyURL); err != nil {
			return err
		}
	}
	return nil
}

// Save 将配置写盘（权限 0600，先写临时文件再重命名）。
func (c *Config) Save() error {
	c.normalize()
	dir := Dir()
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return fmt.Errorf("创建配置目录失败: %w", err)
	}
	raw, err := json.MarshalIndent(c, "", "  ")
	if err != nil {
		return fmt.Errorf("序列化配置失败: %w", err)
	}
	tmp := Path() + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return fmt.Errorf("写入配置失败: %w", err)
	}
	if err := os.Rename(tmp, Path()); err != nil {
		return fmt.Errorf("保存配置失败: %w", err)
	}
	return nil
}

// ParseProxy 解析用户填写的代理地址，缺省协议时补全为 http://。
func ParseProxy(raw string) (*url.URL, error) {
	s := strings.TrimSpace(raw)
	if s == "" {
		return nil, errors.New("代理地址不能为空")
	}
	if !strings.Contains(s, "://") {
		s = "http://" + s
	}
	u, err := url.Parse(s)
	if err != nil {
		return nil, fmt.Errorf("代理地址无效: %w", err)
	}
	switch strings.ToLower(u.Scheme) {
	case "http", "https", "socks5", "socks5h":
	default:
		return nil, fmt.Errorf("不支持的代理协议 %q（可用 http/https/socks5）", u.Scheme)
	}
	if u.Host == "" {
		return nil, errors.New("代理地址缺少主机与端口")
	}
	return u, nil
}

// ProxyFunc 依据代理模式返回 Transport 的 Proxy 函数。
func (c *Config) ProxyFunc() func(*http.Request) (*url.URL, error) {
	switch c.ProxyMode {
	case ProxyNone:
		return nil
	case ProxyCustom:
		u, err := ParseProxy(c.ProxyURL)
		if err != nil {
			return nil
		}
		return http.ProxyURL(u)
	default:
		return http.ProxyFromEnvironment
	}
}

// NewTransport 依据配置创建 Transport。
func (c *Config) NewTransport() *http.Transport {
	return &http.Transport{
		Proxy:                 c.ProxyFunc(),
		MaxIdleConns:          100,
		MaxIdleConnsPerHost:   16,
		IdleConnTimeout:       90 * time.Second,
		TLSHandshakeTimeout:   20 * time.Second,
		ExpectContinueTimeout: 2 * time.Second,
		ForceAttemptHTTP2:     true,
		ResponseHeaderTimeout: 45 * time.Second,
	}
}

// HTTPClient 创建带代理配置的客户端；timeout 为 0 表示不限制总时长（用于下载）。
func (c *Config) HTTPClient(timeout time.Duration) *http.Client {
	return &http.Client{Transport: c.NewTransport(), Timeout: timeout}
}

// MaskToken 将 Token 脱敏为 ghp_****abcd 形式。
func MaskToken(t string) string {
	t = strings.TrimSpace(t)
	if t == "" {
		return ""
	}
	if len(t) <= 8 {
		return "****"
	}
	return t[:4] + "****" + t[len(t)-4:]
}
