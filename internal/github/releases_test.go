package github

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"
)

func TestRecentReleasesParsesAndSendsConditionalRequest(t *testing.T) {
	var gotPath, gotQuery, gotINM string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotQuery = r.URL.RawQuery
		gotINM = r.Header.Get("If-None-Match")
		if gotINM == `W/"v1"` {
			w.WriteHeader(http.StatusNotModified)
			return
		}
		w.Header().Set("ETag", `W/"v1"`)
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode([]map[string]any{
			{
				"tag_name":     "v2.0.0",
				"name":         "v2.0.0",
				"html_url":     "https://github.com/o/r/releases/tag/v2.0.0",
				"body":         "## 更新",
				"published_at": "2026-11-01T00:00:00Z",
				"prerelease":   false,
				"draft":        false,
				"assets": []map[string]any{
					{"name": "r-windows-amd64.exe", "size": 10, "browser_download_url": "https://github.com/o/r/x.exe"},
				},
			},
			{"tag_name": "v3.0.0-rc.1", "prerelease": true, "draft": false},
			{"tag_name": "v4.0.0-draft", "draft": true},
		})
	}))
	defer srv.Close()

	c := New(srv.Client(), "token")
	c.BaseURL = srv.URL

	rels, notModified, etag, err := c.RecentReleases(context.Background(), "o/r", 9, "")
	if err != nil {
		t.Fatalf("请求失败: %v", err)
	}
	if notModified {
		t.Fatal("首次请求不应是 304")
	}
	if gotPath != "/repos/o/r/releases" {
		t.Fatalf("路径错误: %s", gotPath)
	}
	if gotQuery != "per_page=5" {
		t.Fatalf("per_page 应被限制为 5，实际 %q", gotQuery)
	}
	if etag != `W/"v1"` {
		t.Fatalf("ETag 解析错误: %q", etag)
	}
	if len(rels) != 3 {
		t.Fatalf("应返回 3 条原始记录（draft 过滤由上层负责），实际 %d", len(rels))
	}
	if rels[0].Body != "## 更新" {
		t.Fatalf("正文解析错误: %q", rels[0].Body)
	}
	if len(rels[0].Assets) != 1 || rels[0].Assets[0].Name != "r-windows-amd64.exe" {
		t.Fatalf("附件解析错误: %+v", rels[0].Assets)
	}
	if !rels[1].Prerelease {
		t.Fatal("预发布标记解析错误")
	}
	if !rels[2].Draft {
		t.Fatal("草稿标记解析错误")
	}

	// 第二次带 ETag：应命中 304 且保留原 ETag
	rels2, notModified, etag2, err := c.RecentReleases(context.Background(), "o/r", 3, `W/"v1"`)
	if err != nil {
		t.Fatalf("条件请求失败: %v", err)
	}
	if !notModified || rels2 != nil {
		t.Fatal("应命中 304 且返回空列表")
	}
	if etag2 != `W/"v1"` {
		t.Fatalf("304 后应沿用原 ETag，实际 %q", etag2)
	}
	if gotINM != `W/"v1"` {
		t.Fatalf("应携带 If-None-Match，实际 %q", gotINM)
	}
}

func TestRecentReleasesEmptyList(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte("[]"))
	}))
	defer srv.Close()

	c := New(srv.Client(), "token")
	c.BaseURL = srv.URL
	rels, notModified, _, err := c.RecentReleases(context.Background(), "o/r", 3, "")
	if err != nil {
		t.Fatalf("请求失败: %v", err)
	}
	if notModified || len(rels) != 0 {
		t.Fatalf("空列表应返回 0 条，实际 %d", len(rels))
	}
}

func TestReadmeCommitParsesAndHandlesMissing(t *testing.T) {
	mode := "ok"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/repos/o/r/commits" {
			t.Errorf("路径错误: %s", r.URL.Path)
		}
		if r.URL.Query().Get("path") != "README.md" {
			t.Errorf("应限制 path=README.md，实际 %q", r.URL.Query().Get("path"))
		}
		switch mode {
		case "ok":
			w.Header().Set("Content-Type", "application/json")
			_ = json.NewEncoder(w).Encode([]map[string]any{
				{
					"sha":      "abc123",
					"html_url": "https://github.com/o/r/commit/abc123",
					"commit": map[string]any{
						"message": "docs: 更新说明\n\n第二行",
						"author":  map[string]any{"name": "Bob", "date": "2026-11-10T09:00:00Z"},
					},
				},
			})
		case "empty":
			w.Header().Set("Content-Type", "application/json")
			_, _ = w.Write([]byte("[]"))
		case "conflict":
			w.WriteHeader(http.StatusConflict)
			_, _ = w.Write([]byte(`{"message":"Git Repository is empty."}`))
		}
	}))
	defer srv.Close()

	c := New(srv.Client(), "token")
	c.BaseURL = srv.URL

	commit, err := c.ReadmeCommit(context.Background(), "o/r")
	if err != nil {
		t.Fatalf("解析 README 提交失败: %v", err)
	}
	if commit.SHA != "abc123" || commit.Author != "Bob" {
		t.Fatalf("提交信息解析错误: %+v", commit)
	}
	if commit.Message != "docs: 更新说明\n\n第二行" {
		t.Fatalf("提交信息应保留多行内容，实际 %q", commit.Message)
	}
	want, _ := time.Parse(time.RFC3339, "2026-11-10T09:00:00Z")
	if !commit.Date.Equal(want) {
		t.Fatalf("日期解析错误: %v", commit.Date)
	}
	if commit.HTMLURL != "https://github.com/o/r/commit/abc123" {
		t.Fatalf("链接解析错误: %q", commit.HTMLURL)
	}

	mode = "empty"
	if _, err := c.ReadmeCommit(context.Background(), "o/r"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("空提交列表应返回 ErrNotFound，实际 %v", err)
	}

	mode = "conflict"
	if _, err := c.ReadmeCommit(context.Background(), "o/r"); !errors.Is(err, ErrNotFound) {
		t.Fatalf("空仓库(409)应返回 ErrNotFound，实际 %v", err)
	}
}
