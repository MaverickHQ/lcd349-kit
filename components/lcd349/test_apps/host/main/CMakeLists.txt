idf_component_register(SRCS "test_main.c" "test_pure.c" "test_rotate.c" "test_touch.c" "test_kit.c" "test_cpp.cpp" "test_lint.c"
                       PRIV_INCLUDE_DIRS "../../../src/common" "../../../src/emu"
                       PRIV_REQUIRES lcd349 unity json)
