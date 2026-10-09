# Build toolchains

This cell contains the toolchains that Buck2 build rules require.
The scaffold's repository checks use Bash, Git, and DotSlash from the host.
Language toolchains and remote execution configuration will arrive with concrete build targets.

Add a language toolchain with the first project that needs it.
Pin its tools and distributions.
Declare compiler, runtime, linker, and bootstrap tools as build inputs where the rules support this.

Use downloaded distributions for reproducible builds.
Do not copy the complete demo toolchain set into this cell.
It assumes tools from the host and configures languages that this repository does not yet use.

Follow the [build integration guide](../docs/build-integration.md) when adding language toolchains.

Bundled test rules can require a Python bootstrap toolchain.
If a rule needs Python, use a virtual environment with `uv` and pin the distribution.
