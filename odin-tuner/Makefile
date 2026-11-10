BINARY := bin/odin-tuner
MODULE_ZIP := ../odin-tuner-v0.4.0.zip

.PHONY: build package clean

build:
	cd src && CGO_ENABLED=0 GOOS=linux GOARCH=arm64 go build -trimpath -ldflags="-s -w" -o ../$(BINARY) .
	chmod 755 $(BINARY) bin/start-web.sh bin/stop-web.sh bin/restart-web.sh service.sh uninstall.sh action.sh customize.sh

package: build
	rm -f $(MODULE_ZIP)
	cd . && zip -qr9 $(abspath $(MODULE_ZIP)) .

clean:
	rm -f $(BINARY) $(MODULE_ZIP)
