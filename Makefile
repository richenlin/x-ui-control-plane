IMAGE      ?= x-ui:hardened
XRAY_VERSION ?= 26.3.27

.PHONY: help vet build image release clean

help:
	@echo "make vet       - go vet"
	@echo "make image     - build docker image ($(IMAGE))"
	@echo "make release   - build offline migration bundle (dist/)"
	@echo "make clean     - remove dist/"

vet:
	docker run --rm -v $$PWD:/src -v xui-gomod:/go/pkg/mod -w /src \
	  golang:1.21-bookworm bash -lc 'export PATH=$$PATH:/usr/local/go/bin GOFLAGS=-mod=mod; go vet ./...'

image:
	docker build --network host -t $(IMAGE) .

release: image
	@mkdir -p dist
	docker save $(IMAGE) | gzip -1 > dist/x-ui-image.tar.gz
	tar czf dist/x-ui-src.tar.gz --exclude='./dist' --exclude='./.git' .
	cp install.sh compose.yaml README.md dist/
	@cd dist && sha256sum * > SHA256SUMS && ls -lh

clean:
	rm -rf dist
