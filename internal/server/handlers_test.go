package server

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"path/filepath"
	"strings"
	"testing"

	"mygithubtracker/internal/config"
	"mygithubtracker/internal/download"
	"mygithubtracker/internal/manager"
	"mygithubtracker/internal/store"
)

func newTestServer(t *testing.T) (*Server, *httptest.Server, string) {
	t.Helper()
	cfg := config.Default()
	cfg.Token = "gh-test-token"
	cfg.ProxyMode = config.ProxyNone

	st, err := store.Open(filepath.Join(t.TempDir(), "state.json"), nil)
	if err != nil {
		t.Fatalf("打开状态库失败: %v", err)
	}
	hub := NewHub()
	mgr := manager.New(cfg, st, hub.Broadcast, nil)
	dl := download.New(cfg.HTTPClient(0), t.TempDir(), cfg.Token, func(download.Task) {}, nil)

	session := "session-token-for-test"
	srv := New(Options{
		Config:     cfg,
		Manager:    mgr,
		Downloader: dl,
		Store:      st,
		Hub:        hub,
		Token:      session,
		Version:    "test",
	})
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)
	return srv, ts, session
}

func doJSON(t *testing.T, ts *httptest.Server, token, path, body string) (int, map[string]any) {
	t.Helper()
	req, err := http.NewRequest(http.MethodPost, ts.URL+path, strings.NewReader(body))
	if err != nil {
		t.Fatalf("构造请求失败: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-App-Token", token)
	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatalf("请求失败: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out
}

func TestAPIRequiresSessionToken(t *testing.T) {
	_, ts, _ := newTestServer(t)
	resp, err := ts.Client().Get(ts.URL + "/api/state")
	if err != nil {
		t.Fatalf("请求失败: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("缺少令牌时应返回 401，实际 %d", resp.StatusCode)
	}
}

func TestDownloadRejectsNonGitHubURL(t *testing.T) {
	_, ts, session := newTestServer(t)
	code, body := doJSON(t, ts, session, "/api/download", `{"repo":"o/r","url":"https://evil.example.com/x.zip","name":"x.zip"}`)
	if code != http.StatusBadRequest {
		t.Fatalf("非 GitHub 域名应被拒绝，实际 %d (%v)", code, body)
	}
	if msg, _ := body["error"].(string); !strings.Contains(msg, "GitHub") {
		t.Fatalf("错误提示不明确: %v", body)
	}
}

func TestDownloadRejectsUnknownRepo(t *testing.T) {
	_, ts, session := newTestServer(t)
	code, _ := doJSON(t, ts, session, "/api/download", `{"repo":"nobody/nothing"}`)
	if code != http.StatusNotFound {
		t.Fatalf("未知仓库应返回 404，实际 %d", code)
	}
}

func TestPickDirRejectsGet(t *testing.T) {
	_, ts, session := newTestServer(t)
	req, _ := http.NewRequest(http.MethodGet, ts.URL+"/api/pick-dir", nil)
	req.Header.Set("X-App-Token", session)
	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatalf("请求失败: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("GET 应被拒绝，实际 %d", resp.StatusCode)
	}
}

func TestReleasesRequiresRepoParam(t *testing.T) {
	_, ts, session := newTestServer(t)
	req, _ := http.NewRequest(http.MethodGet, ts.URL+"/api/releases", nil)
	req.Header.Set("X-App-Token", session)
	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatalf("请求失败: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusBadRequest {
		t.Fatalf("缺少 repo 应返回 400，实际 %d", resp.StatusCode)
	}
}

func TestOpenRejectsNonGitHubURL(t *testing.T) {
	_, ts, session := newTestServer(t)
	code, body := doJSON(t, ts, session, "/api/open", `{"url":"file:///C:/Windows/System32/calc.exe"}`)
	if code != http.StatusBadRequest {
		t.Fatalf("非 GitHub 链接应被拒绝，实际 %d (%v)", code, body)
	}
}

func TestOpenAllowsHomepageHost(t *testing.T) {
	_, ts, session := newTestServer(t)

	// 关于面板里的发布主页应当放行（避免真的拉起浏览器，只校验白名单判定）
	if _, ok := safeExternalURL("https://yogo.wang"); !ok {
		t.Fatal("https://yogo.wang 应被放行")
	}
	if _, ok := safeExternalURL("https://www.yogo.wang/about"); !ok {
		t.Fatal("https://www.yogo.wang 应被放行")
	}

	// 其余第三方域名仍必须拒绝
	for _, bad := range []string{
		"https://example.com",
		"https://yogo.wang.evil.com",
		"javascript:alert(1)",
	} {
		if _, ok := safeExternalURL(bad); ok {
			t.Fatalf("%s 不应被放行", bad)
		}
	}

	// 接口层面：非法域名仍返回 400
	code, body := doJSON(t, ts, session, "/api/open", `{"url":"https://example.com"}`)
	if code != http.StatusBadRequest {
		t.Fatalf("example.com 应被拒绝，实际 %d (%v)", code, body)
	}
}

func doGetJSON(t *testing.T, ts *httptest.Server, token, path string) (int, map[string]any) {
	t.Helper()
	req, err := http.NewRequest(http.MethodGet, ts.URL+path, nil)
	if err != nil {
		t.Fatalf("构造请求失败: %v", err)
	}
	req.Header.Set("X-App-Token", token)
	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatalf("请求失败: %v", err)
	}
	defer func() { _ = resp.Body.Close() }()
	var out map[string]any
	_ = json.NewDecoder(resp.Body).Decode(&out)
	return resp.StatusCode, out
}

func TestStateFiltersOwnedRepos(t *testing.T) {
	srv, ts, session := newTestServer(t)
	srv.opt.Store.SyncStarred([]store.RepoMeta{
		{FullName: "me/mine", Description: "自己的仓库", Stars: 1, Owned: true},
		{FullName: "someone/else", Description: "别人的仓库", Stars: 2},
	})

	// 默认不隐藏
	code, body := doGetJSON(t, ts, session, "/api/state")
	if code != http.StatusOK {
		t.Fatalf("状态码 %d", code)
	}
	if got := len(body["repos"].([]any)); got != 2 {
		t.Fatalf("默认应显示 2 个仓库，实际 %d", got)
	}
	if body["ownedCount"].(float64) != 1 || body["hiddenCount"].(float64) != 0 {
		t.Fatalf("计数字段异常: owned=%v hidden=%v", body["ownedCount"], body["hiddenCount"])
	}

	// 开启隐藏
	cfg := srv.Config()
	cfg.HideOwned = true
	srv.setConfig(cfg)

	_, body = doGetJSON(t, ts, session, "/api/state")
	repos := body["repos"].([]any)
	if len(repos) != 1 {
		t.Fatalf("应隐藏自有仓库，剩 1 个，实际 %d", len(repos))
	}
	first := repos[0].(map[string]any)
	if first["fullName"] != "someone/else" {
		t.Fatalf("剩余仓库错误: %v", first["fullName"])
	}
	if body["hiddenCount"].(float64) != 1 {
		t.Fatalf("hiddenCount 应为 1，实际 %v", body["hiddenCount"])
	}
	counts := body["counts"].(map[string]any)
	if counts["all"].(float64) != 1 {
		t.Fatalf("counts.all 应基于过滤后集合，实际 %v", counts["all"])
	}
}

func TestStateRequiresTokenForReadme(t *testing.T) {
	_, ts, _ := newTestServer(t)
	resp, err := ts.Client().Get(ts.URL + "/api/readme?repo=o/r")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("README 接口也需要鉴权，实际 %d", resp.StatusCode)
	}
}

func TestAIModelsRequiresToken(t *testing.T) {
	_, ts, _ := newTestServer(t)
	resp, err := ts.Client().Get(ts.URL + "/api/ai/models")
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusUnauthorized {
		t.Fatalf("模型列表接口需要鉴权，实际 %d", resp.StatusCode)
	}
}

func TestAIModelsRejectsUnsupportedMethod(t *testing.T) {
	_, ts, session := newTestServer(t)
	req, _ := http.NewRequest(http.MethodPut, ts.URL+"/api/ai/models", nil)
	req.Header.Set("X-App-Token", session)
	resp, err := ts.Client().Do(req)
	if err != nil {
		t.Fatal(err)
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusMethodNotAllowed {
		t.Fatalf("PUT 应被拒绝，实际 %d", resp.StatusCode)
	}
}

func TestSafeDownloadURL(t *testing.T) {
	allowed := []string{
		"https://github.com/o/r/releases/download/v1/a.zip",
		"https://objects.githubusercontent.com/abc",
		"https://api.github.com/repos/o/r/releases/assets/1",
	}
	for _, u := range allowed {
		if _, ok := safeDownloadURL(u); !ok {
			t.Fatalf("应允许: %s", u)
		}
	}
	denied := []string{
		"http://github.com/o/r/a.zip",
		"https://evil.com/a.zip",
		"https://github.com.evil.com/a.zip",
		"file:///C:/x.zip",
		"javascript:alert(1)",
	}
	for _, u := range denied {
		if _, ok := safeDownloadURL(u); ok {
			t.Fatalf("应拒绝: %s", u)
		}
	}
}
