// Package github 封装 GitHub REST API 的只读调用（星标列表、最新 Release）与资产匹配。
package github

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

const (
	// DefaultBaseURL 是 GitHub API 根地址。
	DefaultBaseURL = "https://api.github.com"
	userAgent      = "MyGithubTracker/1.2"
)

// ErrNotFound 表示资源不存在（例如仓库没有发布过 Release）。
var ErrNotFound = errors.New("资源不存在")

// RateLimitError 表示 API 配额已耗尽。
type RateLimitError struct {
	Limit     int
	Remaining int
	Reset     time.Time
}

func (e *RateLimitError) Error() string {
	msg := fmt.Sprintf("GitHub API 配额已耗尽（%d/%d）", e.Remaining, e.Limit)
	if !e.Reset.IsZero() {
		msg += "，将于 " + e.Reset.Local().Format("15:04") + " 重置"
	}
	return msg
}

// APIError 表示非预期的 HTTP 状态。
type APIError struct {
	Status  int
	Message string
}

func (e *APIError) Error() string {
	if e.Message == "" {
		return fmt.Sprintf("GitHub 返回状态 %d", e.Status)
	}
	return fmt.Sprintf("GitHub 返回状态 %d: %s", e.Status, e.Message)
}

// AsRateLimit 判断错误是否为配额耗尽。
func AsRateLimit(err error) (*RateLimitError, bool) {
	var rle *RateLimitError
	if errors.As(err, &rle) {
		return rle, true
	}
	return nil, false
}

// RateInfo 记录最近一次响应携带的配额信息。
type RateInfo struct {
	Limit     int    `json:"limit"`
	Remaining int    `json:"remaining"`
	Used      int    `json:"used"`
	Reset     string `json:"reset"`
}

// Repo 是星标仓库的简要信息。
type Repo struct {
	FullName string `json:"full_name"`
	Name     string `json:"name"`
	Owner    struct {
		Login string `json:"login"`
	} `json:"owner"`
	Description     string `json:"description"`
	HTMLURL         string `json:"html_url"`
	StargazersCount int    `json:"stargazers_count"`
	Language        string `json:"language"`
	Archived        bool   `json:"archived"`
	Fork            bool   `json:"fork"`
}

// Asset 是 Release 附件。
type Asset struct {
	Name        string `json:"name"`
	Size        int64  `json:"size"`
	DownloadURL string `json:"browser_download_url"`
	APIURL      string `json:"url"`
	ContentType string `json:"content_type"`
}

// Release 是仓库的最新发布。
type Release struct {
	TagName     string    `json:"tag_name"`
	Name        string    `json:"name"`
	HTMLURL     string    `json:"html_url"`
	Body        string    `json:"body"`
	PublishedAt time.Time `json:"published_at"`
	CreatedAt   time.Time `json:"created_at"`
	Prerelease  bool      `json:"prerelease"`
	Draft       bool      `json:"draft"`
	Assets      []Asset   `json:"assets"`
}

// Client 是 GitHub API 客户端。
type Client struct {
	hc      *http.Client
	token   string
	BaseURL string

	mu   sync.RWMutex
	rate RateInfo
}

// New 创建客户端。
func New(hc *http.Client, token string) *Client {
	return &Client{hc: hc, token: strings.TrimSpace(token), BaseURL: DefaultBaseURL}
}

// Rate 返回最近一次响应中的配额信息。
func (c *Client) Rate() RateInfo {
	c.mu.RLock()
	defer c.mu.RUnlock()
	return c.rate
}

func (c *Client) captureRate(resp *http.Response) {
	limit, err1 := strconv.Atoi(resp.Header.Get("X-RateLimit-Limit"))
	remain, err2 := strconv.Atoi(resp.Header.Get("X-RateLimit-Remaining"))
	reset, err3 := strconv.ParseInt(resp.Header.Get("X-RateLimit-Reset"), 10, 64)
	if err1 != nil || err2 != nil {
		return
	}
	info := RateInfo{Limit: limit, Remaining: remain, Used: limit - remain}
	if err3 == nil {
		info.Reset = time.Unix(reset, 0).UTC().Format(time.RFC3339)
	}
	c.mu.Lock()
	c.rate = info
	c.mu.Unlock()
}

func (c *Client) request(ctx context.Context, method, rawURL string, extra map[string]string, out any) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, method, rawURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("Accept", "application/vnd.github+json")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("User-Agent", userAgent)
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}
	for k, v := range extra {
		req.Header.Set(k, v)
	}

	resp, err := c.hc.Do(req)
	if err != nil {
		return nil, fmt.Errorf("网络请求失败: %w", err)
	}
	c.captureRate(resp)

	switch {
	case resp.StatusCode == http.StatusNotModified:
		_ = resp.Body.Close()
		return resp, nil
	case resp.StatusCode >= 200 && resp.StatusCode < 300:
		if out != nil {
			err := json.NewDecoder(resp.Body).Decode(out)
			_ = resp.Body.Close()
			if err != nil {
				return resp, fmt.Errorf("解析响应失败: %w", err)
			}
			return resp, nil
		}
		_, _ = io.Copy(io.Discard, io.LimitReader(resp.Body, 1<<16))
		_ = resp.Body.Close()
		return resp, nil
	case resp.StatusCode == http.StatusNotFound:
		_ = resp.Body.Close()
		return resp, ErrNotFound
	case resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusTooManyRequests:
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		_ = resp.Body.Close()
		ri := c.Rate()
		if resp.StatusCode == http.StatusTooManyRequests || ri.Remaining == 0 {
			return resp, &RateLimitError{Limit: ri.Limit, Remaining: ri.Remaining, Reset: parseTime(ri.Reset)}
		}
		msg := strings.TrimSpace(string(body))
		if resp.StatusCode == http.StatusForbidden && strings.Contains(strings.ToLower(msg), "rate limit") {
			return resp, &RateLimitError{Limit: ri.Limit, Remaining: ri.Remaining, Reset: parseTime(ri.Reset)}
		}
		return resp, &APIError{Status: resp.StatusCode, Message: msg}
	default:
		body, _ := io.ReadAll(io.LimitReader(resp.Body, 512))
		_ = resp.Body.Close()
		return resp, &APIError{Status: resp.StatusCode, Message: strings.TrimSpace(string(body))}
	}
}

func parseTime(s string) time.Time {
	if s == "" {
		return time.Time{}
	}
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return time.Time{}
	}
	return t
}

// ListStarred 拉取当前账号的全部星标仓库（分页，每页 100）。
func (c *Client) ListStarred(ctx context.Context, onPage func(total int)) ([]Repo, error) {
	var all []Repo
	for page := 1; page <= 50; page++ {
		u := fmt.Sprintf("%s/user/starred?per_page=100&page=%d&sort=created&direction=desc", c.BaseURL, page)
		var batch []Repo
		if _, err := c.request(ctx, http.MethodGet, u, nil, &batch); err != nil {
			return all, err
		}
		all = append(all, batch...)
		if onPage != nil {
			onPage(len(all))
		}
		if len(batch) < 100 {
			break
		}
	}
	return all, nil
}

// Readme 获取仓库 README 的原始 Markdown 正文（最多 200KB）。
func (c *Client) Readme(ctx context.Context, fullName string) (text string, truncated bool, err error) {
	owner, name, err := splitFullName(fullName)
	if err != nil {
		return "", false, err
	}
	u := fmt.Sprintf("%s/repos/%s/%s/readme", c.BaseURL, url.PathEscape(owner), url.PathEscape(name))

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, u, nil)
	if err != nil {
		return "", false, err
	}
	req.Header.Set("Accept", "application/vnd.github.raw")
	req.Header.Set("X-GitHub-Api-Version", "2022-11-28")
	req.Header.Set("User-Agent", userAgent)
	if c.token != "" {
		req.Header.Set("Authorization", "Bearer "+c.token)
	}

	resp, err := c.hc.Do(req)
	if err != nil {
		return "", false, fmt.Errorf("请求 README 失败: %w", err)
	}
	c.captureRate(resp)
	defer func() { _ = resp.Body.Close() }()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, readmeMaxBytes+1))
	if err != nil {
		return "", false, fmt.Errorf("读取 README 失败: %w", err)
	}

	switch {
	case resp.StatusCode >= 200 && resp.StatusCode < 300:
	case resp.StatusCode == http.StatusNotFound:
		return "", false, ErrNotFound
	case resp.StatusCode == http.StatusForbidden || resp.StatusCode == http.StatusTooManyRequests:
		ri := c.Rate()
		if resp.StatusCode == http.StatusTooManyRequests || ri.Remaining == 0 {
			return "", false, &RateLimitError{Limit: ri.Limit, Remaining: ri.Remaining, Reset: parseTime(ri.Reset)}
		}
		return "", false, &APIError{Status: resp.StatusCode, Message: strings.TrimSpace(string(raw))}
	default:
		return "", false, &APIError{Status: resp.StatusCode, Message: strings.TrimSpace(string(raw))}
	}

	if int64(len(raw)) > readmeMaxBytes {
		raw = raw[:readmeMaxBytes]
		truncated = true
		// 避免把多字节字符截断成半个
		for len(raw) > 0 && !utf8.Valid(raw) {
			raw = raw[:len(raw)-1]
		}
	}
	return strings.TrimSpace(string(raw)), truncated, nil
}

// readmeMaxBytes 限制 README 体积，避免超长文档拖慢渲染。
const readmeMaxBytes = 200 << 10

// RecentReleases 获取最近 n 个 Release（不含 draft）；etag 非空时携带 If-None-Match。
// 返回 notModified 为 true 表示命中 304（不消耗配额）。空列表表示仓库从未发布 Release。
func (c *Client) RecentReleases(ctx context.Context, fullName string, n int, etag string) ([]Release, bool, string, error) {
	owner, name, err := splitFullName(fullName)
	if err != nil {
		return nil, false, "", err
	}
	if n < 1 {
		n = 1
	}
	if n > 5 {
		n = 5
	}
	u := fmt.Sprintf("%s/repos/%s/%s/releases?per_page=%d",
		c.BaseURL, url.PathEscape(owner), url.PathEscape(name), n)
	extra := map[string]string{}
	if etag != "" {
		extra["If-None-Match"] = etag
	}
	var rels []Release
	resp, err := c.request(ctx, http.MethodGet, u, extra, &rels)
	if err != nil {
		return nil, false, "", err
	}
	if resp.StatusCode == http.StatusNotModified {
		return nil, true, etag, nil
	}
	newETag := resp.Header.Get("ETag")
	if newETag == "" {
		newETag = etag
	}
	if rels == nil {
		rels = []Release{}
	}
	return rels, false, newETag, nil
}

// Commit 是仓库某次提交的摘要。
type Commit struct {
	SHA     string    `json:"sha"`
	Message string    `json:"message"`
	Author  string    `json:"author"`
	Date    time.Time `json:"date"`
	HTMLURL string    `json:"htmlUrl"`
}

// ReadmeCommit 获取 README.md 的最近一次提交；仓库没有 README 或为空仓库时返回 ErrNotFound。
func (c *Client) ReadmeCommit(ctx context.Context, fullName string) (*Commit, error) {
	owner, name, err := splitFullName(fullName)
	if err != nil {
		return nil, err
	}
	u := fmt.Sprintf("%s/repos/%s/%s/commits?path=README.md&per_page=1",
		c.BaseURL, url.PathEscape(owner), url.PathEscape(name))

	var items []struct {
		SHA     string `json:"sha"`
		HTMLURL string `json:"html_url"`
		Commit  struct {
			Message string `json:"message"`
			Author  struct {
				Name string    `json:"name"`
				Date time.Time `json:"date"`
			} `json:"author"`
			Committer struct {
				Date time.Time `json:"date"`
			} `json:"committer"`
		} `json:"commit"`
	}
	if _, err := c.request(ctx, http.MethodGet, u, nil, &items); err != nil {
		var apiErr *APIError
		if errors.As(err, &apiErr) && apiErr.Status == http.StatusConflict {
			return nil, ErrNotFound // 空仓库
		}
		return nil, err
	}
	if len(items) == 0 {
		return nil, ErrNotFound
	}
	first := items[0]
	out := &Commit{
		SHA:     first.SHA,
		Message: strings.TrimSpace(first.Commit.Message),
		Author:  first.Commit.Author.Name,
		HTMLURL: first.HTMLURL,
	}
	if !first.Commit.Author.Date.IsZero() {
		out.Date = first.Commit.Author.Date
	} else {
		out.Date = first.Commit.Committer.Date
	}
	return out, nil
}

// FetchRate 主动查询配额。
func (c *Client) FetchRate(ctx context.Context) (RateInfo, error) {
	var out struct {
		Resources struct {
			Core struct {
				Limit     int   `json:"limit"`
				Remaining int   `json:"remaining"`
				Used      int   `json:"used"`
				Reset     int64 `json:"reset"`
			} `json:"core"`
		} `json:"resources"`
	}
	if _, err := c.request(ctx, http.MethodGet, c.BaseURL+"/rate_limit", nil, &out); err != nil {
		return c.Rate(), err
	}
	info := RateInfo{
		Limit:     out.Resources.Core.Limit,
		Remaining: out.Resources.Core.Remaining,
		Used:      out.Resources.Core.Used,
	}
	if out.Resources.Core.Reset > 0 {
		info.Reset = time.Unix(out.Resources.Core.Reset, 0).UTC().Format(time.RFC3339)
	}
	c.mu.Lock()
	c.rate = info
	c.mu.Unlock()
	return info, nil
}

// CurrentUser 返回 Token 对应的登录名。
func (c *Client) CurrentUser(ctx context.Context) (string, error) {
	var out struct {
		Login string `json:"login"`
	}
	if _, err := c.request(ctx, http.MethodGet, c.BaseURL+"/user", nil, &out); err != nil {
		return "", err
	}
	return out.Login, nil
}

func splitFullName(fullName string) (string, string, error) {
	parts := strings.SplitN(fullName, "/", 2)
	if len(parts) != 2 || parts[0] == "" || parts[1] == "" {
		return "", "", fmt.Errorf("仓库名格式错误: %q", fullName)
	}
	return parts[0], parts[1], nil
}
