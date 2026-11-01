/* SPDX-License-Identifier: Apache-2.0
 *
 * The panel framebuffer as a memory-mapped file, <instance>/fb.bin: a 64-byte
 * header and the panel-native frame exactly as the board's panel receives it.
 * seq is a seqlock: odd while a frame is being copied in. A file rather than
 * shm_open: C, Python and Go can all map it, with ordinary file permissions
 * (spike 0.2). Without an instance directory the frame lives in memory.
 */
#include <errno.h>
#include <fcntl.h>
#include <stdio.h>
#include <string.h>
#include <sys/mman.h>
#include <unistd.h>

#include "emu.h"
#include "freertos/FreeRTOS.h"
#include "freertos/task.h"

#define FB_FILE_BYTES (sizeof(emu_fb_hdr_t) + EMU_PANEL_BYTES)

static uint8_t s_mem[FB_FILE_BYTES] __attribute__((aligned(8)));
static uint8_t *s_fb = s_mem;

emu_fb_hdr_t *emu_fb_hdr(void)
{
    return (emu_fb_hdr_t *)s_fb;
}

static uint8_t *pixels(void)
{
    return s_fb + sizeof(emu_fb_hdr_t);
}

bool emu_fb_open(void)
{
    const char *dir = emu_instance_dir();
    if (dir) {
        char path[512];
        snprintf(path, sizeof path, "%s/fb.bin", dir);
        /* Kept across resets (the viewer maps it), but never a symlink. */
        int fd = open(path, O_RDWR | O_CREAT | O_NOFOLLOW | O_CLOEXEC, 0600);
        if (fd >= 0 && ftruncate(fd, (off_t)FB_FILE_BYTES) == 0) {
            void *m = mmap(NULL, FB_FILE_BYTES, PROT_READ | PROT_WRITE, MAP_SHARED, fd, 0);
            if (m != MAP_FAILED) {
                s_fb = m;
            }
        }
        if (fd >= 0) {
            close(fd);
        }
        if (s_fb == s_mem) {
            fprintf(stderr, "lcd349_emu: can't map %s (%s); the framebuffer stays in memory\n", path, strerror(errno));
        }
    }
    emu_fb_hdr_t *h = emu_fb_hdr();
    memset(h, 0, sizeof *h);
    memset(pixels(), 0, EMU_PANEL_BYTES);
    h->version = EMU_FB_VERSION;
    h->width = LCD349_PANEL_W;
    h->height = LCD349_PANEL_H;
    h->pid = (uint32_t)getpid();
    h->rotation = LCD349_ROTATION_CW90 ? 1 : 2;
    h->byte_swap = LCD349_BYTE_SWAP ? 1 : 2;
    /* The magic goes last, so a reader never sees a half-initialised header. */
    __atomic_thread_fence(__ATOMIC_RELEASE);
    memcpy(h->magic, EMU_FB_MAGIC, 8);
    return s_fb != s_mem;
}

void emu_fb_write(const uint8_t *panel, size_t len)
{
    emu_fb_hdr_t *h = emu_fb_hdr();
    if (len > EMU_PANEL_BYTES) {
        len = EMU_PANEL_BYTES;
    }
    __atomic_add_fetch(&h->seq, 1, __ATOMIC_ACQ_REL); /* odd: writing */
    memcpy(pixels(), panel, len);
    h->last_flush_us = lcd349_time_us();
    __atomic_add_fetch(&h->frames, 1, __ATOMIC_RELAXED);
    __atomic_add_fetch(&h->seq, 1, __ATOMIC_ACQ_REL); /* even: stable */
}

uint32_t emu_fb_read(uint8_t *out)
{
    emu_fb_hdr_t *h = emu_fb_hdr();
    for (;;) {
        uint32_t s1 = __atomic_load_n(&h->seq, __ATOMIC_ACQUIRE);
        if (s1 & 1) {
            /* The writer was preempted mid-frame: let it finish. */
            if (xTaskGetSchedulerState() == taskSCHEDULER_RUNNING) {
                vTaskDelay(1);
            }
            continue;
        }
        memcpy(out, pixels(), EMU_PANEL_BYTES);
        __atomic_thread_fence(__ATOMIC_ACQUIRE);
        if (__atomic_load_n(&h->seq, __ATOMIC_ACQUIRE) == s1) {
            return s1;
        }
    }
}

void emu_fb_status_changed(void)
{
    __atomic_add_fetch(&emu_fb_hdr()->status_seq, 1, __ATOMIC_RELEASE);
}

uint64_t emu_fb_idle_us(void)
{
    uint64_t last = emu_fb_hdr()->last_flush_us;
    uint64_t now = lcd349_time_us();
    return now > last ? now - last : 0;
}

const uint8_t *lcd349__emu_last_frame(uint32_t *frames)
{
    if (frames) {
        *frames = __atomic_load_n(&emu_fb_hdr()->frames, __ATOMIC_ACQUIRE);
    }
    return pixels();
}
