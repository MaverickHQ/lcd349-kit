# Compile check: a minimal product built for both targets in CI.
#   idf.py set-target esp32s3 build
#   idf.py -B build-emu -D SDKCONFIG=build-emu/sdkconfig --preview set-target linux build
cmake_minimum_required(VERSION 3.16)
set(EXTRA_COMPONENT_DIRS "${CMAKE_CURRENT_LIST_DIR}/../..")
# Build only what main needs (the linux target otherwise builds all of ESP-IDF).
set(COMPONENTS main)
include($ENV{IDF_PATH}/tools/cmake/project.cmake)
idf_build_set_property(DEPENDENCIES_LOCK dependencies.lock.${IDF_TARGET})
project(lcd349_build_check)
