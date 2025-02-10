package cozy

import (
	"embed"
)

const Version = "0.0.3"

//go:embed gmi/*.gmi
var FS embed.FS
