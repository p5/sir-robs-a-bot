def _repository_check_impl(ctx):
    command = cmd_args(ctx.attrs.command[RunInfo], hidden = ctx.attrs.resources)
    return [
        DefaultInfo(other_outputs = ctx.attrs.resources),
        RunInfo(args = command),
        ExternalRunnerTestInfo(
            command = [command],
            default_executor = CommandExecutorConfig(
                local_enabled = True,
                remote_cache_enabled = False,
                remote_enabled = False,
            ),
            env = {},
            run_from_project_root = True,
            # The checker reads Git state and untracked files in the checkout.
            supports_test_execution_caching = False,
            type = "custom",
            use_project_relative_paths = True,
        ),
    ]

repository_check = rule(
    attrs = {
        "command": attrs.exec_dep(providers = [RunInfo]),
        "resources": attrs.list(attrs.source(allow_directory = True), default = []),
    },
    impl = _repository_check_impl,
)
