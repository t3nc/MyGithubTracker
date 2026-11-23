(function () {
  'use strict';

  var TOKEN = new URLSearchParams(location.search).get('t') || '';
  var $ = function (id) { return document.getElementById(id); };

  var S = {
    state: null,
    config: null,
    downloads: [],
    filter: 'all',
    sort: 'updated',
    timeRange: 0,
    query: '',
    onlyUnread: false,
    refreshing: false,
    progress: { phase: 'idle', done: 0, total: 0, current: '' },
    settingsOpen: false,
    drawerOpen: false,
    detailOpen: false,
    detailRepo: '',
    detailSeq: 0,
    detailRels: [],
    detailTab: 'releases',
    detailError: '',
    releasesLoaded: false,
    releasesLoading: false,
    releasesMode: 'release',
    readmeCommit: null,
    readmeCache: Object.create(null),
    readmeLoading: '',
    readmeTrans: Object.create(null),
    readmeShow: Object.create(null),
    readmeTransPending: Object.create(null),
    readmeTruncated: Object.create(null),
    tr: Object.create(null),
    trShow: Object.create(null),
    trPending: Object.create(null),
    mdExpanded: Object.create(null),
    choice: null,
    autoRead: Object.create(null)
  };

  // 判断文本是否「以非中文为主」（用于决定是否显示翻译按钮）
  function isMostlyNonChinese(s) {
    if (!s) return false;
    var cjk = (s.match(/[\u4e00-\u9fff]/g) || []).length;
    var latin = (s.match(/[A-Za-z]/g) || []).length;
    if (latin < 24) return false;
    return cjk / (cjk + latin) < 0.18;
  }

  function relKey(rel) {
    return S.detailRepo + '|' + (rel.tag || '') + '|' + (rel.publishedAt || '');
  }

  /* 追踪模式：旧数据没有 trackMode 时按 release 处理 */
  function repoMode(r) {
    var m = r && r.trackMode;
    return m === 'readme' || m === 'none' ? m : 'release';
  }

  /* 最近一次更新事件时间：Release 发布时间 / README 提交时间 */
  function repoUpdateAt(r) {
    var mode = repoMode(r);
    if (mode === 'readme') return r.readmeAt || '';
    if (mode === 'none') return '';
    return r.publishedAt || '';
  }

  /* ---------------- 主题（light / dark / auto） ---------------- */

  var THEME_KEY = 'mgt-theme';
  var mediaDark = window.matchMedia ? window.matchMedia('(prefers-color-scheme: dark)') : null;

  function themeMode() {
    var m = document.documentElement.getAttribute('data-theme-mode');
    return m === 'dark' || m === 'auto' ? m : 'light';
  }

  function resolvedTheme(mode) {
    if (mode === 'light' || mode === 'dark') return mode;
    return mediaDark && mediaDark.matches ? 'dark' : 'light';
  }

  function syncThemeUI() {
    var mode = themeMode();
    var resolved = document.documentElement.getAttribute('data-theme') === 'dark' ? 'dark' : 'light';
    var bar = $('themeBar');
    if (bar) {
      Array.prototype.forEach.call(bar.children, function (b) {
        b.classList.toggle('active', b.getAttribute('data-theme-mode') === mode);
      });
    }
    var btn = $('btnTheme');
    if (btn) {
      var label = mode === 'auto' ? ('跟随系统（当前' + (resolved === 'dark' ? '深色' : '浅色') + '）') : (mode === 'dark' ? '深色' : '浅色');
      btn.title = '主题：' + label + ' · 点击切换 (Ctrl+Shift+L)';
    }
  }

  function applyThemeMode(mode, persist) {
    if (mode !== 'light' && mode !== 'dark' && mode !== 'auto') mode = 'light';
    document.documentElement.setAttribute('data-theme-mode', mode);
    document.documentElement.setAttribute('data-theme', resolvedTheme(mode));
    if (persist) {
      try { localStorage.setItem(THEME_KEY, mode); } catch (e) { /* 忽略隐私模式限制 */ }
    }
    syncThemeUI();
  }

  function toggleTheme() {
    var current = document.documentElement.getAttribute('data-theme') === 'dark' ? 'dark' : 'light';
    applyThemeMode(current === 'dark' ? 'light' : 'dark', true);
  }

  /* ---------------- 工具函数 ---------------- */

  function esc(v) {
    return String(v == null ? '' : v)
      .replace(/&/g, '&amp;').replace(/</g, '&lt;').replace(/>/g, '&gt;')
      .replace(/"/g, '&quot;').replace(/'/g, '&#39;');
  }

  function fmtStars(n) {
    n = n || 0;
    if (n >= 10000) return (n / 1000).toFixed(1).replace(/\.0$/, '') + 'k';
    if (n >= 1000) return (n / 1000).toFixed(1) + 'k';
    return String(n);
  }

  function parseDate(iso) {
    if (!iso) return null;
    var d = new Date(iso);
    return isNaN(d.getTime()) ? null : d;
  }

  function relTime(iso) {
    var d = parseDate(iso);
    if (!d) return '—';
    var diff = Date.now() - d.getTime();
    if (diff < 0) diff = 0;
    var min = Math.floor(diff / 60000);
    if (min < 1) return '刚刚';
    if (min < 60) return min + ' 分钟前';
    var h = Math.floor(min / 60);
    if (h < 24) return h + ' 小时前';
    var day = Math.floor(h / 24);
    if (day < 30) return day + ' 天前';
    var mon = Math.floor(day / 30);
    if (mon < 12) return mon + ' 个月前';
    return Math.floor(mon / 12) + ' 年前';
  }

  function fmtSize(n) {
    n = Number(n) || 0;
    if (n <= 0) return '未知大小';
    var u = ['B', 'KB', 'MB', 'GB'];
    var i = 0;
    var v = n;
    while (v >= 1024 && i < u.length - 1) { v /= 1024; i++; }
    return (i === 0 ? v.toFixed(0) : v.toFixed(1)) + ' ' + u[i];
  }

  function fmtSpeed(bps) {
    if (!bps || bps <= 0) return '';
    return fmtSize(bps) + '/s';
  }

  function pct(done, total) {
    if (!total) return 0;
    return Math.min(100, Math.max(0, Math.round((done / total) * 100)));
  }

  /* ---------------- API ---------------- */

  function api(path, body, allowSoftFail) {
    var opts = { method: body === undefined ? 'GET' : 'POST', headers: { 'X-App-Token': TOKEN } };
    if (body !== undefined) {
      opts.headers['Content-Type'] = 'application/json';
      opts.body = JSON.stringify(body || {});
    }
    return fetch(path, opts).then(function (res) {
      return res.json().catch(function () { return {}; }).then(function (data) {
        if (allowSoftFail) return data;
        if (!res.ok || data.ok === false) {
          throw new Error(data.error || ('请求失败 ' + res.status));
        }
        return data;
      });
    });
  }

  /* ---------------- Toast ---------------- */

  var toastTimer = 0;
  function toast(message, kind) {
    var wrap = $('toasts');
    var el = document.createElement('div');
    el.className = 'toast ' + (kind || '');
    el.innerHTML = '<span class="bar"></span><span class="toast-text">' + esc(message) + '</span>';
    wrap.appendChild(el);
    while (wrap.children.length > 3) wrap.removeChild(wrap.firstChild);
    toastTimer = setTimeout(function () { }, 0);
    setTimeout(function () {
      el.classList.add('out');
      setTimeout(function () { el.remove(); }, 220);
    }, 3600);
  }

  /* ---------------- 数据加载 ---------------- */

  function loadState() {
    return api('/api/state').then(function (data) {
      S.state = data;
      S.refreshing = data.refreshing;
      S.progress = data.progress || S.progress;
      if (!S.refreshing) S.progress.phase = 'idle';
      renderAll();
    }).catch(function (err) {
      setStatus('err', '无法读取状态：' + err.message);
    });
  }

  function loadConfig() {
    return api('/api/config').then(function (data) {
      S.config = data.config;
      renderSettings();
      renderHeader();
      if (!S.config.hasToken) $('onboarding').hidden = false;
      else $('onboarding').hidden = true;
    }).catch(function (err) {
      toast(err.message, 'err');
    });
  }

  // 首次启动还不知道登录名时，静默查一次（用于顶栏账号展示），失败不打扰用户
  function ensureLogin() {
    if (!S.config || !S.config.hasToken || S.config.user) return;
    api('/api/test', {}, true).then(function (data) {
      if (!data.ok || !data.user) return;
      S.config.user = data.user;
      renderHeader();
    }).catch(function () { });
  }

  function loadDownloads() {
    return api('/api/downloads').then(function (data) {
      S.downloads = data.tasks || [];
      renderDrawer(data.dir);
    }).catch(function () { });
  }

  var stateReloadTimer = 0;
  function scheduleStateReload() {
    if (stateReloadTimer) return;
    stateReloadTimer = setTimeout(function () {
      stateReloadTimer = 0;
      loadState();
    }, 260);
  }

  /* ---------------- 渲染：头部 ---------------- */

  function renderHeader() {
    var st = S.state;
    var repos = st ? st.repos : [];
    var updates = st ? st.counts.update : 0;
    var sub = st ? (repos.length + ' 个星标仓库' + (updates ? ' · ' + updates + ' 个有新版本' : '')) : '正在初始化…';
    var login = (st && st.user) || (S.config && S.config.user) || '';
    if (login) sub = '@' + login + ' · ' + sub;
    $('brandSub').textContent = sub;

    // 顶栏账号胶囊：登录名优先取 /api/state（刷新时会补查），回退配置里的账号
    var chip = $('userChip');
    var chipText = $('userChipText');
    if (chip && chipText) {
      chipText.textContent = login ? '@' + login : '未连接';
      chip.classList.toggle('is-on', !!login);
      chip.title = login
        ? ('已连接 GitHub 账号 @' + login + '，点击打开主页')
        : '未连接 GitHub 账号，点击打开设置';
    }

    var rate = st && st.rate ? st.rate : null;
    var quota = $('quota');
    if (rate && rate.limit) {
      var ratio = rate.remaining / rate.limit;
      $('quotaText').textContent = rate.remaining + '/' + rate.limit;
      quota.classList.toggle('warn', ratio < 0.35 && ratio >= 0.1);
      quota.classList.toggle('danger', ratio < 0.1);
      quota.title = '剩余 ' + rate.remaining + ' / ' + rate.limit + (rate.reset ? '，' + new Date(rate.reset).toLocaleTimeString('zh-CN') + ' 重置' : '');
    } else {
      $('quotaText').textContent = '—';
      quota.classList.remove('warn', 'danger');
    }

    var btn = $('btnRefresh');
    btn.disabled = S.refreshing;
    $('btnRefreshText').textContent = S.refreshing ? '刷新中' : '刷新';
    btn.querySelector('svg').classList.toggle('spinning', S.refreshing);

    var active = 0;
    S.downloads.forEach(function (t) {
      if (t.status === 'queued' || t.status === 'downloading') active++;
    });
    var badge = $('dlBadge');
    badge.hidden = active === 0;
    badge.textContent = String(active);

    if (st) {
      $('cntAll').textContent = st.counts.all;
      $('cntUpdate').textContent = st.counts.update;
      $('cntReadme').textContent = st.counts.readme || 0;
      $('cntError').textContent = st.counts.error;
      $('tokenHint').textContent = st.hasToken ? '已连接 GitHub' : '未配置 Token';
      $('lastRefreshText').textContent = st.lastRefresh ? '上次刷新 ' + relTime(st.lastRefresh) : '尚未刷新';
    }
  }

  /* ---------------- 渲染：状态栏 ---------------- */

  function setStatus(kind, text) {
    var dot = $('statusDot');
    dot.className = 'dot' + (kind === 'busy' ? ' busy' : kind === 'err' ? ' err' : '');
    $('statusText').textContent = text;
  }

  function renderStatus() {
    var st = S.state;
    var p = S.progress || {};
    if (S.refreshing) {
      if (p.phase === 'starred') {
        setStatus('busy', '正在拉取星标列表…（已获取 ' + (p.total || 0) + ' 个）');
      } else {
        setStatus('busy', '正在检查 ' + (p.done || 0) + '/' + (p.total || 0) + (p.current ? ' · ' + p.current : ''));
      }
    } else if (st) {
      var txt = '共 ' + st.counts.all + ' 个仓库，' + st.counts.update + ' 个有新版本';
      if (st.hiddenCount > 0) txt += ' · 已隐藏 ' + st.hiddenCount + ' 个自己的仓库';
      setStatus('ok', txt);
    } else {
      setStatus('ok', '准备就绪');
    }

    var bar = $('progressTop');
    if (S.refreshing) {
      bar.classList.add('active');
      var pctDone = p.phase === 'starred' ? 4 : Math.max(4, pct(p.done, p.total));
      bar.style.width = pctDone + '%';
    } else {
      bar.classList.remove('active');
      bar.style.width = '0%';
    }
  }

  /* ---------------- 渲染：列表 ---------------- */

  function visibleRepos() {
    if (!S.state) return [];
    var list = S.state.repos.slice();
    var q = S.query.trim().toLowerCase();

    if (S.filter === 'update') list = list.filter(function (r) { return r.hasUpdate; });
    else if (S.filter === 'readme') list = list.filter(function (r) { return repoMode(r) === 'readme'; });
    else if (S.filter === 'error') list = list.filter(function (r) { return !!r.lastError; });

    if (S.onlyUnread) list = list.filter(function (r) { return r.hasUpdate; });

    if (S.timeRange > 0) {
      var cutoff = Date.now() - S.timeRange * 24 * 3600 * 1000;
      list = list.filter(function (r) {
        var d = parseDate(repoUpdateAt(r));
        return d && d.getTime() >= cutoff;
      });
    }

    if (q) {
      list = list.filter(function (r) {
        var hay = r.fullName + ' ' + (r.description || '') + ' ' + (r.language || '') + ' ' + (r.readmeMessage || '');
        return hay.toLowerCase().indexOf(q) >= 0;
      });
    }

    if (S.sort === 'stars') {
      list.sort(function (a, b) { return (b.stars || 0) - (a.stars || 0); });
    } else if (S.sort === 'name') {
      list.sort(function (a, b) { return a.fullName.localeCompare(b.fullName); });
    }
    return list;
  }

  var ICON = {
    external: '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.9" stroke-linecap="round" stroke-linejoin="round"><path d="M14 4h6v6"/><path d="M20 4 11 13"/><path d="M18 14v5a1 1 0 0 1-1 1H5a1 1 0 0 1-1-1V7a1 1 0 0 1 1-1h5"/></svg>',
    download: '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.9" stroke-linecap="round" stroke-linejoin="round"><path d="M12 4v11"/><path d="m7 11 5 5 5-5"/><path d="M5 20h14"/></svg>',
    check: '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="2.1" stroke-linecap="round" stroke-linejoin="round"><path d="m4 12 5 5L20 6"/></svg>',
    star: '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.7" stroke-linejoin="round"><path d="M12 3.5 14.6 9l6 .5-4.6 3.9 1.4 5.9L12 16.1 6.6 19.3 8 13.4 3.4 9.5l6-.5z"/></svg>',
    clock: '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.7" stroke-linecap="round"><circle cx="12" cy="12" r="8.5"/><path d="M12 7.5V12l3 2"/></svg>',
    alert: '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.8" stroke-linecap="round"><path d="M12 8v5"/><path d="M12 16.5h.01"/><circle cx="12" cy="12" r="8.6"/></svg>',
    empty: '<svg viewBox="0 0 24 24" fill="none" stroke="currentColor" stroke-width="1.6" stroke-linecap="round" stroke-linejoin="round"><path d="M4 7h16v12a1 1 0 0 1-1 1H5a1 1 0 0 1-1-1z"/><path d="M4 7V5a1 1 0 0 1 1-1h5l2 3"/></svg>'
  };

  function repoRow(r, index) {
    var mode = repoMode(r);
    var cls = 'repo';
    if (r.hasUpdate) cls += ' is-update';
    else if (r.lastError) cls += ' is-error';
    else if (mode === 'none' || r.noRelease) cls += ' is-none';

    var chips = '';
    if (r.hasUpdate) chips += '<span class="chip update">新版本</span>';
    if (mode === 'readme') chips += '<span class="chip arch">README</span>';
    if (mode === 'release' && r.prerelease) chips += '<span class="chip pre">预发布</span>';
    if (r.archived) chips += '<span class="chip arch">已归档</span>';

    var tag;
    if (mode === 'readme') {
      var shortSha = (r.readmeSha || '').slice(0, 7);
      tag = '<span class="tag" title="README.md 最近提交 ' + esc(r.readmeSha || '') + '">' + esc(shortSha || 'README') + '</span>';
    } else if (mode === 'none') {
      tag = '<span class="tag empty">无可追踪</span>';
    } else if (r.latestTag) {
      tag = '<span class="tag" title="' + esc(r.latestTag) + '">' + esc(r.latestTag) + '</span>';
    } else {
      tag = '<span class="tag empty">未检查</span>';
    }

    var meta = '';
    meta += '<span class="m">' + ICON.star + fmtStars(r.stars) + '</span>';
    if (r.language) meta += '<span class="m">' + esc(r.language) + '</span>';
    if (r.lastError) {
      meta += '<span class="m" style="color:var(--danger)" title="' + esc(r.lastError) + '">' + ICON.alert + '检查失败</span>';
    } else if (mode === 'readme' && r.readmeMessage) {
      var firstLine = String(r.readmeMessage).split('\n')[0];
      meta += '<span class="m" style="max-width:340px;overflow:hidden;text-overflow:ellipsis;white-space:nowrap" title="' + esc(r.readmeMessage) + '">' + ICON.clock + esc(firstLine) + '</span>';
    } else if (mode === 'none') {
      meta += '<span class="m">既无 Release 也无 README</span>';
    } else if (r.publishedAt) {
      meta += '<span class="m">' + ICON.clock + esc(relTime(r.publishedAt)) + '</span>';
    }

    var actions = '';
    if (r.releaseUrl) {
      actions += '<button class="btn" data-act="release" title="打开 Release 页面">' + ICON.external + '</button>';
    }
    if (r.assets && r.assets.length) {
      actions += '<button class="btn" data-act="download" title="下载（自动匹配 Windows 版本）">' + ICON.download + '</button>';
    }
    if (r.hasUpdate) {
      actions += '<button class="btn" data-act="read" title="标记为已读">' + ICON.check + '</button>';
    }

    var updateAt = repoUpdateAt(r);

    return '' +
      '<div class="' + cls + '" data-repo="' + esc(r.fullName) + '" style="--i:' + Math.min(index, 18) + '" title="点击查看版本详情">' +
        '<div class="repo-state"></div>' +
        '<div class="repo-main">' +
          '<div class="repo-title">' +
            '<button class="repo-name" data-act="repo" style="border:none;background:none;padding:0;cursor:pointer;text-align:left" title="打开仓库主页">' + esc(r.fullName) + '</button>' +
            chips +
          '</div>' +
          '<div class="repo-desc">' + esc(r.description || '（没有描述）') + '</div>' +
          '<div class="repo-meta">' + meta + '</div>' +
        '</div>' +
        '<div class="repo-right">' +
          '<span class="repo-time">' + (updateAt ? esc(relTime(updateAt)) : '—') + '</span>' +
          tag +
          '<div class="repo-actions">' + actions + '</div>' +
        '</div>' +
      '</div>';
  }

  function renderList() {
    var list = $('list');
    if (!S.state) return;

    var repos = visibleRepos();
    if (!repos.length) {
      var hasAny = S.state.repos.length > 0;
      var html = '<div class="empty">' +
        '<div class="empty-icon">' + ICON.empty + '</div>' +
        '<h3>' + (hasAny ? '没有符合筛选条件的仓库' : '还没有同步星标仓库') + '</h3>' +
        '<p>' + (hasAny
          ? '换一个筛选条件、放宽时间范围，或清空搜索关键词再试。'
          : '点击右上角「刷新」拉取你的星标仓库：有 Release 的追踪最新版本，没有 Release 的自动改为追踪 README.md 的提交。首次同步只建立基线，不会把历史版本标记为新版本。') + '</p>' +
        (hasAny ? '' : '<button class="btn btn-primary" data-act="refresh-all">开始第一次刷新</button>') +
        '</div>';
      list.innerHTML = html;
      return;
    }

    var buf = [];
    for (var i = 0; i < repos.length; i++) buf.push(repoRow(repos[i], i));
    list.innerHTML = buf.join('');
  }

  /* ---------------- 渲染：仓库详情 ---------------- */

  // 把 README 里的相对地址补全为 GitHub 上的绝对地址
  function resolveRepoURL(url, repo, kind) {
    var u = String(url || '').trim();
    if (!u) return '';
    if (/^https?:\/\//i.test(u)) return u;
    if (u.indexOf('//') === 0) return 'https:' + u;
    // 任何其他协议（javascript:、data:、vbscript:…）一律拒绝
    if (/^[a-zA-Z][a-zA-Z0-9+.\-]*:/.test(u)) return '';
    if (u.charAt(0) === '#') return '';
    if (!repo) return '';
    var path = u.replace(/^\.\//, '').replace(/^\/+/, '').split('#')[0];
    if (!path) return '';
    var base = kind === 'img'
      ? 'https://raw.githubusercontent.com/' + repo + '/HEAD/'
      : 'https://github.com/' + repo + '/blob/HEAD/';
    return base + path;
  }

  /* ---------- GitHub 风格的安全 HTML：白名单标签 + 白名单属性 ---------- */

  var MD_ALLOW_TAGS = {
    a: 1, p: 1, div: 1, span: 1, br: 1, hr: 1, img: 1, strong: 1, b: 1, em: 1, i: 1, u: 1,
    s: 1, del: 1, ins: 1, sub: 1, sup: 1, kbd: 1, code: 1, pre: 1, blockquote: 1,
    ul: 1, ol: 1, li: 1, dl: 1, dt: 1, dd: 1,
    table: 1, thead: 1, tbody: 1, tfoot: 1, tr: 1, td: 1, th: 1,
    details: 1, summary: 1, picture: 1, source: 1, figure: 1, figcaption: 1, center: 1,
    h1: 1, h2: 1, h3: 1, h4: 1, h5: 1, h6: 1
  };

  // 这些属性按标签白名单重建，其余（含 on*、class、id、style）一律丢弃
  var MD_ATTR_RULES = {
    a: ['href', 'title'],
    img: ['src', 'alt', 'title', 'width', 'height'],
    source: ['src', 'type', 'media'],
    p: ['align'], div: ['align'], center: ['align'],
    h1: ['align'], h2: ['align'], h3: ['align'], h4: ['align'], h5: ['align'], h6: ['align'],
    table: ['align', 'border', 'cellpadding', 'cellspacing', 'width'],
    tr: ['align'], td: ['align', 'colspan', 'rowspan'], th: ['align', 'colspan', 'rowspan'],
    ol: ['start', 'type'], ul: ['type'],
    details: ['open'], blockquote: ['cite'], hr: ['width']
  };

  var MD_ALIGN_VALUES = { left: 1, center: 1, right: 1, justify: 1, top: 1, middle: 1, bottom: 1 };
  var MD_NUMERIC_ATTRS = { colspan: 1, rowspan: 1, start: 1, width: 1, height: 1, border: 1, cellpadding: 1, cellspacing: 1 };

  var tagSafeCache = Object.create(null);

  function looksLikeHTMLStart(line) {
    return /^\s*<\/?[a-zA-Z][a-zA-Z0-9-]*(\s|\/?>)/.test(line);
  }

  // 把 HTML 标签抽成占位符，返回 { text, tags }
  function extractTags(src) {
    var tags = [];
    var text = String(src).replace(
      /<(\/?)([a-zA-Z][a-zA-Z0-9-]*)((?:[^>"']|"[^"]*"|'[^']*')*)>/g,
      function (raw, close, name) {
        tags.push({ raw: raw, name: name.toLowerCase(), close: close === '/' });
        return '\u0001' + (tags.length - 1) + '\u0001';
      }
    );
    return { text: text, tags: tags };
  }

  function attrValueOK(name, value) {
    if (name === 'align') return !!MD_ALIGN_VALUES[String(value).toLowerCase()];
    if (MD_NUMERIC_ATTRS[name]) return /^\d{1,4}%?$/.test(String(value).trim());
    return true;
  }

  var MD_TABLE_PARTS = { tr: 1, td: 1, th: 1, thead: 1, tbody: 1, tfoot: 1 };

  // 表格内的标签是「孤儿」会被解析器丢弃，必须放进对应上下文再解析
  function parseTagElement(raw, name) {
    var open = '<div>', close = '</div>';
    if (name === 'tr') {
      open = '<table><tbody>'; close = '</tbody></table>';
    } else if (name === 'td' || name === 'th') {
      open = '<table><tbody><tr>'; close = '</tr></tbody></table>';
    } else if (name === 'thead' || name === 'tbody' || name === 'tfoot') {
      open = '<table>'; close = '</table>';
    }
    var doc = new DOMParser().parseFromString(open + raw + close, 'text/html');
    if (!doc || !doc.body) return null;
    if (MD_TABLE_PARTS[name]) return doc.body.querySelector(name);
    var wrap = doc.body.firstElementChild;
    return wrap ? wrap.firstElementChild : null;
  }

  // 用惰性文档解析单个标签，再按白名单重建，绝不回写原始属性串
  function buildSafeTag(raw, tagName, repo) {
    if (tagSafeCache[raw] !== undefined) return tagSafeCache[raw];

    var out = '';
    try {
      var el = parseTagElement(raw, tagName);
      if (el) {
        var name = el.nodeName.toLowerCase();
        if (MD_ALLOW_TAGS[name]) {
          var allow = MD_ATTR_RULES[name] || [];
          out = '<' + name;
          for (var i = 0; i < allow.length; i++) {
            var a = allow[i];
            var v = el.getAttribute(a);
            if (v === null) continue;
            if (a === 'href' || a === 'src') {
              var u = resolveRepoURL(v, repo, a === 'src' ? 'img' : 'link');
              if (!u || !/^https?:\/\//i.test(u)) continue;
              out += ' ' + a + '="' + esc(u) + '"';
              continue;
            }
            if (!attrValueOK(a, v)) continue;
            out += ' ' + a + '="' + esc(v) + '"';
          }
          if (name === 'a') out += ' rel="noreferrer noopener"';
          if (name === 'img') out += ' loading="lazy" referrerpolicy="no-referrer" onerror="this.style.display=\'none\'"';
          out += '>';
        }
      }
    } catch (e) {
      out = '';
    }

    if (Object.keys(tagSafeCache).length > 4000) tagSafeCache = Object.create(null);
    tagSafeCache[raw] = out;
    return out;
  }

  function restoreTags(s, tags, repo) {
    return s.replace(/\u0001(\d+)\u0001/g, function (m, i) {
      var tg = tags[Number(i)];
      if (!tg) return '';
      if (!MD_ALLOW_TAGS[tg.name]) return '';
      if (tg.close) return '</' + tg.name + '>';
      return buildSafeTag(tg.raw, tg.name, repo);
    });
  }

  // 整块 HTML（连续的非空行）原样保留结构，只做消毒
  function renderRawHTML(src, repo) {
    var ex = extractTags(src);
    return restoreTags(esc(ex.text), ex.tags, repo);
  }

  // 行内标记：先抽 HTML 标签 → 转义文本 → 还原 markdown → 回填已消毒标签
  function inlineMd(s, repo) {
    var ex = extractTags(s);
    var t = esc(ex.text);

    // 图片：![alt](url)
    t = t.replace(/!\[([^\]]*)\]\(([^)\s]+)\)/g, function (m, alt, url) {
      var src = resolveRepoURL(url, repo, 'img');
      if (!src) return alt;
      return '<img src="' + esc(src) + '" alt="' + alt + '" loading="lazy" referrerpolicy="no-referrer" onerror="this.style.display=\'none\'">';
    });

    t = t.replace(/`([^`]+)`/g, '<code>$1</code>');
    t = t.replace(/\*\*([^*]+)\*\*/g, '<strong>$1</strong>');
    t = t.replace(/\[([^\]]+)\]\(([^)\s]+)\)/g, function (m, label, url) {
      var href = resolveRepoURL(url, repo, 'link');
      if (!href) return label;
      return '<a href="' + esc(href) + '" rel="noreferrer noopener">' + label + '</a>';
    });
    return restoreTags(t, ex.tags, repo);
  }

  function renderMarkdown(src, repo) {
    var text = String(src == null ? '' : src).replace(/\r\n/g, '\n');
    if (!text.trim()) return '';
    // 注释与 script/style 整块直接丢弃
    text = text.replace(/<!--[\s\S]*?-->/g, '').replace(/<(script|style)\b[\s\S]*?<\/\1\s*>/gi, '');
    var lines = text.split('\n');
    var out = [];
    var inList = false;
    var i = 0;

    function closeList() {
      if (inList) { out.push('</ul>'); inList = false; }
    }

    for (i = 0; i < lines.length; i++) {
      var line = lines[i];

      if (/^\s*```/.test(line)) {
        closeList();
        var code = [];
        i++;
        while (i < lines.length && !/^\s*```/.test(lines[i])) { code.push(lines[i]); i++; }
        out.push('<pre><code>' + esc(code.join('\n')) + '</code></pre>');
        continue;
      }
      if (/^\s*$/.test(line)) { closeList(); continue; }

      // 整块 HTML（如 <p align="center">…）：连续非空行作为一块，只做消毒不包裹 <p>
      if (looksLikeHTMLStart(line)) {
        closeList();
        var blockLines = [];
        while (i < lines.length && !/^\s*$/.test(lines[i])) {
          blockLines.push(lines[i]);
          i++;
        }
        i--;
        out.push(renderRawHTML(blockLines.join('\n'), repo));
        continue;
      }

      if (/^\s*(-{3,}|\*{3,}|_{3,})\s*$/.test(line)) {
        closeList();
        out.push('<hr>');
        continue;
      }

      var h = line.match(/^(#{1,6})\s+(.*)$/);
      if (h) {
        closeList();
        var lvl = Math.min(h[1].length + 2, 6);
        out.push('<h' + lvl + '>' + inlineMd(h[2], repo) + '</h' + lvl + '>');
        continue;
      }
      if (/^\s*([-*+]|\d+\.)\s+/.test(line)) {
        if (!inList) { out.push('<ul>'); inList = true; }
        out.push('<li>' + inlineMd(line.replace(/^\s*([-*+]|\d+\.)\s+/, ''), repo) + '</li>');
        continue;
      }
      if (/^&gt;\s?/.test(line) || /^>\s?/.test(line)) {
        closeList();
        out.push('<p>' + inlineMd(line.replace(/^>\s?/, ''), repo) + '</p>');
        continue;
      }
      closeList();
      out.push('<p>' + inlineMd(line, repo) + '</p>');
    }
    closeList();
    return out.join('');
  }

  function readmeCard(rm) {
    var sha = (rm.sha || '').slice(0, 7);
    var meta = [];
    if (rm.author) meta.push(esc(rm.author));
    if (rm.date) meta.push(esc(relTime(rm.date)));
    return '' +
      '<div class="rel-card readme-card">' +
        '<div class="rel-head">' +
          '<span class="rel-tag">README.md</span>' +
          '<span class="chip arch"><span class="sha">' + esc(sha) + '</span></span>' +
          '<span class="rel-time">' + (rm.date ? esc(relTime(rm.date)) : '') + '</span>' +
        '</div>' +
        '<div class="readme-msg">' + esc(rm.message || '（无提交信息）') + '</div>' +
        '<div class="readme-meta">' + meta.join('<span>·</span>') + '</div>' +
        (rm.url ? '<div class="rel-assets"><button class="btn btn-ghost" data-dopen="' + esc(rm.url) + '">打开提交</button></div>' : '') +
      '</div>';
  }

  function relCard(rel, idx) {
    var assets = rel.assets || [];
    var source = String(rel.body || '');
    var key = relKey(rel);
    var zh = S.tr[key] || '';
    var showingZh = !!zh && S.trShow[key] !== false;
    var body = showingZh ? zh : source;
    var hasBody = body.trim().length > 0;
    var longBody = body.length > 180;
    var expanded = !!S.mdExpanded[key];
    var translating = !!S.trPending[key];

    var head = '<div class="rel-head">' +
      '<span class="rel-tag">' + esc(rel.tag || 'untagged') + '</span>' +
      (rel.isLatest ? '<span class="chip update">最新</span>' : '') +
      (rel.prerelease ? '<span class="chip pre">预发布</span>' : '') +
      (rel.name && rel.name !== rel.tag ? '<span class="rel-title">' + esc(rel.name) + '</span>' : '') +
      '<span class="rel-time">' + (rel.publishedAt ? esc(relTime(rel.publishedAt)) : '') + '</span>' +
      '</div>';

    var bodyHTML;
    if (!source.trim()) {
      bodyHTML = '<div class="md-empty">该版本没有填写更新说明</div>';
    } else if (!hasBody) {
      bodyHTML = '<div class="md-empty">译文为空</div>';
    } else {
      bodyHTML = '<div class="md' + (longBody ? ' collapsible' : '') + (expanded ? ' expanded' : '') +
        '" id="md-' + idx + '">' + renderMarkdown(body, S.detailRepo) + '</div>';
    }

    var tools = [];
    if (hasBody && longBody) {
      tools.push('<button class="md-toggle" data-md-toggle="' + idx + '">' + (expanded ? '收起' : '展开全文') + '</button>');
    }
    if (source.trim() && (zh || isMostlyNonChinese(source))) {
      var trLabel = zh ? (showingZh ? '显示原文' : '显示译文') : (translating ? '翻译中' : '翻译成中文');
      tools.push('<button class="md-toggle" data-md-tr="' + idx + '"' + (translating ? ' disabled' : '') + '>' + trLabel + '</button>');
    }
    if (tools.length) {
      bodyHTML += '<div class="md-tools">' + tools.join('') + '</div>';
    }

    var autoName = rel.autoAsset || '';
    var autoUrl = '';
    for (var i = 0; i < assets.length; i++) {
      if (assets[i].name === autoName) { autoUrl = assets[i].url; break; }
    }

    var buttons = '';
    if (autoUrl) {
      buttons += '<button class="btn btn-primary" data-durl="' + esc(autoUrl) + '" data-dname="' + esc(autoName) + '">下载 Windows 版</button>';
    }
    if (assets.length) {
      buttons += '<button class="btn" data-dlist="' + idx + '">' + (buttons ? '选择其他文件' : '查看文件') + '（' + assets.length + '）</button>';
    }
    if (rel.htmlUrl) {
      buttons += '<button class="btn btn-ghost" data-dopen="' + esc(rel.htmlUrl) + '">Release 页面</button>';
    }

    var assetList = '';
    if (assets.length) {
      var rows = [];
      for (var j = 0; j < assets.length; j++) {
        var a = assets[j];
        rows.push('<button class="btn btn-ghost" data-durl="' + esc(a.url) + '" data-dname="' + esc(a.name) + '">' +
          '<span class="fname">' + esc(a.name) + '</span>' +
          '<span style="color:var(--fg-dim)">' + esc(a.sizeText || fmtSize(a.size)) + '</span></button>');
      }
      assetList = '<div class="rel-assets" id="assets-' + idx + '" hidden>' + rows.join('') + '</div>';
    }

    return '<div class="rel-card' + (rel.isLatest ? ' is-latest' : '') + '" id="rel-' + idx + '">' +
      head + bodyHTML +
      (buttons ? '<div class="rel-assets">' + buttons + '</div>' : '') +
      assetList +
      '</div>';
  }

  function syncTabs() {
    var tabs = $('detailTabs');
    if (!tabs) return;
    Array.prototype.forEach.call(tabs.querySelectorAll('button[data-tab]'), function (b) {
      b.classList.toggle('active', b.getAttribute('data-tab') === S.detailTab);
    });
  }

  function openDetail(fullName, tab) {
    var repo = findRepo(fullName);
    if (!repo) return;
    S.detailOpen = true;
    S.detailRepo = fullName;
    S.detailSeq++;
    S.detailRels = [];
    S.detailError = '';
    S.readmeCommit = null;
    S.releasesLoaded = false;
    S.releasesLoading = false;
    S.mdExpanded = Object.create(null);
    S.detailTab = tab === 'readme' ? 'readme' : 'releases';
    var seq = S.detailSeq;

    var mode = repoMode(repo);
    $('detailRepo').textContent = repo.fullName;
    $('detailMode').textContent = mode === 'readme' ? 'README 追踪' : (mode === 'none' ? '无可追踪' : 'Release 追踪');
    $('detailSub').textContent = repo.description || '（没有描述）';
    $('detail').classList.add('open');
    $('detail').setAttribute('aria-hidden', 'false');
    syncTabs();
    syncDetailActions();

    if (S.detailTab === 'readme') {
      renderDetailBody();
      ensureReadme(seq, false);
    } else {
      $('detailBody').innerHTML = '<div class="detail-loading">正在加载最近版本…</div>';
      ensureReleases(seq);
    }
  }

  function ensureReleases(seq) {
    if (S.releasesLoaded || S.releasesLoading) return;
    S.releasesLoading = true;
    api('/api/releases?repo=' + encodeURIComponent(S.detailRepo)).then(function (data) {
      if (seq !== S.detailSeq) return;
      S.releasesLoading = false;
      S.releasesLoaded = true;
      S.detailRels = data.releases || [];
      S.readmeCommit = data.readme || null;
      S.releasesMode = data.mode || 'release';
      renderDetailBody();
    }).catch(function (err) {
      if (seq !== S.detailSeq) return;
      S.releasesLoading = false;
      S.detailError = err.message;
      renderDetailBody();
    });
  }

  function ensureReadme(seq, force) {
    var repo = S.detailRepo;
    if (!force && S.readmeCache[repo]) return;
    if (S.readmeLoading === repo && !force) return;
    S.readmeLoading = repo;
    if (S.detailTab === 'readme') renderDetailBody();

    api('/api/readme?repo=' + encodeURIComponent(repo), undefined, true).then(function (data) {
      if (seq !== S.detailSeq) return;
      S.readmeLoading = '';
      if (!data.ok) {
        S.readmeCache[repo] = { error: data.error || '该仓库没有 README' };
      } else {
        S.readmeCache[repo] = { text: data.text || '', truncated: !!data.truncated };
      }
      renderDetailBody();
    }).catch(function (err) {
      if (seq !== S.detailSeq) return;
      S.readmeLoading = '';
      S.readmeCache[repo] = { error: err.message };
      renderDetailBody();
    });
  }

  function rerenderCard(idx) {
    var el = $('rel-' + idx);
    var rel = S.detailRels[idx];
    if (!el || !rel) return;
    var holder = document.createElement('div');
    holder.innerHTML = relCard(rel, idx);
    var node = holder.firstElementChild;
    if (node) el.replaceWith(node);
  }

  /* ---------------- 翻译引擎（AI 接口 / 浏览器内置） ---------------- */

  var TR_AI = 'ai';
  var TR_BROWSER = 'browser';
  var BROWSER_CHUNK = 1200;
  var browserTr = { instance: null, creating: null };

  function currentEngine() {
    return (S.config && S.config.translateEngine === TR_BROWSER) ? TR_BROWSER : TR_AI;
  }

  function engineLabel() {
    return currentEngine() === TR_BROWSER ? '浏览器内置' : 'AI 接口';
  }

  // Chromium 新版为 self.Translator，早期实现为 self.translation
  function browserEngineKind() {
    if (self.Translator && typeof self.Translator.availability === 'function') return 'new';
    if (self.translation && typeof self.translation.canTranslate === 'function') return 'legacy';
    return '';
  }

  function browserEngineVersion() {
    var ua = navigator.userAgent || '';
    var m = /Edg\/([\d.]+)/.exec(ua) || /Chrome\/([\d.]+)/.exec(ua);
    return m ? m[1] : '';
  }

  // 查询中英翻译模型状态：available / downloadable / downloading / unavailable
  function checkBrowserEngine() {
    var kind = browserEngineKind();
    if (!kind) {
      return Promise.resolve({
        ok: false,
        state: 'unsupported',
        message: '当前浏览器不支持内置翻译（需要 Edge / Chrome 138 及以上）'
      });
    }

    var p;
    if (kind === 'new') {
      p = self.Translator.availability({ sourceLanguage: 'en', targetLanguage: 'zh' }).then(function (state) {
        var msg = {
          available: '模型已就绪，可直接使用',
          downloadable: '可用，首次使用需要下载中英语言包',
          downloading: '语言包下载中',
          unavailable: '暂不可用：未取得中英翻译模型（可点「检测」重试，或切回 AI 引擎）'
        }[state] || ('未知状态：' + state);
        return { ok: state !== 'unavailable', state: state, message: msg };
      });
    } else {
      p = self.translation.canTranslate({ sourceLanguage: 'en', targetLanguage: 'zh' }).then(function (state) {
        var ok = state === 'available' || state === 'readable';
        return {
          ok: ok,
          state: state,
          message: ok ? '模型已就绪，可直接使用'
            : (state === 'downloadable' ? '可用，首次使用需要下载中英语言包' : '浏览器未提供中英翻译模型')
        };
      });
    }
    return p.catch(function (err) {
      return { ok: false, state: 'error', message: '检测失败：' + ((err && err.message) || err) };
    });
  }

  // 创建（或复用）中英翻译器；onProgress 收到语言包下载百分比
  function getBrowserTranslator(onProgress) {
    if (browserTr.instance) return Promise.resolve(browserTr.instance);
    if (browserTr.creating) return browserTr.creating;

    var kind = browserEngineKind();
    if (!kind) return Promise.reject(new Error('当前浏览器不支持内置翻译'));

    var opts = { sourceLanguage: 'en', targetLanguage: 'zh' };
    opts.monitor = function (m) {
      if (!m || typeof m.addEventListener !== 'function' || !onProgress) return;
      try {
        m.addEventListener('downloadprogress', function (e) {
          if (e && e.total) onProgress(Math.max(1, Math.round((e.loaded / e.total) * 100)));
        });
      } catch (e) { /* 该版本不支持 monitor 时忽略 */ }
    };

    var created = (kind === 'new')
      ? self.Translator.create(opts)
      : self.translation.createTranslator(opts);

    browserTr.creating = Promise.resolve(created).then(function (tr) {
      browserTr.instance = tr;
      browserTr.creating = null;
      return tr;
    }).catch(function (err) {
      browserTr.creating = null;
      throw err;
    });
    return browserTr.creating;
  }

  // 浏览器模型单次输入有限，按段落切块后逐段翻译（超长段落硬切），总量不设上限
  function splitForBrowser(text) {
    var src = String(text || '').replace(/\r\n/g, '\n');
    var paras = src.split(/\n{2,}/);
    var chunks = [];
    var cur = '';
    for (var i = 0; i < paras.length; i++) {
      var p = paras[i];
      if (p.length > BROWSER_CHUNK) {
        if (cur) { chunks.push(cur); cur = ''; }
        for (var j = 0; j < p.length; j += BROWSER_CHUNK) chunks.push(p.slice(j, j + BROWSER_CHUNK));
        continue;
      }
      if (cur && cur.length + p.length + 2 > BROWSER_CHUNK) { chunks.push(cur); cur = ''; }
      cur = cur ? cur + '\n\n' + p : p;
    }
    if (cur) chunks.push(cur);
    return { chunks: chunks, truncated: false };
  }

  function browserTranslate(text, hooks) {
    hooks = hooks || {};
    var plan = splitForBrowser(text);
    return getBrowserTranslator(hooks.onProgress).then(function (tr) {
      var out = [];
      function step(i) {
        if (i >= plan.chunks.length) {
          return { ok: true, engine: TR_BROWSER, text: out.join('\n\n'), truncated: plan.truncated };
        }
        return Promise.resolve(tr.translate(plan.chunks[i])).then(function (zh) {
          out.push(String(zh == null ? '' : zh));
          if (hooks.onChunk) hooks.onChunk(i + 1, plan.chunks.length);
          return step(i + 1);
        });
      }
      return step(0);
    });
  }

  // 统一入口：按当前引擎分派，永远 resolve，不会打断调用方渲染
  function translateText(text, hooks) {
    hooks = hooks || {};
    if (currentEngine() === TR_BROWSER) {
      return browserTranslate(text, hooks).catch(function (err) {
        return { ok: false, engine: TR_BROWSER, error: browserErrText(err) };
      });
    }
    return api('/api/translate', { text: text }, true).then(function (data) {
      if (!data.ok) return { ok: false, engine: TR_AI, error: data.error || '翻译失败' };
      return { ok: true, engine: TR_AI, text: data.text, cached: !!data.cached };
    }).catch(function (err) {
      return { ok: false, engine: TR_AI, error: err.message };
    });
  }

  // 把内置翻译的原始英文异常转成可读中文
  function browserErrText(err) {
    var msg = String((err && err.message) || err || '');
    if (/user gesture/i.test(msg)) {
      return '首次使用需要在点击翻译时下载语言包，请再点一次「翻译成中文」';
    }
    if (/unable to create translator/i.test(msg)) {
      return '浏览器还没准备好中英翻译模型（多见于模型未下载或被策略禁用），可在设置里点「检测」查看状态，或切回 AI 引擎';
    }
    if (/not ?found|no such|unavailable|not supported/i.test(msg)) {
      return '浏览器未提供中英翻译模型，可在设置中切回 AI 引擎';
    }
    if (/network|fetch|download/i.test(msg)) {
      return '语言包下载失败，请检查网络后重试（也可在设置中切回 AI 引擎）';
    }
    return '内置翻译失败：' + (msg || '未知错误') + '（可在设置中切回 AI 引擎）';
  }

  // 语言包下载进度做节流，避免 toast 刷屏
  function makeProgressReporter() {
    var last = 0;
    var lastAt = 0;
    return function (pct) {
      if (pct >= 100) {
        if (last < 100) toast('语言包下载完成', 'ok');
        last = 100;
        return;
      }
      var now = Date.now();
      if (now - lastAt < 2500) return;
      lastAt = now;
      last = pct;
      toast('正在下载翻译语言包 ' + pct + '%', 'warn');
    };
  }

  // 翻译前检查：AI 引擎需要 Key，浏览器引擎需要浏览器支持该 API
  function ensureTranslateReady() {
    if (currentEngine() === TR_AI) {
      if (S.config && S.config.hasAIKey) return true;
      toast('请先在「设置 → 内容翻译」中填写 API Key', 'warn');
      if (S.detailOpen) closeDetail();
      openSettings(true);
      return false;
    }
    if (!browserEngineKind()) {
      toast('当前浏览器不支持内置翻译，请在设置中切回 AI 引擎', 'err');
      if (S.detailOpen) closeDetail();
      openSettings(true);
      return false;
    }
    return true;
  }

  // 一键翻译某个版本说明（命中前端缓存则直接切换显示）
  function onTranslate(idx) {
    var rel = S.detailRels[idx];
    if (!rel) return;
    var key = relKey(rel);

    if (S.tr[key]) {
      S.trShow[key] = !(S.trShow[key] !== false);
      rerenderCard(idx);
      return;
    }
    if (!ensureTranslateReady()) return;
    if (S.trPending[key]) return;

    S.trPending[key] = true;
    rerenderCard(idx);

    translateText(String(rel.body || ''), {
      onProgress: makeProgressReporter(),
      onChunk: function (done, total) {
        if (total <= 1) return;
        var btn = document.querySelector('[data-md-tr="' + idx + '"]');
        if (btn) btn.textContent = '翻译中 ' + done + '/' + total;
      }
    }).then(function (res) {
      S.trPending[key] = false;
      if (!res.ok) {
        toast(res.error || '翻译失败', 'err');
        rerenderCard(idx);
        return;
      }
      S.tr[key] = res.text;
      S.trShow[key] = true;
      rerenderCard(idx);
      toast(res.cached ? '已从缓存读取译文' : ('翻译完成（' + engineLabel() + '）'), 'ok');
    });
  }

  function renderDetailBody() {
    syncDetailActions();
    if (S.detailTab === 'readme') {
      renderReadmeView();
      return;
    }
    if (S.detailError) {
      $('detailBody').innerHTML = '<div class="detail-loading" style="color:var(--danger)">' + esc(S.detailError) + '</div>';
      return;
    }
    if (!S.releasesLoaded) {
      $('detailBody').innerHTML = '<div class="detail-loading">正在加载最近版本…</div>';
      return;
    }

    var parts = [];
    if (S.readmeCommit) parts.push(readmeCard(S.readmeCommit));

    var rels = S.detailRels || [];
    for (var i = 0; i < rels.length; i++) parts.push(relCard(rels[i], i));

    if (!parts.length) {
      parts.push('<div class="detail-loading">该仓库没有 Release，也没有可追踪的 README.md 提交。</div>');
      if (S.releasesMode === 'none') {
        parts.push('<div class="detail-loading" style="padding-top:0">可以切到 README 页签，或点右上角「打开仓库」。</div>');
      }
    }
    $('detailBody').innerHTML = parts.join('');
  }

  // README 一键翻译（只翻前 8000 字，避免上百次上游请求）
  var README_TRANSLATE_LIMIT = 8000;

  function onTranslateReadme() {
    var repo = S.detailRepo;
    var cached = S.readmeCache[repo];
    if (!cached || cached.error) return;

    if (S.readmeTrans[repo]) {
      S.readmeShow[repo] = !(S.readmeShow[repo] !== false);
      renderReadmeView();
      return;
    }
    if (!ensureTranslateReady()) return;
    if (S.readmeTransPending[repo]) return;

    S.readmeTransPending[repo] = true;
    renderReadmeView();

    var plain = String(cached.text || '').replace(/<[^>]*>/g, ' ').replace(/[ \t]+/g, ' ');
    // 浏览器引擎在本机分段翻译、不消耗配额，直接翻全文；AI 引擎仍限制 8000 字以控制耗时与费用
    var limited = currentEngine() === TR_AI;
    S.readmeTruncated[repo] = limited && plain.length > README_TRANSLATE_LIMIT;
    var source = limited ? plain.slice(0, README_TRANSLATE_LIMIT) : plain;

    translateText(source, {
      onProgress: makeProgressReporter(),
      onChunk: function (done, total) {
        if (total <= 1) return;
        var btn = document.querySelector('[data-readme-tr]');
        if (btn) btn.textContent = '翻译中 ' + done + '/' + total;
      }
    }).then(function (res) {
      S.readmeTransPending[repo] = false;
      if (!res.ok) {
        toast(res.error || '翻译失败', 'err');
        renderReadmeView();
        return;
      }
      S.readmeTrans[repo] = res.text;
      S.readmeShow[repo] = true;
      renderReadmeView();
      toast(res.cached ? '已从缓存读取译文' : ('翻译完成（' + engineLabel() + '）'), 'ok');
    });
  }

  // 页签行右侧的固定操作区：只在 README 页签下出现，且不随正文滚动
  function syncDetailActions() {
    var box = $('detailActions');
    if (!box) return;

    if (S.detailTab !== 'readme' || !S.detailOpen) {
      box.innerHTML = '';
      return;
    }

    var repo = S.detailRepo;
    var row = findRepo(repo);
    var htmlUrl = row ? row.htmlUrl : '';
    var cached = S.readmeCache[repo];

    var zh = S.readmeTrans[repo] || '';
    var showingZh = !!zh && S.readmeShow[repo] !== false;
    var pending = !!S.readmeTransPending[repo];
    var hasText = !!cached && !cached.error && !!String(cached.text || '').trim();

    var html = '';
    if (htmlUrl) {
      html += '<button class="btn btn-ghost btn-sm" data-dopen="' + esc(htmlUrl) + '">打开仓库主页</button>';
    }
    if (hasText) {
      var label = zh ? (showingZh ? '显示原文' : '显示译文') : (pending ? '翻译中' : '翻译成中文');
      html += '<button class="btn btn-ghost btn-sm" data-readme-tr="1"' + (pending ? ' disabled' : '') + '>' + label + '</button>';
    }
    html += '<button class="btn btn-ghost btn-sm" data-readme-reload="1">重新加载</button>';
    box.innerHTML = html;
  }

  function renderReadmeView() {
    var repo = S.detailRepo;
    var cached = S.readmeCache[repo];
    var parts = [];
    syncDetailActions();

    var zh = S.readmeTrans[repo] || '';
    var showingZh = !!zh && S.readmeShow[repo] !== false;
    var hasText = !!cached && !cached.error && !!String(cached.text || '').trim();

    if (!cached || S.readmeLoading === repo) {
      parts.push('<div class="detail-loading">正在加载 README…</div>');
    } else if (cached.error) {
      parts.push('<div class="detail-loading">' + esc(cached.error) + '</div>');
    } else if (!hasText) {
      parts.push('<div class="detail-loading">该仓库的 README 是空的</div>');
    } else {
      if (showingZh && S.readmeTruncated[repo]) {
        parts.push('<div class="detail-loading" style="padding:0 0 8px;text-align:left">README 较长，仅翻译了前 ' + README_TRANSLATE_LIMIT + ' 字</div>');
      }
      parts.push('<div class="md readme-full">' + renderMarkdown(showingZh ? zh : cached.text, repo) + '</div>');
      if (cached.truncated) {
        parts.push('<div class="detail-loading" style="padding-top:8px">README 超过 200KB，仅显示前 200KB</div>');
      }
    }
    $('detailBody').innerHTML = parts.join('');
  }

  function closeDetail() {
    S.detailOpen = false;
    S.detailSeq++;
    $('detailActions').innerHTML = '';
    $('detail').classList.remove('open');
    $('detail').setAttribute('aria-hidden', 'true');
  }

  function startDownloadURL(repo, url, name) {
    api('/api/download', { repo: repo, url: url, name: name }, true).then(function (data) {
      if (data.ok) {
        toast('已开始下载 ' + data.task.name, 'ok');
        openDrawer(true);
        upsertDownload(data.task);
      } else {
        toast(data.error || '下载失败', 'err');
      }
    }).catch(function (err) { toast(err.message, 'err'); });
  }

  /* ---------------- 渲染：下载抽屉 ---------------- */

  function downloadItem(t) {
    var statusText = {
      queued: '排队中',
      downloading: '下载中',
      done: '已完成',
      failed: '失败',
      canceled: '已取消'
    }[t.status] || t.status;

    var p = pct(t.downloaded, t.total);
    var cls = 'dl-item';
    if (t.status === 'done') cls += ' done';
    if (t.status === 'failed') cls += ' failed';

    var sizeLine = t.total > 0
      ? fmtSize(t.downloaded) + ' / ' + fmtSize(t.total)
      : fmtSize(t.downloaded);
    var speed = t.status === 'downloading' ? fmtSpeed(t.speed) : '';

    var actions = '';
    if (t.status === 'done') {
      actions = '<div class="dl-actions"><button class="btn btn-ghost btn-sm" data-dl="reveal" data-id="' + esc(t.id) + '">打开所在文件夹</button></div>';
    } else if (t.status === 'downloading' || t.status === 'queued') {
      actions = '<div class="dl-actions"><button class="btn btn-ghost btn-sm" data-dl="cancel" data-id="' + esc(t.id) + '">取消</button></div>';
    } else if (t.status === 'failed') {
      actions = '<div class="dl-actions"><button class="btn btn-ghost btn-sm" data-dl="reveal" data-id="' + esc(t.id) + '">打开目录</button></div>';
    }

    var errLine = t.error ? '<div class="dl-meta" style="color:var(--danger);margin-top:4px">' + esc(t.error) + '</div>' : '';

    return '' +
      '<div class="' + cls + '">' +
        '<div class="dl-top"><span class="dl-name" title="' + esc(t.name) + '">' + esc(t.name) + '</span>' +
          '<span class="chip ' + (t.status === 'done' ? 'update' : 'arch') + '">' + esc(statusText) + '</span></div>' +
        '<div class="dl-repo">' + esc(t.repo) + '</div>' +
        '<div class="dl-bar"><div class="dl-fill" style="width:' + (t.total > 0 ? p : 100) + '%"></div></div>' +
        '<div class="dl-meta"><span>' + esc(sizeLine) + '</span><span>' + esc(speed) + '</span></div>' +
        errLine + actions +
      '</div>';
  }

  function renderDrawer(dir) {
    var body = $('downloadList');
    if (dir) $('drawerDir').textContent = dir;
    if (!S.downloads.length) {
      body.innerHTML = '<div class="empty" style="padding:32px 12px">' +
        '<div class="empty-icon">' + ICON.download + '</div>' +
        '<h3 style="font-size:13px">暂无下载任务</h3>' +
        '<p style="font-size:12px">在仓库行点下载按钮即可开始，默认自动匹配 Windows 版本。</p></div>';
      return;
    }
    var buf = [];
    for (var i = 0; i < S.downloads.length; i++) buf.push(downloadItem(S.downloads[i]));
    body.innerHTML = buf.join('');
    renderHeader();
  }

  /* ---------------- 渲染：设置 ---------------- */

  var formProxyMode = 'system';
  var formEngine = TR_AI;

  function syncEngineUI() {
    var bar = $('engineBar');
    if (!bar) return;
    Array.prototype.forEach.call(bar.children, function (b) {
      b.classList.toggle('active', b.getAttribute('data-engine') === formEngine);
    });
    $('aiScopeHint').hidden = formEngine !== TR_BROWSER;
    updateAIKeyHint();
  }

  // Key 提示随引擎变化：浏览器引擎下不再提示「必须配置 Key」
  function updateAIKeyHint() {
    var el = $('aiKeyHint');
    if (!el) return;
    if (S.config && S.config.hasAIKey) {
      el.innerHTML = '用于把版本说明与 README 翻译成中文，Key 只保存在本机。';
      return;
    }
    el.innerHTML = formEngine === TR_AI
      ? '<b style="color:var(--warn)">尚未配置 API Key，AI 引擎不可用。</b>填写你自己的 Key 并保存，或切换到「浏览器内置」引擎。'
      : 'AI 引擎未配置 Key（当前使用浏览器内置引擎，无需 Key）。';
  }

  function refreshEngineStatus() {
    var out = $('engineStatus');
    if (!out) return Promise.resolve();
    if (!browserEngineKind()) {
      out.className = 'test-result err';
      out.textContent = '不支持：当前浏览器未提供内置翻译 API（需要 Edge / Chrome 138 及以上）';
      return Promise.resolve();
    }
    out.className = 'test-result';
    out.textContent = '检测中…';
    return checkBrowserEngine().then(function (r) {
      out.className = 'test-result ' + (r.ok ? 'ok' : 'err');
      var ver = browserEngineVersion();
      out.textContent = r.message + (ver ? ' · 内核 ' + ver : '');
    });
  }

  function renderSettings() {
    if (!S.config) return;
    $('cfgToken').value = '';
    $('cfgToken').placeholder = S.config.hasToken ? ('已保存 ' + S.config.tokenMasked + '（留空则不修改）') : 'ghp_…';
    $('cfgProxyUrl').value = S.config.proxyUrl || '';
    $('cfgDownloadDir').value = S.config.downloadDir || '';
    $('cfgConcurrency').value = S.config.concurrency || 8;
    $('concValue').textContent = S.config.concurrency || 8;
    $('cfgRecentCount').value = S.config.recentCount || 3;
    $('recentValue').textContent = S.config.recentCount || 3;

    $('cfgHideOwned').checked = !!S.config.hideOwned;

    formEngine = S.config.translateEngine === TR_BROWSER ? TR_BROWSER : TR_AI;
    syncEngineUI();
    refreshEngineStatus();

    $('cfgAIBaseUrl').value = S.config.aiBaseUrl || '';
    $('cfgAIKey').value = '';
    $('cfgAIKey').placeholder = S.config.hasAIKey
      ? ('已保存 ' + S.config.aiKeyMasked + '（留空则不修改）')
      : '尚未配置，请填写你的 API Key';
    $('cfgAIModel').value = S.config.aiModel || 'sensenova-6.8-flash-lite';
    updateAIKeyHint();
    $('aboutVersion').textContent = S.config.version || '—';
    $('aboutDir').textContent = S.config.configDir || '—';
    $('aboutDir').title = S.config.configDir || '';

    formProxyMode = S.config.proxyMode || 'system';
    Array.prototype.forEach.call($('proxyModeBar').children, function (btn) {
      btn.classList.toggle('active', btn.dataset.mode === formProxyMode);
    });
    $('proxyUrlField').style.display = formProxyMode === 'custom' ? '' : 'none';
  }

  function renderAll() {
    renderHeader();
    renderList();
    renderStatus();
    renderDrawer();
  }

  /* ---------------- 动作 ---------------- */

  function doRefresh(force) {
    if (S.refreshing) { toast('正在刷新中', 'warn'); return; }
    S.refreshing = true;
    renderHeader();
    renderStatus();
    api('/api/refresh', { force: !!force }).catch(function (err) {
      S.refreshing = false;
      renderAll();
      toast(err.message, 'err');
    });
  }

  function openExternal(url) {
    if (!url) return;
    api('/api/open', { url: url }).catch(function (err) { toast(err.message, 'err'); });
  }

  function markRead(repo, silent) {
    return api('/api/read', { repo: repo }).then(function () {
      if (!silent) toast('已标记为已读', 'ok');
      scheduleStateReload();
    }).catch(function (err) {
      if (!silent) toast(err.message, 'err');
    });
  }

  function startDownload(repo) {
    api('/api/download', { repo: repo }, true).then(function (data) {
      if (data.ok) {
        toast('已开始下载 ' + data.task.name, 'ok');
        openDrawer(true);
        upsertDownload(data.task);
        return;
      }
      if (data.needChoice) {
        openChoice(repo, data.assets || []);
        return;
      }
      if (data.reason === 'no-asset') {
        toast('该版本没有可下载的附件，已为你打开 Release 页面', 'warn');
        var r = findRepo(repo);
        if (r && r.releaseUrl) openExternal(r.releaseUrl);
        return;
      }
      toast(data.error || '下载失败', 'err');
    }).catch(function (err) { toast(err.message, 'err'); });
  }

  function downloadAsset(repo, name) {
    api('/api/download', { repo: repo, asset: name }, true).then(function (data) {
      closeModal();
      if (data.ok) {
        toast('已开始下载 ' + data.task.name, 'ok');
        openDrawer(true);
        upsertDownload(data.task);
      } else {
        toast(data.error || '下载失败', 'err');
      }
    }).catch(function (err) { toast(err.message, 'err'); });
  }

  function findRepo(fullName) {
    if (!S.state) return null;
    for (var i = 0; i < S.state.repos.length; i++) {
      if (S.state.repos[i].fullName === fullName) return S.state.repos[i];
    }
    return null;
  }

  function upsertDownload(task) {
    var found = false;
    for (var i = 0; i < S.downloads.length; i++) {
      if (S.downloads[i].id === task.id) { S.downloads[i] = task; found = true; break; }
    }
    if (!found) S.downloads.unshift(task);
    renderDrawer();
  }

  function openDrawer(on) {
    S.drawerOpen = on !== false ? !S.drawerOpen : false;
    if (on === true) S.drawerOpen = true;
    $('drawer').classList.toggle('open', S.drawerOpen);
    $('drawer').setAttribute('aria-hidden', S.drawerOpen ? 'false' : 'true');
    syncScrim();
    if (S.drawerOpen) loadDownloads();
  }

  function openSettings(on) {
    if (on === undefined) S.settingsOpen = !S.settingsOpen;
    else S.settingsOpen = !!on;
    $('settings').classList.toggle('open', S.settingsOpen);
    $('settings').setAttribute('aria-hidden', S.settingsOpen ? 'false' : 'true');
    if (S.settingsOpen) { loadConfig(); $('cfgToken').focus(); }
    syncScrim();
  }

  function syncScrim() {
    $('scrim').classList.toggle('show', S.settingsOpen);
  }

  function openChoice(repo, assets) {
    S.choice = { repo: repo };
    $('modalSub').textContent = repo + ' · 共 ' + assets.length + ' 个附件';
    var buf = [];
    assets.forEach(function (a) {
      buf.push('<button class="asset" data-asset="' + esc(a.name) + '">' +
        '<span class="asset-name">' + esc(a.name) + '</span>' +
        '<span class="asset-size">' + esc(a.sizeText || fmtSize(a.size)) + '</span>' +
        (a.recommended ? '<span class="chip update">推荐</span>' : '') +
        '</button>');
    });
    $('modalAssets').innerHTML = buf.join('');
    $('modal').classList.add('open');
    $('modal').setAttribute('aria-hidden', 'false');
  }

  function closeModal() {
    S.choice = null;
    $('modal').classList.remove('open');
    $('modal').setAttribute('aria-hidden', 'true');
  }

  /* ---------------- 事件绑定 ---------------- */

  $('btnRefresh').addEventListener('click', function (e) { doRefresh(e.shiftKey); });
  $('btnSettings').addEventListener('click', function () { openSettings(); });
  $('btnDownloads').addEventListener('click', function () { openDrawer(); });
  $('btnDrawerClose').addEventListener('click', function () { openDrawer(false); });
  $('btnSettingsClose').addEventListener('click', function () { openSettings(false); });
  $('btnModalClose').addEventListener('click', closeModal);
  $('scrim').addEventListener('click', function () { openSettings(false); });

  $('search').addEventListener('input', function (e) { S.query = e.target.value; renderList(); });
  $('sortSel').addEventListener('change', function (e) { S.sort = e.target.value; renderList(); });
  $('onlyUnread').addEventListener('change', function (e) { S.onlyUnread = e.target.checked; renderList(); });
  $('rangeSel').addEventListener('change', function (e) {
    S.timeRange = parseInt(e.target.value, 10) || 0;
    renderList();
  });

  /* 主题切换 */
  $('btnTheme').addEventListener('click', toggleTheme);
  $('themeBar').addEventListener('click', function (e) {
    var btn = e.target.closest('button[data-theme-mode]');
    if (!btn) return;
    applyThemeMode(btn.getAttribute('data-theme-mode'), true);
  });

  if (mediaDark) {
    var onSchemeChange = function () {
      if (themeMode() === 'auto') applyThemeMode('auto', false);
    };
    if (mediaDark.addEventListener) mediaDark.addEventListener('change', onSchemeChange);
    else if (mediaDark.addListener) mediaDark.addListener(onSchemeChange);
  }

  /* 详情弹窗 */
  $('btnDetailClose').addEventListener('click', closeDetail);
  $('btnDetailRepo').addEventListener('click', function () {
    var repo = findRepo(S.detailRepo);
    if (repo) openExternal(repo.htmlUrl);
  });
  $('detail').addEventListener('click', function (e) {
    if (e.target === this) closeDetail();
  });

  $('detailTabs').addEventListener('click', function (e) {
    var btn = e.target.closest('button[data-tab]');
    if (!btn) return;
    var tab = btn.getAttribute('data-tab');
    if (tab === S.detailTab) return;
    S.detailTab = tab === 'readme' ? 'readme' : 'releases';
    syncTabs();
    if (S.detailTab === 'readme') ensureReadme(S.detailSeq, false);
    else ensureReleases(S.detailSeq);
    renderDetailBody();
  });

  // 详情弹窗的点击委托：正文与页签行右侧操作区共用同一套处理
  function onDetailClick(e) {
    var readmeTr = e.target.closest('[data-readme-tr]');
    if (readmeTr && !readmeTr.disabled) {
      onTranslateReadme();
      return;
    }

    var reload = e.target.closest('[data-readme-reload]');
    if (reload) {
      delete S.readmeCache[S.detailRepo];
      delete S.readmeTrans[S.detailRepo];
      delete S.readmeTruncated[S.detailRepo];
      ensureReadme(S.detailSeq, true);
      return;
    }

    var link = e.target.closest('a[href]');
    if (link) {
      e.preventDefault();
      openExternal(link.getAttribute('href'));
      return;
    }

    var toggle = e.target.closest('[data-md-toggle]');
    if (toggle) {
      var ti = parseInt(toggle.getAttribute('data-md-toggle'), 10);
      var trel = S.detailRels[ti];
      if (trel) {
        var tk = relKey(trel);
        S.mdExpanded[tk] = !S.mdExpanded[tk];
        rerenderCard(ti);
      }
      return;
    }

    var trBtn = e.target.closest('[data-md-tr]');
    if (trBtn && !trBtn.disabled) {
      onTranslate(parseInt(trBtn.getAttribute('data-md-tr'), 10));
      return;
    }

    var listBtn = e.target.closest('[data-dlist]');
    if (listBtn) {
      var box = $('assets-' + listBtn.getAttribute('data-dlist'));
      if (box) box.hidden = !box.hidden;
      return;
    }

    var openBtn = e.target.closest('[data-dopen]');
    if (openBtn) { openExternal(openBtn.getAttribute('data-dopen')); return; }

    var dlBtn = e.target.closest('[data-durl]');
    if (dlBtn) {
      startDownloadURL(S.detailRepo, dlBtn.getAttribute('data-durl'), dlBtn.getAttribute('data-dname'));
    }
  }

  $('detailBody').addEventListener('click', onDetailClick);
  $('detailActions').addEventListener('click', onDetailClick);

  $('filterBar').addEventListener('click', function (e) {
    var btn = e.target.closest('button[data-filter]');
    if (!btn) return;
    S.filter = btn.dataset.filter;
    Array.prototype.forEach.call($('filterBar').children, function (b) {
      b.classList.toggle('active', b === btn);
    });
    renderList();
  });

  $('list').addEventListener('click', function (e) {
    var refreshAll = e.target.closest('[data-act="refresh-all"]');
    if (refreshAll) { doRefresh(false); return; }

    var row = e.target.closest('[data-repo]');
    if (!row) return;
    var btn = e.target.closest('[data-act]');
    if (!btn) {
      // 点击卡片主体 → 打开版本详情
      openDetail(row.dataset.repo);
      return;
    }
    var repo = findRepo(row.dataset.repo);
    if (!repo) return;
    var act = btn.dataset.act;

    if (act === 'repo') openDetail(repo.fullName, 'readme');
    else if (act === 'release') openExternal(repo.releaseUrl);
    else if (act === 'download') startDownload(repo.fullName);
    else if (act === 'read') markRead(repo.fullName);
  });

  $('downloadList').addEventListener('click', function (e) {
    var btn = e.target.closest('[data-dl]');
    if (!btn) return;
    var id = btn.dataset.id;
    if (btn.dataset.dl === 'reveal') {
      api('/api/reveal', { id: id }).catch(function (err) { toast(err.message, 'err'); });
    } else if (btn.dataset.dl === 'cancel') {
      api('/api/cancel', { id: id }).then(function () { toast('已取消', 'warn'); }).catch(function (err) { toast(err.message, 'err'); });
    }
  });

  $('modalAssets').addEventListener('click', function (e) {
    var btn = e.target.closest('[data-asset]');
    if (!btn || !S.choice) return;
    downloadAsset(S.choice.repo, btn.dataset.asset);
  });

  $('btnOpenDir').addEventListener('click', function () {
    api('/api/open-dir', {}).catch(function (err) { toast(err.message, 'err'); });
  });

  /* 设置面板 */
  $('proxyModeBar').addEventListener('click', function (e) {
    var btn = e.target.closest('button[data-mode]');
    if (!btn) return;
    formProxyMode = btn.dataset.mode;
    Array.prototype.forEach.call(this.children, function (b) {
      b.classList.toggle('active', b === btn);
    });
    $('proxyUrlField').style.display = formProxyMode === 'custom' ? '' : 'none';
    if (formProxyMode === 'custom') $('cfgProxyUrl').focus();
  });

  $('btnToggleToken').addEventListener('click', function () {
    var input = $('cfgToken');
    var show = input.type === 'password';
    input.type = show ? 'text' : 'password';
    this.textContent = show ? '隐藏' : '显示';
  });

  $('cfgConcurrency').addEventListener('input', function (e) {
    $('concValue').textContent = e.target.value;
  });

  $('cfgRecentCount').addEventListener('input', function (e) {
    $('recentValue').textContent = e.target.value;
  });

  $('btnBrowseDir').addEventListener('click', function () {
    var btn = this;
    btn.disabled = true;
    btn.textContent = '选择中…';
    api('/api/pick-dir', {}, true).then(function (data) {
      if (!data.ok) { toast(data.error || '打开文件夹选择框失败', 'err'); return; }
      if (data.canceled) { toast('已取消选择', 'warn'); return; }
      $('cfgDownloadDir').value = data.path;
      toast('已选择目录，记得点保存', 'ok');
    }).catch(function (err) {
      toast(err.message, 'err');
    }).then(function () {
      btn.disabled = false;
      btn.textContent = '浏览';
    });
  });

  $('btnOpenTokenPage').addEventListener('click', function () {
    openExternal('https://github.com/settings/tokens/new?scopes=read:user&description=MyGithubTracker');
  });

  $('btnOpenLog').addEventListener('click', function () {
    api('/api/reveal-log', {}).catch(function (err) { toast(err.message, 'err'); });
  });

  $('btnOpenDownloadDir').addEventListener('click', function () {
    var dir = $('cfgDownloadDir').value.trim();
    api('/api/open-dir', {}).catch(function () {
      toast('保存后可打开下载目录', 'warn');
    });
    if (dir) $('cfgDownloadDir').value = dir;
  });

  $('btnQuit').addEventListener('click', function () {
    if (!confirm('确定退出 MyGithubTracker？')) return;
    api('/api/quit', {}).catch(function () { });
  });

  $('aboutHomepage').addEventListener('click', function (e) {
    e.preventDefault();
    openExternal('https://yogo.wang');
  });

  $('userChip').addEventListener('click', function () {
    var login = (S.state && S.state.user) || (S.config && S.config.user) || '';
    if (login) {
      openExternal('https://github.com/' + login);
      return;
    }
    openSettings(true);
  });

  $('btnTest').addEventListener('click', function () {
    var out = $('testResult');
    out.className = 'test-result';
    out.textContent = '测试中…';
    var payload = {
      token: $('cfgToken').value.trim() || '__KEEP__',
      proxyMode: formProxyMode,
      proxyUrl: $('cfgProxyUrl').value.trim()
    };
    api('/api/test', payload, true).then(function (data) {
      if (data.ok) {
        out.className = 'test-result ok';
        out.textContent = '连接正常 · @' + data.user + ' · 剩余 ' + data.rate.remaining + '/' + data.rate.limit + ' · ' + data.latencyMs + 'ms';
      } else {
        out.className = 'test-result err';
        out.textContent = data.error || '连接失败';
      }
    }).catch(function (err) {
      out.className = 'test-result err';
      out.textContent = err.message;
    });
  });

  function saveConfig(overrides) {
    var payload = {
      token: $('cfgToken').value.trim() || '__KEEP__',
      proxyMode: formProxyMode,
      proxyUrl: $('cfgProxyUrl').value.trim(),
      downloadDir: $('cfgDownloadDir').value.trim(),
      concurrency: parseInt($('cfgConcurrency').value, 10) || 8,
      recentCount: parseInt($('cfgRecentCount').value, 10) || 3,
      translateEngine: formEngine,
      aiBaseUrl: $('cfgAIBaseUrl').value.trim(),
      aiKey: $('cfgAIKey').value.trim() || '__KEEP__',
      aiModel: $('cfgAIModel').value.trim(),
      hideOwned: !!$('cfgHideOwned').checked
    };
    if (overrides) {
      for (var k in overrides) {
        if (Object.prototype.hasOwnProperty.call(overrides, k)) payload[k] = overrides[k];
      }
    }
    return api('/api/config', payload, true).then(function (data) {
      if (!data.ok) {
        toast(data.error || '保存失败', 'err');
        return false;
      }
      S.config = data.config;
      renderSettings();
      return true;
    }).catch(function (err) {
      toast(err.message, 'err');
      return false;
    });
  }

  $('btnSaveConfig').addEventListener('click', function () {
    saveConfig().then(function (ok) {
      if (!ok) return;
      toast('设置已保存', 'ok');
      openSettings(false);
      loadState();
    });
  });

  $('engineBar').addEventListener('click', function (e) {
    var btn = e.target.closest('button[data-engine]');
    if (!btn) return;
    formEngine = btn.getAttribute('data-engine') === TR_BROWSER ? TR_BROWSER : TR_AI;
    syncEngineUI();
    if (formEngine === TR_BROWSER) refreshEngineStatus();
  });

  $('btnCheckEngine').addEventListener('click', function () {
    refreshEngineStatus();
  });

  $('btnToggleAIKey').addEventListener('click', function () {
    var input = $('cfgAIKey');
    var show = input.type === 'password';
    input.type = show ? 'text' : 'password';
    this.textContent = show ? '隐藏' : '显示';
  });

  $('btnFetchModels').addEventListener('click', function () {
    var btn = this;
    var hint = $('aiModelsHint');
    btn.disabled = true;
    btn.textContent = '读取中…';
    hint.textContent = '';
    api('/api/ai/models', {
      aiBaseUrl: $('cfgAIBaseUrl').value.trim(),
      aiKey: $('cfgAIKey').value.trim() || '__KEEP__'
    }, true).then(function (data) {
      if (!data.ok) {
        hint.textContent = ' · ' + (data.error || '读取失败');
        return;
      }
      var list = $('aiModelList');
      while (list.firstChild) list.removeChild(list.firstChild);
      (data.models || []).forEach(function (id) {
        var opt = document.createElement('option');
        opt.value = id;
        list.appendChild(opt);
      });
      hint.textContent = ' · 已读取 ' + (data.models || []).length + ' 个模型（点输入框可选）';
      if (!$('cfgAIModel').value.trim() && data.models && data.models.length) {
        $('cfgAIModel').value = data.models[0];
      }
    }).catch(function (err) {
      hint.textContent = ' · ' + err.message;
    }).then(function () {
      btn.disabled = false;
      btn.textContent = '获取模型';
    });
  });

  $('btnTestAI').addEventListener('click', function () {
    var out = $('aiTestResult');
    out.className = 'test-result';
    out.textContent = '测试中…';
    api('/api/translate/test', {
      aiBaseUrl: $('cfgAIBaseUrl').value.trim(),
      aiKey: $('cfgAIKey').value.trim() || '__KEEP__',
      aiModel: $('cfgAIModel').value.trim()
    }, true).then(function (data) {
      if (!data.ok) {
        out.className = 'test-result err';
        out.textContent = data.error || '测试失败';
        return;
      }
      var sample = String(data.sample || '').replace(/\s+/g, ' ');
      if (sample.length > 24) sample = sample.slice(0, 24) + '…';
      out.className = 'test-result ok';
      out.textContent = '可用 · ' + data.model + ' · ' + data.latencyMs + 'ms · 返回「' + sample + '」';
    }).catch(function (err) {
      out.className = 'test-result err';
      out.textContent = err.message;
    });
  });

  $('btnResetConfig').addEventListener('click', function () {
    formProxyMode = 'system';
    Array.prototype.forEach.call($('proxyModeBar').children, function (b) {
      b.classList.toggle('active', b.dataset.mode === 'system');
    });
    $('proxyUrlField').style.display = 'none';
    $('cfgProxyUrl').value = '';
    $('cfgConcurrency').value = 8;
    $('concValue').textContent = '8';
    toast('已填回默认值，记得点保存', 'warn');
  });

  /* 首次引导 */
  $('btnObTokenPage').addEventListener('click', function () {
    openExternal('https://github.com/settings/tokens/new?scopes=read:user&description=MyGithubTracker');
  });

  $('btnObStart').addEventListener('click', function () {
    var token = $('obToken').value.trim();
    var proxy = $('obProxy').value.trim();
    var err = $('obError');
    if (!token) { err.textContent = '请填写 Token'; return; }
    err.textContent = '';
    var payload = {
      token: token,
      proxyMode: proxy ? 'custom' : 'system',
      proxyUrl: proxy,
      concurrency: 8
    };
    api('/api/config', payload, true).then(function (data) {
      if (!data.ok) { err.textContent = data.error || '保存失败'; return; }
      S.config = data.config;
      $('onboarding').hidden = true;
      renderSettings();
      toast('配置已保存，开始拉取星标仓库', 'ok');
      doRefresh(false);
    }).catch(function (e) { err.textContent = e.message; });
  });

  /* 键盘 */
  document.addEventListener('keydown', function (e) {
    var tag = (document.activeElement && document.activeElement.tagName) || '';
    var typing = tag === 'INPUT' || tag === 'TEXTAREA' || tag === 'SELECT';
    if (e.key === '/' && !typing) { e.preventDefault(); $('search').focus(); return; }
    if (e.key === 'Escape') {
      if (S.detailOpen) { closeDetail(); return; }
      if ($('modal').classList.contains('open')) { closeModal(); return; }
      if (S.drawerOpen) { openDrawer(false); return; }
      if (S.settingsOpen) { openSettings(false); return; }
    }
    if (e.key === ',' && (e.ctrlKey || e.metaKey)) { e.preventDefault(); openSettings(true); }
    if ((e.key === 'L' || e.key === 'l') && e.ctrlKey && e.shiftKey) { e.preventDefault(); toggleTheme(); }
  });

  /* ---------------- SSE ---------------- */

  function connectEvents() {
    var es = new EventSource('/api/events?t=' + encodeURIComponent(TOKEN));
    es.onmessage = function (ev) {
      var payload;
      try { payload = JSON.parse(ev.data); } catch (err) { return; }
      var type = payload.type;
      var data = payload.data;

      if (type === 'progress') {
        S.progress = data;
        S.refreshing = data.phase !== 'idle';
        renderHeader();
        renderStatus();
      } else if (type === 'state-changed') {
        scheduleStateReload();
        renderHeader();
      } else if (type === 'refresh-done') {
        S.refreshing = false;
        S.progress = { phase: 'idle', done: 0, total: 0, current: '' };
        var s = (data && data.summary) || {};
        if (s.aborted) toast(s.message || '刷新已中止', 'warn');
        else toast('刷新完成：' + (s.total || 0) + ' 个仓库，' + (s.updated || 0) + ' 个有新版本' +
          (s.failed ? '，' + s.failed + ' 个失败' : ''), s.failed ? 'warn' : 'ok');
        loadState();
        loadDownloads();
      } else if (type === 'download') {
        if (data && data.id) {
          upsertDownload(data);
          if (data.status === 'done') {
            if (!S.autoRead[data.repo]) {
              S.autoRead[data.repo] = true;
              markRead(data.repo, true);
            }
            toast('下载完成：' + data.name, 'ok');
          } else if (data.status === 'failed') {
            toast('下载失败：' + (data.error || data.name), 'err');
          }
        }
      } else if (type === 'config-changed') {
        S.config = data;
        renderSettings();
        renderHeader();
      } else if (type === 'error') {
        toast((data && data.message) || '发生错误', 'err');
        scheduleStateReload();
      }
    };
    es.onerror = function () {
      setStatus('err', '与本地服务的连接中断，正在重连…');
    };
  }

  /* ---------------- 心跳 ---------------- */

  function heartbeat() {
    api('/api/heartbeat', {}).catch(function () { });
  }

  /* ---------------- 启动 ---------------- */

  // 深链：#repo=owner/name 或 ?repo=owner/name[&tab=readme]
  function openFromHash() {
    var params = new URLSearchParams(location.search);
    var name = params.get('repo') || '';
    var tab = params.get('tab') || '';

    var m = /^#repo=([^&]+)(?:&tab=([a-z]+))?/.exec(location.hash || '');
    if (m) {
      try { name = decodeURIComponent(m[1]); } catch (e) { name = m[1]; }
      if (m[2]) tab = m[2];
    }
    if (!name) return;
    if (findRepo(name)) openDetail(name, tab === 'readme' ? 'readme' : 'releases');
  }

  function boot() {
    // 浏览器可能对本地表单做自动填充，首次引导的两个输入框强制清空
    $('obToken').value = '';
    $('obProxy').value = '';
    if (!TOKEN) {
      document.body.innerHTML = '<div style="padding:40px;font-family:sans-serif;color:var(--fg)">缺少会话令牌，请从程序图标重新启动 MyGithubTracker。</div>';
      return;
    }
    syncThemeUI();
    loadConfig().then(ensureLogin);
    loadState().then(openFromHash);
    loadDownloads();
    connectEvents();
    setInterval(heartbeat, 3000);
    heartbeat();

    // 后台每 60 秒刷新一次相对时间显示
    setInterval(function () {
      if (!S.refreshing && S.state) { renderList(); renderHeader(); }
    }, 60000);
  }

  boot();
})();
