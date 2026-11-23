package github

import "testing"

func TestMatchWindowsAssetPrefersWindows64(t *testing.T) {
	assets := []Asset{
		{Name: "tool-1.2.0-linux-amd64.tar.gz"},
		{Name: "tool-1.2.0-darwin-arm64.dmg"},
		{Name: "tool-1.2.0-windows-amd64.zip"},
		{Name: "tool-1.2.0-windows-amd64.zip.sha256"},
	}
	got, ok := MatchWindowsAsset(assets, "amd64")
	if !ok {
		t.Fatal("期望匹配到 Windows 资产，实际未匹配")
	}
	if got.Name != "tool-1.2.0-windows-amd64.zip" {
		t.Fatalf("匹配结果错误: %s", got.Name)
	}
}

func TestMatchWindowsAssetPrefersExeOverZip(t *testing.T) {
	assets := []Asset{
		{Name: "app-2.0-win-x64.zip"},
		{Name: "app-2.0-win-x64.exe"},
	}
	got, ok := MatchWindowsAsset(assets, "amd64")
	if !ok || got.Name != "app-2.0-win-x64.exe" {
		t.Fatalf("应优先选择 exe，实际: %v", got)
	}
}

func TestMatchWindowsAssetRejectsNonWindowsOnly(t *testing.T) {
	assets := []Asset{
		{Name: "tool-1.0-linux-amd64.tar.gz"},
		{Name: "tool-1.0-darwin-arm64.tar.gz"},
	}
	if got, ok := MatchWindowsAsset(assets, "amd64"); ok {
		t.Fatalf("不应匹配到资产，实际: %v", got)
	}
}

func TestMatchWindowsAssetSingleGenericAsset(t *testing.T) {
	assets := []Asset{{Name: "release-bundle.zip"}}
	got, ok := MatchWindowsAsset(assets, "amd64")
	if !ok || got.Name != "release-bundle.zip" {
		t.Fatalf("唯一可用资产应被选中，实际: %v ok=%v", got, ok)
	}
}

func TestMatchWindowsAssetSkipsChecksums(t *testing.T) {
	assets := []Asset{
		{Name: "app-1.0-windows-amd64.exe.sha256"},
		{Name: "app-1.0-windows-amd64.exe"},
	}
	got, _ := MatchWindowsAsset(assets, "amd64")
	if got == nil || got.Name != "app-1.0-windows-amd64.exe" {
		t.Fatalf("不应选中校验文件，实际: %v", got)
	}
}

func TestRankedAssetsPutsWindowsFirst(t *testing.T) {
	assets := []Asset{
		{Name: "a-linux-amd64.tar.gz"},
		{Name: "a-windows-amd64.zip"},
	}
	ranked := RankedAssets(assets, "amd64")
	if len(ranked) != 2 || ranked[0].Name != "a-windows-amd64.zip" {
		t.Fatalf("排序错误: %v", ranked)
	}
}
