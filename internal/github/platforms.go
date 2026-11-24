package github

import (
	"runtime"
	"strings"
)

// 平台 ID 是前后端唯一契约：Go 负责识别与选优，前端只做 ID 到文案的映射。
const (
	PlatformWindowsX64   = "windows-x64"
	PlatformWindowsX86   = "windows-x86"
	PlatformWindowsARM64 = "windows-arm64"

	PlatformMacOSX64     = "macos-x64"
	PlatformMacOSARM64   = "macos-arm64"
	PlatformMacOSGeneric = "macos-generic"

	PlatformLinuxX64     = "linux-x64"
	PlatformLinuxX86     = "linux-x86"
	PlatformLinuxARM64   = "linux-arm64"
	PlatformLinuxARMv7   = "linux-armv7"
	PlatformLinuxGeneric = "linux-generic"

	PlatformAndroidARM64   = "android-arm64"
	PlatformAndroidARMv7   = "android-armv7"
	PlatformAndroidX86     = "android-x86"
	PlatformAndroidX64     = "android-x64"
	PlatformAndroidGeneric = "android-generic"

	PlatformGeneric = "generic"
)

// PlatformInfo 描述一个可选平台。顺序即前端展示顺序。
type PlatformInfo struct {
	ID   string
	OS   string
	Arch string // x64 / x86 / arm64 / armv7 / ""（通用）
}

var platformRegistry = []PlatformInfo{
	{ID: PlatformWindowsX64, OS: "windows", Arch: "x64"},
	{ID: PlatformWindowsX86, OS: "windows", Arch: "x86"},
	{ID: PlatformWindowsARM64, OS: "windows", Arch: "arm64"},

	{ID: PlatformMacOSX64, OS: "macos", Arch: "x64"},
	{ID: PlatformMacOSARM64, OS: "macos", Arch: "arm64"},
	{ID: PlatformMacOSGeneric, OS: "macos", Arch: ""},

	{ID: PlatformLinuxX64, OS: "linux", Arch: "x64"},
	{ID: PlatformLinuxX86, OS: "linux", Arch: "x86"},
	{ID: PlatformLinuxARM64, OS: "linux", Arch: "arm64"},
	{ID: PlatformLinuxARMv7, OS: "linux", Arch: "armv7"},
	{ID: PlatformLinuxGeneric, OS: "linux", Arch: ""},

	{ID: PlatformAndroidARM64, OS: "android", Arch: "arm64"},
	{ID: PlatformAndroidARMv7, OS: "android", Arch: "armv7"},
	{ID: PlatformAndroidX86, OS: "android", Arch: "x86"},
	{ID: PlatformAndroidX64, OS: "android", Arch: "x64"},
	{ID: PlatformAndroidGeneric, OS: "android", Arch: ""},

	{ID: PlatformGeneric, OS: "generic", Arch: ""},
}

// PlatformAsset 是按平台选优后的下载项。
type PlatformAsset struct {
	Platform string
	Name     string
	Size     int64
	URL      string
	Score    int
}

// PlatformIDs 返回平台注册表顺序的 ID 列表。
func PlatformIDs() []string {
	out := make([]string, 0, len(platformRegistry))
	for _, p := range platformRegistry {
		out = append(out, p.ID)
	}
	return out
}

// ValidPlatform 判断 ID 是否属于注册表。
func ValidPlatform(id string) bool {
	for _, p := range platformRegistry {
		if p.ID == id {
			return true
		}
	}
	return false
}

// platformArch 返回平台 ID 对应的架构；通用平台返回空字符串。
func platformArch(id string) string {
	for _, p := range platformRegistry {
		if p.ID == id {
			return p.Arch
		}
	}
	return ""
}

// normalizeAssetName 把文件名归一化成带空格边界的小写串，便于做 token 匹配，
// 避免 "darwin" 命中 "win"、 "harmony" 命中 "arm" 之类的误判。
func normalizeAssetName(name string) string {
	n := strings.ToLower(strings.TrimSpace(name))
	replacements := []struct{ from, to string }{
		{"x86_64", " x64 "},
		{"x86-64", " x64 "},
		{"amd64", " x64 "},
		{"aarch64", " arm64 "},
		{"armv8l", " arm64 "},
		{"arm64e", " arm64 "},
		{"armv7l", " armv7 "},
		{"armhf", " armv7 "},
		{"armeabi-v7a", " armv7 "},
		{"armeabi", " armv7 "},
		{"arm64-v8a", " arm64 "},
		{"universal2", " universal "},
	}
	for _, r := range replacements {
		n = strings.ReplaceAll(n, r.from, r.to)
	}
	n = strings.NewReplacer(
		"_", " ", "-", " ", "+", " ", ".", " ",
		"(", " ", ")", " ", "[", " ", "]", " ",
		"{", " ", "}", " ", "/", " ", "\\", " ",
	).Replace(n)
	n = strings.Join(strings.Fields(n), " ")
	n = strings.ReplaceAll(n, "x86 64", "x64")
	if n == "" {
		return ""
	}
	return " " + n + " "
}

func hasAnyToken(padded string, tokens ...string) bool {
	for _, t := range tokens {
		if strings.Contains(padded, " "+t+" ") {
			return true
		}
	}
	return false
}

func isJunkAsset(name string) bool {
	n := strings.ToLower(strings.TrimSpace(name))
	if n == "" {
		return true
	}
	for _, s := range junkSuffixes {
		if strings.HasSuffix(n, s) {
			return true
		}
	}
	return false
}

// detectOS 识别操作系统。顺序很重要：macOS 优先，避免 darwin 被 win 命中。
func detectOS(raw, padded string) string {
	switch {
	case strings.HasSuffix(raw, ".dmg"), strings.HasSuffix(raw, ".pkg"), strings.HasSuffix(raw, ".mpkg"),
		hasAnyToken(padded, "darwin", "macos", "mac", "osx", "apple", "silicon", "universal"):
		return "macos"
	case strings.HasSuffix(raw, ".exe"), strings.HasSuffix(raw, ".msi"), strings.HasSuffix(raw, ".msix"),
		hasAnyToken(padded, "windows", "win", "win64", "win32"):
		return "windows"
	case strings.HasSuffix(raw, ".apk"), strings.HasSuffix(raw, ".aab"), hasAnyToken(padded, "android"):
		return "android"
	case strings.HasSuffix(raw, ".deb"), strings.HasSuffix(raw, ".rpm"),
		strings.HasSuffix(raw, ".appimage"), strings.HasSuffix(raw, ".flatpak"), strings.HasSuffix(raw, ".snap"),
		hasAnyToken(padded, "linux", "appimage", "gnu", "musl", "ubuntu", "debian", "fedora"):
		return "linux"
	default:
		return "generic"
	}
}

// detectArch 返回 x64 / x86 / arm64 / armv7 / universal / ""。
func detectArch(padded string) string {
	switch {
	case hasAnyToken(padded, "arm64", "armv8", "arm8", "apple silicon"):
		return "arm64"
	case hasAnyToken(padded, "armv7", "armv6", "arm32", "arm"):
		return "armv7"
	case hasAnyToken(padded, "universal", "fat"):
		return "universal"
	case hasAnyToken(padded, "x64", "intel", "64bit"):
		return "x64"
	case hasAnyToken(padded, "x86", "i386", "i686", "386", "win32", "32bit", "x32"):
		return "x86"
	default:
		return ""
	}
}

// ClassifyAsset 识别附件所属平台。ok=false 表示校验/说明类文件，不参与下载。
func ClassifyAsset(name string) (platform string, ok bool) {
	if isJunkAsset(name) {
		return "", false
	}
	raw := strings.ToLower(strings.TrimSpace(name))
	padded := normalizeAssetName(name)
	if padded == "" {
		return "", false
	}

	switch detectOS(raw, padded) {
	case "macos":
		switch detectArch(padded) {
		case "arm64":
			return PlatformMacOSARM64, true
		case "x64", "x86":
			return PlatformMacOSX64, true
		default:
			return PlatformMacOSGeneric, true
		}
	case "windows":
		switch detectArch(padded) {
		case "arm64", "armv7":
			// Windows on ARM 目标为 ARM64；32 位 ARM Windows 极罕见，归入同一入口。
			return PlatformWindowsARM64, true
		case "x86":
			return PlatformWindowsX86, true
		default:
			return PlatformWindowsX64, true
		}
	case "android":
		switch detectArch(padded) {
		case "arm64":
			return PlatformAndroidARM64, true
		case "armv7":
			return PlatformAndroidARMv7, true
		case "x86":
			return PlatformAndroidX86, true
		case "x64":
			return PlatformAndroidX64, true
		default:
			return PlatformAndroidGeneric, true
		}
	case "linux":
		switch detectArch(padded) {
		case "x64":
			return PlatformLinuxX64, true
		case "x86":
			return PlatformLinuxX86, true
		case "arm64":
			return PlatformLinuxARM64, true
		case "armv7":
			return PlatformLinuxARMv7, true
		default:
			return PlatformLinuxGeneric, true
		}
	default:
		return PlatformGeneric, true
	}
}

// platformScore 给「已归属某平台」的附件打分，用于同一平台内选优。
func platformScore(name, platform string) int {
	raw := strings.ToLower(strings.TrimSpace(name))
	padded := normalizeAssetName(name)

	score := 0
	if platform != PlatformGeneric {
		score += 60
		if platformArch(platform) != "" {
			score += 30
		}
	}

	switch {
	case strings.HasSuffix(raw, ".exe"):
		score += 20
	case strings.HasSuffix(raw, ".msi"), strings.HasSuffix(raw, ".msix"):
		score += 15
	case strings.HasSuffix(raw, ".dmg"):
		score += 20
	case strings.HasSuffix(raw, ".pkg"), strings.HasSuffix(raw, ".mpkg"):
		score += 15
	case strings.HasSuffix(raw, ".appimage"):
		score += 20
	case strings.HasSuffix(raw, ".deb"):
		score += 15
	case strings.HasSuffix(raw, ".rpm"):
		score += 12
	case strings.HasSuffix(raw, ".apk"):
		score += 20
	case strings.HasSuffix(raw, ".aab"):
		score += 15
	case strings.HasSuffix(raw, ".zip"):
		score += 10
	case strings.HasSuffix(raw, ".7z"):
		score += 8
	case strings.HasSuffix(raw, ".tar.gz"):
		score += 8
	}

	if hasAnyToken(padded, "setup", "installer", "install", "portable") {
		score += 5
	}
	return score
}

// BestPerPlatform 为每个平台挑选一个最优附件，按注册表顺序返回。
func BestPerPlatform(assets []Asset) []PlatformAsset {
	best := make(map[string]PlatformAsset, len(platformRegistry))
	for _, a := range assets {
		pid, ok := ClassifyAsset(a.Name)
		if !ok {
			continue
		}
		cand := PlatformAsset{
			Platform: pid,
			Name:     a.Name,
			Size:     a.Size,
			URL:      a.DownloadURL,
			Score:    platformScore(a.Name, pid),
		}
		old, exists := best[pid]
		if !exists || cand.Score > old.Score ||
			(cand.Score == old.Score && strings.ToLower(cand.Name) < strings.ToLower(old.Name)) {
			best[pid] = cand
		}
	}

	out := make([]PlatformAsset, 0, len(best))
	for _, p := range platformRegistry {
		if v, ok := best[p.ID]; ok {
			out = append(out, v)
		}
	}
	return out
}

// MatchPlatform 在附件中挑选指定平台的最优文件。
func MatchPlatform(assets []Asset, platform string) (*Asset, bool) {
	bestScore := -1
	bestIdx := -1
	for i := range assets {
		pid, ok := ClassifyAsset(assets[i].Name)
		if !ok || pid != platform {
			continue
		}
		s := platformScore(assets[i].Name, pid)
		if s > bestScore || (s == bestScore && bestIdx >= 0 && strings.ToLower(assets[i].Name) < strings.ToLower(assets[bestIdx].Name)) {
			bestScore = s
			bestIdx = i
		}
	}
	if bestIdx < 0 {
		return nil, false
	}
	return &assets[bestIdx], true
}

// DefaultPlatform 把本机 GOOS/GOARCH 映射为默认平台 ID。
func DefaultPlatform(goos, goarch string) string {
	if goarch == "" {
		goarch = runtime.GOARCH
	}
	if goos == "" {
		goos = runtime.GOOS
	}
	arch := "x64"
	switch goarch {
	case "arm64":
		arch = "arm64"
	case "386":
		arch = "x86"
	case "arm":
		arch = "armv7"
	}
	switch goos {
	case "windows":
		if arch == "armv7" {
			arch = "x86"
		}
		return "windows-" + arch
	case "darwin":
		if arch == "arm64" {
			return PlatformMacOSARM64
		}
		return PlatformMacOSX64
	case "linux":
		return "linux-" + arch
	case "android":
		return "android-" + arch
	default:
		return PlatformGeneric
	}
}
