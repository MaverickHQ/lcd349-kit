# The board test app (design D1-6, seam S1-4): the kit's SDK on the board,
# run by tools/boardtest, which builds, flashes and drives it:
#   go run ./tools/boardtest [-only <subsystem>]
# sdkconfig.defaults sets the esp32s3 target, so a plain idf.py build works.
cmake_minimum_required(VERSION 3.16)
set(EXTRA_COMPONENT_DIRS "${CMAKE_CURRENT_LIST_DIR}/../..")
# Build only what main needs.
set(COMPONENTS main)
include($ENV{IDF_PATH}/tools/cmake/project.cmake)
idf_build_set_property(DEPENDENCIES_LOCK dependencies.lock.${IDF_TARGET})
project(lcd349_board_test)
