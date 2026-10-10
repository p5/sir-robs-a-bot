load("//tooling/rules:repository_check.bzl", "repository_check")

filegroup(
    name = "files",
    srcs = glob([
        ".agents/skills/**",
        ".buckconfig",
        ".buckroot",
        ".editorconfig",
        ".github/**",
        ".gitignore",
        ".gitattributes",
        ".starlark-format.json",
        "*.md",
        "BUCK",
        "buck2",
        "go.work",
        "go.work.sum",
        "docs/**",
    ]),
    visibility = ["//tooling/tests:repository-tools-test"],
)

filegroup(
    name = "scaffold",
    srcs = {
        "repository": ":files",
        "toolchains": "toolchains//:files",
        "tooling": "//tooling:files",
    },
    visibility = ["PUBLIC"],
)

repository_check(
    name = "check",
    command = "//tooling/checks:repository",
    resources = [":scaffold"],
    visibility = ["PUBLIC"],
)
