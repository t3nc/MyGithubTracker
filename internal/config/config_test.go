package config

import "testing"

func TestNormalizeTranslateEngine(t *testing.T) {
	cases := map[string]string{
		"":           EngineAI,
		"ai":         EngineAI,
		"browser":    EngineBrowser,
		"Browser":    EngineBrowser, // 大小写不同也应识别
		"  browser ": EngineBrowser,
		"edge":       EngineAI, // 非法值回退默认
	}
	for in, want := range cases {
		c := Default()
		c.TranslateEngine = in
		c.normalize()
		if c.TranslateEngine != want {
			t.Fatalf("translateEngine %q 归一化后应 %q，实际 %q", in, want, c.TranslateEngine)
		}
	}
}

func TestDefaultTranslateEngineIsAI(t *testing.T) {
	if got := Default().TranslateEngine; got != EngineAI {
		t.Fatalf("默认引擎应为 %q，实际 %q", EngineAI, got)
	}
}

func TestHasAIKey(t *testing.T) {
	c := Default()
	if c.HasAIKey() {
		t.Fatal("默认不应带 Key")
	}
	c.AIKey = "  sk-test  "
	if !c.HasAIKey() {
		t.Fatal("填了 Key 应判定为已配置")
	}
	if c.EffectiveAIKey() != "sk-test" {
		t.Fatalf("Key 应去除空白，实际 %q", c.EffectiveAIKey())
	}
}
