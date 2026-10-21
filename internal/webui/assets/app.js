// 主题模式: 'system' | 'light' | 'dark'
let currentTheme = localStorage.getItem('storeluna_theme') || 'system';

function applyTheme(theme) {
  currentTheme = theme;
  localStorage.setItem('storeluna_theme', theme);
  const root = document.documentElement;
  const icon = document.getElementById('theme-icon');
  const text = document.getElementById('theme-text');

  if (theme === 'system') {
    const isDark = window.matchMedia('(prefers-color-scheme: dark)').matches;
    root.setAttribute('data-theme', isDark ? 'dark' : 'light');
    icon.textContent = '💻';
    text.textContent = '跟随系统';
  } else if (theme === 'light') {
    root.setAttribute('data-theme', 'light');
    icon.textContent = '☀️';
    text.textContent = '浅色模式';
  } else if (theme === 'dark') {
    root.setAttribute('data-theme', 'dark');
    icon.textContent = '🌙';
    text.textContent = '深色模式';
  }
}

document.getElementById('theme-toggle-btn').addEventListener('click', () => {
  if (currentTheme === 'system') applyTheme('light');
  else if (currentTheme === 'light') applyTheme('dark');
  else applyTheme('system');
});

window.matchMedia('(prefers-color-scheme: dark)').addEventListener('change', () => {
  if (currentTheme === 'system') applyTheme('system');
});

applyTheme(currentTheme);

// 认证与网络请求
let authToken = localStorage.getItem('storeluna_token') || '';

async function apiFetch(url, options = {}) {
  options.headers = options.headers || {};
  if (authToken) {
    options.headers['Authorization'] = 'Bearer ' + authToken;
  }
  const res = await fetch(url, options);
  if (res.status === 401) {
    showAuthModal();
    throw new Error('未授权，请登录');
  }
  return res;
}

function showAuthModal() {
  document.getElementById('auth-modal').style.display = 'flex';
  document.getElementById('logout-btn').style.display = 'none';
  document.getElementById('login-pwd').focus();
}

function hideAuthModal() {
  document.getElementById('auth-modal').style.display = 'none';
  document.getElementById('logout-btn').style.display = 'inline-flex';
}

async function handleLogin(e) {
  e.preventDefault();
  const pwd = document.getElementById('login-pwd').value;
  const errEl = document.getElementById('login-error');
  errEl.style.display = 'none';

  try {
    const res = await fetch('/api/login', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ password: pwd })
    });
    const data = await res.json();
    if (res.ok && data.token) {
      authToken = data.token;
      localStorage.setItem('storeluna_token', authToken);
      hideAuthModal();
      initDashboard();
    } else {
      errEl.textContent = data.error || '密码错误';
      errEl.style.display = 'block';
    }
  } catch (err) {
    errEl.textContent = '连接服务失败: ' + err.message;
    errEl.style.display = 'block';
  }
}

function logout() {
  authToken = '';
  localStorage.removeItem('storeluna_token');
  showAuthModal();
}

function focusPasswordCard() {
  const card = document.getElementById('security-card');
  card.scrollIntoView({ behavior: 'smooth', block: 'center' });
  document.getElementById('new-password').focus();
}

// 原始配置副本与脏值（未保存）追踪
let originalConfig = {};

function getFormValues() {
  return {
    mode: document.getElementById('cfg-mode').value,
    interval: parseInt(document.getElementById('cfg-interval').value, 10),
    serverPort: parseInt(document.getElementById('cfg-server-port').value, 10),
    webuiPort: parseInt(document.getElementById('cfg-webui-port').value, 10),
    upstream: document.getElementById('cfg-upstream').value.trim(),
    proxyEnabled: document.getElementById('cfg-proxy-enabled').checked,
    proxyUrl: document.getElementById('cfg-proxy-url').value.trim(),
    allowIps: document.getElementById('cfg-allow-ips').value.split('\n').map(s => s.trim()).filter(Boolean).join('\n')
  };
}

function checkDirtyState() {
  if (!originalConfig.mode) return;
  const current = getFormValues();

  const dirtyMap = {
    'cfg-mode': current.mode !== originalConfig.mode,
    'cfg-interval': current.interval !== originalConfig.interval,
    'cfg-server-port': current.serverPort !== originalConfig.serverPort,
    'cfg-webui-port': current.webuiPort !== originalConfig.webuiPort,
    'cfg-upstream': current.upstream !== originalConfig.upstream,
    'cfg-proxy-enabled': current.proxyEnabled !== originalConfig.proxyEnabled,
    'cfg-proxy-url': current.proxyUrl !== originalConfig.proxyUrl,
    'cfg-allow-ips': current.allowIps !== originalConfig.allowIps
  };

  let totalDirty = 0;
  for (const [id, isDirty] of Object.entries(dirtyMap)) {
    const inputEl = document.getElementById(id);
    const dotEl = document.getElementById('dot-' + id);
    if (isDirty) {
      totalDirty++;
      inputEl.classList.add('is-dirty');
      if (dotEl) dotEl.classList.add('visible');
    } else {
      inputEl.classList.remove('is-dirty');
      if (dotEl) dotEl.classList.remove('visible');
    }
  }

  // VS Code 风格 Tab Dot 控制
  const tabDot = document.getElementById('config-tab-dot');
  if (totalDirty > 0) {
    tabDot.classList.add('visible');
  } else {
    tabDot.classList.remove('visible');
  }
}

// 绑定输入框变化事件
['cfg-mode', 'cfg-interval', 'cfg-server-port', 'cfg-webui-port', 'cfg-upstream', 'cfg-proxy-enabled', 'cfg-proxy-url', 'cfg-allow-ips'].forEach(id => {
  const el = document.getElementById(id);
  el.addEventListener('input', checkDirtyState);
  el.addEventListener('change', checkDirtyState);
});

// 加载配置
async function loadConfig() {
  try {
    const res = await apiFetch('/api/config');
    const cfg = await res.json();

    const mode = (cfg.sync && cfg.sync.mode) || 'mirror';
    const interval = (cfg.sync && cfg.sync.interval_minutes) || 30;
    const serverPort = (cfg.server && cfg.server.port) || 55150;
    const webuiPort = (cfg.webui && cfg.webui.port) || 65150;
    const upstream = (cfg.sync && cfg.sync.upstream_url) || '';
    const proxyEnabled = !!(cfg.sync && cfg.sync.proxy && cfg.sync.proxy.enabled);
    const proxyUrl = (cfg.sync && cfg.sync.proxy && cfg.sync.proxy.url) || 'http://127.0.0.1:7890';
    const allowIps = (cfg.webui && cfg.webui.allow_ips ? cfg.webui.allow_ips : []).join('\n');

    document.getElementById('cfg-mode').value = mode;
    document.getElementById('cfg-interval').value = interval;
    document.getElementById('cfg-server-port').value = serverPort;
    document.getElementById('cfg-webui-port').value = webuiPort;
    document.getElementById('cfg-upstream').value = upstream;
    document.getElementById('cfg-proxy-enabled').checked = proxyEnabled;
    document.getElementById('cfg-proxy-url').value = proxyUrl;
    document.getElementById('cfg-allow-ips').value = allowIps;

    originalConfig = {
      mode, interval, serverPort, webuiPort, upstream, proxyEnabled, proxyUrl, allowIps
    };

    checkDirtyState();
  } catch (e) {
    console.error('加载配置异常:', e);
  }
}

function resetForm() {
  if (confirm('确认将表单所有改动重置为当前生效的配置？')) {
    loadConfig();
  }
}

// 保存配置
async function saveConfig(e) {
  e.preventDefault();
  const saveBtn = document.getElementById('save-btn');
  saveBtn.disabled = true;
  saveBtn.textContent = '保存中...';

  const current = getFormValues();
  const payload = {
    server: { port: current.serverPort },
    webui: { port: current.webuiPort, allow_ips: current.allowIps.split('\n').filter(Boolean) },
    sync: {
      mode: current.mode,
      interval_minutes: current.interval,
      upstream_url: current.upstream,
      proxy: {
        enabled: current.proxyEnabled,
        url: current.proxyUrl
      }
    }
  };

  try {
    const res = await apiFetch('/api/config', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(payload)
    });
    const result = await res.json();
    if (res.ok) {
      alert('配置已成功保存并完成实时热重载！');
      loadConfig();
      refreshStatus();
    } else {
      alert('保存失败: ' + (result.error || '未知错误'));
    }
  } catch (err) {
    alert('请求失败: ' + err.message);
  } finally {
    saveBtn.disabled = false;
    saveBtn.textContent = '保存并热重载配置';
  }
}

// 单独的管理员密码更新处理
async function handlePasswordUpdate(e) {
  e.preventDefault();
  const newPwd = document.getElementById('new-password').value;
  const confirmPwd = document.getElementById('confirm-password').value;
  const btn = document.getElementById('update-pwd-btn');

  if (newPwd !== confirmPwd) {
    alert('两次输入的密码不一致，请检查！');
    return;
  }
  if (newPwd.length < 4) {
    alert('密码长度不能少于 4 位字符！');
    return;
  }

  btn.disabled = true;
  btn.textContent = '修改中...';

  try {
    const res = await apiFetch('/api/password', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify({ new_password: newPwd })
    });
    const data = await res.json();
    if (res.ok) {
      alert('管理员密码修改成功！');
      document.getElementById('new-password').value = '';
      document.getElementById('confirm-password').value = '';
      refreshStatus();
    } else {
      alert('修改失败: ' + (data.error || '未知错误'));
    }
  } catch (err) {
    alert('请求异常: ' + err.message);
  } finally {
    btn.disabled = false;
    btn.textContent = '更新管理员密码';
  }
}

// 刷新状态与呼吸灯
async function refreshStatus() {
  try {
    const res = await apiFetch('/api/status');
    const json = await res.json();
    const data = json.data;

    document.getElementById('stat-total').textContent = data.total_plugins ? data.total_plugins.toLocaleString() : '0';
    document.getElementById('stat-mode').textContent = (data.mode || '--').toUpperCase();

    if (data.last_sync_time && data.last_sync_time !== '0001-01-01T00:00:00Z') {
      const dt = new Date(data.last_sync_time);
      document.getElementById('stat-time').textContent = dt.toLocaleTimeString();
    } else {
      document.getElementById('stat-time').textContent = '未同步';
    }

    const dot = document.getElementById('pulse-dot');
    if (json.is_syncing) {
      dot.className = 'status-dot dot-yellow';
      document.getElementById('stat-duration').textContent = '⚡ 同步进行中...';
    } else if (data.last_sync_error) {
      dot.className = 'status-dot dot-red';
      document.getElementById('stat-duration').textContent = '异常: ' + data.last_sync_error;
      document.getElementById('stat-duration').style.color = 'var(--danger-color)';
    } else {
      dot.className = 'status-dot dot-green';
      document.getElementById('stat-duration').textContent = (data.last_sync_duration_ms || 0) + ' ms';
      document.getElementById('stat-duration').style.color = 'var(--text-main)';
    }

    // 默认密码警告
    const banner = document.getElementById('default-pwd-banner');
    const secCard = document.getElementById('security-card');
    if (json.is_default_password) {
      banner.style.display = 'flex';
      secCard.classList.add('warning-edge');
    } else {
      banner.style.display = 'none';
      secCard.classList.remove('warning-edge');
    }
  } catch (e) {
    console.error(e);
  }
}

// 立即全量同步
async function triggerSync() {
  const syncBtn = document.getElementById('sync-btn');
  const syncDesc = document.getElementById('sync-desc');
  syncBtn.disabled = true;
  syncBtn.textContent = '正在同步中...';
  syncDesc.textContent = '后台同步任务已启动，请等待索引生成并落盘...';

  try {
    const res = await apiFetch('/api/sync', { method: 'POST' });
    const data = await res.json();
    if (res.ok) {
      syncDesc.textContent = '✅ 已成功发送同步指令！';
      setTimeout(refreshStatus, 1000);
    } else {
      syncDesc.textContent = '❌ 同步失败: ' + (data.error || '未知错误');
    }
  } catch (err) {
    syncDesc.textContent = '❌ 网络异常: ' + err.message;
  } finally {
    syncBtn.disabled = false;
    syncBtn.textContent = '立即全量同步';
  }
}

function initDashboard() {
  hideAuthModal();
  refreshStatus();
  loadConfig();
  setInterval(refreshStatus, 8000);
}

// 自动检测登录态
(async function init() {
  if (!authToken) {
    showAuthModal();
    return;
  }
  try {
    const res = await apiFetch('/api/status');
    if (res.ok) {
      initDashboard();
    }
  } catch (e) {
    showAuthModal();
  }
})();
