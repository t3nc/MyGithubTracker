package server

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"path"
	"runtime"
	"strings"
	"time"

	"mygithubtracker/internal/config"
	"mygithubtracker/internal/github"
	"mygithubtracker/internal/manager"
	"mygithubtracker/internal/store"
	"mygithubtracker/internal/translate"
	"mygithubtracker/internal/window"
)

const keepToken = "__KEEP__"

func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.Header().Set("Cache-Control", "no-store")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

func readJSON(r *http.Request, v any) error {
	if r.Body == nil {
		return io.EOF
	}
	dec := json.NewDecoder(io.LimitReader(r.Body, 1<<20))
	if err := dec.Decode(v); err != nil {
		if errors.Is(err, io.EOF) {
			return nil
		}
		return err
	}
	return nil
}

func onlyPOST(w http.ResponseWriter, r *http.Request) bool {
	if r.Method != http.MethodPost {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"ok": false, "error": "仅支持 POST"})
		return false
	}
	return true
}

func (s *Server) handlePing(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{"app": "MyGithubTracker", "version": s.opt.Version})
}

func (s *Server) handleWake(w http.ResponseWriter, r *http.Request) {
	select {
	case s.wake <- struct{}{}:
	default:
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleHeartbeat(w http.ResponseWriter, r *http.Request) {
	s.Touch()
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "refreshing": s.opt.Manager.Refreshing()})
}

type stateCounts struct {
	All       int `json:"all"`
	Update    int `json:"update"`
	NoRelease int `json:"noRelease"`
	Readme    int `json:"readme"`
	Error     int `json:"error"`
	Archived  int `json:"archived"`
}

// platformDownload 是按平台选优后的下载项，platform 为前后端约定的平台 ID。
type platformDownload struct {
	Platform string `json:"platform"`
	Name     string `json:"name"`
	Size     int64  `json:"size"`
	SizeText string `json:"sizeText"`
	URL      string `json:"url"`
}

// repoView 在仓库状态上追加平台下载项；内嵌结构保证原有 JSON 字段不变。
type repoView struct {
	store.RepoState
	Downloads []platformDownload `json:"downloads"`
}

// newRepoView 只为有附件的 Release 仓库计算平台下载项，README / 无可追踪仓库保持空数组。
func newRepoView(rs store.RepoState) repoView {
	v := repoView{RepoState: rs, Downloads: []platformDownload{}}
	if rs.Mode() != store.TrackRelease || len(rs.Assets) == 0 {
		return v
	}
	candidates := make([]github.Asset, 0, len(rs.Assets))
	for _, a := range rs.Assets {
		candidates = append(candidates, github.Asset{Name: a.Name, Size: a.Size, DownloadURL: a.URL, ContentType: a.ContentType})
	}
	for _, pa := range github.BestPerPlatform(candidates) {
		v.Downloads = append(v.Downloads, platformDownload{
			Platform: pa.Platform,
			Name:     pa.Name,
			Size:     pa.Size,
			SizeText: humanSize(pa.Size),
			URL:      pa.URL,
		})
	}
	return v
}

type stateResponse struct {
	Repos        []repoView       `json:"repos"`
	Refreshing   bool             `json:"refreshing"`
	Progress     manager.Progress `json:"progress"`
	LastRefresh  string           `json:"lastRefresh"`
	Rate         github.RateInfo  `json:"rate"`
	User         string           `json:"user"`
	HasToken     bool             `json:"hasToken"`
	Counts       stateCounts      `json:"counts"`
	Downloads    int              `json:"downloadsActive"`
	Version      string           `json:"version"`
	Arch         string           `json:"arch"`
	OS           string           `json:"os"`
	PlatformHint string           `json:"platformHint"`
	LogPath      string           `json:"logPath"`
	ConfigDir    string           `json:"configDir"`
	HideOwned    bool             `json:"hideOwned"`
	OwnedCount   int              `json:"ownedCount"`
	HiddenCount  int              `json:"hiddenCount"`
}

func (s *Server) handleState(w http.ResponseWriter, r *http.Request) {
	repos := s.opt.Store.Snapshot()
	cfg := s.Config()

	ownedTotal := 0
	for i := range repos {
		if repos[i].Owned {
			ownedTotal++
		}
	}
	hidden := 0
	if cfg.HideOwned && ownedTotal > 0 {
		kept := make([]store.RepoState, 0, len(repos)-ownedTotal)
		for i := range repos {
			if repos[i].Owned {
				hidden++
				continue
			}
			kept = append(kept, repos[i])
		}
		repos = kept
	}

	var counts stateCounts
	counts.All = len(repos)
	for i := range repos {
		switch {
		case repos[i].HasUpdate:
			counts.Update++
		}
		if repos[i].NoRelease {
			counts.NoRelease++
		}
		if repos[i].Mode() == store.TrackReadme {
			counts.Readme++
		}
		if repos[i].LastError != "" {
			counts.Error++
		}
		if repos[i].Archived {
			counts.Archived++
		}
	}

	views := make([]repoView, 0, len(repos))
	for _, rs := range repos {
		views = append(views, newRepoView(rs))
	}

	resp := stateResponse{
		Repos:        views,
		Refreshing:   s.opt.Manager.Refreshing(),
		Progress:     s.opt.Manager.Progress(),
		Rate:         s.opt.Manager.Rate(),
		User:         s.opt.Manager.User(),
		HasToken:     s.Config().Token != "",
		Counts:       counts,
		Downloads:    s.opt.Downloader.Active(),
		Version:      s.opt.Version,
		Arch:         runtime.GOARCH,
		OS:           runtime.GOOS,
		PlatformHint: github.DefaultPlatform(runtime.GOOS, runtime.GOARCH),
		ConfigDir:    config.Dir(),
		HideOwned:    cfg.HideOwned,
		OwnedCount:   ownedTotal,
		HiddenCount:  hidden,
	}
	if t := s.opt.Manager.LastRefresh(); !t.IsZero() {
		resp.LastRefresh = t.UTC().Format(time.RFC3339)
	}
	if s.opt.Log != nil {
		resp.LogPath = s.opt.Log.Path()
	}
	writeJSON(w, http.StatusOK, resp)
}

func (s *Server) handleRefresh(w http.ResponseWriter, r *http.Request) {
	if !onlyPOST(w, r) {
		return
	}
	var body struct {
		Force bool `json:"force"`
	}
	_ = readJSON(r, &body)

	if s.Config().Token == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "请先在设置中填写 GitHub Token"})
		return
	}
	if s.opt.Manager.Refreshing() {
		writeJSON(w, http.StatusConflict, map[string]any{"ok": false, "error": manager.ErrBusy.Error()})
		return
	}
	go func() {
		if err := s.opt.Manager.Refresh(body.Force); err != nil && s.opt.Log != nil {
			s.opt.Log.Errorf("刷新失败: %v", err)
		}
	}()
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleCancelRefresh(w http.ResponseWriter, r *http.Request) {
	if !onlyPOST(w, r) {
		return
	}
	s.opt.Manager.Cancel()
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleRead(w http.ResponseWriter, r *http.Request) {
	if !onlyPOST(w, r) {
		return
	}
	var body struct {
		Repo string `json:"repo"`
	}
	if err := readJSON(r, &body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	if !s.opt.Manager.MarkSeen(body.Repo) {
		writeJSON(w, http.StatusNotFound, map[string]any{"ok": false, "error": "仓库不存在"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

type assetView struct {
	Name        string `json:"name"`
	Size        int64  `json:"size"`
	SizeText    string `json:"sizeText"`
	URL         string `json:"url"`
	Platform    string `json:"platform"`
	Recommended bool   `json:"recommended"`
}

func (s *Server) handleDownload(w http.ResponseWriter, r *http.Request) {
	if !onlyPOST(w, r) {
		return
	}
	var body struct {
		Repo     string `json:"repo"`
		Asset    string `json:"asset"`
		Platform string `json:"platform"`
		URL      string `json:"url"`
		Name     string `json:"name"`
	}
	if err := readJSON(r, &body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": err.Error()})
		return
	}

	// 详情弹窗里旧版本的附件不在本地缓存中，允许显式传入 URL（需通过域名白名单）。
	if body.URL != "" {
		target, safe := safeDownloadURL(body.URL)
		if !safe {
			writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "只允许下载 GitHub 域名下的文件"})
			return
		}
		name := body.Name
		if name == "" {
			if u, perr := url.Parse(target); perr == nil {
				name = path.Base(u.Path)
			}
		}
		task, err := s.opt.Downloader.Start(body.Repo, target, name)
		if err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "task": task})
		return
	}

	rs, ok := s.opt.Store.Get(body.Repo)
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]any{"ok": false, "error": "仓库不存在"})
		return
	}
	if len(rs.Assets) == 0 {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "reason": "no-asset", "error": "该版本没有可下载的附件"})
		return
	}

	candidates := make([]github.Asset, 0, len(rs.Assets))
	for _, a := range rs.Assets {
		candidates = append(candidates, github.Asset{Name: a.Name, Size: a.Size, DownloadURL: a.URL, ContentType: a.ContentType})
	}

	var chosen *github.Asset
	if body.Asset != "" {
		for i := range candidates {
			if strings.EqualFold(candidates[i].Name, body.Asset) {
				chosen = &candidates[i]
				break
			}
		}
		if chosen == nil {
			writeJSON(w, http.StatusNotFound, map[string]any{"ok": false, "error": "附件不存在"})
			return
		}
	} else if body.Platform != "" {
		if !github.ValidPlatform(body.Platform) {
			writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "未知的平台标识"})
			return
		}
		best, found := github.MatchPlatform(candidates, body.Platform)
		if !found {
			writeJSON(w, http.StatusOK, map[string]any{
				"ok":     false,
				"reason": "no-platform",
				"error":  "该版本没有所选平台的可下载文件",
			})
			return
		}
		chosen = best
	} else {
		best, found := github.MatchWindowsAsset(candidates, runtime.GOARCH)
		if !found {
			ranked := github.RankedAssets(candidates, runtime.GOARCH)
			views := make([]assetView, 0, len(ranked))
			for i, a := range ranked {
				pid, _ := github.ClassifyAsset(a.Name)
				views = append(views, assetView{
					Name:        a.Name,
					Size:        a.Size,
					SizeText:    humanSize(a.Size),
					URL:         a.DownloadURL,
					Platform:    pid,
					Recommended: i == 0,
				})
			}
			writeJSON(w, http.StatusOK, map[string]any{
				"ok":         false,
				"needChoice": true,
				"reason":     "ambiguous",
				"assets":     views,
				"repo":       rs.FullName,
			})
			return
		}
		chosen = best
	}

	task, err := s.opt.Downloader.Start(rs.FullName, chosen.DownloadURL, chosen.Name)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "task": task})
}

func (s *Server) handleDownloads(w http.ResponseWriter, r *http.Request) {
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":     true,
		"tasks":  s.opt.Downloader.List(),
		"dir":    s.opt.Downloader.Dir(),
		"active": s.opt.Downloader.Active(),
	})
}

func (s *Server) handleCancel(w http.ResponseWriter, r *http.Request) {
	if !onlyPOST(w, r) {
		return
	}
	var body struct {
		ID string `json:"id"`
	}
	if err := readJSON(r, &body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	if !s.opt.Downloader.Cancel(body.ID) {
		writeJSON(w, http.StatusNotFound, map[string]any{"ok": false, "error": "任务不存在或已结束"})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleReveal(w http.ResponseWriter, r *http.Request) {
	if !onlyPOST(w, r) {
		return
	}
	var body struct {
		ID string `json:"id"`
	}
	if err := readJSON(r, &body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	for _, t := range s.opt.Downloader.List() {
		if t.ID == body.ID {
			if err := window.RevealFile(t.Path); err != nil {
				writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": err.Error()})
				return
			}
			writeJSON(w, http.StatusOK, map[string]any{"ok": true})
			return
		}
	}
	writeJSON(w, http.StatusNotFound, map[string]any{"ok": false, "error": "任务不存在"})
}

// safeExternalURL 仅允许打开 GitHub 域名，避免成为任意命令/协议入口。
func safeExternalURL(raw string) (string, bool) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil {
		return "", false
	}
	if u.Scheme != "https" && u.Scheme != "http" {
		return "", false
	}
	host := strings.ToLower(u.Hostname())
	// 工具发布主页（关于面板里的链接）
	if host == "yogo.wang" || host == "www.yogo.wang" {
		return u.String(), true
	}
	if host != "github.com" && !strings.HasSuffix(host, ".github.com") && host != "githubusercontent.com" && !strings.HasSuffix(host, ".githubusercontent.com") {
		return "", false
	}
	return u.String(), true
}

// safeDownloadURL 只允许下载 GitHub 相关域名下的文件，避免接口被当作任意下载器。
func safeDownloadURL(raw string) (string, bool) {
	u, err := url.Parse(strings.TrimSpace(raw))
	if err != nil || u.Host == "" || u.Scheme != "https" {
		return "", false
	}
	host := strings.ToLower(u.Hostname())
	switch {
	case host == "github.com",
		host == "api.github.com",
		host == "codeload.github.com",
		host == "objects.githubusercontent.com":
	case strings.HasSuffix(host, ".githubusercontent.com"):
	default:
		return "", false
	}
	return u.String(), true
}

func (s *Server) handleReleases(w http.ResponseWriter, r *http.Request) {
	repo := strings.TrimSpace(r.URL.Query().Get("repo"))
	if repo == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "缺少 repo 参数"})
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()

	mode, rels, commit, err := s.opt.Manager.Detail(ctx, repo)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]any{"ok": false, "error": friendlyTestErr(err)})
		return
	}

	// 标记「最新正式版」的位置
	latestIdx := 0
	for i, rel := range rels {
		if !rel.Prerelease {
			latestIdx = i
			break
		}
	}

	views := make([]map[string]any, 0, len(rels))
	for i, rel := range rels {
		ranked := github.RankedAssets(rel.Assets, runtime.GOARCH)
		assets := make([]assetView, 0, len(ranked))
		for j, a := range ranked {
			pid, _ := github.ClassifyAsset(a.Name)
			assets = append(assets, assetView{
				Name:        a.Name,
				Size:        a.Size,
				SizeText:    humanSize(a.Size),
				URL:         a.DownloadURL,
				Platform:    pid,
				Recommended: j == 0,
			})
		}
		downloads := make([]platformDownload, 0)
		for _, pa := range github.BestPerPlatform(rel.Assets) {
			downloads = append(downloads, platformDownload{
				Platform: pa.Platform,
				Name:     pa.Name,
				Size:     pa.Size,
				SizeText: humanSize(pa.Size),
				URL:      pa.URL,
			})
		}
		autoAsset := ""
		if best, ok := github.MatchWindowsAsset(rel.Assets, runtime.GOARCH); ok {
			autoAsset = best.Name
		}
		published := ""
		if !rel.PublishedAt.IsZero() {
			published = rel.PublishedAt.UTC().Format(time.RFC3339)
		} else if !rel.CreatedAt.IsZero() {
			published = rel.CreatedAt.UTC().Format(time.RFC3339)
		}
		views = append(views, map[string]any{
			"tag":         rel.TagName,
			"name":        rel.Name,
			"publishedAt": published,
			"htmlUrl":     rel.HTMLURL,
			"prerelease":  rel.Prerelease,
			"body":        rel.Body,
			"assets":      assets,
			"downloads":   downloads,
			"autoAsset":   autoAsset,
			"isLatest":    i == latestIdx,
		})
	}

	resp := map[string]any{
		"ok":       true,
		"repo":     repo,
		"mode":     mode,
		"releases": views,
	}
	if commit != nil {
		date := ""
		if !commit.Date.IsZero() {
			date = commit.Date.UTC().Format(time.RFC3339)
		}
		resp["readme"] = map[string]any{
			"sha":     commit.SHA,
			"message": commit.Message,
			"author":  commit.Author,
			"date":    date,
			"url":     commit.HTMLURL,
		}
	}
	writeJSON(w, http.StatusOK, resp)
}

func translateErrText(err error) string {
	var apiErr *translate.APIError
	if errors.As(err, &apiErr) {
		if apiErr.Status == 401 || apiErr.Status == 403 {
			return "翻译 Key 无效或无权限（" + fmt.Sprint(apiErr.Status) + "），请在设置中更换"
		}
		return apiErr.Error()
	}
	if errors.Is(err, translate.ErrNoKey) {
		return "请先在设置中填写翻译 API Key"
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return "翻译超时，请稍后重试"
	}
	return err.Error()
}

func (s *Server) handleTranslate(w http.ResponseWriter, r *http.Request) {
	if !onlyPOST(w, r) {
		return
	}
	var body struct {
		Text string `json:"text"`
	}
	if err := readJSON(r, &body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	text := strings.TrimSpace(body.Text)
	if text == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "待翻译内容为空"})
		return
	}

	// 超长说明会分段发送，整体放宽到 4 分钟。
	ctx, cancel := context.WithTimeout(r.Context(), 240*time.Second)
	defer cancel()

	out, cached, err := s.translator().Translate(ctx, text)
	if err != nil {
		if s.opt.Log != nil {
			s.opt.Log.Errorf("翻译失败: %v", err)
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": translateErrText(err)})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":     true,
		"text":   out,
		"cached": cached,
		"model":  s.translator().Model(),
	})
}

func (s *Server) handleTranslateTest(w http.ResponseWriter, r *http.Request) {
	if !onlyPOST(w, r) {
		return
	}
	// 允许用表单里尚未保存的值试跑
	var body struct {
		AIBaseURL string `json:"aiBaseUrl"`
		AIKey     string `json:"aiKey"`
		AIModel   string `json:"aiModel"`
	}
	_ = readJSON(r, &body)

	probe := s.Config().Clone()
	if body.AIKey != "" && body.AIKey != keepToken {
		probe.AIKey = strings.TrimSpace(body.AIKey)
	}
	if m := strings.TrimSpace(body.AIModel); m != "" {
		probe.AIModel = m
	}
	if v := strings.TrimSpace(body.AIBaseURL); v != "" && v != keepToken {
		probe.AIBaseURL = v
	}

	ctx, cancel := context.WithTimeout(r.Context(), 60*time.Second)
	defer cancel()

	client := translate.New(probe.HTTPClient(60*time.Second), probe.EffectiveAIBaseURL(), probe.EffectiveAIKey(), probe.AIModel)
	start := time.Now()
	out, err := client.Ping(ctx)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": translateErrText(err)})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":        true,
		"sample":    out,
		"model":     client.Model(),
		"latencyMs": time.Since(start).Milliseconds(),
	})
}

// handleAIModels 拉取上游可用模型列表（支持用表单里尚未保存的 Base URL / Key 试跑）。
func (s *Server) handleAIModels(w http.ResponseWriter, r *http.Request) {
	var body struct {
		AIBaseURL string `json:"aiBaseUrl"`
		AIKey     string `json:"aiKey"`
	}
	if r.Method == http.MethodPost {
		_ = readJSON(r, &body)
	} else if r.Method != http.MethodGet {
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"ok": false, "error": "仅支持 GET/POST"})
		return
	}

	probe := s.Config().Clone()
	if body.AIKey != "" && body.AIKey != keepToken {
		probe.AIKey = strings.TrimSpace(body.AIKey)
	}
	if v := strings.TrimSpace(body.AIBaseURL); v != "" && v != keepToken {
		probe.AIBaseURL = v
	}

	ctx, cancel := context.WithTimeout(r.Context(), 30*time.Second)
	defer cancel()

	client := translate.New(probe.HTTPClient(30*time.Second), probe.EffectiveAIBaseURL(), probe.EffectiveAIKey(), probe.AIModel)
	models, err := client.ListModels(ctx)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{
			"ok":      false,
			"error":   translateErrText(err),
			"baseUrl": translate.NormalizeBase(probe.EffectiveAIBaseURL()),
		})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":      true,
		"models":  models,
		"baseUrl": translate.NormalizeBase(probe.EffectiveAIBaseURL()),
	})
}

func (s *Server) handleReadme(w http.ResponseWriter, r *http.Request) {
	repo := strings.TrimSpace(r.URL.Query().Get("repo"))
	if repo == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "缺少 repo 参数"})
		return
	}
	rs, ok := s.opt.Store.Get(repo)
	if !ok {
		writeJSON(w, http.StatusNotFound, map[string]any{"ok": false, "error": "仓库不存在"})
		return
	}

	ctx, cancel := context.WithTimeout(r.Context(), 40*time.Second)
	defer cancel()

	text, truncated, err := s.opt.Manager.Readme(ctx, repo)
	if err != nil {
		writeJSON(w, http.StatusBadGateway, map[string]any{"ok": false, "error": friendlyTestErr(err)})
		return
	}
	if text == "" {
		writeJSON(w, http.StatusOK, map[string]any{
			"ok":      false,
			"reason":  "no-readme",
			"error":   "该仓库没有 README.md",
			"repo":    rs.FullName,
			"htmlUrl": rs.HTMLURL,
		})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{
		"ok":        true,
		"repo":      rs.FullName,
		"htmlUrl":   rs.HTMLURL,
		"text":      text,
		"truncated": truncated,
	})
}

func (s *Server) handlePickDir(w http.ResponseWriter, r *http.Request) {
	if !onlyPOST(w, r) {
		return
	}
	picked, canceled, err := window.PickFolder(s.Config().DownloadDir)
	if err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	if canceled {
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "canceled": true})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "canceled": false, "path": picked})
}

func (s *Server) handleOpen(w http.ResponseWriter, r *http.Request) {
	if !onlyPOST(w, r) {
		return
	}
	var body struct {
		URL string `json:"url"`
	}
	if err := readJSON(r, &body); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	target, ok := safeExternalURL(body.URL)
	if !ok {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "只允许打开 GitHub 链接"})
		return
	}
	if err := window.OpenURL(target); err != nil {
		writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleOpenDir(w http.ResponseWriter, r *http.Request) {
	if !onlyPOST(w, r) {
		return
	}
	if err := window.OpenDir(s.opt.Downloader.Dir()); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleRevealLog(w http.ResponseWriter, r *http.Request) {
	if !onlyPOST(w, r) {
		return
	}
	if s.opt.Log == nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "日志未启用"})
		return
	}
	if err := window.RevealFile(s.opt.Log.Path()); err != nil {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": err.Error()})
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
}

func (s *Server) handleLogs(w http.ResponseWriter, r *http.Request) {
	lines := []string{}
	if s.opt.Log != nil {
		lines = s.opt.Log.Lines()
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true, "lines": lines})
}

type configPayload struct {
	Token           string `json:"token"`
	ProxyMode       string `json:"proxyMode"`
	ProxyURL        string `json:"proxyUrl"`
	DownloadDir     string `json:"downloadDir"`
	Concurrency     int    `json:"concurrency"`
	RecentCount     int    `json:"recentCount"`
	TranslateEngine string `json:"translateEngine"`
	AIBaseURL       string `json:"aiBaseUrl"`
	AIKey           string `json:"aiKey"`
	AIModel         string `json:"aiModel"`
	// 用指针区分「未提交」与「显式设为 false」
	HideOwned *bool `json:"hideOwned"`
}

func (s *Server) configView() map[string]any {
	c := s.Config()
	return map[string]any{
		"tokenMasked":     config.MaskToken(c.Token),
		"hasToken":        c.Token != "",
		"proxyMode":       c.ProxyMode,
		"proxyUrl":        c.ProxyURL,
		"downloadDir":     c.DownloadDir,
		"concurrency":     c.Concurrency,
		"recentCount":     c.RecentCount,
		"hideOwned":       c.HideOwned,
		"translateEngine": c.TranslateEngine,
		"aiKeyMasked":     config.MaskToken(c.EffectiveAIKey()),
		"hasAIKey":        c.HasAIKey(),
		"aiModel":         c.AIModel,
		"aiBaseUrl":       translate.NormalizeBase(c.EffectiveAIBaseURL()),
		"aiEndpoint":      translate.NormalizeBase(c.EffectiveAIBaseURL()) + "/chat/completions",
		"user":            s.opt.Manager.User(),
		"configDir":       config.Dir(),
		"version":         s.opt.Version,
	}
}

func (s *Server) handleConfig(w http.ResponseWriter, r *http.Request) {
	switch r.Method {
	case http.MethodGet:
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "config": s.configView()})
	case http.MethodPost:
		var body configPayload
		if err := readJSON(r, &body); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": err.Error()})
			return
		}
		next := s.Config().Clone()
		if body.Token != "" && body.Token != keepToken {
			next.Token = strings.TrimSpace(body.Token)
		}
		switch config.ProxyMode(body.ProxyMode) {
		case config.ProxySystem, config.ProxyNone, config.ProxyCustom:
			next.ProxyMode = config.ProxyMode(body.ProxyMode)
		default:
			writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "未知的代理模式"})
			return
		}
		next.ProxyURL = strings.TrimSpace(body.ProxyURL)
		if strings.TrimSpace(body.DownloadDir) != "" {
			next.DownloadDir = strings.TrimSpace(body.DownloadDir)
		}
		if body.Concurrency > 0 {
			next.Concurrency = body.Concurrency
		}
		if body.RecentCount > 0 {
			next.RecentCount = body.RecentCount
		}
		// AI Key：__KEEP__ 保持不变，空串表示清空（清空后翻译不可用）
		switch body.AIKey {
		case keepToken:
		case "":
			next.AIKey = ""
		default:
			next.AIKey = strings.TrimSpace(body.AIKey)
		}
		if m := strings.TrimSpace(body.AIModel); m != "" {
			next.AIModel = m
		}
		// 翻译引擎只接受白名单取值，非法值保持原样
		if v := strings.TrimSpace(body.TranslateEngine); v == config.EngineAI || v == config.EngineBrowser {
			next.TranslateEngine = v
		}
		// AI Base URL：__KEEP__ 保持不变，空串表示回退默认
		switch body.AIBaseURL {
		case keepToken:
		case "":
			next.AIBaseURL = ""
		default:
			next.AIBaseURL = strings.TrimSpace(body.AIBaseURL)
		}
		if body.HideOwned != nil {
			next.HideOwned = *body.HideOwned
		}
		if next.ProxyMode != config.ProxyCustom {
			next.ProxyURL = ""
		}
		if err := next.Validate(); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": err.Error()})
			return
		}
		if err := next.Save(); err != nil {
			writeJSON(w, http.StatusInternalServerError, map[string]any{"ok": false, "error": err.Error()})
			return
		}
		s.setConfig(next)
		s.opt.Manager.SetConfig(next)
		s.opt.Downloader.SetClient(next.HTTPClient(0), next.DownloadDir, next.Token)
		s.opt.Hub.Broadcast(manager.Event{Type: "config-changed", Data: s.configView()})
		if s.opt.Log != nil {
			s.opt.Log.Printf("配置已更新：代理=%s 并发=%d", next.ProxyMode, next.Concurrency)
		}
		writeJSON(w, http.StatusOK, map[string]any{"ok": true, "config": s.configView()})
	default:
		writeJSON(w, http.StatusMethodNotAllowed, map[string]any{"ok": false, "error": "不支持的方法"})
	}
}

func (s *Server) handleTest(w http.ResponseWriter, r *http.Request) {
	if !onlyPOST(w, r) {
		return
	}
	var body configPayload
	_ = readJSON(r, &body)

	probe := s.Config().Clone()
	if body.Token != "" && body.Token != keepToken {
		probe.Token = strings.TrimSpace(body.Token)
	}
	if body.ProxyMode != "" {
		probe.ProxyMode = config.ProxyMode(body.ProxyMode)
	}
	probe.ProxyURL = strings.TrimSpace(body.ProxyURL)
	if probe.ProxyMode != config.ProxyCustom {
		probe.ProxyURL = ""
	}
	if probe.Token == "" {
		writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": "请先填写 GitHub Token"})
		return
	}
	if probe.ProxyMode == config.ProxyCustom {
		if _, err := config.ParseProxy(probe.ProxyURL); err != nil {
			writeJSON(w, http.StatusBadRequest, map[string]any{"ok": false, "error": err.Error()})
			return
		}
	}

	ctx, cancel := context.WithTimeout(r.Context(), 35*time.Second)
	defer cancel()

	client := github.New(probe.HTTPClient(35*time.Second), probe.Token)
	start := time.Now()
	user, err := client.CurrentUser(ctx)
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": friendlyTestErr(err)})
		return
	}
	rate, err := client.FetchRate(ctx)
	latency := time.Since(start).Milliseconds()
	if err != nil {
		writeJSON(w, http.StatusOK, map[string]any{"ok": false, "error": friendlyTestErr(err)})
		return
	}
	// 记住登录名：顶栏要展示，同时写入配置，下次启动无需再查
	s.opt.Manager.SetUser(user)
	if cur := s.Config(); cur.User != user {
		next := cur.Clone()
		next.User = user
		if err := next.Save(); err == nil {
			s.setConfig(next)
		}
	}

	writeJSON(w, http.StatusOK, map[string]any{
		"ok":        true,
		"user":      user,
		"rate":      rate,
		"latencyMs": latency,
	})
}

func friendlyTestErr(err error) string {
	if rle, ok := github.AsRateLimit(err); ok {
		return rle.Error()
	}
	var apiErr *github.APIError
	if errors.As(err, &apiErr) {
		if apiErr.Status == 401 {
			return "Token 无效或已过期（401）"
		}
		return apiErr.Error()
	}
	if errors.Is(err, github.ErrNotFound) {
		return "接口不存在（404）"
	}
	return fmt.Sprintf("连接失败：%v", err)
}

func (s *Server) handleQuit(w http.ResponseWriter, r *http.Request) {
	if !onlyPOST(w, r) {
		return
	}
	writeJSON(w, http.StatusOK, map[string]any{"ok": true})
	go func() {
		time.Sleep(120 * time.Millisecond)
		s.Shutdown()
	}()
}

func humanSize(n int64) string {
	const unit = 1024
	if n < unit {
		return fmt.Sprintf("%d B", n)
	}
	div, exp := int64(unit), 0
	for n/div >= unit && exp < 3 {
		div *= unit
		exp++
	}
	return fmt.Sprintf("%.1f %cB", float64(n)/float64(div), "KMGT"[exp])
}
