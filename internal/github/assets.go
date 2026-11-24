package github

import (
	"runtime"
	"sort"
	"strings"
)

// 校验/签名/说明类文件不作为安装包。
var junkSuffixes = []string{
	".sha256", ".sha1", ".sha512", ".md5", ".asc", ".sig", ".sign",
	".txt", ".json", ".xml", ".yml", ".yaml", ".pem", ".sbom", ".intoto",
	".nupkg", ".jar", ".whl", ".ipa",
	".blockmap", ".zsync", ".pdb", ".map", ".torrent",
}

// ScoreAsset 对单个资产名打分；excluded 为 true 表示明确不是本平台可用文件。
// 平台识别统一委托给 ClassifyAsset，本函数只保留 Windows 视角以兼容旧调用方。
func ScoreAsset(name, goarch string) (score int, excluded bool) {
	platform, ok := ClassifyAsset(name)
	if !ok {
		return 0, true
	}
	switch platform {
	case PlatformWindowsX64, PlatformWindowsX86, PlatformWindowsARM64:
		if goarch == "" {
			goarch = runtime.GOARCH
		}
		score = platformScore(name, platform)
		want := "x64"
		switch goarch {
		case "arm64":
			want = "arm64"
		case "386":
			want = "x86"
		}
		got := strings.TrimPrefix(platform, "windows-")
		switch {
		case got == want:
			// 架构完全匹配，不扣分
		case got == "x86" && want == "x64":
			score -= 10
		default:
			score -= 15
		}
		return score, false
	case PlatformGeneric:
		// 无法识别平台的通用包：保留可用性，但分数远低于明确的 Windows 包
		return platformScore(name, PlatformGeneric), false
	default:
		return 0, true
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
	if bestScore >= 60 {
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
