/* SPDX-License-Identifier: Apache-2.0
 * Light sleep on the board (seam S1-4). The net lane adds its tests here,
 * each TEST_CASE tagged "[sleep]". The USB link drops while the board sleeps,
 * so keep results in RTC memory and check them in a later stage
 * (TEST_CASE_MULTIPLE_STAGES).
 *
 * After light sleep the host's USB driver may have given up on the board
 * (ESP-IDF's USB Serial/JTAG console guide), and a restart doesn't make the
 * host look again: a disconnect does. So the sleeping stage ends in a short
 * deep sleep, which takes the USB pull-up away and boots the board with the
 * link back. In case it never gets there, a guard task does the same 12 s
 * after the sleep began, and the RTC watchdog resets the chip 20 s after,
 * counting through light sleep. The record says how far the stage got.
 */
#include <stdio.h>
#include <string.h>

#include "esp_attr.h"
#include "esp_log.h"
#include "esp_sleep.h"
#include "esp_system.h"
#include "freertos/FreeRTOS.h"
#include "freertos/task.h"
#include "hal/wdt_hal.h"
#include "lcd349.h"
#include "lcd349_harness.h"
#include "soc/rtc.h"
#include "soc/rtc_cntl_reg.h"
#include "unity.h"
#include "unity_test_runner.h"

#define SLEEP_MARK 0x534C5032u /* "SLP2" */
#define SLEEP_MS 2000
#define GUARD_MS 12000     /* the guard deep-sleeps the board if the stage hasn't by then */
#define RTC_WDT_MS 20000   /* and the RTC watchdog resets it if nothing runs at all */
#define REBOOT_US 500000   /* the deep sleep that ends the stage */

/* How far the sleeping stage got. */
enum { STEP_STARTED, STEP_JOINED, STEP_GUARDED, STEP_IN_CALL, STEP_RETURNED, STEP_DONE };
static const char *const s_step[] = {"started", "joined", "guarded", "in the call", "returned", "done"};

/* What the sleeping stage saw, kept through the USB drop and the deep sleep. */
typedef struct {
    uint32_t mark;
    uint32_t step;
    int32_t err;        /* lcd349_sleep_until_button's result */
    int32_t cause;      /* esp_sleep_get_wakeup_cause() after it */
    int32_t woke_by;    /* -1: not set */
    uint64_t before_us; /* lcd349_time_us() around the call */
    uint64_t after_us;
    int32_t state_before, state_after; /* Wi-Fi states */
    uint32_t ip_before, ip_after;
    uint64_t guard_us;   /* when the guard fired, 0 if it didn't */
    uint32_t guard_step; /* the step it found */
    int32_t guard_cause; /* the last wake cause it saw */
} sleep_record_t;

static RTC_NOINIT_ATTR sleep_record_t s_rec;

static bool wait_connected(uint32_t timeout_ms, lcd349_wifi_status_t *st)
{
    uint32_t t0 = lcd349_time_ms();
    do {
        if (lcd349_wifi_status(st) == LCD349_OK && st->state == LCD349_WIFI_CONNECTED) {
            return true;
        }
        vTaskDelay(pdMS_TO_TICKS(100));
    } while (lcd349_time_ms() - t0 < timeout_ms);
    return false;
}

static void print_record(const sleep_record_t *r)
{
    printf("sleep: stage 1 reached \"%s\"; %s after %llu us, wake cause %d, woke_by %d, Wi-Fi state %d -> %d, address %s\n",
           r->step < sizeof s_step / sizeof s_step[0] ? s_step[r->step] : "?", lcd349_err_str((lcd349_err_t)r->err),
           (unsigned long long)(r->after_us - r->before_us), (int)r->cause, (int)r->woke_by, (int)r->state_before,
           (int)r->state_after, r->ip_after == r->ip_before ? "kept" : "changed");
    if (r->guard_us) {
        printf("sleep: the guard fired %llu us after the call began, at \"%s\", last wake cause %d\n",
               (unsigned long long)(r->guard_us - r->before_us),
               r->guard_step < sizeof s_step / sizeof s_step[0] ? s_step[r->guard_step] : "?", (int)r->guard_cause);
    }
}

/* Ends the stage: a short deep sleep drops the USB link, and the board boots
 * again with the host finding it. */
static void reboot_through_deep_sleep(void)
{
    esp_sleep_disable_wakeup_source(ESP_SLEEP_WAKEUP_ALL);
    esp_deep_sleep(REBOOT_US);
}

static void guard_task(void *arg)
{
    (void)arg;
    vTaskDelay(pdMS_TO_TICKS(GUARD_MS));
    s_rec.guard_us = lcd349_time_us();
    s_rec.guard_step = s_rec.step;
    s_rec.guard_cause = (int32_t)esp_sleep_get_wakeup_cause();
    reboot_through_deep_sleep();
}

/* The RTC watchdog resets the whole chip if the stage stalls with nothing
 * running, asleep or awake: its pause in sleep is turned off. */
static void arm_rtc_watchdog(void)
{
    wdt_hal_context_t rwdt = RWDT_HAL_CONTEXT_DEFAULT();
    wdt_hal_init(&rwdt, WDT_RWDT, 0, false);
    wdt_hal_write_protect_disable(&rwdt);
    wdt_hal_config_stage(&rwdt, WDT_STAGE0, (uint32_t)((uint64_t)RTC_WDT_MS * rtc_clk_slow_freq_get_hz() / 1000u),
                         WDT_STAGE_ACTION_RESET_RTC);
    wdt_hal_enable(&rwdt);
    REG_CLR_BIT(RTC_CNTL_WDTCONFIG0_REG, RTC_CNTL_WDT_PAUSE_IN_SLP);
    wdt_hal_write_protect_enable(&rwdt);
}

/* Stage 1: join the test network, sleep until a timer 2 s later, keep what
 * happened in RTC memory, and reboot through a deep sleep so stage 2 reports
 * it over a fresh USB link. Nothing names the network (design D1-6). */
static void sleep_on_timer(void)
{
    if (s_rec.mark == SLEEP_MARK) {
        printf("sleep: an earlier run's stage 1 never reached stage 2:\n");
        print_record(&s_rec);
    }
    memset(&s_rec, 0, sizeof s_rec);
    s_rec.mark = SLEEP_MARK;
    esp_log_level_set("wifi", ESP_LOG_WARN);
    lcd349_wifi_net_t net = {0};
    TEST_ASSERT_TRUE_MESSAGE(lcd349_harness_wifi(1, net.ssid, sizeof net.ssid, net.password, sizeof net.password),
                             "the host sent no test network");
    lcd349_err_t set = lcd349_wifi_set_networks(&net, 1);
    memset(&net, 0, sizeof net);
    TEST_ASSERT_EQUAL_STRING("LCD349_OK", lcd349_err_str(set));
    TEST_ASSERT_EQUAL_STRING("LCD349_OK", lcd349_err_str(lcd349_wifi_start()));
    lcd349_wifi_status_t st;
    TEST_ASSERT_TRUE_MESSAGE(wait_connected(45000, &st), "not connected within 45 s (see test_wifi_joins_priority_network)");
    s_rec.state_before = st.state;
    s_rec.ip_before = st.ip4;
    s_rec.step = STEP_JOINED;

    /* From here the stage ends only in a deep sleep or a reset. */
    printf("sleep: for %d ms on a timer; the USB link may drop\n", SLEEP_MS);
    fflush(stdout);
    vTaskDelay(pdMS_TO_TICKS(100)); /* let the line out */
    xTaskCreate(guard_task, "sleep_guard", 4096, NULL, configMAX_PRIORITIES - 1, NULL);
    arm_rtc_watchdog();
    s_rec.step = STEP_GUARDED;

    lcd349_btn_t btn = (lcd349_btn_t)-1;
    s_rec.step = STEP_IN_CALL;
    s_rec.before_us = lcd349_time_us();
    s_rec.err = lcd349_sleep_until_button(SLEEP_MS, &btn);
    s_rec.after_us = lcd349_time_us();
    s_rec.cause = (int32_t)esp_sleep_get_wakeup_cause();
    s_rec.step = STEP_RETURNED;
    s_rec.woke_by = (int32_t)btn;
    lcd349_wifi_status(&st);
    s_rec.state_after = st.state;
    s_rec.ip_after = st.ip4;
    s_rec.step = STEP_DONE;
    reboot_through_deep_sleep();
}

/* Stage 2: what stage 1 recorded. */
static void check_timer_wake(void)
{
    sleep_record_t r = s_rec;
    s_rec.mark = 0;
    TEST_ASSERT_EQUAL_HEX32_MESSAGE(SLEEP_MARK, r.mark, "no record from the sleeping stage");
    print_record(&r);
    TEST_ASSERT_EQUAL_UINT32_MESSAGE(STEP_DONE, r.step, "the sleeping stage didn't finish");
    TEST_ASSERT_TRUE_MESSAGE(r.guard_us == 0, "the guard had to end the sleeping stage");
    /* Requirement 4.2: a timer wake is the call's timeout. */
    TEST_ASSERT_EQUAL_STRING("LCD349_ERR_TIMEOUT", lcd349_err_str((lcd349_err_t)r.err));
    TEST_ASSERT_EQUAL_INT_MESSAGE(ESP_SLEEP_WAKEUP_TIMER, r.cause, "the chip didn't wake from light sleep on its timer");
    TEST_ASSERT_GREATER_OR_EQUAL_UINT32(SLEEP_MS * 1000u, (uint32_t)(r.after_us - r.before_us));
    /* Requirement 4.3: the association survives the sleep. */
    TEST_ASSERT_EQUAL_INT_MESSAGE(LCD349_WIFI_CONNECTED, r.state_after, "Wi-Fi isn't connected after the sleep");
    TEST_ASSERT_EQUAL_HEX32(r.ip_before, r.ip_after);
}

TEST_CASE_MULTIPLE_STAGES("test_light_sleep_timer_wake_keeps_wifi", "[sleep]", sleep_on_timer, check_timer_wake);
