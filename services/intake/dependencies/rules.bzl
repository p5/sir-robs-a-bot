def dependency_library(**kwargs):
    kwargs["visibility"] = ["//services/intake/..."]
    kwargs["override_cgo_enabled"] = False
    native.go_library(**kwargs)
