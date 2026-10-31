// Package lcd349kit holds what the lcd349 binary carries from the repository:
// the kit version and, embedded, the files it writes or builds from.
package lcd349kit

import "embed"

// Version is the kit version. It equals LCD349_VERSION_STR in
// components/lcd349/include/lcd349_core.h (internal/repocheck checks).
const Version = "0.2.0"

// RepoURL is the kit's repository, which `lcd349 new` pins. It must match
// plugin/scripts/lcd349-launch (internal/repocheck checks).
const RepoURL = "https://github.com/MaverickHQ/lcd349-kit.git"

// IDFVersion is the ESP-IDF release the kit is pinned to (ESP_IDF in
// VERSIONS; internal/repocheck checks).
const IDFVersion = "v5.5.5"

// ViewerSource is the viewer's source tree (viewer/), which lcd349 builds into
// its cache when no lcd349-view binary ships beside it (design §Instances).
//
//go:embed viewer/CMakeLists.txt viewer/*.c viewer/*.h
var ViewerSource embed.FS

// DeviceReference is the generated device facts and fidelity list the skill
// carries (internal/devicegen writes them), for the MCP tool device_info.
//
//go:embed plugin/skills/lcd349/references/device.md plugin/skills/lcd349/references/fidelity.md
var DeviceReference embed.FS

// AppTemplate is templates/app, which `lcd349 new` copies: files ending in
// .tmpl are Go templates, and gitignore becomes .gitignore.
//
//go:embed templates/app
var AppTemplate embed.FS
