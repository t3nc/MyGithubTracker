// Package store 负责仓库追踪状态的持久化（state.json）。
package store

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

const fileVersion = 1

// Asset 是从 Release 中缓存的附件信息。
type Asset struct {
	Name        string `json:"name"`
	Size        int64  `json:"size"`
	URL         string `json:"url"`
	ContentType string `json:"contentType"`
}

// 追踪模式。
const (
	// TrackRelease 追踪仓库的最新 Release。
	TrackRelease = "release"
	// TrackReadme 仓库没有 Release，退化为追踪 README.md 的最近提交。
	TrackReadme = "readme"
	// TrackNone 既没有 Release 也没有 README，无可追踪内容。
	TrackNone = "none"
)

// ReleaseBrief 是缓存的版本元信息（不含正文与附件，控制 state.json 体积）。
type ReleaseBrief struct {
	Tag         string `json:"tag"`
	Name        string `json:"name"`
	PublishedAt string `json:"publishedAt"`
	HTMLURL     string `json:"htmlUrl"`
	Prerelease  bool   `json:"prerelease"`
}

// RepoState 是单个仓库的完整追踪状态。
type RepoState struct {
	FullName    string `json:"fullName"`
	Description string `json:"description"`
	HTMLURL     string `json:"htmlUrl"`
	Stars       int    `json:"stars"`
	Language    string `json:"language"`
	Archived    bool   `json:"archived"`
	// Owned 表示该仓库属于当前登录账号自己（owner.login == 自己的登录名）。
	Owned bool `json:"owned"`

	LatestTag   string  `json:"latestTag"`
	LatestName  string  `json:"latestName"`
	ReleaseURL  string  `json:"releaseUrl"`
	PublishedAt string  `json:"publishedAt"`
	Prerelease  bool    `json:"prerelease"`
	Assets      []Asset `json:"assets"`

	// 追踪模式：release（默认）/ readme / none。旧状态文件为空字符串时按 release 处理。
	TrackMode string         `json:"trackMode"`
	Releases  []ReleaseBrief `json:"releases"`

	// README 追踪（无 Release 仓库）
	ReadmeSHA     string `json:"readmeSha"`
	ReadmeMessage string `json:"readmeMessage"`
	ReadmeAuthor  string `json:"readmeAuthor"`
	ReadmeAt      string `json:"readmeAt"`
	ReadmeURL     string `json:"readmeUrl"`
	SeenReadmeSHA string `json:"seenReadmeSha"`
	NoReadme      bool   `json:"noReadme"`

	ETag       string `json:"etag"`
	SeenTag    string `json:"seenTag"`
	NoRelease  bool   `json:"noRelease"`
	LastError  string `json:"lastError"`
	CheckedAt  string `json:"checkedAt"`
	HasUpdate  bool   `json:"hasUpdate"`
	IgnoredTag string `json:"ignoredTag"`
}

// Mode 返回规范化的追踪模式，兼容旧数据（空值视为 release）。
func (r *RepoState) Mode() string {
	switch r.TrackMode {
	case TrackReadme, TrackNone, TrackRelease:
		return r.TrackMode
	default:
		return TrackRelease
	}
}

// HasNewUpdate 判断是否存在相对「已读」的新更新。
func (r *RepoState) HasNewUpdate() bool {
	switch r.Mode() {
	case TrackReadme:
		return r.ReadmeSHA != "" && r.SeenReadmeSHA != "" && r.ReadmeSHA != r.SeenReadmeSHA
	case TrackNone:
		return false
	default:
		return r.LatestTag != "" && r.SeenTag != "" && r.LatestTag != r.SeenTag && r.LatestTag != r.IgnoredTag
	}
}

// UpdateAt 返回该仓库最近一次更新事件的时间（RFC3339），用于列表排序与时间筛选。
func (r *RepoState) UpdateAt() string {
	switch r.Mode() {
	case TrackReadme:
		return r.ReadmeAt
	case TrackNone:
		return ""
	default:
		return r.PublishedAt
	}
}

type fileData struct {
	Version   int          `json:"version"`
	UpdatedAt string       `json:"updatedAt"`
	Repos     []*RepoState `json:"repos"`
}

// Store 是并发安全的仓库状态集合，支持节流落盘。
type Store struct {
	mu        sync.RWMutex
	path      string
	repos     map[string]*RepoState
	updatedAt time.Time
	dirty     bool
	saveMu    sync.Mutex
	logf      func(format string, args ...any)
}

// Open 读取（或创建）状态文件。
func Open(path string, logf func(string, ...any)) (*Store, error) {
	s := &Store{path: path, repos: map[string]*RepoState{}, logf: logf}

	raw, err := os.ReadFile(path)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return s, nil
		}
		return nil, fmt.Errorf("读取状态文件失败: %w", err)
	}

	var data fileData
	if err := json.Unmarshal(raw, &data); err != nil {
		// 状态文件损坏：备份后重建，避免程序无法启动。
		_ = os.Rename(path, path+".broken")
		if s.logf != nil {
			s.logf("状态文件损坏，已备份为 state.json.broken: %v", err)
		}
		return s, nil
	}

	for _, r := range data.Repos {
		if r == nil || r.FullName == "" {
			continue
		}
		if r.Assets == nil {
			r.Assets = []Asset{}
		}
		if r.Releases == nil {
			r.Releases = []ReleaseBrief{}
		}
		r.HasUpdate = r.HasNewUpdate()
		s.repos[strings.ToLower(r.FullName)] = r
	}
	if t, err := time.Parse(time.RFC3339, data.UpdatedAt); err == nil {
		s.updatedAt = t
	}
	return s, nil
}

// Snapshot 返回按发布时间倒序排列的状态副本。
func (s *Store) Snapshot() []RepoState {
	s.mu.RLock()
	list := make([]RepoState, 0, len(s.repos))
	for _, r := range s.repos {
		cp := *r
		cp.HasUpdate = r.HasNewUpdate()
		if cp.Assets == nil {
			cp.Assets = []Asset{}
		}
		if cp.Releases == nil {
			cp.Releases = []ReleaseBrief{}
		}
		list = append(list, cp)
	}
	s.mu.RUnlock()

	sort.Slice(list, func(i, j int) bool {
		a, b := list[i], list[j]
		if a.HasUpdate != b.HasUpdate {
			return a.HasUpdate
		}
		// 注意：parseTime 返回的 error 非 nil 才表示「没有可用的时间」。
		ta, ea := parseTime(a.UpdateAt())
		tb, eb := parseTime(b.UpdateAt())
		switch {
		case ea != nil && eb != nil: // 都没有时间，交给 Star 数兜底
		case ea != nil: // a 无时间，排后面
			return false
		case eb != nil:
			return true
		case !ta.Equal(tb):
			return ta.After(tb)
		}
		if a.Stars != b.Stars {
			return a.Stars > b.Stars
		}
		return strings.ToLower(a.FullName) < strings.ToLower(b.FullName)
	})
	return list
}

// Get 返回单个仓库状态的副本。
func (s *Store) Get(fullName string) (RepoState, bool) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	r, ok := s.repos[strings.ToLower(fullName)]
	if !ok {
		return RepoState{}, false
	}
	cp := *r
	cp.HasUpdate = r.HasNewUpdate()
	if cp.Releases == nil {
		cp.Releases = []ReleaseBrief{}
	}
	return cp, true
}

// Mutate 在写锁内修改指定仓库状态；不存在时返回 false。
func (s *Store) Mutate(fullName string, fn func(*RepoState)) bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	r, ok := s.repos[strings.ToLower(fullName)]
	if !ok {
		return false
	}
	fn(r)
	r.HasUpdate = r.HasNewUpdate()
	s.dirty = true
	s.updatedAt = time.Now()
	return true
}

// SyncStarred 用最新的星标仓库列表刷新仓库集合：
// 已存在仓库保留追踪状态并更新元信息，新仓库写入基线，取消星标的仓库删除。
func (s *Store) SyncStarred(repos []RepoMeta) {
	s.mu.Lock()
	defer s.mu.Unlock()

	next := make(map[string]*RepoState, len(repos))
	for _, m := range repos {
		key := strings.ToLower(m.FullName)
		if cur, ok := s.repos[key]; ok {
			cur.Description = m.Description
			cur.HTMLURL = m.HTMLURL
			cur.Stars = m.Stars
			cur.Language = m.Language
			cur.Archived = m.Archived
			cur.Owned = m.Owned
			next[key] = cur
			continue
		}
		next[key] = &RepoState{
			FullName:    m.FullName,
			Description: m.Description,
			HTMLURL:     m.HTMLURL,
			Stars:       m.Stars,
			Language:    m.Language,
			Archived:    m.Archived,
			Owned:       m.Owned,
			Assets:      []Asset{},
			Releases:    []ReleaseBrief{},
			TrackMode:   TrackRelease,
		}
	}
	s.repos = next
	s.dirty = true
	s.updatedAt = time.Now()
}

// RepoMeta 是来自 GitHub 的仓库元信息。
type RepoMeta struct {
	FullName    string
	Description string
	HTMLURL     string
	Stars       int
	Language    string
	Archived    bool
	Owned       bool
}

// MarkSeen 将仓库的已读版本更新为当前最新版本（按追踪模式分别推进）。
func (s *Store) MarkSeen(fullName string) bool {
	return s.Mutate(fullName, func(r *RepoState) {
		switch r.Mode() {
		case TrackReadme:
			if r.ReadmeSHA == "" {
				return
			}
			r.SeenReadmeSHA = r.ReadmeSHA
		case TrackNone:
			return
		default:
			if r.LatestTag == "" {
				return
			}
			r.SeenTag = r.LatestTag
			r.IgnoredTag = ""
		}
	})
}

// Prune 清理 30 天未检查且无状态的遗留项（当前实现保留全部，仅用于未来扩展）。
func (s *Store) Prune(olderThan time.Duration) int {
	cut := time.Now().Add(-olderThan)
	s.mu.Lock()
	defer s.mu.Unlock()
	removed := 0
	for k, r := range s.repos {
		if r.CheckedAt == "" {
			continue
		}
		t, err := time.Parse(time.RFC3339, r.CheckedAt)
		if err == nil && t.Before(cut) && r.LatestTag == "" && r.LastError != "" {
			delete(s.repos, k)
			removed++
		}
	}
	if removed > 0 {
		s.dirty = true
	}
	return removed
}

// Flush 立即写盘（若存在未保存改动）。
func (s *Store) Flush() error {
	s.saveMu.Lock()
	defer s.saveMu.Unlock()

	s.mu.RLock()
	if !s.dirty {
		s.mu.RUnlock()
		return nil
	}
	data := fileData{Version: fileVersion, UpdatedAt: s.updatedAt.UTC().Format(time.RFC3339), Repos: make([]*RepoState, 0, len(s.repos))}
	for _, r := range s.repos {
		cp := *r
		cp.HasUpdate = r.HasNewUpdate()
		data.Repos = append(data.Repos, &cp)
	}
	s.mu.RUnlock()

	raw, err := json.Marshal(&data)
	if err != nil {
		return fmt.Errorf("序列化状态失败: %w", err)
	}
	if err := os.MkdirAll(filepath.Dir(s.path), 0o700); err != nil {
		return fmt.Errorf("创建状态目录失败: %w", err)
	}
	tmp := s.path + ".tmp"
	if err := os.WriteFile(tmp, raw, 0o600); err != nil {
		return fmt.Errorf("写入状态文件失败: %w", err)
	}
	if err := os.Rename(tmp, s.path); err != nil {
		return fmt.Errorf("保存状态文件失败: %w", err)
	}

	s.mu.Lock()
	s.dirty = false
	s.mu.Unlock()
	return nil
}

// SaveLoop 周期性落盘，直到 ctx 结束。
func (s *Store) SaveLoop(done <-chan struct{}, interval time.Duration) {
	ticker := time.NewTicker(interval)
	defer ticker.Stop()
	for {
		select {
		case <-done:
			return
		case <-ticker.C:
			if err := s.Flush(); err != nil && s.logf != nil {
				s.logf("保存状态失败: %v", err)
			}
		}
	}
}

// LastUpdated 返回最近一次状态变更时间。
func (s *Store) LastUpdated() time.Time {
	s.mu.RLock()
	defer s.mu.RUnlock()
	return s.updatedAt
}

func parseTime(s string) (time.Time, error) {
	if s == "" {
		return time.Time{}, errors.New("empty")
	}
	return time.Parse(time.RFC3339, s)
}
