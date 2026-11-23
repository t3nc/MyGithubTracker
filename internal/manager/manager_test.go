package manager

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"sync"
	"testing"

	"mygithubtracker/internal/config"
	"mygithubtracker/internal/store"
)

type fakeGitHub struct {
	mu          sync.Mutex
	tag         string
	etag        string
	readmeSHA   string
	releaseReqs int
	commitReqs  int
	lastINM     string
}

func (f *fakeGitHub) handler() http.Handler {
	mux := http.NewServeMux()

	mux.HandleFunc("/user/starred", func(w http.ResponseWriter, r *http.Request) {
		repos := []map[string]any{
			{
				"full_name":        "acme/tool",
				"name":             "tool",
				"description":      "有 Release 的仓库",
				"html_url":         "https://github.com/acme/tool",
				"stargazers_count": 42,
				"language":         "Go",
			},
			{
				"full_name":        "acme/quiet",
				"name":             "quiet",
				"description":      "只有 README 的仓库",
				"html_url":         "https://github.com/acme/quiet",
				"stargazers_count": 7,
			},
			{
				"full_name":        "acme/empty",
				"name":             "empty",
				"description":      "既无 Release 也无 README",
				"html_url":         "https://github.com/acme/empty",
				"stargazers_count": 1,
			},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(repos)
	})

	mux.HandleFunc("/repos/acme/tool/releases", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.releaseReqs++
		f.lastINM = r.Header.Get("If-None-Match")
		tag, etag := f.tag, f.etag
		f.mu.Unlock()

		if inm := r.Header.Get("If-None-Match"); inm != "" && inm == etag {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		w.Header().Set("ETag", etag)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode([]map[string]any{
			{
				"tag_name":     tag,
				"name":         "Release " + tag,
				"html_url":     "https://github.com/acme/tool/releases/tag/" + tag,
				"published_at": "2026-11-20T10:00:00Z",
				"body":         "## 更新内容\n\n- 修复若干问题\n- 新增一键下载",
				"assets": []map[string]any{
					{"name": "tool-windows-amd64.zip", "size": 1024, "browser_download_url": "https://github.com/acme/tool/releases/download/x/tool.zip"},
					{"name": "tool-linux-amd64.tar.gz", "size": 2048, "browser_download_url": "https://github.com/acme/tool/releases/download/x/tool.tar.gz"},
				},
			},
			{
				"tag_name":     "v0.9.0",
				"name":         "Release v0.9.0",
				"html_url":     "https://github.com/acme/tool/releases/tag/v0.9.0",
				"published_at": "2026-10-01T10:00:00Z",
				"assets":       []map[string]any{},
			},
			{
				"tag_name":     "v9.9.9-beta",
				"name":         "Beta",
				"published_at": "2026-11-21T10:00:00Z",
				"prerelease":   true,
				"assets":       []map[string]any{},
			},
			{
				"tag_name": "v9.9.9-draft",
				"name":     "Draft",
				"draft":    true,
				"assets":   []map[string]any{},
			},
		})
	})

	emptyReleases := func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.releaseReqs++
		f.mu.Unlock()
		w.Header().Set("ETag", `W/"empty"`)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte("[]"))
	}
	mux.HandleFunc("/repos/acme/quiet/releases", emptyReleases)
	mux.HandleFunc("/repos/acme/empty/releases", emptyReleases)

	mux.HandleFunc("/repos/acme/quiet/commits", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.commitReqs++
		sha := f.readmeSHA
		f.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode([]map[string]any{
			{
				"sha":      sha,
				"html_url": "https://github.com/acme/quiet/commit/" + sha,
				"commit": map[string]any{
					"message": "docs: 更新使用说明",
					"author":  map[string]any{"name": "Alice", "date": "2026-11-19T08:30:00Z"},
				},
			},
		})
	})

	mux.HandleFunc("/repos/acme/empty/commits", func(w http.ResponseWriter, r *http.Request) {
		f.mu.Lock()
		f.commitReqs++
		f.mu.Unlock()
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte("[]"))
	})

	return mux
}

func newTestManager(t *testing.T) (*Manager, *store.Store, *fakeGitHub, *httptest.Server) {
	t.Helper()
	fake := &fakeGitHub{
		tag:       "v1.0.0",
		etag:      `W/"etag-1"`,
		readmeSHA: "aaaa1111bbbb2222",
	}
	srv := httptest.NewServer(fake.handler())
	t.Cleanup(srv.Close)

	cfg := config.Default()
	cfg.Token = "test-token"
	cfg.Concurrency = 2
	cfg.ProxyMode = config.ProxyNone
	cfg.RecentCount = 3

	st, err := store.Open(filepath.Join(t.TempDir(), "state.json"), nil)
	if err != nil {
		t.Fatalf("打开状态库失败: %v", err)
	}
	m := New(cfg, st, func(any) {}, nil)
	m.Client().BaseURL = srv.URL
	return m, st, fake, srv
}

func TestRefreshEstablishesBaselineThenDetectsUpdate(t *testing.T) {
	m, st, fake, _ := newTestManager(t)

	if err := m.Refresh(false); err != nil {
		t.Fatalf("首次刷新失败: %v", err)
	}

	if got := len(st.Snapshot()); got != 3 {
		t.Fatalf("期望 3 个仓库，实际 %d", got)
	}

	tool, ok := st.Get("acme/tool")
	if !ok {
		t.Fatal("缺少 acme/tool")
	}
	if tool.Mode() != store.TrackRelease {
		t.Fatalf("追踪模式应为 release，实际 %q", tool.Mode())
	}
	if tool.LatestTag != "v1.0.0" {
		t.Fatalf("应取最新正式版 v1.0.0，实际 %q", tool.LatestTag)
	}
	if tool.HasUpdate {
		t.Fatal("首次同步不应标记为有更新（应建立基线）")
	}
	if tool.SeenTag != "v1.0.0" {
		t.Fatalf("基线应为 v1.0.0，实际 %q", tool.SeenTag)
	}
	if len(tool.Releases) != 3 {
		t.Fatalf("应缓存 3 个版本（排除 draft），实际 %d", len(tool.Releases))
	}
	if len(tool.Assets) != 2 {
		t.Fatalf("应缓存最新版本的 2 个附件，实际 %d", len(tool.Assets))
	}

	// 没有 Release 的仓库应回退到 README 追踪
	quiet, _ := st.Get("acme/quiet")
	if quiet.Mode() != store.TrackReadme {
		t.Fatalf("quiet 应为 readme 模式，实际 %q", quiet.Mode())
	}
	if quiet.ReadmeSHA == "" || quiet.ReadmeMessage == "" {
		t.Fatal("应记录 README 提交信息")
	}
	if quiet.HasUpdate {
		t.Fatal("README 首次同步不应标记为有更新")
	}
	if quiet.UpdateAt() == "" {
		t.Fatal("readme 模式应有更新时间")
	}

	// 既无 Release 也无 README
	empty, _ := st.Get("acme/empty")
	if empty.Mode() != store.TrackNone {
		t.Fatalf("empty 应为 none 模式，实际 %q", empty.Mode())
	}
	if !empty.NoReadme {
		t.Fatal("empty 应标记 NoReadme")
	}

	// 上游发布新版本
	fake.mu.Lock()
	fake.tag = "v1.1.0"
	fake.etag = `W/"etag-2"`
	fake.mu.Unlock()

	if err := m.Refresh(false); err != nil {
		t.Fatalf("第二次刷新失败: %v", err)
	}
	tool, _ = st.Get("acme/tool")
	if !tool.HasUpdate {
		t.Fatal("发布新版本后应标记为有更新")
	}
	if tool.SeenTag != "v1.0.0" {
		t.Fatalf("已读版本不应被自动推进，实际 %q", tool.SeenTag)
	}

	if !m.MarkSeen("acme/tool") {
		t.Fatal("标记已读失败")
	}
	tool, _ = st.Get("acme/tool")
	if tool.HasUpdate {
		t.Fatal("标记已读后不应再有更新标记")
	}
}

func TestReadmeUpdateDetectionAndMarkSeen(t *testing.T) {
	m, st, fake, _ := newTestManager(t)
	if err := m.Refresh(false); err != nil {
		t.Fatalf("首次刷新失败: %v", err)
	}

	fake.mu.Lock()
	fake.readmeSHA = "cccc3333dddd4444"
	fake.mu.Unlock()

	if err := m.Refresh(false); err != nil {
		t.Fatalf("第二次刷新失败: %v", err)
	}

	quiet, _ := st.Get("acme/quiet")
	if !quiet.HasUpdate {
		t.Fatal("README 变更后应标记为有更新")
	}
	if quiet.ReadmeSHA != "cccc3333dddd4444" {
		t.Fatalf("应记录新的提交 sha，实际 %q", quiet.ReadmeSHA)
	}
	if quiet.SeenReadmeSHA == quiet.ReadmeSHA {
		t.Fatal("已读 sha 不应被自动推进")
	}

	if !m.MarkSeen("acme/quiet") {
		t.Fatal("readme 模式标记已读失败")
	}
	quiet, _ = st.Get("acme/quiet")
	if quiet.HasUpdate {
		t.Fatal("标记已读后 readme 模式不应再有更新")
	}
}

func TestRefreshSendsIfNoneMatchAndHandles304(t *testing.T) {
	m, st, fake, _ := newTestManager(t)
	if err := m.Refresh(false); err != nil {
		t.Fatalf("首次刷新失败: %v", err)
	}

	fake.mu.Lock()
	beforeRelease := fake.releaseReqs
	beforeCommit := fake.commitReqs
	fake.mu.Unlock()

	if err := m.Refresh(false); err != nil {
		t.Fatalf("第二次刷新失败: %v", err)
	}

	fake.mu.Lock()
	releaseDelta := fake.releaseReqs - beforeRelease
	commitDelta := fake.commitReqs - beforeCommit
	inm := fake.lastINM
	fake.mu.Unlock()

	if releaseDelta != 3 {
		t.Fatalf("3 个仓库应各请求一次 Release 列表，实际 %d", releaseDelta)
	}
	if inm != `W/"etag-1"` {
		t.Fatalf("应携带 If-None-Match，实际 %q", inm)
	}
	// quiet / empty 两个仓库即使 Release 列表命中 304，也必须继续检查 README
	if commitDelta != 2 {
		t.Fatalf("readme 模式仓库应继续检查提交，实际请求 %d 次", commitDelta)
	}

	tool, _ := st.Get("acme/tool")
	if tool.ETag != `W/"etag-1"` {
		t.Fatalf("命中 304 后应保留原 ETag，实际 %q", tool.ETag)
	}
	if tool.LatestTag != "v1.0.0" {
		t.Fatalf("命中 304 后应保留原版本数据，实际 %q", tool.LatestTag)
	}
	if len(tool.Releases) != 3 {
		t.Fatalf("命中 304 后应保留版本列表，实际 %d", len(tool.Releases))
	}
	if tool.HasUpdate {
		t.Fatal("命中 304 不应产生更新标记")
	}
}

func TestDetailReturnsBodiesAndAssets(t *testing.T) {
	m, _, _, _ := newTestManager(t)
	if err := m.Refresh(false); err != nil {
		t.Fatalf("刷新失败: %v", err)
	}

	mode, releases, commit, err := m.Detail(context.Background(), "acme/tool")
	if err != nil {
		t.Fatalf("获取详情失败: %v", err)
	}
	if mode != store.TrackRelease {
		t.Fatalf("模式应为 release，实际 %q", mode)
	}
	if len(releases) != 3 {
		t.Fatalf("应返回 3 个版本，实际 %d", len(releases))
	}
	if releases[0].Body == "" {
		t.Fatal("详情应包含版本正文")
	}
	if len(releases[0].Assets) != 2 {
		t.Fatalf("详情应包含附件，实际 %d", len(releases[0].Assets))
	}
	if commit != nil {
		t.Fatal("release 模式不应返回 README 提交")
	}

	mode, releases, commit, err = m.Detail(context.Background(), "acme/quiet")
	if err != nil {
		t.Fatalf("获取 README 详情失败: %v", err)
	}
	if mode != store.TrackReadme {
		t.Fatalf("模式应为 readme，实际 %q", mode)
	}
	if len(releases) != 0 {
		t.Fatalf("readme 模式不应有版本列表，实际 %d", len(releases))
	}
	if commit == nil || commit.SHA == "" {
		t.Fatal("应返回 README 提交详情")
	}
}

func TestRefreshWithoutToken(t *testing.T) {
	cfg := config.Default()
	cfg.Token = ""
	st, err := store.Open(filepath.Join(t.TempDir(), "state.json"), nil)
	if err != nil {
		t.Fatal(err)
	}
	m := New(cfg, st, nil, nil)
	if err := m.Refresh(false); err != ErrNoToken {
		t.Fatalf("未配置 Token 时应返回 ErrNoToken，实际 %v", err)
	}
}
