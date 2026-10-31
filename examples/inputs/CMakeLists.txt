# inputs: shows and logs every input and state the board offers (touch samples,
# button gestures, battery, Wi-Fi, power). Builds for both targets:
#   idf.py set-target esp32s3 build flash monitor
#   lcd349 build            (emulator: build-emu/)
cmake_minimum_required(VERSION 3.16)
set(COMPONENTS main)
include($ENV{IDF_PATH}/tools/cmake/project.cmake)
idf_build_set_property(DEPENDENCIES_LOCK dependencies.lock.${IDF_TARGET})
project(inputs)
