package version

var (
	// Version 应该在构建时通过 -ldflags 注入
	// go build -ldflags "-X 'github.com/cylonchau/kube-res-foresight/pkg/version.Version=$(git describe --tags)'"
	Version = "v0.0.0-dev"
)
