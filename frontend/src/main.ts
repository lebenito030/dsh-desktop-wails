import './style.css';

import {
    EventsOn,
} from '../wailsjs/runtime/runtime';
import {
    GetStatus,
    RetryBootstrap,
    ApplyUpdate,
    WindowMin,
    WindowToggleMax,
    WindowHideToTray,
} from '../wailsjs/go/main/App';

// ---- DOM 引用 ----
const frame = document.getElementById('dsh-frame') as HTMLIFrameElement;
const dragStrip = document.getElementById('drag-strip')!;

const btnMin = document.getElementById('btn-min') as HTMLButtonElement;
const btnMax = document.getElementById('btn-max') as HTMLButtonElement;
const btnClose = document.getElementById('btn-close') as HTMLButtonElement;
const winControls = document.getElementById('win-controls')!;
const icoMax = document.getElementById('ico-max')!;
const icoRestore = document.getElementById('ico-restore')!;

const overlay = document.getElementById('overlay')!;
const overlayTitle = document.getElementById('overlay-title')!;
const progressFill = document.getElementById('progress-fill')!;
const progressPct = document.getElementById('progress-pct')!;
const overlayLog = document.getElementById('overlay-log')!;
const btnRetry = document.getElementById('btn-retry') as HTMLButtonElement;
const btnOverlayClose = document.getElementById('btn-overlay-close') as HTMLButtonElement;

const updateModal = document.getElementById('update-modal')!;
const updateDesc = document.getElementById('update-desc')!;
const btnUpdateNow = document.getElementById('btn-update-now') as HTMLButtonElement;
const btnUpdateLater = document.getElementById('btn-update-later') as HTMLButtonElement;

// ---- 就绪 URL：iframe 加载（含登录 token 的地址经 Go 侧代理处理） ----
function setIframeUrl(url: string): void {
    if (frame.src !== url) {
        frame.src = url;
    }
}

// ---- 主题跟随：代理向 DSH 页面注入探针，经 postMessage 上报 ----
// （iframe 跨源，壳自己读不到页面配色，只能让页面自己上报）
window.addEventListener('message', (ev) => {
    if (ev.origin !== new URL(frame.src).origin) return;
    const data = ev.data as { type?: string; theme?: string };
    if (!data) return;
    if (data.type === 'dsh-desktop-theme' && (data.theme === 'dark' || data.theme === 'light')) {
        // 标题栏与右上角按钮的配色都跟随 DSH 主题
        document.body.dataset.theme = data.theme;
        winControls.dataset.theme = data.theme;
    }
});

// ---- 进度浮层 ----
function showOverlay(title: string): void {
    overlayTitle.textContent = title;
    overlayLog.textContent = '';
    btnRetry.classList.add('hidden');
    btnOverlayClose.classList.add('hidden');
    setProgress(0, 0, true);
    overlay.classList.remove('hidden');
}

function setProgress(got: number, total: number, indeterminate: boolean): void {
    if (indeterminate || total <= 0) {
        progressFill.classList.add('indeterminate');
        progressFill.style.width = '';
        progressPct.textContent = '';
    } else {
        progressFill.classList.remove('indeterminate');
        const pct = Math.min(100, Math.round((got / total) * 100));
        progressFill.style.width = pct + '%';
        progressPct.textContent = pct + '%';
    }
}

function showOverlayIfHidden(title: string): void {
    if (overlay.classList.contains('hidden')) {
        showOverlay(title);
    } else {
        overlayTitle.textContent = title;
    }
}

// ---- 事件接线 ----
EventsOn('runtime:status', (_payload: { status: string; detail: string }) => {
    // 状态点已随工具栏移除；保留事件以备恢复。 iframe URL 由 runtime:url 驱动。
});

EventsOn('runtime:url', (url: string) => {
    setIframeUrl(url);
    overlay.classList.add('hidden');
});

EventsOn('runtime:log', (line: string) => {
    if (!overlay.classList.contains('hidden')) {
        appendLog(line);
    }
});

EventsOn('bootstrap:progress', (p: {
    phase: string; detail: string;
    downloaded: number; total: number; indeterminate: boolean;
}) => {
    switch (p.phase) {
        case 'node':
        case 'dsh':
            showOverlayIfHidden('正在准备 DSH 运行时');
            setProgress(p.downloaded, p.total, p.indeterminate);
            if (p.detail && p.phase === 'dsh') appendLog(p.detail);
            break;
        case 'done':
        case 'skipped':
            overlay.classList.add('hidden');
            break;
        case 'failed':
            overlayTitle.textContent = '安装失败';
            progressFill.classList.remove('indeterminate');
            progressFill.style.width = '0%';
            appendLog(p.detail);
            btnRetry.classList.remove('hidden');
            break;
    }
});

EventsOn('update:available', (u: { local: string; latest: string }) => {
    updateDesc.textContent = `当前版本 ${u.local}，最新版本 ${u.latest}。更新会先停止 DSH，下载安装完成后再自动重启。`;
    updateModal.classList.remove('hidden');
});

EventsOn('update:progress', (p: {
    phase: string; detail: string;
    downloaded: number; total: number; indeterminate: boolean;
}) => {
    switch (p.phase) {
        case 'dsh':
            showOverlayIfHidden('正在更新 DSH');
            setProgress(p.downloaded, p.total, p.indeterminate);
            if (p.detail) appendLog(p.detail);
            break;
        case 'done':
            overlay.classList.add('hidden');
            break;
        case 'failed':
            overlayTitle.textContent = '更新失败（已回退旧版本）';
            appendLog(p.detail);
            btnOverlayClose.classList.remove('hidden');
            break;
    }
});

// ---- 日志（浮层内滚动，超长截断） ----
function appendLog(line: string): void {
    overlayLog.textContent += line + '\n';
    const lines = overlayLog.textContent.split('\n');
    if (lines.length > 400) {
        overlayLog.textContent = lines.slice(-400).join('\n');
    }
    overlayLog.scrollTop = overlayLog.scrollHeight;
}

// ---- 窗口控制按钮 ----
btnMin.addEventListener('click', () => WindowMin());
btnMax.addEventListener('click', () => WindowToggleMax());
btnClose.addEventListener('click', () => WindowHideToTray());

// 标题栏双击 = 最大化 / 还原，与原生标题栏行为一致
dragStrip.addEventListener('dblclick', () => WindowToggleMax());

EventsOn('window:maximised', (maximised: boolean) => {
    icoMax.classList.toggle('hidden', maximised);
    icoRestore.classList.toggle('hidden', !maximised);
});

// ---- 浮层按钮 ----
btnRetry.addEventListener('click', () => {
    btnRetry.classList.add('hidden');
    overlayLog.textContent = '';
    RetryBootstrap();
});

btnOverlayClose.addEventListener('click', () => {
    overlay.classList.add('hidden');
});

btnUpdateNow.addEventListener('click', () => {
    updateModal.classList.add('hidden');
    ApplyUpdate();
});

btnUpdateLater.addEventListener('click', () => {
    updateModal.classList.add('hidden');
});

// ---- 初始化：拉一次全量状态（含已就位时的 URL） ----
GetStatus().then((s: { status: string; detail: string; url: string }) => {
    if (s.url) setIframeUrl(s.url);
}).catch(console.error);
