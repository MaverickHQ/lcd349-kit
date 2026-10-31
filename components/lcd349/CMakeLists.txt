# lcd349: board SDK for the Waveshare ESP32-S3-Touch-LCD-3.49.
# The shared core is in src/common. The backend is chosen by target:
# src/hw for the board (esp32s3), src/emu for the emulator (ESP-IDF linux target).
idf_build_get_property(target IDF_TARGET)

set(srcs
    "src/common/axs_touch.c"
    "src/common/battery_curve.c"
    "src/common/board_info.c"
    "src/common/classifier.c"
    "src/common/core.c"
    "src/common/debounce.c"
    "src/common/evt.c"
    "src/common/lvgl_port.c"
    "src/common/rotate.c"
    "src/common/tags.c")

if(target STREQUAL "linux")
    list(APPEND srcs
        "src/emu/control.c"
        "src/emu/control_methods.c"
        "src/emu/devmodel.c"
        "src/emu/download.c"
        "src/emu/emu_backend.c"
        "src/emu/fb.c"
        "src/emu/introspect.c"
        "src/emu/kv_file.c"
        "src/emu/lint.c"
        "src/emu/png.c"
        "src/emu/serial.c"
        "src/emu/wifi_sim.c")
    set(backend_dir "src/emu")
    set(backend_requires json)
elseif(target STREQUAL "esp32s3")
    # One file per subsystem, so each lane of phase 1 fills only its own.
    list(APPEND srcs
        "src/hw/audio.c"
        "src/hw/battery.c"
        "src/hw/buttons.c"
        "src/hw/console.c"
        "src/hw/display.c"
        "src/hw/expander.c"
        "src/hw/hw_backend.c"
        "src/hw/kv.c"
        "src/hw/power.c"
        "src/hw/rtc.c"
        "src/hw/sleep.c"
        "src/hw/touch.c"
        "src/hw/wifi.c")
    set(backend_dir "src/hw")
    # Everything the subsystems use, listed once here so no lane edits this file.
    set(backend_requires
        esp_timer esp_system esp_psram
        esp_driver_i2c esp_driver_gpio esp_driver_ledc esp_driver_spi esp_lcd # power, buttons, display, touch
        esp_adc                                                                # battery
        esp_driver_i2s                                                         # audio (with espressif/esp_codec_dev)
        nvs_flash                                                              # storage
        esp_driver_usb_serial_jtag vfs                                         # console
        esp_wifi esp_netif esp_event esp_pm)                                   # Wi-Fi and light sleep
else()
    message(FATAL_ERROR "lcd349 supports esp32s3 (the board) and linux (the emulator), not ${target}")
endif()

idf_component_register(SRCS ${srcs}
                       INCLUDE_DIRS "include"
                       PRIV_INCLUDE_DIRS "src/common" ${backend_dir}
                       REQUIRES freertos
                       PRIV_REQUIRES log heap ${backend_requires})
