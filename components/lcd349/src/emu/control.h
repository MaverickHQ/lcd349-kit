/* SPDX-License-Identifier: Apache-2.0
 *
 * Internal to the control server: JSON-RPC 2.0, one message per line, over
 * <instance>/control.sock. control.c is the transport; control_methods.c has
 * the methods.
 */
#pragma once

#include <stdbool.h>
#include <stdint.h>

#include "cJSON.h"
#include "emu.h"

#define RPC_PARSE_ERROR -32700
#define RPC_INVALID_REQUEST -32600
#define RPC_METHOD_NOT_FOUND -32601
#define RPC_INVALID_PARAMS -32602
#define RPC_INTERNAL -32603
#define RPC_UNAVAILABLE 1 /* not possible in the device's current state */

typedef struct rpc_client rpc_client_t;

typedef enum { RPC_DONE, RPC_PENDING, RPC_FAIL } rpc_res_t;

typedef struct {
    int code;
    char msg[256];
} rpc_err_t;

typedef rpc_res_t (*rpc_handler_t)(rpc_client_t *c, const cJSON *params, cJSON **result, rpc_err_t *err);
/* A pending request is polled every loop until it finishes. */
typedef rpc_res_t (*rpc_poll_t)(rpc_client_t *c, cJSON **result, rpc_err_t *err);

struct rpc_client {
    int fd;
    char *in;
    size_t in_len, in_cap;
    size_t partial;  /* bytes after the last newline in `in`: the line being received */
    bool discarding; /* dropping an over-long line until its newline */
    bool eof;        /* the client finished sending: answer what it sent, then close */
    char *out;
    size_t out_len, out_off;
    /* A pending request. */
    cJSON *pending_id; /* NULL for a notification */
    bool pending;
    rpc_poll_t poll;
    void *op; /* the method's state; freed with free() when the request ends */
    /* Serial line numbers at this request and the previous one, so `wait`
     * also sees lines logged while the previous request (a press) ran. */
    uint64_t serial_prev, serial_cur;
    int exit_after_reply; /* exit code, or 0 */
    uint8_t held;         /* buttons held by input.press {state: "down"}: bit 0 BOOT, bit 1 PWR */
};

typedef struct {
    const char *name;
    rpc_handler_t fn;
    bool in_download_mode; /* also served while BOOT held the device in download mode */
} rpc_method_t;

extern const rpc_method_t emu_rpc_methods[];

rpc_res_t rpc_fail(rpc_err_t *err, int code, const char *fmt, ...) __attribute__((format(printf, 3, 4)));
bool emu_control_in_download(void);
/* Called every loop: recording, header status, darkness. */
void emu_methods_tick(void);
/* Releases anything a pending op holds when its client goes away. */
void emu_methods_cancel(rpc_client_t *c);
/* Releases the buttons a client was holding when it disconnects. */
void emu_methods_client_closed(rpc_client_t *c);
/* Exits the process with code after the current reply is sent. */
void emu_control_exit_after_reply(rpc_client_t *c, int code);
void emu_exit(int code); /* sets the header, flushes serial output, exits */

/* Shared method helpers. */
cJSON *emu_state_json(bool full);
bool emu_display_dark(void);
