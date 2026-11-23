# MyGithubTracker

追踪你 GitHub 星标仓库的更新情况，只看 Release（没有 Release 的仓库自动改为追踪 `README.md` 的提交）。

纯 Go 实现，零第三方依赖、零 CGO、零 Node 构建链；界面用系统自带的 Edge 以无边框窗口承载本地页面，产物是单个 exe。

## 功能

- **星标同步**：读取当前账号全部星标仓库（分页，支持数百个）。
- **Release 追踪**：拉取每个仓库最近 N 个版本（N 可设 1–5，默认 3），以最新正式版作为「当前版本」。
- **README 追踪**：没有 Release 的仓库，自动改为追踪 `README.md` 的最近一次提交（sha / 提交信息 / 作者 / 时间），同样参与更新判定。
- **增量刷新**：本地保存 ETag，命中 `304 Not Modified` 时 GitHub 不计配额，日常刷新几乎零成本。
- **更新提示**：首次同步只建立基线，不会把历史版本全部标成「新版本」；之后版本变化（或 README 提交变化）才标红，可一键「已读」。
- **时间筛选**：全部 / 24 小时 / 7 天 / 30 天 / 90 天内有过更新。
- **版本详情**：点击仓库卡片弹出最近 N 个版本的更新说明（Markdown 渲染）与附件列表，可一键下载 Windows 版、展开全部附件、打开 Release 页面。
- **下载管理**：进度、速度、取消、失败重试 3 次（指数退避）、同名文件自动去重（`(1)`、`(2)`）。
- **代理**：跟随系统 / 不使用 / 自定义 `http(s)://` 或 `socks5://`，API 与下载共用。
- **路径选择**：设置页可用系统文件夹选择框挑选下载目录。

## 快速开始

1. 双击 `dist/MyGithubTracker.exe`（或自行编译，见下）。
2. 首次启动会要求填写 **Personal Access Token**：
   - 打开 <https://github.com/settings/tokens/new?scopes=read:user&description=MyGithubTracker>
   - 勾选 `read:user`（想读取私有仓库的星标再加 `repo`）
   - 生成后粘贴到窗口里，点「保存并开始」。
3. 程序会自动拉取星标仓库并检查最新版本。首次只建立基线，之后有更新才会标记。

## 使用说明

| 操作 | 说明 |
| --- | --- |
| 刷新 | 顶栏「刷新」按钮，增量刷新；按住 <kbd>Shift</kbd> 点击可忽略 ETag 强制全量 |
| 筛选 | 全部 / 有更新 / 仅 README / 失败，可与时间范围、搜索、排序叠加 |
| 排序 | 默认「最新发布」= 按最近一次更新时间倒序（Release 发布时间，或 README 模式的提交时间）；也可切换按 Star 数、名称排序 |
| 查看详情 | 点击仓库卡片，弹出最近 N 个版本的更新说明与附件 |
| 查看 README | 点击卡片里的仓库名，弹窗切到 README 页签直接预览；渲染与 GitHub 一致，Markdown + 安全 HTML（`<p align>`、`<details>`、`<table>`、`<img>` 等），相对图片/链接自动补全。「打开仓库主页 / 翻译成中文 / 重新加载」固定在页签行右侧，长文档滚动时不会滚走 |
| 直接下载 | 卡片行内下载按钮或详情里的「下载 Windows 版」，自动匹配 windows/x64/exe/msi/zip |
| 选择其他文件 | 详情里展开全部附件手动挑选 |
| 已读 | 标记后该版本不再算「新版本」；下载完成后也会自动标记 |
| 快捷键 | <kbd>/</kbd> 聚焦搜索、<kbd>Esc</kbd> 关闭弹窗、<kbd>Ctrl</kbd>+<kbd>,</kbd> 打开设置 |

## 过滤自己的仓库

设置 → 列表 → 勾选「隐藏我自己创建的仓库」，保存后列表不再显示 `owner` 等于你 GitHub 登录名的仓库，状态栏会提示「已隐藏 N 个自己的仓库」。

登录名来自「测试连接」写入的账号；若为空，刷新时会自动补查一次。登录名未知时不做任何过滤，避免误伤。

## 明暗主题

界面配色与 GitHub 网页版一致（Primer 色板），支持三种模式：

- **浅色**（默认）：白底 + `#d0d7de` 边框 + `#1f2328` 文字，主按钮为 GitHub 绿
- **深色**：`#0d1117` 底 + `#30363d` 边框 + `#e6edf3` 文字
- **跟随系统**：随 Windows 明暗设置实时切换

切换方式：

| 位置 | 操作 |
| --- | --- |
| 顶栏 | 月亮 / 太阳图标按钮，一键在浅色与深色之间切换 |
| 设置 → 外观 | 三个按钮选择浅色 / 深色 / 跟随系统 |
| 快捷键 | <kbd>Ctrl</kbd>+<kbd>Shift</kbd>+<kbd>L</kbd> |

选择保存在本机（窗口 profile 的 `localStorage`），重启后保持。地址栏加 `?theme=light` 或 `?theme=dark` 可临时覆盖且不写入偏好，便于截图核对。

## 内容翻译

英文内容可以一键翻译成中文：

- **版本说明**：详情弹窗里对有英文内容的版本会多一个「翻译成中文」，点一下就地换成中文，再点「显示原文」切回
- **README**：README 页签工具栏上也有同名按钮；浏览器引擎会翻译全文（按段串行、显示 `翻译中 3/120` 进度），AI 引擎为控制耗时与费用只翻前 8000 字并明确提示

译文按 Markdown 渲染，保留标题、列表与代码块结构。

### 翻译引擎

设置 → 内容翻译 → 翻译引擎，可选：

| 引擎 | 说明 |
| --- | --- |
| **AI 接口**（默认） | 调用 OpenAI 兼容服务，质量更稳定，需要 Key 与网络 |
| **浏览器内置** | 使用 Edge / Chrome 自带的本地翻译模型，不消耗 API 配额、不需要 Key；需要 Chromium 138+，首次使用要下载中英语言包 |

- 点「检测」可查看当前状态：`模型已就绪` / `首次使用需要下载语言包` / `暂不可用`
- 浏览器引擎首次使用时，Chromium 要求**在点击翻译的那一刻**触发下载（安全限制），所以第一次点「翻译成中文」会开始下载并显示进度，之后即可离线使用
- 浏览器引擎不可用时不会自动改配置，切回「AI 接口」即可继续用
- 依赖 Edge/Chrome 的翻译组件：程序启动参数不会禁用 `Translate` 特性、组件更新与后台网络，否则内置翻译会拿不到模型（若曾失败，重新点一次即可）

### AI 引擎配置

| 项 | 说明 |
| --- | --- |
| API 地址 | 任意 OpenAI 兼容服务的**根地址**，如 `https://token.sensenova.cn/v1`；会自动补 `/chat/completions`，直接粘贴完整 endpoint 也能识别 |
| API Key | **必须填写**（程序不内置任何 Key）。未配置时点翻译会提示并自动打开设置面板 |
| 模型 | 可手填，也可点「获取模型」从 `{base}/models` 读取候选（实测 SenseNova 返回 7 个模型） |

- **代理**：翻译请求与 GitHub 请求共用同一套代理设置
- **长文本**：AI 引擎超过 1600 字自动分段翻译再拼接；浏览器引擎按 1200 字分段串行翻译
- **缓存**：AI 引擎相同内容 2 小时内不重复请求；可先点「测试翻译」验证 Key 是否可用

> AI 引擎只发送当前版本的说明正文，不含仓库信息、Token 或其他本地数据。浏览器引擎的翻译完全在本机完成。

## 数据与配置

全部保存在本机，不联网上传任何信息：

```
%APPDATA%\MyGithubTracker\
├── config.json          # Token、代理、下载目录、并发数、最近版本数（权限 0600）
├── state.json           # 各仓库的追踪状态与缓存（不含 Release 正文）
├── app.log              # 运行日志（5MB 轮转）
└── window-profile\      # 界面窗口使用的独立 Edge 配置目录
```

Token 在界面上默认遮蔽显示，日志与接口返回中一律脱敏。

## 代理

设置 → 网络代理：

- **跟随系统**：读取 `HTTP_PROXY` / `HTTPS_PROXY` 环境变量
- **不使用**：直连
- **自定义**：如 `http://127.0.0.1:7890`、`socks5://127.0.0.1:1080`

改完点「测试连接」可验证账号、剩余配额与延迟。

## 编译

需要 Go 1.23+（推荐 1.27）。本机若未安装：`winget install GoLang.Go`，或从 <https://go.dev/dl/> 下载。

```powershell
.\build.ps1          # vet + test + build，产物 dist\MyGithubTracker.exe
```

等价于：

```powershell
go vet ./...
go test ./...
go build -trimpath -ldflags "-s -w -H windowsgui" -o dist/MyGithubTracker.exe .
```

`-H windowsgui` 让 exe 不带控制台窗口，因此诊断信息请查看 `app.log`。

### 调试与自建 API

设置环境变量 `MGT_API_BASE` 可覆盖 GitHub API 根地址（例如自建 GitHub Enterprise 或本地联调服务）：

```powershell
$env:MGT_API_BASE = "http://127.0.0.1:18080"
.\dist\MyGithubTracker.exe
```

## 应用图标

图标是 GitHub Octocat 剪影（白底圆角 + 深色猫）。由 `tools/genicon` 用系统 Edge 逐尺寸渲染后打包成 16–256px 的多尺寸 ICO，产出两份：

| 文件 | 用途 |
| --- | --- |
| `web/favicon.ico` | 内嵌进页面，Edge 应用窗口的窗口图标与任务栏图标 |
| `build/app.ico` + `rsrc_windows_amd64.syso` | 资源对象，让 exe 在资源管理器里显示图标 |

改图标后重新生成（需要 Edge/Chrome 用于渲染，`rsrc` 只在改图标时需要）：

```powershell
go run ./tools/genicon
go install github.com/akavel/rsrc@latest   # 首次需要
rsrc -ico build/app.ico -o rsrc_windows_amd64.syso -arch amd64
```

生成的 `.syso` 已在仓库中，日常 `go build` 不需要额外工具。

## 常见问题

**Q：为什么会话令牌无效 / 页面白屏？**
从程序图标重新启动即可，页面地址中的会话令牌每个进程不同。

**Q：刷新报 403 配额耗尽？**
GitHub 认证用户每小时 5000 次。程序做了 ETag 增量，通常不会消耗完；用完后可在界面看到重置时间。

**Q：提示「未找到 Edge，已回退到默认浏览器」？**
程序需要 Microsoft Edge 或 Google Chrome 提供 `--app` 窗口。找不到时会用系统默认浏览器打开同一页面，功能不受影响。

**Q：没有 Release 的仓库显示「无可追踪」？**
该仓库既没有 Release，也没有 `README.md` 的提交记录（空仓库或没有 README）。

**Q：状态文件损坏？**
程序会自动把损坏文件重命名为 `state.json.broken` 并重建，历史基线和已读记录会丢失，重新刷新即可。

## 安全说明

- 服务只监听 `127.0.0.1`，并校验 `Host` 与每次运行的随机会话令牌。
- 「打开链接」只允许 `github.com`；「下载」只允许 GitHub 域名。
- Release 说明按安全子集渲染（先转义再还原有限 Markdown），不执行任何脚本。
