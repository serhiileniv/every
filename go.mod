module github.com/serhiileniv/every

// A conservative floor, not the toolchain this happens to be developed on.
// `go mod init` writes whatever is installed locally, which pinned 1.27 and
// meant the pinned CI container and every slightly older machine refused to
// build it. Nothing here needs a recent language feature.
go 1.22
