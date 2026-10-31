/* SPDX-License-Identifier: Apache-2.0
 *
 * lcd349: board SDK for the Waveshare ESP32-S3-Touch-LCD-3.49.
 *
 * Include this one header. Products use LVGL 9 for the UI and this API for
 * everything physical; the same source builds for the board (esp32s3) and
 * the emulator (linux). See lcd349_core.h for the contracts every call keeps.
 */
#pragma once

#include "lcd349_board.h"
#include "lcd349_core.h"
#include "lcd349_display.h"
#include "lcd349_input.h"
#include "lcd349_power.h"
#include "lcd349_audio.h"
#include "lcd349_console.h"
#include "lcd349_rtc.h"
#include "lcd349_kv.h"
#include "lcd349_wifi.h"
