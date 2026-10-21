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

// 认证与 API 请求
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

function focusPasswordInput() {
  const pwdInput = document.getElementById('cfg-password');
  pwdInput.scrollIntoView({ behavior: 'smooth', block: 'center' });
  pwdInput.focus();
}

// 仪表盘数据刷新
async function refreshStatus() {
  try {
    const res = await apiFetch('/api/status');
    const json = await res.json();
    const data = json.data;

    document.getElementById('stat-total').textContent = data.total_plugins ? data.total_plugins.toLocaleString() : '0';
    document.getElementById('stat-mode').textContent = data.mode ? data.mode.toUpperCase() : '--';

    if (data.last_sync_time && data.last_sync_time !== '0001-01-01T00:00:00Z') {
      const dt = new Date(data.last_sync_time);
      document.getElementById('stat-time').textContent = dt.toLocaleTimeString();
    } else {
      document.getElementById('stat-time').textContent = '未同步';
    }

    if (data.last_sync_error) {
      document.getElementById('stat-duration').textContent = '❌ 错误: ' + data.last_sync_error;
      document.getElementById('stat-duration').style.color = 'var(--danger-color)';
    } else {
      document.getElementById('stat-duration').textContent = (data.last_sync_duration_ms || 0) + ' ms';
      document.getElementById('stat-duration').style.color = 'var(--text-main)';
    }

    // 默认密码警告提示
    const defaultBanner = document.getElementById('default-pwd-banner');
    if (json.is_default_password) {
      defaultBanner.style.display = 'flex';
    } else {
      defaultBanner.style.display = 'none';
    }
  } catch (e) {
    console.error(e);
  }
}

// 加载配置
async function loadConfig() {
  try {
    const res = await apiFetch('/api/config');
    const cfg = await res.json();

    document.getElementById('cfg-mode').value = cfg.sync.mode || 'mirror';
    document.getElementById('cfg-interval').value = cfg.sync.interval_minutes || 30;
    document.getElementById('cfg-server-port').value = cfg.server.port || 55150;
    document.getElementById('cfg-webui-port').value = cfg.webui.port || 65150;
    document.getElementById('cfg-upstream').value = cfg.sync.upstream_url || '';
    document.getElementById('cfg-allow-ips').value = (cfg.webui.allow_ips || []).join('\n');
    document.getElementById('cfg-password').value = '';
  } catch (e) {
    console.error(e);
  }
}

// 保存配置
async function saveConfig(e) {
  e.preventDefault();
  const saveBtn = document.getElementById('save-btn');
  saveBtn.disabled = true;
  saveBtn.textContent = '保存中...';

  const mode = document.getElementById('cfg-mode').value;
  const interval = parseInt(document.getElementById('cfg-interval').value, 10);
  const serverPort = parseInt(document.getElementById('cfg-server-port').value, 10);
  const webuiPort = parseInt(document.getElementById('cfg-webui-port').value, 10);
  const upstream = document.getElementById('cfg-upstream').value.trim();
  const allowIPs = document.getElementById('cfg-allow-ips').value.split('\n').map(s => s.trim()).filter(Boolean);
  const pwd = document.getElementById('cfg-password').value.trim();

  const payload = {
    server: { port: serverPort },
    webui: { port: webuiPort, allow_ips: allowIPs },
    sync: { mode: mode, interval_minutes: interval, upstream_url: upstream }
  };
  if (pwd) {
    payload.webui.admin_password = pwd;
  }

  try {
    const res = await apiFetch('/api/config', {
      method: 'POST',
      headers: { 'Content-Type': 'application/json' },
      body: JSON.stringify(payload)
    });
    const result = await res.json();
    if (res.ok) {
      alert('配置已成功保存并完成热重载！');
      document.getElementById('cfg-password').value = '';
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

// 手动触发同步
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
      syncDesc.textContent = '✅ 同步触发完成！';
      setTimeout(refreshStatus, 1500);
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
  setInterval(refreshStatus, 10000);
}

// 检查是否已登录
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
