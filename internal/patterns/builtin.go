package patterns

import "embed"

//go:embed *.yaml
var builtinPatterns embed.FS
