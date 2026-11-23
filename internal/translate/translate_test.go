package translate

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

func TestTranslateSendsOpenAIRequestAndParses(t *testing.T) {
	var gotAuth, gotPath string
	var body map[string]any
	var calls int32

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		atomic.AddInt32(&calls, 1)
		gotAuth = r.Header.Get("Authorization")
		gotPath = r.URL.Path
		_ = json.NewDecoder(r.Body).Decode(&body)
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices":[{"message":{"role":"assistant","content":"  你好，世界  "},"finish_reason":"stop"}]}`))
	}))
	defer srv.Close()

	c := New(srv.Client(), srv.URL+"/v1/chat/completions", "sk-test", "sensenova-6.8-flash-lite")
	out, cached, err := c.Translate(context.Background(), "Hello world")
	if err != nil {
		t.Fatalf("翻译失败: %v", err)
	}
	if out != "你好，世界" {
		t.Fatalf("应去除首尾空白，实际 %q", out)
	}
	if cached {
		t.Fatal("首次调用不应命中缓存")
	}
	if gotAuth != "Bearer sk-test" {
		t.Fatalf("鉴权头错误: %q", gotAuth)
	}
	if gotPath != "/v1/chat/completions" {
		t.Fatalf("请求路径错误: %q", gotPath)
	}
	if body["model"] != "sensenova-6.8-flash-lite" {
		t.Fatalf("模型名错误: %v", body["model"])
	}
	msgs, _ := body["messages"].([]any)
	if len(msgs) != 2 {
		t.Fatalf("应发送 system + user 两条消息，实际 %d", len(msgs))
	}

	// 第二次应命中缓存，且不再发请求
	before := atomic.LoadInt32(&calls)
	out2, cached2, err := c.Translate(context.Background(), "Hello world")
	if err != nil || out2 != out {
		t.Fatalf("缓存结果不一致: %v %q", err, out2)
	}
	if !cached2 {
		t.Fatal("第二次调用应命中缓存")
	}
	if atomic.LoadInt32(&calls) != before {
		t.Fatal("命中缓存不应再发请求")
	}
}

func TestNormalizeBase(t *testing.T) {
	cases := map[string]string{
		"":                            DefaultBaseURL,
		"   ":                         DefaultBaseURL,
		"https://api.example.com/v1":  "https://api.example.com/v1",
		"https://api.example.com/v1/": "https://api.example.com/v1",
		"  https://api.example.com/v1/chat/completions  ": "https://api.example.com/v1",
		"https://api.example.com/v1/models":               "https://api.example.com/v1",
		"https://api.example.com":                         "https://api.example.com",
		"http://127.0.0.1:11434/v1/":                      "http://127.0.0.1:11434/v1",
	}
	for in, want := range cases {
		if got := normalizeBase(in); got != want {
			t.Fatalf("normalizeBase(%q) = %q，期望 %q", in, got, want)
		}
	}
}

func TestListModelsParsesAndDedupes(t *testing.T) {
	var gotPath, gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath = r.URL.Path
		gotAuth = r.Header.Get("Authorization")
		_, _ = w.Write([]byte(`{"data":[
			{"id":"z-model"},
			{"id":"a-model"},
			{"id":"a-model"},
			{"id":"  "}
		]}`))
	}))
	defer srv.Close()

	c := New(srv.Client(), srv.URL+"/v1", "sk-abc", "m")
	models, err := c.ListModels(context.Background())
	if err != nil {
		t.Fatalf("拉取模型失败: %v", err)
	}
	if gotPath != "/v1/models" {
		t.Fatalf("应请求 {base}/models，实际 %s", gotPath)
	}
	if gotAuth != "Bearer sk-abc" {
		t.Fatalf("鉴权头错误: %q", gotAuth)
	}
	if len(models) != 2 || models[0] != "a-model" || models[1] != "z-model" {
		t.Fatalf("应去重排序，实际 %v", models)
	}
}

func TestListModelsRequiresKeyAndHandlesError(t *testing.T) {
	c := New(&http.Client{Timeout: time.Second}, "", "", "m")
	if _, err := c.ListModels(context.Background()); !errors.Is(err, ErrNoKey) {
		t.Fatalf("无 Key 应返回 ErrNoKey，实际 %v", err)
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":{"message":"bad key"}}`))
	}))
	defer srv.Close()

	c2 := New(srv.Client(), srv.URL, "bad", "m")
	_, err := c2.ListModels(context.Background())
	var apiErr *APIError
	if !errors.As(err, &apiErr) || apiErr.Status != http.StatusUnauthorized {
		t.Fatalf("应返回 401 APIError，实际 %v", err)
	}
}

func TestTranslateHandlesUpstreamError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		_, _ = w.Write([]byte(`{"error":{"message":"invalid api key"}}`))
	}))
	defer srv.Close()

	c := New(srv.Client(), srv.URL, "bad", "m")
	_, _, err := c.Translate(context.Background(), "hello")
	var apiErr *APIError
	if !errors.As(err, &apiErr) {
		t.Fatalf("应返回 APIError，实际 %v", err)
	}
	if apiErr.Status != http.StatusUnauthorized {
		t.Fatalf("状态码错误: %d", apiErr.Status)
	}
	if !strings.Contains(apiErr.Error(), "invalid api key") {
		t.Fatalf("错误信息丢失: %s", apiErr.Error())
	}
}

func TestTranslateRequiresKeyAndInput(t *testing.T) {
	c := New(&http.Client{Timeout: time.Second}, "", "", "m")
	if _, _, err := c.Translate(context.Background(), "hi"); !errors.Is(err, ErrNoKey) {
		t.Fatalf("无 Key 时应返回 ErrNoKey，实际 %v", err)
	}

	c2 := New(&http.Client{Timeout: time.Second}, "", "sk-x", "m")
	if _, _, err := c2.Translate(context.Background(), "   "); !errors.Is(err, ErrEmptyInput) {
		t.Fatalf("空内容应返回 ErrEmptyInput，实际 %v", err)
	}
}

func TestTranslateSplitsLongInputIntoChunks(t *testing.T) {
	var mu sync.Mutex
	var chunkSizes []int

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body struct {
			Messages []struct {
				Role    string `json:"role"`
				Content string `json:"content"`
			} `json:"messages"`
		}
		_ = json.NewDecoder(r.Body).Decode(&body)
		if len(body.Messages) == 2 {
			mu.Lock()
			chunkSizes = append(chunkSizes, len([]rune(body.Messages[1].Content)))
			mu.Unlock()
		}
		_, _ = w.Write([]byte(`{"choices":[{"message":{"content":"译文"}}]}`))
	}))
	defer srv.Close()

	c := New(srv.Client(), srv.URL, "sk-x", "m")
	long := strings.Repeat("长", MaxInputRunes+500)
	out, _, err := c.Translate(context.Background(), long)
	if err != nil {
		t.Fatalf("翻译失败: %v", err)
	}

	mu.Lock()
	defer mu.Unlock()
	if len(chunkSizes) < 2 {
		t.Fatalf("超长文本应分段请求，实际只发了 %d 次", len(chunkSizes))
	}
	total := 0
	for _, n := range chunkSizes {
		if n > chunkRunes {
			t.Fatalf("单段长度 %d 超过上限 %d", n, chunkRunes)
		}
		total += n
	}
	if total != MaxInputRunes {
		t.Fatalf("超长输入应被截断到 %d 字，实际累计 %d", MaxInputRunes, total)
	}
	if !strings.Contains(out, "译文") {
		t.Fatalf("译文拼接异常: %q", out)
	}
}

func TestSplitChunksKeepsParagraphBoundaries(t *testing.T) {
	para := strings.Repeat("a", 400)
	src := para + "\n\n" + para + "\n\n" + para
	chunks := splitChunks(src, 1000)
	if len(chunks) != 2 {
		t.Fatalf("应切成 2 段，实际 %d: %v", len(chunks), chunks)
	}
	if !strings.HasPrefix(chunks[0], "aaa") || strings.Count(chunks[0], "\n\n") != 1 {
		t.Fatalf("应在段落边界切分: %q", chunks[0])
	}
	for _, c := range chunks {
		if len([]rune(c)) > 1000 {
			t.Fatalf("段落长度超限: %d", len([]rune(c)))
		}
	}

	// 单段落超过上限时应硬切
	huge := strings.Repeat("b", 2500)
	parts := splitChunks(huge, 1000)
	if len(parts) != 3 {
		t.Fatalf("单段应硬切为 3 份，实际 %d", len(parts))
	}
}
