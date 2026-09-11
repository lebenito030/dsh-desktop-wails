# -*- coding: utf-8 -*-
"""生成 DSH Desktop 的全部图标资源（ICO / PNG）。

    python build/gen-icons.py        # 需要 numpy + Pillow

产物：

| 文件 | 尺寸 | 用途 |
|---|---|---|
| `appicon.png` | 1024 | Wails 应用图标源（Windows exe / Linux / macOS .app）|
| `tray.ico` | 16/20/24/32/48 | Windows 托盘（全 DIB）|
| `windows/icon.ico` | 16…256 | exe 与任务栏窗口（≥128 用 PNG 压缩）|
| `tray.png` | 32 | Linux 托盘（彩色）|
| `tray-template.png` | 32 | macOS 菜单栏（单色 template，库按 16pt 渲染 → 32px = 16pt @2x）|

设计依据（数值是量出来的，不是拍的，详见 docs/01-design.md「图标」一节）：

- 白色圆角方块 + 黑鲸鱼，**满格零内缩**（实测 150% 缩放下 `SM_CXSMICON=24`，
  微信绿色方块与相邻白色徽标实测都是 24×24 → 托盘图标内容填满整格才是标准画法）；
- 圆角半径 = 边长的 22%；鲸鱼宽 = 边长的 64%，等比居中；
- 鲸鱼色 `#232323`（采样自设计稿），方块 `#FFFFFF`。
- **macOS 例外**：菜单栏图标必须是「黑色 + alpha」的 template（系统按浅/深色菜单栏
  自动反色），且不要方块底色——所以另出一份只有鲸鱼的 `tray-template.png`。

渲染方式：8~16 倍超采样下做解析判定，再盒式降采样得到抗锯齿边缘。
圆角方块用圆角矩形 SDF，鲸鱼用 nonzero 环绕规则的扫描线填充。
"""
import io
import os
import re
import struct

import numpy as np
from PIL import Image

HERE = os.path.dirname(os.path.abspath(__file__))
SVG = os.path.join(HERE, "dsh-logo.svg")
OUT = HERE

BADGE = (255, 255, 255)      # 白色圆角方块
WHALE = (0x23, 0x23, 0x23)   # 鲸鱼（采样自设计稿）
RADIUS_RATIO = 0.22          # 圆角半径 / 边长
WHALE_W_RATIO = 0.64         # 鲸鱼宽 / 边长
TEMPLATE_W_RATIO = 0.94      # macOS template 里鲸鱼宽 / 画布边长（菜单栏图标要占满）


# ---------- 路径解析（绝对 M / C / L / Z） ----------

def parse_path(d):
    toks = re.findall(r"[MCZ]|-?\d*\.?\d+(?:[eE][-+]?\d+)?", d)
    subs, cur, cmd = [], [], None
    i = 0
    x = y = sx = sy = 0.0
    while i < len(toks):
        t = toks[i]
        if t in ("M", "C", "Z"):
            cmd = t
            i += 1
            if cmd == "Z":
                if cur:
                    cur.append((sx, sy))
                continue
        if cmd == "M":
            x, y = float(toks[i]), float(toks[i + 1])
            i += 2
            if cur:
                subs.append(np.array(cur))
            cur = [(x, y)]
            sx, sy = x, y
            cmd = "L"
        elif cmd == "C":
            x1, y1 = float(toks[i]), float(toks[i + 1])
            x2, y2 = float(toks[i + 2]), float(toks[i + 3])
            x3, y3 = float(toks[i + 4]), float(toks[i + 5])
            i += 6
            span = (abs(x1 - x) + abs(y1 - y) + abs(x2 - x) + abs(y2 - y)
                    + abs(x3 - x) + abs(y3 - y))
            n = max(8, min(96, int(span * 2) + 8))
            for k in range(1, n + 1):
                u = k / n
                m = 1 - u
                cur.append((m ** 3 * x + 3 * m * m * u * x1 + 3 * m * u * u * x2 + u ** 3 * x3,
                            m ** 3 * y + 3 * m * m * u * y1 + 3 * m * u * u * y2 + u ** 3 * y3))
            x, y = x3, y3
        elif cmd == "L":
            x, y = float(toks[i]), float(toks[i + 1])
            i += 2
            cur.append((x, y))
        else:
            raise SystemExit("unsupported path command: %r" % cmd)
    if cur:
        subs.append(np.array(cur))
    return subs


def whale_edges(subs, target_w, cx, cy):
    """把路径映射成像素空间的边数组，鲸鱼按 target_w 等比缩放并居中于 (cx, cy)。"""
    pts = np.vstack(subs)
    bx0, by0 = pts.min(axis=0)
    bx1, by1 = pts.max(axis=0)
    scale = target_w / (bx1 - bx0)
    ox = cx - (bx0 + bx1) / 2 * scale
    oy = cy - (by0 + by1) / 2 * scale
    edges = []
    for sub in subs:
        p = sub.copy()
        p[:, 0] = p[:, 0] * scale + ox
        p[:, 1] = p[:, 1] * scale + oy
        a, b = p[:-1], p[1:]
        keep = ~((a[:, 0] == b[:, 0]) & (a[:, 1] == b[:, 1]))
        edges.append(np.hstack([a[keep], b[keep]]))
    return np.vstack(edges)


# ---------- 栅格化 ----------

def fill_nonzero(edges, sub, ss):
    """nonzero 环绕规则扫描线填充，返回 sub x sub 的 0/1 覆盖。"""
    x0, y0, x1, y1 = edges[:, 0], edges[:, 1], edges[:, 2], edges[:, 3]
    dy = y1 - y0
    keep = dy != 0
    x0, y0, x1, y1, dy = x0[keep], y0[keep], x1[keep], y1[keep], dy[keep]
    dxdy = (x1 - x0) / dy
    sign = np.where(dy > 0, 1.0, -1.0)

    ys = (np.arange(sub) + 0.5) / ss
    sx = (np.arange(sub) + 0.5) / ss
    cover = np.zeros((sub, sub), dtype=bool)
    for r, yv in enumerate(ys):
        hit = ((y0 <= yv) & (yv < y1)) | ((y1 <= yv) & (yv < y0))
        if not hit.any():
            continue
        xs = x0[hit] + (yv - y0[hit]) * dxdy[hit]
        order = np.argsort(xs, kind="stable")
        wind = np.cumsum(sign[hit][order])
        idx = np.searchsorted(xs[order], sx, side="right") - 1
        w = np.where(idx >= 0, wind[np.maximum(idx, 0)], 0.0)
        cover[r] = w != 0
    return cover


def rounded_box(size, ss):
    """圆角矩形 SDF 的覆盖率（满格，无内缩）。"""
    r = RADIUS_RATIO * size
    c = size / 2.0
    v = (np.arange(size * ss) + 0.5) / ss
    qx = np.abs(v[:, None] - c) - (c - r)
    qy = np.abs(v[None, :] - c) - (c - r)
    d = np.minimum(np.maximum(qx, qy), 0) + np.hypot(np.maximum(qx, 0), np.maximum(qy, 0)) - r
    return d <= 0


def downsample(mask, size, ss):
    return mask.reshape(size, ss, size, ss).mean(axis=(1, 3))


def supersample(size):
    return int(max(2, min(16, 2048 // size)))


def render(size, subs, preview=False):
    """白色圆角方块 + 黑鲸鱼（Windows 托盘 / Linux 托盘 / 应用图标）。"""
    ss = supersample(size)
    sub = size * ss

    badge = rounded_box(size, ss)
    edges = whale_edges(subs, WHALE_W_RATIO * size, size / 2.0, size / 2.0)
    whale = fill_nonzero(edges, sub, ss) & badge

    cb = downsample(badge, size, ss)
    cw = downsample(whale, size, ss)

    rgb = np.zeros((size, size, 3), dtype=np.float64)
    for k in range(3):
        rgb[..., k] = BADGE[k] * (1 - cw) + WHALE[k] * cw
    rgba = np.zeros((size, size, 4), dtype=np.uint8)
    rgba[..., :3] = np.clip(rgb + 0.5, 0, 255).astype(np.uint8)
    rgba[..., 3] = np.clip(cb * 255 + 0.5, 0, 255).astype(np.uint8)
    img = Image.fromarray(rgba, "RGBA")
    if preview:
        back = np.zeros((size, size, 4), dtype=np.uint8)
        back[..., :3] = 0xFF
        return Image.alpha_composite(Image.fromarray(back, "RGBA"), img)
    return img


def render_template(size, subs):
    """macOS 菜单栏 template：只有鲸鱼，纯黑 + alpha；系统按菜单栏明暗自动反色。"""
    ss = supersample(size)
    sub = size * ss
    edges = whale_edges(subs, TEMPLATE_W_RATIO * size, size / 2.0, size / 2.0)
    cw = downsample(fill_nonzero(edges, sub, ss), size, ss)

    rgba = np.zeros((size, size, 4), dtype=np.uint8)
    rgba[..., 3] = np.clip(cw * 255 + 0.5, 0, 255).astype(np.uint8)
    return Image.fromarray(rgba, "RGBA")


# ---------- 容器封装 ----------

def dib_entry(img):
    w, h = img.size
    px = np.array(img)
    xor = px[::-1][..., [2, 1, 0, 3]].tobytes()
    stride = ((w + 31) // 32) * 4
    mask = np.zeros((h, stride), dtype=np.uint8)
    alpha = px[::-1, :, 3]
    for x in range(w):
        mask[alpha[:, x] == 0, x // 8] |= np.uint8(0x80 >> (x % 8))
    andmask = mask.tobytes()
    hdr = struct.pack("<IiiHHIIiiII", 40, w, h * 2, 1, 32, 0,
                      len(xor) + len(andmask), 0, 0, 0, 0)
    return hdr + xor + andmask


def png_entry(img):
    b = io.BytesIO()
    img.save(b, format="PNG", optimize=True)
    return b.getvalue()


def write_ico(path, imgs):
    sizes = sorted(imgs)
    blobs = [png_entry(imgs[s]) if s >= 128 else dib_entry(imgs[s]) for s in sizes]
    offset = 6 + 16 * len(sizes)
    dirs = b""
    for s, blob in zip(sizes, blobs):
        dim = 0 if s >= 256 else s
        dirs += struct.pack("<BBBBHHII", dim, dim, 0, 0, 1, 32, len(blob), offset)
        offset += len(blob)
    with open(path, "wb") as f:
        f.write(struct.pack("<HHH", 0, 1, len(sizes)) + dirs + b"".join(blobs))


def main():
    svg = open(SVG, encoding="utf-8").read()
    d = re.search(r'\sd="([^"]+)"', svg).group(1)
    subs = parse_path(d)
    print("subpaths:", len(subs), "points:", sum(len(s) for s in subs))

    cache = {}

    def get(s, preview=False):
        key = (s, preview)
        if key not in cache:
            cache[key] = render(s, subs, preview)
        return cache[key]

    get(1024).save(os.path.join(OUT, "appicon.png"))
    write_ico(os.path.join(OUT, "tray.ico"),
              {s: get(s) for s in (16, 20, 24, 32, 48)})
    write_ico(os.path.join(OUT, "windows", "icon.ico"),
              {s: get(s) for s in (16, 20, 24, 32, 48, 64, 128, 256)})

    # Linux 托盘：彩色，32px 够面板缩放到 16~24 且 HiDPI 不糊。
    get(32).save(os.path.join(OUT, "tray.png"))

    # macOS 菜单栏：库在 systray_darwin.m 里把 NSImage 强制设成 16×16 **点**
    # （[image setSize:NSMakeSize(16,16)]），所以资产要给 32×32 像素
    # ——正好是 16pt @2x，Retina 上一像素不浪费，也无需再出 @3x。
    render_template(32, subs).save(os.path.join(OUT, "tray-template.png"))

    tmp = os.environ.get("TEMP", "/tmp")
    get(256, True).save(os.path.join(tmp, "badge-preview-light.png"))
    print("done ->", OUT)


if __name__ == "__main__":
    main()
