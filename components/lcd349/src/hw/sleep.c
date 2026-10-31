/* SPDX-License-Identifier: Apache-2.0
 *
 * Light sleep (requirement 4.2). While lcd349_sleep_until_button waits, esp_pm
 * puts the chip into light sleep whenever every task is idle (tickless idle).
 * Light sleep is allowed only for the call (design D1-11). The chip wakes on
 * its timer for the next task timeout, the call's own included, and on BOOT
 * or PWR. Wi-Fi keeps its association through modem power save (wifi.c).
 *
 * A pin wakes light sleep only on a level, but buttons.c runs BOOT and PWR on
 * edge interrupts, and a level interrupt on its handler would fire for as long
 * as a button is held. So for the call the pins' interrupts are off, and each
 * pin wakes the chip on the level it hasn't got: a press, or the release of a
 * button held at the call. An idle hook, which runs after every wake before
 * the chip can sleep again, follows the levels, moves each pin's wake level
 * and reports a press. Afterwards the pins go back to edge interrupts with one
 * interrupt pending, so buttons.c's task reads them again and the waking press
 * arrives as a normal button event.
 */
#include "hw.h"

#if CONFIG_LCD349_LIGHT_SLEEP

#include "freertos/FreeRTOS.h"
#include "freertos/semphr.h"

#include "driver/gpio.h"
#include "esp_freertos_hooks.h"
#include "esp_log.h"
#include "esp_pm.h"
#include "esp_sleep.h"

static const char *TAG = "lcd349_sleep";
static const gpio_num_t s_pin[LCD349_BTN_COUNT] = {
    [LCD349_BTN_BOOT] = LCD349_PIN_BOOT,
    [LCD349_BTN_PWR] = LCD349_PIN_PWR,
};
static bool s_busy;              /* a call is in progress */
static SemaphoreHandle_t s_wake; /* given by the idle hook on a press */

/* The idle hook's state, under s_lock. */
static portMUX_TYPE s_lock = portMUX_INITIALIZER_UNLOCKED;
static bool s_armed;
static bool s_down[LCD349_BTN_COUNT];    /* the level each pin's wake waits to leave */
static bool s_seen_up[LCD349_BTN_COUNT]; /* released at some time during the call */
static int s_woke_by;                    /* -1 until a press */

/* Wakes the chip when the pin leaves the level it has now. */
static void wake_on_change(int b, bool down)
{
    s_down[b] = down;
    gpio_wakeup_enable(s_pin[b], down ? GPIO_INTR_HIGH_LEVEL : GPIO_INTR_LOW_LEVEL);
}

/* Runs on each core's idle task, after every wake and before the chip can
 * sleep again. Returns true: the core may idle. */
static bool idle_hook(void)
{
    if (!__atomic_load_n(&s_armed, __ATOMIC_ACQUIRE)) {
        return true;
    }
    bool pressed = false;
    portENTER_CRITICAL(&s_lock);
    for (int b = 0; s_armed && b < LCD349_BTN_COUNT; b++) {
        bool down = gpio_get_level(s_pin[b]) == 0;
        if (down == s_down[b]) {
            continue;
        }
        wake_on_change(b, down);
        if (!down) {
            s_seen_up[b] = true;
        } else if (s_seen_up[b] && s_woke_by < 0) {
            s_woke_by = b;
            pressed = true;
        }
    }
    portEXIT_CRITICAL(&s_lock);
    if (pressed) {
        xSemaphoreGive(s_wake);
    }
    return true;
}

static lcd349_err_t hooks(bool on)
{
    for (UBaseType_t core = 0; core < portNUM_PROCESSORS; core++) {
        if (!on) {
            esp_deregister_freertos_idle_hook_for_cpu(idle_hook, core);
        } else if (esp_register_freertos_idle_hook_for_cpu(idle_hook, core) != ESP_OK) {
            hooks(false);
            return LCD349_ERR_NO_MEM;
        }
    }
    return LCD349_OK;
}

/* Takes the pins off their interrupts and arms their wakes. */
static void arm(void)
{
    portENTER_CRITICAL(&s_lock);
    for (int b = 0; b < LCD349_BTN_COUNT; b++) {
        gpio_intr_disable(s_pin[b]);
        bool down = gpio_get_level(s_pin[b]) == 0;
        s_seen_up[b] = !down;
        wake_on_change(b, down);
    }
    s_woke_by = -1;
    __atomic_store_n(&s_armed, true, __ATOMIC_RELEASE);
    portEXIT_CRITICAL(&s_lock);
}

/* Gives the pins back to buttons.c on edge interrupts, each with one
 * interrupt pending: a level interrupt for the level the pin has latches at
 * once and stays pending after the switch back to edges. Returns the button
 * that woke the call, or -1. */
static int disarm(void)
{
    portENTER_CRITICAL(&s_lock);
    __atomic_store_n(&s_armed, false, __ATOMIC_RELEASE);
    int woke_by = s_woke_by;
    for (int b = 0; b < LCD349_BTN_COUNT; b++) {
        gpio_wakeup_disable(s_pin[b]);
        gpio_set_intr_type(s_pin[b], gpio_get_level(s_pin[b]) ? GPIO_INTR_HIGH_LEVEL : GPIO_INTR_LOW_LEVEL);
        gpio_intr_enable(s_pin[b]);
        gpio_set_intr_type(s_pin[b], GPIO_INTR_ANYEDGE);
    }
    portEXIT_CRITICAL(&s_lock);
    return woke_by;
}

/* Waits for a press, or until the timeout (0: none) has passed on the kit's
 * clock. Across light sleeps FreeRTOS's tick count runs ahead of that clock
 * (on the board a 2001-tick wait returned after 1.87 s), so the wait is
 * measured on the clock and only bounded by ticks. */
static void wait_for_press(uint32_t timeout_ms)
{
    if (!timeout_ms) {
        xSemaphoreTake(s_wake, portMAX_DELAY);
        return;
    }
    uint64_t until = be_time_us() + (uint64_t)timeout_ms * 1000u;
    for (uint64_t now; (now = be_time_us()) < until;) {
        if (xSemaphoreTake(s_wake, pdMS_TO_TICKS((uint32_t)((until - now + 999) / 1000)) + 1) == pdTRUE) {
            return;
        }
    }
}

static lcd349_err_t sleep_until_button(uint32_t timeout_ms, lcd349_btn_t *woke_by)
{
    if (!s_wake && !(s_wake = xSemaphoreCreateBinary())) {
        return LCD349_ERR_NO_MEM;
    }
    lcd349_err_t err = hooks(true);
    if (err != LCD349_OK) {
        return err;
    }
    xSemaphoreTake(s_wake, 0); /* drop a press that came after the last call ended */
    arm();
    esp_sleep_enable_gpio_wakeup();
    esp_pm_config_t was, pm;
    esp_pm_get_configuration(&was);
    pm = was;
    pm.light_sleep_enable = true;
    esp_err_t e = esp_pm_configure(&pm);
    if (e == ESP_OK) {
        wait_for_press(timeout_ms);
    } else {
        ESP_LOGE(TAG, "esp_pm_configure: %s", esp_err_to_name(e));
    }
    esp_pm_configure(&was); /* a refusal may have got part way */
    esp_sleep_disable_wakeup_source(ESP_SLEEP_WAKEUP_GPIO);
    int b = disarm();
    hooks(false);
    if (e != ESP_OK) {
        return LCD349_ERR_UNAVAILABLE;
    }
    if (b < 0) {
        return LCD349_ERR_TIMEOUT;
    }
    if (woke_by) {
        *woke_by = (lcd349_btn_t)b;
    }
    return LCD349_OK;
}

lcd349_err_t be_sleep_until_button(uint32_t timeout_ms, lcd349_btn_t *woke_by)
{
    if (__atomic_exchange_n(&s_busy, true, __ATOMIC_ACQUIRE)) {
        return LCD349_ERR_STATE; /* another task is in the call */
    }
    lcd349_err_t err = sleep_until_button(timeout_ms, woke_by);
    __atomic_store_n(&s_busy, false, __ATOMIC_RELEASE);
    return err;
}

#else

/* LCD349_LIGHT_SLEEP is off: no power management or tickless idle (D1-11). */
lcd349_err_t be_sleep_until_button(uint32_t timeout_ms, lcd349_btn_t *woke_by)
{
    (void)timeout_ms;
    (void)woke_by;
    return LCD349_ERR_UNAVAILABLE;
}

#endif
