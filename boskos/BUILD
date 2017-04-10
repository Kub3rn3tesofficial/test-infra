package(default_visibility = ["//visibility:public"])

licenses(["notice"])

load(
    "@io_bazel_rules_go//go:def.bzl",
    "go_binary",
    "go_library",
    "go_test",
)

go_binary(
    name = "boskos",
    library = ":go_default_library",
    tags = ["automanaged"],
)

go_test(
    name = "go_default_test",
    srcs = ["boskos_test.go"],
    data = ["resources.json"],
    library = ":go_default_library",
    tags = ["automanaged"],
    deps = [
        "//boskos/common:go_default_library",
        "//boskos/ranch:go_default_library",
    ],
)

go_library(
    name = "go_default_library",
    srcs = ["boskos.go"],
    tags = ["automanaged"],
    deps = [
        "//boskos/ranch:go_default_library",
        "//vendor:github.com/Sirupsen/logrus",
    ],
)

filegroup(
    name = "package-srcs",
    srcs = glob(["**"]),
    tags = ["automanaged"],
    visibility = ["//visibility:private"],
)

filegroup(
    name = "all-srcs",
    srcs = [
        ":package-srcs",
        "//boskos/common:all-srcs",
        "//boskos/ranch:all-srcs",
    ],
    tags = ["automanaged"],
)
