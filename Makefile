VERSION := 2.0.0
LDFLAGS := -s -w

.PHONY: build gui test windows installer release clean resources idm-icon

# command line program for the current system
build:
	go build -ldflags "$(LDFLAGS)" -o godm-cli ./cmd/godm

# Windows desktop app (GoDM.exe) and helper (godm-cli.exe), placed next to the installer script
windows:
	CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -ldflags "$(LDFLAGS) -H windowsgui" -o installer/GoDM.exe ./cmd/godm-gui
	CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -ldflags "$(LDFLAGS)" -o installer/godm-cli.exe ./cmd/godm

# needs NSIS (makensis); produces installer/GoDM-Setup-$(VERSION).exe
installer: windows
	cd installer && makensis godm.nsi

# Extract the real icon from a local Internet Download Manager installation.
# Set IDM_EXE=... when IDMan.exe is installed in a non-standard folder.
IDM_EXE ?=
idm-icon:
	powershell.exe -NoProfile -ExecutionPolicy Bypass -File scripts/extract-idm-icon.ps1 -IdmExePath "$(IDM_EXE)"

# regenerate the Windows icon + manifest resource for GoDM.exe (run idm-icon first)
resources:
	cd cmd/godm-gui && go run github.com/akavel/rsrc@v0.10.2 -manifest godm.manifest -ico ../../installer/godm.ico -arch amd64 -o rsrc_windows_amd64.syso

test:
	go test -race ./...

release:
	CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -ldflags "$(LDFLAGS) -H windowsgui" -o dist/GoDM-windows-amd64.exe ./cmd/godm-gui
	CGO_ENABLED=0 GOOS=windows GOARCH=amd64 go build -ldflags "$(LDFLAGS)" -o dist/godm-cli-windows-amd64.exe ./cmd/godm
	CGO_ENABLED=0 GOOS=windows GOARCH=arm64 go build -ldflags "$(LDFLAGS)" -o dist/godm-cli-windows-arm64.exe ./cmd/godm
	CGO_ENABLED=0 GOOS=darwin  GOARCH=arm64 go build -ldflags "$(LDFLAGS)" -o dist/godm-cli-macos-arm64 ./cmd/godm
	CGO_ENABLED=0 GOOS=darwin  GOARCH=amd64 go build -ldflags "$(LDFLAGS)" -o dist/godm-cli-macos-amd64 ./cmd/godm
	CGO_ENABLED=0 GOOS=linux   GOARCH=amd64 go build -ldflags "$(LDFLAGS)" -o dist/godm-cli-linux-amd64 ./cmd/godm
	CGO_ENABLED=0 GOOS=linux   GOARCH=arm64 go build -ldflags "$(LDFLAGS)" -o dist/godm-cli-linux-arm64 ./cmd/godm

clean:
	rm -rf dist godm-cli installer/GoDM.exe installer/godm-cli.exe
