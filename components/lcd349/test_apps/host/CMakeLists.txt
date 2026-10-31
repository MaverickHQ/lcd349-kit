# Host tests for the lcd349 component: build for the linux target and run.
#   idf.py --preview build && ./build/lcd349_host_test.elf
# sdkconfig.defaults sets the target. `set-target linux` wipes the build, so run it
# only after editing sdkconfig.defaults (the existing sdkconfig keeps old values).
cmake_minimum_required(VERSION 3.16)
set(EXTRA_COMPONENT_DIRS "${CMAKE_CURRENT_LIST_DIR}/../..")
set(COMPONENTS main)
include($ENV{IDF_PATH}/tools/cmake/project.cmake)
idf_build_set_property(DEPENDENCIES_LOCK dependencies.lock.${IDF_TARGET})
project(lcd349_host_test)
