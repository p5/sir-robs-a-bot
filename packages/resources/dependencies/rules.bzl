def dependency_library(**kwargs):
    kwargs["visibility"] = ["//packages/resources/...", "//services/intake/..."]
    kwargs["override_cgo_enabled"] = False
    native.go_library(**kwargs)
