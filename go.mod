module github.com/p2psearch/p2psearch

go 1.26.0

require (
	github.com/goed2k/core v0.1.3
	golang.org/x/net v0.57.0
	gopkg.in/yaml.v3 v3.0.1
)

replace github.com/goed2k/core => ./third_party/goed2k

require (
	golang.org/x/sys v0.48.0 // indirect
	golang.org/x/text v0.40.0 // indirect
)
