// Package manager 编排星标仓库的刷新流程：拉取列表、并发检查最新 Release、维护进度与配额。
package manager

import (
	"context"
	"errors"
	"fmt"
	"runtime"
	"strings"
	"sync"
	"time"

	"mygithubtracker/internal/config"
	"mygithubtracker/internal/github"
	"mygithubtracker/internal/store"
)

// Event 是推送给前端的事件。
type Event struct {
	Type string `json:"type"`
	Data any    `json:"data,omitempty"`
}

// Progress 描述一次刷新的进度。
type Progress struct {
	Phase      string `json:"phase"`
	Done       int    `json:"done"`
	Total      int    `json:"total"`
	Current    string `json:"current"`
	Updated    int    `json:"updated"`
	Failed     int    `json:"failed"`
	StartedAt  string `json:"startedAt"`
	FinishedAt string `json:"finishedAt"`
}

// Summary 是一次刷新的结果概要。
type Summary struct {
	Total      int    `json:"total"`
	Updated    int    `json:"updated"`
	Failed     int    `json:"failed"`
	NoRelease  int    `json:"noRelease"`
	DurationMs int64  `json:"durationMs"`
	Aborted    bool   `json:"aborted"`
	Message    string `json:"message"`
}

// ErrBusy 表示已有刷新在进行中。
var ErrBusy = errors.New("正在刷新中，请稍候")

// ErrNoToken 表示尚未配置 Token。
var ErrNoToken = errors.New("请先在设置中填写 GitHub Token")

// Manager 是刷新编排器。
type Manager struct {
	cfg      *config.Config
	st       *store.Store
	emit     func(any)
	logf     func(format string, args ...any)
	clientMu sync.RWMutex
	client   *github.Client

	mu          sync.RWMutex
	refreshing  bool
	progress    Progress
	lastRefresh time.Time
	user        string
	cancel      context.CancelFunc

	detailMu    sync.Mutex
	detailCache map[string]detailEntry

	readmeMu    sync.Mutex
	readmeCache map[string]readmeEntry
}

// readmeEntry 是 README 正文的短时缓存（空文本表示该仓库没有 README）。
type readmeEntry struct {
	text      string
	truncated bool
	at        time.Time
}

// detailEntry 是详情弹窗数据的短时缓存。
type detailEntry struct {
	mode     string
	releases []github.Release
	readme   *github.Commit
	at       time.Time
}

const (
	detailTTL      = 5 * time.Minute
	detailCacheMax = 200
)

// New 创建 Manager。
func New(cfg *config.Config, st *store.Store, emit func(any), logf func(string, ...any)) *Manager {
	m := &Manager{
		cfg:         cfg.Clone(),
		st:          st,
		emit:        emit,
		logf:        logf,
		detailCache: map[string]detailEntry{},
		readmeCache: map[string]readmeEntry{},
	}
	m.user = cfg.User
	m.rebuildClient()
	if t := st.LastUpdated(); !t.IsZero() {
		m.lastRefresh = t
	}
	return m
}

func (m *Manager) rebuildClient() {
	c := github.New(m.cfg.HTTPClient(60*time.Second), m.cfg.Token)
	if m.cfg.APIBase != "" {
		c.BaseURL = m.cfg.APIBase
	}
	m.clientMu.Lock()
	m.client = c
	m.clientMu.Unlock()
}

// SetConfig 应用新配置（会重建底层 HTTP 客户端）。
func (m *Manager) SetConfig(cfg *config.Config) {
	m.cfg = cfg.Clone()
	m.rebuildClient()
	m.mu.Lock()
	m.user = cfg.User
	m.mu.Unlock()
}

// Client 返回当前的 GitHub 客户端。
func (m *Manager) Client() *github.Client {
	m.clientMu.RLock()
	defer m.clientMu.RUnlock()
	return m.client
}

// Config 返回当前配置副本。
func (m *Manager) Config() *config.Config { return m.cfg.Clone() }

// Refreshing 返回是否正在刷新。
func (m *Manager) Refreshing() bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.refreshing
}

// Progress 返回当前进度。
func (m *Manager) Progress() Progress {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.progress
}

// LastRefresh 返回上次刷新时间。
func (m *Manager) LastRefresh() time.Time {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.lastRefresh
}

// User 返回当前登录名。
func (m *Manager) User() string {
	m.mu.RLock()
	defer m.mu.RUnlock()
	return m.user
}

// SetUser 记录当前登录名（供顶栏展示，不落盘）。
func (m *Manager) SetUser(login string) {
	login = strings.TrimSpace(login)
	if login == "" {
		return
	}
	m.mu.Lock()
	m.user = login
	m.cfg.User = login
	m.mu.Unlock()
}

// Rate 返回最近一次请求的配额信息。
func (m *Manager) Rate() github.RateInfo { return m.Client().Rate() }

func (m *Manager) emitEvent(t string, data any) {
	if m.emit != nil {
		m.emit(Event{Type: t, Data: data})
	}
}

// Cancel 中止正在进行的刷新。
func (m *Manager) Cancel() {
	m.mu.RLock()
	c := m.cancel
	m.mu.RUnlock()
	if c != nil {
		c()
	}
}

// Refresh 执行一次刷新；force 为 true 时忽略 ETag 缓存做全量校验。
func (m *Manager) Refresh(force bool) error {
	if m.cfg.Token == "" {
		return ErrNoToken
	}

	m.mu.Lock()
	if m.refreshing {
		m.mu.Unlock()
		return ErrBusy
	}
	ctx, cancel := context.WithCancel(context.Background())
	m.cancel = cancel
	m.refreshing = true
	start := time.Now()
	m.progress = Progress{Phase: "starred", StartedAt: start.UTC().Format(time.RFC3339)}
	m.mu.Unlock()

	// 刷新会带来新的版本数据，详情缓存立即失效。
	m.clearDetailCache()

	var (
		summary Summary
		fatal   error
	)

	defer func() {
		cancel()
		summary.DurationMs = time.Since(start).Milliseconds()
		summary.Total = m.Progress().Total
		summary.Updated = m.Progress().Updated
		summary.Failed = m.Progress().Failed
		if err := m.st.Flush(); err != nil && m.logf != nil {
			m.logf("刷新结束保存状态失败: %v", err)
		}
		m.mu.Lock()
		m.refreshing = false
		m.cancel = nil
		m.progress.Phase = "idle"
		m.progress.FinishedAt = time.Now().UTC().Format(time.RFC3339)
		m.progress.Current = ""
		m.lastRefresh = time.Now()
		m.mu.Unlock()
		if summary.Message == "" {
			summary.Message = "刷新完成"
		}
		m.emitEvent("refresh-done", map[string]any{
			"summary": summary,
			"rate":    m.Rate(),
			"user":    m.User(),
		})
		if m.logf != nil {
			m.logf("刷新结束: 共 %d，新版本 %d，失败 %d，耗时 %dms，中止=%v", summary.Total, summary.Updated, summary.Failed, summary.DurationMs, summary.Aborted)
		}
	}()

	m.emitEvent("progress", m.Progress())

	repos, err := m.Client().ListStarred(ctx, func(total int) {
		m.mu.Lock()
		m.progress.Phase = "starred"
		m.progress.Total = total
		m.mu.Unlock()
		m.emitEvent("progress", m.Progress())
	})
	if err != nil {
		fatal = err
		summary.Message = friendlyErr(err)
		summary.Aborted = true
		if m.logf != nil {
			m.logf("拉取星标列表失败: %v", err)
		}
		m.emitEvent("error", map[string]any{"message": summary.Message})
		return fatal
	}

	// 判定「自己的仓库」需要登录名；缺失时补一次，失败不影响刷新。
	login := m.User()
	if login == "" {
		if u, uerr := m.Client().CurrentUser(ctx); uerr == nil && u != "" {
			login = u
			m.mu.Lock()
			m.user = u
			m.mu.Unlock()
		}
	}

	metas := make([]store.RepoMeta, 0, len(repos))
	for _, r := range repos {
		metas = append(metas, store.RepoMeta{
			FullName:    r.FullName,
			Description: r.Description,
			HTMLURL:     r.HTMLURL,
			Stars:       r.StargazersCount,
			Language:    r.Language,
			Archived:    r.Archived,
			Owned:       login != "" && strings.EqualFold(r.Owner.Login, login),
		})
	}
	m.st.SyncStarred(metas)
	m.emitEvent("state-changed", nil)

	targets := m.st.Snapshot()
	m.mu.Lock()
	m.progress.Phase = "checking"
	m.progress.Total = len(targets)
	m.progress.Done = 0
	m.mu.Unlock()
	m.emitEvent("progress", m.Progress())
	m.emitEvent("state-changed", nil)

	conc := m.cfg.Concurrency
	if conc < config.MinConcurrency || conc > config.MaxConcurrency {
		conc = config.DefaultConcurrency
	}

	sem := make(chan struct{}, conc)
	var wg sync.WaitGroup
	var counters struct {
		sync.Mutex
		updated, failed, noRelease int
	}
	var abortOnce sync.Once
	var abortMsg string

	lastEmit := time.Now()

	for _, rs := range targets {
		if ctx.Err() != nil {
			break
		}
		wg.Add(1)
		go func(rs store.RepoState) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()

			if ctx.Err() != nil {
				return
			}
			updated, noRelease, err := m.checkOne(ctx, rs, force)

			counters.Lock()
			if updated {
				counters.updated++
			}
			if noRelease {
				counters.noRelease++
			}
			if err != nil {
				counters.failed++
			}
			counters.Unlock()

			m.mu.Lock()
			m.progress.Done++
			m.progress.Current = rs.FullName
			m.progress.Updated = counters.updated
			m.progress.Failed = counters.failed
			done, total := m.progress.Done, m.progress.Total
			p := m.progress
			m.mu.Unlock()

			if time.Since(lastEmit) > 150*time.Millisecond || done >= total {
				lastEmit = time.Now()
				m.emitEvent("progress", p)
			}

			if err != nil {
				if rle, ok := github.AsRateLimit(err); ok {
					abortOnce.Do(func() {
						abortMsg = rle.Error()
						if m.logf != nil {
							m.logf("配额耗尽，中止本轮刷新: %v", rle)
						}
						m.emitEvent("error", map[string]any{"message": abortMsg})
						m.Cancel()
					})
				}
			}
		}(rs)
	}
	wg.Wait()

	summary.Total = len(targets)
	summary.Updated = counters.updated
	summary.Failed = counters.failed
	summary.NoRelease = counters.noRelease
	if abortMsg != "" {
		summary.Aborted = true
		summary.Message = abortMsg
	} else if ctx.Err() != nil {
		summary.Aborted = true
		summary.Message = "刷新已取消"
	}
	m.mu.Lock()
	m.progress.Updated = counters.updated
	m.progress.Failed = counters.failed
	m.mu.Unlock()
	return nil
}

// checkOne 检查单个仓库：优先取最近 N 个 Release，没有 Release 时回退到 README 提交。
// 返回是否发现新更新、是否「无 Release」、错误。
func (m *Manager) checkOne(ctx context.Context, rs store.RepoState, force bool) (bool, bool, error) {
	now := time.Now().UTC().Format(time.RFC3339)
	n := m.recentCount()

	etag := rs.ETag
	if force {
		etag = ""
	}

	rels, notModified, newETag, err := m.Client().RecentReleases(ctx, rs.FullName, n, etag)

	if err != nil {
		if errors.Is(err, github.ErrNotFound) {
			return m.checkReadme(ctx, rs, now)
		}
		if ctx.Err() != nil {
			return false, false, nil
		}
		if _, ok := github.AsRateLimit(err); ok {
			return false, false, err
		}
		msg := friendlyErr(err)
		m.st.Mutate(rs.FullName, func(s *store.RepoState) {
			s.LastError = msg
			s.CheckedAt = now
		})
		if m.logf != nil {
			m.logf("检查 %s 失败: %v", rs.FullName, err)
		}
		return false, false, err
	}

	if notModified {
		// 该仓库此前走 README 追踪时，Release 列表的 304 不能代表 README 没变。
		if rs.Mode() != store.TrackRelease {
			return m.checkReadme(ctx, rs, now)
		}
		m.st.Mutate(rs.FullName, func(s *store.RepoState) {
			s.LastError = ""
			s.CheckedAt = now
		})
		return false, false, nil
	}

	visible := make([]github.Release, 0, len(rels))
	for _, r := range rels {
		if r.Draft {
			continue
		}
		visible = append(visible, r)
	}
	if len(visible) == 0 {
		return m.checkReadme(ctx, rs, now)
	}

	// 优先取最新正式版；全部为预发布时退回首条。
	latest := visible[0]
	for _, r := range visible {
		if !r.Prerelease {
			latest = r
			break
		}
	}

	var updated bool
	m.st.Mutate(rs.FullName, func(s *store.RepoState) {
		s.TrackMode = store.TrackRelease
		s.NoRelease = false
		s.NoReadme = false
		s.ReadmeSHA = ""
		s.ReadmeMessage = ""
		s.ReadmeAuthor = ""
		s.ReadmeAt = ""
		s.ReadmeURL = ""
		s.LastError = ""
		s.LatestTag = latest.TagName
		s.LatestName = latest.Name
		s.ReleaseURL = latest.HTMLURL
		if !latest.PublishedAt.IsZero() {
			s.PublishedAt = latest.PublishedAt.UTC().Format(time.RFC3339)
		} else if !latest.CreatedAt.IsZero() {
			s.PublishedAt = latest.CreatedAt.UTC().Format(time.RFC3339)
		} else {
			s.PublishedAt = ""
		}
		s.Prerelease = latest.Prerelease
		s.ETag = newETag
		s.CheckedAt = now
		s.Assets = make([]store.Asset, 0, len(latest.Assets))
		for _, a := range latest.Assets {
			s.Assets = append(s.Assets, store.Asset{Name: a.Name, Size: a.Size, URL: a.DownloadURL, ContentType: a.ContentType})
		}
		s.Releases = make([]store.ReleaseBrief, 0, len(visible))
		for _, r := range visible {
			if len(s.Releases) >= n {
				break
			}
			published := ""
			if !r.PublishedAt.IsZero() {
				published = r.PublishedAt.UTC().Format(time.RFC3339)
			} else if !r.CreatedAt.IsZero() {
				published = r.CreatedAt.UTC().Format(time.RFC3339)
			}
			s.Releases = append(s.Releases, store.ReleaseBrief{
				Tag:         r.TagName,
				Name:        r.Name,
				PublishedAt: published,
				HTMLURL:     r.HTMLURL,
				Prerelease:  r.Prerelease,
			})
		}
		if s.SeenTag == "" {
			// 首次见到该仓库：建立基线，不把历史版本标记为新版本。
			s.SeenTag = latest.TagName
		}
		updated = s.HasNewUpdate()
	})
	return updated, false, nil
}

// checkReadme 追踪 README.md 的最近一次提交（用于没有 Release 的仓库）。
func (m *Manager) checkReadme(ctx context.Context, rs store.RepoState, now string) (bool, bool, error) {
	commit, err := m.Client().ReadmeCommit(ctx, rs.FullName)
	if err != nil {
		if errors.Is(err, github.ErrNotFound) {
			m.st.Mutate(rs.FullName, func(s *store.RepoState) {
				s.TrackMode = store.TrackNone
				s.NoReadme = true
				s.NoRelease = true
				s.LatestTag = ""
				s.LatestName = ""
				s.ReleaseURL = ""
				s.PublishedAt = ""
				s.Prerelease = false
				s.Assets = []store.Asset{}
				s.Releases = []store.ReleaseBrief{}
				s.ReadmeSHA = ""
				s.ReadmeMessage = ""
				s.ReadmeAuthor = ""
				s.ReadmeAt = ""
				s.ReadmeURL = ""
				s.ETag = ""
				s.LastError = ""
				s.CheckedAt = now
			})
			return false, true, nil
		}
		if ctx.Err() != nil {
			return false, false, nil
		}
		if _, ok := github.AsRateLimit(err); ok {
			return false, false, err
		}
		msg := friendlyErr(err)
		m.st.Mutate(rs.FullName, func(s *store.RepoState) {
			s.LastError = msg
			s.CheckedAt = now
		})
		if m.logf != nil {
			m.logf("检查 %s 的 README 失败: %v", rs.FullName, err)
		}
		return false, false, err
	}

	var updated bool
	m.st.Mutate(rs.FullName, func(s *store.RepoState) {
		s.TrackMode = store.TrackReadme
		s.NoRelease = true
		s.NoReadme = false
		s.LastError = ""
		s.LatestTag = ""
		s.LatestName = ""
		s.ReleaseURL = ""
		s.PublishedAt = ""
		s.Prerelease = false
		s.Assets = []store.Asset{}
		s.Releases = []store.ReleaseBrief{}
		s.ETag = ""
		s.ReadmeSHA = commit.SHA
		s.ReadmeMessage = commit.Message
		s.ReadmeAuthor = commit.Author
		s.ReadmeURL = commit.HTMLURL
		if !commit.Date.IsZero() {
			s.ReadmeAt = commit.Date.UTC().Format(time.RFC3339)
		} else {
			s.ReadmeAt = ""
		}
		s.CheckedAt = now
		if s.SeenReadmeSHA == "" {
			s.SeenReadmeSHA = commit.SHA
		}
		updated = s.HasNewUpdate()
	})
	return updated, false, nil
}

func (m *Manager) recentCount() int {
	n := m.cfg.RecentCount
	if n < config.MinRecentCount || n > config.MaxRecentCount {
		return config.DefaultRecentCount
	}
	return n
}

// Detail 返回详情弹窗所需数据：最近 N 个版本（含正文与附件），无 Release 时返回 README 提交。
func (m *Manager) Detail(ctx context.Context, fullName string) (string, []github.Release, *github.Commit, error) {
	key := strings.ToLower(strings.TrimSpace(fullName))
	if key == "" {
		return "", nil, nil, errors.New("仓库名为空")
	}

	m.detailMu.Lock()
	if e, ok := m.detailCache[key]; ok && time.Since(e.at) < detailTTL {
		m.detailMu.Unlock()
		return e.mode, e.releases, e.readme, nil
	}
	m.detailMu.Unlock()

	if _, ok := m.st.Get(fullName); !ok {
		return "", nil, nil, fmt.Errorf("仓库不存在: %s", fullName)
	}

	n := m.recentCount()
	var lastErr error

	rels, _, _, err := m.Client().RecentReleases(ctx, fullName, n, "")
	if err != nil {
		if !errors.Is(err, github.ErrNotFound) {
			lastErr = err
		}
	}

	visible := make([]github.Release, 0, len(rels))
	for _, r := range rels {
		if !r.Draft {
			visible = append(visible, r)
		}
	}

	mode := store.TrackRelease
	var commit *github.Commit
	if len(visible) == 0 {
		c, cerr := m.Client().ReadmeCommit(ctx, fullName)
		switch {
		case cerr == nil:
			commit = c
			mode = store.TrackReadme
		case errors.Is(cerr, github.ErrNotFound):
			mode = store.TrackNone
		default:
			if lastErr == nil {
				lastErr = cerr
			}
			mode = store.TrackNone
		}
	}

	if lastErr != nil && len(visible) == 0 && commit == nil {
		return mode, nil, nil, lastErr
	}

	m.detailMu.Lock()
	if len(m.detailCache) >= detailCacheMax {
		m.detailCache = map[string]detailEntry{}
	}
	m.detailCache[key] = detailEntry{mode: mode, releases: visible, readme: commit, at: time.Now()}
	m.detailMu.Unlock()

	return mode, visible, commit, nil
}

// Readme 返回仓库 README 原文；noReadme 为 true 表示该仓库没有 README。
func (m *Manager) Readme(ctx context.Context, fullName string) (text string, truncated bool, err error) {
	key := strings.ToLower(strings.TrimSpace(fullName))
	if key == "" {
		return "", false, errors.New("仓库名为空")
	}

	m.readmeMu.Lock()
	if e, ok := m.readmeCache[key]; ok && time.Since(e.at) < detailTTL {
		m.readmeMu.Unlock()
		return e.text, e.truncated, nil
	}
	m.readmeMu.Unlock()

	text, truncated, err = m.Client().Readme(ctx, fullName)
	if err != nil {
		if !errors.Is(err, github.ErrNotFound) {
			return "", false, err
		}
		// 没有 README：缓存空结果，避免重复请求
		text, truncated = "", false
	}

	m.readmeMu.Lock()
	if len(m.readmeCache) >= detailCacheMax {
		m.readmeCache = map[string]readmeEntry{}
	}
	m.readmeCache[key] = readmeEntry{text: text, truncated: truncated, at: time.Now()}
	m.readmeMu.Unlock()

	return text, truncated, nil
}

func (m *Manager) clearDetailCache() {
	m.detailMu.Lock()
	m.detailCache = map[string]detailEntry{}
	m.detailMu.Unlock()

	m.readmeMu.Lock()
	m.readmeCache = map[string]readmeEntry{}
	m.readmeMu.Unlock()
}

func friendlyErr(err error) string {
	if rle, ok := github.AsRateLimit(err); ok {
		return rle.Error()
	}
	if errors.Is(err, github.ErrNotFound) {
		return "未找到"
	}
	var apiErr *github.APIError
	if errors.As(err, &apiErr) {
		if apiErr.Status == 401 {
			return "Token 无效或已过期（401）"
		}
		return apiErr.Error()
	}
	return err.Error()
}

// Arch 返回本机架构，供资产匹配使用。
func Arch() string { return runtime.GOARCH }

// TestConnection 校验 Token 与网络（含代理）是否可用。
func (m *Manager) TestConnection(ctx context.Context) (map[string]any, error) {
	if m.cfg.Token == "" {
		return nil, ErrNoToken
	}
	start := time.Now()
	user, err := m.Client().CurrentUser(ctx)
	if err != nil {
		return nil, err
	}
	rate, err := m.Client().FetchRate(ctx)
	if err != nil {
		return nil, err
	}
	m.mu.Lock()
	m.user = user
	m.mu.Unlock()
	m.cfg.User = user
	return map[string]any{
		"user":      user,
		"rate":      rate,
		"latencyMs": time.Since(start).Milliseconds(),
	}, nil
}

// MarkSeen 把仓库标记为已读。
func (m *Manager) MarkSeen(fullName string) bool {
	ok := m.st.MarkSeen(fullName)
	if ok {
		m.emitEvent("state-changed", nil)
	}
	return ok
}
