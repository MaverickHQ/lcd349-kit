# Framework adapters

The kit supports one stack: **ESP-IDF with LVGL 9, in C.** Supporting Arduino, PlatformIO, ESPHome or MicroPython as well would multiply the build and test matrix (frameworks × backends × hosts) before one path works well, so other stacks wait for a reason.

## The decision gate

The kit was built for two consumers: herdr-pager (ESP-IDF and C) and a second project whose stack isn't decided yet. When it is, the kit adds **at most one adapter**, for that stack:

| The second project uses | The kit does |
|---|---|
| ESP-IDF with LVGL, in C | nothing: it's supported |
| Arduino or PlatformIO | a C++ wrapper header, an example using Arduino as an ESP-IDF component, and a PlatformIO example. A sketch that uses only the kit's API runs in the emulator; arbitrary Arduino libraries won't, and the docs will say so |
| ESPHome | a board package (YAML) for the board. ESPHome's own SDL host display covers desktop layout work; the kit's emulator isn't extended to run ESPHome |
| MicroPython, Rust or anything else | nothing in the kit. The device knowledge and design guide still apply |

## What keeps the door open

Three rules cost nothing now and keep a later adapter cheap:

- **The public headers stay framework-neutral:** they include only the C standard library, LVGL and each other (a repository check enforces it), so a C++ or Arduino wrapper is thin.
- **The control protocol, the MCP tools and the lint act on the device model,** not on the product's framework, so any product that runs in the emulator gets them.
- **The device knowledge is data:** `device.json`, the generated hardware page and the design guide serve any stack.

## Asking for another stack

Open an issue that names the project, its stack, and what it needs from the board. The adapter for the second project is planned (requirement 16); other stacks are out of scope unless a real consumer needs them.
