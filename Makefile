.PHONY: build
build:
	CGO_ENABLED=0 installsuffix=cgo go build -ldflags "-extldflags '-static' -s"

.PHONY: image
image:
	docker build .