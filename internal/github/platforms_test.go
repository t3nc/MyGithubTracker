package github

import "testing"

func TestClassifyAsset(t *testing.T) {
	cases := []struct {
		name string
		want string
		ok   bool
	}{
		{"tool-1.2.0-windows-amd64.zip", PlatformWindowsX64, true},
		{"tool-1.2.0-win-x64.exe", PlatformWindowsX64, true},
		{"tool-1.2.0-win32.exe", PlatformWindowsX86, true},
		{"tool-1.2.0-windows-arm64.exe", PlatformWindowsARM64, true},
		{"tool-1.2.0-win-arm.exe", PlatformWindowsARM64, true},
		{"tool-1.2.0-win64.msi", PlatformWindowsX64, true},

		{"tool-1.2.0-darwin-x86_64.dmg", PlatformMacOSX64, true},
		{"tool-1.2.0-darwin-arm64.dmg", PlatformMacOSARM64, true},
		{"tool-1.2.0-macos-universal.pkg", PlatformMacOSGeneric, true},
		{"tool-1.2.0-macos.dmg", PlatformMacOSGeneric, true},

		{"tool-1.2.0-linux-amd64.tar.gz", PlatformLinuxX64, true},
		{"tool-1.2.0-linux-arm64.tar.gz", PlatformLinuxARM64, true},
		{"tool-1.2.0-linux-armv7.deb", PlatformLinuxARMv7, true},
		{"tool-1.2.0-linux-arm.tar.gz", PlatformLinuxARMv7, true},
		{"tool-1.2.0-linux-i386.AppImage", PlatformLinuxX86, true},
		{"tool-1.2.0-linux.zip", PlatformLinuxGeneric, true},

		{"tool-1.2.0-android-arm64-v8a.apk", PlatformAndroidARM64, true},
		{"tool-1.2.0-android-armeabi-v7a.apk", PlatformAndroidARMv7, true},
		{"tool-1.2.0-android-x86.apk", PlatformAndroidX86, true},
		{"tool-1.2.0-android-x86_64.apk", PlatformAndroidX64, true},
		{"tool-1.2.0.apk", PlatformAndroidGeneric, true},

		{"release-bundle.zip", PlatformGeneric, true},
		{"tool-1.2.0-universal.zip", PlatformMacOSGeneric, true},
		{"tool-harmony.zip", PlatformGeneric, true},

		{"tool-1.2.0-windows-amd64.zip.sha256", "", false},
		{"checksums.txt", "", false},
		{"app.exe.sig", "", false},
		{"library-1.0.jar", "", false},
		{"tool-1.2.0-win-x64.exe.blockmap", "", false},
		{"", "", false},
	}

	for _, c := range cases {
		got, ok := ClassifyAsset(c.name)
		if ok != c.ok || got != c.want {
			t.Errorf("ClassifyAsset(%q) = (%q, %v)，期望 (%q, %v)", c.name, got, ok, c.want, c.ok)
		}
	}
}

func TestBestPerPlatformPicksInstallerAndOrders(t *testing.T) {
	assets := []Asset{
		{Name: "app-1.0-win-x64-portable.zip", Size: 2, DownloadURL: "u-win-zip"},
		{Name: "app-1.0-win-x64.exe", Size: 1, DownloadURL: "u-win-exe"},
		{Name: "app-1.0-macos-arm64.dmg", DownloadURL: "u-mac-arm"},
		{Name: "app-1.0-linux-x86_64.AppImage", DownloadURL: "u-linux-appimage"},
		{Name: "app-1.0-linux-x86_64.deb", DownloadURL: "u-linux-deb"},
		{Name: "app-1.0.apk", DownloadURL: "u-apk"},
		{Name: "notes.txt", DownloadURL: "u-notes"},
	}

	got := BestPerPlatform(assets)
	wantOrder := []string{
		PlatformWindowsX64,
		PlatformMacOSARM64,
		PlatformLinuxX64,
		PlatformAndroidGeneric,
	}
	if len(got) != len(wantOrder) {
		t.Fatalf("平台数量错误: %+v", got)
	}
	for i, want := range wantOrder {
		if got[i].Platform != want {
			t.Fatalf("第 %d 个平台应为 %s，实际 %s（%+v）", i, want, got[i].Platform, got)
		}
	}
	if got[0].Name != "app-1.0-win-x64.exe" {
		t.Fatalf("Windows 应优先 exe，实际 %s", got[0].Name)
	}
	if got[2].Name != "app-1.0-linux-x86_64.AppImage" {
		t.Fatalf("Linux 应优先 AppImage，实际 %s", got[2].Name)
	}
}

func TestBestPerPlatformKeepsOnlyBestPerPlatform(t *testing.T) {
	assets := []Asset{
		{Name: "app-1.0-macos-x64.dmg", DownloadURL: "a"},
		{Name: "app-1.0-macos-x64.zip", DownloadURL: "b"},
		{Name: "app-1.0-macos-universal.dmg", DownloadURL: "c"},
	}
	got := BestPerPlatform(assets)
	if len(got) != 2 {
		t.Fatalf("应只保留 macos-x64 与 macos-generic 两项，实际 %+v", got)
	}
}

func TestMatchPlatform(t *testing.T) {
	assets := []Asset{
		{Name: "app-1.0-win-x64.zip", DownloadURL: "win-zip"},
		{Name: "app-1.0-win-x64.exe", DownloadURL: "win-exe"},
		{Name: "app-1.0-win-arm64.exe", DownloadURL: "win-arm"},
	}

	got, ok := MatchPlatform(assets, PlatformWindowsX64)
	if !ok || got.DownloadURL != "win-exe" {
		t.Fatalf("Windows x64 应选 exe，实际 %+v ok=%v", got, ok)
	}
	got, ok = MatchPlatform(assets, PlatformWindowsARM64)
	if !ok || got.DownloadURL != "win-arm" {
		t.Fatalf("Windows ARM64 匹配错误，实际 %+v ok=%v", got, ok)
	}
	if _, ok := MatchPlatform(assets, PlatformLinuxX64); ok {
		t.Fatal("不存在的平台不应匹配")
	}
}

func TestDefaultPlatform(t *testing.T) {
	cases := []struct {
		goos   string
		goarch string
		want   string
	}{
		{"windows", "amd64", PlatformWindowsX64},
		{"windows", "arm64", PlatformWindowsARM64},
		{"windows", "386", PlatformWindowsX86},
		{"darwin", "amd64", PlatformMacOSX64},
		{"darwin", "arm64", PlatformMacOSARM64},
		{"linux", "amd64", PlatformLinuxX64},
		{"linux", "arm64", PlatformLinuxARM64},
		{"linux", "arm", PlatformLinuxARMv7},
		{"linux", "386", PlatformLinuxX86},
		{"android", "arm64", PlatformAndroidARM64},
		{"freebsd", "amd64", PlatformGeneric},
	}
	for _, c := range cases {
		if got := DefaultPlatform(c.goos, c.goarch); got != c.want {
			t.Errorf("DefaultPlatform(%s, %s) = %s，期望 %s", c.goos, c.goarch, got, c.want)
		}
	}
}

func TestPlatformIDsUniqueAndOrdered(t *testing.T) {
	ids := PlatformIDs()
	if len(ids) == 0 || ids[0] != PlatformWindowsX64 || ids[len(ids)-1] != PlatformGeneric {
		t.Fatalf("平台顺序不符合预期: %v", ids)
	}
	seen := make(map[string]bool, len(ids))
	for _, id := range ids {
		if seen[id] {
			t.Fatalf("平台 ID 重复: %s", id)
		}
		seen[id] = true
		if !ValidPlatform(id) {
			t.Fatalf("注册表里的平台应有效: %s", id)
		}
	}
	if ValidPlatform("plan9-x64") {
		t.Fatal("未知平台不应有效")
	}
}
