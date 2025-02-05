package cozy

import (
	"embed"
)

const Version = "0.0.2"

//go:embed gmi/*.gmi
var FS embed.FS
