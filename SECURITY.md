# Security policy

## Reporting a vulnerability

Please don't open a public issue for a security problem. Report it privately instead: on the repository's [Security tab](https://github.com/MaverickHQ/lcd349-kit/security), choose **Report a vulnerability**. The report stays private between you and the maintainer until a fix is published.

It helps to include:

- what's affected: the CLI, the emulator, the MCP server, the plugin's launcher, the SDK on the board, or a release binary;
- `lcd349 version`;
- how to reproduce it, and what it lets someone do.

## Supported versions

Only the latest release is supported. Before 1.0, a fix ships in a new release rather than as a patch to an older one.

## What the kit trusts

[docs/security.md](docs/security.md) says what the kit runs on your computer, the files it creates, the MCP server's limits, and how to check a release binary.
