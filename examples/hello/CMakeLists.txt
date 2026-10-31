# hello: the smallest lcd349 product. One screen; every touch and button edge
# is logged and shown. Builds for both targets:
#   idf.py set-target esp32s3 build flash monitor     (the board)
#   lcd349 build                                      (the emulator, in build-emu/)
cmake_minimum_required(VERSION 3.16)
set(COMPONENTS main)
include($ENV{IDF_PATH}/tools/cmake/project.cmake)
idf_build_set_property(DEPENDENCIES_LOCK dependencies.lock.${IDF_TARGET})
project(hello)
