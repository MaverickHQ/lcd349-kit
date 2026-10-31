# lcd349-view: the emulator's window (C + SDL2). Built on its own, without
# ESP-IDF or the component:
#   cmake -S viewer -B viewer/build && cmake --build viewer/build
cmake_minimum_required(VERSION 3.16)
project(lcd349_view C)

# Keep in step with LCD349_VERSION_STR (internal/repocheck checks).
set(VIEW_VERSION "0.2.0" CACHE STRING "Version printed by lcd349-view --version")
option(VIEW_WERROR "Treat warnings as errors (CI)" OFF)

set(CMAKE_C_STANDARD 11)
set(CMAKE_C_STANDARD_REQUIRED ON)
if(NOT CMAKE_BUILD_TYPE)
  set(CMAKE_BUILD_TYPE Release)
endif()

find_package(SDL2 CONFIG QUIET)
if(TARGET SDL2::SDL2)
  set(VIEW_SDL SDL2::SDL2)
else()
  find_package(PkgConfig REQUIRED)
  pkg_check_modules(SDL2 REQUIRED IMPORTED_TARGET sdl2)
  set(VIEW_SDL PkgConfig::SDL2)
endif()

set(VIEW_WARNINGS -Wall -Wextra -Wshadow -Wno-unused-parameter)
if(VIEW_WERROR)
  list(APPEND VIEW_WARNINGS -Werror)
endif()

# The pure parts: no SDL, no I/O.
add_library(view_core STATIC core.c)
target_include_directories(view_core PUBLIC ${CMAKE_CURRENT_SOURCE_DIR})
target_compile_options(view_core PRIVATE ${VIEW_WARNINGS})
if(UNIX AND NOT APPLE)
  target_link_libraries(view_core PUBLIC m)
endif()

add_executable(lcd349-view view.c audio.c display_mm.c)
target_compile_options(lcd349-view PRIVATE ${VIEW_WARNINGS})
target_compile_definitions(lcd349-view PRIVATE VIEW_VERSION="${VIEW_VERSION}")
target_link_libraries(lcd349-view PRIVATE view_core ${VIEW_SDL})
if(APPLE)
  target_link_libraries(lcd349-view PRIVATE "-framework CoreGraphics")
endif()

# Unit tests, in the repository only: they check the inverse rotation against
# the kit's own rotate.c.
set(KIT_DIR ${CMAKE_CURRENT_SOURCE_DIR}/../components/lcd349)
if(EXISTS ${KIT_DIR}/src/common/rotate.c)
  add_executable(lcd349-view-tests test_core.c ${KIT_DIR}/src/common/rotate.c)
  target_include_directories(lcd349-view-tests PRIVATE ${KIT_DIR}/src/common ${KIT_DIR}/include)
  target_compile_options(lcd349-view-tests PRIVATE ${VIEW_WARNINGS})
  target_link_libraries(lcd349-view-tests PRIVATE view_core)
  enable_testing()
  add_test(NAME view_core COMMAND lcd349-view-tests)
endif()
