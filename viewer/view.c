/* SPDX-License-Identifier: Apache-2.0
 *
 * lcd349-view: a window onto an emulator instance (requirement 4, design D3).
 * It maps the instance's fb.bin read-only, draws the device at physical size
 * inside its case, and sends mouse, keyboard and button clicks back over the
 * control socket. The product never links SDL; this process does.
 *
 *   lcd349-view <instance-dir> [--scale S] [--mute] [-v]
 *   lcd349-view <instance-dir> --selftest out.bmp   (CI: SDL_VIDEODRIVER=dummy)
 *   lcd349-view --print-scale
 *
 * Exit codes: 0 closed, 1 error or self-test failure, 2 usage, 3 no window
 * (lcd349 run falls back to headless).
 */
#define SDL_MAIN_HANDLED
#include <SDL.h>
#include <errno.h>
#include <fcntl.h>
#include <math.h>
#include <signal.h>
#include <stdarg.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <sys/mman.h>
#include <sys/socket.h>
#include <sys/stat.h>
#include <sys/un.h>
#include <unistd.h>

#include "audio.h"
#include "core.h"
#include "display_mm.h"

#ifndef VIEW_VERSION
#define VIEW_VERSION "dev"
#endif

#define EXIT_USAGE 2
#define EXIT_NO_WINDOW 3

#define AUDIO_POLL_MS 40
#define RETRY_MS 250
#define TOAST_MS 4000
#define SELFTEST_LIMIT_MS 20000

static struct {
    const char *dir;
    float scale; /* 0: physical */
    bool mute, verbose;
    const char *selftest;
} opt;

static void say(const char *fmt, ...) __attribute__((format(printf, 1, 2)));
static void say(const char *fmt, ...)
{
    va_list ap;
    va_start(ap, fmt);
    fputs("lcd349-view: ", stdout);
    vprintf(fmt, ap);
    fputc('\n', stdout);
    fflush(stdout);
    va_end(ap);
}

/* ---- The instance: fb.bin ---- */

static struct {
    const uint8_t *map;
    dev_t dev;
    ino_t ino;
    uint32_t pid;  /* the instance whose frames are shown */
    bool alive;    /* its process exists */
    view_hdr_t hdr;
    bool hdr_ok;
    uint32_t shown_seq;
    uint32_t last_check;
} s_fb = {.shown_seq = UINT32_MAX};

static uint16_t s_panel[VIEW_PANEL_W * VIEW_PANEL_H];
static uint16_t s_land[VIEW_LCD_W * VIEW_LCD_H];

static void conns_reset(void);

/* Maps fb.bin, again if the file was replaced. Never maps a short file, so a
 * mapped page can't vanish under us. */
static void fb_attach(void)
{
    char path[1024];
    snprintf(path, sizeof path, "%s/fb.bin", opt.dir);
    struct stat st;
    if (stat(path, &st) != 0 || st.st_size < (off_t)VIEW_FB_BYTES) {
        return;
    }
    if (s_fb.map && st.st_dev == s_fb.dev && st.st_ino == s_fb.ino) {
        return;
    }
    int fd = open(path, O_RDONLY | O_CLOEXEC);
    if (fd < 0) {
        return;
    }
    void *m = mmap(NULL, VIEW_FB_BYTES, PROT_READ, MAP_SHARED, fd, 0);
    close(fd);
    if (m == MAP_FAILED) {
        return;
    }
    if (s_fb.map) {
        munmap((void *)s_fb.map, VIEW_FB_BYTES);
    }
    s_fb.map = m;
    s_fb.dev = st.st_dev;
    s_fb.ino = st.st_ino;
    s_fb.pid = 0;
}

/* Reads the header; notices a new instance (a reset starts a new process). */
static bool fb_status(void)
{
    if (!s_fb.map) {
        return false;
    }
    view_hdr_t h;
    if (!view_hdr_parse(s_fb.map, &h)) {
        s_fb.hdr_ok = false; /* starting: the magic is written last */
        return false;
    }
    bool changed = !s_fb.hdr_ok || h.status_seq != s_fb.hdr.status_seq || h.pid != s_fb.hdr.pid;
    s_fb.hdr = h;
    s_fb.hdr_ok = true;
    if (h.pid != s_fb.pid) {
        s_fb.pid = h.pid;
        s_fb.shown_seq = UINT32_MAX;
        s_fb.alive = true;
        say("attached to %s (pid %u)", opt.dir, (unsigned)h.pid);
        view_audio_resync();
        conns_reset();
        changed = true;
    }
    return changed;
}

/* Copies a new frame if there is one. True if the texture needs updating. */
static bool fb_frame(void)
{
    if (!s_fb.hdr_ok) {
        return false;
    }
    const uint32_t *seqp = (const uint32_t *)(s_fb.map + VIEW_OFF_SEQ);
    uint32_t seq = __atomic_load_n(seqp, __ATOMIC_ACQUIRE);
    if ((seq & 1u) || seq == s_fb.shown_seq) {
        return false;
    }
    memcpy(s_panel, s_fb.map + VIEW_FB_HDR, sizeof s_panel);
    __atomic_thread_fence(__ATOMIC_ACQUIRE);
    if (__atomic_load_n(seqp, __ATOMIC_ACQUIRE) != seq) {
        return false; /* torn: try again next loop */
    }
    view_panel_to_landscape(s_panel, s_land, s_fb.hdr.cw, s_fb.hdr.swap);
    s_fb.shown_seq = seq;
    return true;
}

/* ---- Control connections: one for touch, one for buttons and audio ---- */

typedef enum { REQ_NONE, REQ_TOUCH, REQ_PRESS, REQ_AUDIO } req_t;

typedef struct {
    const char *name;
    int fd;
    char in[96 * 1024];
    size_t in_len;
    char out[24 * 1024];
    size_t out_len;
    req_t inflight;
    int id;
    int press_btn;
    bool press_down;
    uint32_t retry_at;
} conn_t;

static conn_t s_touch_conn = {.name = "touch", .fd = -1};
static conn_t s_ctl_conn = {.name = "control", .fd = -1};

static view_touchq_t s_touchq;
static struct {
    bool want;    /* held by a key or the mouse */
    bool sent;    /* the instance holds it */
    bool refused; /* the instance said no: wait for a new press */
} s_btn[VIEW_N_BUTTONS];
static uint64_t s_audio_since;
static bool s_audio_sync = true; /* the first answer only sets the cursor */
static uint32_t s_audio_next;

static char s_toast[512];
static uint32_t s_toast_until;
static bool s_dirty = true;

static void toast(const char *msg)
{
    snprintf(s_toast, sizeof s_toast, "%s", msg);
    s_toast_until = SDL_GetTicks() + TOAST_MS;
    s_dirty = true;
    if (opt.verbose || opt.selftest) {
        say("%s", msg);
    }
}

static void conn_close(conn_t *c)
{
    if (c->fd >= 0) {
        close(c->fd);
    }
    c->fd = -1;
    c->in_len = c->out_len = 0;
    c->inflight = REQ_NONE;
    c->retry_at = SDL_GetTicks() + RETRY_MS;
}

/* Everything a lost connection held is gone: the instance releases buttons
 * when their client leaves, and a reset forgets everything. */
static void conns_reset(void)
{
    conn_close(&s_touch_conn);
    conn_close(&s_ctl_conn);
    s_touch_conn.retry_at = s_ctl_conn.retry_at = 0;
    view_touch_clear(&s_touchq);
    for (int i = 0; i < VIEW_N_BUTTONS; i++) {
        s_btn[i].sent = false;
        s_btn[i].refused = s_btn[i].want; /* a key held across a reset needs a new press */
    }
    s_audio_sync = true;
    s_dirty = true;
}

static bool conn_open(conn_t *c)
{
    struct sockaddr_un a = {.sun_family = AF_UNIX};
    if ((size_t)snprintf(a.sun_path, sizeof a.sun_path, "%s/control.sock", opt.dir) >= sizeof a.sun_path) {
        return false;
    }
    int fd = socket(AF_UNIX, SOCK_STREAM, 0);
    if (fd < 0) {
        return false;
    }
    if (connect(fd, (struct sockaddr *)&a, sizeof a) != 0) {
        close(fd);
        return false;
    }
    fcntl(fd, F_SETFD, FD_CLOEXEC);
    fcntl(fd, F_SETFL, fcntl(fd, F_GETFL) | O_NONBLOCK);
    c->fd = fd;
    c->in_len = c->out_len = 0;
    c->inflight = REQ_NONE;
    return true;
}

static void conn_flush(conn_t *c)
{
    while (c->fd >= 0 && c->out_len > 0) {
        ssize_t n = send(c->fd, c->out, c->out_len, 0);
        if (n > 0) {
            memmove(c->out, c->out + n, c->out_len - (size_t)n);
            c->out_len -= (size_t)n;
        } else if (n < 0 && errno == EINTR) {
            continue;
        } else if (n < 0 && (errno == EAGAIN || errno == EWOULDBLOCK)) {
            return;
        } else {
            conn_close(c);
        }
    }
}

/* Sends one request; params is a JSON object. */
static void conn_request(conn_t *c, req_t kind, const char *method, const char *params)
{
    int n = snprintf(c->out + c->out_len, sizeof c->out - c->out_len, "{\"jsonrpc\":\"2.0\",\"id\":%d,\"method\":\"%s\",\"params\":%s}\n",
                     ++c->id, method, params);
    if (n < 0 || (size_t)n >= sizeof c->out - c->out_len) {
        return;
    }
    c->out_len += (size_t)n;
    c->inflight = kind;
    conn_flush(c);
}

static jtok_t s_tok[8192];

static void lower_id(const char *id, char *out, size_t cap)
{
    size_t i = 0;
    for (; id[i] && i + 1 < cap; i++) {
        out[i] = (char)(id[i] >= 'A' && id[i] <= 'Z' ? id[i] + 32 : id[i]);
    }
    out[i] = '\0';
}

static void on_answer(conn_t *c, const char *line, size_t len)
{
    int n = json_parse(line, len, s_tok, (int)(sizeof s_tok / sizeof s_tok[0]));
    if (n <= 0 || s_tok[0].type != JT_OBJ) {
        say("%s connection: unreadable answer; reconnecting", c->name);
        conn_close(c);
        return;
    }
    if ((int)json_num(line, s_tok, json_get(line, s_tok, 0, "id"), -1) != c->id) {
        return; /* not the answer we wait for */
    }
    req_t kind = c->inflight;
    c->inflight = REQ_NONE;
    int err = json_get(line, s_tok, 0, "error");
    int res = json_get(line, s_tok, 0, "result");
    char msg[400] = "";
    if (err >= 0) {
        json_str(line, s_tok, json_get(line, s_tok, err, "message"), msg, sizeof msg);
    }
    switch (kind) {
    case REQ_TOUCH:
        if (err >= 0) {
            view_touch_clear(&s_touchq);
            toast(msg);
        } else if (!json_true(s_tok, json_get(line, s_tok, res, "delivered"))) {
            char reason[400];
            json_str(line, s_tok, json_get(line, s_tok, res, "reason"), reason, sizeof reason);
            view_touch_clear(&s_touchq);
            toast(reason[0] ? reason : "touch not delivered");
        }
        break;
    case REQ_PRESS: {
        int b = c->press_btn;
        if (err >= 0) {
            if (c->press_down) {
                s_btn[b].refused = true;
            } else {
                s_btn[b].sent = false; /* not held by us after all */
            }
            toast(msg);
        } else {
            s_btn[b].sent = c->press_down;
            if (c->press_down && json_true(s_tok, json_get(line, s_tok, res, "woke"))) {
                char t[64];
                snprintf(t, sizeof t, "%s woke the device: this press only wakes it", VIEW_BUTTONS[b].id);
                toast(t);
            }
        }
        s_dirty = true;
        break;
    }
    case REQ_AUDIO:
        if (err >= 0) {
            s_audio_next = SDL_GetTicks() + 2000; /* download mode: no audio */
            break;
        }
        {
            int evs = json_get(line, s_tok, res, "events");
            for (int i = 0; !s_audio_sync && i < (evs >= 0 ? s_tok[evs].size : 0); i++) {
                int e = json_item(s_tok, evs, i);
                uint32_t t_ms = (uint32_t)json_num(line, s_tok, json_get(line, s_tok, e, "t_ms"), 0);
                if (json_true(s_tok, json_get(line, s_tok, e, "stop"))) {
                    view_audio_stop(t_ms);
                    continue;
                }
                uint16_t hz = (uint16_t)json_num(line, s_tok, json_get(line, s_tok, e, "hz"), 0);
                uint16_t ms = (uint16_t)json_num(line, s_tok, json_get(line, s_tok, e, "ms"), 0);
                uint8_t vol = (uint8_t)json_num(line, s_tok, json_get(line, s_tok, e, "volume"), 0);
                view_audio_tone(t_ms, hz, ms, vol);
                if (opt.verbose || opt.selftest) {
                    say("tone %u Hz %u ms volume %u", hz, ms, vol);
                }
            }
            s_audio_since = (uint64_t)json_num(line, s_tok, json_get(line, s_tok, res, "next"), (double)s_audio_since);
            s_audio_sync = false;
        }
        break;
    case REQ_NONE:
        break;
    }
}

static void conn_pump(conn_t *c)
{
    if (c->fd < 0) {
        return;
    }
    conn_flush(c);
    while (c->fd >= 0) {
        if (c->in_len == sizeof c->in) {
            say("%s connection: answer too long; reconnecting", c->name);
            conn_close(c);
            return;
        }
        ssize_t n = recv(c->fd, c->in + c->in_len, sizeof c->in - c->in_len, 0);
        if (n < 0 && errno == EINTR) {
            continue;
        }
        if (n < 0 && (errno == EAGAIN || errno == EWOULDBLOCK)) {
            return;
        }
        if (n <= 0) {
            conn_close(c); /* the instance went away */
            conns_reset();
            return;
        }
        c->in_len += (size_t)n;
        char *nl;
        while (c->fd >= 0 && (nl = memchr(c->in, '\n', c->in_len))) {
            size_t len = (size_t)(nl - c->in);
            on_answer(c, c->in, len);
            if (c->fd < 0) {
                return;
            }
            memmove(c->in, nl + 1, c->in_len - len - 1);
            c->in_len -= len + 1;
        }
    }
}

/* Sends what is waiting: touch samples on one connection; button changes,
 * then audio polls, on the other. One request at a time on each. */
static void conns_service(void)
{
    uint32_t now = SDL_GetTicks();
    conn_t *cs[] = {&s_touch_conn, &s_ctl_conn};
    for (int i = 0; i < 2; i++) {
        if (cs[i]->fd < 0 && s_fb.hdr_ok && (int32_t)(now - cs[i]->retry_at) >= 0) {
            if (!conn_open(cs[i])) {
                cs[i]->retry_at = now + RETRY_MS;
            } else if (cs[i] == &s_ctl_conn) {
                say("connected to pid %u's control socket", (unsigned)s_fb.pid);
            }
        }
        conn_pump(cs[i]);
    }

    conn_t *t = &s_touch_conn;
    if (t->fd >= 0 && t->inflight == REQ_NONE && s_touchq.n > 0) {
        static view_sample_t smp[VIEW_TOUCHQ_MAX];
        int n = view_touch_take(&s_touchq, smp, VIEW_TOUCHQ_MAX);
        char params[VIEW_TOUCHQ_MAX * 40 + 64];
        size_t o = (size_t)snprintf(params, sizeof params, "{\"live\":true,\"points\":[");
        for (int i = 0; i < n; i++) {
            o += (size_t)snprintf(params + o, sizeof params - o, "%s{\"x\":%u,\"y\":%u,\"down\":%s}", i ? "," : "", smp[i].x, smp[i].y,
                                  smp[i].down ? "true" : "false");
        }
        snprintf(params + o, sizeof params - o, "]}");
        conn_request(t, REQ_TOUCH, "input.touch", params);
    }

    conn_t *c = &s_ctl_conn;
    if (c->fd < 0 || c->inflight != REQ_NONE) {
        return;
    }
    for (int i = 0; i < VIEW_N_BUTTONS; i++) {
        bool down;
        if (s_btn[i].want && !s_btn[i].sent && !s_btn[i].refused) {
            down = true;
        } else if (!s_btn[i].want && s_btn[i].sent) {
            down = false;
        } else {
            continue;
        }
        char id[16], params[96];
        lower_id(VIEW_BUTTONS[i].id, id, sizeof id);
        snprintf(params, sizeof params, "{\"button\":\"%s\",\"state\":\"%s\"}", id, down ? "down" : "up");
        c->press_btn = i;
        c->press_down = down;
        conn_request(c, REQ_PRESS, "input.press", params);
        return;
    }
    if (!opt.mute && (int32_t)(now - s_audio_next) >= 0) {
        char params[64];
        if (s_audio_sync) {
            snprintf(params, sizeof params, "{}");
        } else {
            snprintf(params, sizeof params, "{\"since\":%llu}", (unsigned long long)s_audio_since);
        }
        s_audio_next = now + AUDIO_POLL_MS;
        conn_request(c, REQ_AUDIO, "audio.events", params);
    }
}

/* ---- Window, scale and drawing ---- */

static SDL_Window *s_win;
static SDL_Renderer *s_ren;
static SDL_Texture *s_tex;
static view_layout_t s_lay;
static float s_ppp = 1; /* drawable pixels per window point */
static char s_scale_source[48], s_scale_detail[200];

/* Window points per device pixel for the display at index d. */
static float physical_scale(int d)
{
    if (opt.scale > 0) {
        snprintf(s_scale_source, sizeof s_scale_source, "--scale");
        snprintf(s_scale_detail, sizeof s_scale_detail, "set by --scale");
        return opt.scale;
    }
    const char *env = getenv("LCD349_VIEW_PPI");
    if (env && *env) {
        float ppi = strtof(env, NULL);
        if (ppi > 0) {
            snprintf(s_scale_source, sizeof s_scale_source, "LCD349_VIEW_PPI");
            snprintf(s_scale_detail, sizeof s_scale_detail, "%.0f pixels per inch at %.2f pixels per point", ppi, s_ppp);
            return view_scale_from_ppi(ppi, s_ppp);
        }
    }
    const char *drv = SDL_GetCurrentVideoDriver();
    if (drv && (!strcmp(drv, "dummy") || !strcmp(drv, "offscreen") || !strcmp(drv, "evdev"))) {
        snprintf(s_scale_source, sizeof s_scale_source, "none");
        snprintf(s_scale_detail, sizeof s_scale_detail, "no physical display (%s video driver)", drv);
        return 1.0f;
    }
    SDL_Rect b;
    if (SDL_GetDisplayBounds(d, &b) == 0) {
        float mm = view_display_mm_w(b.x, b.y, b.w, b.h);
        if (mm > 0) {
            snprintf(s_scale_source, sizeof s_scale_source, "display size");
            snprintf(s_scale_detail, sizeof s_scale_detail, "%.1f mm across %d points", mm, b.w);
            return view_scale_from_mm((float)b.w, mm);
        }
    }
    float ddpi, hdpi, vdpi;
    if (SDL_GetDisplayDPI(d, &ddpi, &hdpi, &vdpi) == 0 && hdpi > 0) {
        bool nominal = view_dpi_is_nominal(hdpi);
        snprintf(s_scale_source, sizeof s_scale_source, nominal ? "nominal dpi" : "display dpi");
        snprintf(s_scale_detail, sizeof s_scale_detail, "%s reports %.0f dpi%s", drv ? drv : "SDL", hdpi,
                 nominal ? ", a nominal value: calibrate with --scale or LCD349_VIEW_PPI" : "");
        return view_scale_from_ppi(hdpi, s_ppp);
    }
    snprintf(s_scale_source, sizeof s_scale_source, "unknown");
    snprintf(s_scale_detail, sizeof s_scale_detail, "no display size or dpi: calibrate with --scale or LCD349_VIEW_PPI");
    return 1.0f;
}

/* Chooses the scale for the window's display and resizes the window. */
static void apply_scale(void)
{
    int d = s_win ? SDL_GetWindowDisplayIndex(s_win) : 0;
    float scale = physical_scale(d < 0 ? 0 : d);
    if (scale < 0.25f) {
        scale = 0.25f;
    }
    if (scale > 8.0f) {
        scale = 8.0f;
    }
    SDL_Rect u;
    if (opt.scale <= 0 && SDL_GetDisplayUsableBounds(d < 0 ? 0 : d, &u) == 0 && u.w > 0 && u.h > 0) {
        view_layout(&s_lay, scale);
        float fit = fminf(u.w * 0.95f / s_lay.win_w, u.h * 0.95f / s_lay.win_h);
        if (fit < 1.0f) {
            scale *= fit;
            size_t n = strlen(s_scale_detail);
            snprintf(s_scale_detail + n, sizeof s_scale_detail - n, "; shrunk to fit the display, so not physical size");
        }
    }
    view_layout(&s_lay, scale);
    if (s_win) {
        SDL_SetWindowSize(s_win, (int)s_lay.win_w, (int)s_lay.win_h);
    }
    say("scale %.3f points per device pixel (%s: %s)", scale, s_scale_source, s_scale_detail);
    s_dirty = true;
}

/* Drawable pixels per window point. SDL resizes a window's drawable only when
 * it handles the resize, so this runs before every frame, not just once. */
static void update_pixel_ratio(void)
{
    int ww, wh, dw, dh;
    SDL_GetWindowSize(s_win, &ww, &wh);
    if (SDL_GetRendererOutputSize(s_ren, &dw, &dh) != 0 || ww <= 0) {
        return;
    }
    float ppp = (float)dw / (float)ww;
    static float applied = -1, applied_scale = -1;
    if (ppp == applied && s_lay.scale == applied_scale) {
        return;
    }
    bool changed = applied > 0 && fabsf(ppp - applied) > 0.01f;
    s_ppp = applied = ppp;
    applied_scale = s_lay.scale;
    SDL_RenderSetScale(s_ren, s_ppp, s_ppp);
    float px = s_lay.scale * s_ppp;
    SDL_SetTextureScaleMode(s_tex, fabsf(px - roundf(px)) < 0.01f ? SDL_ScaleModeNearest : SDL_ScaleModeLinear);
    const char *env = getenv("LCD349_VIEW_PPI");
    if (changed && opt.scale <= 0 && env && *env) {
        apply_scale(); /* a density in pixels depends on the pixel ratio */
    }
}

static void fill(const view_rect_t *r, uint32_t rgb)
{
    SDL_SetRenderDrawColor(s_ren, (Uint8)(rgb >> 16), (Uint8)(rgb >> 8), (Uint8)rgb, 255);
    SDL_FRect f = {r->x, r->y, r->w, r->h};
    SDL_RenderFillRectF(s_ren, &f);
}

static void outline(const view_rect_t *r, uint32_t rgb)
{
    SDL_SetRenderDrawColor(s_ren, (Uint8)(rgb >> 16), (Uint8)(rgb >> 8), (Uint8)rgb, 255);
    SDL_FRect f = {r->x, r->y, r->w, r->h};
    SDL_RenderDrawRectF(s_ren, &f);
}

static void text(const char *s, float x, float y, float px, uint32_t rgb)
{
    static view_rect_t r[4096];
    static SDL_FRect f[4096];
    int n = view_text_rects(s, x, y, px, r, 4096);
    for (int i = 0; i < n; i++) {
        f[i] = (SDL_FRect){r[i].x, r[i].y, r[i].w, r[i].h};
    }
    SDL_SetRenderDrawColor(s_ren, (Uint8)(rgb >> 16), (Uint8)(rgb >> 8), (Uint8)rgb, 255);
    SDL_RenderFillRectsF(s_ren, f, n);
}

/* Wraps s into the box at px points per dot, as many lines as fit. */
static void text_box(const char *s, const view_rect_t *box, float px, uint32_t rgb)
{
    int per_line = (int)((box->w + px) / (6 * px));
    int lines = (int)((box->h + px) / (8 * px));
    char line[256];
    float y = box->y;
    while (*s && lines-- > 0 && per_line > 0) {
        int take = (int)strlen(s);
        if (take > per_line) {
            take = per_line;
            for (int i = per_line; i > per_line / 2; i--) {
                if (s[i] == ' ') {
                    take = i;
                    break;
                }
            }
        }
        if (take > (int)sizeof line - 1) {
            take = (int)sizeof line - 1;
        }
        memcpy(line, s, (size_t)take);
        line[take] = '\0';
        text(line, box->x, y, px, rgb);
        s += take;
        while (*s == ' ') {
            s++;
        }
        y += 8 * px;
    }
}

typedef struct {
    char text[32];
    uint32_t bg;
} badge_t;

static int badges(badge_t *b)
{
    int n = 0;
    const view_hdr_t *h = &s_fb.hdr;
    bool connected = s_ctl_conn.fd >= 0 || s_touch_conn.fd >= 0;
    if (!s_fb.hdr_ok) {
        b[n++] = (badge_t){"NO INSTANCE", 0xB71C1C};
        return n;
    }
    if (h->power == VIEW_POWER_OFF) {
        b[n++] = (badge_t){"OFF", 0xB71C1C};
    } else if (!connected && !s_fb.alive) {
        b[n++] = (badge_t){"NOT RUNNING", 0xB71C1C};
    } else if (h->power == VIEW_POWER_DOWNLOAD) {
        b[n++] = (badge_t){"DOWNLOAD MODE", 0xE65100};
    } else if (h->power == VIEW_POWER_LIGHT_SLEEP) {
        b[n++] = (badge_t){"SLEEP", 0x1565C0};
    } else if (h->asleep) {
        b[n++] = (badge_t){"SCREEN OFF", 0x37474F};
    } else {
        b[n++] = (badge_t){"ON", 0x2E7D32};
    }
    if (h->on_battery) {
        snprintf(b[n].text, sizeof b[n].text, "BAT %u%%", h->battery_pct);
        b[n++].bg = h->battery_pct < 15 ? 0xE65100 : 0x37474F;
    } else {
        b[n++] = (badge_t){"USB", 0x37474F};
    }
    static const char *wifi[] = {"WIFI OFF", "WIFI SCAN", "WIFI JOIN", "WIFI", "NO WIFI"};
    if (h->power != VIEW_POWER_DOWNLOAD) {
        b[n] = (badge_t){"", h->wifi == 3 ? 0x2E7D32 : 0x37474F};
        snprintf(b[n++].text, sizeof b[0].text, "%s", h->wifi < 5 ? wifi[h->wifi] : "WIFI ?");
        snprintf(b[n].text, sizeof b[n].text, "LIGHT %u%%", h->backlight);
        b[n++].bg = 0x37474F;
    }
    return n;
}

static void render(void)
{
    update_pixel_ratio();
    SDL_SetRenderDrawColor(s_ren, 0x1C, 0x1D, 0x20, 255);
    SDL_RenderClear(s_ren);

    /* Badges: shrink the text until they fit on one line. */
    badge_t b[6];
    int nb = badges(b);
    float px = 2.0f;
    for (; px > 1.0f; px -= 0.5f) {
        float w = 0;
        for (int i = 0; i < nb; i++) {
            w += view_text_width(b[i].text, px) + 12 + 6;
        }
        if (w <= s_lay.badges.w) {
            break;
        }
    }
    float x = s_lay.badges.x;
    for (int i = 0; i < nb; i++) {
        float w = view_text_width(b[i].text, px) + 12;
        view_rect_t r = {x, s_lay.badges.y, w, s_lay.badges.h};
        fill(&r, b[i].bg);
        text(b[i].text, x + 6, s_lay.badges.y + (s_lay.badges.h - 7 * px) / 2, px, 0xFFFFFF);
        x += w + 6;
    }

    /* The case and its screen. */
    fill(&s_lay.case_r, 0x2F3136);
    outline(&s_lay.case_r, 0x5C6068);
    fill(&s_lay.screen, 0x000000);
    const view_hdr_t *h = &s_fb.hdr;
    bool lit = s_fb.hdr_ok && s_fb.shown_seq != UINT32_MAX && h->power != VIEW_POWER_OFF && h->power != VIEW_POWER_LIGHT_SLEEP &&
               (h->power == VIEW_POWER_DOWNLOAD || (!h->asleep && h->backlight > 0));
    if (lit) {
        /* Backlight dimming, linear in the product's level. Download mode
         * draws its notice at full brightness. */
        Uint8 m = h->power == VIEW_POWER_DOWNLOAD ? 255 : (Uint8)(255 * (h->backlight > 100 ? 100 : h->backlight) / 100);
        SDL_SetTextureColorMod(s_tex, m, m, m);
        SDL_FRect dst = {s_lay.screen.x, s_lay.screen.y, s_lay.screen.w, s_lay.screen.h};
        SDL_RenderCopyF(s_ren, s_tex, NULL, &dst);
    }

    /* Buttons: keys below the case, and caps on it where measured. */
    bool unmeasured = false;
    for (int i = 0; i < VIEW_N_BUTTONS; i++) {
        bool down = s_btn[i].want || s_btn[i].sent;
        fill(&s_lay.keys[i], down ? 0x90A4AE : 0x455A64);
        outline(&s_lay.keys[i], 0x78909C);
        char label[32];
        snprintf(label, sizeof label, "%s (%c)", VIEW_BUTTONS[i].id, VIEW_BUTTONS[i].key);
        float kpx = view_text_width(label, 2) <= s_lay.keys[i].w - 16 ? 2.0f : 1.5f;
        float tw = view_text_width(label, kpx);
        text(label, s_lay.keys[i].x + (s_lay.keys[i].w - tw) / 2, s_lay.keys[i].y + (s_lay.keys[i].h - 7 * kpx) / 2, kpx,
             down ? 0x102027 : 0xFFFFFF);
        if (s_lay.on_case[i].w > 0) {
            fill(&s_lay.on_case[i], down ? 0x90A4AE : 0x5C6068);
        } else {
            unmeasured = true;
        }
    }
    if (unmeasured || !VIEW_SCREEN_OFFSET_VERIFIED) {
        text_box(unmeasured ? "Not yet measured: button positions on the case, and the screen's place in it"
                            : "Not yet measured: the screen's place in the case",
                 &s_lay.caption, 1, 0x9AA0A6);
    }
    bool toasting = s_toast[0] && (int32_t)(SDL_GetTicks() - s_toast_until) < 0;
    if (toasting) {
        text_box(s_toast, &s_lay.hint, 1, 0xFFCC80);
    } else {
        text_box("Drag on the screen to touch. Hold B or P, or a key above, to press.", &s_lay.hint, 1, 0xC0C4C8);
    }
}

/* ---- Input ---- */

static int s_mouse_btn = -1;   /* a drawn button held by the mouse */
static bool s_mouse_touch;     /* the mouse is a finger on the screen */

static void release_all(void)
{
    for (int i = 0; i < VIEW_N_BUTTONS; i++) {
        s_btn[i].want = false;
        s_btn[i].refused = false;
    }
    if (s_mouse_touch) {
        view_touch_push(&s_touchq, s_touchq.last_x, s_touchq.last_y, false);
        s_mouse_touch = false;
    }
    s_mouse_btn = -1;
    s_dirty = true;
}

static void on_event(const SDL_Event *e, bool *quit)
{
    int dx, dy;
    switch (e->type) {
    case SDL_QUIT:
        *quit = true;
        break;
    case SDL_WINDOWEVENT:
        if (e->window.event == SDL_WINDOWEVENT_FOCUS_LOST) {
            release_all(); /* a key released elsewhere would stay held */
        } else if (e->window.event == SDL_WINDOWEVENT_DISPLAY_CHANGED && opt.scale <= 0) {
            apply_scale();
        }
        s_dirty = true;
        break;
    case SDL_KEYDOWN:
    case SDL_KEYUP:
        if (e->key.repeat) {
            break;
        }
        for (int i = 0; i < VIEW_N_BUTTONS; i++) {
            if (e->key.keysym.sym == (SDL_Keycode)VIEW_BUTTONS[i].key) {
                s_btn[i].want = e->type == SDL_KEYDOWN;
                if (!s_btn[i].want) {
                    s_btn[i].refused = false;
                }
                s_dirty = true;
            }
        }
        if (e->type == SDL_KEYDOWN && e->key.keysym.sym == SDLK_q && (e->key.keysym.mod & KMOD_GUI)) {
            *quit = true;
        }
        break;
    case SDL_MOUSEBUTTONDOWN:
        if (e->button.button != SDL_BUTTON_LEFT) {
            break;
        }
        if ((s_mouse_btn = view_hit_button(&s_lay, (float)e->button.x, (float)e->button.y)) >= 0) {
            s_btn[s_mouse_btn].want = true;
            s_dirty = true;
        } else if (view_to_device(&s_lay, (float)e->button.x, (float)e->button.y, &dx, &dy)) {
            s_mouse_touch = true;
            view_touch_push(&s_touchq, dx, dy, true);
        }
        break;
    case SDL_MOUSEMOTION:
        if (s_mouse_touch) {
            if (view_to_device(&s_lay, (float)e->motion.x, (float)e->motion.y, &dx, &dy)) {
                view_touch_push(&s_touchq, dx, dy, true);
            } else {
                view_touch_push(&s_touchq, dx, dy, false); /* the finger slid off the glass */
                s_mouse_touch = false;
            }
        }
        break;
    case SDL_MOUSEBUTTONUP:
        if (e->button.button != SDL_BUTTON_LEFT) {
            break;
        }
        if (s_mouse_btn >= 0) {
            s_btn[s_mouse_btn].want = false;
            s_btn[s_mouse_btn].refused = false;
            s_mouse_btn = -1;
            s_dirty = true;
        }
        if (s_mouse_touch) {
            view_to_device(&s_lay, (float)e->button.x, (float)e->button.y, &dx, &dy);
            view_touch_push(&s_touchq, dx, dy, false);
            s_mouse_touch = false;
        }
        break;
    default:
        break;
    }
}

/* ---- Self-test (CI): a click on the example's TAP button and a BOOT press,
 * through the same handlers as real input, then a saved frame ---- */

static struct {
    int phase;
    uint32_t t, start, last_frame;
} s_st;

static void push_mouse(Uint32 type, int dx, int dy)
{
    SDL_Event e = {0};
    e.type = type;
    e.button.button = SDL_BUTTON_LEFT;
    e.button.state = type == SDL_MOUSEBUTTONDOWN ? SDL_PRESSED : SDL_RELEASED;
    e.button.x = (int)(s_lay.screen.x + (dx + 0.5f) * s_lay.scale);
    e.button.y = (int)(s_lay.screen.y + (dy + 0.5f) * s_lay.scale);
    SDL_PushEvent(&e);
}

static void push_key(Uint32 type, SDL_Keycode k)
{
    SDL_Event e = {0};
    e.type = type;
    e.key.keysym.sym = k;
    e.key.state = type == SDL_KEYDOWN ? SDL_PRESSED : SDL_RELEASED;
    SDL_PushEvent(&e);
}

static bool idle_inputs(void)
{
    for (int i = 0; i < VIEW_N_BUTTONS; i++) {
        if (s_btn[i].want != s_btn[i].sent) {
            return false;
        }
    }
    return s_touchq.n == 0 && s_touch_conn.inflight == REQ_NONE && (s_ctl_conn.inflight == REQ_NONE || s_ctl_conn.inflight == REQ_AUDIO);
}

static bool save_bmp(const char *path)
{
    int w, h;
    if (SDL_GetRendererOutputSize(s_ren, &w, &h) != 0) {
        return false;
    }
    SDL_Surface *s = SDL_CreateRGBSurfaceWithFormat(0, w, h, 32, SDL_PIXELFORMAT_ARGB8888);
    if (!s) {
        return false;
    }
    bool ok = SDL_RenderReadPixels(s_ren, NULL, SDL_PIXELFORMAT_ARGB8888, s->pixels, s->pitch) == 0 && SDL_SaveBMP(s, path) == 0;
    SDL_FreeSurface(s);
    return ok;
}

/* Returns -1 to keep going, else the exit code. */
static int selftest_step(bool audio_ok)
{
    uint32_t now = SDL_GetTicks();
    if (!s_st.start) {
        s_st.start = now;
    }
    if (now - s_st.start > SELFTEST_LIMIT_MS) {
        say("selftest: FAIL at phase %d after %u ms (frame seq %u, %s)", s_st.phase, (unsigned)(now - s_st.start), (unsigned)s_fb.shown_seq,
            s_ctl_conn.fd >= 0 ? "connected" : "not connected");
        return 1;
    }
    switch (s_st.phase) {
    case 0: /* attached, connected, and the UI has settled */
        if (s_fb.shown_seq != UINT32_MAX && s_ctl_conn.fd >= 0 && s_touch_conn.fd >= 0 && now - s_st.last_frame > 500) {
            push_mouse(SDL_MOUSEBUTTONDOWN, VIEW_LCD_W / 2, VIEW_LCD_H / 2 + 2); /* inputs: the TAP button */
            s_st.phase = 1;
            s_st.t = now;
        }
        break;
    case 1:
        if (now - s_st.t >= 80) {
            push_mouse(SDL_MOUSEBUTTONUP, VIEW_LCD_W / 2, VIEW_LCD_H / 2 + 2);
            s_st.phase = 2;
        }
        break;
    case 2:
        if (idle_inputs()) {
            push_key(SDL_KEYDOWN, SDLK_b);
            s_st.phase = 3;
            s_st.t = now;
        }
        break;
    case 3:
        if (now - s_st.t >= 150) {
            push_key(SDL_KEYUP, SDLK_b);
            s_st.phase = 4;
            s_st.t = now;
        }
        break;
    case 4: /* settled: inputs answered, no new frame for 400 ms, the tap's tone played */
        if (idle_inputs() && now - s_st.last_frame > 400 && now - s_st.t > 400 &&
            (!audio_ok || view_audio_started() > 0 || now - s_st.t > 3000)) {
            s_dirty = true;
            render();
            bool saved = save_bmp(opt.selftest);
            SDL_RenderPresent(s_ren);
            printf("{\"selftest\":\"%s\",\"frame_seq\":%u,\"tones_received\":%u,\"tones_started\":%u,\"audio\":%s,"
                   "\"scale\":%.4f,\"pixels_per_point\":%.4f,\"screen\":[%d,%d,%d,%d],\"backlight\":%u,\"bmp\":\"%s\"}\n",
                   saved ? "pass" : "fail", (unsigned)s_fb.shown_seq, view_audio_received(), view_audio_started(), audio_ok ? "true" : "false",
                   s_lay.scale, s_ppp, (int)lroundf(s_lay.screen.x * s_ppp), (int)lroundf(s_lay.screen.y * s_ppp),
                   (int)lroundf(s_lay.screen.w * s_ppp), (int)lroundf(s_lay.screen.h * s_ppp), s_fb.hdr.backlight, opt.selftest);
            fflush(stdout);
            return saved ? 0 : 1;
        }
        break;
    }
    return -1;
}

/* ---- main ---- */

static int usage(void)
{
    fprintf(stderr, "usage: lcd349-view <instance-dir> [--scale S] [--mute] [-v] [--selftest out.bmp]\n"
                    "       lcd349-view --print-scale | --version\n"
                    "  --scale S   window points per device pixel (0.25-8); default: physical size\n"
                    "  LCD349_VIEW_PPI=N  your display's pixels per inch, when the size is wrong\n");
    return EXIT_USAGE;
}

int main(int argc, char **argv)
{
    bool print_scale = false;
    for (int i = 1; i < argc; i++) {
        if (!strcmp(argv[i], "--scale") && i + 1 < argc) {
            char *end;
            opt.scale = strtof(argv[++i], &end);
            if (*end || opt.scale < 0.25f || opt.scale > 8.0f) {
                fprintf(stderr, "lcd349-view: --scale takes a number from 0.25 to 8\n");
                return EXIT_USAGE;
            }
        } else if (!strcmp(argv[i], "--selftest") && i + 1 < argc) {
            opt.selftest = argv[++i];
        } else if (!strcmp(argv[i], "--mute")) {
            opt.mute = true;
        } else if (!strcmp(argv[i], "-v")) {
            opt.verbose = true;
        } else if (!strcmp(argv[i], "--print-scale")) {
            print_scale = true;
        } else if (!strcmp(argv[i], "--version")) {
            printf("lcd349-view %s (device.json %s)\n", VIEW_VERSION, VIEW_DEVICE_SHA);
            return 0;
        } else if (argv[i][0] != '-' && !opt.dir) {
            opt.dir = argv[i];
        } else {
            return usage();
        }
    }
    if (!opt.dir && !print_scale) {
        return usage();
    }
    signal(SIGPIPE, SIG_IGN);

    SDL_SetMainReady();
    if (SDL_Init(SDL_INIT_VIDEO) != 0) {
        fprintf(stderr, "lcd349-view: can't open a window: %s (lcd349 doctor checks SDL)\n", SDL_GetError());
        return EXIT_NO_WINDOW;
    }
    SDL_SetHint(SDL_HINT_MOUSE_FOCUS_CLICKTHROUGH, "1");
    s_win = SDL_CreateWindow("lcd349-view", SDL_WINDOWPOS_CENTERED, SDL_WINDOWPOS_CENTERED, 640, 320,
                             SDL_WINDOW_ALLOW_HIGHDPI | (print_scale ? SDL_WINDOW_HIDDEN : 0));
    s_ren = s_win ? SDL_CreateRenderer(s_win, -1, opt.selftest || print_scale ? SDL_RENDERER_SOFTWARE : 0) : NULL;
    s_tex = s_ren ? SDL_CreateTexture(s_ren, SDL_PIXELFORMAT_RGB565, SDL_TEXTUREACCESS_STREAMING, VIEW_LCD_W, VIEW_LCD_H) : NULL;
    if (!s_win || !s_ren || !s_tex) {
        fprintf(stderr, "lcd349-view: can't open a window: %s (lcd349 doctor checks SDL)\n", SDL_GetError());
        SDL_Quit();
        return EXIT_NO_WINDOW;
    }
    update_pixel_ratio();
    apply_scale();
    if (print_scale) {
        printf("{\"scale\":%.4f,\"source\":\"%s\",\"detail\":\"%s\",\"pixels_per_point\":%.2f,\"window\":[%.0f,%.0f]}\n", s_lay.scale,
               s_scale_source, s_scale_detail, s_ppp, s_lay.win_w, s_lay.win_h);
        SDL_Quit();
        return 0;
    }
    char title[300];
    const char *base = strrchr(opt.dir, '/');
    snprintf(title, sizeof title, "lcd349-view: %s", base && base[1] ? base + 1 : opt.dir);
    SDL_SetWindowTitle(s_win, title);

    bool audio_ok = !opt.mute && view_audio_open();
    if (!opt.mute && !audio_ok) {
        say("no audio output (%s): tones stay silent", SDL_GetError());
    }

    int code = 0;
    bool quit = false;
    while (!quit) {
        SDL_Event e;
        if (SDL_WaitEventTimeout(&e, 5)) {
            do {
                on_event(&e, &quit);
            } while (SDL_PollEvent(&e));
        }
        uint32_t now = SDL_GetTicks();
        if (now - s_fb.last_check >= (s_fb.hdr_ok ? 1000u : 100u)) {
            s_fb.last_check = now;
            fb_attach();
            if (s_fb.hdr_ok && s_ctl_conn.fd < 0) {
                bool alive = kill((pid_t)s_fb.hdr.pid, 0) == 0 || errno == EPERM;
                s_dirty |= alive != s_fb.alive;
                s_fb.alive = alive;
            }
        }
        s_dirty |= fb_status();
        if (fb_frame()) {
            SDL_UpdateTexture(s_tex, NULL, s_land, VIEW_LCD_W * 2);
            s_st.last_frame = now;
            s_dirty = true;
        }
        conns_service();
        static uint32_t last_render;
        if (s_toast[0] && (int32_t)(now - s_toast_until) >= 0) {
            s_toast[0] = '\0';
            s_dirty = true;
        }
        if (s_dirty || now - last_render > 1000) {
            render();
            SDL_RenderPresent(s_ren);
            s_dirty = false;
            last_render = now;
        }
        if (opt.selftest && (code = selftest_step(audio_ok)) >= 0) {
            break;
        }
    }
    view_audio_close();
    SDL_Quit();
    return code < 0 ? 0 : code;
}
