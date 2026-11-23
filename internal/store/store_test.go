package store

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"
)

func newStore(t *testing.T, repos []*RepoState) *Store {
	t.Helper()
	path := filepath.Join(t.TempDir(), "state.json")
	data := fileData{
		Version:   fileVersion,
		UpdatedAt: time.Now().UTC().Format(time.RFC3339),
		Repos:     repos,
	}
	raw, err := json.Marshal(&data)
	if err != nil {
		t.Fatalf("序列化失败: %v", err)
	}
	if err := os.WriteFile(path, raw, 0o600); err != nil {
		t.Fatalf("写测试状态文件失败: %v", err)
	}
	s, err := Open(path, nil)
	if err != nil {
		t.Fatalf("打开状态库失败: %v", err)
	}
	return s
}

func names(list []RepoState) []string {
	out := make([]string, len(list))
	for i, r := range list {
		out[i] = r.FullName
	}
	return out
}

func assertOrder(t *testing.T, got []string, want []string) {
	t.Helper()
	if len(got) != len(want) {
		t.Fatalf("数量不符: 期望 %v，实际 %v", want, got)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("第 %d 项应为 %s，实际 %s（完整顺序 %v）", i+1, want[i], got[i], got)
		}
	}
}

// 回归用例：Star 数绝不能盖过更新时间。
func TestSnapshotSortsByUpdateTimeNotStars(t *testing.T) {
	now := time.Now().UTC()
	s := newStore(t, []*RepoState{
		{FullName: "big/old", Stars: 900000, PublishedAt: now.Add(-90 * 24 * time.Hour).Format(time.RFC3339)},
		{FullName: "small/new", Stars: 10, PublishedAt: now.Add(-1 * time.Hour).Format(time.RFC3339)},
		{FullName: "mid/mid", Stars: 500, PublishedAt: now.Add(-72 * time.Hour).Format(time.RFC3339)},
	})

	assertOrder(t, names(s.Snapshot()), []string{"small/new", "mid/mid", "big/old"})
}

func TestSnapshotMixesReadmeAndReleaseTimes(t *testing.T) {
	now := time.Now().UTC()
	s := newStore(t, []*RepoState{
		{FullName: "a/release", Stars: 100, TrackMode: TrackRelease, PublishedAt: now.Add(-48 * time.Hour).Format(time.RFC3339)},
		{FullName: "b/readme", Stars: 1, TrackMode: TrackReadme, ReadmeAt: now.Add(-2 * time.Hour).Format(time.RFC3339), ReadmeSHA: "abc"},
		{FullName: "c/release", Stars: 50, TrackMode: TrackRelease, PublishedAt: now.Add(-5 * 24 * time.Hour).Format(time.RFC3339)},
	})

	assertOrder(t, names(s.Snapshot()), []string{"b/readme", "a/release", "c/release"})
}

func TestSnapshotPutsMissingTimeLast(t *testing.T) {
	now := time.Now().UTC()
	s := newStore(t, []*RepoState{
		{FullName: "no/time", Stars: 999999, TrackMode: TrackNone},
		{FullName: "has/time", Stars: 1, PublishedAt: now.Add(-24 * time.Hour).Format(time.RFC3339)},
		{FullName: "empty/readme", Stars: 5000, TrackMode: TrackReadme},
	})

	// 两个都没有时间，按 Star 降序兜底（999999 > 5000）
	assertOrder(t, names(s.Snapshot()), []string{"has/time", "no/time", "empty/readme"})
}

func TestSnapshotPutsUpdatesFirst(t *testing.T) {
	now := time.Now().UTC()
	s := newStore(t, []*RepoState{
		{FullName: "fresh/old", Stars: 1, PublishedAt: now.Add(-30 * 24 * time.Hour).Format(time.RFC3339), LatestTag: "v1.0.0", SeenTag: "v1.0.0"},
		{FullName: "updated/old", Stars: 1, PublishedAt: now.Add(-60 * 24 * time.Hour).Format(time.RFC3339), LatestTag: "v2.0.0", SeenTag: "v1.0.0"},
		{FullName: "fresh/new", Stars: 1, PublishedAt: now.Add(-1 * time.Hour).Format(time.RFC3339), LatestTag: "v1.0.0", SeenTag: "v1.0.0"},
	})

	assertOrder(t, names(s.Snapshot()), []string{"updated/old", "fresh/new", "fresh/old"})
}

func TestSnapshotTieBreaksByStarsThenName(t *testing.T) {
	same := time.Now().UTC().Add(-time.Hour).Format(time.RFC3339)
	s := newStore(t, []*RepoState{
		{FullName: "b/two", Stars: 5, PublishedAt: same},
		{FullName: "a/one", Stars: 5, PublishedAt: same},
		{FullName: "c/three", Stars: 50, PublishedAt: same},
	})

	assertOrder(t, names(s.Snapshot()), []string{"c/three", "a/one", "b/two"})
}

// 旧状态文件没有 trackMode 字段时按 release 处理，且不报错。
func TestOpenBackwardCompatibleWithLegacyState(t *testing.T) {
	path := filepath.Join(t.TempDir(), "state.json")
	legacy := `{"version":1,"updatedAt":"2026-01-01T00:00:00Z","repos":[
		{"fullName":"old/repo","stars":3,"latestTag":"v1.2.0","seenTag":"v1.1.0","publishedAt":"2026-01-01T00:00:00Z"}
	]}`
	if err := os.WriteFile(path, []byte(legacy), 0o600); err != nil {
		t.Fatal(err)
	}
	s, err := Open(path, nil)
	if err != nil {
		t.Fatalf("读取旧状态失败: %v", err)
	}

	r, ok := s.Get("old/repo")
	if !ok {
		t.Fatal("应读到 old/repo")
	}
	if r.Mode() != TrackRelease {
		t.Fatalf("旧数据应视为 release，实际 %q", r.Mode())
	}
	if !r.HasUpdate {
		t.Fatal("v1.2.0 相对 v1.1.0 应判定为有更新")
	}
	if r.Releases == nil {
		t.Fatal("Releases 应为空切片而不是 nil")
	}
}

func TestMarkSeenPerMode(t *testing.T) {
	s := newStore(t, []*RepoState{
		{FullName: "a/release", LatestTag: "v2", SeenTag: "v1"},
		{FullName: "b/readme", TrackMode: TrackReadme, ReadmeSHA: "new", SeenReadmeSHA: "old"},
	})

	s.MarkSeen("a/release")
	r, _ := s.Get("a/release")
	if r.HasUpdate || r.SeenTag != "v2" {
		t.Fatalf("release 已读失败: seen=%q update=%v", r.SeenTag, r.HasUpdate)
	}

	s.MarkSeen("b/readme")
	r, _ = s.Get("b/readme")
	if r.HasUpdate || r.SeenReadmeSHA != "new" {
		t.Fatalf("readme 已读失败: seen=%q update=%v", r.SeenReadmeSHA, r.HasUpdate)
	}
}
