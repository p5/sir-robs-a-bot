# Repository instructions

Read `README.md`, `CONTEXT.md`, and `docs/repository.md` before you change this repository.
Read project instructions before you change a project.

- Organize projects by purpose. Do not choose a language before the task requires one.
- Keep implementation details inside the owning module.
- Keep dependency declarations in the selected build system's source of truth.
- Do not maintain two dependency graphs by hand.
- Add shared libraries only when there are explicit users.
- Add local instructions only when project rules differ from these rules.
- Keep agent workspaces, credentials, and execution artifacts outside the source tree.
- Run the checks that cover the change. Report checks that you cannot run.
- Do not treat an empty test suite as proof that behavior works.
- Write technical text with short sentences, active voice, and consistent terms.

Use the pinned `./buck2` launcher.
Run `./buck2 run //tooling:verify` for scaffold changes.
Run `./buck2 run //tooling:fmt` to format Buck files.
Run `./buck2 audit visibility //... toolchains//...` for build graph changes.
Keep repository checks local. They read Git metadata and untracked files.

For substantial tasks and handoffs, follow [agent work](docs/agent-work.md).

Buck2 is the build system. See `docs/build-system.md`.
