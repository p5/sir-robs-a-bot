def dependency_library(**kwargs):
    kwargs["visibility"] = ["//packages/reconcile/..."]
    kwargs["override_cgo_enabled"] = False
    # gobuckify omits go:embed assets. Keep this package's upstream schema
    # defaults in the compile inputs without editing its generated target.
    if kwargs.get("package_name") == "google.golang.org/protobuf/internal/editiondefaults":
        kwargs["embed_srcs"] = ["editions_defaults.binpb"]
    native.go_library(**kwargs)
