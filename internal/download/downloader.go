// Package download 实现带进度、重试与取消的流式文件下载队列。
package download

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
)

// 任务状态。
const (
	StatusQueued      = "queued"
	StatusDownloading = "downloading"
	StatusDone        = "done"
	StatusFailed      = "failed"
	StatusCanceled    = "canceled"
)

// Task 是一个下载任务。
type Task struct {
	ID         string `json:"id"`
	Repo       string `json:"repo"`
	Name       string `json:"name"`
	Path       string `json:"path"`
	URL        string `json:"url"`
	Total      int64  `json:"total"`
	Downloaded int64  `json:"downloaded"`
	Speed      int64  `json:"speed"`
	Status     string `json:"status"`
	Error      string `json:"error"`
	Attempt    int    `json:"attempt"`
	CreatedAt  string `json:"createdAt"`
	FinishedAt string `json:"finishedAt"`
}

// Downloader 管理下载队列。
type Downloader struct {
	mu      sync.Mutex
	tasks   []*Task
	index   map[string]*Task
	cancels map[string]context.CancelFunc
	seq     int

	cfgMu  sync.RWMutex
	client *http.Client
	dir    string
	token  string

	emit func(Task)
	logf func(format string, args ...any)
}

// New 创建下载器。
func New(client *http.Client, dir, token string, emit func(Task), logf func(string, ...any)) *Downloader {
	return &Downloader{
		index:   map[string]*Task{},
		cancels: map[string]context.CancelFunc{},
		client:  client,
		dir:     dir,
		token:   token,
		emit:    emit,
		logf:    logf,
	}
}

// SetClient 更新 HTTP 客户端（代理配置变更后调用）。
func (d *Downloader) SetClient(c *http.Client, dir, token string) {
	d.cfgMu.Lock()
	d.client = c
	d.dir = dir
	d.token = token
	d.cfgMu.Unlock()
}

// Dir 返回当前下载目录。
func (d *Downloader) Dir() string {
	d.cfgMu.RLock()
	defer d.cfgMu.RUnlock()
	return d.dir
}

// List 返回任务副本（最新在前）。
func (d *Downloader) List() []Task {
	d.mu.Lock()
	defer d.mu.Unlock()
	out := make([]Task, 0, len(d.tasks))
	for _, t := range d.tasks {
		out = append(out, *t)
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].CreatedAt > out[j].CreatedAt })
	return out
}

// Active 返回未完成任务数。
func (d *Downloader) Active() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	n := 0
	for _, t := range d.tasks {
		if t.Status == StatusQueued || t.Status == StatusDownloading {
			n++
		}
	}
	return n
}

func (d *Downloader) push(t *Task) {
	d.mu.Lock()
	d.tasks = append(d.tasks, t)
	d.index[t.ID] = t
	d.mu.Unlock()
	if d.emit != nil {
		d.emit(*t)
	}
}

func (d *Downloader) update(id string, fn func(*Task)) {
	d.mu.Lock()
	t, ok := d.index[id]
	if !ok {
		d.mu.Unlock()
		return
	}
	fn(t)
	cp := *t
	d.mu.Unlock()
	if d.emit != nil {
		d.emit(cp)
	}
}

// Start 启动一个下载任务。
func (d *Downloader) Start(repo, rawURL, name string) (*Task, error) {
	if rawURL == "" {
		return nil, errors.New("下载地址为空")
	}
	dir := d.Dir()
	if err := os.MkdirAll(dir, 0o755); err != nil {
		return nil, fmt.Errorf("创建下载目录失败: %w", err)
	}
	safe := SanitizeFileName(name)
	if safe == "" {
		safe = "download.bin"
	}
	path := UniquePath(dir, safe)

	d.seq++
	task := &Task{
		ID:        fmt.Sprintf("t%d-%d", time.Now().UnixNano(), d.seq),
		Repo:      repo,
		Name:      safe,
		Path:      path,
		URL:       rawURL,
		Status:    StatusQueued,
		CreatedAt: time.Now().UTC().Format(time.RFC3339),
	}

	ctx, cancel := context.WithCancel(context.Background())
	d.mu.Lock()
	d.cancels[task.ID] = cancel
	d.mu.Unlock()

	d.push(task)
	go d.run(ctx, task.ID, rawURL)
	return task, nil
}

// Cancel 取消指定任务。
func (d *Downloader) Cancel(id string) bool {
	d.mu.Lock()
	cancel, ok := d.cancels[id]
	d.mu.Unlock()
	if ok {
		cancel()
	}
	return ok
}

func (d *Downloader) run(ctx context.Context, id, rawURL string) {
	defer func() {
		d.mu.Lock()
		if c, ok := d.cancels[id]; ok {
			c()
			delete(d.cancels, id)
		}
		d.mu.Unlock()
	}()

	var lastErr error
	for attempt := 1; attempt <= 3; attempt++ {
		if ctx.Err() != nil {
			d.finish(id, StatusCanceled, "")
			return
		}
		d.update(id, func(t *Task) { t.Attempt = attempt; t.Status = StatusDownloading; t.Error = "" })
		lastErr = d.attempt(ctx, id, rawURL)
		if lastErr == nil {
			d.finish(id, StatusDone, "")
			if d.logf != nil {
				d.logf("下载完成: %s", id)
			}
			return
		}
		if ctx.Err() != nil {
			d.finish(id, StatusCanceled, "")
			return
		}
		if d.logf != nil {
			d.logf("下载失败（第 %d 次）: %v", attempt, lastErr)
		}
		if attempt < 3 {
			d.update(id, func(t *Task) { t.Error = lastErr.Error() })
			select {
			case <-ctx.Done():
				d.finish(id, StatusCanceled, "")
				return
			case <-time.After(time.Duration(1<<(attempt-1)) * time.Second):
			}
		}
	}
	d.finish(id, StatusFailed, errText(lastErr))
}

func (d *Downloader) finish(id, status, msg string) {
	d.update(id, func(t *Task) {
		t.Status = status
		if msg != "" {
			t.Error = msg
		}
		t.Speed = 0
		t.FinishedAt = time.Now().UTC().Format(time.RFC3339)
		if status == StatusDone {
			t.Downloaded = t.Total
		}
	})
}

func (d *Downloader) attempt(ctx context.Context, id, rawURL string) error {
	d.cfgMu.RLock()
	client := d.client
	token := d.token
	d.cfgMu.RUnlock()
	if client == nil {
		return errors.New("HTTP 客户端未初始化")
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, rawURL, nil)
	if err != nil {
		return err
	}
	req.Header.Set("User-Agent", "MyGithubTracker/1.2")
	req.Header.Set("Accept", "application/octet-stream")
	// 仅对 GitHub API 域名附带 Token，避免凭证被重定向带到第三方 CDN。
	if token != "" && strings.HasPrefix(rawURL, "https://api.github.com/") {
		req.Header.Set("Authorization", "Bearer "+token)
	}

	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("下载失败，HTTP %d", resp.StatusCode)
	}

	d.mu.Lock()
	t, ok := d.index[id]
	var target string
	var knownTotal int64
	if ok {
		target = t.Path + ".part"
		knownTotal = t.Total
	}
	d.mu.Unlock()
	if !ok {
		return errors.New("任务不存在")
	}

	total := resp.ContentLength
	if total <= 0 {
		total = knownTotal
	}
	d.update(id, func(t *Task) { t.Total = total })

	f, err := os.Create(target)
	if err != nil {
		return err
	}

	pw := &progressWriter{w: f, d: d, id: id, last: time.Now()}
	buf := make([]byte, 64*1024)
	if _, err := io.CopyBuffer(pw, resp.Body, buf); err != nil {
		_ = f.Close()
		_ = os.Remove(target)
		return err
	}
	if err := f.Close(); err != nil {
		_ = os.Remove(target)
		return err
	}

	d.mu.Lock()
	final := d.index[id].Path
	d.mu.Unlock()
	if err := os.Rename(target, final); err != nil {
		_ = os.Remove(target)
		return err
	}
	return nil
}

type progressWriter struct {
	w    io.Writer
	d    *Downloader
	id   string
	last time.Time
	init bool
}

func (p *progressWriter) Write(b []byte) (int, error) {
	n, werr := p.w.Write(b)
	if werr != nil {
		return n, werr
	}
	var snap Task
	now := time.Now()
	p.d.mu.Lock()
	t, ok := p.d.index[p.id]
	if !ok {
		p.d.mu.Unlock()
		return n, errors.New("任务已移除")
	}
	t.Downloaded += int64(n)
	if t.Total > 0 && t.Downloaded > t.Total {
		t.Downloaded = t.Total
	}
	snap = *t
	p.d.mu.Unlock()

	if !p.init || now.Sub(p.last) > 200*time.Millisecond || (snap.Total > 0 && snap.Downloaded >= snap.Total) {
		elapsed := time.Since(mustParse(snap.CreatedAt)).Seconds()
		if elapsed > 0.05 {
			snap.Speed = int64(float64(snap.Downloaded) / elapsed)
		}
		if p.d.emit != nil {
			p.d.emit(snap)
		}
		p.last = now
		p.init = true
	}
	return n, nil
}

func mustParse(s string) time.Time {
	t, err := time.Parse(time.RFC3339, s)
	if err != nil {
		return time.Now()
	}
	return t
}

func errText(err error) string {
	if err == nil {
		return ""
	}
	return err.Error()
}

// SanitizeFileName 去掉 Windows 文件名非法字符。
func SanitizeFileName(name string) string {
	name = strings.TrimSpace(name)
	replacer := strings.NewReplacer(
		"<", "_", ">", "_", ":", "_", "\"", "_", "/", "_", "\\", "_", "|", "_", "?", "_", "*", "_",
	)
	name = replacer.Replace(name)
	name = strings.Trim(name, " .")
	if len(name) > 180 {
		ext := filepath.Ext(name)
		base := strings.TrimSuffix(name, ext)
		if len(base) > 160 {
			base = base[:160]
		}
		name = base + ext
	}
	return name
}

// UniquePath 返回不冲突的保存路径，冲突时追加 (1)、(2)。
func UniquePath(dir, name string) string {
	ext := filepath.Ext(name)
	base := strings.TrimSuffix(name, ext)
	p := filepath.Join(dir, name)
	for i := 1; fileExists(p); i++ {
		p = filepath.Join(dir, fmt.Sprintf("%s (%d)%s", base, i, ext))
	}
	return p
}

func fileExists(p string) bool {
	if _, err := os.Stat(p); err == nil {
		return true
	}
	if _, err := os.Stat(p + ".part"); err == nil {
		return true
	}
	return false
}
