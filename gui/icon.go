package main

import (
	_ "embed"
)

// appIconPNG is the application icon (window + non-Windows tray).
// Source file: gui/frontend/src/assets/app.png — drop the user's ready
// PNG there and rebuild; gui/app.ico for the exe is generated from the
// same file by script/png2ico.go (see build.bat step 4).
//
//go:embed frontend/src/assets/app.png
var appIconPNG []byte

// appIconICO is the multi-size Windows icon, also generated from app.png
// by script/png2ico.go. The tray uses it on Windows so the shell gets a
// native 16x16 entry instead of a downscaled 256px PNG.
//
//go:embed app.ico
var appIconICO []byte
