package proxy

import (
	"fmt"

	"github.com/inebotov/aish/internal/config"
)

// routeFile is $AISH_RUN/route: the [route] table as init.bash reads it
// into __aish_route_* when the shell starts, a key=value per line.
func routeFile(r config.Route) []byte {
	return fmt.Appendf(nil, "capital=%t\nnot_found=%t\nsuffix=%s\nmin_words=%d\n",
		r.Capital, r.NotFound, r.Suffix, r.MinWords)
}
