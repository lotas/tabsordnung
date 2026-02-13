package analyzer

import (
	"net/url"
	"sort"
	"strings"

	"github.com/lotas/tabsordnung/internal/types"
)

func NormalizeURL(rawURL string) string {
	u, err := url.Parse(rawURL)
	if err != nil {
		return rawURL
	}
	u.Fragment = ""
	params := u.Query()
	for k := range params {
		sort.Strings(params[k])
	}
	u.RawQuery = params.Encode()
	result := u.String()
	if strings.HasSuffix(result, "/") && result != u.Scheme+"://"+u.Host+"/" {
		result = strings.TrimRight(result, "/")
	}
	return result
}

func AnalyzeDuplicates(tabs []*types.Tab) {
	groups := make(map[string][]int)
	for i, tab := range tabs {
		normalized := NormalizeURL(tab.URL)
		groups[normalized] = append(groups[normalized], i)
	}
	for _, indices := range groups {
		if len(indices) < 2 {
			continue
		}
		for _, i := range indices {
			tabs[i].IsDuplicate = true
			var others []int
			for _, j := range indices {
				if j != i {
					others = append(others, j)
				}
			}
			tabs[i].DuplicateOf = others
		}
	}
}
