// Package translate 通过 OpenAI 兼容的 chat/completions 接口做中英翻译。
package translate

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"sort"
	"strings"
	"sync"
	"time"
)

// 默认服务参数（SenseNova，OpenAI 兼容接口的根地址）。
const (
	DefaultBaseURL = "https://token.sensenova.cn/v1"
	DefaultModel   = "sensenova-6.8-flash-lite"
	// MaxInputRunes 限制单次翻译的输入长度，避免超长正文把模型上下文打满。
	MaxInputRunes = 12000
)

const systemPrompt = `你是一个技术文档翻译器。把用户给出的发布说明翻译成简体中文。

规则：
1. 只输出译文本身，不要任何解释、前言或结语。
2. 保留原有的 Markdown 结构：标题、列表符号、代码块、行内代码、链接地址原样保留。
3. 专有名词、软件名、命令、参数、文件名、版本号不要翻译，保持英文原样。
4. 中文里不要出现多余的英文句末标点，语气简洁、技术化。`

// Client 是翻译客户端。
type Client struct {
	hc      *http.Client
	baseURL string
	key     string
	model   string

	mu    sync.Mutex
	cache map[string]cacheEntry
}

type cacheEntry struct {
	text string
	at   time.Time
}

const (
	cacheTTL      = 2 * time.Hour
	cacheMaxItems = 300
)

// New 创建客户端；baseURL 支持根地址或完整 endpoint，内部统一归一化。
func New(hc *http.Client, baseURL, key, model string) *Client {
	if strings.TrimSpace(model) == "" {
		model = DefaultModel
	}
	return &Client{
		hc:      hc,
		baseURL: normalizeBase(baseURL),
		key:     strings.TrimSpace(key),
		model:   model,
		cache:   map[string]cacheEntry{},
	}
}

// normalizeBase 把用户填的地址归一化成根地址：
// 去掉首尾空白与末尾斜杠，并裁掉误贴的 /chat/completions、/models 后缀。
func normalizeBase(raw string) string {
	s := strings.TrimSpace(raw)
	if s == "" {
		return DefaultBaseURL
	}
	s = strings.TrimRight(s, "/")
	lower := strings.ToLower(s)
	for _, suffix := range []string{"/chat/completions", "/models"} {
		if strings.HasSuffix(lower, suffix) {
			s = strings.TrimRight(s[:len(s)-len(suffix)], "/")
			break
		}
	}
	if s == "" {
		return DefaultBaseURL
	}
	return s
}

// NormalizeBase 对外暴露根地址归一化（供设置页回显）。
func NormalizeBase(raw string) string { return normalizeBase(raw) }

// chatURL 返回补全接口地址。
func (c *Client) chatURL() string { return c.baseURL + "/chat/completions" }

// modelsURL 返回模型列表接口地址。
func (c *Client) modelsURL() string { return c.baseURL + "/models" }

// ListModels 拉取上游可用模型（OpenAI 兼容的 GET {base}/models）。
func (c *Client) ListModels(ctx context.Context) ([]string, error) {
	if c.key == "" {
		return nil, ErrNoKey
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.modelsURL(), nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.key)
	req.Header.Set("User-Agent", "MyGithubTracker/1.0")

	resp, err := c.hc.Do(req)
	if err != nil {
		return nil, fmt.Errorf("请求模型列表失败: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, fmt.Errorf("读取模型列表失败: %w", err)
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, &APIError{Status: resp.StatusCode, Body: strings.TrimSpace(string(raw))}
	}

	var out struct {
		Data []struct {
			ID string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("解析模型列表失败: %w", err)
	}

	seen := map[string]bool{}
	models := make([]string, 0, len(out.Data))
	for _, m := range out.Data {
		id := strings.TrimSpace(m.ID)
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		models = append(models, id)
	}
	if len(models) == 0 {
		return nil, errors.New("该服务没有返回可用模型")
	}
	sort.Strings(models)
	return models, nil
}

// ErrNoKey 表示未配置 API Key。
var ErrNoKey = errors.New("请先在设置中填写翻译 API Key")

// ErrEmptyInput 表示待翻译内容为空。
var ErrEmptyInput = errors.New("待翻译内容为空")

// APIError 表示上游返回的非 2xx 状态。
type APIError struct {
	Status int
	Body   string
}

func (e *APIError) Error() string {
	msg := strings.TrimSpace(e.Body)
	if len(msg) > 300 {
		msg = msg[:300] + "…"
	}
	if msg == "" {
		return fmt.Sprintf("翻译服务返回状态 %d", e.Status)
	}
	return fmt.Sprintf("翻译服务返回状态 %d: %s", e.Status, msg)
}

type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type chatRequest struct {
	Model       string        `json:"model"`
	Messages    []chatMessage `json:"messages"`
	Temperature float64       `json:"temperature"`
	Stream      bool          `json:"stream"`
}

type chatResponse struct {
	Choices []struct {
		Message struct {
			Content string `json:"content"`
		} `json:"message"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Error *struct {
		Message string `json:"message"`
		Type    string `json:"type"`
	} `json:"error"`
}

// chunkRunes 是单次请求的最大字数；超长正文分段翻译，避免上游网关超时。
const chunkRunes = 1600

// Translate 把文本翻译成中文，命中缓存时直接返回。
func (c *Client) Translate(ctx context.Context, text string) (string, bool, error) {
	if c.key == "" {
		return "", false, ErrNoKey
	}
	src := strings.TrimSpace(text)
	if src == "" {
		return "", false, ErrEmptyInput
	}
	if len([]rune(src)) > MaxInputRunes {
		src = string([]rune(src)[:MaxInputRunes])
	}

	key := cacheKey(c.model, src)
	if cached, ok := c.lookup(key); ok {
		return cached, true, nil
	}

	chunks := splitChunks(src, chunkRunes)
	var result string
	if len(chunks) == 1 {
		out, err := c.translateOne(ctx, chunks[0])
		if err != nil {
			return "", false, err
		}
		result = out
	} else {
		parts := make([]string, 0, len(chunks))
		for i, chunk := range chunks {
			if ctx.Err() != nil {
				return "", false, ctx.Err()
			}
			out, err := c.translateOne(ctx, chunk)
			if err != nil {
				return "", false, fmt.Errorf("第 %d/%d 段翻译失败: %w", i+1, len(chunks), err)
			}
			parts = append(parts, out)
		}
		result = strings.Join(parts, "\n\n")
	}

	c.store(key, result)
	return result, false, nil
}

// splitChunks 按段落把文本切成不超过 max 个字符的小段。
func splitChunks(s string, max int) []string {
	if max <= 0 {
		return []string{s}
	}
	paras := strings.Split(s, "\n\n")
	chunks := make([]string, 0, 4)
	var cur strings.Builder
	curLen := 0

	flush := func() {
		if curLen > 0 {
			text := strings.TrimSpace(cur.String())
			if text != "" {
				chunks = append(chunks, text)
			}
			cur.Reset()
			curLen = 0
		}
	}

	for _, p := range paras {
		runes := []rune(p)
		if len(runes) > max {
			flush()
			for len(runes) > 0 {
				take := max
				if len(runes) < take {
					take = len(runes)
				}
				chunks = append(chunks, string(runes[:take]))
				runes = runes[take:]
			}
			continue
		}
		if curLen > 0 && curLen+len(runes)+2 > max {
			flush()
		}
		if curLen > 0 {
			cur.WriteString("\n\n")
			curLen += 2
		}
		cur.WriteString(p)
		curLen += len(runes)
	}
	flush()

	if len(chunks) == 0 {
		return []string{s}
	}
	return chunks
}

// translateOne 发起一次 chat/completions 请求。
func (c *Client) translateOne(ctx context.Context, src string) (string, error) {
	payload, err := json.Marshal(chatRequest{
		Model: c.model,
		Messages: []chatMessage{
			{Role: "system", Content: systemPrompt},
			{Role: "user", Content: src},
		},
		Temperature: 0.2,
		Stream:      false,
	})
	if err != nil {
		return "", fmt.Errorf("构造请求失败: %w", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, c.chatURL(), bytes.NewReader(payload))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Accept", "application/json")
	req.Header.Set("Authorization", "Bearer "+c.key)
	req.Header.Set("User-Agent", "MyGithubTracker/1.0")

	resp, err := c.hc.Do(req)
	if err != nil {
		return "", fmt.Errorf("请求翻译服务失败: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", fmt.Errorf("读取翻译结果失败: %w", err)
	}

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return "", &APIError{Status: resp.StatusCode, Body: strings.TrimSpace(string(raw))}
	}

	var out chatResponse
	if err := json.Unmarshal(raw, &out); err != nil {
		return "", fmt.Errorf("解析翻译结果失败: %w", err)
	}
	if out.Error != nil && out.Error.Message != "" {
		return "", &APIError{Status: resp.StatusCode, Body: out.Error.Message}
	}
	if len(out.Choices) == 0 {
		return "", errors.New("翻译服务没有返回内容")
	}

	result := strings.TrimSpace(out.Choices[0].Message.Content)
	if result == "" {
		return "", errors.New("翻译结果为空")
	}
	return result, nil
}

// Ping 用一句英文验证 Key 与网络是否可用。
func (c *Client) Ping(ctx context.Context) (string, error) {
	out, _, err := c.Translate(ctx, "Fixed a crash when opening large files, and improved release notes rendering.")
	return out, err
}

// Model 返回当前使用的模型名。
func (c *Client) Model() string { return c.model }

func cacheKey(model, src string) string {
	sum := sha256.Sum256([]byte(model + "\x00" + src))
	return hex.EncodeToString(sum[:16])
}

func (c *Client) lookup(key string) (string, bool) {
	c.mu.Lock()
	defer c.mu.Unlock()
	e, ok := c.cache[key]
	if !ok {
		return "", false
	}
	if time.Since(e.at) > cacheTTL {
		delete(c.cache, key)
		return "", false
	}
	return e.text, true
}

func (c *Client) store(key, text string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	if len(c.cache) >= cacheMaxItems {
		c.cache = map[string]cacheEntry{}
	}
	c.cache[key] = cacheEntry{text: text, at: time.Now()}
}
