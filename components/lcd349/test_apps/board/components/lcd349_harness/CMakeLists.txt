idf_component_register(SRCS "lcd349_harness.c"
                       INCLUDE_DIRS "include"
                       REQUIRES unity
                       PRIV_REQUIRES lcd349 esp_driver_usb_serial_jtag vfs json heap esp_system)
# The harness keeps its own list of the registered tests, with each one's
# stages, so it can run a multi-stage test across a reboot.
target_link_libraries(${COMPONENT_LIB} INTERFACE "-Wl,--wrap=unity_testcase_register")
