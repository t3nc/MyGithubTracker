package download

import (
	"bytes"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strconv"
	"sync"
	"testing"
	"time"
)

func TestDownloaderWritesFileAndDedupes(t *testing.T) {
	payload := bytes.Repeat([]byte("mgt"), 2000)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Length", strconv.Itoa(len(payload)))
		_, _ = w.Write(payload)
	}))
	defer srv.Close()

	dir := t.TempDir()
	var mu sync.Mutex
	d := New(&http.Client{Timeout: 10 * time.Second}, dir, "", func(task Task) {
		mu.Lock()
		mu.Unlock()
	}, nil)

	task, err := d.Start("acme/tool", srv.URL+"/tool.zip", "tool.zip")
	if err != nil {
		t.Fatalf("启动下载失败: %v", err)
	}

	if !waitStatus(d, task.ID, StatusDone, 8*time.Second) {
		t.Fatalf("下载未在预期时间内完成，状态: %s", currentStatus(d, task.ID))
	}

	path := filepath.Join(dir, "tool.zip")
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("下载文件不存在: %v", err)
	}
	if info.Size() != int64(len(payload)) {
		t.Fatalf("文件大小不符: 期望 %d，实际 %d", len(payload), info.Size())
	}
	if _, err := os.Stat(path + ".part"); err == nil {
		t.Fatal("临时文件应已被重命名，不应残留 .part")
	}

	// 同名文件再次下载时应自动去重
	task2, err := d.Start("acme/tool", srv.URL+"/tool.zip", "tool.zip")
	if err != nil {
		t.Fatalf("第二次启动下载失败: %v", err)
	}
	if !waitStatus(d, task2.ID, StatusDone, 8*time.Second) {
		t.Fatalf("第二次下载未完成，状态: %s", currentStatus(d, task2.ID))
	}
	if _, err := os.Stat(filepath.Join(dir, "tool (1).zip")); err != nil {
		t.Fatalf("同名文件应去重为 tool (1).zip: %v", err)
	}
}

func TestDownloaderMarksFailureOnHTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Error(w, "nope", http.StatusInternalServerError)
	}))
	defer srv.Close()

	d := New(&http.Client{Timeout: 5 * time.Second}, t.TempDir(), "", nil, nil)
	task, err := d.Start("acme/tool", srv.URL+"/x.zip", "x.zip")
	if err != nil {
		t.Fatalf("启动下载失败: %v", err)
	}
	if !waitStatus(d, task.ID, StatusFailed, 12*time.Second) {
		t.Fatalf("应标记失败，实际状态: %s", currentStatus(d, task.ID))
	}
}

func TestSanitizeFileName(t *testing.T) {
	cases := map[string]string{
		`a/b:c*d?.zip`:  "a_b_c_d_.zip",
		"  spaced.zip ": "spaced.zip",
	}
	for in, want := range cases {
		if got := SanitizeFileName(in); got != want {
			t.Fatalf("SanitizeFileName(%q) = %q，期望 %q", in, got, want)
		}
	}
}

func currentStatus(d *Downloader, id string) string {
	for _, task := range d.List() {
		if task.ID == id {
			return task.Status
		}
	}
	return "missing"
}

func waitStatus(d *Downloader, id, want string, timeout time.Duration) bool {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		if currentStatus(d, id) == want {
			return true
		}
		time.Sleep(20 * time.Millisecond)
	}
	return false
}
