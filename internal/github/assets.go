package github

import (
	"runtime"
	"sort"
	"strings"
)

// 命中这些片段的资产属于其他平台或校验文件，直接排除。
var excludeTokens = []string{
	"darwin", "macos", "osx", "os x", "apple", "linux", "android",
	"freebsd", "openbsd", "netbsd", "sunos", "illumos",
	"appimage", ".deb", ".rpm", ".dmg", ".apk", ".pkg", ".flatpak", ".snap", ".ipa",
	".nupkg", ".jar", ".whl",
}

// 校验/签名/说明类文件不作为安装包。
var junkSuffixes = []string{
	".sha256", ".sha1", ".sha512", ".md5", ".asc", ".sig", ".sign",
	".txt", ".json", ".xml", ".yml", ".yaml", ".pem", ".sbom", ".intoto",
}

// Windows 平台标识。
var windowsTokens = []string{
	"windows", "win64", "win32", "win-x64", "win-x86", "win-x32",
	"win_x64", "win_x86", "win_amd64", "win-", "-win", "_win", "win.",
}

const (
	scoreWindows   = 60
	scoreArchMatch = 30
	scoreExe       = 20
	scoreMsi       = 15
	scoreArchive   = 10
	scoreSetupHint = 5
)

// ScoreAsset 对单个资产名打分；excluded 为 true 表示明确不是本平台可用文件。
func ScoreAsset(name, goarch string) (score int, excluded bool) {
	n := strings.ToLower(strings.TrimSpace(name))
	if n == "" {
		return 0, true
	}
	for _, s := range junkSuffixes {
		if strings.HasSuffix(n, s) {
			return 0, true
		}
	}
	for _, t := range excludeTokens {
		if strings.Contains(n, t) {
			return 0, true
		}
	}

	if goarch == "" {
		goarch = runtime.GOARCH
	}

	for _, t := range windowsTokens {
		if strings.Contains(n, t) {
			score += scoreWindows
			break
		}
	}
	score += archScore(n, goarch)

	switch {
	case strings.HasSuffix(n, ".exe"):
		score += scoreExe
	case strings.HasSuffix(n, ".msi"):
		score += scoreMsi
	case strings.HasSuffix(n, ".zip"):
		score += scoreArchive
	case strings.HasSuffix(n, ".7z"):
		score += scoreArchive - 2
	case strings.HasSuffix(n, ".tar.gz"):
		score += scoreArchive - 4
	}
	for _, h := range []string{"setup", "installer", "install", "portable", "x64"} {
		if strings.Contains(n, h) {
			score += scoreSetupHint
			break
		}
	}
	return score, false
}

func archScore(n, goarch string) int {
	switch goarch {
	case "arm64":
		switch {
		case strings.Contains(n, "arm64"), strings.Contains(n, "aarch64"):
			return scoreArchMatch
		case strings.Contains(n, "x86_64"), strings.Contains(n, "amd64"), strings.Contains(n, "x64"):
			return -15
		}
		return 0
	default:
		switch {
		case strings.Contains(n, "x86_64"), strings.Contains(n, "amd64"),
			strings.Contains(n, "x64"), strings.Contains(n, "64bit"), strings.Contains(n, "64-bit"):
			return scoreArchMatch
		case strings.Contains(n, "arm64"), strings.Contains(n, "aarch64"):
			return -15
		case strings.Contains(n, "i686"), strings.Contains(n, "i386"),
			strings.Contains(n, "x86"), strings.Contains(n, "386"):
			return -10
		}
		return 0
	}
}

// MatchWindowsAsset 在资产列表中挑选最适合本机 Windows 的文件。
// 仅当最高分达到 Windows 平台判定阈值，或列表只有一个可用资产时返回 ok。
func MatchWindowsAsset(assets []Asset, goarch string) (*Asset, bool) {
	if goarch == "" {
		goarch = runtime.GOARCH
	}
	bestScore := -1000
	bestIdx := -1
	usable := 0
	for i := range assets {
		s, excluded := ScoreAsset(assets[i].Name, goarch)
		if excluded {
			continue
		}
		usable++
		if s > bestScore {
			bestScore = s
			bestIdx = i
		}
	}
	if bestIdx < 0 {
		return nil, false
	}
	if bestScore >= scoreWindows {
		return &assets[bestIdx], true
	}
	if usable == 1 {
		return &assets[bestIdx], true
	}
	return nil, false
}

// RankedAssets 返回按推荐程度排序的可下载资产（用于让用户手动挑选）。
func RankedAssets(assets []Asset, goarch string) []Asset {
	type scored struct {
		a     Asset
		score int
		ex    bool
	}
	list := make([]scored, 0, len(assets))
	for _, a := range assets {
		s, ex := ScoreAsset(a.Name, goarch)
		list = append(list, scored{a: a, score: s, ex: ex})
	}
	sort.SliceStable(list, func(i, j int) bool {
		if list[i].ex != list[j].ex {
			return !list[i].ex
		}
		if list[i].score != list[j].score {
			return list[i].score > list[j].score
		}
		return strings.ToLower(list[i].a.Name) < strings.ToLower(list[j].a.Name)
	})
	out := make([]Asset, 0, len(list))
	for _, it := range list {
		out = append(out, it.a)
	}
	return out
}
