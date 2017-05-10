load("@io_bazel_rules_go//go:def.bzl", "go_binary", "go_library", "go_prefix")

go_prefix("k8s.io/test-infra")

filegroup(
    name = "package-srcs",
    srcs = glob(
        ["**"],
        exclude = [
            "bazel-*/**",
            ".git/**",
            "*.db",
            "*.gz",
        ],
    ),
    visibility = ["//visibility:private"],
)

filegroup(
    name = "buckets",
    srcs = ["buckets.yaml"],
    visibility = ["//:__subpackages__"],
)

filegroup(
    name = "all-srcs",
    srcs = [
        ":package-srcs",
        "//boskos:all-srcs",
        "//experiment:all-srcs",
        "//gcsweb/cmd/gcsweb:all-srcs",
        "//gcsweb/pkg/version:all-srcs",
        "//images/pull_kubernetes_bazel:all-srcs",
        "//jenkins:all-srcs",
        "//jobs:all-srcs",
        "//kettle:all-srcs",
        "//kubetest:all-srcs",
        "//logexporter/cmd:all-srcs",
        "//mungegithub:all-srcs",
        "//prow:all-srcs",
        "//scenarios:all-srcs",
        "//testgrid/config:all-srcs",
        "//testgrid/jenkins_verify:all-srcs",
        "//triage:all-srcs",
        "//velodrome:all-srcs",
        "//vendor:all-srcs",
        "//verify:all-srcs",
    ],
    tags = ["automanaged"],
    visibility = ["//visibility:public"],
)

go_binary(
    name = "test-infra",
    library = ":go_default_library",
    tags = ["automanaged"],
)

go_library(
    name = "go_default_library",
    srcs = ["gcs.go"],
    tags = ["automanaged"],
    deps = [
        "//vendor:cloud.google.com/go/storage",
        "//vendor:google.golang.org/api/googleapi",
    ],
)
