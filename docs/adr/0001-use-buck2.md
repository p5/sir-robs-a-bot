# Use Buck2 for the repository build graph

Use Buck2 for this multi-language agent software factory.
Its explicit dependency graph supports selective builds, dependency queries, and future remote execution.
Each language integration still needs pinned toolchains and generated dependency targets.

Use a dated DotSlash launcher and its bundled prelude to pin the executable and rules together.
Add language toolchains with their first projects.
This decision selects the build system.
Application modules, services, and implementation languages remain open.
