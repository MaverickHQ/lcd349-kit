/* SPDX-License-Identifier: Apache-2.0
 *
 * The control server's transport (design D4, requirement 5): JSON-RPC 2.0, one
 * message per line, on <instance>/control.sock (directory 0700, socket 0600).
 * No TCP or UDP listener exists anywhere (requirement 13.1).
 *
 * It runs as a FreeRTOS task with non-blocking sockets: each loop polls with
 * timeout 0, serves at most one request per client, and yields with
 * vTaskDelay(1), because the linux target's scheduler is cooperative. In
 * download mode there is no scheduler, so the same loop blocks in poll().
 * Requests that take time (wait, input.*) stay pending and are answered when
 * they finish; a client's next request waits for its previous answer.
 */
#include <errno.h>
#include <fcntl.h>
#include <poll.h>
#include <stdarg.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <sys/socket.h>
#include <sys/stat.h>
#include <sys/un.h>
#include <unistd.h>

#include "esp_log.h"
#include "freertos/FreeRTOS.h"
#include "freertos/task.h"

#include "control.h"

#define MAX_CLIENTS 8
#define LINE_CAP (64 * 1024) /* requirement 5.8 */
#define READ_CHUNK 16384
#define IN_MAX (1024 * 1024) /* unanswered input a client may queue */

static const char *TAG = "lcd349_ctl";
static int s_listen = -1;
static rpc_client_t s_clients[MAX_CLIENTS];
static bool s_download;

bool emu_control_in_download(void)
{
    return s_download;
}

rpc_res_t rpc_fail(rpc_err_t *err, int code, const char *fmt, ...)
{
    err->code = code;
    va_list ap;
    va_start(ap, fmt);
    vsnprintf(err->msg, sizeof err->msg, fmt, ap);
    va_end(ap);
    return RPC_FAIL;
}

/* ---- Output ---- */

static void queue_out(rpc_client_t *c, const char *s)
{
    size_t n = strlen(s);
    if (c->out_off == c->out_len) {
        c->out_off = c->out_len = 0;
    }
    char *grown = realloc(c->out, c->out_len + n + 1);
    if (!grown) {
        ESP_LOGE(TAG, "out of memory for a reply");
        return;
    }
    c->out = grown;
    memcpy(c->out + c->out_len, s, n);
    c->out[c->out_len + n] = '\n';
    c->out_len += n + 1;
}

static void reply(rpc_client_t *c, const cJSON *id, cJSON *result, const rpc_err_t *err)
{
    cJSON *msg = cJSON_CreateObject();
    cJSON_AddStringToObject(msg, "jsonrpc", "2.0");
    cJSON_AddItemToObject(msg, "id", id ? cJSON_Duplicate(id, true) : cJSON_CreateNull());
    if (err) {
        cJSON *e = cJSON_AddObjectToObject(msg, "error");
        cJSON_AddNumberToObject(e, "code", err->code);
        cJSON_AddStringToObject(e, "message", err->msg);
        cJSON_Delete(result);
    } else {
        cJSON_AddItemToObject(msg, "result", result ? result : cJSON_CreateObject());
    }
    char *txt = cJSON_PrintUnformatted(msg);
    cJSON_Delete(msg);
    if (txt) {
        queue_out(c, txt);
        cJSON_free(txt);
    }
}

static void reply_error(rpc_client_t *c, const cJSON *id, int code, const char *msg)
{
    rpc_err_t e = {.code = code};
    snprintf(e.msg, sizeof e.msg, "%s", msg);
    reply(c, id, NULL, &e);
}

/* ---- Clients ---- */

static void end_pending(rpc_client_t *c)
{
    if (c->op) {
        emu_methods_cancel(c); /* releases what the op holds, finished or not */
    }
    cJSON_Delete(c->pending_id);
    free(c->op);
    c->pending_id = NULL;
    c->op = NULL;
    c->poll = NULL;
    c->pending = false;
}

static void close_client(rpc_client_t *c)
{
    int code = c->exit_after_reply;
    end_pending(c);
    emu_methods_client_closed(c);
    close(c->fd);
    free(c->in);
    free(c->out);
    memset(c, 0, sizeof *c);
    c->fd = -1;
    if (code) {
        emu_exit(code); /* the client left before reading the answer: act anyway */
    }
}

void emu_control_exit_after_reply(rpc_client_t *c, int code)
{
    c->exit_after_reply = code;
}

static void dispatch(rpc_client_t *c, const char *line, size_t len)
{
    c->serial_prev = c->serial_cur;
    c->serial_cur = emu_serial_next();
    cJSON *req = cJSON_ParseWithLength(line, len);
    if (!req) {
        reply_error(c, NULL, RPC_PARSE_ERROR, "parse error: the line isn't valid JSON");
        return;
    }
    const cJSON *id = cJSON_GetObjectItem(req, "id");
    bool notification = id == NULL;
    const cJSON *method = cJSON_GetObjectItem(req, "method");
    const cJSON *params = cJSON_GetObjectItem(req, "params");
    if (!cJSON_IsObject(req) || !cJSON_IsString(method)) {
        reply_error(c, id, RPC_INVALID_REQUEST, "invalid request: want {\"jsonrpc\":\"2.0\",\"id\":..,\"method\":\"..\",\"params\":{..}}");
        cJSON_Delete(req);
        return;
    }
    if (params && !cJSON_IsObject(params)) {
        reply_error(c, id, RPC_INVALID_PARAMS, "params must be an object");
        cJSON_Delete(req);
        return;
    }
    const rpc_method_t *m = NULL;
    for (const rpc_method_t *it = emu_rpc_methods; it->name; it++) {
        if (strcmp(it->name, method->valuestring) == 0) {
            m = it;
            break;
        }
    }
    if (!m) {
        char msg[200];
        snprintf(msg, sizeof msg, "no method %.80s; call info for the list", method->valuestring);
        reply_error(c, id, RPC_METHOD_NOT_FOUND, msg);
        cJSON_Delete(req);
        return;
    }
    if (s_download && !m->in_download_mode) {
        reply_error(c, id, RPC_UNAVAILABLE,
                    "the device is in ROM download mode (BOOT was held through the reset), so the product isn't running; "
                    "use power {\"action\":\"reset\"} to start it");
        cJSON_Delete(req);
        return;
    }
    cJSON *empty = NULL;
    if (!params) {
        params = empty = cJSON_CreateObject();
    }
    cJSON *result = NULL;
    rpc_err_t err = {0};
    rpc_res_t r = m->fn(c, params, &result, &err);
    if (r == RPC_PENDING) {
        c->pending = true;
        c->pending_id = notification ? NULL : cJSON_Duplicate(id, true);
    } else if (!notification) {
        reply(c, id, result, r == RPC_FAIL ? &err : NULL);
    } else {
        cJSON_Delete(result);
    }
    cJSON_Delete(empty);
    cJSON_Delete(req);
}

static void poll_pending(rpc_client_t *c)
{
    cJSON *result = NULL;
    rpc_err_t err = {0};
    rpc_res_t r = c->poll(c, &result, &err);
    if (r == RPC_PENDING) {
        return;
    }
    if (c->pending_id) {
        reply(c, c->pending_id, result, r == RPC_FAIL ? &err : NULL);
    } else {
        cJSON_Delete(result);
    }
    end_pending(c);
}

/* Reads what's available. Returns false when the client has gone or floods us. */
static bool read_client(rpc_client_t *c)
{
    char buf[READ_CHUNK];
    for (;;) {
        ssize_t n = read(c->fd, buf, sizeof buf);
        if (n < 0 && errno == EINTR) {
            continue; /* the simulator delivers its tick as a signal */
        }
        if (n < 0 && (errno == EAGAIN || errno == EWOULDBLOCK)) {
            return true;
        }
        if (n == 0) {
            /* The client has finished sending (`printf ... | nc -U` shuts
             * down its side): answer what it sent, then close. */
            c->eof = true;
            return true;
        }
        if (n < 0) {
            return false;
        }
        for (ssize_t i = 0; i < n; i++) {
            char b = buf[i];
            if (c->discarding) {
                c->discarding = b != '\n';
                continue;
            }
            if (b != '\n' && c->partial >= LINE_CAP) {
                /* The line being received is over the cap: refuse it, keep serving (requirement 5.8). */
                c->in_len -= c->partial;
                c->partial = 0;
                c->discarding = true;
                reply_error(c, NULL, RPC_INVALID_REQUEST, "request line longer than 65536 bytes refused");
                continue;
            }
            if (c->in_len == c->in_cap) {
                size_t cap = c->in_cap ? c->in_cap * 2 : 4096;
                char *grown = cap <= IN_MAX ? realloc(c->in, cap) : NULL;
                if (!grown) {
                    ESP_LOGW(TAG, "client sent over %d bytes without waiting for answers; closing it", IN_MAX);
                    return false;
                }
                c->in = grown;
                c->in_cap = cap;
            }
            c->in[c->in_len++] = b;
            c->partial = b == '\n' ? 0 : c->partial + 1;
        }
        if ((size_t)n < sizeof buf) {
            return true;
        }
    }
}

/* Dispatches the first complete line, if any. */
static void next_request(rpc_client_t *c)
{
    char *nl = c->in ? memchr(c->in, '\n', c->in_len) : NULL;
    if (!nl) {
        return;
    }
    size_t len = (size_t)(nl - c->in);
    /* Skip blank lines quietly. */
    bool blank = true;
    for (size_t i = 0; i < len; i++) {
        blank &= c->in[i] == ' ' || c->in[i] == '\r' || c->in[i] == '\t';
    }
    if (!blank) {
        dispatch(c, c->in, len);
    }
    memmove(c->in, nl + 1, c->in_len - len - 1);
    c->in_len -= len + 1;
}

#ifndef MSG_NOSIGNAL
#define MSG_NOSIGNAL 0 /* macOS: the socket has SO_NOSIGPIPE instead */
#endif

/* Sends what it can. Returns false when the client has gone: a write to a
 * closed socket must not raise SIGPIPE, which would end the instance. */
static bool write_client(rpc_client_t *c)
{
    while (c->out_off < c->out_len) {
        ssize_t n = send(c->fd, c->out + c->out_off, c->out_len - c->out_off, MSG_NOSIGNAL);
        if (n < 0 && errno == EINTR) {
            continue;
        }
        if (n < 0 && (errno == EAGAIN || errno == EWOULDBLOCK)) {
            return true; /* the rest goes on a later loop */
        }
        if (n <= 0) {
            return false;
        }
        c->out_off += (size_t)n;
    }
    return true;
}

static void accept_clients(void)
{
    for (;;) {
        int fd = accept(s_listen, NULL, NULL);
        if (fd < 0) {
            if (errno == EINTR) {
                continue;
            }
            return;
        }
        fcntl(fd, F_SETFL, fcntl(fd, F_GETFL) | O_NONBLOCK);
        fcntl(fd, F_SETFD, FD_CLOEXEC);
#ifdef SO_NOSIGPIPE
        int one = 1;
        setsockopt(fd, SOL_SOCKET, SO_NOSIGPIPE, &one, sizeof one);
#endif
        rpc_client_t *slot = NULL;
        for (int i = 0; i < MAX_CLIENTS; i++) {
            if (s_clients[i].fd < 0) {
                slot = &s_clients[i];
                break;
            }
        }
        if (!slot) {
            const char *busy = "{\"jsonrpc\":\"2.0\",\"id\":null,\"error\":{\"code\":1,\"message\":\"too many clients (8)\"}}\n";
            ssize_t w = send(fd, busy, strlen(busy), MSG_NOSIGNAL);
            (void)w;
            close(fd);
            continue;
        }
        memset(slot, 0, sizeof *slot);
        slot->fd = fd;
        slot->serial_cur = emu_serial_next();
    }
}

static void serve_once(int timeout_ms)
{
    struct pollfd pfd[MAX_CLIENTS + 1];
    int idx[MAX_CLIENTS + 1];
    int n = 0;
    pfd[n] = (struct pollfd){.fd = s_listen, .events = POLLIN};
    idx[n++] = -1;
    for (int i = 0; i < MAX_CLIENTS; i++) {
        rpc_client_t *c = &s_clients[i];
        if (c->fd < 0) {
            continue;
        }
        pfd[n] = (struct pollfd){.fd = c->fd, .events = (short)((c->eof ? 0 : POLLIN) | (c->out_off < c->out_len ? POLLOUT : 0))};
        idx[n++] = i;
    }
    int r = poll(pfd, (nfds_t)n, timeout_ms);
    if (r < 0 && errno != EINTR) {
        ESP_LOGE(TAG, "poll: %s", strerror(errno));
    }
    if (r > 0) {
        for (int k = 0; k < n; k++) {
            if (idx[k] < 0) {
                if (pfd[k].revents & POLLIN) {
                    accept_clients();
                }
                continue;
            }
            rpc_client_t *c = &s_clients[idx[k]];
            if (pfd[k].revents & POLLIN && !read_client(c)) {
                close_client(c);
                continue;
            }
            if (pfd[k].revents & (POLLERR | POLLNVAL) || (pfd[k].revents & POLLHUP && !(pfd[k].revents & POLLIN))) {
                close_client(c);
            }
        }
    }
    for (int i = 0; i < MAX_CLIENTS; i++) {
        rpc_client_t *c = &s_clients[i];
        if (c->fd < 0) {
            continue;
        }
        if (c->out_off < c->out_len && !write_client(c)) {
            close_client(c);
            continue;
        }
        if (c->out_off < c->out_len) {
            continue; /* finish one answer before starting the next request */
        }
        if (c->exit_after_reply) {
            emu_exit(c->exit_after_reply);
        }
        if (c->pending) {
            poll_pending(c);
        } else {
            next_request(c);
        }
        if (c->eof && !c->pending && c->out_off >= c->out_len && !(c->in && memchr(c->in, '\n', c->in_len))) {
            close_client(c); /* everything it sent is answered */
        }
    }
    emu_methods_tick();
}

static void control_task(void *arg)
{
    (void)arg;
    for (;;) {
        serve_once(0);
        vTaskDelay(1);
    }
}

static bool open_socket(void)
{
    const char *dir = emu_instance_dir();
    if (!dir) {
        ESP_LOGW(TAG, "LCD349_INSTANCE_DIR isn't set: no control socket (run instances with lcd349 run)");
        return false;
    }
    struct sockaddr_un addr = {.sun_family = AF_UNIX};
    int len = snprintf(addr.sun_path, sizeof addr.sun_path, "%s/control.sock", dir);
    if (len < 0 || (size_t)len >= sizeof addr.sun_path) {
        ESP_LOGE(TAG, "%s/control.sock is longer than a Unix socket path allows (%u bytes); use a shorter instance directory",
                 dir, (unsigned)sizeof addr.sun_path - 1);
        return false;
    }
    chmod(dir, 0700);
    unlink(addr.sun_path);
    s_listen = socket(AF_UNIX, SOCK_STREAM, 0);
    if (s_listen < 0) {
        return false;
    }
    mode_t old = umask(0077);
    int ok = bind(s_listen, (struct sockaddr *)&addr, sizeof addr);
    umask(old);
    if (ok != 0 || chmod(addr.sun_path, 0600) != 0 || listen(s_listen, 8) != 0) {
        ESP_LOGE(TAG, "can't serve %s: %s", addr.sun_path, strerror(errno));
        close(s_listen);
        s_listen = -1;
        return false;
    }
    fcntl(s_listen, F_SETFL, fcntl(s_listen, F_GETFL) | O_NONBLOCK);
    fcntl(s_listen, F_SETFD, FD_CLOEXEC);
    for (int i = 0; i < MAX_CLIENTS; i++) {
        s_clients[i].fd = -1;
    }
    return true;
}

void emu_control_start(void)
{
    if (!open_socket()) {
        return;
    }
    if (xTaskCreate(control_task, "lcd349_ctl", 32768, NULL, LCD349_PRIO_EMU, NULL) != pdPASS) {
        ESP_LOGE(TAG, "can't start the control task");
    }
}

/* Download mode: no FreeRTOS, so the same loop blocks in poll() instead. */
void emu_control_serve_download(void)
{
    s_download = true;
    if (!open_socket()) {
        for (;;) {
            pause();
        }
    }
    for (;;) {
        serve_once(20);
    }
}

void emu_exit(int code)
{
    if (code == LCD349_EXIT_OFF) {
        emu_set_power(EMU_POWER_OFF);
    }
    printf("lcd349 emulator: %s\n", code == LCD349_EXIT_OFF ? "power off" : "reset");
    exit(code);
}
