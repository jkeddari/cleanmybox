package assets

import "embed"

//go:embed css/* js/* fonts/* img/*
var AssetsFS embed.FS
