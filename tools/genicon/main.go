// genicon 生成应用图标（GitHub Octocat，多尺寸 ICO）。
//
// 用法：go run ./tools/genicon
// 产出：build/app.ico（供 rsrc 嵌入 exe）与 web/favicon.ico（供窗口/任务栏）。
//
// 图形用系统自带的 Edge 逐尺寸渲染（白底圆角 + Octocat 剪影），
// 再用标准库组装 ICO。这样能直接复用 GitHub 官方 path（含圆弧命令），
// 不必自己写 SVG 光栅化，也就不会把 logo 画歪。
package main

import (
	"bytes"
	"encoding/binary"
	"fmt"
	"image/png"
	"log"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
)

var iconSizes = []int{16, 20, 24, 32, 40, 48, 64, 96, 128, 256}

// Octicons mark-github（16x16 viewBox）的官方路径。
const octocatPath = "M8 0C3.58 0 0 3.58 0 8c0 3.54 2.29 6.53 5.47 7.59.4.07.55-.17.55-.38 " +
	"0-.19-.01-.82-.01-1.49-2.01.37-2.53-.49-2.69-.94-.09-.23-.48-.94-.82-1.13-.28-.15-.68-.52-.01-.53.63-.01 " +
	"1.08.58 1.23.82.72 1.21 1.87.87 2.33.66.07-.52.28-.87.51-1.07-1.78-.2-3.64-.89-3.64-3.95 " +
	"0-.87.31-1.59.82-2.15-.08-.2-.36-1.02.08-2.12 0 0 .67-.21 2.2.82.64-.18 1.32-.27 2-.27.68 0 " +
	"1.36.09 2 .27 1.53-1.04 2.2-.82 2.2-.82.44 1.1.16 1.92.08 2.12.51.56.82 1.27.82 2.15 0 " +
	"3.07-1.87 3.75-3.65 3.95.29.25.54.73.54 1.48 0 1.07-.01 1.93-.01 2.2 0 .21.15.46.55.38" +
	"A8.013 8.013 0 0 0 16 8c0-4.42-3.58-8-8-8z"

func main() {
	edge, err := findEdge()
	if err != nil {
		log.Fatalf("未找到 Edge，无法渲染图标：%v", err)
	}

	tmp, err := os.MkdirTemp("", "mgt-icon-")
	if err != nil {
		log.Fatalf("创建临时目录失败: %v", err)
	}
	defer func() { _ = os.RemoveAll(tmp) }()

	profile := filepath.Join(tmp, "profile")
	frames := make([][]byte, 0, len(iconSizes))
	for _, size := range iconSizes {
		html := filepath.Join(tmp, fmt.Sprintf("icon-%d.html", size))
		if err := os.WriteFile(html, []byte(iconHTML(size)), 0o644); err != nil {
			log.Fatalf("写入 %dpx 页面失败: %v", size, err)
		}
		out := filepath.Join(tmp, fmt.Sprintf("icon-%d.png", size))
		if err := shot(edge, profile, html, out, size); err != nil {
			log.Fatalf("渲染 %dpx 失败: %v", size, err)
		}
		raw, err := os.ReadFile(out)
		if err != nil {
			log.Fatalf("读取 %dpx 结果失败: %v", size, err)
		}
		if _, err := png.DecodeConfig(bytes.NewReader(raw)); err != nil {
			log.Fatalf("%dpx 输出不是有效 PNG: %v", size, err)
		}
		frames = append(frames, raw)
	}

	ico := buildICO(iconSizes, frames)
	for _, out := range []string{
		filepath.Join("build", "app.ico"),
		filepath.Join("web", "favicon.ico"),
	} {
		if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
			log.Fatal(err)
		}
		if err := os.WriteFile(out, ico, 0o644); err != nil {
			log.Fatal(err)
		}
		fmt.Printf("已生成 %s（%d 字节，%d 个尺寸）\n", out, len(ico), len(iconSizes))
	}
}

// iconHTML 生成指定尺寸的图标页面：白色圆角底 + 居中的 Octocat 剪影。
func iconHTML(size int) string {
	radius := int(float64(size)*0.22 + 0.5)
	if radius < 2 {
		radius = 2
	}
	glyph := int(float64(size) * 0.80)
	return fmt.Sprintf(`<!doctype html>
<html><head><meta charset="utf-8"><style>
html,body{margin:0;padding:0;background:transparent;overflow:hidden}
#tile{width:%dpx;height:%dpx;box-sizing:border-box;background:#ffffff;border-radius:%dpx;
display:flex;align-items:center;justify-content:center}
#tile svg{width:%dpx;height:%dpx;display:block;fill:#1f2328}
</style></head><body>
<div id="tile"><svg viewBox="0 0 16 16" xmlns="http://www.w3.org/2000/svg"><path d="%s"/></svg></div>
</body></html>`, size, size, radius, glyph, glyph, octocatPath)
}

func shot(edge, profile, html, out string, size int) error {
	args := []string{
		"--headless=new",
		"--disable-gpu",
		"--hide-scrollbars",
		"--default-background-color=00000000",
		"--force-device-scale-factor=1",
		fmt.Sprintf("--window-size=%d,%d", size, size),
		"--virtual-time-budget=1500",
		"--no-first-run",
		"--disable-extensions",
		"--disable-sync",
		"--user-data-dir=" + profile,
		"--screenshot=" + out,
		"file:///" + filepath.ToSlash(html),
	}
	cmd := exec.Command(edge, args...)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return fmt.Errorf("%v: %s", err, strings.TrimSpace(stderr.String()))
	}
	if _, err := os.Stat(out); err != nil {
		return fmt.Errorf("未生成截图: %v", err)
	}
	return nil
}

func findEdge() (string, error) {
	candidates := []string{
		filepath.Join(os.Getenv("ProgramFiles(x86)"), `Microsoft\Edge\Application\msedge.exe`),
		filepath.Join(os.Getenv("ProgramFiles"), `Microsoft\Edge\Application\msedge.exe`),
		filepath.Join(os.Getenv("LOCALAPPDATA"), `Microsoft\Edge\Application\msedge.exe`),
		filepath.Join(os.Getenv("ProgramFiles"), `Google\Chrome\Application\chrome.exe`),
		filepath.Join(os.Getenv("ProgramFiles(x86)"), `Google\Chrome\Application\chrome.exe`),
	}
	for _, p := range candidates {
		if p == "" {
			continue
		}
		if st, err := os.Stat(p); err == nil && !st.IsDir() {
			return p, nil
		}
	}
	if runtime.GOOS != "windows" {
		return "", fmt.Errorf("当前平台 %s 不支持（需要 Windows 上的 Edge/Chrome）", runtime.GOOS)
	}
	return "", fmt.Errorf("未在常见路径找到 msedge.exe")
}

// buildICO 组装 ICO 容器（每个尺寸存 PNG 帧，Vista 及以上支持）。
func buildICO(sizes []int, frames [][]byte) []byte {
	var buf bytes.Buffer
	_ = binary.Write(&buf, binary.LittleEndian, uint16(0)) // reserved
	_ = binary.Write(&buf, binary.LittleEndian, uint16(1)) // type: icon
	_ = binary.Write(&buf, binary.LittleEndian, uint16(len(sizes)))

	offset := 6 + 16*len(sizes)
	for i, size := range sizes {
		dim := byte(size)
		if size >= 256 {
			dim = 0
		}
		buf.WriteByte(dim) // width
		buf.WriteByte(dim) // height
		buf.WriteByte(0)   // palette
		buf.WriteByte(0)   // reserved
		_ = binary.Write(&buf, binary.LittleEndian, uint16(1))  // planes
		_ = binary.Write(&buf, binary.LittleEndian, uint16(32)) // bit depth
		_ = binary.Write(&buf, binary.LittleEndian, uint32(len(frames[i])))
		_ = binary.Write(&buf, binary.LittleEndian, uint32(offset))
		offset += len(frames[i])
	}
	for _, frame := range frames {
		buf.Write(frame)
	}
	return buf.Bytes()
}
