/* SPDX-License-Identifier: Apache-2.0
 *
 * Serial capture. stdout and stderr go through a pipe to a reader thread that
 * writes everything on to the real stdout (the instance's pty or serial.log)
 * and keeps the last lines in a ring, so the control server's `wait` can
 * match log lines from both ESP_LOG and plain printf.
 *
 * The console's input side: a second reader takes lines from stdin (the pty
 * `lcd349 run` gives the instance) for lcd349_console_read_line().
 *
 * Both readers are plain pthreads with every signal blocked: the FreeRTOS
 * simulator drives its tick with SIGALRM and must never take it on a thread
 * it doesn't own. They never call FreeRTOS.
 */
#include <errno.h>
#include <fcntl.h>
#include <sys/stat.h>
#include <time.h>
#include <poll.h>
#include <pthread.h>
#include <signal.h>
#include <stdio.h>
#include <stdlib.h>
#include <string.h>
#include <unistd.h>

#include "emu.h"

#define RING_LINES 1024
#define LINE_MAX_LEN 512

static pthread_mutex_t s_mu = PTHREAD_MUTEX_INITIALIZER;
static char s_ring[RING_LINES][LINE_MAX_LEN];
static uint64_t s_next = 1; /* number of the next complete line */
static char s_partial[LINE_MAX_LEN];
static size_t s_partial_len;
static int s_out_fd = -1;  /* the original stdout */
static int s_read_fd = -1;
static pthread_t s_thread;
static bool s_running;

static void push_line_locked(void)
{
    s_partial[s_partial_len] = '\0';
    /* Drop a trailing \r from CRLF output. */
    if (s_partial_len && s_partial[s_partial_len - 1] == '\r') {
        s_partial[s_partial_len - 1] = '\0';
    }
    memcpy(s_ring[s_next % RING_LINES], s_partial, s_partial_len + 1);
    s_next++;
    s_partial_len = 0;
}

static void write_all(int fd, const char *p, size_t n)
{
    while (n) {
        ssize_t w = write(fd, p, n);
        if (w < 0 && errno == EINTR) {
            continue;
        }
        if (w <= 0) {
            return;
        }
        p += w;
        n -= (size_t)w;
    }
}

static volatile sig_atomic_t s_reader_busy; /* holding bytes it has read but not written */

/* Bytes from the product: into the ring first, so `wait` sees a line as soon
 * as it's logged even while the console is slow to take it, then on. */
static void pass_on(const char *buf, size_t n)
{
    pthread_mutex_lock(&s_mu);
    for (size_t i = 0; i < n; i++) {
        if (buf[i] == '\n') {
            push_line_locked();
        } else {
            if (s_partial_len == LINE_MAX_LEN - 1) {
                push_line_locked(); /* split very long lines */
            }
            s_partial[s_partial_len++] = buf[i];
        }
    }
    pthread_mutex_unlock(&s_mu);
    write_all(s_out_fd, buf, n);
}

/* A write that fails leaves stdio's error flag set, and macOS then drops
 * everything printed to that stream, for good. Clear it if it's ever set.
 * Never wait for the stream's lock: a task blocked writing to the pipe may
 * hold it, and only this thread drains the pipe. */
static bool revive_stdio(void)
{
    bool revived = false;
    FILE *streams[] = {stdout, stderr};
    for (size_t i = 0; i < 2; i++) {
        if (ftrylockfile(streams[i]) == 0) {
            if (ferror(streams[i])) {
                clearerr(streams[i]);
                revived = true;
            }
            funlockfile(streams[i]);
        }
    }
    return revived;
}

static void *reader(void *arg)
{
    (void)arg;
    char buf[4096];
    struct pollfd pfd = {.fd = s_read_fd, .events = POLLIN};
    for (;;) {
        if (revive_stdio()) {
            static const char note[] = "lcd349_emu: a console write failed; output restored\n";
            s_reader_busy = 1;
            pass_on(note, sizeof note - 1);
            s_reader_busy = 0;
        }
        int r = poll(&pfd, 1, 200);
        if (r == 0 || (r < 0 && errno == EINTR)) {
            continue;
        }
        ssize_t n = read(s_read_fd, buf, sizeof buf);
        s_reader_busy = 1;
        if (n < 0 && (errno == EINTR || errno == EAGAIN)) {
            s_reader_busy = 0;
            continue;
        }
        if (n <= 0) {
            s_reader_busy = 0;
            break;
        }
        pass_on(buf, (size_t)n);
        s_reader_busy = 0;
    }
    return NULL;
}

/* ---- Console input ---- */

#define IN_LINES 16

static char s_in[IN_LINES][LCD349_CONSOLE_LINE_MAX + 1];
static unsigned s_in_head, s_in_count; /* guarded by s_mu */
static uint32_t s_in_dropped;
static pid_t s_parent;

static void in_push_locked(const char *line, size_t len)
{
    if (s_in_count == IN_LINES) {
        s_in_dropped++; /* a full receive buffer drops what arrives */
        return;
    }
    char *slot = s_in[(s_in_head + s_in_count) % IN_LINES];
    memcpy(slot, line, len);
    slot[len] = '\0';
    s_in_count++;
}

/* The console went away (read gave end of file or EIO). Under `lcd349 run`
 * that means the supervisor has gone, and the instance must not outlive it.
 * A terminal's Ctrl-D also reads as end of file; the parent is still there. */
static bool console_orphaned(void)
{
    return getppid() != s_parent;
}

static void *stdin_reader(void *arg)
{
    (void)arg;
    bool tty = isatty(STDIN_FILENO);
    char buf[512];
    char line[LCD349_CONSOLE_LINE_MAX + 1];
    size_t len = 0;
    bool after_cr = false;
    for (;;) {
        ssize_t n = read(STDIN_FILENO, buf, sizeof buf);
        if (n < 0 && (errno == EINTR || errno == EAGAIN)) {
            if (errno == EAGAIN) {
                usleep(10000);
            }
            continue;
        }
        if (n <= 0) {
            if (!tty) {
                return NULL; /* a file or /dev/null: nothing more will come */
            }
            if (console_orphaned()) {
                _exit(LCD349_EXIT_OFF);
            }
            usleep(50000); /* Ctrl-D, or the hang-up before the parent is gone */
            continue;
        }
        pthread_mutex_lock(&s_mu);
        for (ssize_t i = 0; i < n; i++) {
            char ch = buf[i];
            if (ch == '\n' && after_cr) {
                after_cr = false; /* the LF of a CRLF */
                continue;
            }
            after_cr = ch == '\r';
            if (ch == '\r' || ch == '\n') {
                in_push_locked(line, len);
                len = 0;
            } else if (len < LCD349_CONSOLE_LINE_MAX) {
                line[len++] = ch; /* the rest of an overlong line is dropped */
            }
        }
        pthread_mutex_unlock(&s_mu);
    }
}

static void start_stdin_reader(void)
{
    struct stat st;
    if (fstat(STDIN_FILENO, &st) != 0) {
        return;
    }
    s_parent = getppid();
    pthread_t t;
    sigset_t all, old;
    sigfillset(&all);
    pthread_sigmask(SIG_BLOCK, &all, &old);
    if (pthread_create(&t, NULL, stdin_reader, NULL) == 0) {
        pthread_detach(t);
    }
    pthread_sigmask(SIG_SETMASK, &old, NULL);
}

/* Called from FreeRTOS tasks: takes the next line, if any, without waiting. */
bool emu_console_take(char *out, size_t size)
{
    bool ok = false;
    emu_crit_enter();
    pthread_mutex_lock(&s_mu);
    if (s_in_count) {
        const char *line = s_in[s_in_head];
        size_t len = strnlen(line, LCD349_CONSOLE_LINE_MAX);
        if (len >= size) {
            len = size - 1;
        }
        memcpy(out, line, len);
        out[len] = '\0';
        s_in_head = (s_in_head + 1) % IN_LINES;
        s_in_count--;
        ok = true;
    }
    pthread_mutex_unlock(&s_mu);
    emu_crit_exit();
    return ok;
}

/* At exit: flush, restore the real stdout (closing the pipe's last writer),
 * and let the reader pass on everything still in the pipe. */
static void shutdown_capture(void)
{
    if (!s_running) {
        return;
    }
    s_running = false;
    fflush(stdout);
    fflush(stderr);
    dup2(s_out_fd, STDOUT_FILENO);
    dup2(s_out_fd, STDERR_FILENO);
    pthread_join(s_thread, NULL);
}

void emu_serial_init(void)
{
    if (s_running) {
        return;
    }
    start_stdin_reader();
    int p[2];
    fflush(stdout);
    fflush(stderr);
    if (pipe(p) != 0) {
        return;
    }
    s_out_fd = dup(STDOUT_FILENO);
    s_read_fd = p[0];
    if (s_out_fd < 0 || dup2(p[1], STDOUT_FILENO) < 0 || dup2(p[1], STDERR_FILENO) < 0) {
        close(p[0]);
        close(p[1]);
        return;
    }
    close(p[1]);
    /* Line-buffer stdout so every line reaches the pipe promptly, and
     * serial.log keeps it if the instance is killed (spike 0.2). */
    setvbuf(stdout, NULL, _IOLBF, 0);
    setvbuf(stderr, NULL, _IONBF, 0);

    sigset_t all, old;
    sigfillset(&all);
    pthread_sigmask(SIG_BLOCK, &all, &old);
    int err = pthread_create(&s_thread, NULL, reader, NULL);
    pthread_sigmask(SIG_SETMASK, &old, NULL);
    if (err != 0) {
        dup2(s_out_fd, STDOUT_FILENO);
        dup2(s_out_fd, STDERR_FILENO);
        return;
    }
    s_running = true;
    atexit(shutdown_capture);
}

/* ---- Crash output ----
 * ESP-IDF's linux main() catches SIGABRT and SIGSEGV, prints a message and
 * calls _exit(1), which skips shutdown_capture(): whatever is still in the
 * pipe, the product's last lines included, would die with the process. So,
 * once main() has installed those handlers, the kit puts its own first: it
 * points stdout and stderr straight at the console, sends on what the pipe
 * still holds, then runs ESP-IDF's handler. Only async-signal-safe calls. */

static const int FATAL_SIGNALS[] = {SIGABRT, SIGSEGV, SIGBUS};
#define N_FATAL (sizeof FATAL_SIGNALS / sizeof FATAL_SIGNALS[0])
static struct sigaction s_prev_fatal[N_FATAL];

static void fatal_drain(int sig)
{
    if (s_running) {
        dup2(s_out_fd, STDOUT_FILENO);
        dup2(s_out_fd, STDERR_FILENO);
        /* Let the reader finish a chunk it holds, so lines stay in order. */
        for (int i = 0; i < 100 && s_reader_busy; i++) {
            struct timespec ts = {0, 1000000};
            nanosleep(&ts, NULL);
        }
        fcntl(s_read_fd, F_SETFL, fcntl(s_read_fd, F_GETFL) | O_NONBLOCK);
        char buf[4096];
        ssize_t n;
        while ((n = read(s_read_fd, buf, sizeof buf)) > 0) {
            write_all(s_out_fd, buf, (size_t)n);
        }
    }
    for (size_t i = 0; i < N_FATAL; i++) {
        if (FATAL_SIGNALS[i] != sig) {
            continue;
        }
        const struct sigaction *prev = &s_prev_fatal[i];
        if (!(prev->sa_flags & SA_SIGINFO) && prev->sa_handler != SIG_DFL && prev->sa_handler != SIG_IGN) {
            prev->sa_handler(sig); /* ESP-IDF's: prints and exits */
        }
        signal(sig, SIG_DFL);
        raise(sig); /* delivered when this handler returns */
    }
}

/* The FreeRTOS port installs its tick handler (SIGALRM) without SA_RESTART,
 * so a task blocked in a system call when the tick lands gets EINTR. A write
 * to a full console pipe then fails, and stdio drops everything printed
 * after it: the instance carries on with a silent log (found by the KN9
 * soak). The board never interrupts a write, so restart them. Called once
 * the scheduler runs, after the port has installed the handler. */
void emu_serial_restart_interrupted_calls(void)
{
    struct sigaction sa;
    if (sigaction(SIGALRM, NULL, &sa) == 0 && sa.sa_handler != SIG_DFL && sa.sa_handler != SIG_IGN) {
        sa.sa_flags |= SA_RESTART;
        sigaction(SIGALRM, &sa, NULL);
    }
}

void emu_serial_catch_crashes(void)
{
    struct sigaction sa;
    memset(&sa, 0, sizeof sa);
    sa.sa_handler = fatal_drain;
    sigemptyset(&sa.sa_mask);
    for (size_t i = 0; i < N_FATAL; i++) {
        sigaction(FATAL_SIGNALS[i], &sa, &s_prev_fatal[i]);
    }
}

/* Called from FreeRTOS tasks: the critical section keeps the tick from
 * suspending the task while it holds the reader's mutex. */
uint64_t emu_serial_next(void)
{
    emu_crit_enter();
    pthread_mutex_lock(&s_mu);
    uint64_t n = s_next;
    pthread_mutex_unlock(&s_mu);
    emu_crit_exit();
    return n;
}

bool emu_serial_line(uint64_t n, char *out, size_t out_len)
{
    bool ok = false;
    emu_crit_enter();
    pthread_mutex_lock(&s_mu);
    if (n >= 1 && n < s_next && s_next - n <= RING_LINES) {
        size_t len = strnlen(s_ring[n % RING_LINES], LINE_MAX_LEN);
        if (len >= out_len) {
            len = out_len - 1;
        }
        memcpy(out, s_ring[n % RING_LINES], len);
        out[len] = '\0';
        ok = true;
    }
    pthread_mutex_unlock(&s_mu);
    emu_crit_exit();
    return ok;
}
