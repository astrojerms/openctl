package bootstrap

import _ "embed"

//go:embed templates/systemd.service
var SystemdUnit []byte

//go:embed templates/openrc.init
var OpenRCInit []byte
