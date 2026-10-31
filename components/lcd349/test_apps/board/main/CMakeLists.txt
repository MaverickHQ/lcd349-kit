# Every test_*.c here, so a lane adds a test file without editing this list
# (CONFIGURE_DEPENDS: an existing build picks a new file up too). ESP-IDF's
# first pass over this file only collects requirements, in script mode,
# where CONFIGURE_DEPENDS isn't allowed.
# Tests register themselves (TEST_CASE with a subsystem tag), so the archive
# is linked whole. The kit's private headers are visible for test hooks.
if(CMAKE_BUILD_EARLY_EXPANSION)
    set(tests "")
else()
    file(GLOB tests CONFIGURE_DEPENDS RELATIVE ${CMAKE_CURRENT_LIST_DIR} "test_*.c")
endif()
idf_component_register(SRCS "app_main.c" ${tests}
                       PRIV_INCLUDE_DIRS "../../../src/common" "../../../src/hw"
                       PRIV_REQUIRES lcd349 lcd349_harness unity
                                     esp_timer esp_driver_i2c esp_driver_gpio esp_adc nvs_flash
                                     esp_wifi esp_netif esp_event esp_pm json
                       WHOLE_ARCHIVE)
