package window

import (
	"context"
	"encoding/base64"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf16"
)

// folderPickerScript 是固定的常量脚本，用户路径通过环境变量传入，避免字符串拼接注入。
// -STA 必须保留，否则 WinForms 对话框会异常。
const folderPickerScript = `
$ErrorActionPreference = 'Stop'
Add-Type -AssemblyName System.Windows.Forms
$dialog = New-Object System.Windows.Forms.FolderBrowserDialog
$dialog.Description = '选择下载保存目录'
$dialog.ShowNewFolderButton = $true
$initial = $env:MGT_INITIAL_DIR
if ($initial -and (Test-Path -LiteralPath $initial)) { $dialog.SelectedPath = $initial }
$owner = New-Object System.Windows.Forms.Form
$owner.TopMost = $true
$owner.WindowState = 'Minimized'
$owner.ShowInTaskbar = $false
try {
    $result = $dialog.ShowDialog($owner)
} finally {
    $owner.Dispose()
}
if ($result -eq [System.Windows.Forms.DialogResult]::OK) {
    [Console]::Out.Write($dialog.SelectedPath)
}
`

// PickFolder 弹出系统文件夹选择框，返回所选目录；canceled 为 true 表示用户取消。
func PickFolder(initial string) (path string, canceled bool, err error) {
	shell := filepath.Join(os.Getenv("SystemRoot"), "System32", "WindowsPowerShell", "v1.0", "powershell.exe")
	if _, statErr := os.Stat(shell); statErr != nil {
		if p, lookErr := exec.LookPath("powershell.exe"); lookErr == nil {
			shell = p
		} else {
			return "", false, errors.New("未找到 PowerShell，无法打开文件夹选择框")
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Minute)
	defer cancel()

	cmd := exec.CommandContext(ctx, shell,
		"-NoProfile", "-STA", "-NonInteractive", "-WindowStyle", "Hidden",
		"-EncodedCommand", encodeUTF16LE(folderPickerScript),
	)
	cmd.Env = append(os.Environ(), "MGT_INITIAL_DIR="+initial)
	hide(cmd)

	out, err := cmd.Output()
	if err != nil {
		if ctx.Err() != nil {
			return "", false, errors.New("选择目录超时")
		}
		return "", false, fmt.Errorf("打开文件夹选择框失败: %w", err)
	}

	picked := strings.TrimSpace(string(out))
	if picked == "" {
		return "", true, nil
	}
	info, statErr := os.Stat(picked)
	if statErr != nil || !info.IsDir() {
		return "", false, fmt.Errorf("所选路径不是有效目录: %s", picked)
	}
	return picked, false, nil
}

// encodeUTF16LE 把脚本转为 PowerShell -EncodedCommand 需要的 Base64(UTF-16LE)。
func encodeUTF16LE(s string) string {
	units := utf16.Encode([]rune(s))
	buf := make([]byte, 0, len(units)*2)
	for _, u := range units {
		buf = append(buf, byte(u), byte(u>>8))
	}
	return base64.StdEncoding.EncodeToString(buf)
}
